/**
 * Shared primitives.
 *
 * Every screen in this app is a dense table or a form over the same API shapes,
 * so the pieces below exist to keep twenty screens from inventing twenty
 * slightly different tables. Anything appearing on more than two screens belongs
 * here rather than in one of them.
 */
import { useEffect, useState, type ReactNode } from 'react'
import { RequestFailed } from '../lib/api'

export function PageHeader({
  title,
  meta,
  actions,
}: {
  title: string
  meta?: ReactNode
  actions?: ReactNode
}) {
  return (
    <header className="mb-4 flex items-start justify-between gap-4">
      <div className="min-w-0">
        <h1 className="font-display text-[22px] font-semibold leading-tight tracking-[-.01em]">
          {title}
        </h1>
        {meta && <div className="id-chip mt-1">{meta}</div>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </header>
  )
}

export function Card({
  title,
  aside,
  children,
  className = '',
}: {
  title?: ReactNode
  aside?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={`card ${className}`}>
      {(title || aside) && (
        <div className="flex items-baseline justify-between gap-3 border-b border-line px-4 py-3">
          <h2 className="font-display text-[15px] font-semibold">{title}</h2>
          {aside && <div className="id-chip">{aside}</div>}
        </div>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}

export function Table({ head, children }: { head: ReactNode; children: ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-card border border-line bg-surface">
      <table className="w-full border-collapse text-body">
        <thead>
          <tr className="border-b border-line bg-panel text-left">{head}</tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  )
}

/** A column heading. children is optional: an actions column has no label, and
 *  requiring one there produced either a compile error or an invented word. */
export function Th({ children, className = '' }: { children?: ReactNode; className?: string }) {
  return (
    <th
      scope="col"
      className={`cap whitespace-nowrap px-4 py-2.5 ${className}`}
    >
      {children}
    </th>
  )
}

export function Td({
  children,
  className = '',
  colSpan,
}: {
  children?: ReactNode
  className?: string
  colSpan?: number
}) {
  return (
    <td colSpan={colSpan} className={`px-3 py-2 align-top ${className}`}>
      {children}
    </td>
  )
}

export function Tr({ children, className = '' }: { children: ReactNode; className?: string }) {
  return (
    <tr className={`border-b border-line-soft last:border-0 hover:bg-panel ${className}`}>
      {children}
    </tr>
  )
}

type Tone = 'neutral' | 'accent' | 'overdue' | 'duesoon' | 'ok'

const toneClass: Record<Tone, string> = {
  neutral: 'border-line bg-chrome text-muted',
  accent: 'border-accent-line bg-accent-wash text-accent',
  // Earth red, and only where a deadline or an erasure is at stake.
  overdue: 'border-legal-line bg-legal-wash text-legal',
  duesoon: 'border-duesoon/30 bg-duesoon/5 text-duesoon',
  ok: 'border-accent-line bg-accent-wash text-accent',
}

/** A label, never a bare colour. Colour alone fails for the reader who cannot
 *  distinguish these two reds, and this app uses them for legal deadlines. */
export function StatusPill({ children, tone = 'neutral' }: { children: ReactNode; tone?: Tone }) {
  return (
    <span
      className={`inline-block whitespace-nowrap rounded border px-2 py-0.5 font-mono text-[11px] font-medium ${toneClass[tone]}`}
    >
      {children}
    </span>
  )
}

/** Marks a field or column holding sensitive personal data.
 *
 * Present wherever such a field appears -- list, grid, export dialog -- because
 * whether data is sensitive changes who may read it and how it must be erased,
 * and someone acting on it should never have to remember which fields those
 * were. */
export function SensitiveTag({ children = 'nhạy cảm' }: { children?: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 rounded border border-legal-line bg-legal-wash px-2 py-0.5 font-mono text-[11px] font-medium text-legal">
      <span aria-hidden>◆</span>
      {children}
    </span>
  )
}

/**
 * A bordered aside carrying a consequence, not a decoration.
 *
 * Four screens built their own before this existed, which is the reliable signal
 * that a thing belongs here. Tone is required rather than defaulted: a callout
 * with no considered tone is usually one that did not need to be a callout.
 */
export function Callout({
  tone,
  title,
  children,
}: {
  tone: Tone
  title?: ReactNode
  children: ReactNode
}) {
  const wash: Record<Tone, string> = {
    neutral: 'border-line bg-panel',
    accent: 'border-accent-line bg-accent-wash',
    overdue: 'border-legal-line bg-legal-wash',
    duesoon: 'border-duesoon/25 bg-duesoon/[0.06]',
    ok: 'border-accent-line bg-accent-wash',
  }
  return (
    <div
      role={tone === 'overdue' ? 'alert' : undefined}
      className={`rounded-card border px-4 py-3 text-body ${wash[tone]}`}
    >
      {title && <p className="font-medium text-ink">{title}</p>}
      <div className={title ? 'mt-0.5' : ''}>{children}</div>
    </div>
  )
}

/** A labelled figure. */
export function Stat({ label, value, note }: { label: ReactNode; value: ReactNode; note?: ReactNode }) {
  return (
    <div>
      <div className="cap">{label}</div>
      <div className="font-display text-[26px] font-semibold leading-none tracking-[-.01em]">
        {value}
      </div>
      {note && <div className="id-chip mt-1.5">{note}</div>}
    </div>
  )
}

export function Empty({ title, hint }: { title: string; hint?: ReactNode }) {
  return (
    <div className="rounded-card border border-dashed border-line bg-panel px-6 py-10 text-center">
      <p className="font-display text-[15px] font-semibold">{title}</p>
      {/* Why it is empty, not just that it is. "Chưa có dữ liệu" leaves a reader
          unsure whether the screen is broken or the work simply has not started. */}
      {hint && <p className="mx-auto mt-2 max-w-md text-body text-muted">{hint}</p>}
    </div>
  )
}

export function Loading({ label = 'Đang tải…' }: { label?: string }) {
  return (
    <p role="status" className="px-1 py-4 text-body text-muted">
      {label}
    </p>
  )
}

export function ErrorBanner({ error, retry }: { error: unknown; retry?: () => void }) {
  const failed = error instanceof RequestFailed ? error : null
  return (
    <div
      role="alert"
      className="rounded-card border border-legal-line bg-legal-wash px-4 py-3 text-body text-legal"
    >
      <p className="font-medium">{failed?.body.title ?? 'Không tải được dữ liệu.'}</p>
      {failed?.body.trace_id && (
        // Shown so a report to whoever runs the deployment can be matched to the
        // exact request in the log, instead of "it broke this morning".
        <p className="id-chip mt-1 text-legal/70">mã tra cứu: {failed.body.trace_id}</p>
      )}
      {retry && (
        <button type="button" onClick={retry} className="btn mt-2">
          Thử lại
        </button>
      )}
    </div>
  )
}

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string
  hint?: ReactNode
  error?: string
  children: ReactNode
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-chip font-medium text-ink">{label}</span>
      {children}
      {hint && !error && <span className="text-meta text-muted">{hint}</span>}
      {error && (
        <span role="alert" className="text-meta text-legal">
          {error}
        </span>
      )}
    </label>
  )
}

/** Number formatting, one place. Vietnamese groups with dots, and a table of
 *  counts that mixes separators reads as two different systems. */
export function num(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—'
  return n.toLocaleString('vi-VN')
}

/** A rate, or an em dash when there is no denominator.
 *
 * Printing 0% when nothing has been measured is the more damaging of the two
 * mistakes: it reads as a real, bad number rather than as an absent one. */
export function pct(numerator: number, denominator: number): string {
  if (!denominator) return '—'
  return `${Math.round((numerator / denominator) * 100)}%`
}

export function date(iso: string | null | undefined): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleDateString('vi-VN')
}

export function dateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  return new Date(iso).toLocaleString('vi-VN', { dateStyle: 'short', timeStyle: 'short' })
}

export interface Deadline {
  text: string
  tone: Tone
  overdue: boolean
}

/**
 * How long is left against a deadline, phrased for a legal SLA.
 *
 * Overdue is stated in the same units as time remaining and never rounded down
 * to "hôm nay": under Decree 356/2025 a missed deadline is a sanction risk, and
 * the interface should not make six hours late look like nearly on time.
 */
export function deadline(dueAt: string | null | undefined, now = Date.now()): Deadline {
  if (!dueAt) return { text: 'không có hạn', tone: 'neutral', overdue: false }
  const diffHours = (new Date(dueAt).getTime() - now) / 3_600_000

  if (diffHours < 0) {
    const late = Math.abs(diffHours)
    return {
      text: late < 24 ? `quá hạn ${Math.floor(late)}h` : `quá hạn ${Math.floor(late / 24)} ngày`,
      tone: 'overdue',
      overdue: true,
    }
  }
  return {
    text: diffHours < 24 ? `còn ${Math.floor(diffHours)}h` : `còn ${Math.floor(diffHours / 24)} ngày`,
    tone: diffHours < 24 ? 'duesoon' : 'neutral',
    overdue: false,
  }
}

/** Copy-to-clipboard with a short confirmation.
 *
 * Used wherever a value exists to be pasted elsewhere -- API keys, webhook
 * secrets, short URLs, public ids. Never writes to storage.
 */
export function useCopy(): { copied: boolean; copy: (text: string) => void } {
  const [copied, setCopied] = useState(false)
  useEffect(() => {
    if (!copied) return
    const t = setTimeout(() => setCopied(false), 2000)
    return () => clearTimeout(t)
  }, [copied])
  return {
    copied,
    copy: (text: string) => {
      void navigator.clipboard.writeText(text).then(() => setCopied(true))
    },
  }
}

/** How long ago, for things that already happened.
 *
 * Distinct from deadline(), which counts down to an obligation. Mixing the two
 * would let "2 giờ trước" and "còn 2 giờ" render through the same path, and they
 * are read very differently on a compliance queue.
 */
export function relativeTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return '—'
  const diffMinutes = (now - new Date(iso).getTime()) / 60_000
  if (diffMinutes < 1) return 'vừa xong'
  if (diffMinutes < 60) return `${Math.floor(diffMinutes)} phút trước`
  if (diffMinutes < 24 * 60) return `${Math.floor(diffMinutes / 60)} giờ trước`
  if (diffMinutes < 48 * 60) return 'hôm qua'
  if (diffMinutes < 30 * 24 * 60) return `${Math.floor(diffMinutes / (24 * 60))} ngày trước`
  return date(iso)
}
