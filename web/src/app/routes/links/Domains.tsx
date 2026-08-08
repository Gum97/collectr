/**
 * 1o -- the domains short codes are issued on.
 *
 * Three things on this screen are consequences rather than settings, and each is
 * said before the button is pressed rather than after the API refuses:
 *
 *  - changing the default only affects links created from then on; every
 *    existing link keeps its own host, because its code is already printed;
 *  - a domain still carrying links cannot be removed, and the 409 explaining
 *    that arrives too late to be useful, so the row says it up front;
 *  - a host is unique across the whole deployment, not per tenant, so "already
 *    registered" is deliberately silent about who holds it.
 */
import { useState, type FormEvent } from 'react'
import { Link as RouterLink, useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, RequestFailed, type List } from '../../lib/api'
import { can, useMe } from '../../lib/session'
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
  num,
} from '../../components/ui'
import type { DomainRow } from './Links'

interface AddedDomain {
  id: string
  host: string
  is_default: boolean
  /** The DNS record and certificate step; without it the host resolves nowhere
   *  and every code issued on it is dead on arrival. */
  next_step: string
}

export function Domains() {
  const { projectId } = useParams()
  const me = useMe()
  const mayManage = can(me.data, 'member.manage')
  const qc = useQueryClient()

  const domains = useQuery({
    queryKey: ['domains'],
    queryFn: async () => (await api.get<List<DomainRow>>('/api/v1/domains')).data,
  })

  const [host, setHost] = useState('')
  const [asDefault, setAsDefault] = useState(false)
  const [added, setAdded] = useState<AddedDomain | null>(null)

  const add = useMutation({
    mutationFn: async () =>
      api.post<AddedDomain>('/api/v1/domains', { host: host.trim(), is_default: asDefault }),
    onSuccess: (d) => {
      setAdded(d)
      setHost('')
      setAsDefault(false)
      void qc.invalidateQueries({ queryKey: ['domains'] })
    },
  })

  const addFailure = add.error instanceof RequestFailed ? add.error : null

  function submit(e: FormEvent) {
    e.preventDefault()
    add.mutate()
  }

  const total = domains.data?.length ?? 0
  const linkTotal = (domains.data ?? []).reduce((n, d) => n + d.link_count, 0)

  return (
    <div className="p-6">
      <PageHeader
        title="Tên miền phát mã"
        meta={`${num(total)} tên miền · ${num(linkTotal)} link đang dùng`}
        actions={
          <RouterLink to={`/p/${projectId}/links`} className="btn">
            ← Link &amp; QR
          </RouterLink>
        }
      />

      <p className="mb-3 max-w-3xl text-body text-muted">
        Mỗi link nằm trên đúng một tên miền và giữ tên miền đó cả đời. Mã chỉ cần duy nhất trong
        phạm vi <span className="font-semibold">(tên miền, mã)</span> — nên{' '}
        <span className="font-mono text-meta">/tet2026</span> tồn tại song song trên hai tên miền
        khác nhau mà không xung đột.
      </p>

      {domains.isPending && <Loading />}
      {domains.isError && <ErrorBanner error={domains.error} retry={() => void domains.refetch()} />}

      {domains.data && domains.data.length === 0 && (
        <Empty
          title="Chưa có tên miền nào"
          hint="Không có tên miền thì không tạo được link: mã rút gọn luôn phải nằm trên một host cụ thể mà redirect nhận ra qua Host header."
        />
      )}

      {domains.data && domains.data.length > 0 && (
        <Table
          head={
            <>
              <Th>Tên miền</Th>
              <Th>Vai trò</Th>
              <Th className="text-right">Link đang dùng</Th>
              <Th>Ví dụ</Th>
              <Th>Thao tác</Th>
            </>
          }
        >
          {domains.data.map((d) => (
            <DomainLine key={d.id} domain={d} mayManage={mayManage} />
          ))}
        </Table>
      )}

      <Card title="Thêm tên miền" className="mt-4 max-w-2xl">
        {!mayManage ? (
          <p className="text-body text-muted">
            Thêm tên miền cần quyền <span className="font-semibold">member.manage</span>, không phải{' '}
            <span className="font-semibold">link.write</span>: nó đổi thứ mà cả deployment trả lời và
            cần thêm bản ghi DNS lẫn chứng chỉ TLS. Người tạo được link không đương nhiên được trỏ một
            tên miền mới vào chúng.
          </p>
        ) : (
          <form onSubmit={submit} className="grid gap-3">
            <Field
              label="Host"
              error={addFailure?.fields.host ?? (addFailure?.body.code === 'host_taken' ? hostTakenHint : undefined)}
              hint="Chỉ tên miền, không kèm scheme hay đường dẫn. Ví dụ: links.acme.vn"
            >
              <input
                id="host"
                className="input"
                placeholder="links.acme.vn"
                inputMode="url"
                value={host}
                onChange={(e) => setHost(e.target.value)}
              />
            </Field>

            <label className="flex items-start gap-2 text-body">
              <input
                type="checkbox"
                className="mt-0.5"
                checked={asDefault}
                onChange={(e) => setAsDefault(e.target.checked)}
              />
              <span>
                Đặt làm mặc định ngay
                <span className="block text-meta text-muted">
                  Chỉ ảnh hưởng link tạo mới. {num(linkTotal)} link hiện có giữ nguyên tên miền của
                  chúng — mã đã in ra không đổi được.
                </span>
              </span>
            </label>

            {add.isError && !addFailure?.fields.host && addFailure?.body.code !== 'host_taken' && (
              <ErrorBanner error={add.error} />
            )}

            <div>
              <button type="submit" className="btn-primary" disabled={add.isPending || !host.trim()}>
                {add.isPending ? 'Đang thêm…' : 'Thêm tên miền'}
              </button>
            </div>
          </form>
        )}

        {added && (
          <div role="status" className="mt-3 rounded border border-ok/50 bg-ok/5 px-3 py-2 text-body">
            <p className="font-semibold text-ok">Đã thêm {added.host}. Còn một bước nữa.</p>
            <p className="mt-0.5 text-muted">{added.next_step}</p>
            <p className="mt-1 text-muted">
              Trước khi DNS và chứng chỉ sẵn sàng, mọi link phát trên tên miền này sẽ không mở được —
              đừng in QR trước bước đó.
            </p>
          </div>
        )}
      </Card>
    </div>
  )
}

const hostTakenHint =
  'Tên miền này đã được đăng ký. Host là duy nhất trên toàn deployment (redirect chỉ có Host header ' +
  'để biết mã thuộc về ai), và hệ thống cố ý không cho biết tổ chức nào đang giữ nó.'

/* ------------------------------------------------------------------ row */

function DomainLine({ domain: d, mayManage }: { domain: DomainRow; mayManage: boolean }) {
  const qc = useQueryClient()
  const [confirming, setConfirming] = useState<'default' | 'delete' | null>(null)

  const setDefault = useMutation({
    mutationFn: async () => api.put<{ message: string }>(`/api/v1/domains/${d.id}/default`),
    onSuccess: () => {
      setConfirming(null)
      void qc.invalidateQueries({ queryKey: ['domains'] })
    },
  })

  const remove = useMutation({
    mutationFn: async () => api.del<void>(`/api/v1/domains/${d.id}`),
    onSuccess: () => {
      setConfirming(null)
      void qc.invalidateQueries({ queryKey: ['domains'] })
    },
  })

  const removeFailure = remove.error instanceof RequestFailed ? remove.error : null
  const inUse = d.link_count > 0

  return (
    <>
      <Tr>
        <Td>
          <div className="font-semibold">{d.host}</div>
          <div className="id-chip">{d.id}</div>
        </Td>
        <Td>
          {d.is_default ? (
            <StatusPill tone="accent">Mặc định</StatusPill>
          ) : (
            <StatusPill>Đang phục vụ</StatusPill>
          )}
        </Td>
        <Td className="text-right font-mono">{num(d.link_count)}</Td>
        <Td className="font-mono text-meta text-muted">{d.short_url_example}</Td>
        <Td className="text-right">
          {mayManage && (
            <div className="flex justify-end gap-1">
              {!d.is_default && (
                <button type="button" className="btn" onClick={() => setConfirming('default')}>
                  Đặt mặc định
                </button>
              )}
              <button
                type="button"
                className="btn text-overdue"
                disabled={d.is_default}
                title={d.is_default ? 'Tên miền mặc định không xoá được — đặt tên miền khác làm mặc định trước.' : undefined}
                onClick={() => setConfirming('delete')}
              >
                Xoá
              </button>
            </div>
          )}
        </Td>
      </Tr>

      {confirming === 'default' && (
        <Tr>
          <Td className="bg-panel" >
            <div className="text-body">
              <p className="font-semibold">Đặt {d.host} làm mặc định?</p>
              {/* Stated here rather than in a toast afterwards: somebody switching
                  the default usually expects existing QR codes to follow, and they
                  will not. */}
              <p className="mt-0.5 text-muted">
                Chỉ <span className="font-semibold">link tạo mới</span> dùng tên miền này. Mọi link
                đã có giữ nguyên tên miền của chúng — kể cả link trên tên miền mặc định hiện tại.
                Không có thao tác nào chuyển link giữa hai tên miền.
              </p>
              {setDefault.isError && (
                <p role="alert" className="mt-1 text-meta text-overdue">
                  {setDefault.error instanceof RequestFailed
                    ? setDefault.error.body.title
                    : 'Không đổi được tên miền mặc định.'}
                </p>
              )}
              <div className="mt-2 flex gap-2">
                <button
                  type="button"
                  className="btn-primary"
                  disabled={setDefault.isPending}
                  onClick={() => setDefault.mutate()}
                >
                  {setDefault.isPending ? 'Đang đổi…' : 'Đổi mặc định'}
                </button>
                <button type="button" className="btn" onClick={() => setConfirming(null)}>
                  Thôi
                </button>
              </div>
            </div>
          </Td>
          <Td className="bg-panel">{null}</Td>
          <Td className="bg-panel">{null}</Td>
          <Td className="bg-panel">{null}</Td>
          <Td className="bg-panel">{null}</Td>
        </Tr>
      )}

      {confirming === 'delete' && (
        <Tr>
          <Td className="bg-overdue/5">
            <div className="text-body">
              <p className="font-semibold text-overdue">Xoá {d.host}?</p>
              {inUse ? (
                // The API will refuse this with 409 domain_in_use. Saying why here
                // means the person reads it before spending a click on it, and
                // knows what to do instead.
                <p className="mt-0.5 text-muted">
                  Tên miền này còn <span className="font-semibold">{num(d.link_count)} link</span>{' '}
                  đang dùng, nên API sẽ từ chối (409 <span className="font-mono">domain_in_use</span>
                  ). Không phải vì thận trọng: xoá tên miền làm mọi mã đã in ra hoặc đã chia sẻ trên
                  đó ngừng phân giải, và không có cách nào chuyển chúng sang tên miền khác. Xoá hoặc
                  để hết hạn từng link trước.
                </p>
              ) : (
                <p className="mt-0.5 text-muted">
                  Tên miền này không còn link nào, nên xoá được. Sau khi xoá, deployment không trả lời
                  host này nữa và bản ghi DNS trỏ về đây nên được gỡ.
                </p>
              )}
              {removeFailure && (
                <p role="alert" className="mt-1 text-meta text-overdue">
                  {removeFailure.body.title}
                </p>
              )}
              <div className="mt-2 flex gap-2">
                <button
                  type="button"
                  className="btn text-overdue"
                  disabled={remove.isPending || inUse}
                  onClick={() => remove.mutate()}
                >
                  {remove.isPending ? 'Đang xoá…' : inUse ? 'Không xoá được' : 'Xoá tên miền'}
                </button>
                <button type="button" className="btn" onClick={() => setConfirming(null)}>
                  Thôi
                </button>
              </div>
            </div>
          </Td>
          <Td className="bg-overdue/5">{null}</Td>
          <Td className="bg-overdue/5">{null}</Td>
          <Td className="bg-overdue/5">{null}</Td>
          <Td className="bg-overdue/5">{null}</Td>
        </Tr>
      )}
    </>
  )
}
