/**
 * The flow tab: the pages as blocks, the branches as labelled arrows.
 *
 * Read-only on purpose. A canvas you can rewire is a second, competing editor,
 * and the two disagree the moment one of them has a bug -- so this view answers
 * "where does an answer send someone" and nothing else. Editing stays in the
 * Soạn tab, where a rule sits next to the question it reads.
 *
 * Drawn with inline SVG for the connectors and plain elements for the blocks, so
 * the labels stay real text a screen reader and a text search can both find. No
 * diagram library: the graph is a handful of pages, and the layout it needs
 * (rank by distance from the first page) is a dozen lines.
 *
 * The simulator runs the shared engine -- the same evaluate() the public form and
 * the Go server run -- rather than a second interpretation of the rules. A
 * preview that disagreed with the runtime would be worse than no preview.
 */
import { useMemo, useState } from 'react'
import { EngineError, evaluate, type Answers, type FieldID, type PageID } from '../../../shared/engine'
import { SensitiveTag, StatusPill } from '../../components/ui'
import {
  type DraftSchema,
  type ValidationResult,
  buildFlowGraph,
  fieldLabel,
  isChoice,
  issueText,
  pageLabel,
  sensitiveFieldsOnPage,
} from './useDraft'

const NODE_W = 214
const NODE_H = 64
const CONSENT_W = 260
const GAP_X = 26
const GAP_Y = 62
const SIDE_MARGIN = 44 // room for a back edge to loop around

interface Placed {
  x: number
  y: number
  w: number
}

interface Props {
  schema: DraftSchema
  title: string
  validation: ValidationResult | null | undefined
  checking: boolean
  onCheck: () => void
  simulatorOpen: boolean
  onToggleSimulator: () => void
}

export function FlowDiagram({
  schema,
  title,
  validation,
  checking,
  onCheck,
  simulatorOpen,
  onToggleSimulator,
}: Props) {
  const graph = useMemo(() => buildFlowGraph(schema), [schema])
  const [answers, setAnswers] = useState<Answers>({})

  const simulation = useMemo(() => {
    if (!simulatorOpen) return null
    try {
      return { result: evaluate(schema, answers), error: null as string | null }
    } catch (err) {
      return {
        result: null,
        error: err instanceof EngineError ? err.message : 'Không mô phỏng được luồng này.',
      }
    }
  }, [schema, answers, simulatorOpen])

  const path = simulation?.result?.path ?? []

  // ---- layout ------------------------------------------------------------
  const layout = useMemo(() => {
    const bands = new Map<number, PageID[]>()
    const maxRank = schema.pages.reduce((m, p) => Math.max(m, graph.rank[p.id] ?? 0), 0)
    for (const p of schema.pages) {
      const band = graph.reachable.has(p.id) ? (graph.rank[p.id] ?? 0) : maxRank + 1
      const list = bands.get(band)
      if (list) list.push(p.id)
      else bands.set(band, [p.id])
    }
    const keys = [...bands.keys()].sort((a, b) => a - b)

    let width = CONSENT_W
    for (const k of keys) {
      const n = bands.get(k)?.length ?? 0
      width = Math.max(width, n * (NODE_W + GAP_X) - GAP_X)
    }

    const placed = new Map<PageID, Placed>()
    keys.forEach((k, row) => {
      const ids = bands.get(k) ?? []
      const rowWidth = ids.length * (NODE_W + GAP_X) - GAP_X
      const startX = (width - rowWidth) / 2
      ids.forEach((id, i) => {
        placed.set(id, { x: startX + i * (NODE_W + GAP_X), y: row * (NODE_H + GAP_Y), w: NODE_W })
      })
    })

    const rows = keys.length
    const consent: Placed = {
      x: (width - CONSENT_W) / 2,
      y: rows * (NODE_H + GAP_Y),
      w: CONSENT_W,
    }
    const submitY = consent.y + NODE_H + 34
    return { placed, width, consent, submitY, height: submitY + 34 }
  }, [schema.pages, graph])

  const onPath = (from: PageID, to: PageID): boolean => {
    for (let i = 0; i + 1 < path.length; i++) if (path[i] === from && path[i + 1] === to) return true
    return false
  }

  const branchCount = graph.edges.filter((e) => e.kind !== 'default').length

  return (
    <div className="flex h-full min-h-0 gap-4">
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="mb-3 flex items-center gap-3">
          <h2 className="text-body font-semibold">Sơ đồ luồng · {title}</h2>
          <span className="id-chip">
            {schema.pages.length} trang · {branchCount} nhánh
          </span>
          <div className="flex-1" />
          <button type="button" className="btn py-1 text-meta" onClick={onToggleSimulator}>
            {simulatorOpen ? 'Đóng mô phỏng' : 'Xem trước / mô phỏng'}
          </button>
          <button type="button" className="btn py-1 text-meta" onClick={onCheck} disabled={checking}>
            {checking ? 'Đang kiểm…' : 'Kiểm graph'}
          </button>
        </header>

        <div className="min-h-0 flex-1 overflow-auto rounded border border-line bg-panel p-4">
          {schema.pages.length === 0 ? (
            <p className="text-body text-muted">
              Chưa có trang nào để vẽ. Thêm trang ở tab Soạn.
            </p>
          ) : (
            <div
              className="relative mx-auto"
              style={{ width: layout.width + SIDE_MARGIN, height: layout.height }}
            >
              <svg
                className="absolute inset-0 text-ink"
                width={layout.width + SIDE_MARGIN}
                height={layout.height}
                aria-hidden
              >
                <defs>
                  <marker
                    id="cf-arrow"
                    markerWidth="7"
                    markerHeight="7"
                    refX="6"
                    refY="3.5"
                    orient="auto"
                  >
                    <path d="M0,0 L7,3.5 L0,7 Z" className="fill-ink" />
                  </marker>
                  <marker
                    id="cf-arrow-loop"
                    markerWidth="7"
                    markerHeight="7"
                    refX="6"
                    refY="3.5"
                    orient="auto"
                  >
                    <path d="M0,0 L7,3.5 L0,7 Z" className="fill-overdue" />
                  </marker>
                </defs>
                {graph.edges.map((e, i) => {
                  const from = layout.placed.get(e.from)
                  if (!from) return null
                  const to = e.to === '' ? layout.consent : layout.placed.get(e.to)
                  if (!to) return null

                  const x1 = from.x + from.w / 2
                  const y1 = from.y + NODE_H
                  const x2 = to.x + to.w / 2
                  const y2 = to.y

                  if (e.back) {
                    const side = layout.width + SIDE_MARGIN - 8
                    return (
                      <path
                        key={i}
                        d={`M ${from.x + from.w} ${from.y + NODE_H / 2} H ${side} V ${to.y + NODE_H / 2} H ${to.x + to.w}`}
                        className="fill-none stroke-overdue"
                        strokeWidth={1.5}
                        strokeDasharray="4 3"
                        markerEnd="url(#cf-arrow-loop)"
                      />
                    )
                  }

                  const mid = y1 + (y2 - y1) / 2
                  const lit = e.to !== '' && onPath(e.from, e.to)
                  return (
                    <path
                      key={i}
                      d={`M ${x1} ${y1} V ${mid} H ${x2} V ${y2 - 7}`}
                      className={`fill-none ${lit ? 'stroke-accent' : 'stroke-ink'}`}
                      strokeWidth={lit ? 2.5 : 1.5}
                      markerEnd="url(#cf-arrow)"
                    />
                  )
                })}
              </svg>

              {/* Edge labels as real text rather than SVG text: they are the
                  part of the picture a reader most needs to be able to select,
                  search and hear read aloud. */}
              {graph.edges.map((e, i) => {
                const from = layout.placed.get(e.from)
                const to = e.to === '' ? layout.consent : layout.placed.get(e.to)
                if (!from || !to || !e.label || e.back) return null
                const y1 = from.y + NODE_H
                const y2 = to.y
                return (
                  <span
                    key={i}
                    style={{ left: to.x + to.w / 2, top: y1 + (y2 - y1) / 2 - 9 }}
                    className="absolute -translate-x-1/2 whitespace-nowrap rounded border border-line bg-surface px-1 font-mono text-meta text-muted"
                  >
                    {e.label}
                  </span>
                )
              })}

              {schema.pages.map((p) => {
                const pos = layout.placed.get(p.id)
                if (!pos) return null
                const sensitive = sensitiveFieldsOnPage(schema, p.id)
                const reachable = graph.reachable.has(p.id)
                const lit = path.includes(p.id)
                const branch = graph.branchPages.has(p.id)
                return (
                  <div
                    key={p.id}
                    style={{ left: pos.x, top: pos.y, width: pos.w, height: NODE_H }}
                    className={`absolute overflow-hidden rounded bg-surface px-2.5 py-1.5 ${
                      lit ? 'border-2 border-accent' : branch ? 'border-2 border-line' : 'border border-line'
                    } ${reachable ? '' : 'border-dashed border-duesoon'}`}
                  >
                    <div className="font-mono text-meta text-faint">
                      TRANG {schema.pages.indexOf(p) + 1}
                      {branch && ' · ĐIỂM RẼ'}
                      {!reachable && ' · KHÔNG TỚI ĐƯỢC'}
                    </div>
                    <div className="truncate text-meta font-semibold">
                      {p.title || 'chưa đặt tên'}
                    </div>
                    <div className="id-chip">
                      {p.fields.length} field
                      {p.fields.some((f) => schema.fields[f]?.required) && ' · có bắt buộc'}
                    </div>
                    {sensitive.length > 0 && (
                      <div className="absolute bottom-1 right-2 font-mono text-meta text-accent">
                        ◆ chứa field nhạy cảm
                      </div>
                    )}
                  </div>
                )
              })}

              <div
                style={{ left: layout.consent.x, top: layout.consent.y, width: CONSENT_W, height: NODE_H }}
                className="absolute rounded border border-accent bg-surface px-2.5 py-1.5"
              >
                <div className="font-mono text-meta text-accent">ĐỒNG Ý · LUÔN HIỂN THỊ</div>
                <div className="text-meta font-semibold">Đồng ý xử lý dữ liệu</div>
                <div className="id-chip">
                  {(schema.consent?.purposes ?? []).length} mục đích
                  {schema.consent?.sensitive_notice_required && ' · có thông báo nhạy cảm'}
                </div>
              </div>

              <div
                style={{ left: layout.width / 2, top: layout.submitY }}
                className="absolute -translate-x-1/2 rounded-full border border-dashed border-line bg-surface px-3 py-1 text-meta font-semibold"
              >
                Gửi
              </div>
            </div>
          )}
        </div>

        <GraphChecks schema={schema} validation={validation} checks={graph.checks} />
      </div>

      {simulatorOpen && (
        <aside className="w-72 shrink-0 overflow-y-auto rounded border border-line bg-surface p-3">
          <h3 className="mb-1 font-mono text-meta tracking-caps text-faint">
            MÔ PHỎNG · CHẠY ENGINE DÙNG CHUNG
          </h3>
          <p className="mb-2 text-meta text-muted">
            Trả lời thử các câu có luật đọc tới. Kết quả do đúng engine mà server dùng để kiểm
            required tính ra.
          </p>

          <Simulator schema={schema} answers={answers} onChange={setAnswers} />

          {simulation?.error && (
            <p role="alert" className="mt-3 rounded border border-overdue/40 bg-overdue/5 px-2 py-1.5 text-meta text-overdue">
              {simulation.error}
            </p>
          )}

          {simulation?.result && (
            <div className="mt-3 border-t border-line pt-2">
              <div className="font-mono text-meta tracking-caps text-faint">ĐƯỜNG ĐI</div>
              <ol className="mb-2 mt-1 text-meta">
                {simulation.result.path.map((pid, i) => (
                  <li key={pid}>
                    {i + 1}. {pageLabel(schema, pid)}
                  </li>
                ))}
              </ol>
              <div className="font-mono text-meta tracking-caps text-faint">
                FIELD HIỂN THỊ ({simulation.result.visible.length})
              </div>
              <ul className="mt-1 flex flex-col gap-0.5 text-meta">
                {simulation.result.visible.map((fid) => (
                  <li key={fid} className="flex items-center gap-1.5">
                    <span className="truncate">{fieldLabel(schema, fid)}</span>
                    {simulation.result.required.includes(fid) && (
                      <span className="text-accent" title="bắt buộc">
                        *
                      </span>
                    )}
                    {schema.fields[fid]?.sensitive && <SensitiveTag />}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </aside>
      )}
    </div>
  )
}

/** One control per question a rule reads. Only those: they are what decides the
 *  path, and a full form preview would just be the public page rebuilt badly. */
function Simulator({
  schema,
  answers,
  onChange,
}: {
  schema: DraftSchema
  answers: Answers
  onChange: (a: Answers) => void
}) {
  const asked: FieldID[] = []
  for (const r of schema.rules ?? []) {
    if (r.when.field && !asked.includes(r.when.field) && schema.fields[r.when.field]) {
      asked.push(r.when.field)
    }
  }

  if (asked.length === 0) {
    return (
      <p className="text-meta text-muted">
        Chưa có luật nào đọc câu trả lời, nên luồng chỉ đi tuần tự qua các trang.
      </p>
    )
  }

  function set(fid: FieldID, value: unknown) {
    const next = { ...answers }
    if (value === '' || value === undefined) delete next[fid]
    else next[fid] = value
    onChange(next)
  }

  return (
    <div className="flex flex-col gap-2">
      {asked.map((fid) => {
        const f = schema.fields[fid]
        if (!f) return null
        const id = `sim-${fid}`
        return (
          <div key={fid}>
            <label htmlFor={id} className="block text-meta font-semibold">
              {f.label || fid}
            </label>
            {f.type === 'multi_choice' ? (
              <div className="flex flex-col gap-0.5 pt-0.5">
                {(f.options ?? []).map((o) => {
                  const chosen = Array.isArray(answers[fid]) ? (answers[fid] as unknown[]) : []
                  const on = chosen.map(String).includes(o.id)
                  return (
                    <label key={o.id} className="flex items-center gap-1.5 text-meta">
                      <input
                        type="checkbox"
                        checked={on}
                        className="size-3 accent-ink"
                        onChange={(e) => {
                          const next = new Set(chosen.map(String))
                          if (e.target.checked) next.add(o.id)
                          else next.delete(o.id)
                          set(fid, [...next])
                        }}
                      />
                      {o.label || o.id}
                    </label>
                  )
                })}
              </div>
            ) : isChoice(f.type) ? (
              <select
                id={id}
                className="input py-1 text-meta"
                value={String(answers[fid] ?? '')}
                onChange={(e) => set(fid, e.target.value)}
              >
                <option value="">— chưa trả lời —</option>
                {(f.options ?? []).map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.label || o.id}
                  </option>
                ))}
              </select>
            ) : (
              <input
                id={id}
                className="input py-1 text-meta"
                type={f.type === 'rating' ? 'number' : f.type === 'date' ? 'date' : 'text'}
                value={String(answers[fid] ?? '')}
                onChange={(e) => set(fid, e.target.value)}
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

/**
 * The check box under the diagram.
 *
 * It prefers the server's answer whenever there is one. The local graph walk
 * mirrors the Go checks, but publish is decided by the server, and a screen that
 * says "✓ không có vòng lặp" while the API is about to refuse the publish is
 * worse than one that admits it has not asked yet.
 */
function GraphChecks({
  schema,
  validation,
  checks,
}: {
  schema: DraftSchema
  validation: ValidationResult | null | undefined
  checks: ReturnType<typeof buildFlowGraph>['checks']
}) {
  const issues = validation?.issues ?? []
  const errors = issues.filter((i) => i.severity === 'error')
  const warnings = issues.filter((i) => i.severity !== 'error')

  return (
    <section
      role="status"
      className="mt-3 rounded border border-dashed border-line bg-surface px-3 py-2"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="font-mono text-meta tracking-caps text-faint">KIỂM GRAPH</span>
        {checks.map((c) => (
          <span key={c.code} className="text-meta">
            <span aria-hidden>{c.ok ? '✓' : '✕'}</span>{' '}
            <span className={c.ok ? '' : 'font-semibold text-overdue'}>{c.label}</span>
            {c.detail && <span className="id-chip"> {c.detail}</span>}
          </span>
        ))}
        <span className="flex-1" />
        {validation ? (
          <StatusPill tone={validation.ok ? 'ok' : 'overdue'}>
            {validation.ok ? 'server: publish được' : `server: ${errors.length} lỗi chặn`}
          </StatusPill>
        ) : (
          <StatusPill tone="neutral">chưa kiểm trên server</StatusPill>
        )}
      </div>

      {(errors.length > 0 || warnings.length > 0) && (
        <ul className="mt-2 flex flex-col gap-1 border-t border-line pt-2">
          {[...errors, ...warnings].map((i, n) => (
            <li key={n} className="text-meta">
              <span
                className={`font-mono text-meta ${i.severity === 'error' ? 'text-overdue' : 'text-duesoon'}`}
              >
                {i.severity === 'error' ? 'LỖI' : 'CẢNH BÁO'}
              </span>{' '}
              {issueText(schema, i)}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
