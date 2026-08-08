/**
 * Cài đặt tổ chức — and the line between two kinds of configuration.
 *
 * The sidebar has linked here since the shell was written; the route did not
 * exist, so the link landed on the "no such page" screen. That page is written
 * for a mistyped URL, and this was a button the application drew itself.
 *
 * The screen is in two halves on purpose. The top is what the organisation owns
 * and may change. The bottom is what the deployment was started with, shown so
 * an operator can verify it without an SSH session, and read-only because
 * storage endpoints and keys belong to whoever runs the process.
 *
 * That is not tidiness. An administrator who could edit the storage endpoint
 * here could point every future attachment at a bucket they own, from a stolen
 * session, without touching the server — and changing it at runtime would
 * strand everything already written, since files.storage_key would address a
 * bucket the process no longer has.
 */
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router'
import { api, RequestFailed } from '../lib/api'
import { can, useMe } from '../lib/session'
import { Callout, Card, ErrorBanner, Field, Loading, PageHeader } from '../components/ui'

interface Deployment {
  storage_driver: string
  mail_configured: boolean
  base_url: string
  short_url_base: string
  default_retention_days: number
  dsr_sla_hours: number
  mfa_grace_hours: number
  public_write_ip_limit: number
  public_write_form_limit: number
}

interface Org {
  id: string
  name: string
  slug: string
  settings: Record<string, unknown> | null
  deployment: Deployment
}

export function OrgSettings() {
  const me = useMe()
  const qc = useQueryClient()
  const mayManage = can(me.data, 'member.manage')

  const org = useQuery({
    queryKey: ['org'],
    queryFn: () => api.get<Org>('/api/v1/org'),
    enabled: mayManage,
  })

  const [name, setName] = useState('')
  const [saved, setSaved] = useState(false)

  // Seeded from the server once it answers, not on every render: typing into
  // the box must not be undone by a background refetch.
  useEffect(() => {
    if (org.data) setName(org.data.name)
  }, [org.data])

  const save = useMutation({
    mutationFn: () =>
      api.patch<void>('/api/v1/org', {
        name: name.trim(),
        settings: org.data?.settings ?? {},
      }),
    onSuccess: () => {
      setSaved(true)
      void qc.invalidateQueries({ queryKey: ['org'] })
    },
  })

  if (!mayManage) {
    return (
      <div className="p-6">
        <PageHeader title="Cài đặt tổ chức" />
        <Callout tone="neutral" title="Không đủ quyền">
          Màn này cần quyền <span className="font-mono">member.manage</span>. Vai trò của bạn xem
          được dữ liệu nhưng không đổi được cấu hình tổ chức.
        </Callout>
      </div>
    )
  }

  const d = org.data?.deployment
  const fieldErrors = save.error instanceof RequestFailed ? (save.error.fields ?? {}) : {}

  return (
    <div className="p-6">
      <PageHeader
        title="Cài đặt tổ chức"
        meta={org.data ? `${org.data.slug} · ${org.data.id}` : undefined}
      />

      {org.isPending && <Loading />}
      {org.error && <ErrorBanner error={org.error} retry={() => void org.refetch()} />}

      {org.data && (
        <div className="grid gap-4">
          <Card title="Tổ chức" aside="tổ chức tự đổi được">
            <div className="grid gap-3">
              <Field
                label="Tên tổ chức"
                error={fieldErrors['name']}
                hint="Tên này in trên văn bản đồng ý mà người điền form đọc, với tư cách bên thu thập dữ liệu. Đổi tên không viết lại các lượt đồng ý đã ghi — chúng dẫn chiếu một bản văn theo hash — nhưng nó đổi tên bên mà người tiếp theo được cho biết là họ đang giao dịch cùng."
              >
                <input
                  className="input"
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value)
                    setSaved(false)
                  }}
                />
              </Field>

              <Field
                label="Định danh (slug)"
                hint="Không sửa được. Nó nằm trong đường dẫn cố định của các văn bản đồng ý mà chủ thể dữ liệu đã được đưa — một văn bản đổi địa chỉ sau đó thì không còn là bằng chứng đã được viện dẫn nữa."
              >
                <input className="input font-mono" value={org.data.slug} disabled />
              </Field>

              <div className="flex items-center gap-3">
                <button
                  type="button"
                  className="btn-primary"
                  disabled={save.isPending || name.trim() === '' || name === org.data.name}
                  onClick={() => save.mutate()}
                >
                  {save.isPending ? 'Đang lưu…' : 'Lưu'}
                </button>
                {saved && <span className="text-meta text-ok">Đã lưu.</span>}
              </div>
              {save.error && !Object.keys(fieldErrors).length && <ErrorBanner error={save.error} />}
            </div>
          </Card>

          <Card title="Nội dung tuân thủ" aside="sửa ở màn riêng">
            <div className="grid gap-1.5 text-body">
              <p>
                <Link className="underline" to="/compliance?tab=purposes">
                  Mục đích xử lý
                </Link>{' '}
                và{' '}
                <Link className="underline" to="/compliance?tab=documents">
                  văn bản đồng ý
                </Link>{' '}
                nằm ở Trung tâm tuân thủ, không phải ở đây.
              </p>
              <p className="text-meta text-muted">
                Chúng có version và có hash. Một ô nhập nằm lẫn giữa các cài đặt khác sẽ khiến việc
                sửa chúng trông ngang hàng với đổi tên tổ chức, trong khi publish một văn bản mới là
                một hành vi pháp lý.
              </p>
            </div>
          </Card>

          {d && (
            <Card title="Cấu hình triển khai" aside="chỉ đọc">
              <Callout tone="neutral" title="Đây là cấu hình của máy chủ, không phải của tổ chức">
                Những giá trị dưới đây do người vận hành đặt bằng biến môi trường và{' '}
                <span className="font-semibold">không sửa được từ giao diện</span>. Điểm cuối lưu
                trữ và khoá bí mật không hiển thị ở đây, kể cả cho chủ sở hữu: một phiên quản trị bị
                chiếm mà đổi được điểm cuối lưu trữ là mọi tệp đính kèm về sau đi thẳng sang chỗ
                khác — không cần chạm tới máy chủ.
              </Callout>

              <dl className="mt-3 grid gap-x-6 gap-y-2 sm:grid-cols-2">
                <Fact label="Lưu trữ tệp" value={d.storage_driver === 's3' ? 'S3 (tương thích)' : 'Đĩa cục bộ'} />
                <Fact
                  label="Gửi email"
                  value={d.mail_configured ? 'đã cấu hình SMTP' : 'chưa cấu hình'}
                  warn={!d.mail_configured}
                  note={
                    d.mail_configured
                      ? undefined
                      : 'Lời mời và liên kết cho chủ thể dữ liệu sẽ không gửi được. Hệ thống vẫn chạy bình thường, nên lỗi này im lặng.'
                  }
                />
                <Fact label="Origin ứng dụng" value={d.base_url} mono />
                <Fact label="Origin link rút gọn" value={d.short_url_base || d.base_url} mono />
                <Fact label="Hạn lưu mặc định" value={`${d.default_retention_days} ngày`} />
                <Fact label="Hạn trả lời DSR" value={`${d.dsr_sla_hours} giờ`} />
                <Fact label="Ân hạn bật 2FA" value={`${d.mfa_grace_hours} giờ`} />
                <Fact
                  label="Giới hạn gửi công khai"
                  value={`${d.public_write_ip_limit}/phút mỗi dải /24 · ${d.public_write_form_limit}/phút mỗi biểu mẫu`}
                  note="Quá thấp cho gian hàng hội chợ, nơi mọi người đi chung một NAT."
                />
              </dl>
            </Card>
          )}
        </div>
      )}
    </div>
  )
}

function Fact({
  label,
  value,
  note,
  mono,
  warn,
}: {
  label: string
  value: string
  note?: string
  mono?: boolean
  warn?: boolean
}) {
  return (
    <div>
      <dt className="text-meta uppercase tracking-label text-faint">{label}</dt>
      <dd className={`text-body ${mono ? 'font-mono text-meta' : ''} ${warn ? 'text-overdue' : ''}`}>
        {value}
      </dd>
      {note && <p className="mt-0.5 text-meta text-muted">{note}</p>}
    </div>
  )
}
