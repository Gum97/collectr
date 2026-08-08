/**
 * One-time secret display, shared by the integration screens.
 *
 * Webhook signing secrets and API keys are handed back by the API exactly once,
 * at creation, and only a hash or a sealed copy is kept afterwards. That is a
 * deliberate property -- a value the server can re-read is a value an attacker
 * who reaches the server can re-read -- and it makes this panel the only moment
 * the operator will ever see the string.
 *
 * So it does three things and refuses to do a fourth: it shows the value in
 * full, it copies it, and it will not disappear until the person says they have
 * stored it. The fourth thing, persisting it anywhere in the browser, is the
 * whole point of not doing.
 */
import { useState } from 'react'

export function SecretOnce({
  title,
  value,
  what,
  extra,
  onDone,
}: {
  /** Heading of the panel, e.g. "Khoá ký webhook". */
  title: string
  /** The secret itself. Never written anywhere but this component's props. */
  value: string
  /** One line naming what the secret is for, in the reader's terms. */
  what: string
  /** Extra warning specific to the caller (e.g. answers leaving the system). */
  extra?: React.ReactNode
  onDone: () => void
}) {
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)

  async function copy() {
    // clipboard is undefined on an insecure origin, so the failure path tells
    // the reader to select the text rather than silently doing nothing.
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setCopyFailed(false)
    } catch {
      setCopied(false)
      setCopyFailed(true)
    }
  }

  return (
    <section
      role="alert"
      aria-labelledby="secret-once-title"
      className="rounded border border-accent bg-accent/5 p-3"
    >
      <h2 id="secret-once-title" className="text-body font-semibold text-accent">
        <span aria-hidden>⚠ </span>
        {title} — hiện đúng một lần
      </h2>
      <p className="mt-1 text-body">{what}</p>
      <p className="mt-1 text-body font-semibold">
        Sao chép ngay và cất vào nơi quản lý bí mật của bạn. Đóng bảng này là mất vĩnh viễn:
        hệ thống không giữ bản đọc lại được, nên không ai — kể cả quản trị viên — hiện lại được
        giá trị này. Mất thì phải cấp lại cái mới.
      </p>

      <div className="mt-2 flex items-start gap-2">
        <code className="min-w-0 flex-1 select-all break-all rounded border border-line bg-surface px-2 py-1.5 font-mono text-body">
          {value}
        </code>
        <button type="button" onClick={copy} className="btn shrink-0">
          {copied ? '✓ Đã chép' : 'Sao chép'}
        </button>
      </div>

      {copyFailed && (
        <p role="alert" className="mt-1 text-meta text-overdue">
          Trình duyệt không cho chép tự động (thường do trang không chạy trên HTTPS).
          Hãy bôi đen chuỗi trên rồi chép tay.
        </p>
      )}
      {copied && (
        <p role="status" className="mt-1 text-meta text-muted">
          Đã nằm trong clipboard — nhớ dán vào nơi lưu trữ trước khi chép thứ khác đè lên.
        </p>
      )}

      {extra && <div className="mt-2 text-meta text-muted">{extra}</div>}

      <button type="button" onClick={onDone} className="btn-primary mt-3">
        Tôi đã lưu — đóng
      </button>
    </section>
  )
}

/**
 * A destination shown without its query string.
 *
 * Plenty of receivers authenticate by putting a token in the URL. That makes the
 * endpoint list a place where a shared screen, a screenshot or a shoulder leaks
 * the credential, so parameter names stay (they help identify the endpoint) and
 * values do not.
 */
export function shortUrl(raw: string): { text: string; redacted: boolean } {
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return { text: raw.length > 64 ? `${raw.slice(0, 64)}…` : raw, redacted: false }
  }

  let path = parsed.pathname === '/' ? '' : parsed.pathname
  if (path.length > 40) path = `${path.slice(0, 40)}…`

  const keys = Array.from(new Set(Array.from(parsed.searchParams.keys())))
  if (keys.length === 0) return { text: parsed.origin + path, redacted: false }

  const shown = keys.slice(0, 3).map((k) => `${k}=•••`)
  if (keys.length > 3) shown.push(`+${keys.length - 3}`)
  return { text: `${parsed.origin}${path}?${shown.join('&')}`, redacted: true }
}
