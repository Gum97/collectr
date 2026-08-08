import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { evalCondition, evaluate, type Answers, type Condition, type Schema } from './engine'

/**
 * The fixture is read from the Go package's own testdata, not copied.
 *
 * A copy is a fork with a delay: the two files agree until someone edits one,
 * and the divergence surfaces as a form that behaves differently in the browser
 * than on the server. Reading the same bytes means a rule added on the Go side
 * without a matching client change fails here immediately.
 */
const golden = JSON.parse(
  readFileSync(
    new URL('../../../internal/modules/forms/engine/testdata/golden.json', import.meta.url),
    'utf8',
  ),
) as {
  schema: Schema
  cases: {
    name: string
    answers: Answers
    want_path?: string[]
    want_visible?: string[]
    want_required?: string[]
    want_error?: boolean
  }[]
  condition_cases: {
    name: string
    cond: Condition
    answers: Answers
    want?: boolean
    want_error?: boolean
  }[]
}

describe('evaluate matches the shared golden fixtures', () => {
  for (const tc of golden.cases) {
    it(tc.name, () => {
      if (tc.want_error) {
        expect(() => evaluate(golden.schema, tc.answers)).toThrow()
        return
      }
      const got = evaluate(golden.schema, tc.answers)
      if (tc.want_path) expect(got.path).toEqual(tc.want_path)
      if (tc.want_visible) expect(got.visible).toEqual(tc.want_visible)
      if (tc.want_required) expect(got.required).toEqual(tc.want_required)
    })
  }

  it('reads a fixture file that actually has cases in it', () => {
    // Guards the path above: a renamed or moved fixture would otherwise make
    // this suite pass by testing nothing.
    expect(golden.cases.length).toBeGreaterThan(4)
  })
})

describe('evalCondition matches the shared operator fixtures', () => {
  for (const tc of golden.condition_cases) {
    it(tc.name, () => {
      if (tc.want_error) {
        expect(() => evalCondition(tc.cond, tc.answers)).toThrow()
        return
      }
      expect(evalCondition(tc.cond, tc.answers)).toBe(tc.want)
    })
  }

  it('runs the full operator set, not a subset', () => {
    // These cases were Go-only until they moved into the fixture. If they ever
    // drift back out, this fails rather than the suite quietly shrinking.
    expect(golden.condition_cases.length).toBeGreaterThanOrEqual(15)
  })
})
