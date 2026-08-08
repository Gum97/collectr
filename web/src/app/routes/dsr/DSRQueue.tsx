import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router'
import { api } from '../../lib/api'
import {
  deadline,
  Empty,
  ErrorBanner,
  Loading,
  num,
  PageHeader,
  StatusPill,
  Table,
  Td,
  Th,
  Tr,
} from '../../components/ui'
import {
  isClosed,
  maskIdentifier,
  RIGHT_CODES,
  rightOf,
  shortId,
  STATUS_LABEL,
  type RightCode,
} from './rights'

/**
 * One request as `GET /api/v1/dsr/requests` returns it.
 *
 * The required fields are exactly what `AdminHandler.list` writes today. The
 * optional ones are things this screen must show but the endpoint does not send
 * yet -- they are typed optional rather than assumed, so a missing value renders
 * as "chưa ghi nhận" instead of as a confident wrong answer.
 */
export interface DSRRow {
  id: string
  type: string
  status: string
  received_at: string
  due_at: string
  /** Computed server-side. Authoritative over the browser clock. */
  overdue: boolean
  hours_remaining: number
  note: string
  data_subject_id: string
  needs_human: boolean

  /** Already masked by the API when present. Never a full identifier. */
  subject_identifier?: string | null
  verification_method?: string | null
  verified_at?: string | null
  /**
   * Answered after the deadline had already passed.
   *
   * A different fact from `overdue`: a fulfilled request is never overdue, so
   * without this flag a late answer looks exactly like a punctual one once it
   * closes. The Go side records it in the audit payload at resolve time.
   */
  answered_late?: boolean | null
  fulfilled_at?: string | null
  /** Projects the request touches, for the queue's context column. */
  projects?: { id: string; name: string }[] | null
  /**
   * What the request would reach if granted.
   *
   * Absent today -- nothing reports it -- and deliberately nullable rather than
   * defaulted to zero, because "0 bản ghi" on an erasure request reads as
   * "nothing to lose" when the truth is "nobody looked".
   */
  scope?: DSRScope | null
}

export interface DSRScope {
  submissions: number
  files: number
  projects: number
  items?: {
    id: string
    form_title: string
    project_name?: string | null
    has_sensitive?: boolean
    /** e.g. "form đã đóng", "đang bị legal hold". */
    note?: string | null
  }[]
}

export interface DSRList {
  data: DSRRow[]
  overdue_count: number
}

/** Page size asked for. The API caps at 200; anything beyond is not shown, and
 *  a truncated compliance queue must say so rather than look complete. */
const LIMIT = 200

export function useDSRRequests(scope: 'open' | 'all') {
  return useQuery({
    queryKey: ['dsr', 'requests', scope],
    queryFn: () =>
      api.get<DSRList>(
        `/api/v1/dsr/requests?status=${scope === 'all' ? 'all' : 'open'}&limit=${LIMIT}`,
      ),
    // A deadline countdown that is minutes stale is a deadline countdown that
    // lies, and this one carries a sanction risk.
    staleTime: 30_000,
    refetchInterval: 60_000,
  })
}

export function DSRQueue() {
  const [scope, setScope] = useState<'open' | 'all'>('open')
  const [type, setType] = useState<RightCode | 'all'>('all')
  const q = useDSRRequests(scope)

  const rows = useMemo(() => sortByUrgency(q.data?.data ?? []), [q.data])
  const shown = type === 'all' ? rows : rows.filter((r) => r.type === type)
  const openCount = rows.filter((r) => !isClosed(r.status)).length

  return (
    <div className="p-6">
      <PageHeader
        title="Yêu cầu chủ thể dữ liệu"
        meta="ListForAdmin · sắp theo due_at tăng dần"
        actions={
          q.data && (
            <>
              <StatusPill tone={q.data.overdue_count > 0 ? 'overdue' : 'ok'}>
                Quá hạn · {num(q.data.overdue_count)}
              </StatusPill>
              <StatusPill>Đang mở · {num(openCount)}</StatusPill>
            </>
          )
        }
      />

      <div className="mb-3 flex flex-wrap items-center gap-1.5 rounded border border-line bg-panel px-3 py-2.5">
        <Chip active={type === 'all'} onClick={() => setType('all')}>
          tất cả
        </Chip>
        {RIGHT_CODES.map((code) => (
          <Chip key={code} active={type === code} onClick={() => setType(code)}>
            {code}
          </Chip>
        ))}
        <span className="ml-auto flex items-center gap-1.5">
          <Chip active={scope === 'open'} onClick={() => setScope('open')}>
            đang mở
          </Chip>
          <Chip active={scope === 'all'} onClick={() => setScope('all')}>
            kể cả đã đóng
          </Chip>
        </span>
      </div>

      {q.isPending && <Loading label="Đang tải hàng đợi…" />}
      {q.isError && <ErrorBanner error={q.error} retry={() => void q.refetch()} />}

      {q.data && shown.length === 0 && (
        <Empty
          title={
            type === 'all'
              ? 'Không có yêu cầu nào trong phạm vi đang xem'
              : `Không có yêu cầu loại ${type}`
          }
          hint={
            scope === 'open'
              ? 'Đang lọc theo các yêu cầu chưa đóng. Yêu cầu đã đáp ứng hoặc đã từ chối nằm ở "kể cả đã đóng".'
              : 'Chưa có chủ thể nào thực hiện quyền của mình trong tổ chức này, hoặc bộ lọc loại quyền đang thu hẹp danh sách.'
          }
        />
      )}

      {q.data && shown.length > 0 && (
        <Table
          head={
            <>
              <Th>MÃ</Th>
              <Th>LOẠI</Th>
              <Th>CHỦ THỂ · XÁC MINH</Th>
              <Th>TRẠNG THÁI</Th>
              <Th>DỰ ÁN LIÊN QUAN</Th>
              <Th>CÒN LẠI</Th>
              <Th>HÀNH ĐỘNG</Th>
            </>
          }
        >
          {shown.map((r) => (
            <QueueRow key={r.id} row={r} />
          ))}
        </Table>
      )}

      {q.data && q.data.data.length >= LIMIT && (
        <p role="status" className="mt-2 text-meta font-semibold text-duesoon">
          ⚠ Danh sách bị cắt ở {num(LIMIT)} yêu cầu — còn yêu cầu chưa hiển thị. Lọc theo "đang mở"
          để chắc chắn thấy hết những cái còn hạn phải trả lời.
        </p>
      )}

      <div className="mt-3 rounded border border-dashed border-line px-3 py-2.5 text-meta leading-relaxed text-muted">
        Hạn được chốt lúc yêu cầu được tạo và lưu cứng vào bản ghi. Đổi cấu hình SLA về sau không kéo
        lùi hạn của yêu cầu đang chạy. Khi đóng, hệ thống ghi lại việc trả lời có muộn hay không
        trước khi áp trạng thái kết thúc — <span className="id-chip">answered_late</span> trong nhật
        ký audit. Quá hạn là rủi ro xử phạt theo NĐ 356/2025, không phải chỉ là chỉ số nội bộ.
      </div>
    </div>
  )
}

function QueueRow({ row }: { row: DSRRow }) {
  const right = rightOf(row.type)
  const closed = isClosed(row.status)
  const d = deadline(row.due_at)
  // The server decides whether a deadline has passed; the browser clock only
  // supplies the wording. A laptop an hour behind must not turn an overdue
  // request into a comfortable one.
  const overdue = row.overdue || d.overdue

  return (
    <Tr className={closed ? 'text-muted' : overdue ? 'bg-overdue/5' : ''}>
      <Td className="font-mono text-meta">{shortId(row.id)}</Td>
      <Td>
        <div className={`font-mono text-meta ${row.type === 'erase' ? 'text-legal' : ''}`}>
          {row.type}
        </div>
        <div className="id-chip">{right.label}</div>
      </Td>
      <Td>
        <div className="text-body">{maskIdentifier(row.subject_identifier)}</div>
        <div className="id-chip">
          {row.verification_method ?? 'magic_link'} · đã xác minh · chủ thể{' '}
          {shortId(row.data_subject_id)}
        </div>
      </Td>
      <Td>
        <div className="font-mono text-meta">{row.status}</div>
        <div className="id-chip">
          {STATUS_LABEL[row.status] ?? row.status}
          {row.needs_human && !closed && ' · cần người xử lý'}
        </div>
      </Td>
      <Td className="text-meta">
        {row.projects && row.projects.length > 0 ? (
          row.projects.map((p) => (
            <div key={p.id} className="truncate">
              {p.name}
            </div>
          ))
        ) : (
          // Not "0 dự án": the endpoint does not report scope yet, and an empty
          // scope on an erasure request would be a dangerous thing to imply.
          <span className="id-chip">chưa xác định</span>
        )}
      </Td>
      <Td>
        <RemainingCell row={row} />
      </Td>
      <Td className="text-right">
        <Link to={`/compliance/dsr/${row.id}`} className="btn inline-block">
          {closed ? 'Xem' : 'Xử lý'}
        </Link>
      </Td>
    </Tr>
  )
}

/**
 * Time left, or -- once closed -- whether the answer came in time.
 *
 * These are two different questions and the column answers whichever one still
 * applies. Printing "quá hạn 3 ngày" next to a request that was fulfilled would
 * describe a breach that never happened; printing nothing at all would hide one
 * that did.
 */
function RemainingCell({ row }: { row: DSRRow }) {
  if (!isClosed(row.status)) {
    const d = deadline(row.due_at)
    const tone = row.overdue ? 'overdue' : d.tone
    return <StatusPill tone={tone}>{row.overdue && !d.overdue ? 'đã quá hạn' : d.text}</StatusPill>
  }

  if (row.answered_late === true) {
    return <StatusPill tone="overdue">trả lời muộn</StatusPill>
  }
  if (row.answered_late === false) {
    return <StatusPill tone="ok">đúng hạn</StatusPill>
  }
  // Derivable only when the API sends the closing timestamp. Otherwise say so:
  // claiming "đúng hạn" without evidence is the one wrong answer here.
  if (row.fulfilled_at) {
    const late = new Date(row.fulfilled_at).getTime() > new Date(row.due_at).getTime()
    return <StatusPill tone={late ? 'overdue' : 'ok'}>{late ? 'trả lời muộn' : 'đúng hạn'}</StatusPill>
  }
  return (
    <span className="id-chip" title="API chưa trả cờ answered_late cho yêu cầu đã đóng">
      chưa ghi nhận
    </span>
  )
}

/**
 * Open requests first, soonest deadline at the top; closed ones sink.
 *
 * The API already orders by `due_at`, which is the rule that matters, but it
 * interleaves closed requests with live ones when the queue is viewed with
 * "kể cả đã đóng". A finished request has no deadline left to miss and must not
 * push a live one down the page.
 */
function sortByUrgency(rows: DSRRow[]): DSRRow[] {
  return [...rows].sort((a, b) => {
    const ac = isClosed(a.status)
    const bc = isClosed(b.status)
    if (ac !== bc) return ac ? 1 : -1
    const at = new Date(a.due_at).getTime()
    const bt = new Date(b.due_at).getTime()
    return ac ? bt - at : at - bt
  })
}

function Chip({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`rounded border px-2 py-1 font-mono text-meta ${
        active ? 'border-line bg-ink text-white' : 'border-faint text-muted hover:bg-chrome'
      }`}
    >
      {children}
    </button>
  )
}
