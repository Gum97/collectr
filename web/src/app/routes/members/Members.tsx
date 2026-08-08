import { useState } from 'react'
import { Link } from 'react-router'
import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, type List } from '../../lib/api'
import { useProjects, type Project } from '../../lib/projects'
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
  dateTime,
} from '../../components/ui'
import { CapabilityChips, RoleBadge } from './RoleMatrix'
import {
  ORG_ROLES,
  PROJECT_ROLES,
  ROLE_LABELS,
  ROLE_SUMMARY,
  grants,
  projectReachLabel,
  requiresMFA,
  spansAllProjects,
} from './roles'

/**
 * Wireframe 1r. Who is in the organisation, what that lets them do, and where the
 * second factor is missing.
 *
 * The screen answers two different questions and keeps them apart on purpose: an
 * organisation role, which is the same everywhere, and a set of project grants, which
 * are not. Someone can be a plain `member` at the organisation level and still be the
 * manager of one project -- flattening those into a single "role" column is how an
 * administrator ends up granting `admin` to give somebody access to one campaign.
 */

interface MemberRow {
  user_id: string
  email: string
  name: string
  role: string
  joined_at: string
  /** Not returned by GET /api/v1/members yet. Optional rather than defaulted to false:
   *  "we do not know" and "no second factor" must not look the same on a screen whose
   *  job is to find accounts without one. */
  mfa_enabled?: boolean
  last_login_at?: string | null
}

interface ProjectMemberRow {
  user_id: string
  email: string
  name: string
  role: string
  granted_at: string
}

interface InvitationRow {
  id: string
  email: string
  org_role: string
  expires_at: string
  created_at: string
}

/** One person's grant inside one project. */
interface Grant {
  project: Project
  role: string
}

export function Members() {
  const me = useMe()
  const qc = useQueryClient()
  const [selected, setSelected] = useState<string | null>(null)
  const [editingRole, setEditingRole] = useState<{ userId: string; next: string } | null>(null)

  const allowed = can(me.data, 'member.manage')

  const members = useQuery({
    queryKey: ['members'],
    queryFn: async () => (await api.get<List<MemberRow>>('/api/v1/members')).data,
    enabled: allowed,
  })

  const invitations = useQuery({
    queryKey: ['invitations'],
    queryFn: async () =>
      (await api.get<List<InvitationRow>>('/api/v1/members/invitations')).data,
    enabled: allowed,
  })

  const projects = useProjects()
  // Only the projects this administrator may administer. The API refuses the member
  // list of any other, and asking anyway would fill the screen with 403s.
  const readable = (projects.data ?? []).filter((p) => p.access !== 'none' && !p.archived_at)

  const projectMembers = useQueries({
    queries: readable.map((p) => ({
      queryKey: ['project-members', p.id],
      queryFn: async () =>
        (await api.get<List<ProjectMemberRow>>(`/api/v1/projects/${p.id}/members`)).data,
      enabled: allowed,
      retry: false,
    })),
  })

  const grantsByUser = new Map<string, Grant[]>()
  let unreadableProjects = 0
  readable.forEach((project, i) => {
    const q = projectMembers[i]
    if (!q) return
    if (q.isError) {
      unreadableProjects += 1
      return
    }
    for (const row of q.data ?? []) {
      const list = grantsByUser.get(row.user_id) ?? []
      list.push({ project, role: row.role })
      grantsByUser.set(row.user_id, list)
    }
  })

  const ownerCount = (members.data ?? []).filter((m) => m.role === 'owner').length
  const selectedMember = (members.data ?? []).find((m) => m.user_id === selected) ?? null

  const grantRole = useMutation({
    mutationFn: async (v: { projectId: string; userId: string; role: string }) =>
      api.put<void>(`/api/v1/projects/${v.projectId}/members/${v.userId}`, { role: v.role }),
    onSuccess: (_d, v) => qc.invalidateQueries({ queryKey: ['project-members', v.projectId] }),
  })

  const revokeRole = useMutation({
    mutationFn: async (v: { projectId: string; userId: string }) =>
      api.del<void>(`/api/v1/projects/${v.projectId}/members/${v.userId}`),
    onSuccess: (_d, v) => qc.invalidateQueries({ queryKey: ['project-members', v.projectId] }),
  })

  const removeMember = useMutation({
    mutationFn: async (userId: string) => api.del<void>(`/api/v1/members/${userId}`),
    onSuccess: () => {
      setSelected(null)
      qc.invalidateQueries({ queryKey: ['members'] })
    },
  })

  if (me.isPending) return <Loading />
  if (!allowed) {
    return (
      <div className="p-6">
        <PageHeader title="Thành viên & quyền" />
        <Empty
          title="Bạn không có quyền quản lý thành viên"
          hint={
            <>
              Trang này cần capability <code className="font-mono">member.manage</code>, chỉ có ở
              vai trò tổ chức <code className="font-mono">owner</code> và{' '}
              <code className="font-mono">admin</code>. Hãy nhờ một trong hai cấp quyền, hoặc{' '}
              <Link to="/members/roles" className="text-accent underline">
                xem quyền của từng vai trò
              </Link>
              .
            </>
          }
        />
      </div>
    )
  }

  return (
    <div className="p-6">
      <PageHeader
        title="Thành viên & quyền"
        meta="quyền hiệu lực = quyền tổ chức ∪ quyền dự án"
        actions={
          <>
            <Link to="/members/roles" className="btn">
              Ma trận 8 vai trò
            </Link>
            <Link to="/members/invitations" className="btn-primary">
              + Mời
            </Link>
          </>
        }
      />

      {members.isError && (
        <div className="mb-3">
          <ErrorBanner error={members.error} retry={() => members.refetch()} />
        </div>
      )}
      {members.isPending && <Loading label="Đang tải danh sách thành viên…" />}

      {members.data && members.data.length === 0 && (
        <Empty
          title="Chưa có ai ngoài bạn"
          hint="Tổ chức mới tạo chỉ có tài khoản chủ sở hữu. Mời đồng nghiệp để họ nhận vai trò riêng ở từng dự án."
        />
      )}

      {members.data && members.data.length > 0 && (
        <Table
          head={
            <>
              <Th>Người dùng</Th>
              <Th>Vai trò org</Th>
              <Th>Quyền dự án</Th>
              <Th>MFA</Th>
              <Th>Đăng nhập cuối</Th>
              <Th className="text-right">&nbsp;</Th>
            </>
          }
        >
          {members.data.map((m) => (
            <Tr key={m.user_id} className={selected === m.user_id ? 'bg-chrome/50' : ''}>
              <Td>
                <button
                  type="button"
                  onClick={() => setSelected(selected === m.user_id ? null : m.user_id)}
                  className="text-left"
                  aria-expanded={selected === m.user_id}
                >
                  <span className="text-body font-semibold">{m.name || m.email}</span>
                  <span className="id-chip block">{m.email}</span>
                </button>
              </Td>
              <Td>
                <RoleBadge role={m.role} />
                {m.user_id === me.data?.user_id && (
                  <span className="id-chip block">là bạn</span>
                )}
              </Td>
              <Td>
                <ProjectReach role={m.role} grants={grantsByUser.get(m.user_id) ?? []} />
              </Td>
              <Td>
                <MFACell enabled={m.mfa_enabled} role={m.role} />
              </Td>
              <Td className="font-mono text-meta text-muted">
                {m.last_login_at === undefined ? '—' : dateTime(m.last_login_at)}
              </Td>
              <Td className="text-right">
                <button
                  type="button"
                  className="btn"
                  onClick={() => setSelected(selected === m.user_id ? null : m.user_id)}
                >
                  {selected === m.user_id ? 'Đóng' : 'Quyền'}
                </button>
              </Td>
            </Tr>
          ))}

          {/* Pending invitations sit in the same table, dimmed. They are people who will
              hold these permissions shortly, and a separate screen makes it easy to
              approve an invitation and then forget it exists. */}
          {(invitations.data ?? []).map((inv) => (
            <Tr key={inv.id} className="bg-panel">
              <Td>
                <span className="text-body text-muted">{inv.email}</span>
                <span className="id-chip block">
                  mời {new Date(inv.created_at).toLocaleDateString('vi-VN')} · hết hạn{' '}
                  {new Date(inv.expires_at).toLocaleDateString('vi-VN')}
                </span>
              </Td>
              <Td>
                <RoleBadge role={inv.org_role} />
              </Td>
              <Td className="text-meta text-muted">—</Td>
              <Td>
                <span className="id-chip">chờ</span>
              </Td>
              <Td className="text-meta text-muted">—</Td>
              <Td className="text-right">
                <Link to="/members/invitations" className="btn">
                  Lời mời
                </Link>
              </Td>
            </Tr>
          ))}
        </Table>
      )}

      <p className="mt-2 text-meta text-muted">
        Không có custom role. Mọi thay đổi vai trò được ghi vào nhật ký audit và phiên của người
        bị đổi bị thu hồi trong vòng 60 giây.
        {unreadableProjects > 0 && (
          <>
            {' '}
            <span className="text-duesoon">
              ⚠ {unreadableProjects} dự án không đọc được danh sách thành viên — cột “Quyền dự
              án” chưa đầy đủ.
            </span>
          </>
        )}
      </p>

      {selectedMember && (
        <div className="mt-4 grid gap-3 lg:grid-cols-2">
          <MemberDetail
            // Keyed so switching person resets the panel's own state rather than
            // showing one member's name over another member's project preview.
            key={selectedMember.user_id}
            member={selectedMember}
            grants={grantsByUser.get(selectedMember.user_id) ?? []}
            projects={readable}
            isSelf={selectedMember.user_id === me.data?.user_id}
            ownerCount={ownerCount}
            editingRole={editingRole?.userId === selectedMember.user_id ? editingRole.next : null}
            onEditRole={(next) =>
              setEditingRole(next === null ? null : { userId: selectedMember.user_id, next })
            }
            onGrant={(projectId, role) =>
              grantRole.mutate({ projectId, userId: selectedMember.user_id, role })
            }
            onRevoke={(projectId) =>
              revokeRole.mutate({ projectId, userId: selectedMember.user_id })
            }
            onRemove={() => removeMember.mutate(selectedMember.user_id)}
            busy={grantRole.isPending || revokeRole.isPending || removeMember.isPending}
            error={grantRole.error ?? revokeRole.error ?? removeMember.error}
          />
        </div>
      )}
    </div>
  )
}

/** The "quyền dự án" cell.
 *
 * Reach across projects is read off the role model rather than guessed: an org role
 * that grants anything at all spans every project, which is the same test the API uses
 * when it fills in `project.access`. */
function ProjectReach({ role, grants: held }: { role: string; grants: Grant[] }) {
  const inherited = projectReachLabel(role)
  if (inherited) return <span className="text-meta text-muted">{inherited}</span>
  if (held.length === 0) {
    return (
      <span className="text-meta text-duesoon">
        chưa được cấp ở dự án nào — chưa thấy được gì
      </span>
    )
  }
  return (
    <div className="flex flex-col gap-0.5">
      {held.map((g) => (
        <span key={g.project.id} className="text-meta">
          {g.project.name} · <span className="font-semibold">{g.role}</span>
        </span>
      ))}
    </div>
  )
}

function MFACell({ enabled, role }: { enabled: boolean | undefined; role: string }) {
  if (enabled === undefined) {
    // Rule: an unknown is not a "no". The list endpoint does not carry this yet, and
    // rendering it as "tắt" would send somebody chasing a problem that may not exist.
    return <span className="id-chip">chưa rõ</span>
  }
  if (enabled) return <span className="id-chip">bật ✓</span>
  return requiresMFA(role) ? (
    <StatusPill tone="overdue">tắt · bắt buộc</StatusPill>
  ) : (
    <span className="id-chip">tắt</span>
  )
}

function MemberDetail({
  member,
  grants: held,
  projects,
  isSelf,
  ownerCount,
  editingRole,
  onEditRole,
  onGrant,
  onRevoke,
  onRemove,
  busy,
  error,
}: {
  member: MemberRow
  grants: Grant[]
  projects: Project[]
  isSelf: boolean
  ownerCount: number
  editingRole: string | null
  onEditRole: (next: string | null) => void
  onGrant: (projectId: string, role: string) => void
  onRevoke: (projectId: string) => void
  onRemove: () => void
  busy: boolean
  error: unknown
}) {
  const [newProject, setNewProject] = useState('')
  const [newRole, setNewRole] = useState<string>('viewer')
  const [previewProject, setPreviewProject] = useState<string>(held[0]?.project.id ?? '')

  const ungranted = projects.filter((p) => !held.some((g) => g.project.id === p.id))
  const previewRole = held.find((g) => g.project.id === previewProject)?.role
  const warning = roleChangeWarning({ member, next: editingRole, isSelf, ownerCount })
  const removalWarning = isSelf && member.role === 'owner' && ownerCount <= 1

  return (
    <>
      <Card title={member.name || member.email} aside={member.email}>
        {error ? (
          <div className="mb-3">
            <ErrorBanner error={error} />
          </div>
        ) : null}

        <fieldset className="border-0 p-0">
          <legend className="text-meta font-semibold">Vai trò tổ chức</legend>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <select
              aria-label="Vai trò tổ chức"
              className="input w-auto"
              value={editingRole ?? member.role}
              onChange={(e) => onEditRole(e.target.value === member.role ? null : e.target.value)}
            >
              {ORG_ROLES.map((r) => (
                <option key={r} value={r}>
                  {r} — {ROLE_LABELS[r]}
                </option>
              ))}
            </select>
            {editingRole && (
              <>
                <button
                  type="button"
                  className="btn-primary"
                  disabled
                  title="Chưa có endpoint đổi vai trò tổ chức."
                >
                  Đổi vai trò
                </button>
                <button type="button" className="btn" onClick={() => onEditRole(null)}>
                  Huỷ
                </button>
              </>
            )}
          </div>

          <p className="mt-1.5 text-meta text-muted">
            {ROLE_SUMMARY[(editingRole ?? member.role) as keyof typeof ROLE_SUMMARY] ?? ''}
          </p>

          {warning && (
            <p
              role="alert"
              className={`mt-2 rounded border px-2.5 py-2 text-meta leading-relaxed ${
                warning.level === 'block'
                  ? 'border-overdue/50 bg-overdue/5 text-overdue'
                  : 'border-duesoon/50 bg-duesoon/5 text-duesoon'
              }`}
            >
              <strong>{warning.level === 'block' ? '⛔ ' : '⚠ '}</strong>
              {warning.text}
            </p>
          )}

          {editingRole && (
            <p className="mt-2 text-meta text-muted">
              API chưa có endpoint đổi vai trò tổ chức. Cần{' '}
              <code className="font-mono">PUT /api/v1/members/{'{id}'}</code> với{' '}
              <code className="font-mono">{'{ "role": "…" }'}</code>; hiện chỉ có DELETE. Cho tới
              lúc đó, cách đổi vai trò là gỡ khỏi tổ chức rồi mời lại với vai trò mới.
            </p>
          )}
        </fieldset>

        <div className="mt-4 border-t border-line pt-3">
          <p className="text-meta font-semibold">Gỡ khỏi tổ chức</p>
          <p className="mt-0.5 text-meta text-muted">
            Thu hồi mọi phiên và API key người này tạo, trong vòng 60 giây. Nhật ký audit vẫn giữ
            tham chiếu tới họ.
          </p>
          {removalWarning ? (
            <p role="alert" className="mt-2 text-meta font-semibold text-overdue">
              ⛔ Bạn là chủ sở hữu duy nhất. Gỡ chính mình sẽ để tổ chức không còn ai quản trị
              được — hãy chỉ định một chủ sở hữu khác trước.
            </p>
          ) : (
            <button
              type="button"
              className="btn mt-2 border-overdue text-overdue"
              disabled={busy}
              onClick={() => {
                if (
                  window.confirm(
                    `Gỡ ${member.email} khỏi tổ chức? Mọi phiên của họ sẽ bị thu hồi.`,
                  )
                )
                  onRemove()
              }}
            >
              Gỡ khỏi tổ chức
            </button>
          )}
        </div>
      </Card>

      <Card
        title="Quyền theo dự án"
        aside={spansAllProjects(member.role) ? 'kế thừa từ vai trò tổ chức' : `${held.length} dự án`}
      >
        {spansAllProjects(member.role) && (
          <p className="mb-2 rounded border border-dashed border-line px-2 py-1.5 text-meta text-muted">
            Vai trò <code className="font-mono">{member.role}</code> đã với tới mọi dự án. Cấp
            thêm vai trò dự án ở đây chỉ có tác dụng nếu sau này họ bị hạ xuống{' '}
            <code className="font-mono">member</code>.
          </p>
        )}

        {held.length === 0 ? (
          <p className="text-meta text-muted">Chưa được cấp vai trò ở dự án nào.</p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {held.map((g) => (
              <li key={g.project.id} className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate text-body">{g.project.name}</span>
                <select
                  aria-label={`Vai trò của ${member.email} ở ${g.project.name}`}
                  className="input w-auto py-1 text-meta"
                  value={g.role}
                  disabled={busy}
                  onChange={(e) => onGrant(g.project.id, e.target.value)}
                >
                  {PROJECT_ROLES.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                </select>
                <button
                  type="button"
                  className="btn py-1 text-meta"
                  disabled={busy}
                  onClick={() => onRevoke(g.project.id)}
                >
                  Gỡ
                </button>
              </li>
            ))}
          </ul>
        )}

        {ungranted.length > 0 && (
          <div className="mt-3 flex flex-wrap items-end gap-2 border-t border-line pt-3">
            <label className="flex flex-col gap-1">
              <span className="text-meta font-semibold">Cấp thêm dự án</span>
              <select
                className="input w-auto"
                value={newProject}
                onChange={(e) => setNewProject(e.target.value)}
              >
                <option value="">Chọn dự án…</option>
                {ungranted.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-meta font-semibold">Vai trò</span>
              <select
                className="input w-auto"
                value={newRole}
                onChange={(e) => setNewRole(e.target.value)}
              >
                {PROJECT_ROLES.map((r) => (
                  <option key={r} value={r}>
                    {r} — {ROLE_LABELS[r]}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              className="btn-primary"
              disabled={!newProject || busy}
              onClick={() => {
                onGrant(newProject, newRole)
                setNewProject('')
              }}
            >
              Cấp quyền
            </button>
          </div>
        )}

        {/* The wireframe's "Trần Hoà thấy được gì ở dự án CSKH (analyst)". Effective
            capabilities for one person in one project, with the withheld ones named. */}
        <div className="mt-3 rounded border border-dashed border-line p-2.5">
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <span className="text-meta font-semibold">
              {member.name || member.email} thấy được gì ở
            </span>
            <select
              aria-label="Xem quyền hiệu lực ở dự án"
              className="input w-auto py-0.5 text-meta"
              value={previewProject}
              onChange={(e) => setPreviewProject(e.target.value)}
            >
              <option value="">— chỉ vai trò tổ chức —</option>
              {held.map((g) => (
                <option key={g.project.id} value={g.project.id}>
                  {g.project.name} ({g.role})
                </option>
              ))}
            </select>
          </div>
          <CapabilityChips orgRole={member.role} projectRole={previewRole} />
          {previewRole === 'analyst' && (
            <p className="mt-1.5 text-meta text-muted">
              Xuất được nhưng field nhạy cảm trong file ra <span className="font-mono">••••</span>.
            </p>
          )}
        </div>
      </Card>
    </>
  )
}

interface RoleWarning {
  level: 'block' | 'warn'
  text: string
}

/**
 * Warns before somebody locks themselves out.
 *
 * The API refuses to remove the last owner, but by then the administrator has already
 * committed to the action and gets a 409 they have to interpret. Saying it before the
 * click is the difference between a guard rail and an error message.
 */
export function roleChangeWarning({
  member,
  next,
  isSelf,
  ownerCount,
}: {
  member: MemberRow
  next: string | null
  isSelf: boolean
  ownerCount: number
}): RoleWarning | null {
  if (!next || next === member.role) return null

  if (isSelf && member.role === 'owner' && next !== 'owner' && ownerCount <= 1) {
    return {
      level: 'block',
      text: 'Bạn là chủ sở hữu duy nhất của tổ chức. Hạ vai trò của chính mình sẽ để lại một tổ chức không ai quản trị được, và không có cách nào tự lấy lại quyền. Hãy chỉ định một chủ sở hữu khác trước.',
    }
  }

  if (isSelf && grants(member.role, 'member.manage') && !grants(next, 'member.manage')) {
    return {
      level: 'warn',
      text: `Vai trò ${next} không có member.manage. Sau khi đổi, bạn sẽ không mở được màn hình này nữa và sẽ cần một chủ sở hữu khác cấp lại quyền.`,
    }
  }

  if (member.role === 'owner' && next !== 'owner' && ownerCount <= 1) {
    return {
      level: 'block',
      text: 'Đây là chủ sở hữu cuối cùng. Tổ chức phải luôn còn ít nhất một chủ sở hữu — API sẽ từ chối thay đổi này.',
    }
  }

  if (!spansAllProjects(next) && spansAllProjects(member.role)) {
    return {
      level: 'warn',
      text: `Vai trò ${next} không có quyền nào ở cấp tổ chức. Người này sẽ mất truy cập vào mọi dự án cho tới khi được cấp vai trò ở từng dự án cụ thể.`,
    }
  }

  return null
}
