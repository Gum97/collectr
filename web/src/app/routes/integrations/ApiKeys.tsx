/**
 * Screen 2c -- API keys.
 *
 * The interesting part of this screen is what it refuses to do. An API key is a
 * credential with no person behind it: it lives in a CI variable, it is copied
 * between environments, and when it is used at 3am nobody can be asked why. So
 * the scope list is a fixed, closed set, and the scopes that decide who may read
 * sensitive personal data or act on a data subject's request are rendered,
 * visibly, as unavailable -- not hidden, because a reader who cannot see them
 * will assume the tool forgot rather than that it declined.
 *
 * BACKEND: /api/v1/api-keys does not exist yet. Nothing here calls it. The
 * contract below is the one documented in docs/10-public-api.md §10.1 and is
 * was gated behind ENDPOINTS_READY until the endpoints existed; the flag stays as one
 * constant once the handler lands.
 */
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, RequestFailed, type List } from '../../lib/api'
import { useMe } from '../../lib/session'
import { useProjects } from '../../lib/projects'
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
  date,
  dateTime,
} from '../../components/ui'
import { SecretOnce } from './SecretOnce'

/** Flip to true only when GET/POST/DELETE /api/v1/api-keys are mounted in the
 *  mux. Until then the screen never issues a request that would 404. */
const ENDPOINTS_READY = true

interface ApiKeyRow {
  id: string
  name: string
  /** First 8 characters, e.g. clc_live_8f2a. The rest is only ever hashed. */
  prefix: string
  project_id: string | null
  scopes: string[]
  last_used_at: string | null
  expires_at: string | null
  revoked_at: string | null
  created_at: string
}

interface IssuedKey {
  id: string
  name: string
  prefix: string
  scopes: string[]
  expires_at: string
  /** Present only on the issue response. */
  key: string
}

/**
 * The closed set of scopes an API key may hold.
 *
 * Not derived from the caller's own capabilities: the server already narrows a
 * key's scopes to a subset of its creator's, and duplicating that rule here
 * would produce a list that drifts from the one actually enforced.
 */
const GRANTABLE: { scope: string; label: string }[] = [
  { scope: 'form.read', label: 'Đọc biểu mẫu và schema' },
  { scope: 'submission.read', label: 'Đọc lượt gửi — field nhạy cảm vẫn bị che' },
  { scope: 'link.read', label: 'Đọc link rút gọn và thống kê lượt bấm' },
  { scope: 'link.write', label: 'Tạo và sửa link rút gọn' },
  { scope: 'analytics.read', label: 'Đọc số liệu phân tích tổng hợp' },
]

/**
 * Scopes that cannot be attached to a key, with the reason stated where the
 * choice is made rather than in a document nobody opens.
 *
 * `enforced` marks the four the server itself refuses (authn.apiKeyForbidden).
 * `submission.export` is refused here as a matter of interface policy: the
 * server would currently accept it, and that is reported as a gap rather than
 * quietly worked around.
 */
const REFUSED: { scope: string; why: string; enforced: boolean }[] = [
  {
    scope: 'submission.read_sensitive',
    why: 'Đọc dữ liệu nhạy cảm (sức khoẻ, sinh trắc, lý lịch tư pháp…) cần một người chịu trách nhiệm và phiên đăng nhập có MFA. Một chuỗi ký tự nằm trong cấu hình CI không trả lời được câu hỏi “ai đã đọc và vì mục đích gì”.',
    enforced: true,
  },
  {
    scope: 'submission.export',
    why: 'Xuất hàng loạt là truy cập hàng loạt dữ liệu cá nhân — sự kiện phải ghi audit kèm tên người thực hiện. Key không có người đứng sau nên bản ghi audit sẽ trống chỗ quan trọng nhất. Dùng phiên người dùng để xuất.',
    enforced: false,
  },
  {
    scope: 'dsr.handle',
    why: 'Xử lý yêu cầu của chủ thể dữ liệu là hành vi pháp lý có thời hạn 72h và có hậu quả. Nó cần người quyết định, không phải job tự động.',
    enforced: true,
  },
  {
    scope: 'audit.read',
    why: 'Nhật ký audit là bằng chứng đối chứng. Cấp cho key quyền đọc nó là đưa cho tích hợp bên ngoài toàn bộ dấu vết của chính nó.',
    enforced: true,
  },
  {
    scope: 'member.manage',
    why: 'Quản lý thành viên và phân quyền là đường tự nâng quyền ngắn nhất trong hệ thống. Nó không bao giờ được nằm trong một credential không hết hạn theo phiên.',
    enforced: true,
  },
]

const TTL_CHOICES = [
  { days: 30, label: '30 ngày' },
  { days: 90, label: '90 ngày' },
  { days: 180, label: '180 ngày' },
  { days: 365, label: '1 năm' },
]

export function ApiKeys() {
  const me = useMe()
  const qc = useQueryClient()
  const [issued, setIssued] = useState<IssuedKey | null>(null)

  const canManage = Boolean(me.data?.capabilities.includes('apikey.manage'))

  const keys = useQuery({
    queryKey: ['api-keys'],
    queryFn: async () => (await api.get<List<ApiKeyRow>>('/api/v1/api-keys')).data,
    enabled: ENDPOINTS_READY && canManage,
  })

  const revoke = useMutation({
    mutationFn: (id: string) => api.del<void>(`/api/v1/api-keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['api-keys'] }),
  })

  return (
    <div className="p-6">
      <PageHeader
        title="API key"
        meta={
          <>
            iam.api_keys · cần quyền apikey.manage
            {keys.data && ` · ${keys.data.length} key`}
          </>
        }
      />

      {!canManage && !me.isPending && (
        <Empty
          title="Bạn không có quyền quản lý API key"
          hint="Cấp key là cấp quyền truy cập dữ liệu cho một hệ thống bên ngoài, nên việc này giới hạn ở quyền apikey.manage. Hãy nhờ Admin của tổ chức."
        />
      )}

      {canManage && (
        <>
          {issued && (
            <div className="mb-4">
              <SecretOnce
                title={`API key “${issued.name}”`}
                value={issued.key}
                what={`Gửi kèm mỗi request dưới dạng header Authorization: Bearer <key>. Key hết hạn ngày ${date(issued.expires_at)}.`}
                extra={
                  <>
                    Hệ thống chỉ giữ <span className="font-semibold">sha256 của key</span> và
                    tiền tố <span className="font-mono">{issued.prefix}</span> để nhận diện trong
                    danh sách. Phạm vi: {issued.scopes.join(', ')}. Đừng đặt key vào mã nguồn —
                    tiền tố <span className="font-mono">clc_live_</span> được các bộ quét rò rỉ
                    trên Git nhận ra, và đó là cách bạn muốn phát hiện sự cố, không phải cách
                    bạn muốn tạo ra nó.
                  </>
                }
                onDone={() => setIssued(null)}
              />
            </div>
          )}

          {!ENDPOINTS_READY && (
            <div role="status" className="mb-4 rounded border border-duesoon bg-duesoon/5 p-3">
              <p className="text-body font-semibold text-duesoon">
                <span aria-hidden>⚠ </span>Backend chưa có endpoint API key
              </p>
              <p className="mt-1 text-body">
                Bảng <span className="font-mono text-meta">iam.api_keys</span> và hàm cấp key đã
                có trong hệ thống, nhưng chưa route HTTP nào được gắn vào mux. Màn hình này dựng
                theo hợp đồng ở <span className="font-mono text-meta">docs/10-public-api.md</span>{' '}
                §10.1 và <span className="font-semibold">không gọi endpoint nào</span> — gọi một
                đường dẫn không tồn tại chỉ đổi một lỗi thiếu tính năng thành một lỗi 404 khó
                đọc. Danh sách bên dưới trống vì chưa có nguồn dữ liệu, không phải vì tổ chức
                chưa có key nào.
              </p>
            </div>
          )}

          <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_360px]">
            <div className="min-w-0">
              {ENDPOINTS_READY && keys.isPending && <Loading />}
              {keys.isError && <ErrorBanner error={keys.error} retry={() => keys.refetch()} />}

              {(!ENDPOINTS_READY || keys.data?.length === 0) && !keys.isError && (
                <Empty
                  title={ENDPOINTS_READY ? 'Chưa có API key nào' : 'Chưa đọc được danh sách key'}
                  hint={
                    ENDPOINTS_READY
                      ? 'Tổ chức này chưa cấp key nào. Không có key nghĩa là chưa hệ thống bên ngoài nào gọi được vào API quản trị.'
                      : 'Cần GET /api/v1/api-keys. Đến khi endpoint đó tồn tại, không có cách nào biết tổ chức đang có bao nhiêu key đang sống.'
                  }
                />
              )}

              {keys.data && keys.data.length > 0 && (
                <Table
                  head={
                    <>
                      <Th>Tên · tiền tố</Th>
                      <Th>Phạm vi</Th>
                      <Th>Dùng lần cuối</Th>
                      <Th>Hạn</Th>
                      <Th className="text-right">Thao tác</Th>
                    </>
                  }
                >
                  {keys.data.map((k) => (
                    <KeyRow
                      key={k.id}
                      row={k}
                      onRevoke={() => {
                        if (
                          window.confirm(
                            `Thu hồi key “${k.name}”?\n\nCó hiệu lực ngay ở request kế tiếp. Mọi tích hợp đang dùng key này sẽ nhận 401 và không có cách khôi phục — phải cấp key mới.`,
                          )
                        ) {
                          revoke.mutate(k.id)
                        }
                      }}
                    />
                  ))}
                </Table>
              )}

              {revoke.isError && (
                <div className="mt-3">
                  <ErrorBanner error={revoke.error} />
                </div>
              )}

              <p className="mt-3 text-meta text-muted">
                Chỉ lưu hash của key, nên không ai đọc lại được giá trị đã cấp. Thu hồi có hiệu
                lực ngay ở request kế tiếp; mọi lần cấp và thu hồi đều ghi vào nhật ký audit.
                Key bắt buộc có hạn — một credential vĩnh viễn là một khoản nợ vĩnh viễn.
              </p>
            </div>

            <IssueForm onIssued={setIssued} />
          </div>
        </>
      )}
    </div>
  )
}

function KeyRow({ row, onRevoke }: { row: ApiKeyRow; onRevoke: () => void }) {
  const revoked = Boolean(row.revoked_at)
  const expired = !revoked && Boolean(row.expires_at) && new Date(row.expires_at!) < new Date()

  return (
    <Tr className={revoked || expired ? 'opacity-60' : ''}>
      <Td>
        <div className={`font-semibold ${revoked ? 'line-through' : ''}`}>{row.name}</div>
        <div className="id-chip">{row.prefix}…</div>
      </Td>

      <Td>
        <div className="flex flex-col gap-0.5 font-mono text-meta">
          {row.scopes.map((s) => (
            <span key={s}>{s}</span>
          ))}
        </div>
      </Td>

      <Td className="whitespace-nowrap font-mono text-meta">
        {/* "chưa dùng" and "không rõ" would be the same em dash otherwise, and a
            key that has never been used is a key that can be revoked freely. */}
        {row.last_used_at ? dateTime(row.last_used_at) : <span className="text-muted">chưa dùng</span>}
      </Td>

      <Td className="whitespace-nowrap">
        {revoked ? (
          <StatusPill tone="neutral">đã thu hồi</StatusPill>
        ) : expired ? (
          <StatusPill tone="overdue">đã hết hạn</StatusPill>
        ) : (
          <ExpiryPill expiresAt={row.expires_at} />
        )}
      </Td>

      <Td className="whitespace-nowrap text-right">
        {!revoked && (
          <button type="button" className="btn" onClick={onRevoke}>
            Thu hồi
          </button>
        )}
      </Td>
    </Tr>
  )
}

function ExpiryPill({ expiresAt }: { expiresAt: string | null }) {
  if (!expiresAt) return <StatusPill tone="overdue">không có hạn</StatusPill>
  const days = Math.floor((new Date(expiresAt).getTime() - Date.now()) / 86_400_000)
  if (days <= 7) return <StatusPill tone="duesoon">còn {days} ngày</StatusPill>
  return <StatusPill tone="neutral">{date(expiresAt)}</StatusPill>
}

function IssueForm({ onIssued }: { onIssued: (k: IssuedKey) => void }) {
  const projects = useProjects()
  const [name, setName] = useState('')
  const [projectId, setProjectId] = useState('')
  const [ttlDays, setTtlDays] = useState(90)
  const [scopes, setScopes] = useState<string[]>(['form.read'])

  const issue = useMutation({
    mutationFn: () =>
      api.post<IssuedKey>('/api/v1/api-keys', {
        name: name.trim(),
        project_id: projectId || null,
        // Filtered again at submit, not only at render: a disabled input is a
        // hint to a person, never a control over what is sent.
        scopes: scopes.filter((s) => GRANTABLE.some((g) => g.scope === s)),
        expires_in_days: ttlDays,
      }),
    onSuccess: onIssued,
  })

  const fieldErrors = issue.error instanceof RequestFailed ? issue.error.fields : {}

  return (
    <Card title="Cấp key mới" className="h-fit bg-panel">
      <form
        className="grid gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          if (ENDPOINTS_READY) issue.mutate()
        }}
      >
        <Field
          label="Tên"
          hint="Đặt theo hệ thống sẽ dùng nó, không phải theo người tạo. Sáu tháng sau, “key của Duy” không cho biết tắt đi thì cái gì hỏng."
          error={fieldErrors['name']}
        >
          <input
            className="input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Đồng bộ kho hàng"
            required
          />
        </Field>

        <div className="grid grid-cols-2 gap-2">
          <Field label="Giới hạn dự án" error={fieldErrors['project_id']}>
            <select
              className="input"
              value={projectId}
              onChange={(e) => setProjectId(e.target.value)}
            >
              <option value="">Toàn tổ chức</option>
              {projects.data
                ?.filter((p) => p.access !== 'none' && !p.archived_at)
                .map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
            </select>
          </Field>

          <Field label="Hết hạn sau" error={fieldErrors['expires_in_days']}>
            <select
              className="input font-mono"
              value={ttlDays}
              onChange={(e) => setTtlDays(Number(e.target.value))}
            >
              {TTL_CHOICES.map((t) => (
                <option key={t.days} value={t.days}>
                  {t.label}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <p className="-mt-1 text-meta text-muted">
          Giới hạn dự án là hàng rào thứ hai sau phạm vi: một key chỉ đọc được biểu mẫu của dự án
          đã chọn, kể cả khi phạm vi nói “đọc lượt gửi”.
        </p>

        <fieldset>
          <legend className="text-meta font-semibold">Phạm vi (scope)</legend>
          {fieldErrors['scopes'] && (
            <p role="alert" className="mt-1 text-meta text-overdue">
              {fieldErrors['scopes']}
            </p>
          )}
          <div className="mt-1.5 flex flex-col gap-1.5">
            {GRANTABLE.map((g) => (
              <label key={g.scope} className="flex items-start gap-2">
                <input
                  type="checkbox"
                  className="mt-0.5 shrink-0 accent-[#1a1a1a]"
                  checked={scopes.includes(g.scope)}
                  onChange={(e) =>
                    setScopes((prev) =>
                      e.target.checked ? [...prev, g.scope] : prev.filter((s) => s !== g.scope),
                    )
                  }
                />
                <span className="min-w-0">
                  <span className="block font-mono text-meta">{g.scope}</span>
                  <span className="block text-meta text-muted">{g.label}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>

        <RefusedScopes />

        {issue.isError && Object.keys(fieldErrors).length === 0 && (
          <ErrorBanner error={issue.error} />
        )}

        <button
          type="submit"
          className="btn-primary"
          disabled={!ENDPOINTS_READY || issue.isPending || scopes.length === 0}
        >
          {issue.isPending ? 'Đang cấp…' : 'Cấp key — hiện đúng một lần'}
        </button>

        {!ENDPOINTS_READY && (
          <p role="status" className="text-meta text-duesoon">
            Nút bị khoá vì <span className="font-mono">POST /api/v1/api-keys</span> chưa tồn tại.
            Mọi lựa chọn ở trên đã đúng hợp đồng và sẽ hoạt động ngay khi endpoint được gắn.
          </p>
        )}
        {ENDPOINTS_READY && scopes.length === 0 && (
          <p role="status" className="text-meta text-muted">
            Chọn ít nhất một phạm vi. Key không có phạm vi nào thì không gọi được gì.
          </p>
        )}
      </form>
    </Card>
  )
}

/**
 * The refusals, rendered as refusals.
 *
 * These are real checkboxes, disabled and unchecked, rather than absent entries.
 * Someone looking for `dsr.handle` needs to find it and read why they cannot
 * have it; if it is simply missing they will assume the list is incomplete and
 * go looking for a way around.
 */
function RefusedScopes() {
  return (
    <div className="rounded border border-accent bg-accent/5 p-2.5">
      <p className="text-meta font-semibold text-accent">Không cấp được cho API key</p>
      <ul className="mt-1.5 flex flex-col gap-2">
        {REFUSED.map((r) => (
          <li key={r.scope} className="flex items-start gap-2">
            <input
              type="checkbox"
              disabled
              checked={false}
              readOnly
              aria-label={`${r.scope} — không cấp được cho API key`}
              className="mt-0.5 shrink-0"
            />
            <span className="min-w-0">
              <span className="flex flex-wrap items-center gap-1">
                <span className="font-mono text-meta text-muted line-through">{r.scope}</span>
                <span className="id-chip rounded border border-accent/40 px-1 text-accent">
                  {r.enforced ? 'API cũng từ chối' : 'chặn ở giao diện'}
                </span>
              </span>
              <span className="mt-0.5 block text-meta text-muted">{r.why}</span>
            </span>
          </li>
        ))}
      </ul>
      <p className="mt-2 text-meta">
        Điểm chung: mỗi hành động trên đều cần một con người chịu trách nhiệm khi có người hỏi
        “ai đã làm việc này”. API key không trả lời được câu đó — nó là một chuỗi ký tự nằm
        trong biến môi trường của một pipeline. Cần làm những việc đó thì dùng phiên đăng nhập
        của chính bạn.
      </p>
    </div>
  )
}
