import { describe, expect, it } from 'vitest'
import {
  buildRegistry,
  cellView,
  clipToFrom,
  cursorForTo,
  formatValue,
  registrySpan,
  statusMeta,
  versionSpan,
  type ApiColumn,
  type ApiRow,
  type GridColumn,
} from './columns'

function col(over: Partial<ApiColumn> & { field_id: string }): ApiColumn {
  return {
    label: over.field_id,
    type: 'text',
    sensitive: false,
    in_versions: [1],
    ...over,
  }
}

function row(over: Partial<ApiRow> = {}): ApiRow {
  return {
    id: 'sb_1',
    form_version: 3,
    submitted_at: '2026-08-06T09:41:00Z',
    status: 'active',
    cells: {},
    ...over,
  }
}

function live(fieldId: string, over: Partial<GridColumn> = {}): GridColumn {
  const base = buildRegistry([col({ field_id: fieldId, in_versions: [1, 2, 3] })])[0]!
  return { ...base, ...over }
}

describe('buildRegistry', () => {
  it('keeps first-appearance order for live columns', () => {
    const out = buildRegistry([
      col({ field_id: 'fld_name', in_versions: [1, 2, 3] }),
      col({ field_id: 'fld_phone', in_versions: [1, 2, 3] }),
      col({ field_id: 'fld_health', in_versions: [3], sensitive: true }),
    ])
    expect(out.map((c) => c.key)).toEqual(['fld_name', 'fld_phone', 'fld_health'])
  })

  it('sinks retired columns to the right and hides them by default', () => {
    const out = buildRegistry([
      col({ field_id: 'fld_old', in_versions: [1, 2], retired_after: 2 }),
      col({ field_id: 'fld_name', in_versions: [1, 2, 3] }),
    ])
    expect(out.map((c) => c.fieldId)).toEqual(['fld_name', 'fld_old'])
    expect(out[0]!.hiddenByDefault).toBe(false)
    expect(out[1]!.hiddenByDefault).toBe(true)
    expect(out[1]!.retiredAfter).toBe(2)
  })

  it('gives a field split by a type change two distinct keys', () => {
    // A text field turned into a choice field is two columns, never one coerced
    // column: coercing would rewrite what respondents actually submitted.
    const out = buildRegistry([
      col({ field_id: 'fld_x', type: 'text', type_variant: 'text', in_versions: [1, 2, 3] }),
      col({ field_id: 'fld_x', type: 'choice', type_variant: 'choice', in_versions: [4] }),
    ])
    expect(out.map((c) => c.key)).toEqual(['fld_x@text', 'fld_x@choice'])
    expect(new Set(out.map((c) => c.key)).size).toBe(2)
  })

  it('marks a column sensitive and records which versions asked it', () => {
    const out = buildRegistry([col({ field_id: 'fld_health', sensitive: true, in_versions: [3, 4] })])
    expect(out[0]!.sensitive).toBe(true)
    expect(out[0]!.versions).toBe('v3–v4')
  })

  it('falls back to the field id when a version left the label empty', () => {
    const out = buildRegistry([col({ field_id: 'fld_x', label: '' })])
    expect(out[0]!.label).toBe('fld_x')
  })
})

describe('versionSpan', () => {
  it('collapses a run', () => {
    expect(versionSpan([1, 2, 3])).toBe('v1–v3')
  })
  it('splits at a gap', () => {
    expect(versionSpan([1, 2, 5])).toBe('v1–v2, v5')
  })
  it('handles a single version and duplicates', () => {
    expect(versionSpan([4])).toBe('v4')
    expect(versionSpan([2, 2, 3])).toBe('v2–v3')
  })
  it('says nothing rather than v0 when there are no versions', () => {
    expect(versionSpan([])).toBe('—')
  })
  it('spans the whole registry', () => {
    const out = buildRegistry([
      col({ field_id: 'a', in_versions: [1, 2] }),
      col({ field_id: 'b', in_versions: [3] }),
    ])
    expect(registrySpan(out)).toBe('v1–v3')
  })
})

describe('cellView — the four emptinesses stay apart', () => {
  const nameCol = live('fld_name')
  const healthCol = live('fld_health', { sensitive: true, versions: 'v3' })

  it('distinguishes not asked, hidden and blank', () => {
    const r = row({
      form_version: 2,
      cells: {
        fld_name: { state: 'not_asked' },
      },
    })
    const notAsked = cellView(nameCol, r, { revealSensitive: false })
    const hidden = cellView(nameCol, row({ cells: { fld_name: { state: 'hidden' } } }), {
      revealSensitive: false,
    })
    const blank = cellView(nameCol, row({ cells: { fld_name: { state: 'blank' } } }), {
      revealSensitive: false,
    })

    expect(notAsked.kind).toBe('not_asked')
    expect(notAsked.text).toBe('n/a ở v2')
    expect(hidden.text).toBe('∅')
    expect(blank.text).toBe('—')

    const texts = [notAsked.text, hidden.text, blank.text]
    expect(new Set(texts).size).toBe(3)
    expect(new Set([notAsked.meaning, hidden.meaning, blank.meaning]).size).toBe(3)
  })

  it('masks a sensitive answer until reveal is granted', () => {
    const r = row({ cells: { fld_health: { state: 'answered', value: 'tiểu đường type 2' } } })
    expect(cellView(healthCol, r, { revealSensitive: false }).kind).toBe('masked')
    expect(cellView(healthCol, r, { revealSensitive: false }).text).toBe('••••')
    expect(cellView(healthCol, r, { revealSensitive: true }).text).toBe('tiểu đường type 2')
  })

  it('does not mask a masked cell into looking blank', () => {
    const r = row({ cells: { fld_health: { state: 'answered', value: '••••••' } } })
    const masked = cellView(healthCol, r, { revealSensitive: false })
    const blank = cellView(healthCol, row({ cells: { fld_health: { state: 'blank' } } }), {
      revealSensitive: false,
    })
    expect(masked.text).not.toBe(blank.text)
  })

  it('lets an erased record override every cell state', () => {
    const r = row({ status: 'erased', cells: { fld_name: { state: 'answered', value: 'Nguyễn Văn A' } } })
    const view = cellView(nameCol, r, { revealSensitive: true })
    expect(view.kind).toBe('erased')
    expect(view.text).toBe('đã xoá theo DSR')
    expect(view.text).not.toContain('Nguyễn')
  })

  it('reports crypto-shred for the sensitive columns of an erased record', () => {
    const r = row({ status: 'erased', cells: { fld_health: { state: 'answered', value: 'x' } } })
    expect(cellView(healthCol, r, { revealSensitive: true }).text).toBe('crypto-shred')
  })

  it('flags a missing or unrecognised cell instead of drawing it as empty', () => {
    const missing = cellView(nameCol, row({ cells: {} }), { revealSensitive: false })
    expect(missing.kind).toBe('unknown')
    expect(missing.text).toBe('?')

    const strange = cellView(nameCol, row({ cells: { fld_name: { state: 'quarantined' } } }), {
      revealSensitive: false,
    })
    expect(strange.kind).toBe('unknown')
    expect(strange.meaning).toContain('quarantined')
  })
})

describe('formatValue', () => {
  it('keeps option ids as stored', () => {
    expect(formatValue('opt_yes')).toBe('opt_yes')
    expect(formatValue(['opt_a', 'opt_c'])).toBe('opt_a; opt_c')
  })
  it('renders booleans, numbers and files', () => {
    expect(formatValue(true)).toBe('có')
    expect(formatValue(4)).toBe('4')
    expect(formatValue({ file_id: 'fl_01J' })).toBe('tệp fl_01J')
  })
  it('renders an absent value as nothing rather than "null"', () => {
    expect(formatValue(null)).toBe('')
    expect(formatValue(undefined)).toBe('')
  })
})

describe('statusMeta', () => {
  it('explains each state rather than repeating its label', () => {
    for (const code of ['active', 'restricted', 'erased', 'withdrawn', 'anonymized']) {
      const meta = statusMeta(code)
      expect(meta.meaning.length).toBeGreaterThan(20)
      expect(meta.label).not.toBe(code)
    }
  })

  it('excludes restricted, erased and withdrawn records from exports', () => {
    expect(statusMeta('restricted').excluded).toBe(true)
    expect(statusMeta('erased').excluded).toBe(true)
    expect(statusMeta('withdrawn').excluded).toBe(true)
    expect(statusMeta('active').excluded).toBe(false)
  })

  it('treats an unknown status as not safe to process', () => {
    const meta = statusMeta('quarantined')
    expect(meta.label).toBe('quarantined')
    expect(meta.excluded).toBe(true)
  })
})

describe('date range over the keyset cursor', () => {
  it('turns the upper bound into an end-of-day cursor', () => {
    const cursor = cursorForTo('2026-08-06')
    expect(cursor).not.toBe('')
    expect(new Date(cursor).getTime()).toBeGreaterThan(new Date('2026-08-06T00:00:00').getTime())
    expect(cursorForTo('')).toBe('')
    expect(cursorForTo('not-a-date')).toBe('')
  })

  it('clips the tail of a page and reports that the walk is over', () => {
    const rows = [
      row({ id: 'a', submitted_at: '2026-08-06T09:00:00Z' }),
      row({ id: 'b', submitted_at: '2026-08-05T09:00:00Z' }),
      row({ id: 'c', submitted_at: '2026-08-01T09:00:00Z' }),
    ]
    const out = clipToFrom(rows, '2026-08-05')
    expect(out.rows.map((r) => r.id)).toEqual(['a', 'b'])
    expect(out.reachedStart).toBe(true)
  })

  it('does not claim the walk is over while the whole page is inside the range', () => {
    const rows = [row({ id: 'a', submitted_at: '2026-08-06T09:00:00Z' })]
    expect(clipToFrom(rows, '2026-08-01').reachedStart).toBe(false)
    expect(clipToFrom(rows, '').rows).toHaveLength(1)
  })
})
