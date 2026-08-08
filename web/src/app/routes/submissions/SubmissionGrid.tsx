/**
 * The submission table itself.
 *
 * Presentational on purpose: fetching, paging and the reveal decision live in
 * Submissions.tsx, so this file only has to answer "what does this cell look
 * like", which is the part that has to stay consistent with the legend.
 */
import { SensitiveTag, StatusPill, Table, Td, Th, Tr, dateTime } from '../../components/ui'
import {
  CELL_LEGEND,
  cellView,
  statusMeta,
  type ApiRow,
  type CellKind,
  type GridColumn,
} from './columns'

/** How each cell kind is drawn. The absent kinds are deliberately unlike each
 *  other in shape as well as colour: three greys would read as one grey. */
const kindClass: Record<CellKind, string> = {
  answered: '',
  masked: 'text-accent',
  blank: 'text-faint',
  hidden: 'text-faint font-mono text-meta',
  not_asked: 'text-faint font-mono text-meta italic',
  erased: 'text-faint italic',
  unknown: 'text-duesoon font-mono text-meta',
}

export function SubmissionGrid({
  columns,
  rows,
  revealSensitive,
  onRequestReveal,
}: {
  columns: GridColumn[]
  rows: ApiRow[]
  revealSensitive: boolean
  /** Absent when the reader has no right to unmask, which is different from
   *  there being nothing to unmask. */
  onRequestReveal?: () => void
}) {
  return (
    <div className="flex flex-col gap-2">
      <Table
        head={
          <>
            <Th>GỬI LÚC</Th>
            <Th>VERSION</Th>
            {columns.map((c) => (
              <Th key={c.key}>
                <span className="flex items-center gap-1">
                  <span title={`${c.label} · ${c.type} · hỏi ở ${c.versions}`}>
                    {c.fieldId.toUpperCase()}
                    {c.typeVariant && <span className="text-accent">@{c.typeVariant}</span>}
                  </span>
                  {c.sensitive && (
                    <span className="text-legal" title="Field nhạy cảm" aria-label="field nhạy cảm">
                      ◆
                    </span>
                  )}
                </span>
                <span className="mt-0.5 block font-mono text-meta normal-case tracking-normal text-faint">
                  {c.retiredAfter ? `gỡ từ v${c.retiredAfter + 1}` : c.versions}
                </span>
              </Th>
            ))}
            <Th>TRẠNG THÁI</Th>
          </>
        }
      >
        {rows.map((row) => {
          const status = statusMeta(row.status)
          return (
            <Tr key={row.id} className={status.dimmed ? 'bg-panel/60' : ''}>
              <Td className="whitespace-nowrap font-mono text-meta">{dateTime(row.submitted_at)}</Td>
              <Td className="font-mono text-meta">v{row.form_version}</Td>
              {columns.map((c) => {
                const view = cellView(c, row, { revealSensitive })
                return (
                  <Td key={c.key} className={kindClass[view.kind]}>
                    <span title={view.meaning || undefined}>{view.text}</span>
                    {view.kind === 'masked' && onRequestReveal && (
                      <button
                        type="button"
                        onClick={onRequestReveal}
                        className="ml-1 rounded border border-dashed border-accent px-1 text-meta font-semibold text-accent"
                      >
                        hiện
                      </button>
                    )}
                  </Td>
                )
              })}
              <Td>
                <span title={status.meaning}>
                  <StatusPill tone={status.tone}>{status.label}</StatusPill>
                </span>
              </Td>
            </Tr>
          )
        })}
      </Table>

      <Legend columns={columns} rows={rows} />
    </div>
  )
}

/**
 * The legend is not decoration.
 *
 * Without it the reader has to guess whether `∅` means zero, and the whole
 * point of keeping the three absences apart is lost at the moment somebody
 * reads the table.
 */
function Legend({ columns, rows }: { columns: GridColumn[]; rows: ApiRow[] }) {
  const statuses = [...new Set(rows.map((r) => r.status))].map(statusMeta)
  const sensitive = columns.filter((c) => c.sensitive)

  return (
    <div className="rounded border border-dashed border-line bg-panel px-3 py-2 text-meta text-muted">
      <dl className="grid gap-x-4 gap-y-1 sm:grid-cols-2">
        {CELL_LEGEND.map((item) => (
          <div key={item.sample} className="flex gap-2">
            <dt className="w-16 shrink-0 font-mono text-meta text-ink">{item.sample}</dt>
            <dd className="min-w-0">{item.meaning}</dd>
          </div>
        ))}
        {statuses.map((s) => (
          <div key={s.code} className="flex gap-2">
            <dt className="w-16 shrink-0">
              <StatusPill tone={s.tone}>{s.label}</StatusPill>
            </dt>
            <dd className="min-w-0">{s.meaning}</dd>
          </div>
        ))}
      </dl>

      {sensitive.length > 0 && (
        <p className="mt-2 flex flex-wrap items-center gap-1 border-t border-line pt-2">
          <SensitiveTag>field nhạy cảm</SensitiveTag>
          <span>
            {sensitive.map((c) => c.fieldId).join(', ')} — che mặc định. Mở lớp che là một lần truy cập
            dữ liệu nhạy cảm và được ghi vào nhật ký audit.
          </span>
        </p>
      )}
    </div>
  )
}
