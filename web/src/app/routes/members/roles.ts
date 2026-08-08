/**
 * The role and capability model, mirrored for display.
 *
 * ⚠ THIS FILE MUST MATCH THE API'S SOURCE OF TRUTH:
 *   - internal/modules/iam/domain/roles.go   (orgCapabilities, projectCapabilities,
 *                                             MFARequiredRoles)
 *   - internal/platform/authn/authn.go       (the capability constants)
 *
 * Nothing here decides anything. Access decisions come from the API -- `me.capabilities`,
 * `project.access`, `project.my_role`. This table exists only so an administrator can
 * read what a role means *before* handing it to somebody, which is the one question the
 * API cannot answer for a person who does not hold the role.
 *
 * Note on scope: docs/11-rbac.md names a wider capability vocabulary
 * (project.read, form.delete, submission.edit, submission.delete, org.settings,
 * org.billing). Those are not defined in authn.go and no handler checks them, so they
 * are deliberately absent here -- showing an administrator a permission the system does
 * not enforce would be worse than showing none.
 */

export type OrgRole = 'owner' | 'admin' | 'member' | 'dpo'
export type ProjectRole = 'manager' | 'editor' | 'analyst' | 'viewer'
export type Role = OrgRole | ProjectRole

export type Capability =
  | 'link.read'
  | 'link.write'
  | 'link.delete'
  | 'form.read'
  | 'form.write'
  | 'form.publish'
  | 'submission.read'
  | 'submission.read_sensitive'
  | 'submission.export'
  | 'analytics.read'
  | 'consent.manage'
  | 'dsr.handle'
  | 'audit.read'
  | 'apikey.manage'
  | 'webhook.manage'
  | 'member.manage'

export interface CapabilityMeta {
  readonly id: Capability
  readonly label: string
  readonly group: string
  /** Marks the three capabilities the model splits out on purpose. Reading a record,
   *  reading the sensitive fields inside it, and taking the whole lot away are three
   *  different acts with three different consequences. */
  readonly consequential?: boolean
}

/** Order follows the constant block in authn.go so a diff between the two is readable. */
export const CAPABILITIES: readonly CapabilityMeta[] = [
  { id: 'link.read', label: 'Xem link & QR', group: 'Link' },
  { id: 'link.write', label: 'Tạo & sửa link', group: 'Link' },
  { id: 'link.delete', label: 'Xoá link', group: 'Link' },

  { id: 'form.read', label: 'Xem biểu mẫu', group: 'Biểu mẫu' },
  { id: 'form.write', label: 'Sửa biểu mẫu', group: 'Biểu mẫu' },
  { id: 'form.publish', label: 'Publish biểu mẫu', group: 'Biểu mẫu' },

  { id: 'submission.read', label: 'Đọc dữ liệu gửi về', group: 'Dữ liệu' },
  {
    id: 'submission.read_sensitive',
    label: 'Đọc field nhạy cảm',
    group: 'Dữ liệu',
    consequential: true,
  },
  { id: 'submission.export', label: 'Xuất dữ liệu hàng loạt', group: 'Dữ liệu', consequential: true },

  { id: 'analytics.read', label: 'Xem phân tích', group: 'Dữ liệu' },

  { id: 'consent.manage', label: 'Quản lý văn bản đồng ý', group: 'Tuân thủ' },
  { id: 'dsr.handle', label: 'Xử lý yêu cầu chủ thể (DSR)', group: 'Tuân thủ' },
  { id: 'audit.read', label: 'Đọc nhật ký audit', group: 'Tuân thủ', consequential: true },

  { id: 'apikey.manage', label: 'Quản lý API key', group: 'Quản trị' },
  { id: 'webhook.manage', label: 'Quản lý webhook', group: 'Quản trị' },
  { id: 'member.manage', label: 'Quản lý thành viên & quyền', group: 'Quản trị' },
]

export const ORG_ROLES: readonly OrgRole[] = ['owner', 'admin', 'member', 'dpo']
export const PROJECT_ROLES: readonly ProjectRole[] = ['manager', 'editor', 'analyst', 'viewer']

/** Mirrors domain.orgCapabilities. */
export const ORG_CAPABILITIES: Record<OrgRole, readonly Capability[]> = {
  owner: [
    'link.read',
    'link.write',
    'link.delete',
    'form.read',
    'form.write',
    'form.publish',
    'submission.read',
    'submission.read_sensitive',
    'submission.export',
    'analytics.read',
    'consent.manage',
    'dsr.handle',
    'audit.read',
    'apikey.manage',
    'webhook.manage',
    'member.manage',
  ],
  admin: [
    'link.read',
    'link.write',
    'link.delete',
    'form.read',
    'form.write',
    'form.publish',
    'submission.read',
    'submission.read_sensitive',
    'submission.export',
    'analytics.read',
    'consent.manage',
    'dsr.handle',
    'audit.read',
    'apikey.manage',
    'webhook.manage',
    'member.manage',
  ],
  // Empty on purpose, and the emptiness is the point: a plain member sees nothing at
  // the organisation level. Everything they can reach arrives through a grant on one
  // project. The default is "sees nothing", not "sees everything minus exceptions".
  member: [],
  dpo: [
    'audit.read',
    'dsr.handle',
    'consent.manage',
    'submission.read',
    'form.read',
    'analytics.read',
  ],
}

/** Mirrors domain.projectCapabilities. */
export const PROJECT_CAPABILITIES: Record<ProjectRole, readonly Capability[]> = {
  manager: [
    'link.read',
    'link.write',
    'link.delete',
    'form.read',
    'form.write',
    'form.publish',
    'submission.read',
    'submission.read_sensitive',
    'submission.export',
    'analytics.read',
    'apikey.manage',
    'webhook.manage',
  ],
  editor: [
    'link.read',
    'link.write',
    'form.read',
    'form.write',
    'form.publish',
    'submission.read',
    'analytics.read',
  ],
  analyst: [
    'link.read',
    'form.read',
    'submission.read',
    'submission.export',
    'analytics.read',
  ],
  viewer: ['link.read', 'form.read', 'submission.read', 'analytics.read'],
}

/**
 * Absences worth stating out loud, per role.
 *
 * An unticked cell in a matrix is easy to read as "not applicable". These are the ones
 * where the gap is the design: they get an explicit ✕ so nobody hands out a role
 * expecting a permission that was withheld on purpose.
 *
 * Sourced from the comments in roles.go and docs/11-rbac.md §11.3–11.4.
 */
export const NOTABLE_DENIALS: Partial<Record<Role, readonly Capability[]>> = {
  dpo: ['submission.read_sensitive', 'submission.export', 'form.write'],
  analyst: ['submission.read_sensitive', 'form.write'],
  editor: ['submission.read_sensitive', 'submission.export'],
  viewer: ['submission.read_sensitive', 'submission.export', 'form.write'],
}

/** Mirrors domain.MFARequiredRoles: the roles that reach personal data across every
 *  project, and therefore may not rely on a password alone. */
export const MFA_REQUIRED_ROLES: readonly OrgRole[] = ['owner', 'admin', 'dpo']

export function requiresMFA(orgRole: string): boolean {
  return (MFA_REQUIRED_ROLES as readonly string[]).includes(orgRole)
}

export const ROLE_LABELS: Record<Role, string> = {
  owner: 'Chủ sở hữu',
  admin: 'Quản trị viên',
  member: 'Thành viên',
  dpo: 'Phụ trách bảo vệ dữ liệu (DPO)',
  manager: 'Quản lý dự án',
  editor: 'Biên tập biểu mẫu',
  analyst: 'Phân tích',
  viewer: 'Chỉ xem',
}

/** One line an administrator can act on, not a restatement of the capability list. */
export const ROLE_SUMMARY: Record<Role, string> = {
  owner: 'Toàn quyền trên mọi dự án, kể cả cài đặt tổ chức. Tổ chức phải luôn còn ít nhất một người.',
  admin: 'Như chủ sở hữu trên dữ liệu và thành viên, trừ việc xoá tổ chức và chuyển quyền sở hữu.',
  member: 'Không có quyền nào ở cấp tổ chức. Chỉ thấy được những gì được cấp riêng ở từng dự án.',
  dpo: 'Đọc xuyên mọi dự án để giám sát tuân thủ. Không xuất được dữ liệu và không đọc được field nhạy cảm.',
  manager: 'Toàn quyền trong một dự án, kể cả field nhạy cảm, xuất dữ liệu, API key và webhook.',
  editor: 'Dựng và publish biểu mẫu, đọc dữ liệu gửi về. Không xuất được, không thấy field nhạy cảm.',
  analyst: 'Xuất được dữ liệu để làm báo cáo, nhưng field nhạy cảm trong file xuất ra bị che ••••.',
  viewer: 'Chỉ đọc trong phạm vi một dự án. Không sửa, không xuất, không thấy field nhạy cảm.',
}

export function isOrgRole(r: string): r is OrgRole {
  return (ORG_ROLES as readonly string[]).includes(r)
}

export function isProjectRole(r: string): r is ProjectRole {
  return (PROJECT_ROLES as readonly string[]).includes(r)
}

export function roleLabel(r: string): string {
  return isOrgRole(r) || isProjectRole(r) ? ROLE_LABELS[r] : r
}

export function capabilitiesOf(role: string): readonly Capability[] {
  if (isOrgRole(role)) return ORG_CAPABILITIES[role]
  if (isProjectRole(role)) return PROJECT_CAPABILITIES[role]
  return []
}

export function grants(role: string, capability: Capability): boolean {
  return capabilitiesOf(role).includes(capability)
}

/**
 * The effective set for one membership: the union of the organisation role and every
 * project role held. Mirrors domain.Capabilities.
 *
 * There are no deny rules on either side. Mixing allow and deny creates precedence
 * puzzles that nobody can resolve at the moment they matter.
 */
export function effectiveCapabilities(
  orgRole: string,
  projectRoles: readonly string[] = [],
): Capability[] {
  const seen = new Set<Capability>(capabilitiesOf(orgRole))
  for (const pr of projectRoles) for (const c of capabilitiesOf(pr)) seen.add(c)
  return [...seen].sort()
}

/**
 * Whether an organisation role reaches every project on its own.
 *
 * Tested as "grants anything at all" rather than by listing role names, the same way
 * the API's accessLevel does it -- a role added later is then classified correctly
 * without anyone remembering to edit this line.
 */
export function spansAllProjects(orgRole: string): boolean {
  return capabilitiesOf(orgRole).length > 0
}

/** How to describe someone's reach across projects in one table cell. */
export function projectReachLabel(orgRole: string): string | null {
  if (!spansAllProjects(orgRole)) return null
  if (orgRole === 'dpo') return 'đọc xuyên mọi dự án · không xuất được'
  return 'mọi dự án (kế thừa)'
}
