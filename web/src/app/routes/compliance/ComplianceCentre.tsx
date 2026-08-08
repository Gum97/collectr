/**
 * 1e -- the organisation-wide compliance centre, as the DPO sees it.
 *
 * Everything here crosses every project, because the duty does: a request to be
 * erased does not arrive addressed to a marketing campaign. The role that works
 * this screen holds `audit.read`, `dsr.handle`, `consent.manage` and
 * `submission.read` and deliberately holds neither `submission.export` nor
 * `form.write` -- it supervises, it does not operate. The screen reflects that by
 * not rendering controls the role cannot use, rather than showing them disabled.
 */
import { useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RequestFailed, api, type List } from '../../lib/api'
import { retentionLabel, useProjects } from '../../lib/projects'
import { can, useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Loading,
  PageHeader,
  StatusPill,
  Table,
  Td,
  Th,
  Tr,
  date,
  dateTime,
  deadline,
  num,
} from '../../components/ui'
import { subjectLabel, type IdentifierKind } from './mask'
import { ConsentDocuments } from './ConsentDocuments'
import { Purposes, usePurposes, legalBasisLabel, purposeRequired } from './Purposes'

export interface DsrRequest {
  id: string
  type: string
  status: string
  received_at: string
  due_at: string
  overdue: boolean
  hours_remaining: number
  note?: string | null
  data_subject_id: string
  needs_human: boolean
  /** Masked or not, whatever arrives is masked again before display. The admin
   *  endpoint returns no identifier today -- see the report accompanying this
   *  screen -- so these stay optional. */
  subject_identifier?: string | null
  subject_identifier_kind?: IdentifierKind | null
  project_id?: string | null
  project_name?: string | null
  form_id?: string | null
  form_title?: string | null
  fulfilled_at?: string | null
}

interface DsrListResponse {
  data: DsrRequest[]
  overdue_count?: number
}

interface VerifyResult {
  tenant_id: string
  entries: number
  valid: boolean
  broken_at?: number
  reason?: string
}

interface AuditEntry {
  seq: number
  occurred_at: string
  action: string
  actor?: { type?: string; id?: string; ip_prefix?: string } | null
  target?: Record<string, unknown> | null
}

const TAB_IDS = ['requests', 'documents', 'purposes', 'retention', 'audit'] as const
type TabID = (typeof TAB_IDS)[number]

const TAB_LABELS: Record<TabID, string> = {
  requests: 'Yêu cầu',
  documents: 'Văn bản đồng ý',
  purposes: 'Mục đích',
  retention: 'Lưu trữ',
  audit: 'Nhật ký',
}

/**
 * useDsrRequests loads the queue.
 *
 * `status=all` because a closed request is part of the record: a DPO checking
 * whether a deadline was met needs the ones that were answered, not only the
 * ones still waiting.
 */
export function useDsrRequests() {
  const me = useMe()
  return useQuery({
    queryKey: ['dsr-requests'],
    queryFn: () => api.get<DsrListResponse>('/api/v1/dsr/requests?status=all&limit=200'),
    enabled: can(me.data, 'dsr.handle'),
    // These are legal deadlines counted in hours. Half an hour of staleness is
    // the most this screen should ever show.
    staleTime: 30_000,
  })
}

export function requestTypeLabel(type: string): string {
  switch (type) {
    case 'access':
      return 'Truy cập dữ liệu'
    case 'rectify':
      return 'Chỉnh sửa'
    case 'erase':
      return 'Yêu cầu xóa'
    case 'restrict':
      return 'Hạn chế xử lý'
    case 'withdraw':
      return 'Rút đồng ý'
    case 'export':
      return 'Xuất dữ liệu (chủ thể)'
    case 'object':
      return 'Phản đối xử lý'
    default:
      return type
  }
}

export function isClosed(req: DsrRequest): boolean {
  return req.status === 'fulfilled' || req.status === 'rejected'
}

/** Open requests first, nearest deadline at the top -- overdue ones therefore
 *  lead the list without needing a separate rule. Closed ones follow, newest
 *  first, as history rather than work. */
export function sortRequests(rows: DsrRequest[]): DsrRequest[] {
  const open = rows.filter((r) => !isClosed(r))
  const closed = rows.filter(isClosed)
  open.sort((a, b) => Date.parse(a.due_at) - Date.parse(b.due_at))
  closed.sort((a, b) => Date.parse(b.received_at) - Date.parse(a.received_at))
  return [...open, ...closed]
}

export function ComplianceCentre() {
  const me = useMe()
  const [params, setParams] = useSearchParams()
  const [windowDays, setWindowDays] = useState(30)

  // Counted from the append-only record table, so a subject who withdrew and
  // later granted again still shows up. current_consents is overwritten and
  // would report that person as never having withdrawn at all.
  const withdrawals = useQuery({
    queryKey: ['consent-withdrawals', windowDays],
    queryFn: () =>
      api.get<{ total: number; by_purpose: Record<string, number> }>(
        `/api/v1/consent/withdrawals?days=${windowDays}`,
      ),
    enabled: can(me.data, 'consent.manage'),
  })

  const raw = params.get('tab')
  const tab: TabID = (TAB_IDS as readonly string[]).includes(raw ?? '') ? (raw as TabID) : 'requests'

  function selectTab(next: TabID) {
    const q = new URLSearchParams(params)
    q.set('tab', next)
    // The composer flag belongs to the tab that opened it.
    q.delete('new')
    setParams(q, { replace: true })
  }

  const mayHandle = can(me.data, 'dsr.handle')
  const mayAudit = can(me.data, 'audit.read')
  const mayExport = can(me.data, 'submission.export')

  const requests = useDsrRequests()
  const rows = requests.data?.data ?? []
  const open = rows.filter((r) => !isClosed(r))
  const overdue = open.filter((r) => r.overdue)

  const chain = useQuery({
    queryKey: ['audit-verify'],
    queryFn: () => api.post<VerifyResult>('/api/v1/audit/verify'),
    enabled: mayAudit,
    // Verification walks the whole chain. It is a deliberate act, not something
    // to re-run every time the tab regains focus.
    staleTime: 5 * 60_000,
    refetchOnWindowFocus: false,
  })

  return (
    <div className="p-6">
      <PageHeader
        title="Trung tâm tuân thủ"
        meta={
          <>
            vai trò: {me.data?.org_role ?? '—'} · xuyên mọi dự án
            {/* Stated, because it explains why this screen has no export control
                anywhere on it. */}
            {!mayExport && ' · không có quyền xuất'}
          </>
        }
        actions={
          <div className="flex items-center gap-1.5">
            <label htmlFor="window" className="font-mono text-meta tracking-caps text-faint">
              KHOẢNG
            </label>
            <select
              id="window"
              className="input w-auto py-1 text-body"
              value={windowDays}
              onChange={(e) => setWindowDays(Number(e.target.value))}
            >
              <option value={7}>7 ngày</option>
              <option value={30}>30 ngày</option>
              <option value={90}>90 ngày</option>
            </select>
          </div>
        }
      />

      <div className="mb-4 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <Metric
          label="QUÁ HẠN SLA"
          // An unreadable queue is an em dash, never a zero: "chưa đếm được" and
          // "không có yêu cầu nào quá hạn" call for opposite reactions.
          value={queueMetric(mayHandle, requests, overdue.length)}
          tone={overdue.length > 0 ? 'overdue' : 'neutral'}
          hint={
            !mayHandle
              ? 'cần quyền dsr.handle'
              : requests.isError
                ? 'không tải được hàng đợi'
                : overdue.length > 0
                  ? 'phải xử lý ngay'
                  : 'không có yêu cầu nào quá hạn'
          }
        />
        <Metric
          label="DSR ĐANG MỞ"
          value={queueMetric(mayHandle, requests, open.length)}
          hint={
            !mayHandle
              ? 'cần quyền dsr.handle'
              : requests.isError
                ? 'không tải được hàng đợi'
                : `${num(rows.length)} yêu cầu tính cả đã đóng`
          }
        />
        <Metric
          label="RÚT ĐỒNG Ý"
          // Still a dash while the request is in flight or has failed. Zero and
          // "nobody counted" lead to opposite conclusions about whether a
          // purpose is being pushed too hard, so the screen never guesses one
          // for the other.
          value={withdrawals.data ? num(withdrawals.data.total) : '—'}
          hint={
            withdrawals.isPending
              ? 'đang đếm…'
              : withdrawals.error
                ? 'không đọc được số lượt rút'
                : `trong ${windowDays} ngày`
          }
        />
        <Metric
          label="CHUỖI HASH"
          value={
            !mayAudit
              ? '—'
              : chain.isPending
                ? '…'
                : chain.data
                  ? chain.data.valid
                    ? 'Nguyên vẹn ✓'
                    : `Gãy tại #${num(chain.data.broken_at ?? 0)}`
                  : 'không kiểm được'
          }
          tone={chain.data && !chain.data.valid ? 'overdue' : 'neutral'}
          hint={
            !mayAudit
              ? 'cần quyền audit.read'
              : chain.data
                ? (chain.data.reason ?? `${num(chain.data.entries)} bản ghi đã đối chiếu`)
                : chain.isError
                  ? 'không chạy được kiểm tra'
                  : undefined
          }
          action={
            mayAudit && !chain.isFetching ? (
              <button type="button" className="btn px-2 py-0.5 text-meta" onClick={() => void chain.refetch()}>
                Kiểm lại
              </button>
            ) : undefined
          }
        />
      </div>

      <nav
        aria-label="Khu vực tuân thủ"
        className="mb-3 flex flex-wrap gap-4 border-b border-line pb-1.5 text-meta font-semibold"
      >
        {TAB_IDS.map((id) => (
          <button
            key={id}
            type="button"
            onClick={() => selectTab(id)}
            aria-current={tab === id ? 'page' : undefined}
            className={
              tab === id
                ? 'border-b-2 border-line pb-1 text-ink'
                : 'border-b-2 border-transparent pb-1 text-muted hover:text-ink'
            }
          >
            {TAB_LABELS[id]}
          </button>
        ))}
      </nav>

      {tab === 'requests' && <RequestsTab windowDays={windowDays} />}
      {tab === 'documents' && <ConsentDocuments />}
      {tab === 'purposes' && <Purposes />}
      {tab === 'retention' && <RetentionTab />}
      {tab === 'audit' && <AuditTab />}

      <p className="mt-3 id-chip">
        Định danh chủ thể dữ liệu luôn hiện ở dạng che một phần. Màn hình này mở cả ngày trong văn
        phòng.
      </p>
    </div>
  )
}

/** A count off the DSR queue, or the reason there is no count. */
function queueMetric(
  allowed: boolean,
  query: { data?: DsrListResponse; isError: boolean },
  value: number,
): string {
  if (!allowed || query.isError) return '—'
  return query.data ? num(value) : '…'
}

function Metric({
  label,
  value,
  hint,
  tone = 'neutral',
  action,
}: {
  label: string
  value: ReactNode
  hint?: ReactNode
  tone?: 'neutral' | 'overdue'
  action?: ReactNode
}) {
  const frame =
    tone === 'overdue' ? 'border-overdue bg-overdue/5 text-overdue' : 'border-line bg-surface'
  return (
    <div className={`rounded border p-2.5 ${frame}`}>
      <div className="flex items-baseline justify-between gap-2">
        <span className="font-mono text-meta tracking-caps opacity-70">{label}</span>
        {action}
      </div>
      <div className="text-[17px] font-semibold leading-tight">{value}</div>
      {hint && <div className="id-chip mt-0.5 leading-tight">{hint}</div>}
    </div>
  )
}

function RequestsTab({ windowDays }: { windowDays: number }) {
  const me = useMe()
  const mayHandle = can(me.data, 'dsr.handle')
  const requests = useDsrRequests()
  const [expanded, setExpanded] = useState<string | null>(null)

  if (!mayHandle) {
    return (
      <Empty
        title="Không có quyền xem hàng đợi yêu cầu"
        hint="Danh sách yêu cầu của chủ thể dữ liệu cần quyền dsr.handle. Vai trò dpo, owner và admin có quyền này."
      />
    )
  }
  if (requests.isPending) return <Loading label="Đang tải hàng đợi…" />
  if (requests.isError) {
    return <ErrorBanner error={requests.error} retry={() => void requests.refetch()} />
  }

  const all = sortRequests(requests.data.data)
  const cutoff = Date.now() - windowDays * 86_400_000
  // Open requests are always shown, however old: hiding an overdue request
  // because it fell outside a time window is exactly the failure this screen
  // exists to prevent. The window only trims the closed history.
  const rows = all.filter((r) => !isClosed(r) || Date.parse(r.received_at) >= cutoff)

  if (rows.length === 0) {
    return (
      <Empty
        title="Không có yêu cầu nào"
        hint={`Chưa có chủ thể dữ liệu nào gửi yêu cầu trong ${windowDays} ngày qua, và không có yêu cầu nào đang mở. Chủ thể gửi yêu cầu qua cổng tự phục vụ, không qua màn hình này.`}
      />
    )
  }

  return (
    <ul className="flex flex-col gap-1.5">
      {rows.map((req) => (
        <RequestRow
          key={req.id}
          req={req}
          mayHandle={mayHandle}
          expanded={expanded === req.id}
          onToggle={() => setExpanded(expanded === req.id ? null : req.id)}
        />
      ))}
    </ul>
  )
}

function RequestRow({
  req,
  mayHandle,
  expanded,
  onToggle,
}: {
  req: DsrRequest
  mayHandle: boolean
  expanded: boolean
  onToggle: () => void
}) {
  const qc = useQueryClient()
  const [note, setNote] = useState('')

  const resolve = useMutation({
    mutationFn: (action: 'fulfill' | 'reject') =>
      api.post<{ id: string; status: string }>(`/api/v1/dsr/requests/${req.id}/${action}`, { note }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['dsr-requests'] })
    },
  })

  const closed = isClosed(req)
  const due = deadline(req.due_at)
  const subject = subjectLabel(req.data_subject_id, req.subject_identifier, req.subject_identifier_kind)
  const scope = req.form_title ?? req.project_name ?? null

  const frame = closed
    ? 'border border-dashed border-line text-muted'
    : due.overdue
      ? 'border border-overdue bg-overdue/5'
      : 'border border-line bg-surface'

  const fields = resolve.error instanceof RequestFailed ? resolve.error.fields : {}

  return (
    <li className={`rounded ${frame}`}>
      <div className="flex items-center gap-2.5 p-2.5">
        <div className="min-w-0 flex-1">
          <div className="text-meta font-semibold">
            {requestTypeLabel(req.type)} · {subject}
          </div>
          <div className={`id-chip ${!closed && due.overdue ? 'text-overdue' : ''}`}>
            {req.type} ·{' '}
            {closed
              ? `${req.status === 'fulfilled' ? 'đã hoàn tất' : 'đã từ chối'}${
                  req.fulfilled_at ? ` ${date(req.fulfilled_at)}` : ''
                }`
              : due.text}
            {scope ? ` · ${scope}` : ''}
          </div>
        </div>

        {!closed && (
          <>
            {/* Colour is never the only signal: the state is spelled out here as
                well as in the frame. */}
            <StatusPill tone={due.tone}>{due.text}</StatusPill>
            <button
              type="button"
              className={req.needs_human && mayHandle ? 'btn-primary' : 'btn'}
              aria-expanded={expanded}
              onClick={onToggle}
            >
              {req.needs_human && mayHandle ? 'Xử lý' : 'Mở'}
            </button>
          </>
        )}
      </div>

      {expanded && !closed && (
        <div className="border-t border-line p-2.5">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-meta sm:grid-cols-4">
            <Detail label="Nhận lúc">{dateTime(req.received_at)}</Detail>
            <Detail label="Hạn trả lời">{dateTime(req.due_at)}</Detail>
            <Detail label="Trạng thái">{req.status}</Detail>
            <Detail label="Mã chủ thể">
              <span className="id-chip">{req.data_subject_id}</span>
            </Detail>
          </dl>

          {req.note && <p className="mt-2 text-meta">Ghi chú: {req.note}</p>}

          {!req.needs_human && (
            <p className="mt-2 text-meta text-muted">
              Loại yêu cầu này được worker xử lý tự động; can thiệp tay chỉ khi nó không tự hoàn tất.
            </p>
          )}

          {mayHandle ? (
            <div className="mt-3 flex flex-col gap-2">
              <label htmlFor={`note-${req.id}`} className="text-meta font-semibold">
                Ghi chú xử lý
                <span className="font-normal text-muted"> — bắt buộc khi từ chối</span>
              </label>
              <textarea
                id={`note-${req.id}`}
                className="input min-h-16 text-body"
                value={note}
                onChange={(e) => setNote(e.target.value)}
              />
              {fields.note && (
                <span role="alert" className="text-meta text-overdue">
                  {fields.note}
                </span>
              )}
              {resolve.isError && !fields.note && <ErrorBanner error={resolve.error} />}
              <div className="flex gap-2">
                <button
                  type="button"
                  className="btn-primary"
                  disabled={resolve.isPending}
                  onClick={() => resolve.mutate('fulfill')}
                >
                  Đáp ứng
                </button>
                <button
                  type="button"
                  className="btn"
                  disabled={resolve.isPending}
                  onClick={() => resolve.mutate('reject')}
                >
                  Từ chối
                </button>
              </div>
              <p className="id-chip">
                Cả hai lựa chọn đều ghi vào nhật ký bất biến, kèm việc trả lời có đúng hạn hay không.
              </p>
            </div>
          ) : (
            <p className="mt-2 text-meta text-muted">
              Chỉ vai trò có dsr.handle mới đóng được yêu cầu.
            </p>
          )}
        </div>
      )}
    </li>
  )
}

function Detail({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="font-mono text-meta tracking-caps text-faint">{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}

/** Retention across the organisation: what is kept, for how long, decided where. */
function RetentionTab() {
  const projects = useProjects()
  const purposes = usePurposes()

  return (
    <div className="flex flex-col gap-3">
      <Card title="Hạn lưu mặc định theo dự án" aside={`${num(projects.data?.length ?? 0)} dự án`}>
        {projects.isPending && <Loading />}
        {projects.isError && <ErrorBanner error={projects.error} />}
        {projects.data && projects.data.length === 0 && (
          <Empty title="Chưa có dự án nào" hint="Hạn lưu được đặt ở cấp dự án và kế thừa xuống biểu mẫu." />
        )}
        {projects.data && projects.data.length > 0 && (
          <Table
            head={
              <>
                <Th>Dự án</Th>
                <Th>Hạn lưu mặc định</Th>
                <Th>Truy cập của bạn</Th>
              </>
            }
          >
            {projects.data.map((p) => (
              <Tr key={p.id}>
                <Td>
                  <div className="font-semibold">{p.name}</div>
                  <div className="id-chip">{p.slug}</div>
                </Td>
                <Td>
                  {p.default_retention_days ? (
                    retentionLabel(p.default_retention_days)
                  ) : (
                    // An unset retention is a finding, not a blank cell: data with
                    // no expiry keeps itself forever.
                    <StatusPill tone="duesoon">chưa đặt hạn lưu</StatusPill>
                  )}
                </Td>
                <Td className="text-meta text-muted">
                  {p.access === 'none' ? 'không truy cập được' : (p.my_role || 'qua vai trò tổ chức')}
                </Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>

      <Card title="Hạn lưu theo mục đích xử lý">
        {purposes.isPending && <Loading />}
        {purposes.isError && <ErrorBanner error={purposes.error} />}
        {purposes.data && purposes.data.length === 0 && (
          <Empty
            title="Chưa khai báo mục đích nào"
            hint="Hạn lưu riêng theo mục đích được đặt ở tab Mục đích."
          />
        )}
        {purposes.data && purposes.data.length > 0 && (
          <Table
            head={
              <>
                <Th>Mục đích</Th>
                <Th>Căn cứ</Th>
                <Th>Hạn lưu</Th>
              </>
            }
          >
            {purposes.data.map((p) => (
              <Tr key={p.id}>
                <Td>
                  <span className="font-semibold">{p.name}</span>{' '}
                  <span className="id-chip">
                    {p.code} · {purposeRequired(p) ? 'bắt buộc' : 'tùy chọn'}
                  </span>
                </Td>
                <Td className="text-meta">{legalBasisLabel(p.legal_basis)}</Td>
                <Td className="whitespace-nowrap text-meta">
                  {retentionLabel(p.retention_days ?? null)}
                </Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>

      <Card title="Cách xóa được thực hiện">
        <ul className="flex list-disc flex-col gap-1 pl-4 text-body">
          <li>
            Hạn xóa của một bản ghi được tính <span className="font-semibold">tại thời điểm gửi</span>.
            Đổi chính sách hôm nay không hồi tố lên dữ liệu cũ, trừ khi chạy re-apply.
          </li>
          <li>
            Field nhạy cảm và file đính kèm mã hóa bằng khóa riêng của từng chủ thể — xóa khóa là
            crypto-shred, ciphertext trong mọi bản backup thành rác.
          </li>
          <li>
            Phần còn lại xóa cứng; backup còn giữ tối đa 30 ngày và điều đó được ghi trong chính sách.
          </li>
          <li>
            Bản ghi đồng ý và nhật ký audit <span className="font-semibold">được giữ lại</span> nhưng
            đã cắt liên kết tới người thật — nghĩa vụ chứng minh vẫn còn, khả năng truy ngược thì không.
          </li>
        </ul>
      </Card>
    </div>
  )
}

/**
 * The audit trail itself.
 *
 * Not scoped by the window control at the top: `GET /api/v1/audit` takes a
 * cursor, not a date range, and passing a parameter the API ignores would put a
 * "30 ngày" label over a list that is nothing of the sort.
 */
function AuditTab() {
  const me = useMe()
  const mayAudit = can(me.data, 'audit.read')

  const entries = useQuery({
    queryKey: ['audit-entries'],
    queryFn: async () => (await api.get<List<AuditEntry>>('/api/v1/audit?limit=50')).data,
    enabled: mayAudit,
  })

  if (!mayAudit) {
    return (
      <Empty
        title="Không có quyền đọc nhật ký"
        hint="Nhật ký audit cần quyền audit.read. Người bị giám sát không tự đọc được nhật ký giám sát mình — đó là lý do quyền này tách riêng."
      />
    )
  }
  if (entries.isPending) return <Loading />
  if (entries.isError) {
    const missing = entries.error instanceof RequestFailed && entries.error.status === 404
    if (missing) {
      return (
        <Empty
          title="Chưa đọc được nhật ký"
          hint="GET /api/v1/audit chưa được triển khai ở API. Việc kiểm tra tính toàn vẹn chuỗi hash (ô CHUỖI HASH ở trên) đã hoạt động."
        />
      )
    }
    return <ErrorBanner error={entries.error} retry={() => void entries.refetch()} />
  }
  if (entries.data.length === 0) {
    return (
      <Empty
        title="Chưa có bản ghi nào trong nhật ký"
        hint="Mọi hành động trên dữ liệu cá nhân đều được ghi; danh sách rỗng nghĩa là chưa có hành động nào."
      />
    )
  }

  return (
    <Table
      head={
        <>
          <Th>#</Th>
          <Th>Thời điểm</Th>
          <Th>Hành động</Th>
          <Th>Người thực hiện</Th>
          <Th>Đối tượng</Th>
        </>
      }
    >
      {entries.data.map((e) => (
        <Tr key={e.seq}>
          <Td className="font-mono text-meta">{e.seq}</Td>
          <Td className="whitespace-nowrap">{dateTime(e.occurred_at)}</Td>
          <Td className="font-mono text-meta">{e.action}</Td>
          <Td>
            <div className="text-meta">{e.actor?.type ?? '—'}</div>
            <div className="id-chip">{e.actor?.id ?? ''}</div>
          </Td>
          <Td className="id-chip">{e.target ? JSON.stringify(e.target) : '—'}</Td>
        </Tr>
      ))}
    </Table>
  )
}
