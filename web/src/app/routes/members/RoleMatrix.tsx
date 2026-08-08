import { Link } from 'react-router'
import { Card, PageHeader, Table, Td, Th, Tr } from '../../components/ui'
import {
  CAPABILITIES,
  NOTABLE_DENIALS,
  ORG_ROLES,
  PROJECT_ROLES,
  ROLE_LABELS,
  ROLE_SUMMARY,
  capabilitiesOf,
  effectiveCapabilities,
  grants,
  requiresMFA,
  type Capability,
  type CapabilityMeta,
  type Role,
} from './roles'

/**
 * The eight roles, spelled out.
 *
 * A permission model nobody can read is a permission model nobody follows: the
 * administrator hands out `admin` because it is the one they understand, and the
 * separation between reading a record and reading the sensitive fields inside it stops
 * meaning anything. So the whole table is on one screen, and the two facts most often
 * got wrong -- that `member` grants nothing, and that `dpo` watches without extracting
 * -- are stated in words above it rather than left to be inferred from empty cells.
 */
export function RoleMatrix() {
  const groups = [...new Set(CAPABILITIES.map((c) => c.group))]

  return (
    <div className="p-6">
      <PageHeader
        title="Quyền của 8 vai trò"
        meta="4 vai trò tổ chức · 4 vai trò dự án · quyền hiệu lực = hợp của hai bên"
        actions={
          <Link to="/members" className="btn">
            ← Thành viên
          </Link>
        }
      />

      <div className="mb-4 grid gap-3 md:grid-cols-2">
        <Callout title="member không có quyền nào ở cấp tổ chức">
          Cột <code className="font-mono">member</code> trống hoàn toàn — đó là chủ đích, không
          phải thiếu sót. Người mang vai trò này chỉ thấy được thứ gì đó sau khi được cấp vai trò
          ở một dự án cụ thể. Mặc định là <strong>không thấy gì</strong>, chứ không phải thấy tất
          cả rồi trừ dần.
        </Callout>
        <Callout title="dpo giám sát, không vận hành">
          Đọc được xuyên mọi dự án, nhưng <strong>không xuất được dữ liệu</strong> và{' '}
          <strong>không đọc được field nhạy cảm</strong>. Hai ô đó bị bỏ trống có chủ đích: người
          chịu trách nhiệm giám sát việc xử lý dữ liệu không nên đồng thời mang được dữ liệu đó ra
          ngoài.
        </Callout>
      </div>

      <Table
        head={
          <>
            <Th className="min-w-56">Capability</Th>
            {ORG_ROLES.map((r) => (
              <RoleHeader key={r} role={r} scope="tổ chức" />
            ))}
            {PROJECT_ROLES.map((r, i) => (
              <RoleHeader key={r} role={r} scope="dự án" first={i === 0} />
            ))}
          </>
        }
      >
        {groups.map((group) => (
          <GroupRows key={group} group={group} />
        ))}
      </Table>

      <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-meta text-muted">
        <span>
          <Glyph kind="granted" /> có quyền
        </span>
        <span>
          <Glyph kind="denied" /> cố tình không cấp
        </span>
        <span>
          <Glyph kind="absent" /> không thuộc vai trò này
        </span>
        <span className="text-faint">
          Không có custom role. Đổi vai trò được ghi vào nhật ký audit và thu hồi phiên trong 60
          giây.
        </span>
      </div>

      <div className="mt-4 grid gap-3 md:grid-cols-2">
        {[...ORG_ROLES, ...PROJECT_ROLES].map((role) => (
          <Card
            key={role}
            title={ROLE_LABELS[role]}
            aside={`${role} · ${capabilitiesOf(role).length} capability`}
          >
            <p className="text-body text-muted">{ROLE_SUMMARY[role]}</p>
            {requiresMFA(role) && (
              <p className="mt-1.5 text-meta font-semibold text-duesoon">
                ⚠ Bắt buộc bật MFA — vai trò này chạm được dữ liệu cá nhân của mọi dự án.
              </p>
            )}
          </Card>
        ))}
      </div>
    </div>
  )
}

function GroupRows({ group }: { group: string }) {
  const rows = CAPABILITIES.filter((c) => c.group === group)
  return (
    <>
      <tr className="border-b border-line bg-chrome/60">
        <td colSpan={1 + ORG_ROLES.length + PROJECT_ROLES.length} className="px-3 py-1">
          <span className="font-mono text-meta tracking-caps text-faint">
            {group.toUpperCase()}
          </span>
        </td>
      </tr>
      {rows.map((cap) => (
        <CapabilityRow key={cap.id} cap={cap} />
      ))}
    </>
  )
}

function CapabilityRow({ cap }: { cap: CapabilityMeta }) {
  return (
    <Tr>
      <Td>
        <div className="flex items-baseline gap-2">
          <span className="text-body">{cap.label}</span>
          {cap.consequential && (
            <span
              className="rounded border border-legal-line bg-legal-wash px-1.5 text-meta font-medium text-legal"
              title="Tách riêng có chủ đích: hệ quả khác hẳn các quyền lân cận."
            >
              tách riêng
            </span>
          )}
        </div>
        <div className="id-chip">{cap.id}</div>
      </Td>
      {ORG_ROLES.map((r) => (
        <Cell key={r} role={r} cap={cap.id} />
      ))}
      {PROJECT_ROLES.map((r, i) => (
        <Cell key={r} role={r} cap={cap.id} first={i === 0} />
      ))}
    </Tr>
  )
}

function Cell({ role, cap, first }: { role: Role; cap: Capability; first?: boolean }) {
  const held = grants(role, cap)
  const denied = NOTABLE_DENIALS[role]?.includes(cap) ?? false
  const kind = held ? 'granted' : denied ? 'denied' : 'absent'
  const empty = capabilitiesOf(role).length === 0

  return (
    <Td
      className={`text-center ${first ? 'border-l border-line' : ''} ${
        empty ? 'bg-canvas/60' : ''
      }`}
    >
      <Glyph kind={kind} />
      <span className="sr-only">
        {ROLE_LABELS[role]}:{' '}
        {kind === 'granted' ? 'có' : kind === 'denied' ? 'cố tình không cấp' : 'không có'}
      </span>
    </Td>
  )
}

/** Never colour alone: each state carries its own glyph, because two of these three are
 *  reds and the reader who cannot tell them apart still has to get this right. */
function Glyph({ kind }: { kind: 'granted' | 'denied' | 'absent' }) {
  if (kind === 'granted') return <span className="font-semibold text-ink">✓</span>
  // Earth red, not green.
  //
  // A denial here is not an absence -- it is a boundary somebody drew on
  // purpose, and the reason is legal: a DPO who could export data would be
  // supervising work they also perform. Green is this product's colour for
  // ordinary actions, so rendering a deliberate refusal in it says the opposite
  // of what the cell means.
  if (kind === 'denied') return <span className="font-semibold text-legal">✕</span>
  return <span className="text-faint">·</span>
}

function RoleHeader({ role, scope, first }: { role: Role; scope: string; first?: boolean }) {
  return (
    <Th className={`text-center ${first ? 'border-l border-line' : ''}`}>
      <div className="text-ink">{role}</div>
      <div className="font-normal normal-case tracking-normal">{scope}</div>
    </Th>
  )
}

function Callout({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded border border-dashed border-legal-line bg-legal-wash px-3 py-2.5">
      <p className="text-body font-semibold text-legal">{title}</p>
      <p className="mt-1 text-meta leading-relaxed text-ink">{children}</p>
    </div>
  )
}

/**
 * What one person can actually do in one project — the wireframe's "Trần Hoà thấy được
 * gì ở dự án CSKH".
 *
 * Shows the withheld capabilities alongside the granted ones. A list of only what
 * somebody *can* do reads as complete, and the reader then assumes the absent ones were
 * never on the table; naming the two or three that were deliberately withheld is what
 * turns this from a summary into an answer.
 */
export function CapabilityChips({
  orgRole,
  projectRole,
}: {
  orgRole: string
  projectRole?: string
}) {
  const held = effectiveCapabilities(orgRole, projectRole ? [projectRole] : [])
  const denials = [
    ...new Set([...(NOTABLE_DENIALS[orgRole as Role] ?? []), ...(projectRole ? (NOTABLE_DENIALS[projectRole as Role] ?? []) : [])]),
  ].filter((c) => !held.includes(c))

  if (held.length === 0 && denials.length === 0) {
    return (
      <p className="text-meta text-muted">
        Không có quyền nào. Vai trò <code className="font-mono">member</code> chỉ nhận quyền qua
        việc được cấp vai trò ở từng dự án.
      </p>
    )
  }

  return (
    <div className="flex flex-wrap gap-1.5">
      {held.map((c) => (
        <span
          key={c}
          className="rounded border border-line px-1.5 py-0.5 font-mono text-meta"
        >
          {c}
        </span>
      ))}
      {denials.map((c) => (
        <span
          key={c}
          className="rounded border border-dashed border-legal-line px-1.5 py-0.5 font-mono text-meta text-legal"
        >
          ✕ {c}
        </span>
      ))}
    </div>
  )
}

/** The role as a chip. Dashed for `member`, because a role that grants nothing should
 *  not look like the ones that do. */
export function RoleBadge({ role }: { role: string }) {
  const empty = capabilitiesOf(role).length === 0
  const oversight = role === 'dpo'
  return (
    <span
      title={ROLE_SUMMARY[role as Role] ?? role}
      className={`inline-block whitespace-nowrap rounded-full px-2 py-0.5 text-meta font-semibold ${
        empty
          ? 'border border-dashed border-line text-muted'
          : oversight
            ? 'border border-accent text-accent'
            : 'border border-line text-ink'
      }`}
    >
      {role}
    </span>
  )
}
