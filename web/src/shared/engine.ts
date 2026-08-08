/**
 * Client port of internal/modules/forms/engine.
 *
 * This decides what the respondent sees as they answer. The server keeps the
 * authoritative copy and re-evaluates every submission -- trusting the client's
 * claim about which fields it displayed would let a caller submit answers for a
 * branch they never saw, and therefore never saw the consent text for.
 *
 * The two implementations are held together by testdata/golden.json, which both
 * read unchanged. A rule engine that disagrees between browser and server
 * produces a form that validates on screen and fails on submit, which a person
 * filling it in experiences as the form simply being broken.
 */

export type FieldID = string
export type PageID = string

export interface Option {
  id: string
  label: string
}

/**
 * Mirrors domain.Field in internal/modules/forms/domain/schema.go.
 *
 * Field names are the Go json tags, not what reads well here. `max_mb` was
 * `max_bytes` in an earlier draft of this file, which meant an upload limit read
 * as undefined -- a size cap that silently is not one.
 */
export interface Field {
  type: string
  label: string
  required?: boolean
  hidden?: boolean

  options?: Option[]

  pii?: string
  sensitive?: boolean
  /** The field used to recognise a data subject across submissions. */
  identifier?: boolean

  scale?: number
  accept?: string[]
  /** Megabytes, per the Go tag -- not bytes. */
  max_mb?: number
  multiline?: boolean
  /** One of the named formats in format.ts. Empty means any text. */
  format?: string
  /** Bounds: YYYY-MM-DD for a date, a number for the numeric formats. */
  min?: string
  max?: string
}

export interface Page {
  id: PageID
  title: string
  next?: PageID
  fields: FieldID[]
}

export interface Condition {
  op: string
  field: FieldID
  value?: unknown
}

export interface Action {
  action: string
  target: string
}

export interface Rule {
  id: string
  on_page: PageID
  when: Condition
  then?: Action[]
  else?: Action[]
}

export interface Purpose {
  code: string
  required?: boolean
  label?: string
}

export interface Schema {
  v: number
  pages: Page[]
  fields: Record<FieldID, Field>
  rules?: Rule[]
  consent?: { purposes?: Purpose[] }
}

export type Answers = Record<string, unknown>

export interface Result {
  /** Fields actually shown, in page then declaration order. */
  visible: FieldID[]
  /** The visible fields that must be answered. */
  required: FieldID[]
  /** Pages traversed, in order. */
  path: PageID[]
}

export class EngineError extends Error {}

/** Evaluate walks the form as the answers direct and reports what was shown. */
export function evaluate(schema: Schema, answers: Answers): Result {
  const result: Result = { visible: [], required: [], path: [] }
  if (schema.pages.length === 0) return result

  const byPage = new Map<PageID, Rule[]>()
  for (const rule of schema.rules ?? []) {
    const list = byPage.get(rule.on_page)
    if (list) list.push(rule)
    else byPage.set(rule.on_page, [rule])
  }

  let current: PageID = schema.pages[0]!.id
  const seen = new Set<PageID>()

  // Publishing rejects cyclic navigation, but a schema written before that check
  // existed -- or edited straight in the database -- must not be able to hang
  // the tab. The visited set bounds the walk regardless.
  for (let i = 0; i <= schema.pages.length; i++) {
    const page = schema.pages.find((p) => p.id === current)
    if (!page) throw new EngineError(`page "${current}" referenced but not defined`)
    if (seen.has(current)) throw new EngineError(`navigation cycle at page "${current}"`)
    seen.add(current)
    result.path.push(current)

    const { visible, required, next } = evalPage(schema, page, byPage.get(current) ?? [], answers)
    result.visible.push(...visible)
    result.required.push(...required)

    if (next === '') return result
    current = next
  }
  throw new EngineError(`navigation exceeded ${schema.pages.length} pages`)
}

interface PageResult {
  visible: FieldID[]
  required: FieldID[]
  /** '' means the form ends here. */
  next: PageID | ''
}

function evalPage(schema: Schema, page: Page, rules: Rule[], answers: Answers): PageResult {
  // Start from the declared state, then let rules amend it. A field marked
  // hidden appears only if a rule shows it.
  const shown = new Map<FieldID, boolean>()
  const must = new Map<FieldID, boolean>()
  for (const fid of page.fields) {
    const f = schema.fields[fid]
    if (!f) throw new EngineError(`page "${page.id}" lists undefined field "${fid}"`)
    shown.set(fid, !f.hidden)
    must.set(fid, Boolean(f.required))
  }

  let jump: PageID = ''
  let ended = false
  let decided = false

  // One pass, in declaration order. Later rules override earlier ones, which
  // makes the outcome depend only on the schema's own ordering.
  for (const rule of rules) {
    const match = evalCondition(rule.when, answers)
    const actions = (match ? rule.then : rule.else) ?? []
    for (const a of actions) {
      switch (a.action) {
        case 'show':
          shown.set(a.target, true)
          break
        case 'hide':
          shown.set(a.target, false)
          break
        case 'require':
          must.set(a.target, true)
          break
        case 'optional':
          must.set(a.target, false)
          break
        case 'goto':
          jump = a.target
          ended = false
          decided = true
          break
        case 'end':
          jump = ''
          ended = true
          decided = true
          break
        default:
          throw new EngineError(`rule "${rule.id}": unknown action "${a.action}"`)
      }
    }
  }

  const visible: FieldID[] = []
  const required: FieldID[] = []
  for (const fid of page.fields) {
    if (!shown.get(fid)) continue
    visible.push(fid)
    if (must.get(fid)) required.push(fid)
  }

  if (decided && ended) return { visible, required, next: '' }
  if (decided) return { visible, required, next: jump }
  return { visible, required, next: defaultNext(schema, page.id) }
}

/** Where the form goes when no rule decides: the declared next, or the following
 *  page in order. */
function defaultNext(schema: Schema, id: PageID): PageID | '' {
  const i = schema.pages.findIndex((p) => p.id === id)
  if (i < 0) return ''
  const page = schema.pages[i]!
  if (page.next) return page.next
  return schema.pages[i + 1]?.id ?? ''
}

/**
 * evalCondition resolves one comparison.
 *
 * Exported because the rule builder previews a condition against sample answers
 * before the rule is saved, and because the shared fixture tests it directly.
 *
 * An unanswered field makes every comparison false except the emptiness checks.
 * Treating a missing answer as a zero value would route respondents down the
 * "rating is 0, so ask what went wrong" branch before they had rated anything.
 */
export function evalCondition(c: Condition, answers: Answers): boolean {
  const raw = answers[c.field]
  const answered = c.field in answers && !isEmpty(raw)

  switch (c.op) {
    case 'is_empty':
      return !answered
    case 'is_not_empty':
      return answered
  }
  if (!answered) return false

  switch (c.op) {
    case 'eq':
      return equal(raw, c.value)
    case 'neq':
      return !equal(raw, c.value)
    case 'in':
      return inList(raw, c.value)
    case 'not_in':
      return !inList(raw, c.value)
    case 'contains':
      return contains(raw, c.value)
    case 'gt':
    case 'gte':
    case 'lt':
    case 'lte':
      return compareOrder(c.op, raw, c.value)
    case 'between': {
      const bounds = c.value
      if (!Array.isArray(bounds) || bounds.length !== 2) {
        throw new EngineError('operator "between" needs a two-element value')
      }
      return compareOrder('gte', raw, bounds[0]) && compareOrder('lte', raw, bounds[1])
    }
    default:
      throw new EngineError(`unknown operator "${c.op}"`)
  }
}

function isEmpty(v: unknown): boolean {
  if (v === null || v === undefined) return true
  if (typeof v === 'string') return v.trim() === ''
  if (Array.isArray(v)) return v.length === 0
  if (typeof v === 'object') return Object.keys(v as object).length === 0
  return false
}

/**
 * equal compares an answer against a rule value.
 *
 * Both sides arrive from JSON, where a choice is an option id string and a
 * rating is a number, so comparison normalises to string except when both sides
 * are genuinely numeric.
 */
function equal(answer: unknown, want: unknown): boolean {
  const an = asNumber(answer)
  const wn = asNumber(want)
  if (an !== null && wn !== null) return an === wn
  return asString(answer) === asString(want)
}

function inList(answer: unknown, want: unknown): boolean {
  if (!Array.isArray(want)) return false
  return want.some((item) => equal(answer, item))
}

/** contains checks membership for multi-select answers and substring for text. */
function contains(answer: unknown, want: unknown): boolean {
  if (Array.isArray(answer)) return answer.some((item) => equal(item, want))
  return asString(answer).includes(asString(want))
}

function compareOrder(op: string, answer: unknown, want: unknown): boolean {
  const an = asNumber(answer)
  const wn = asNumber(want)
  if (an !== null && wn !== null) return applyOrder(op, cmpNumber(an, wn))

  // Dates are ISO-8601, which sorts correctly as text, so ordering works for
  // date fields without parsing them.
  const as = asString(answer)
  const ws = asString(want)
  if (as === '' || ws === '') return false
  return applyOrder(op, as < ws ? -1 : as > ws ? 1 : 0)
}

function applyOrder(op: string, c: number): boolean {
  switch (op) {
    case 'gt':
      return c > 0
    case 'gte':
      return c >= 0
    case 'lt':
      return c < 0
    case 'lte':
      return c <= 0
    default:
      return false
  }
}

function cmpNumber(a: number, b: number): number {
  return a < b ? -1 : a > b ? 1 : 0
}

/** asNumber mirrors the Go side, which also parses numeric strings -- a rating
 *  posted as "4" by a form control must compare like the number 4. */
function asNumber(v: unknown): number | null {
  if (typeof v === 'number') return Number.isFinite(v) ? v : null
  if (typeof v === 'string') {
    const t = v.trim()
    if (t === '') return null
    const n = Number(t)
    return Number.isFinite(n) ? n : null
  }
  return null
}

function asString(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'boolean') return v ? 'true' : 'false'
  if (typeof v === 'number') return String(v)
  return String(v)
}
