import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  CAPABILITIES,
  MFA_REQUIRED_ROLES,
  NOTABLE_DENIALS,
  ORG_CAPABILITIES,
  ORG_ROLES,
  PROJECT_CAPABILITIES,
  PROJECT_ROLES,
  capabilitiesOf,
  effectiveCapabilities,
  spansAllProjects,
  type Capability,
  type Role,
} from './roles'

/**
 * roles.ts is a copy of a Go file, and a copy left to drift is worse than no copy: it
 * would show an administrator a permission the API does not actually grant. These tests
 * read roles.go and authn.go and compare, so the drift fails the build rather than
 * quietly misinforming somebody handing out access.
 */

const goRoles = fileURLToPath(
  new URL('../../../../../internal/modules/iam/domain/roles.go', import.meta.url),
)
const goAuthn = fileURLToPath(
  new URL('../../../../../internal/platform/authn/authn.go', import.meta.url),
)

/** Extracts a `var <name> = map[string][]string{ ... }` literal. */
function goMapBlock(src: string, name: string): string {
  const start = src.indexOf(`var ${name} = map[string][]string{`)
  if (start < 0) throw new Error(`${name} not found in roles.go`)
  const end = src.indexOf('\n}', start)
  return src.slice(start, end)
}

/** Maps the Go constant identifiers (CapLinkRead) to their string values (link.read). */
function goCapabilityConstants(src: string): Map<string, string> {
  const out = new Map<string, string>()
  for (const m of src.matchAll(/^\s*(Cap\w+)\s*=\s*"([^"]+)"/gm)) {
    if (m[1] && m[2]) out.set(m[1], m[2])
  }
  return out
}

function goRoleCapabilities(block: string, goRoleConst: string, consts: Map<string, string>) {
  const m = new RegExp(`${goRoleConst}:\\s*\\{([\\s\\S]*?)\\}`).exec(block)
  if (!m || m[1] === undefined) throw new Error(`${goRoleConst} not found`)
  return [...m[1].matchAll(/authn\.(Cap\w+)/g)].map(([, id]) => {
    const value = consts.get(id ?? '')
    if (!value) throw new Error(`unknown capability constant ${id}`)
    return value
  })
}

// describe.skip still evaluates its callback, so the reads below have to be
// guarded by an early return rather than by the skip alone -- otherwise a
// missing file throws during collection instead of skipping.
//
// The Docker build copies internal/ into the web stage precisely so this does
// not skip there: a drift guard that quietly does not run in CI is not a guard.
const goSourceAvailable = existsSync(goRoles) && existsSync(goAuthn)
const mirrors = goSourceAvailable ? describe : describe.skip

mirrors('mirrors the Go source of truth', () => {
  if (!goSourceAvailable) return
  const rolesSrc = readFileSync(goRoles, 'utf8')
  const consts = goCapabilityConstants(readFileSync(goAuthn, 'utf8'))

  const goOrgConst: Record<string, string> = {
    owner: 'RoleOwner',
    admin: 'RoleAdmin',
    member: 'RoleMember',
    dpo: 'RoleDPO',
  }
  const goProjectConst: Record<string, string> = {
    manager: 'RoleManager',
    editor: 'RoleEditor',
    analyst: 'RoleAnalyst',
    viewer: 'RoleViewer',
  }

  it('every capability constant in authn.go is described here', () => {
    const described = new Set(CAPABILITIES.map((c) => c.id))
    expect([...consts.values()].filter((v) => !described.has(v as Capability))).toEqual([])
    expect(CAPABILITIES.length).toBe(consts.size)
  })

  it.each(ORG_ROLES)('org role %s grants exactly what roles.go grants', (role) => {
    const block = goMapBlock(rolesSrc, 'orgCapabilities')
    const want = goRoleCapabilities(block, goOrgConst[role] ?? '', consts).sort()
    expect([...ORG_CAPABILITIES[role]].sort()).toEqual(want)
  })

  it.each(PROJECT_ROLES)('project role %s grants exactly what roles.go grants', (role) => {
    const block = goMapBlock(rolesSrc, 'projectCapabilities')
    const want = goRoleCapabilities(block, goProjectConst[role] ?? '', consts).sort()
    expect([...PROJECT_CAPABILITIES[role]].sort()).toEqual(want)
  })

  it('requires MFA for the same roles as MFARequiredRoles', () => {
    const m = /var MFARequiredRoles = \[\]string\{([^}]*)\}/.exec(rolesSrc)
    const want = (m?.[1] ?? '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    const ours = MFA_REQUIRED_ROLES.map((r) => goOrgConst[r])
    expect(ours.sort()).toEqual(want.sort())
  })
})

describe('the two facts the matrix has to make obvious', () => {
  it('member holds nothing at the organisation level', () => {
    expect(ORG_CAPABILITIES.member).toEqual([])
    expect(spansAllProjects('member')).toBe(false)
  })

  it('member only ever gains anything through a project grant', () => {
    expect(effectiveCapabilities('member', [])).toEqual([])
    expect(effectiveCapabilities('member', ['viewer'])).toContain('submission.read')
  })

  it('dpo can neither export nor read sensitive fields', () => {
    expect(ORG_CAPABILITIES.dpo).not.toContain('submission.export')
    expect(ORG_CAPABILITIES.dpo).not.toContain('submission.read_sensitive')
    // ...but does hold the oversight capabilities, or the role would be pointless.
    expect(ORG_CAPABILITIES.dpo).toContain('audit.read')
    expect(ORG_CAPABILITIES.dpo).toContain('submission.read')
  })

  it('analyst exports without seeing sensitive fields', () => {
    expect(PROJECT_CAPABILITIES.analyst).toContain('submission.export')
    expect(PROJECT_CAPABILITIES.analyst).not.toContain('submission.read_sensitive')
  })
})

describe('effectiveCapabilities', () => {
  it('is the union of the org role and every project role', () => {
    const got = effectiveCapabilities('member', ['manager', 'analyst'])
    expect(got).toContain('submission.read_sensitive')
    expect(got).toContain('submission.export')
  })

  it('deduplicates and sorts, so two identical sets compare equal', () => {
    expect(effectiveCapabilities('member', ['viewer', 'viewer'])).toEqual(
      effectiveCapabilities('member', ['viewer']),
    )
    const got = effectiveCapabilities('dpo', ['manager'])
    expect(got).toEqual([...got].sort())
  })

  it('never subtracts: a project grant cannot take away an org capability', () => {
    const org = capabilitiesOf('dpo')
    const combined = effectiveCapabilities('dpo', ['viewer'])
    for (const c of org) expect(combined).toContain(c)
  })
})

describe('table integrity', () => {
  it('lists no capability twice', () => {
    const ids = CAPABILITIES.map((c) => c.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('every capability a role grants has a row in the matrix', () => {
    const described = new Set(CAPABILITIES.map((c) => c.id))
    for (const role of [...ORG_ROLES, ...PROJECT_ROLES]) {
      for (const c of capabilitiesOf(role)) expect(described.has(c)).toBe(true)
    }
  })

  it('never marks a capability as denied that the role actually holds', () => {
    for (const [role, denied] of Object.entries(NOTABLE_DENIALS)) {
      const held = capabilitiesOf(role as Role)
      for (const c of denied ?? []) expect(held).not.toContain(c)
    }
  })
})
