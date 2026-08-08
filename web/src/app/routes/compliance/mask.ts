/**
 * Masking data subject identifiers for the compliance screens.
 *
 * These screens sit open on an office monitor all day. Whoever works the queue
 * needs to tell two cases apart, not to read a customer's email address -- and
 * anyone walking past the desk should not be able to read it either. So the
 * queue shows a masked form of the identifier and never the raw value.
 *
 * Pure functions on purpose. This is the one part of these screens whose
 * correctness cannot be checked by looking at them: a mask that quietly stops
 * masking still renders perfectly, which is why it is the part that is tested.
 *
 * Every function here is idempotent -- masking an already masked value returns
 * it unchanged. The API may start returning pre-masked identifiers, and a second
 * pass must not turn `n***@gmail.com` into `n***`.
 */

export type IdentifierKind = 'email' | 'phone'

/** Separators people type into phone numbers; they carry no information. */
const PHONE_SEPARATORS = /[\s.()-]/g

/** A phone-ish value: digits, an optional leading +, and any masking already
 *  applied. The asterisks matter -- without them a masked number would fail this
 *  test and fall through to the opaque mask, losing the digits an operator uses
 *  to recognise the case. */
const PHONE_LIKE = /^\+?[\d*]{5,}$/

/** A value that has already been reduced to nothing but mask characters. */
const ALREADY_HIDDEN = /^\*+$/

/**
 * maskEmail keeps the first character of the local part and the whole domain.
 *
 * The domain stays because it is rarely identifying on its own and it tells the
 * reader which channel the person used. The local part is the identifying half,
 * so only its first character survives -- enough to distinguish two rows in a
 * queue, not enough to write to someone.
 */
export function maskEmail(raw: string): string {
  const value = raw.trim()
  const at = value.lastIndexOf('@')
  if (at < 0) return maskOpaque(value)

  const local = value.slice(0, at)
  const rest = value.slice(at) // includes the '@'

  // A one-character local part is entirely revealed by revealing its first
  // character, so nothing is kept. An already hidden local part is left as it is
  // rather than growing an asterisk on every pass.
  if (local.length <= 1 || ALREADY_HIDDEN.test(local)) return `***${rest}`
  return `${local.slice(0, 1)}***${rest}`
}

/**
 * maskPhone hides the two digits after the network prefix.
 *
 * The shape comes from the wireframe: `09**234567`. The prefix stays because it
 * is shared by millions of subscribers, and the tail stays because it is what an
 * operator reads back when a person is on the phone quoting their own number.
 *
 * The value is normalised first, so the same subscriber written `+84901234567`,
 * `84901234567` or `090 123 4567` renders as one and the same person. Two
 * spellings of one number in a queue read as two cases.
 */
export function maskPhone(raw: string): string {
  const value = normalisePhone(raw.trim())
  if (value.length <= 4) return '*'.repeat(value.length)
  return `${value.slice(0, 2)}**${value.slice(4)}`
}

/** normalisePhone drops formatting and puts a Vietnamese number into its
 *  national form. */
function normalisePhone(value: string): string {
  const compact = value.replace(PHONE_SEPARATORS, '')
  if (compact.startsWith('+84')) return `0${compact.slice(3)}`
  // A bare 84 prefix is only a country code when the number is longer than a
  // national one; 0847… is a real subscriber number starting with 84 after the 0.
  if (compact.startsWith('84') && compact.length >= 11) return `0${compact.slice(2)}`
  return compact
}

/** maskOpaque is the fallback for a value whose shape is not recognised. It
 *  reveals one character, because guessing wrong about the format must never
 *  reveal more than the formats we do understand. */
export function maskOpaque(raw: string): string {
  const value = raw.trim()
  if (value.length <= 1 || ALREADY_HIDDEN.test(value)) return '***'
  return `${value.slice(0, 1)}***`
}

/**
 * maskIdentifier masks whatever identifier the API supplied.
 *
 * `kind` comes from the API when it knows (the subject table records email or
 * phone); otherwise the shape decides. Returns null rather than an empty string
 * when there is nothing to show, so the caller has to choose a fallback instead
 * of silently rendering a blank where a person should be.
 */
export function maskIdentifier(
  value: string | null | undefined,
  kind?: IdentifierKind | null,
): string | null {
  if (value === null || value === undefined) return null
  const trimmed = value.trim()
  if (trimmed === '') return null

  if (kind === 'email') return maskEmail(trimmed)
  if (kind === 'phone') return maskPhone(trimmed)

  if (trimmed.includes('@')) return maskEmail(trimmed)
  if (PHONE_LIKE.test(trimmed.replace(PHONE_SEPARATORS, ''))) return maskPhone(trimmed)
  return maskOpaque(trimmed)
}

/** shortId shortens an opaque id for display. Ids are shown in full nowhere on
 *  these screens; they exist to be quoted in a support thread. */
export function shortId(id: string, keep = 8): string {
  const value = id.trim()
  if (value.length <= keep) return value
  return `${value.slice(0, keep)}…`
}

/**
 * subjectLabel names a data subject in a list.
 *
 * The admin DSR endpoint deliberately returns only the subject id today -- the
 * queue is meant to be workable without reading anyone's contact details. When
 * an identifier is present it is masked; when it is not, the row is named by its
 * subject id, which is still stable enough to follow one case across screens.
 */
export function subjectLabel(
  subjectId: string,
  identifier?: string | null,
  kind?: IdentifierKind | null,
): string {
  return maskIdentifier(identifier, kind) ?? `chủ thể ${shortId(subjectId)}`
}
