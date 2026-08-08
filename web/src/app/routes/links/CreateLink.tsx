/**
 * 1o -- creating a link.
 *
 * The domain is shown but not chosen. `POST /api/v1/links` takes no domain: a new
 * code always lands on the tenant's default host, and which host that is only
 * changes on the Tên miền screen. Presenting a picker here would let someone
 * choose a value the API then ignores, and the mistake surfaces on a printed
 * poster.
 */
import { useState, type FormEvent } from 'react'
import { Link as RouterLink, useNavigate, useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, RequestFailed, type List } from '../../lib/api'
import { Card, ErrorBanner, Field, PageHeader, num } from '../../components/ui'
import type { DomainRow, FormOption, LinkRow } from './Links'

type Destination = 'url' | 'form'

export function CreateLink() {
  const { projectId } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const [destination, setDestination] = useState<Destination>('url')
  const [targetURL, setTargetURL] = useState('')
  const [formID, setFormID] = useState('')
  const [alias, setAlias] = useState('')
  const [expiresAt, setExpiresAt] = useState('')

  const domains = useQuery({
    queryKey: ['domains'],
    queryFn: async () => (await api.get<List<DomainRow>>('/api/v1/domains')).data,
    staleTime: 60_000,
  })

  const forms = useQuery({
    queryKey: ['forms', projectId],
    queryFn: async () => (await api.get<List<FormOption>>(`/api/v1/forms?project_id=${projectId}`)).data,
    enabled: Boolean(projectId),
  })

  const create = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = { project_id: projectId }
      if (destination === 'form') body.form_id = formID
      else body.target_url = targetURL.trim()
      if (alias.trim()) body.alias = alias.trim()
      // datetime-local gives local wall-clock time with no zone; the API wants
      // RFC3339. Converting through Date keeps the moment the operator meant
      // rather than shifting the expiry by the office's UTC offset.
      if (expiresAt) body.expires_at = new Date(expiresAt).toISOString()
      return api.post<LinkRow>('/api/v1/links', body)
    },
    onSuccess: (link) => {
      void qc.invalidateQueries({ queryKey: ['links', projectId] })
      void qc.invalidateQueries({ queryKey: ['domains'] })
      void navigate(`/p/${projectId}/links/${link.id}`)
    },
  })

  const failure = create.error instanceof RequestFailed ? create.error : null
  const fields = failure?.fields ?? {}
  const defaultDomain = domains.data?.find((d) => d.is_default)

  function submit(e: FormEvent) {
    e.preventDefault()
    create.mutate()
  }

  const noDomain = domains.data !== undefined && domains.data.length === 0

  return (
    <div className="p-6">
      <PageHeader
        title="Link mới"
        meta={
          defaultDomain
            ? `link sẽ nằm trên ${defaultDomain.host}`
            : domains.isPending
              ? '…'
              : 'chưa có tên miền để phát mã'
        }
        actions={
          <RouterLink to={`/p/${projectId}/links`} className="btn">
            Huỷ
          </RouterLink>
        }
      />

      {noDomain && (
        <div role="alert" className="mb-3 rounded border border-duesoon/50 bg-duesoon/5 px-3 py-2 text-body">
          <p className="font-semibold text-duesoon">Tổ chức chưa có tên miền nào để phát mã.</p>
          <p className="mt-0.5 text-muted">
            Mã rút gọn luôn nằm trên một tên miền cụ thể, nên không thể tạo link trước khi có ít nhất
            một tên miền.{' '}
            <RouterLink to={`/p/${projectId}/links/domains`} className="font-semibold underline">
              Thêm tên miền
            </RouterLink>{' '}
            (cần quyền member.manage).
          </p>
        </div>
      )}

      <form onSubmit={submit} className="grid max-w-2xl gap-3">
        <Card title="Link này trỏ đi đâu">
          <fieldset className="grid gap-2">
            <legend className="sr-only">Kiểu đích</legend>
            <label className="flex items-center gap-2 text-body">
              <input
                type="radio"
                name="destination"
                value="url"
                checked={destination === 'url'}
                onChange={() => setDestination('url')}
              />
              Một URL bên ngoài
            </label>
            {destination === 'url' && (
              <div className="pl-6">
                <Field
                  label="URL đích"
                  error={fields.target_url}
                  hint="Tham số utm_* gắn trên link rút gọn sẽ được chuyển tiếp sang đây. Tham số đã có sẵn trong URL này không bị ghi đè."
                >
                  <input
                    id="target-url"
                    className="input"
                    inputMode="url"
                    placeholder="https://acme.vn/landing"
                    value={targetURL}
                    onChange={(e) => setTargetURL(e.target.value)}
                  />
                </Field>
              </div>
            )}

            <label className="flex items-center gap-2 text-body">
              <input
                type="radio"
                name="destination"
                value="form"
                checked={destination === 'form'}
                onChange={() => setDestination('form')}
              />
              Một biểu mẫu trong dự án
            </label>
            {destination === 'form' && (
              <div className="pl-6">
                <Field
                  label="Biểu mẫu"
                  error={fields.form_id}
                  hint="Lượt bấm và lượt gửi của biểu mẫu sẽ nối được với nhau mà không cần cookie bên thứ ba."
                >
                  <select
                    id="form-id"
                    className="input"
                    value={formID}
                    onChange={(e) => setFormID(e.target.value)}
                  >
                    <option value="">— chọn biểu mẫu —</option>
                    {(forms.data ?? []).map((f) => (
                      <option key={f.id} value={f.id}>
                        {f.title}
                        {f.status === 'live' ? '' : ` (${f.status})`}
                      </option>
                    ))}
                  </select>
                </Field>
                {forms.data?.length === 0 && (
                  <p className="mt-1 text-meta text-muted">Dự án này chưa có biểu mẫu nào.</p>
                )}
              </div>
            )}
          </fieldset>
        </Card>

        <Card title="Mã và hạn dùng">
          <div className="grid gap-3">
            <Field
              label="Alias (tuỳ chọn)"
              error={fields.alias ?? (failure?.body.code === 'alias_taken' ? aliasTakenMessage(defaultDomain?.host) : undefined)}
              hint={
                defaultDomain
                  ? `Để trống thì hệ thống tự sinh mã. Alias chỉ cần duy nhất trên ${defaultDomain.host} — tên miền khác dùng lại được cùng một alias.`
                  : 'Để trống thì hệ thống tự sinh mã.'
              }
            >
              <div className="flex items-center gap-1">
                <span className="id-chip whitespace-nowrap">
                  {defaultDomain ? `${defaultDomain.host}/` : '…/'}
                </span>
                <input
                  id="alias"
                  className="input"
                  placeholder="tet2026"
                  value={alias}
                  onChange={(e) => setAlias(e.target.value)}
                />
              </div>
            </Field>

            <Field
              label="Hạn dùng (tuỳ chọn)"
              error={fields.expires_at}
              hint="Sau thời điểm này link trả 410 — người quét QR đã in ra sẽ biết chiến dịch đã kết thúc, chứ không thấy trang lỗi 404."
            >
              <input
                id="expires-at"
                type="datetime-local"
                className="input"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
            </Field>
          </div>
        </Card>

        {defaultDomain && (
          <p className="id-chip">
            Link mới nằm trên <span className="font-semibold">{defaultDomain.host}</span> (
            {num(defaultDomain.link_count)} link đang dùng). Đổi tên miền mặc định ở{' '}
            <RouterLink to={`/p/${projectId}/links/domains`} className="underline">
              Tên miền
            </RouterLink>{' '}
            — link đã tạo giữ nguyên tên miền của chúng.
          </p>
        )}

        {create.isError && !hasHandledError(failure) && <ErrorBanner error={create.error} />}
        {failure?.body.code === 'no_domain' && (
          <div role="alert" className="rounded border border-overdue/40 bg-overdue/5 px-3 py-2 text-body">
            <p className="font-semibold text-overdue">{failure.body.title}</p>
            <p className="mt-0.5 text-muted">
              Thêm tên miền cần quyền member.manage, không phải link.write: nó đổi thứ mà deployment
              trả lời và cần thêm bản ghi DNS lẫn chứng chỉ.
            </p>
          </div>
        )}

        <div className="flex items-center gap-2">
          <button
            type="submit"
            className="btn-primary"
            disabled={create.isPending || noDomain || (destination === 'form' ? !formID : !targetURL.trim())}
          >
            {create.isPending ? 'Đang tạo…' : 'Tạo link'}
          </button>
          <RouterLink to={`/p/${projectId}/links`} className="btn">
            Huỷ
          </RouterLink>
        </div>
      </form>
    </div>
  )
}

function aliasTakenMessage(host: string | undefined): string {
  return host
    ? `Alias này đã có trên ${host}. Chọn alias khác, hoặc để trống để hệ thống tự sinh mã.`
    : 'Alias này đã được dùng trên tên miền mặc định. Chọn alias khác.'
}

/** Errors already rendered next to the input that caused them, so the banner
 *  does not repeat them as one opaque line at the bottom. */
function hasHandledError(failure: RequestFailed | null): boolean {
  if (!failure) return false
  if (failure.body.code === 'alias_taken' || failure.body.code === 'no_domain') return true
  return Object.keys(failure.fields).length > 0
}
