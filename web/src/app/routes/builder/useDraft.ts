/**
 * Builder state: the draft schema being edited, plus the calls that persist it.
 *
 * The draft is the one place in this app where a local buffer is right. Every
 * other screen reads and writes whole records, but here a person types a label
 * one character at a time, and round-tripping each keystroke through the server
 * would make the field they are editing jump under the cursor. So: the query
 * owns the loaded draft, a local buffer owns the edit in progress, and a
 * debounced PUT reconciles them.
 *
 * Three invariants from docs/06-deep-dives.md govern every operation below:
 *
 *   1. field_id / option_id are stable ULIDs, never reused. Editing a label
 *      keeps the id; deleting and re-adding a question mints a new one, because
 *      answers already collected point at the old id and must not be silently
 *      re-attached to a different question.
 *   2. Answers are stored by id, never by label.
 *   3. A published version is immutable. Nothing here edits the live version --
 *      every operation writes the draft only.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type List } from '../../lib/api'
import type {
  Action,
  Condition,
  Field,
  FieldID,
  Page,
  PageID,
  Purpose,
  Rule,
  Schema,
} from '../../../shared/engine'

/* ------------------------------------------------------------------ types */

/** The consent block the server stores, which the shared engine type does not
 *  describe because the runtime engine never reads it. */
export interface ConsentBlock {
  purposes?: Purpose[]
  sensitive_notice_required?: boolean
}

export type DraftSchema = Schema & { consent?: ConsentBlock }

export interface FormDetail {
  id: string
  project_id: string
  public_id: string
  title: string
  status: string
  live_version_id: string | null
  retention_days: number | null
  retention_action: string
  created_at: string
  /** The working copy. Absent when the form has never been edited since it was
   *  published; has_draft says which, because an empty draft and a missing one
   *  are different and saving over the wrong one deletes every question. */
  draft_schema?: DraftSchema | null
  has_draft?: boolean
  /** The published schema, used as the starting point when no draft exists. */
  live_schema?: DraftSchema | null
  live_version_no?: number
}

/** The schema the builder should open.
 *
 * The draft when one exists, otherwise a copy of the live version -- which is
 * what "dựa trên vN" in the header has always claimed. Returning nothing for a
 * published-and-untouched form left the builder explaining an API gap that had
 * since been filled, because it was reading a field name that never shipped.
 */
export function openingSchema(f: FormDetail | undefined): DraftSchema | null {
  if (!f) return null
  if (f.has_draft && f.draft_schema) return f.draft_schema
  return f.live_schema ?? null
}

export interface VersionRow {
  id: string
  version_no: number
  published_at: string
  retired_at: string | null
  field_count: number
  rule_count: number
}

export interface Issue {
  code: string
  severity: string
  target?: string
  message: string
}

export interface ValidationResult {
  ok: boolean
  issues?: Issue[] | null
}

export interface Change {
  kind: string
  class: string
  target: string
  message: string
}

export interface DiffResult {
  changes?: Change[] | null
  blocked: boolean
  breaking: boolean
}

/** What POST /draft/validate answers: what publishing would do, without doing it. */
export interface PublishPreview {
  validation: ValidationResult
  diff: DiffResult
}

export interface PublishedVersion {
  version_id: string
  version_no: number
  published_at: string
}

export const SEVERITY_ERROR = 'error'
export const CLASS_BREAKING = 'breaking'
export const CLASS_BLOCKED = 'blocked'

/* --------------------------------------------------------------- id minting */

const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'

let lastMs = -1
let lastRand: number[] = []

function randomIndices(n: number): number[] {
  const out = new Array<number>(n)
  const bytes = new Uint8Array(n)
  const c: Crypto | undefined = globalThis.crypto
  if (c && typeof c.getRandomValues === 'function') c.getRandomValues(bytes)
  else for (let i = 0; i < n; i++) bytes[i] = Math.floor(Math.random() * 256)
  for (let i = 0; i < n; i++) out[i] = (bytes[i] ?? 0) % 32
  return out
}

function encodeTime(ms: number): string {
  let t = ms
  let s = ''
  for (let i = 0; i < 10; i++) {
    s = CROCKFORD.charAt(t % 32) + s
    t = Math.floor(t / 32)
  }
  return s
}

/**
 * A ULID, minted in the browser.
 *
 * Ids are minted client-side because a question must be addressable the instant
 * it appears on screen -- rules point at it, and waiting for a server round trip
 * would mean the panel could not bind a condition to a field the user just
 * dropped. Randomness is incremented rather than redrawn inside one millisecond
 * so ids created in the same tick still sort in creation order.
 */
export function ulid(now: number = Date.now()): string {
  if (now === lastMs) {
    for (let i = lastRand.length - 1; i >= 0; i--) {
      const v = (lastRand[i] ?? 0) + 1
      if (v < 32) {
        lastRand[i] = v
        break
      }
      lastRand[i] = 0
    }
  } else {
    lastMs = now
    lastRand = randomIndices(16)
  }
  let s = encodeTime(now)
  for (const i of lastRand) s += CROCKFORD.charAt(i)
  return s
}

export const newFieldId = (): FieldID => `fld_${ulid()}`
export const newOptionId = (): string => `opt_${ulid()}`
export const newPageId = (): PageID => `pg_${ulid()}`
export const newRuleId = (): string => `rl_${ulid()}`

/* ------------------------------------------------------------ field catalogue */

export interface FieldTypeDef {
  type: string
  label: string
  /** One line explaining what the respondent sees, for the palette tooltip. */
  hint: string
}

/** The seven types the server's domain package accepts, in wireframe order. */
export const FIELD_TYPES: FieldTypeDef[] = [
  { type: 'text', label: 'Văn bản', hint: 'Một dòng hoặc nhiều dòng tự do' },
  { type: 'choice', label: 'Chọn một', hint: 'Nút chọn, chỉ một đáp án' },
  { type: 'multi_choice', label: 'Chọn nhiều', hint: 'Hộp kiểm, nhiều đáp án' },
  { type: 'rating', label: 'Đánh giá', hint: 'Thang điểm 2–10' },
  { type: 'date', label: 'Ngày', hint: 'Ngày theo lịch' },
  { type: 'dropdown', label: 'Thả xuống', hint: 'Danh sách dài, chọn một' },
  { type: 'file', label: 'Tải tệp', hint: 'Đính kèm tệp, kiểm bằng magic bytes' },
]

export const TYPE_LABEL: Record<string, string> = Object.fromEntries(
  FIELD_TYPES.map((t) => [t.type, t.label]),
)

export function isChoice(type: string | undefined): boolean {
  return type === 'choice' || type === 'multi_choice' || type === 'dropdown'
}

/**
 * A new question of the given type.
 *
 * Only keys the server's schema declares are emitted: PUT /draft decodes with
 * DisallowUnknownFields, so an extra key does not get ignored, it fails the
 * whole save with a 400.
 */
export function blankField(type: string): Field {
  const f: Field = { type, label: '' }
  if (isChoice(type)) {
    f.options = [
      { id: newOptionId(), label: 'Lựa chọn 1' },
      { id: newOptionId(), label: 'Lựa chọn 2' },
    ]
  }
  if (type === 'rating') f.scale = 5
  return f
}

/* ------------------------------------------------------------- normalisation */

/**
 * Fills in what the server may send as null.
 *
 * Go marshals a nil slice or map as `null`, so `schema.pages.map(...)` on a form
 * whose draft has never been touched throws before anything renders. Unknown
 * keys are preserved by spreading: the server's schema carries fields this
 * client does not model (`limits`, `identifier`, `min`/`max`), and rebuilding
 * the object from scratch would silently drop them on the next save.
 */
export function normalizeSchema(raw: DraftSchema | null | undefined): DraftSchema {
  return {
    ...raw,
    v: raw?.v ?? 1,
    pages: (raw?.pages ?? []).map((p) => ({ ...p, fields: p.fields ?? [] })),
    fields: raw?.fields ?? {},
    rules: raw?.rules ?? [],
    consent: {
      ...raw?.consent,
      purposes: raw?.consent?.purposes ?? [],
      sensitive_notice_required: raw?.consent?.sensitive_notice_required ?? false,
    },
  }
}

/* ------------------------------------------------------------------ queries */

export function useForm(formId: string | undefined) {
  return useQuery({
    queryKey: ['form', formId],
    queryFn: async () => await api.get<FormDetail>(`/api/v1/forms/${formId}`),
    enabled: Boolean(formId),
  })
}

export function useVersions(formId: string | undefined) {
  return useQuery({
    queryKey: ['form-versions', formId],
    queryFn: async () =>
      (await api.get<List<VersionRow>>(`/api/v1/forms/${formId}/versions`)).data,
    enabled: Boolean(formId),
  })
}

/**
 * The publish preview.
 *
 * A POST behind useQuery on purpose: the endpoint is named "validate" and
 * changes nothing, it only reports what publishing would do. Modelling it as a
 * mutation would mean the publish screen showed nothing until the reader
 * pressed something first.
 */
export function usePublishPreview(formId: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ['publish-preview', formId],
    queryFn: async () =>
      await api.post<PublishPreview>(`/api/v1/forms/${formId}/draft/validate`),
    enabled: Boolean(formId) && enabled,
    staleTime: 0,
    retry: false,
  })
}

export function usePublish(formId: string | undefined) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () =>
      await api.post<PublishedVersion>(`/api/v1/forms/${formId}/draft/publish`),
    onSettled: () => {
      // Refetched on failure too: a 422 from publish carries the issue list in a
      // body shape lib/api.ts cannot surface, so the preview is re-read to say
      // exactly what blocked it.
      void qc.invalidateQueries({ queryKey: ['publish-preview', formId] })
      void qc.invalidateQueries({ queryKey: ['form-versions', formId] })
      void qc.invalidateQueries({ queryKey: ['form', formId] })
      void qc.invalidateQueries({ queryKey: ['forms'] })
    },
  })
}

/* ------------------------------------------------------------------ editor */

export interface DraftEditor {
  schema: DraftSchema | null
  dirty: boolean
  saving: boolean
  savedAt: number | null
  saveError: unknown
  /** What the last save said about the draft. Advisory: publish is the gate. */
  advisory: ValidationResult | null
  apply: (fn: (s: DraftSchema) => DraftSchema) => void
  saveNow: () => void
}

const AUTOSAVE_MS = 1200

export function useDraftEditor(
  formId: string | undefined,
  remote: DraftSchema | null | undefined,
  writable: boolean,
): DraftEditor {
  const qc = useQueryClient()
  const [schema, setSchema] = useState<DraftSchema | null>(null)
  const [dirty, setDirty] = useState(false)
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const seeded = useRef<DraftSchema | null | undefined>(undefined)

  const save = useMutation({
    mutationFn: async (next: DraftSchema) =>
      await api.put<ValidationResult>(`/api/v1/forms/${formId}/draft`, next),
    onSuccess: () => {
      setDirty(false)
      setSavedAt(Date.now())
      void qc.invalidateQueries({ queryKey: ['publish-preview', formId] })
    },
  })

  // Adopt the server copy once, and again only when the editor has nothing
  // unsaved: a background refetch must never overwrite what someone is typing.
  useEffect(() => {
    if (remote === undefined || seeded.current === remote) return
    if (dirty) return
    seeded.current = remote
    setSchema(normalizeSchema(remote))
  }, [remote, dirty])

  const saveRef = useRef(save)
  saveRef.current = save

  useEffect(() => {
    if (!dirty || !schema || !formId || !writable) return
    const t = setTimeout(() => saveRef.current.mutate(schema), AUTOSAVE_MS)
    return () => clearTimeout(t)
  }, [dirty, schema, formId, writable])

  const apply = useCallback((fn: (s: DraftSchema) => DraftSchema) => {
    setSchema((prev) => (prev ? fn(prev) : prev))
    setDirty(true)
  }, [])

  const saveNow = useCallback(() => {
    if (schema && formId && writable) saveRef.current.mutate(schema)
  }, [schema, formId, writable])

  return {
    schema,
    dirty,
    saving: save.isPending,
    savedAt,
    saveError: save.error,
    advisory: save.data ?? null,
    apply,
    saveNow,
  }
}

/* -------------------------------------------------------- schema operations */

function withPages(s: DraftSchema, pages: Page[]): DraftSchema {
  return { ...s, pages }
}

export function pageIndex(s: DraftSchema, id: PageID): number {
  return s.pages.findIndex((p) => p.id === id)
}

export function pageLabel(s: DraftSchema, id: PageID): string {
  const i = pageIndex(s, id)
  if (i < 0) return id
  const p = s.pages[i]
  return `trang ${i + 1} · ${p?.title || 'chưa đặt tên'}`
}

export function fieldLabel(s: DraftSchema, id: FieldID): string {
  return s.fields[id]?.label || id
}

export function optionLabel(s: DraftSchema, fieldId: FieldID, optionId: string): string {
  const o = s.fields[fieldId]?.options?.find((x) => x.id === optionId)
  return o?.label || optionId
}

export function pageOfField(s: DraftSchema, id: FieldID): PageID | null {
  for (const p of s.pages) if (p.fields.includes(id)) return p.id
  return null
}

export function addPage(s: DraftSchema, title?: string): { schema: DraftSchema; pageId: PageID } {
  const pageId = newPageId()
  const page: Page = { id: pageId, title: title ?? `Trang ${s.pages.length + 1}`, fields: [] }
  return { schema: withPages(s, [...s.pages, page]), pageId }
}

export function updatePage(s: DraftSchema, id: PageID, patch: Partial<Page>): DraftSchema {
  return withPages(
    s,
    s.pages.map((p) => (p.id === id ? { ...p, ...patch } : p)),
  )
}

/**
 * Deletes a page with its questions, the rules that ran on it, and every jump
 * that pointed at it.
 *
 * Leaving the dangling references behind would be more honest but less useful:
 * validation would block the publish with errors about rules the person can no
 * longer see, since the page holding them is gone.
 */
export function removePage(s: DraftSchema, id: PageID): DraftSchema {
  const page = s.pages.find((p) => p.id === id)
  const doomed = new Set(page?.fields ?? [])
  const fields: Record<FieldID, Field> = {}
  for (const [fid, f] of Object.entries(s.fields)) if (!doomed.has(fid)) fields[fid] = f

  const rules = (s.rules ?? [])
    .filter((r) => r.on_page !== id && !doomed.has(r.when.field))
    .map((r) => ({
      ...r,
      then: (r.then ?? []).filter((a) => !targets(a, doomed, id)),
      else: (r.else ?? []).filter((a) => !targets(a, doomed, id)),
    }))

  const pages = s.pages
    .filter((p) => p.id !== id)
    // A `next` left pointing at the deleted page is a dangling reference that
    // blocks the publish with an error about a page nobody can still see.
    .map((p) => (p.next === id ? { ...p, next: undefined } : p))

  return { ...s, pages, fields, rules }
}

function targets(a: Action, fields: Set<string>, pageId: PageID): boolean {
  if (a.action === 'goto') return a.target === pageId
  return fields.has(a.target)
}

export function addField(
  s: DraftSchema,
  pageId: PageID,
  type: string,
  at?: number,
): { schema: DraftSchema; fieldId: FieldID } {
  const fieldId = newFieldId()
  const pages = s.pages.map((p) => {
    if (p.id !== pageId) return p
    const next = [...p.fields]
    next.splice(at ?? next.length, 0, fieldId)
    return { ...p, fields: next }
  })
  return {
    schema: { ...s, pages, fields: { ...s.fields, [fieldId]: blankField(type) } },
    fieldId,
  }
}

/** Edits a question in place. The id is never part of the patch: a relabelled
 *  question is the same question, and answers already stored point at this id. */
export function updateField(s: DraftSchema, id: FieldID, patch: Partial<Field>): DraftSchema {
  const current = s.fields[id]
  if (!current) return s
  return { ...s, fields: { ...s.fields, [id]: { ...current, ...patch } } }
}

/** Every rule that reads this field or acts on it. Shown on the field card so
 *  nobody deletes a question that three branches depend on without knowing. */
export function rulesForField(s: DraftSchema, id: FieldID): Rule[] {
  return (s.rules ?? []).filter(
    (r) =>
      r.when.field === id ||
      [...(r.then ?? []), ...(r.else ?? [])].some((a) => a.action !== 'goto' && a.target === id),
  )
}

export function removeField(s: DraftSchema, id: FieldID): DraftSchema {
  const fields: Record<FieldID, Field> = {}
  for (const [fid, f] of Object.entries(s.fields)) if (fid !== id) fields[fid] = f

  const rules = (s.rules ?? [])
    .filter((r) => r.when.field !== id)
    .map((r) => ({
      ...r,
      then: (r.then ?? []).filter((a) => a.action === 'goto' || a.target !== id),
      else: (r.else ?? []).filter((a) => a.action === 'goto' || a.target !== id),
    }))

  return {
    ...s,
    fields,
    rules,
    pages: s.pages.map((p) => ({ ...p, fields: p.fields.filter((f) => f !== id) })),
  }
}

/** Moves a question, possibly to another page. Order is layout; it does not
 *  change navigation, which is a property of the page. */
export function moveField(
  s: DraftSchema,
  id: FieldID,
  toPageId: PageID,
  toIndex: number,
): DraftSchema {
  const from = pageOfField(s, id)
  if (!from) return s

  const stripped = s.pages.map((p) => ({ ...p, fields: p.fields.filter((f) => f !== id) }))
  const pages = stripped.map((p) => {
    if (p.id !== toPageId) return p
    const next = [...p.fields]
    next.splice(Math.max(0, Math.min(toIndex, next.length)), 0, id)
    return { ...p, fields: next }
  })
  return withPages(s, pages)
}

export function movePage(s: DraftSchema, id: PageID, delta: number): DraftSchema {
  const i = pageIndex(s, id)
  const j = i + delta
  if (i < 0 || j < 0 || j >= s.pages.length) return s
  const pages = [...s.pages]
  const a = pages[i]
  const b = pages[j]
  if (!a || !b) return s
  pages[i] = b
  pages[j] = a
  return withPages(s, pages)
}

export function addOption(
  s: DraftSchema,
  fieldId: FieldID,
): { schema: DraftSchema; optionId: string } {
  const f = s.fields[fieldId]
  const optionId = newOptionId()
  if (!f) return { schema: s, optionId }
  const options = [...(f.options ?? []), { id: optionId, label: `Lựa chọn ${(f.options?.length ?? 0) + 1}` }]
  return { schema: updateField(s, fieldId, { options }), optionId }
}

export function updateOption(
  s: DraftSchema,
  fieldId: FieldID,
  optionId: string,
  label: string,
): DraftSchema {
  const f = s.fields[fieldId]
  if (!f?.options) return s
  return updateField(s, fieldId, {
    options: f.options.map((o) => (o.id === optionId ? { ...o, label } : o)),
  })
}

/** Rules that compare against this option. Removing it while one exists is a
 *  blocked publish, not a warning, so the panel says so before the click. */
export function rulesUsingOption(s: DraftSchema, fieldId: FieldID, optionId: string): Rule[] {
  return (s.rules ?? []).filter((r) => {
    if (r.when.field !== fieldId) return false
    const v = r.when.value
    if (Array.isArray(v)) return v.some((x) => String(x) === optionId)
    return String(v ?? '') === optionId
  })
}

export function removeOption(s: DraftSchema, fieldId: FieldID, optionId: string): DraftSchema {
  const f = s.fields[fieldId]
  if (!f?.options) return s
  return updateField(s, fieldId, { options: f.options.filter((o) => o.id !== optionId) })
}

export function addRule(
  s: DraftSchema,
  fieldId: FieldID,
): { schema: DraftSchema; ruleId: string } {
  const ruleId = newRuleId()
  const onPage = pageOfField(s, fieldId)
  if (!onPage) return { schema: s, ruleId }
  const f = s.fields[fieldId]
  const firstOption = f?.options?.[0]?.id
  const rule: Rule = {
    id: ruleId,
    on_page: onPage,
    when: { op: 'eq', field: fieldId, value: firstOption ?? '' },
    then: [],
  }
  return { schema: { ...s, rules: [...(s.rules ?? []), rule] }, ruleId }
}

export function updateRule(s: DraftSchema, ruleId: string, patch: Partial<Rule>): DraftSchema {
  return {
    ...s,
    rules: (s.rules ?? []).map((r) => (r.id === ruleId ? { ...r, ...patch } : r)),
  }
}

export function removeRule(s: DraftSchema, ruleId: string): DraftSchema {
  return { ...s, rules: (s.rules ?? []).filter((r) => r.id !== ruleId) }
}

/**
 * Actions on this rule that quietly do nothing.
 *
 * evaluate() applies show/hide/require/optional against the fields of the page
 * the rule runs on, so an action aimed at a question on another page is dropped
 * on the floor. Nothing rejects it -- not the server's Validate, not the type
 * system -- which makes it the kind of rule that looks correct in the builder
 * and silently fails to reveal a question for every respondent.
 */
export function inertActions(s: DraftSchema, rule: Rule): Action[] {
  const page = s.pages.find((p) => p.id === rule.on_page)
  if (!page) return []
  return [...(rule.then ?? []), ...(rule.else ?? [])].filter(
    (a) =>
      a.action !== 'goto' && a.action !== 'end' && s.fields[a.target] && !page.fields.includes(a.target),
  )
}

export function hasSensitive(s: DraftSchema): boolean {
  return Object.values(s.fields).some((f) => f.sensitive)
}

export function sensitiveFieldsOnPage(s: DraftSchema, pageId: PageID): FieldID[] {
  const page = s.pages.find((p) => p.id === pageId)
  return (page?.fields ?? []).filter((f) => s.fields[f]?.sensitive)
}

/* --------------------------------------------------------- describing rules */

export const OPERATORS: { op: string; label: string }[] = [
  { op: 'eq', label: '=' },
  { op: 'neq', label: '≠' },
  { op: 'in', label: 'thuộc' },
  { op: 'not_in', label: 'không thuộc' },
  { op: 'contains', label: 'chứa' },
  { op: 'gt', label: '>' },
  { op: 'gte', label: '≥' },
  { op: 'lt', label: '<' },
  { op: 'lte', label: '≤' },
  { op: 'between', label: 'trong khoảng' },
  { op: 'is_empty', label: 'bỏ trống' },
  { op: 'is_not_empty', label: 'có trả lời' },
]

export const ACTIONS: { action: string; label: string }[] = [
  { action: 'show', label: 'hiện field' },
  { action: 'hide', label: 'ẩn field' },
  { action: 'require', label: 'bắt buộc field' },
  { action: 'optional', label: 'bỏ bắt buộc field' },
  { action: 'goto', label: 'nhảy tới trang' },
  { action: 'end', label: 'kết thúc biểu mẫu' },
]

export function operatorLabel(op: string): string {
  return OPERATORS.find((o) => o.op === op)?.label ?? op
}

export function needsValue(op: string): boolean {
  return op !== 'is_empty' && op !== 'is_not_empty'
}

/** The condition in words, with option labels resolved. Rules are stored by id;
 *  a reader needs to see the wording the respondent sees. */
export function describeCondition(s: DraftSchema, c: Condition): string {
  const fid = c.field
  const label = (v: unknown): string =>
    isChoice(s.fields[fid]?.type) ? `“${optionLabel(s, fid, String(v))}”` : `“${String(v)}”`

  switch (c.op) {
    case 'is_empty':
      return 'NẾU bỏ trống'
    case 'is_not_empty':
      return 'NẾU có trả lời'
    case 'between': {
      const v = Array.isArray(c.value) ? c.value : []
      return `NẾU trong khoảng ${String(v[0] ?? '?')} – ${String(v[1] ?? '?')}`
    }
    case 'in':
    case 'not_in': {
      const v = Array.isArray(c.value) ? c.value : []
      const list = v.map(label).join(', ') || '—'
      return `NẾU ${operatorLabel(c.op)} ${list}`
    }
    default:
      return `NẾU ${operatorLabel(c.op)} ${label(c.value)}`
  }
}

export function describeAction(s: DraftSchema, a: Action): string {
  switch (a.action) {
    case 'goto':
      return `→ nhảy tới ${pageLabel(s, a.target)}`
    case 'end':
      return '→ kết thúc biểu mẫu'
    case 'show':
      return `→ hiện ${fieldLabel(s, a.target)}`
    case 'hide':
      return `→ ẩn ${fieldLabel(s, a.target)}`
    case 'require':
      return `→ bắt buộc ${fieldLabel(s, a.target)}`
    case 'optional':
      return `→ bỏ bắt buộc ${fieldLabel(s, a.target)}`
    default:
      return `→ ${a.action} ${a.target}`
  }
}

/* ---------------------------------------------------------------- the graph */

export interface FlowEdge {
  from: PageID
  /** '' means the form ends after this page. */
  to: PageID | ''
  kind: 'default' | 'goto' | 'end'
  label: string
  ruleId?: string
  /** True when the edge points back into the path that reached it: a loop. */
  back: boolean
}

export interface GraphCheck {
  code: string
  label: string
  ok: boolean
  detail?: string
}

export interface FlowGraph {
  edges: FlowEdge[]
  /** Distance from the first page, used to lay the diagram out in bands. */
  rank: Record<PageID, number>
  reachable: Set<PageID>
  unreachable: PageID[]
  cycleAt: PageID | null
  branchPages: Set<PageID>
  checks: GraphCheck[]
}

/**
 * The navigation graph, for drawing and for the read-only checks beside it.
 *
 * This mirrors the server's walkGraph so the picture matches what publishing
 * will decide, but it is not the authority: POST /draft/validate is, and the
 * screen shows the server's verdict whenever it has one. The local copy exists
 * because a diagram that only updates after a round trip is a diagram nobody
 * trusts while editing.
 */
export function buildFlowGraph(s: DraftSchema): FlowGraph {
  const edges: FlowEdge[] = []
  const branchPages = new Set<PageID>()

  s.pages.forEach((p, i) => {
    const rules = (s.rules ?? []).filter((r) => r.on_page === p.id)
    for (const r of rules) {
      const branches: [Action[], string][] = [
        [r.then ?? [], describeCondition(s, r.when)],
        [r.else ?? [], 'NGƯỢC LẠI'],
      ]
      for (const [actions, label] of branches) {
        for (const a of actions) {
          if (a.action === 'goto') {
            branchPages.add(p.id)
            edges.push({ from: p.id, to: a.target, kind: 'goto', label, ruleId: r.id, back: false })
          } else if (a.action === 'end') {
            branchPages.add(p.id)
            edges.push({ from: p.id, to: '', kind: 'end', label, ruleId: r.id, back: false })
          }
        }
      }
    }
    const fallthrough = p.next || s.pages[i + 1]?.id || ''
    edges.push({
      from: p.id,
      to: fallthrough,
      kind: 'default',
      label: branchPages.has(p.id) ? 'mặc định' : '',
      back: false,
    })
  })

  const out = new Map<PageID, PageID[]>()
  for (const e of edges) {
    if (e.to === '') continue
    const list = out.get(e.from)
    if (list) list.push(e.to)
    else out.set(e.from, [e.to])
  }

  const reachable = new Set<PageID>()
  const colour = new Map<PageID, number>() // 0 unseen, 1 on stack, 2 done
  const backEdges = new Set<string>()
  let cycleAt: PageID | null = null

  const visit = (id: PageID): void => {
    if (colour.get(id) === 2) return
    colour.set(id, 1)
    reachable.add(id)
    for (const next of out.get(id) ?? []) {
      if (colour.get(next) === 1) {
        backEdges.add(`${id}->${next}`)
        if (!cycleAt) cycleAt = next
        continue
      }
      visit(next)
    }
    colour.set(id, 2)
  }
  const first = s.pages[0]
  if (first) visit(first.id)

  for (const e of edges) if (e.to !== '' && backEdges.has(`${e.from}->${e.to}`)) e.back = true

  // Longest-path ranking over the forward edges, so an arrow always points down
  // the page. Capped by the page count: a loop cannot make it run away.
  const rank: Record<PageID, number> = {}
  for (const p of s.pages) rank[p.id] = 0
  for (let i = 0; i < s.pages.length; i++) {
    let moved = false
    for (const e of edges) {
      if (e.to === '' || e.back) continue
      const from = rank[e.from] ?? 0
      if ((rank[e.to] ?? 0) < from + 1) {
        rank[e.to] = from + 1
        moved = true
      }
    }
    if (!moved) break
  }

  const unreachable = s.pages.filter((p) => !reachable.has(p.id)).map((p) => p.id)
  const purposes = s.consent?.purposes ?? []
  const sensitive = hasSensitive(s)

  const checks: GraphCheck[] = [
    {
      code: 'cycle',
      label: 'không có vòng lặp',
      ok: cycleAt === null,
      detail: cycleAt ? `quay lại ${pageLabel(s, cycleAt)}` : undefined,
    },
    {
      code: 'unreachable',
      label: 'không có trang không tới được',
      ok: unreachable.length === 0,
      detail:
        unreachable.length > 0
          ? unreachable.map((p) => pageLabel(s, p)).join(' · ')
          : undefined,
    },
    {
      code: 'consent_last',
      label: 'khối đồng ý luôn ở cuối',
      // The consent block is not a page: the public runtime renders it after the
      // last page of whichever branch the respondent took, so it cannot be
      // skipped. What can be missing is the purpose it is collected under.
      ok: purposes.length > 0,
      detail: purposes.length > 0 ? `${purposes.length} mục đích` : 'chưa khai báo mục đích xử lý',
    },
  ]
  if (sensitive) {
    checks.push({
      code: 'sensitive_notice',
      label: 'có thông báo dữ liệu nhạy cảm',
      ok: Boolean(s.consent?.sensitive_notice_required),
      detail: s.consent?.sensitive_notice_required
        ? undefined
        : 'biểu mẫu thu dữ liệu nhạy cảm nhưng chưa bật thông báo',
    })
  }

  return { edges, rank, reachable, unreachable, cycleAt, branchPages, checks }
}

/* ----------------------------------------------- server messages in Vietnamese */

/** Validation issues arrive from the Go package in English. The code is the
 *  contract; the sentence beside it is written here so the screen speaks one
 *  language. Unknown codes fall through to the server text rather than to
 *  "có lỗi xảy ra", which would tell the reader nothing. */
export function issueText(s: DraftSchema, issue: Issue): string {
  const target = issue.target ?? ''
  const asField = () => (s.fields[target] ? `“${fieldLabel(s, target)}”` : target)
  const asPage = () => (pageIndex(s, target) >= 0 ? pageLabel(s, target) : target)

  switch (issue.code) {
    case 'empty_schema':
      return 'Biểu mẫu chưa có trang nào.'
    case 'navigation_cycle':
      return `Điều hướng quay vòng về ${asPage()} — người trả lời sẽ không bao giờ tới được cuối.`
    case 'unreachable_page':
      return `${asPage()} không tới được từ trang đầu.`
    case 'required_but_unreachable':
      return `Field ${asField()} bắt buộc nhưng không đường nào hiện nó — biểu mẫu sẽ luôn từ chối submit.`
    case 'dangling_reference':
      return `Tham chiếu treo ở ${target || 'schema'}: ${issue.message}`
    case 'duplicate_id':
      return `Id bị trùng: ${target}.`
    case 'orphan_field':
      return `Field ${asField()} chưa nằm trên trang nào nên không ai trả lời được.`
    case 'invalid_field_config':
      return `Cấu hình field ${asField()} chưa hợp lệ: ${issue.message}`
    case 'consent_block_missing':
      return 'Biểu mẫu thu dữ liệu cá nhân nhưng chưa khai báo mục đích xử lý — không có căn cứ pháp lý để lưu.'
    case 'sensitive_notice_missing':
      return 'Biểu mẫu thu dữ liệu nhạy cảm nên phải báo rõ điều đó cho chủ thể dữ liệu.'
    case 'limit_exceeded':
      return `Vượt giới hạn schema: ${issue.message}`
    default:
      return issue.message
  }
}

/** The same for diff entries. The consequence for data already collected is the
 *  part the publisher actually needs, so it is stated, not implied. */
export function changeText(s: DraftSchema, c: Change): string {
  const name = s.fields[c.target] ? `“${fieldLabel(s, c.target)}”` : c.target
  switch (c.kind) {
    case 'field_removed':
      return `Bỏ field ${c.target} — câu trả lời cũ vẫn giữ nguyên, cột trong bảng dữ liệu được đánh dấu đã gỡ.`
    case 'field_type_changed':
      return `${name} đổi kiểu — bảng dữ liệu tách thành hai cột cho hai kiểu, không ép kiểu dữ liệu cũ.`
    case 'label_changed':
      return `Đổi nhãn ${name} — id giữ nguyên nên dữ liệu cũ không bị ảnh hưởng.`
    case 'field_now_required':
      return `${name} chuyển sang bắt buộc — bản ghi thu trước đó không bị coi là không hợp lệ.`
    case 'field_now_sensitive':
      return `${name} chuyển sang nhạy cảm — dữ liệu thu trước đó đã lưu không mã hoá và không được bảo vệ hồi tố.`
    case 'field_added':
      return `Thêm field ${name} — bản ghi cũ hiện ô trống nghĩa là chưa từng được hỏi.`
    case 'option_added':
      return `Thêm lựa chọn cho ${name}.`
    case 'option_removed':
      return `Bỏ một lựa chọn của ${name} — bản ghi đã chọn nó vẫn giữ id cũ và hiện là lựa chọn đã gỡ.`
    case 'sensitive_introduced':
      return 'Version này bắt đầu thu dữ liệu nhạy cảm — phải cập nhật văn bản đồng ý và publish lại văn bản đó.'
    default:
      return c.message
  }
}

/** The +/~/− marker the wireframe puts in front of each non-breaking change. */
export function changeMark(kind: string): string {
  if (kind.endsWith('_added') || kind === 'field_added' || kind === 'option_added') return '+ thêm'
  if (kind.endsWith('_removed')) return '− bỏ'
  return '~ sửa'
}
