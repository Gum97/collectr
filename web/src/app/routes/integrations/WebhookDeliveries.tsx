/**
 * Screen 2f (part 2) -- delivery history for one endpoint, and manual replay.
 *
 * This is the screen someone opens at 09:15 because the CRM is missing the
 * submission that arrived at 08:31. It therefore leads with the failure: what
 * the receiver answered, how many times we tried, and how long we waited before
 * giving up. What it deliberately does not show is the payload -- a delivery log
 * is a copy of personal data, and it is not a second place to read it from.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router'
import { api, type List } from '../../lib/api'
import { useMe } from '../../lib/session'
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
  dateTime,
  num,
} from '../../components/ui'
import { shortUrl } from './SecretOnce'

interface WebhookRow {
  id: string
  url: string
  events: string[]
  active: boolean
  include_answers: boolean
  consecutive_failures: number
  disabled_at: string | null
  disabled_reason: string
}

interface DeliveryRow {
  id: string
  event_type: string
  attempt: number
  status: string
  /** The four fields below are optional on purpose: the current API returns only
   *  id, event_type, attempt and status. They render as "—" until it returns
   *  more, which is honest -- an absent measurement is not a zero. */
  response_code?: number | null
  created_at?: string | null
  delivered_at?: string | null
  next_attempt_at?: string | null
  duration_ms?: number | null
}

const MAX_ATTEMPTS = 8

export function WebhookDeliveries() {
  const { projectId, webhookId } = useParams()
  const me = useMe()
  const qc = useQueryClient()
  const canManage = Boolean(me.data?.capabilities.includes('webhook.manage'))

  // There is no GET /api/v1/webhooks/{id}, so the endpoint's own details come
  // from the project list this screen was navigated from.
  const hooks = useQuery({
    queryKey: ['webhooks', projectId],
    queryFn: async () =>
      (await api.get<List<WebhookRow>>(`/api/v1/webhooks?project_id=${projectId}`)).data,
    enabled: Boolean(projectId) && canManage,
  })
  const hook = hooks.data?.find((h) => h.id === webhookId)

  const deliveries = useQuery({
    queryKey: ['webhook-deliveries', webhookId],
    queryFn: async () =>
      (await api.get<List<DeliveryRow>>(`/api/v1/webhooks/${webhookId}/deliveries`)).data,
    enabled: Boolean(webhookId) && canManage,
  })

  const replay = useMutation({
    mutationFn: (deliveryId: string) =>
      api.post<{ status: string }>(
        `/api/v1/webhooks/${webhookId}/deliveries/${deliveryId}/replay`,
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['webhook-deliveries', webhookId] }),
  })

  const rows = deliveries.data ?? []
  // Only show the caveat while it is true. Once the API carries response codes,
  // the note disappears on its own instead of lying about a fixed backend.
  const missingDetail =
    rows.length > 0 && rows.every((d) => d.response_code == null && !d.created_at)

  return (
    <div className="p-6">
      <PageHeader
        title="Lịch sử gửi"
        meta={
          <>
            /api/v1/webhooks/{'{id}'}/deliveries · 100 lần gần nhất
            {hook && ` · ${shortUrl(hook.url).text}`}
          </>
        }
        actions={
          <Link to={`/p/${projectId}/integrations/webhooks`} className="btn">
            ← Danh sách endpoint
          </Link>
        }
      />

      {!canManage && !me.isPending && (
        <Empty
          title="Bạn không có quyền xem nhật ký gửi"
          hint="Nhật ký này cần quyền webhook.manage vì nó cho biết bản ghi nào đã được đẩy ra hệ thống bên ngoài."
        />
      )}

      {canManage && (
        <>
          {hook?.disabled_at && (
            <div role="alert" className="mb-4 rounded border border-overdue bg-overdue/5 p-3">
              <p className="text-body font-semibold text-overdue">
                <span aria-hidden>✕ </span>Endpoint này đang bị vô hiệu hoá
              </p>
              <p className="mt-1 text-body">
                Hệ thống tự tắt sau 20 lần gửi thất bại liên tiếp
                {hook.disabled_reason ? ` (${hook.disabled_reason})` : ''}, từ{' '}
                {dateTime(hook.disabled_at)}. Trong lúc bị tắt,{' '}
                <span className="font-semibold">sự kiện mới không được xếp hàng chờ</span> — chúng
                không được giữ lại để gửi bù sau, nên khoảng thời gian này là một lỗ hổng dữ liệu
                ở phía nhận.
              </p>
              <p className="mt-1 text-body">
                <span className="font-semibold">Cách bật lại:</span> sửa cho endpoint trả 2xx,
                sau đó xoá endpoint và tạo lại ở màn danh sách. API hiện chưa có đường bật lại
                tại chỗ. Khi tạo lại bạn sẽ nhận khoá ký mới và phải cập nhật ở phía nhận.
              </p>
            </div>
          )}

          {deliveries.isPending && <Loading />}
          {deliveries.isError && (
            <ErrorBanner error={deliveries.error} retry={() => deliveries.refetch()} />
          )}

          {deliveries.data && deliveries.data.length === 0 && (
            <Empty
              title="Chưa có lần gửi nào"
              hint={
                hook
                  ? `Endpoint này đăng ký ${hook.events.join(', ')}. Chưa có sự kiện nào thuộc các loại đó phát sinh trong dự án kể từ khi endpoint được tạo — hoặc chúng đã bị dọn theo hạn lưu 30 ngày của nhật ký.`
                  : 'Chưa có sự kiện nào được xếp hàng cho endpoint này, hoặc nhật ký đã quá hạn lưu 30 ngày.'
              }
            />
          )}

          {deliveries.data && deliveries.data.length > 0 && (
            <Table
              head={
                <>
                  <Th>Thời điểm</Th>
                  <Th>Mã HTTP</Th>
                  <Th>Sự kiện</Th>
                  <Th className="text-right">Lần thử</Th>
                  <Th className="text-right">Độ trễ</Th>
                  <Th>Kết quả</Th>
                  <Th className="text-right">Thao tác</Th>
                </>
              }
            >
              {deliveries.data.map((d) => (
                <DeliveryLine
                  key={d.id}
                  delivery={d}
                  replaying={replay.isPending && replay.variables === d.id}
                  onReplay={() => replay.mutate(d.id)}
                />
              ))}
            </Table>
          )}

          {replay.isError && (
            <div className="mt-3">
              <ErrorBanner error={replay.error} />
            </div>
          )}
          {replay.isSuccess && (
            <p role="status" className="mt-3 text-body text-ok">
              Đã xếp lại vào hàng đợi. Lần thử được đặt về 0 và worker sẽ gửi trong vài giây;
              tải lại trang để xem kết quả.
            </p>
          )}

          {missingDetail && (
            <p role="status" className="mt-3 text-meta text-muted">
              Cột <span className="font-semibold">Thời điểm</span>,{' '}
              <span className="font-semibold">Mã HTTP</span> và{' '}
              <span className="font-semibold">Độ trễ</span> đang hiện “—” vì API mới chỉ trả về
              loại sự kiện, số lần thử và trạng thái. Đây là thiếu dữ liệu, không phải giá trị 0.
            </p>
          )}

          <div className="mt-4">
            <ReplayHelp />
          </div>
        </>
      )}
    </div>
  )
}

function DeliveryLine({
  delivery,
  replaying,
  onReplay,
}: {
  delivery: DeliveryRow
  replaying: boolean
  onReplay: () => void
}) {
  const d = delivery
  // Only 'failed' and 'dead' can be re-queued; the API answers 404 for anything
  // else, so the button is not offered for them.
  const replayable = d.status === 'dead' || d.status === 'failed'

  return (
    <Tr>
      <Td className="whitespace-nowrap font-mono text-meta">
        {dateTime(d.delivered_at ?? d.created_at)}
      </Td>

      <Td className="font-mono text-meta">
        {d.response_code == null ? (
          <span className="text-muted">—</span>
        ) : (
          <span className={d.response_code >= 400 || d.response_code === 0 ? 'text-overdue' : ''}>
            {d.response_code === 0 ? 'không phản hồi' : d.response_code}
          </span>
        )}
      </Td>

      <Td className="font-mono text-meta">{d.event_type}</Td>

      <Td className="whitespace-nowrap text-right font-mono text-meta">
        {num(d.attempt)}/{MAX_ATTEMPTS}
      </Td>

      <Td className="whitespace-nowrap text-right font-mono text-meta">{latency(d)}</Td>

      <Td>
        <DeliveryStatus status={d.status} />
        {d.status === 'pending' && d.next_attempt_at && (
          <div className="id-chip mt-0.5">thử lại lúc {dateTime(d.next_attempt_at)}</div>
        )}
      </Td>

      <Td className="whitespace-nowrap text-right">
        {replayable ? (
          <button type="button" className="btn" onClick={onReplay} disabled={replaying}>
            {replaying ? 'Đang xếp…' : 'Gửi lại'}
          </button>
        ) : (
          <span className="id-chip">
            {d.status === 'delivered' ? 'đã nhận' : 'đang trong hàng đợi'}
          </span>
        )}
      </Td>
    </Tr>
  )
}

function DeliveryStatus({ status }: { status: string }) {
  switch (status) {
    case 'delivered':
      return <StatusPill tone="ok">✓ đã gửi</StatusPill>
    case 'pending':
      return <StatusPill tone="neutral">○ đang chờ</StatusPill>
    case 'failed':
      return <StatusPill tone="duesoon">△ thất bại</StatusPill>
    case 'dead':
      return <StatusPill tone="overdue">✕ đã bỏ cuộc</StatusPill>
    default:
      return <StatusPill tone="neutral">{status}</StatusPill>
  }
}

/** Round-trip time, from whichever pair of fields the API gave us. */
function latency(d: DeliveryRow): string {
  const ms =
    d.duration_ms ??
    (d.created_at && d.delivered_at
      ? new Date(d.delivered_at).getTime() - new Date(d.created_at).getTime()
      : null)

  // No measurement is an em dash, never "0 ms": one means the API did not tell
  // us, the other means the receiver answered instantly.
  if (ms == null || Number.isNaN(ms)) return '—'
  if (ms < 1000) return `${num(Math.round(ms))} ms`
  return `${(ms / 1000).toFixed(1)} s`
}

function ReplayHelp() {
  return (
    <Card title="Gửi lại một lần giao">
      <ul className="space-y-1.5 text-body">
        <li>
          Chỉ những lần giao ở trạng thái <span className="font-mono text-meta">failed</span>{' '}
          hoặc <span className="font-mono text-meta">dead</span> mới gửi lại được. Một lần đã
          nhận thành công không gửi lại được từ đây — làm vậy là tạo bản ghi trùng ở phía nhận.
        </li>
        <li>
          Gửi lại đặt số lần thử về 0 và xếp lại vào hàng đợi ngay. Nội dung{' '}
          <span className="font-semibold">giữ nguyên như lúc sự kiện xảy ra</span>, không dựng
          lại từ dữ liệu hiện tại.
        </li>
        <li>
          Header <span className="font-mono text-meta">X-Collectr-Delivery</span> không đổi,
          nên bên nhận có khử trùng lặp đúng cách sẽ tự bỏ qua nếu họ đã xử lý rồi.
        </li>
        <li>
          Nhật ký chỉ giữ 30 ngày. Quá hạn đó thì không còn gì để gửi lại — đây là hạn lưu, cùng
          nguyên tắc với dữ liệu cá nhân trong lượt gửi biểu mẫu.
        </li>
      </ul>
    </Card>
  )
}
