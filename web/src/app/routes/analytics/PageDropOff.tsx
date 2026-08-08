/**
 * Where respondents stop, page by page.
 *
 * This is the most actionable block in the whole analytics screen: it names the
 * page that loses people rather than reporting that people are lost.
 *
 * It is also a different measurement from the funnel above it. The funnel is
 * read from analytics.funnel_rollups, which cover the whole period; these rows
 * are computed from raw page-view events, which only reach back as far as the
 * raw-event retention. So the two are shown as two blocks with their own
 * coverage stated, and no number here is ever divided by a number up there --
 * that is how a link report once produced a repeat rate of -3.1.
 */
import { Card, Empty, StatusPill, SensitiveTag, num, pct, date } from '../../components/ui'
import { SplitBar } from './Bars'

export interface DropOffRow {
  page_id: string
  entered: number
  left: number
  /** Page title, when the endpoint can resolve it. Falls back to the raw id --
   *  guessing a title from the id would be inventing form structure. */
  title?: string | null
  /** True when the page holds a field marked sensitive. Reported by the API, not
   *  inferred here: whether a field is sensitive is a property of the schema. */
  sensitive?: boolean | null
}

export function PageDropOff({
  rows,
  coverageFrom,
}: {
  rows: DropOffRow[] | null | undefined
  /** Earliest moment the raw events can speak for. */
  coverageFrom?: string | null
}) {
  if (!rows) {
    return (
      <Card title="Rơi rớt theo trang biểu mẫu">
        <Empty
          title="Chưa đo được rơi rớt theo trang"
          hint={
            <>
              Số liệu này cần sự kiện <code className="font-mono">form_page_view</code> cho từng
              trang, và phần trả lời của endpoint phân tích hiện chưa kèm mục này. Không có nó thì
              câu hỏi “trang nào làm người ta bỏ cuộc” không trả lời được — và điền số ước lượng
              vào đây còn tệ hơn để trống.
            </>
          }
        />
      </Card>
    )
  }

  if (rows.length === 0) {
    return (
      <Card title="Rơi rớt theo trang biểu mẫu">
        <Empty
          title="Không có lượt xem trang nào trong kỳ"
          hint="Biểu mẫu chưa được mở trong khoảng thời gian đang chọn, hoặc các lượt mở đã nằm ngoài hạn lưu sự kiện thô. Thử nới khoảng thời gian."
        />
      </Card>
    )
  }

  const widest = Math.max(...rows.map((r) => r.entered))
  // The page that loses the largest share of the people who reached it. Share,
  // not headcount: the first page always loses the most people in absolute terms
  // simply by being first.
  let worst = ''
  let worstRate = 0
  for (const r of rows) {
    if (r.entered <= 0) continue
    const rate = r.left / r.entered
    if (rate > worstRate) {
      worstRate = rate
      worst = r.page_id
    }
  }

  return (
    <Card
      title="Rơi rớt theo trang biểu mẫu"
      aside={
        coverageFrom
          ? `từ sự kiện thô, chỉ tính từ ${date(coverageFrom)}`
          : 'từ sự kiện thô của form'
      }
    >
      <ol className="flex flex-col gap-3">
        {rows.map((r) => {
          const danger = r.page_id === worst
          const stayed = Math.max(0, r.entered - Math.min(r.left, r.entered))
          return (
            <li key={r.page_id}>
              <div className="flex items-baseline justify-between gap-3">
                <div className="flex min-w-0 items-baseline gap-2">
                  <span
                    className={`truncate text-body ${danger ? 'font-semibold text-overdue' : 'font-semibold'}`}
                  >
                    {r.title || r.page_id}
                  </span>
                  {r.sensitive && <SensitiveTag>field nhạy cảm</SensitiveTag>}
                  {danger && <StatusPill tone="overdue">rơi rớt mạnh nhất</StatusPill>}
                </div>
                <span className="shrink-0 font-mono text-meta">
                  {num(r.entered)} → {num(stayed)} ·{' '}
                  {/* No minus sign in front of an em dash: "−—" reads as a
                      measured loss of nothing rather than as nothing measured. */}
                  {r.entered > 0 ? `−${pct(Math.min(r.left, r.entered), r.entered)}` : '—'}
                </span>
              </div>
              {r.title && <div className="id-chip">{r.page_id}</div>}
              <div className="mt-1">
                <SplitBar entered={r.entered} left={r.left} widest={widest} danger={danger} />
              </div>
            </li>
          )
        })}
      </ol>

      <p className="mt-3 border-t border-dashed border-line pt-2 text-meta text-muted">
        Cột đậm là số người đi tiếp, phần cuối thanh là số người dừng lại ở trang đó. Tỉ lệ luôn
        tính trên số người <em>vào chính trang ấy</em> — không tính trên tổng lượt bắt đầu điền ở
        phễu phía trên, vì hai con số đó lấy từ hai nguồn phủ hai khoảng thời gian khác nhau.
      </p>
    </Card>
  )
}
