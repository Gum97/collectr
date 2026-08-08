import { NavLink, Outlet, useParams } from 'react-router'
import { mfaGraceHoursLeft, useMe } from '../lib/session'
import { retentionLabel, useProjects, type Project } from '../lib/projects'

/**
 * Shell B from the wireframes: the project tree is the spine, modules hang off it.
 *
 * Chosen over a flat module nav because every permission in this system is
 * scoped to a project -- the API checks actor.InProject on almost every write.
 * A nav that puts modules first has to answer "in which project?" somewhere off
 * to the side, and the answer is then easy to lose track of while acting on
 * somebody's personal data.
 */
export function Shell() {
  const me = useMe()
  const projects = useProjects()

  return (
    <div className="flex min-h-screen">
      <nav className="flex w-[248px] shrink-0 flex-col gap-1 border-r border-line bg-panel px-3 py-4">
        <div className="flex items-baseline justify-between px-1 pb-2">
          <span className="font-display text-[19px] font-semibold tracking-[-.01em]">Collectr</span>
          <span className="id-chip">{me.data?.org_name ?? ''}</span>
        </div>

        <section aria-labelledby="nav-projects" className="mt-1">
          <h2 id="nav-projects" className="cap px-2 pb-1.5">
            DỰ ÁN ({projects.data?.length ?? 0})
          </h2>
          {projects.isPending && <p className="px-2 py-2 text-meta text-muted">Đang tải…</p>}
          {projects.data?.map((p) => <ProjectBranch key={p.id} project={p} />)}
        </section>

        <section aria-labelledby="nav-org" className="mt-5 border-t border-line pt-4">
          <h2 id="nav-org" className="cap px-2 pb-1.5">
            TOÀN TỔ CHỨC
          </h2>
          <OrgLink to="/compliance">Tuân thủ &amp; DSR</OrgLink>
          <OrgLink to="/audit">Nhật ký audit</OrgLink>
          <OrgLink to="/members">Thành viên &amp; quyền</OrgLink>
          <OrgLink to="/settings">Cài đặt tổ chức</OrgLink>
        </section>

        <div className="mt-auto flex items-center gap-2.5 border-t border-line pt-4">
          <div className="size-8 shrink-0 rounded-full bg-accent-wash ring-1 ring-accent-line" aria-hidden />
          <div className="min-w-0">
            <div className="truncate text-chip font-medium">{me.data?.name || me.data?.email}</div>
            <div className="id-chip">
              {me.data?.org_role}
              {/* Stated in the chrome, not buried in settings: an admin account
                  without a second factor is the single most consequential
                  misconfiguration available here. */}
              {me.data?.mfa_enabled ? ' · MFA on' : ' · chưa bật MFA'}
            </div>
          </div>
        </div>
      </nav>

      <main className="min-w-0 flex-1">
        <MFAReminder hoursLeft={mfaGraceHoursLeft(me.data)} />
        <Outlet />
      </main>
    </div>
  )
}

function ProjectBranch({ project }: { project: Project }) {
  const { projectId } = useParams()
  const open = projectId === project.id
  const denied = project.access === 'none'

  if (denied) {
    // Named but not linked. Hiding it entirely would leave people unable to ask
    // to be added to something they cannot see exists, which is how shadow
    // spreadsheets start.
    return (
      <div className="rounded px-2 py-2">
        <div className="text-body text-ghost">{project.name}</div>
        <div className="id-chip mt-0.5 flex items-center gap-1 text-duesoon">
          <span aria-hidden>⚠</span> không có quyền truy cập
        </div>
      </div>
    )
  }

  return (
    <div className="pb-1">
      <NavLink
        to={`/p/${project.id}`}
        className={({ isActive }) =>
          `block rounded px-2 py-2 text-body font-medium ${
            isActive || open ? 'bg-accent text-white' : 'hover:bg-chrome'
          }`
        }
      >
        {project.name}
      </NavLink>
      <div className="id-chip px-2 pt-1">
        {project.my_role || 'qua vai trò tổ chức'} ·{' '}
        {retentionLabel(project.default_retention_days)}
      </div>
      {open && (
        <div className="mt-1 flex flex-col">
          <ModuleLink to={`/p/${project.id}/forms`}>Biểu mẫu</ModuleLink>
          <ModuleLink to={`/p/${project.id}/links`}>Link &amp; QR</ModuleLink>
          <ModuleLink to={`/p/${project.id}/submissions`}>Dữ liệu</ModuleLink>
          <ModuleLink to={`/p/${project.id}/analytics`}>Phân tích</ModuleLink>
          <ModuleLink to={`/p/${project.id}/settings`}>Cài đặt dự án</ModuleLink>
        </div>
      )}
    </div>
  )
}

function ModuleLink({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        `rounded py-1.5 pl-5 pr-2 text-body ${isActive ? 'font-medium text-accent' : 'text-muted hover:bg-chrome hover:text-ink'}`
      }
    >
      {children}
    </NavLink>
  )
}

function OrgLink({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        `block rounded px-2 py-2 text-body ${isActive ? 'bg-accent font-medium text-white' : 'text-muted hover:bg-chrome hover:text-ink'}`
      }
    >
      {children}
    </NavLink>
  )
}

/**
 * Shown while a privileged account still works without a second factor.
 *
 * A reminder, not a wall: on a fresh install the person is looking around, and
 * refusing every screen before showing anything is how a product gets
 * uninstalled rather than secured. When the window closes the server stops
 * granting capabilities and this becomes a gate instead.
 */
function MFAReminder({ hoursLeft }: { hoursLeft: number | null }) {
  if (hoursLeft === null) return null
  const urgent = hoursLeft <= 24
  return (
    <div
      className={`flex flex-wrap items-center justify-between gap-3 border-b px-6 py-2.5 text-body ${
        urgent ? 'border-legal-line bg-legal-wash text-legal' : 'border-line bg-chrome text-muted'
      }`}
    >
      <span>
        Vai trò của bạn bắt buộc xác thực hai lớp.{' '}
        <strong className="font-medium">
          {hoursLeft > 0 ? `Còn ${hoursLeft} giờ` : 'Đã hết hạn'}
        </strong>{' '}
        trước khi tài khoản mất quyền truy cập.
      </span>
      <NavLink to="/account/mfa" className="btn-primary shrink-0">
        Bật ngay
      </NavLink>
    </div>
  )
}
