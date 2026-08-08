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
  onRectify,
  onHistory,
}: {
  columns: GridColumn[]
  rows: ApiRow[]
  revealSensitive: boolean
  /** Absent when the reader may not handle data subject requests. Correcting a
   *  record on somebody's behalf is a DSR, not a table edit, so it is gated by
   *  the same capability as the rest of them. */
  onRectify?: (row: ApiRow) => void
  /** Always available to a reader of the grid: the history is answers, and
   *  whoever may read the answers may read what they used to be. */
  onHistory: (row: ApiRow) => void
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
                {/* The question, not its id. The id was the header until a real
                    grid was looked at: six columns of FLD_01KZ… are unreadable,
                    and the label was reachable only by hovering — which is not
                    reachable at all on the phone or in a screenshot. The id stays
                    below because it is what the API and the export are keyed by. */}
                <span className="flex items-center gap-1">
                  <span title={`${c.fieldId} · ${c.type} · hỏi ở ${c.versions}`}>
                    {c.label}
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
            <Th>SỬA ĐỔI</Th>
            {onRectify && <Th>{''}</Th>}
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
              <Td className="whitespace-nowrap">
                {row.revision_count ? (
                  <button
                    type="button"
                    onClick={() => onHistory(row)}
                    className="rounded border border-accent px-1.5 text-meta font-semibold text-accent"
                    title="Bản ghi này đã được sửa sau khi gửi. Xem đã đổi gì, ai đổi."
                  >
                    {row.revision_count} lần
                  </button>
                ) : (
                  // No button when there is nothing to show. A control that
                  // opens an empty box teaches people to stop pressing it, and
                  // this one is worth pressing on the rows where it appears.
                  <span className="text-meta text-faint" title="Chưa từng sửa">
                    —
                  </span>
                )}
              </Td>
              {onRectify && (
                <Td className="whitespace-nowrap">
                  {row.subject_id && !status.dimmed ? (
                    <button
                      type="button"
                      onClick={() => onRectify(row)}
                      className="rounded border border-line px-1.5 text-meta text-muted hover:text-ink"
                      title="Chủ thể yêu cầu sửa và bạn sửa thay họ. Ghi thành một yêu cầu chỉnh sửa."
                    >
                      sửa
                    </button>
                  ) : (
                    // Said, not left blank. A missing button with no reason reads
                    // as a broken screen; these two are different situations and
                    // neither is an error.
                    <span
                      className="text-meta text-faint"
                      title={
                        row.subject_id
                          ? 'Bản ghi đã xoá hoặc đã ẩn danh — không còn gì để sửa.'
                          : 'Biểu mẫu này không hỏi thông tin định danh, nên không có chủ thể nào để đứng tên yêu cầu.'
                      }
                    >
                      —
                    </span>
                  )}
                </Td>
              )}
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
          // No fixed width on this one. w-16 fits the mono samples above and is
          // narrower than the pill, so "Bình thường" overlapped the sentence
          // explaining it -- the legend rendered unreadable at exactly the place
          // it exists to be read.
          <div key={s.code} className="flex items-start gap-2">
            <dt className="shrink-0">
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
            {/* The question, not its id. Third place this leaked. */}
            {sensitive.map((c) => c.label).join(', ')} — che mặc định. Mở lớp che là một lần truy cập
            dữ liệu nhạy cảm và được ghi vào nhật ký audit.
          </span>
        </p>
      )}
    </div>
  )
}
