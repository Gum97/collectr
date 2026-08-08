/**
 * The client format checks, graded by the Go module's own fixture.
 *
 * The path reaches into internal/ on purpose. Copying the file here would make
 * the two suites agree right up until somebody edits one of them, and the
 * disagreement would surface as a respondent whose answer the page accepted and
 * the server refused.
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { FORMATS, checkFormat, type FormattedField } from './format'

const fixture = JSON.parse(
  readFileSync(
    new URL('../../../internal/modules/forms/domain/testdata/formats.json', import.meta.url),
    'utf8',
  ),
) as { cases: Array<{ name: string; field: FormattedField; value: string; ok: boolean }> }

describe('checkFormat matches the shared fixture', () => {
  for (const tc of fixture.cases) {
    it(tc.name, () => {
      const msg = checkFormat(tc.field, tc.value)
      if (tc.ok) expect(msg, `rejected a valid value: ${msg}`).toBe('')
      else expect(msg, 'accepted an invalid value').not.toBe('')
    })
  }

  it('grades against the whole fixture, not a handful of it', () => {
    expect(fixture.cases.length).toBeGreaterThanOrEqual(40)
  })
})

describe('every format is usable', () => {
  it('says what to type rather than quoting its pattern', () => {
    for (const [name, spec] of Object.entries(FORMATS)) {
      expect(spec.hint, `${name} has no hint`).toBeTruthy()
      expect(spec.label, `${name} has no label`).toBeTruthy()
      // A hint containing regex punctuation is a pattern leaking into the page.
      expect(spec.hint, `${name} quotes its own pattern`).not.toMatch(/[\\^$*+?[\]{}|]/)
    }
  })

  it('covers the formats the Go module defines', () => {
    // Kept in step by hand; the Go test asserts the same count from its side.
    expect(Object.keys(FORMATS).sort()).toEqual([
      'email',
      'integer',
      'national_id',
      'number',
      'phone_vn',
      'tax_code',
      'url',
    ])
  })
})
