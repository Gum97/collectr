import { useState } from 'react'
import { Link } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RequestFailed, api, type List } from '../../lib/api'
import { useProjects } from '../../lib/projects'
import { can, useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Field,
  Loading,
  PageHeader,
  StatusPill,
  date,
  deadline,
} from '../../components/ui'
import { CapabilityChips, RoleBadge } from './RoleMatrix'
import { ORG_ROLES, PROJECT_ROLES, ROLE_LABELS, capabilitiesOf, requiresMFA } from './roles'

/**
 * Wireframe 2d. Offering somebody access, and keeping track of the offers still open.
 *
 * The consequences of the role are spelled out under the form rather than after the
 * fact. An invitation is the moment a permission is decided, and it is decided by
 * someone who is thinking about a person ("Linh from the campaigns team"), not about
 * `submission.read_sensitive`.
 */

interface InvitationRow {
  id: string
  email: string
  org_role: string
  expires_at: string
  created_at: string
  /** Not returned by GET /api/v1/members/invitations today. */
  invited_by?: string
  invited_by_email?: string
  /** Likewise absent, which is why "Gửi lại" cannot carry project grants over. */
  project_grants?: { project_id: string; role: string }[]
}

interface MemberRow {
  user_id: string
  email: string
  name: string
  role: string
}

export function Invitations() {
  const me = useMe()
  const qc = useQueryClient()
  const allowed = can(me.data, 'member.manage')

  const [email, setEmail] = useState('')
  const [orgRole, setOrgRole] = useState<string>('member')
  const [projectId, setProjectId] = useState('')
  const [projectRole, setProjectRole] = useState<string>('viewer')
  const [sent, setSent] = useState<string | null>(null)

  const projects = useProjects()
  const grantable = (projects.data ?? []).filter((p) => p.access !== 'none' && !p.archived_at)

  const invitations = useQuery({
    queryKey: ['invitations'],
    queryFn: async () =>
      (await api.get<List<InvitationRow>>('/api/v1/members/invitations')).data,
    enabled: allowed,
  })

  const members = useQuery({
    queryKey: ['members'],
    queryFn: async () => (await api.get<List<MemberRow>>('/api/v1/members')).data,
    enabled: allowed,
  })

  const invite = useMutation({
    mutationFn: async (v: {
      email: string
      org_role: string
      project_grants: { project_id: string; role: string }[]
    }) => api.post<{ id: string; email: string }>('/api/v1/members/invitations', v),
    onSuccess: (res) => {
      setSent(res.email)
      setEmail('')
      setProjectId('')
      qc.invalidateQueries({ queryKey: ['invitations'] })
    },
  })

  const revoke = useMutation({
    mutationFn: async (id: string) => api.del<void>(`/api/v1/members/invitations/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['invitations'] }),
  })

  if (me.isPending) return <Loading />
  if (!allowed) {
    return (
      <div className="p-6">
        <PageHeader title="Mời vào tổ chức" />
        <Empty
          title="Bạn không có quyền mời thành viên"
          hint={
            <>
              Cần capability <code className="font-mono">member.manage</code> — chỉ vai trò{' '}
              <code className="font-mono">owner</code> và <code className="font-mono">admin</code>{' '}
              có.
            </>
          }
        />
      </div>
    )
  }

  const fieldErrors = invite.error instanceof RequestFailed ? invite.error.fields : {}
  const conflict = invite.error instanceof RequestFailed && invite.error.status === 409

  function submit(e: React.FormEvent) {
    e.preventDefault()
    setSent(null)
    invite.mutate({
      email: email.trim(),
      org_role: orgRole,
      project_grants: projectId ? [{ project_id: projectId, role: projectRole }] : [],
    })
  }

  return (
    <div className="p-6">
      <PageHeader
        title="Mời vào tổ chức"
        meta={`${members.data?.length ?? 0} thành viên · ${invitations.data?.length ?? 0} lời mời chờ`}
        actions={
          <Link to="/members" className="btn">
            ← Thành viên
          </Link>
        }
      />

      <div className="grid gap-3 lg:grid-cols-[1.4fr_1fr]">
        <Card title="Lời mời mới">
          <form onSubmit={submit} className="flex flex-col gap-3">
            <div className="grid gap-3 sm:grid-cols-[1.6fr_1fr]">
              <Field label="Email" error={fieldErrors['email']}>
                <input
                  type="email"
                  required
                  autoComplete="off"
                  className="input"
                  placeholder="ten@congty.vn"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
              </Field>
              <Field
                label="Vai trò tổ chức"
                error={fieldErrors['org_role']}
                hint={requiresMFA(orgRole) ? undefined : 'Không bắt buộc MFA'}
              >
                <select
                  className="input"
                  value={orgRole}
                  onChange={(e) => setOrgRole(e.target.value)}
                >
                  {ORG_ROLES.map((r) => (
                    <option key={r} value={r}>
                      {ROLE_LABELS[r]} ({r})
                    </option>
                  ))}
                </select>
              </Field>
            </div>

            <fieldset className="rounded border border-dashed border-line p-2.5">
              <legend className="px-1 text-meta font-semibold">
                Cấp luôn vai trò ở một dự án (tuỳ chọn)
              </legend>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Dự án" error={fieldErrors['project_grants']}>
                  <select
                    className="input"
                    value={projectId}
                    onChange={(e) => setProjectId(e.target.value)}
                  >
                    <option value="">— không cấp bây giờ —</option>
                    {grantable.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label="Vai trò dự án">
                  <select
                    className="input"
                    disabled={!projectId}
                    value={projectRole}
                    onChange={(e) => setProjectRole(e.target.value)}
                  >
                    {PROJECT_ROLES.map((r) => (
                      <option key={r} value={r}>
                        {ROLE_LABELS[r]} ({r})
                      </option>
                    ))}
                  </select>
                </Field>
              </div>
              {orgRole === 'member' && !projectId && (
                <p className="mt-2 text-meta text-duesoon">
                  ⚠ Vai trò <code className="font-mono">member</code> không có quyền nào ở cấp tổ
                  chức. Không cấp dự án nào thì người này đăng nhập vào và <strong>không thấy
                  gì</strong>. Có thể cấp sau ở màn Thành viên.
                </p>
              )}
            </fieldset>

            {invite.isError && (
              <ErrorBanner
                error={
                  conflict
                    ? new RequestFailed(409, {
                        title: 'Người này đã là thành viên của tổ chức.',
                        status: 409,
                        code: 'already_member',
                      })
                    : invite.error
                }
              />
            )}

            {sent && (
              <p
                role="status"
                className="rounded border border-ok/40 bg-ok/5 px-3 py-2 text-body text-ok"
              >
                Đã gửi lời mời tới <strong>{sent}</strong>. Lời mời có hiệu lực 7 ngày.
              </p>
            )}

            <div>
              <button type="submit" className="btn-primary" disabled={invite.isPending || !email}>
                {invite.isPending ? 'Đang gửi…' : 'Gửi lời mời'}
              </button>
            </div>
          </form>
        </Card>

        <Card title="Vai trò này kèm theo" aside={`${capabilitiesOf(orgRole).length + (projectId ? capabilitiesOf(projectRole).length : 0)} capability`}>
          <CapabilityChips orgRole={orgRole} projectRole={projectId ? projectRole : undefined} />
          {requiresMFA(orgRole) && (
            <p className="mt-2 rounded border border-duesoon/50 bg-duesoon/5 px-2.5 py-2 text-meta text-duesoon">
              ⚠ Vai trò <code className="font-mono">{orgRole}</code> chạm được dữ liệu cá nhân của
              mọi dự án, nên người nhận <strong>phải bật xác thực 2 lớp (TOTP)</strong> trước khi
              vào được.
            </p>
          )}
          <p className="mt-2 text-meta text-muted">
            Không có custom role: 8 vai trò đặt sẵn là toàn bộ lựa chọn.{' '}
            <Link to="/members/roles" className="text-accent underline">
              Xem ma trận đầy đủ
            </Link>
            .
          </p>
        </Card>
      </div>

      <h2 className="mb-2 mt-5 text-body font-semibold">Đang chờ chấp nhận</h2>

      {invitations.isPending && <Loading label="Đang tải lời mời…" />}
      {invitations.isError && (
        <ErrorBanner error={invitations.error} retry={() => invitations.refetch()} />
      )}

      {invitations.data?.length === 0 && (
        <Empty
          title="Không có lời mời nào đang chờ"
          hint="Lời mời hết hạn sau 7 ngày và tự biến mất khỏi danh sách này. Ai đã chấp nhận sẽ xuất hiện ở màn Thành viên."
        />
      )}

      {(invitations.data ?? []).length > 0 && (
        <Card>
          <ul className="flex flex-col divide-y divide-line">
            {(invitations.data ?? []).map((inv) => {
              const left = deadline(inv.expires_at)
              return (
                <li
                  key={inv.id}
                  className="flex flex-wrap items-center justify-between gap-3 py-2.5 first:pt-0 last:pb-0"
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-body">{inv.email}</span>
                      <RoleBadge role={inv.org_role} />
                    </div>
                    <div className="id-chip">
                      gửi {date(inv.created_at)} · người mời:{' '}
                      {inv.invited_by_email ?? inv.invited_by ?? 'không rõ'}
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <StatusPill tone={left.tone}>{left.text}</StatusPill>
                    <button
                      type="button"
                      className="btn"
                      disabled={invite.isPending}
                      onClick={() =>
                        invite.mutate({
                          email: inv.email,
                          org_role: inv.org_role,
                          // The list endpoint does not return the original grants, so a
                          // resend cannot reproduce them. Sending an empty list is the
                          // honest option: it under-grants rather than guessing, and the
                          // note below says so.
                          project_grants: inv.project_grants ?? [],
                        })
                      }
                    >
                      Gửi lại
                    </button>
                    <button
                      type="button"
                      className="btn border-overdue text-overdue"
                      disabled={revoke.isPending}
                      onClick={() => {
                        if (window.confirm(`Thu hồi lời mời gửi tới ${inv.email}?`))
                          revoke.mutate(inv.id)
                      }}
                    >
                      Thu hồi
                    </button>
                  </div>
                </li>
              )
            })}
          </ul>

          {revoke.isError && (
            <div className="mt-3">
              <ErrorBanner error={revoke.error} />
            </div>
          )}

          <p className="mt-3 border-t border-dashed border-line pt-2 text-meta text-muted">
            “Gửi lại” tạo một lời mời mới cho cùng địa chỉ và cùng vai trò tổ chức, với hạn 7 ngày
            mới; lời mời cũ mất hiệu lực ngay.{' '}
            <span className="text-duesoon">
              Quyền dự án kèm theo lời mời cũ không giữ lại được — API chưa trả về chúng, hãy cấp
              lại ở màn Thành viên sau khi người này tham gia.
            </span>
          </p>
        </Card>
      )}
    </div>
  )
}
