/**
 * Screen 2f (part 1) -- webhook endpoints for one project.
 *
 * A webhook is the one place in this product where personal data can be pushed
 * out to a system nobody here operates, so the screen is built around three
 * facts rather than around the form: what leaves (metadata only, unless the
 * operator opts in and says so out loud), who can prove it came from us (the
 * signing secret, shown once), and why an endpoint stopped receiving.
 */
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router'
import { api, RequestFailed, type List } from '../../lib/api'
import { useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Field,
  Loading,
  PageHeader,
  StatusPill,
  Table,
  Td,
  Th,
  Tr,
} from '../../components/ui'
import { SecretOnce, shortUrl } from './SecretOnce'

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

interface CreatedWebhook {
  id: string
  url: string
  events: string[]
  include_answers: boolean
  /** Present only on the create response. Never returned by the list. */
  secret: string
  warning?: string
}

/** The subscribable events, copied from internal/modules/webhooks/domain.
 *
 * Hard-coded because the API does not publish the list; a typo here becomes a
 * 422 at create time rather than a silent subscription to nothing. */
const EVENTS: { code: string; label: string; note?: string }[] = [
  { code: 'submission.created', label: 'Có lượt gửi biểu mẫu mới' },
  { code: 'submission.updated', label: 'Lượt gửi được sửa lại' },
  { code: 'form.published', label: 'Biểu mẫu xuất bản version mới' },
  { code: 'link.created', label: 'Có link rút gọn mới' },
  {
    code: 'consent.withdrawn',
    label: 'Chủ thể rút đồng ý',
    note: 'Quan trọng nhất về tuân thủ: hệ thống nhận được sự kiện này phải dừng xử lý ngay.',
  },
  {
    code: 'dsr.received',
    label: 'Có yêu cầu của chủ thể dữ liệu',
    note: 'Cho phép hệ thống phía sau khoá bản ghi trước khi hết hạn 72h.',
  },
  { code: 'export.ready', label: 'Tệp xuất đã sẵn sàng' },
]

const DISABLE_AFTER_FAILURES = 20

export function Webhooks() {
  const { projectId } = useParams()
  const me = useMe()
  const qc = useQueryClient()
  const [created, setCreated] = useState<CreatedWebhook | null>(null)
  const [composing, setComposing] = useState(false)

  const canManage = Boolean(me.data?.capabilities.includes('webhook.manage'))

  const hooks = useQuery({
    queryKey: ['webhooks', projectId],
    queryFn: async () =>
      (await api.get<List<WebhookRow>>(`/api/v1/webhooks?project_id=${projectId}`)).data,
    enabled: Boolean(projectId) && canManage,
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.del<void>(`/api/v1/webhooks/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['webhooks', projectId] }),
  })

  return (
    <div className="p-6">
      <PageHeader
        title="Webhook"
        meta={
          <>
            /api/v1/webhooks · cần quyền webhook.manage
            {hooks.data && ` · ${hooks.data.length} endpoint`}
          </>
        }
        actions={
          canManage && (
            <button type="button" className="btn-primary" onClick={() => setComposing((v) => !v)}>
              {composing ? 'Huỷ' : '+ Endpoint mới'}
            </button>
          )
        }
      />

      {!canManage && !me.isPending && (
        <Empty
          title="Bạn không có quyền xem webhook"
          hint="Cấu hình webhook cần quyền webhook.manage. Endpoint webhook quyết định dữ liệu cá nhân đi ra đâu, nên quyền này chỉ dành cho vai trò quản trị. Hãy nhờ Admin của tổ chức cấp."
        />
      )}

      {created && (
        <div className="mb-4">
          <SecretOnce
            title="Khoá ký webhook"
            value={created.secret}
            what={`Bên nhận dùng khoá này để xác minh mỗi lần gửi tới ${created.url} thực sự đến từ Collectr.`}
            extra={
              <>
                {created.warning && (
                  <p className="mb-1 font-semibold text-accent">{created.warning}</p>
                )}
                Cách dùng ở phần “Xác minh chữ ký” bên dưới. Muốn đổi khoá thì hiện phải xoá
                endpoint và tạo lại — chưa có chức năng xoay khoá tại chỗ.
              </>
            }
            onDone={() => setCreated(null)}
          />
        </div>
      )}

      {composing && canManage && (
        <div className="mb-4">
          <CreateForm
            projectId={projectId ?? ''}
            onCreated={(w) => {
              setCreated(w)
              setComposing(false)
              qc.invalidateQueries({ queryKey: ['webhooks', projectId] })
            }}
          />
        </div>
      )}

      {canManage && (
        <>
          {hooks.isPending && <Loading />}
          {hooks.isError && <ErrorBanner error={hooks.error} retry={() => hooks.refetch()} />}

          {hooks.data && hooks.data.length === 0 && (
            <Empty
              title="Dự án này chưa có endpoint nào"
              hint="Chưa có hệ thống bên ngoài nào được đăng ký nhận sự kiện. Không có endpoint nghĩa là không có dữ liệu nào của dự án rời khỏi Collectr qua đường webhook."
            />
          )}

          {hooks.data && hooks.data.length > 0 && (
            <Table
              head={
                <>
                  <Th>Đích đến</Th>
                  <Th>Sự kiện đăng ký</Th>
                  <Th>Nội dung gửi</Th>
                  <Th>Trạng thái</Th>
                  <Th className="text-right">Thao tác</Th>
                </>
              }
            >
              {hooks.data.map((h) => (
                <HookRow
                  key={h.id}
                  hook={h}
                  projectId={projectId ?? ''}
                  onDelete={() => {
                    if (
                      window.confirm(
                        `Xoá endpoint ${h.url}?\n\nLịch sử gửi của nó cũng mất theo, và các sự kiện đang chờ sẽ không được gửi nữa.`,
                      )
                    ) {
                      remove.mutate(h.id)
                    }
                  }}
                />
              ))}
            </Table>
          )}

          {remove.isError && (
            <div className="mt-3">
              <ErrorBanner error={remove.error} />
            </div>
          )}

          <div className="mt-4 grid gap-4 lg:grid-cols-2">
            <SignatureHelp />
            <DeliveryRules />
          </div>
        </>
      )}
    </div>
  )
}

function HookRow({
  hook,
  projectId,
  onDelete,
}: {
  hook: WebhookRow
  projectId: string
  onDelete: () => void
}) {
  const url = shortUrl(hook.url)
  const disabled = Boolean(hook.disabled_at)

  return (
    <Tr>
      <Td>
        <div className="break-all font-mono text-meta">{url.text}</div>
        {url.redacted && (
          // Not an aesthetic truncation: a token in the query string is a
          // credential, and this table gets screenshotted into chat.
          <div className="id-chip">giá trị tham số đã ẩn — URL đầy đủ chỉ nằm ở cấu hình</div>
        )}
      </Td>

      <Td>
        <div className="flex flex-wrap gap-1">
          {hook.events.map((e) => (
            <span key={e} className="id-chip rounded border border-line px-1 py-0.5">
              {e}
            </span>
          ))}
        </div>
      </Td>

      <Td>
        {hook.include_answers ? (
          <StatusPill tone="accent">◆ có câu trả lời</StatusPill>
        ) : (
          <StatusPill tone="neutral">chỉ id + metadata</StatusPill>
        )}
      </Td>

      <Td>
        {disabled ? (
          <>
            <StatusPill tone="overdue">✕ bị vô hiệu hoá</StatusPill>
            <div className="mt-1 max-w-xs text-meta text-overdue">
              Tự tắt sau {DISABLE_AFTER_FAILURES} lần gửi thất bại liên tiếp
              {hook.disabled_reason ? ` (${hook.disabled_reason})` : ''}. Endpoint chết lâu ngày
              sẽ làm nghẽn hàng đợi của cả dự án, nên hệ thống ngắt nó ra.
            </div>
            <div className="mt-1 max-w-xs text-meta text-muted">
              <span className="font-semibold">Bật lại:</span> sửa cho endpoint trả 2xx, rồi
              xoá và tạo lại endpoint này — API hiện chưa có đường bật lại tại chỗ. Khoá ký
              mới sẽ được cấp, nhớ cập nhật ở phía nhận.
            </div>
          </>
        ) : hook.active ? (
          <>
            <StatusPill tone="ok">● đang bật</StatusPill>
            {hook.consecutive_failures > 0 && (
              <div className="mt-1 text-meta text-duesoon">
                {hook.consecutive_failures}/{DISABLE_AFTER_FAILURES} lần thất bại liên tiếp —
                còn {DISABLE_AFTER_FAILURES - hook.consecutive_failures} lần nữa là tự tắt.
              </div>
            )}
          </>
        ) : (
          <>
            <StatusPill tone="neutral">○ đã tắt</StatusPill>
            <div className="mt-1 text-meta text-muted">Tắt thủ công, không phải do lỗi.</div>
          </>
        )}
      </Td>

      <Td className="whitespace-nowrap text-right">
        <Link
          to={`/p/${projectId}/integrations/webhooks/${hook.id}/deliveries`}
          className="btn inline-block"
        >
          Lịch sử gửi
        </Link>
        <button type="button" className="btn ml-2" onClick={onDelete}>
          Xoá
        </button>
      </Td>
    </Tr>
  )
}

function CreateForm({
  projectId,
  onCreated,
}: {
  projectId: string
  onCreated: (w: CreatedWebhook) => void
}) {
  const [url, setUrl] = useState('https://')
  const [events, setEvents] = useState<string[]>(['submission.created'])
  const [includeAnswers, setIncludeAnswers] = useState(false)
  const [acknowledged, setAcknowledged] = useState(false)

  const create = useMutation({
    mutationFn: () =>
      api.post<CreatedWebhook>('/api/v1/webhooks', {
        project_id: projectId,
        url: url.trim(),
        events,
        include_answers: includeAnswers,
      }),
    onSuccess: onCreated,
  })

  const fieldErrors = create.error instanceof RequestFailed ? create.error.fields : {}
  const blocked = includeAnswers && !acknowledged

  return (
    <Card title="Endpoint mới">
      <form
        className="grid gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          if (!blocked) create.mutate()
        }}
      >
        <Field
          label="URL đích"
          hint="Bắt buộc https. Địa chỉ nội bộ (127.x, 10.x, 192.168.x, 169.254.x) bị từ chối, và địa chỉ được kiểm lại ở mỗi lần gửi — một tên miền đổi DNS về mạng nội bộ sau khi lưu vẫn không đi qua được."
          error={fieldErrors['url']}
        >
          <input
            id="webhook-url"
            className="input font-mono"
            type="url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://crm.acme.vn/hooks/collectr"
            required
          />
        </Field>

        <fieldset>
          <legend className="text-meta font-semibold">Sự kiện đăng ký</legend>
          {fieldErrors['events'] && (
            <p role="alert" className="mt-1 text-meta text-overdue">
              {fieldErrors['events']}
            </p>
          )}
          <div className="mt-1 grid gap-1.5 sm:grid-cols-2">
            {EVENTS.map((ev) => (
              <label key={ev.code} className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 shrink-0 accent-[#1a1a1a]"
                  checked={events.includes(ev.code)}
                  onChange={(e) =>
                    setEvents((prev) =>
                      e.target.checked ? [...prev, ev.code] : prev.filter((c) => c !== ev.code),
                    )
                  }
                />
                <span className="min-w-0">
                  <span className="block font-mono text-meta">{ev.code}</span>
                  <span className="block text-meta text-muted">{ev.label}</span>
                  {ev.note && <span className="block text-meta text-accent">{ev.note}</span>}
                </span>
              </label>
            ))}
          </div>
        </fieldset>

        <div className="rounded border border-accent bg-accent/5 p-2.5">
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-0.5 shrink-0 accent-[#c8502d]"
              checked={includeAnswers}
              onChange={(e) => {
                setIncludeAnswers(e.target.checked)
                setAcknowledged(false)
              }}
            />
            <span className="text-body">
              <span className="font-semibold">Gửi kèm câu trả lời (`answers`)</span>
              <span className="mt-0.5 block text-meta">
                Mặc định tắt. Khi tắt, payload chỉ có id bản ghi và metadata — bên nhận muốn
                đọc nội dung thì phải gọi API và tự chịu kiểm tra quyền. Field nhạy cảm không
                bao giờ rời hệ thống qua webhook.
              </span>
            </span>
          </label>

          {includeAnswers && (
            <div className="mt-2 border-t border-accent/30 pt-2">
              <p className="text-meta text-accent">
                Bật mục này là <span className="font-semibold">chuyển dữ liệu cá nhân cho bên
                thứ ba</span>. Bạn phải ghi nhận bên nhận vào hồ sơ hoạt động xử lý dữ liệu; nếu
                đích nằm ngoài Việt Nam còn phát sinh nghĩa vụ đánh giá tác động chuyển dữ liệu
                xuyên biên giới. Lựa chọn này được ghi vào nhật ký audit kèm tên bạn.
              </p>
              <label className="mt-1.5 flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 shrink-0 accent-[#c8502d]"
                  checked={acknowledged}
                  onChange={(e) => setAcknowledged(e.target.checked)}
                />
                <span className="text-meta font-semibold">
                  Tôi xác nhận đã ghi nhận bên nhận này vào hồ sơ xử lý dữ liệu.
                </span>
              </label>
            </div>
          )}
        </div>

        {create.isError && Object.keys(fieldErrors).length === 0 && (
          <ErrorBanner error={create.error} />
        )}

        <div className="flex items-center gap-2">
          <button type="submit" className="btn-primary" disabled={create.isPending || blocked}>
            {create.isPending ? 'Đang tạo…' : 'Tạo endpoint — khoá ký hiện một lần'}
          </button>
          {blocked && (
            <span className="text-meta text-accent">
              Cần xác nhận ở trên trước khi tạo.
            </span>
          )}
        </div>
      </form>
    </Card>
  )
}

function SignatureHelp() {
  return (
    <Card title="Xác minh chữ ký" aside="X-Collectr-Signature">
      <p className="text-body">
        Mỗi lần gửi kèm bốn header. Bên nhận phải kiểm chữ ký trước khi đọc body — nếu không,
        bất kỳ ai biết URL đều gửi được sự kiện giả vào hệ thống của bạn.
      </p>
      <pre className="mt-2 overflow-x-auto rounded border border-line bg-panel p-2 font-mono text-meta leading-relaxed">
        {`X-Collectr-Event:     submission.created
X-Collectr-Delivery:  018f…              # id lần gửi, dùng để khử trùng lặp
X-Collectr-Timestamp: 1754467200         # giây epoch
X-Collectr-Signature: sha256=9a1f…`}
      </pre>
      <ol className="mt-2 list-decimal space-y-1 pl-4 text-body">
        <li>
          Ghép chuỗi ký:{' '}
          <code className="font-mono text-meta">timestamp + "." + raw_body</code> — dùng
          <span className="font-semibold"> body thô</span>, đừng parse rồi serialize lại, một dấu
          cách khác chỗ là chữ ký sai.
        </li>
        <li>
          Tính <code className="font-mono text-meta">HMAC-SHA256(secret, chuỗi ký)</code> với
          khoá ký lấy lúc tạo endpoint, rồi hex hoá.
        </li>
        <li>
          So sánh <span className="font-semibold">constant-time</span>{' '}
          (<code className="font-mono text-meta">hmac.Equal</code>,{' '}
          <code className="font-mono text-meta">hash_equals</code>,{' '}
          <code className="font-mono text-meta">crypto.timingSafeEqual</code>). So bằng{' '}
          <code className="font-mono text-meta">==</code> rò rỉ khoá qua thời gian phản hồi.
        </li>
        <li>
          Từ chối nếu <code className="font-mono text-meta">timestamp</code> lệch quá{' '}
          <span className="font-semibold">5 phút</span> so với giờ hiện tại. Không có bước này
          thì một bản ghi cũ bắt được vẫn phát lại được nguyên vẹn.
        </li>
        <li>
          Khử trùng lặp theo <code className="font-mono text-meta">X-Collectr-Delivery</code>:
          hệ thống bảo đảm gửi ít nhất một lần, nên cùng một sự kiện có thể đến hai lần.
        </li>
      </ol>
      <p className="mt-2 text-meta text-muted">
        Khoá ký được cất ở dạng mã hoá và không đọc lại được — kể cả người tạo ra nó. Đây là
        lý do bảng lúc tạo nói “hiện một lần”.
      </p>
    </Card>
  )
}

function DeliveryRules() {
  return (
    <Card title="Quy tắc gửi lại và tự tắt">
      <ul className="space-y-1.5 text-body">
        <li>
          <span className="font-semibold">2xx</span> → thành công, chuỗi lỗi liên tiếp của
          endpoint được đặt lại về 0.
        </li>
        <li>
          <span className="font-semibold">4xx</span> (trừ 408 và 429) → thất bại ngay, không thử
          lại. Yêu cầu sai thì gửi lại vẫn sai, chỉ tổ đập vào bên nhận.
        </li>
        <li>
          <span className="font-semibold">5xx, timeout, 429</span> → thử lại tối đa 8 lần, giãn
          cách theo cấp số nhân có jitter (10s → 30s → 2m → … → 12h). Jitter là bắt buộc: sau khi
          endpoint sập một giờ, hàng nghìn lần gửi cùng đến hạn một lúc.
        </li>
        <li>
          Hết lượt → trạng thái <span className="font-mono text-meta">dead</span>, giữ 30 ngày
          để gửi lại thủ công ở màn “Lịch sử gửi”.
        </li>
        <li>
          <span className="font-semibold text-overdue">
            {DISABLE_AFTER_FAILURES} lần thất bại liên tiếp
          </span>{' '}
          → endpoint tự tắt và Admin nhận email. Không phải hình phạt: một endpoint đã chết từ
          lâu sẽ chiếm hàng đợi của mọi endpoint còn sống.
        </li>
      </ul>
      <p className="mt-2 text-meta text-muted">
        Nhật ký gửi chịu cùng hạn lưu với lượt gửi biểu mẫu, và phần thân phản hồi của bên nhận
        chỉ được giữ 1 KB đầu — nhật ký tích hợp không phải chỗ thứ hai để đọc dữ liệu cá nhân.
      </p>
    </Card>
  )
}
