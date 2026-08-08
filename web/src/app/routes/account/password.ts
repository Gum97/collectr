/**
 * Password rules, evaluated in the browser.
 *
 * Three states, not two. A rule the browser cannot decide -- "has this password
 * appeared in a breach dump?" -- reports `unknown` rather than a tick, because a
 * tick claims a check was performed. The same reasoning as printing `—` instead
 * of `0%` when there is no denominator: an unmeasured thing must not look like a
 * measured one.
 *
 * Nothing here is enforcement. The server validates again on every write; this
 * exists so somebody choosing a password finds out before submitting it.
 */

/** MinLength on the server (internal/platform/password). Kept in step by hand. */
export const MIN_LENGTH = 12

/** MaxLength on the server: a bound on hashing cost, not a security rule. */
export const MAX_LENGTH = 1024

export type RuleState = 'pass' | 'fail' | 'unknown'

export type RuleId = 'length' | 'max_length' | 'breached' | 'reuse' | 'personal'

export interface PasswordRule {
  id: RuleId
  label: string
  state: RuleState
  /** Why the rule could not be decided, or what went wrong. */
  detail?: string
}

export type Score = 0 | 1 | 2 | 3 | 4

export interface PasswordCheck {
  rules: PasswordRule[]
  score: Score
  scoreLabel: string
  /** True when no rule the browser could decide came back failing. */
  ok: boolean
}

export interface PasswordContext {
  /** The account the password belongs to, so it can be rejected as a component. */
  email?: string
  /** Only known on the change-password form; absent during a reset by email. */
  currentPassword?: string
}

const SCORE_LABELS: Record<Score, string> = {
  0: 'rất yếu',
  1: 'yếu',
  2: 'tạm được',
  3: 'khá',
  4: 'mạnh',
}

/**
 * The few thousand most-used passwords are a real breach list; this is not one.
 *
 * It holds the handful that turn up first in any credential-stuffing run,
 * including the Vietnamese ones an English list would miss. It exists to catch
 * the obvious case at the keystroke, and its presence is never reported as "this
 * password is not breached" -- only as "this one certainly is".
 */
const NOTORIOUS = new Set([
  'password',
  'passw0rd',
  'qwerty',
  'qwertyuiop',
  'iloveyou',
  'letmein',
  'admin',
  'administrator',
  'welcome',
  'monkey',
  'dragon',
  'sunshine',
  'football',
  'baseball',
  'princess',
  'abcabc',
  'abcdef',
  'abcdefg',
  'asdfgh',
  'zxcvbn',
  'matkhau',
  'khongbiet',
  'vietnam',
  'hanoi',
  'saigon',
  'anhyeuem',
  'emyeuanh',
  'toiyeuvietnam',
  'collectr',
])

/** Leet substitutions, applied only to strings that still contain letters. */
const LEET: Record<string, string> = {
  '0': 'o',
  '1': 'l',
  '3': 'e',
  '4': 'a',
  '5': 's',
  '7': 't',
  '8': 'b',
  '@': 'a',
  $: 's',
  '!': 'i',
  '+': 't',
}

/** Length in characters, not bytes.
 *
 * The server counts bytes, so a Vietnamese password passes there sooner than it
 * does here. Being the stricter of the two is the safe direction: this can only
 * ask for a longer password than the server would accept, never wave through one
 * the server will reject. */
function length(pw: string): number {
  return Array.from(pw).length
}

/** Drops the year, the exclamation mark and whatever else was bolted on the end. */
function stripSuffix(s: string): string {
  return s.replace(/[^\p{L}]+$/u, '')
}

function deleet(s: string): string {
  return Array.from(s)
    .map((ch) => LEET[ch] ?? ch)
    .join('')
}

/**
 * Reduces a password to the word somebody was thinking of when they chose it.
 *
 * The suffix comes off before the substitutions, otherwise "p@ssw0rd!2026"
 * turns "2026" into "2o26" and the word underneath is never recovered.
 */
function candidates(pw: string): string[] {
  const lower = pw.toLowerCase().trim()
  const forms = new Set<string>([lower, stripSuffix(lower)])

  for (const form of [lower, stripSuffix(lower)]) {
    if (!/[a-z]/.test(form)) continue
    const plain = deleet(form)
    forms.add(plain)
    forms.add(stripSuffix(plain))
    // Punctuation dropped last: "p_a_s_s_w_o_r_d" is the same idea.
    forms.add(plain.replace(/[^a-z0-9]/g, ''))
    forms.add(stripSuffix(plain).replace(/[^a-z0-9]/g, ''))
  }

  forms.delete('')
  return [...forms]
}

/** Keyboard rows and the digits, tripled so a run that wraps still matches. */
const RUNS = ['0123456789', 'abcdefghijklmnopqrstuvwxyz', 'qwertyuiop', 'asdfghjkl', 'zxcvbnm'].map(
  (row) => row.repeat(3),
)

/** One character repeated, or a straight run up or down the keyboard. */
function isTrivialRun(pw: string): boolean {
  const straight = pw.toLowerCase()
  const chars = Array.from(straight)
  if (chars.length < 4) return false
  if (new Set(chars).size === 1) return true

  const reversed = chars.reverse().join('')
  return RUNS.some((row) => row.includes(straight) || row.includes(reversed))
}

function looksNotorious(pw: string): boolean {
  if (isTrivialRun(pw)) return true
  return candidates(pw).some((form) => NOTORIOUS.has(form))
}

/** The part of an address before the @, when it is long enough to be a real name. */
function emailLocalPart(email: string | undefined): string | null {
  if (!email) return null
  const local = email.split('@')[0]?.toLowerCase().trim() ?? ''
  return local.length >= 3 ? local : null
}

function characterClasses(pw: string): number {
  let n = 0
  if (/[a-z]/.test(pw)) n++
  if (/[A-Z]/.test(pw)) n++
  if (/[0-9]/.test(pw)) n++
  if (/[^A-Za-z0-9]/.test(pw)) n++
  return n
}

function clamp(n: number): Score {
  return Math.max(0, Math.min(4, n)) as Score
}

/**
 * Scores length first and composition second, on purpose.
 *
 * Composition rules mostly teach people to append "1!"; a long ordinary phrase
 * beats a short scrambled one, and the meter should say so.
 */
function strength(pw: string, failed: boolean): Score {
  if (!pw) return 0
  if (failed) return 0

  const n = length(pw)
  let s = 0
  if (n >= 8) s++
  if (n >= MIN_LENGTH) s++
  if (n >= 16) s++
  if (characterClasses(pw) >= 3 || n >= 24) s++

  // A long password made of four distinct characters is not a long password.
  if (new Set(Array.from(pw.toLowerCase())).size < 5) s -= 2
  return clamp(s)
}

/**
 * Checks a candidate password and returns one row per rule, in display order.
 *
 * The rows mirror the wireframe: minimum length, breach list, and reuse. Rules
 * that need information the caller did not supply come back `unknown` rather
 * than being silently dropped, so the same three lines are on screen whether
 * this is a reset by email or a change from the account page.
 */
export function checkPassword(pw: string, ctx: PasswordContext = {}): PasswordCheck {
  const rules: PasswordRule[] = []
  const n = length(pw)

  rules.push({
    id: 'length',
    label: `tối thiểu ${MIN_LENGTH} ký tự`,
    state: pw === '' ? 'unknown' : n >= MIN_LENGTH ? 'pass' : 'fail',
    detail: pw === '' ? undefined : n < MIN_LENGTH ? `còn thiếu ${MIN_LENGTH - n} ký tự` : undefined,
  })

  // Only shown when broken. A limit nobody is near is noise on a form that is
  // already asking somebody to read five lines before they can continue.
  if (n > MAX_LENGTH) {
    rules.push({
      id: 'max_length',
      label: `không quá ${MAX_LENGTH.toLocaleString('vi-VN')} ký tự`,
      state: 'fail',
    })
  }

  rules.push(
    pw === ''
      ? { id: 'breached', label: 'không nằm trong danh sách mật khẩu bị lộ', state: 'unknown' }
      : looksNotorious(pw)
        ? {
            id: 'breached',
            label: 'không nằm trong danh sách mật khẩu bị lộ',
            state: 'fail',
            detail: 'đây là một trong những mật khẩu bị thử đầu tiên',
          }
        : {
            id: 'breached',
            label: 'không nằm trong danh sách mật khẩu bị lộ',
            state: 'unknown',
            // Said plainly rather than shown as a tick: the browser only knows a
            // short list of the worst offenders, and a tick here would read as a
            // guarantee nobody has checked.
            detail: 'trình duyệt chỉ chặn được các mật khẩu phổ biến nhất',
          },
  )

  rules.push(
    ctx.currentPassword === undefined
      ? {
          id: 'reuse',
          label: 'không được trùng mật khẩu cũ',
          state: 'unknown',
          detail: 'không kiểm tra được ở bước này',
        }
      : {
          id: 'reuse',
          label: 'không được trùng mật khẩu cũ',
          state: pw === '' ? 'unknown' : pw === ctx.currentPassword ? 'fail' : 'pass',
        },
  )

  const local = emailLocalPart(ctx.email)
  if (local) {
    const contains = pw !== '' && pw.toLowerCase().includes(local)
    rules.push({
      id: 'personal',
      label: 'không chứa tên tài khoản email',
      state: pw === '' ? 'unknown' : contains ? 'fail' : 'pass',
    })
  }

  const failed = rules.some((r) => r.state === 'fail')
  const score = strength(pw, failed)

  return { rules, score, scoreLabel: SCORE_LABELS[score], ok: pw !== '' && !failed }
}
