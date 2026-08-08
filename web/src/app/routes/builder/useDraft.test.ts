import { describe, expect, it } from 'vitest'
import { evaluate } from '../../../shared/engine'
import {
  addField,
  addRule,
  buildFlowGraph,
  type DraftSchema,
  describeAction,
  describeCondition,
  inertActions,
  moveField,
  normalizeSchema,
  removeField,
  rulesForField,
  ulid,
  updateField,
  updateRule,
} from './useDraft'

function blank(): DraftSchema {
  return normalizeSchema(null)
}

/** A three-page form whose second page branches on a choice. */
function sample(): { schema: DraftSchema; ids: Record<string, string> } {
  let s = blank()
  s = {
    ...s,
    pages: ['Thông tin', 'Nhu cầu', 'Sức khoẻ'].map((title, i) => ({
      id: `pg_${i}`,
      title,
      fields: [],
    })),
  }

  const name = addField(s, 'pg_0', 'text')
  s = name.schema
  const used = addField(s, 'pg_1', 'choice')
  s = used.schema
  const health = addField(s, 'pg_2', 'text')
  s = health.schema

  s = updateField(s, used.fieldId, {
    label: 'Bạn đã dùng sản phẩm chưa?',
    options: [
      { id: 'opt_yes', label: 'Rồi' },
      { id: 'opt_no', label: 'Chưa' },
    ],
  })
  s = updateField(s, health.fieldId, { label: 'Tình trạng sức khoẻ', sensitive: true })

  return { schema: s, ids: { name: name.fieldId, used: used.fieldId, health: health.fieldId } }
}

describe('ulid', () => {
  it('mints 26 sortable characters', () => {
    const id = ulid()
    expect(id).toHaveLength(26)
    expect(id).toMatch(/^[0-9A-HJKMNP-TV-Z]{26}$/)
  })

  it('stays unique and ordered inside one millisecond', () => {
    const fixed = 1_760_000_000_000
    const a = ulid(fixed)
    const b = ulid(fixed)
    const c = ulid(fixed)
    expect(new Set([a, b, c]).size).toBe(3)
    expect([a, b, c]).toEqual([a, b, c].slice().sort())
  })
})

describe('normalizeSchema', () => {
  it('survives the nulls Go emits for empty slices and maps', () => {
    const s = normalizeSchema({ v: 2, pages: null, fields: null, rules: null } as unknown as DraftSchema)
    expect(s.pages).toEqual([])
    expect(s.fields).toEqual({})
    expect(s.rules).toEqual([])
    expect(s.v).toBe(2)
  })

  it('keeps keys this client does not model, so a save cannot drop them', () => {
    const raw = { v: 1, pages: [], fields: {}, limits: { max_fields: 50 } } as unknown as DraftSchema
    expect(normalizeSchema(raw)).toMatchObject({ limits: { max_fields: 50 } })
  })
})

describe('field operations', () => {
  it('never reuses an id after a delete and re-add', () => {
    const { schema, ids } = sample()
    const after = removeField(schema, ids.used ?? '')
    const recreated = addField(after, 'pg_1', 'choice')
    expect(recreated.fieldId).not.toBe(ids.used)
    expect(after.fields[ids.used ?? '']).toBeUndefined()
  })

  it('keeps the id when the label changes', () => {
    const { schema, ids } = sample()
    const renamed = updateField(schema, ids.used ?? '', { label: 'Đã dùng chưa?' })
    expect(Object.keys(renamed.fields).sort()).toEqual(Object.keys(schema.fields).sort())
    expect(renamed.fields[ids.used ?? '']?.label).toBe('Đã dùng chưa?')
  })

  it('drops the rules that read a deleted field and the actions aimed at it', () => {
    const { schema, ids } = sample()
    const withRule = addRule(schema, ids.used ?? '')
    let s = updateRule(withRule.schema, withRule.ruleId, {
      when: { op: 'eq', field: ids.used ?? '', value: 'opt_no' },
      then: [{ action: 'show', target: ids.health ?? '' }],
    })
    expect(rulesForField(s, ids.health ?? '')).toHaveLength(1)

    s = removeField(s, ids.health ?? '')
    expect(s.rules?.[0]?.then).toEqual([])

    s = removeField(s, ids.used ?? '')
    expect(s.rules).toEqual([])
  })

  it('moves a question to another page without touching navigation', () => {
    const { schema, ids } = sample()
    const moved = moveField(schema, ids.health ?? '', 'pg_0', 0)
    expect(moved.pages[0]?.fields).toEqual([ids.health, ids.name])
    expect(moved.pages[2]?.fields).toEqual([])
    expect(moved.pages.map((p) => p.id)).toEqual(schema.pages.map((p) => p.id))
  })
})

describe('describing rules in the reader’s language', () => {
  it('resolves option ids to the wording the respondent sees', () => {
    const { schema, ids } = sample()
    expect(describeCondition(schema, { op: 'eq', field: ids.used ?? '', value: 'opt_yes' })).toBe(
      'NẾU = “Rồi”',
    )
    expect(describeCondition(schema, { op: 'is_empty', field: ids.used ?? '' })).toBe('NẾU bỏ trống')
  })

  it('names the page a jump lands on', () => {
    const { schema } = sample()
    expect(describeAction(schema, { action: 'goto', target: 'pg_2' })).toBe(
      '→ nhảy tới trang 3 · Sức khoẻ',
    )
  })
})

describe('buildFlowGraph', () => {
  it('reports a clean linear form as clean', () => {
    const { schema } = sample()
    const g = buildFlowGraph(schema)
    expect(g.cycleAt).toBeNull()
    expect(g.unreachable).toEqual([])
    expect(g.edges.filter((e) => e.kind === 'default')).toHaveLength(3)
    expect(g.rank['pg_2']).toBe(2)
  })

  it('finds a loop created by a goto pointing backwards', () => {
    const { schema, ids } = sample()
    const withRule = addRule(schema, ids.used ?? '')
    const s = updateRule(withRule.schema, withRule.ruleId, {
      when: { op: 'eq', field: ids.used ?? '', value: 'opt_yes' },
      then: [{ action: 'goto', target: 'pg_0' }],
    })
    const g = buildFlowGraph(s)
    expect(g.cycleAt).toBe('pg_0')
    expect(g.checks.find((c) => c.code === 'cycle')?.ok).toBe(false)
    expect(g.edges.some((e) => e.back)).toBe(true)
  })

  it('finds a page nothing routes to', () => {
    const { schema } = sample()
    // Page 1 jumps straight past page 2, and page 2 is not the fallthrough of
    // anything else, so nobody can ever land on it.
    const s: DraftSchema = {
      ...schema,
      pages: schema.pages.map((p) => (p.id === 'pg_0' ? { ...p, next: 'pg_2' } : p)),
    }
    const g = buildFlowGraph(s)
    expect(g.unreachable).toEqual(['pg_1'])
    expect(g.checks.find((c) => c.code === 'unreachable')?.ok).toBe(false)
  })

  it('flags a form that collects data without declaring a purpose', () => {
    const { schema } = sample()
    expect(buildFlowGraph(schema).checks.find((c) => c.code === 'consent_last')?.ok).toBe(false)
    const withPurpose: DraftSchema = {
      ...schema,
      consent: { purposes: [{ code: 'service', required: true }], sensitive_notice_required: true },
    }
    expect(buildFlowGraph(withPurpose).checks.find((c) => c.code === 'consent_last')?.ok).toBe(true)
  })

  it('asks for a sensitive-data notice only when a field is sensitive', () => {
    const { schema, ids } = sample()
    expect(buildFlowGraph(schema).checks.some((c) => c.code === 'sensitive_notice')).toBe(true)
    const plain = updateField(schema, ids.health ?? '', { sensitive: false })
    expect(buildFlowGraph(plain).checks.some((c) => c.code === 'sensitive_notice')).toBe(false)
  })
})

describe('what the builder produces runs through the shared engine', () => {
  it('takes the branch a goto rule describes, and the fallthrough otherwise', () => {
    const { schema, ids } = sample()
    const withRule = addRule(schema, ids.used ?? '')
    const s = updateRule(withRule.schema, withRule.ruleId, {
      when: { op: 'eq', field: ids.used ?? '', value: 'opt_yes' },
      then: [{ action: 'goto', target: 'pg_2' }],
      else: [{ action: 'end', target: '' }],
    })

    expect(evaluate(s, { [ids.used ?? '']: 'opt_yes' }).path).toEqual(['pg_0', 'pg_1', 'pg_2'])
    expect(evaluate(s, { [ids.used ?? '']: 'opt_no' }).path).toEqual(['pg_0', 'pg_1'])
  })

  it('reports a required question the branch hid as not required on that path', () => {
    const { schema, ids } = sample()
    // The revealed question has to live on the same page as the rule -- see the
    // inertActions test below for why.
    const reason = addField(schema, 'pg_1', 'text')
    let s = updateField(reason.schema, reason.fieldId, {
      label: 'Vì sao chưa dùng?',
      required: true,
      hidden: true,
    })
    const withRule = addRule(s, ids.used ?? '')
    s = updateRule(withRule.schema, withRule.ruleId, {
      when: { op: 'eq', field: ids.used ?? '', value: 'opt_no' },
      then: [{ action: 'show', target: reason.fieldId }],
    })

    expect(evaluate(s, { [ids.used ?? '']: 'opt_no' }).required).toEqual([reason.fieldId])
    expect(evaluate(s, { [ids.used ?? '']: 'opt_yes' }).required).toEqual([])
  })
})

describe('inertActions', () => {
  /**
   * The check exists because the engine drops these silently and the server's
   * Validate does not object: a `show` aimed across a page boundary is a branch
   * that looks wired up in the builder and reveals nothing to a respondent.
   */
  it('flags a show aimed at a field on another page, and proves the engine ignores it', () => {
    const { schema, ids } = sample()
    let s = updateField(schema, ids.health ?? '', { required: true, hidden: true })
    const withRule = addRule(s, ids.used ?? '')
    s = updateRule(withRule.schema, withRule.ruleId, {
      when: { op: 'eq', field: ids.used ?? '', value: 'opt_yes' },
      then: [{ action: 'show', target: ids.health ?? '' }],
    })

    const rule = s.rules?.[0]
    expect(rule && inertActions(s, rule)).toHaveLength(1)
    expect(evaluate(s, { [ids.used ?? '']: 'opt_yes' }).visible).not.toContain(ids.health)
  })

  it('says nothing about a goto, which is page-scoped by design', () => {
    const { schema, ids } = sample()
    const withRule = addRule(schema, ids.used ?? '')
    const s = updateRule(withRule.schema, withRule.ruleId, {
      when: { op: 'eq', field: ids.used ?? '', value: 'opt_yes' },
      then: [{ action: 'goto', target: 'pg_2' }],
    })
    const rule = s.rules?.[0]
    expect(rule && inertActions(s, rule)).toEqual([])
  })
})
