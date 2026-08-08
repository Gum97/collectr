/**
 * Named answer formats, mirroring internal/modules/forms/domain/format.go.
 *
 * Both sides are graded by internal/modules/forms/domain/testdata/formats.json,
 * read unchanged rather than copied. The client exists so a respondent sees the
 * problem while they are still on the field; the server exists because a
 * submission is an HTTP request and the page's opinion of it is advisory. When
 * the two drift, the failure lands on the person who already filled the form in.
 *
 * Deliberately not a tenant-supplied regular expression. This code runs in every
 * respondent's browser, where a backtracking pattern freezes the tab -- and the
 * administrator who pasted it would never see that happen.
 */

export interface FormatSpec {
  label: string
  /** Says what to type. Never quotes the pattern: "không khớp ^\d{10}$" helps nobody. */
  hint: string
  /** The keyboard a phone should offer. Wrong here means hunting for digits on a 12-digit id. */
  inputMode: 'text' | 'email' | 'tel' | 'numeric' | 'decimal' | 'url'
  /** Whether min/max are compared as numbers. */
  numeric?: boolean
  pattern: RegExp
  /** Removed before matching: people punctuate phone numbers and tax codes. */
  strip?: string
}

export const FORMATS: Record<string, FormatSpec> = {
  email: {
    label: 'Email',
    hint: 'Địa chỉ email chưa đúng dạng, ví dụ: ten@congty.vn',
    inputMode: 'email',
    pattern: /^[^@\s]+@[^@\s.]+(\.[^@\s.]+)+$/,
  },
  phone_vn: {
    label: 'Số điện thoại Việt Nam',
    hint: 'Số điện thoại gồm 10 chữ số bắt đầu bằng 0, ví dụ: 0912345678',
    inputMode: 'tel',
    strip: ' .-()',
    pattern: /^(?:0|\+84)\d{9}$/,
  },
  tax_code: {
    label: 'Mã số thuế',
    hint: 'Mã số thuế gồm 10 chữ số, hoặc 10 chữ số kèm 3 số đơn vị phụ thuộc (ví dụ 0123456789-001)',
    inputMode: 'numeric',
    strip: ' .',
    pattern: /^\d{10}(-\d{3})?$/,
  },
  national_id: {
    label: 'Số CCCD',
    hint: 'Số căn cước công dân gồm 12 chữ số',
    inputMode: 'numeric',
    strip: ' .',
    pattern: /^\d{12}$/,
  },
  url: {
    label: 'Đường dẫn',
    hint: 'Đường dẫn phải bắt đầu bằng http:// hoặc https://',
    inputMode: 'url',
    pattern: /^https?:\/\/[^\s/]+\.[^\s/]+/,
  },
  number: {
    label: 'Số',
    hint: 'Chỉ nhập số',
    inputMode: 'decimal',
    numeric: true,
    strip: ' ',
    pattern: /^-?\d+([.,]\d+)?$/,
  },
  integer: {
    label: 'Số nguyên',
    hint: 'Chỉ nhập số nguyên',
    inputMode: 'numeric',
    numeric: true,
    strip: ' ',
    pattern: /^-?\d+$/,
  },
}

/** The field shape this module reads, matching the Go json tags exactly. */
export interface FormattedField {
  type: string
  format?: string
  min?: string
  max?: string
}

function stripChars(s: string, cutset?: string): string {
  if (!cutset) return s
  let out = ''
  for (const ch of s) if (!cutset.includes(ch)) out += ch
  return out
}

/**
 * Checks one answer against its field's format and bounds.
 *
 * Returns a message for the respondent, or '' when the value passes. A blank
 * value always passes: whether blank is acceptable is required-ness, decided by
 * the rule engine. Deciding it in two places is how a field becomes
 * unanswerable — hidden by a rule, and still rejected for being empty.
 */
export function checkFormat(field: FormattedField, raw: string): string {
  const v = raw.trim()
  if (v === '') return ''

  if (field.format) {
    const spec = FORMATS[field.format]
    // An unknown format means the page is older or newer than the schema that
    // reached it. Refusing the answer would lose it.
    if (!spec) return ''
    const cleaned = stripChars(v, spec.strip)
    if (!spec.pattern.test(cleaned)) return spec.hint
    if (spec.numeric) return checkNumericBounds(field, cleaned)
    return ''
  }

  if (field.type === 'date') return checkDateBounds(field, v)
  return ''
}

function checkNumericBounds(field: FormattedField, v: string): string {
  if (!field.min && !field.max) return ''
  // The comma is the Vietnamese decimal separator and the pattern allowed one,
  // so the parse has to as well.
  const n = Number(v.replace(',', '.'))
  if (Number.isNaN(n)) return ''
  if (field.min !== undefined && field.min !== '') {
    const lo = Number(field.min)
    if (!Number.isNaN(lo) && n < lo) return `Giá trị phải từ ${field.min} trở lên`
  }
  if (field.max !== undefined && field.max !== '') {
    const hi = Number(field.max)
    if (!Number.isNaN(hi) && n > hi) return `Giá trị không được vượt quá ${field.max}`
  }
  return ''
}

/** ISO dates sort lexicographically, so a string compare is correct and cannot
 *  pick up a timezone the way Date parsing would. */
function checkDateBounds(field: FormattedField, v: string): string {
  if (field.min && v < field.min) return `Ngày phải từ ${field.min} trở đi`
  if (field.max && v > field.max) return `Ngày không được sau ${field.max}`
  return ''
}

/** The inputmode attribute for a field, or undefined for ordinary text. */
export function inputModeFor(field: FormattedField): string | undefined {
  return field.format ? FORMATS[field.format]?.inputMode : undefined
}
