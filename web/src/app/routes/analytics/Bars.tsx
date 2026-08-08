/**
 * Chart parts for the analytics cluster.
 *
 * Plain CSS boxes and one inline SVG, no charting library: everything drawn here
 * is a horizontal bar or a polyline over at most a few hundred points, and a
 * chart library would arrive with its own colour system, its own tooltip and its
 * own idea of what an axis looks like -- three things this app already decided.
 *
 * Two rules the callers rely on:
 *   - a bar is never the only place a number appears. Every row prints its own
 *     count as text, so a bar that renders wrong cannot silently change what the
 *     reader believes.
 *   - the share label is computed with pct(), which prints an em dash when the
 *     denominator is zero. A funnel whose first step is zero -- a form reached
 *     directly rather than through a short link -- must not read as "0%".
 */
import type { ReactNode } from 'react'
import { num, pct } from '../../components/ui'

/** Shade ramp for funnel steps, dark to light. Decorative only: each row carries
 *  its label and its count as text beside the bar. */
const shades = ['bg-ink', 'bg-muted', 'bg-faint', 'bg-chrome']

export interface Step {
  key: string
  label: string
  value: number
  /** Extra line under the label -- where the number came from, usually. */
  note?: ReactNode
}

/**
 * The funnel itself: one bar per step, widths against the largest step.
 *
 * Widths are scaled by the largest step rather than by `base`, because the two
 * differ exactly when `base` is zero or smaller than a later step, and in that
 * case scaling by `base` would produce bars wider than their track (or none at
 * all). The percentage label still uses `base`, which is the honest denominator
 * and may legitimately have no answer.
 */
export function StepBars({ steps, base }: { steps: Step[]; base: number }) {
  const widest = Math.max(base, ...steps.map((s) => s.value))

  return (
    <ol className="flex flex-col gap-2">
      {steps.map((s, i) => {
        const width = widest > 0 ? (s.value / widest) * 100 : 0
        return (
          <li key={s.key} className="flex items-center gap-3">
            <div className="w-28 shrink-0 text-right">
              <div className="text-meta font-semibold leading-tight">{s.label}</div>
              {s.note && <div className="id-chip leading-tight">{s.note}</div>}
            </div>
            <div className="h-6 flex-1 border border-line bg-surface" aria-hidden>
              <div
                className={`h-full ${shades[i] ?? 'bg-chrome'}`}
                style={{ width: `${width}%` }}
              />
            </div>
            <div className="w-28 shrink-0 font-mono text-meta">
              {num(s.value)} · {pct(s.value, base)}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

/**
 * One page of a form: how many arrived, and how much of that arrival was lost.
 *
 * The lost part is drawn at the end of the bar rather than as a second bar, so
 * the eye compares losses against the arrivals that produced them and not
 * against the losses of a page nobody reached.
 */
export function SplitBar({
  entered,
  left,
  widest,
  danger = false,
}: {
  entered: number
  left: number
  widest: number
  danger?: boolean
}) {
  if (entered <= 0) {
    return (
      <div
        className="h-5 border border-dashed border-line"
        title="không có lượt vào trang này trong kỳ"
        aria-hidden
      />
    )
  }
  const span = widest > 0 ? (entered / widest) * 100 : 0
  const lost = Math.min(left, entered)
  const lostShare = (lost / entered) * 100

  return (
    <div className="h-5" aria-hidden>
      <div className="flex h-full border border-line" style={{ width: `${span}%` }}>
        <div className="h-full bg-ink" style={{ width: `${100 - lostShare}%` }} />
        <div
          className={`h-full ${danger ? 'bg-overdue' : 'bg-accent'}`}
          style={{ width: `${lostShare}%` }}
        />
      </div>
    </div>
  )
}

export interface Series {
  key: string
  label: string
  values: number[]
  /** Stroke pattern, so the series stay apart for a reader who cannot tell the
   *  greys apart. The legend repeats the pattern next to the name. */
  dash?: string
  className: string
}

/**
 * Series over time, as one SVG.
 *
 * `preserveAspectRatio="none"` lets the drawing stretch to whatever width the
 * card has; `vector-effect="non-scaling-stroke"` keeps the lines one pixel wide
 * while it does, which is the whole reason this is hand-drawn rather than a
 * <div> per bucket.
 */
export function TrendChart({
  buckets,
  series,
  label,
}: {
  buckets: string[]
  series: Series[]
  label: string
}) {
  const max = Math.max(0, ...series.flatMap((s) => s.values))
  if (max === 0 || buckets.length === 0) {
    return (
      <p className="px-1 py-3 text-meta text-muted">
        Chưa có sự kiện nào trong kỳ này, nên không có đường nào để vẽ.
      </p>
    )
  }

  const W = 600
  const H = 100
  const pad = 3
  const x = (i: number, n: number) => (n <= 1 ? W / 2 : (i / (n - 1)) * W)
  const y = (v: number) => H - pad - (v / max) * (H - pad * 2)

  const first = buckets[0]
  const last = buckets[buckets.length - 1]

  return (
    <figure className="m-0">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={label}
        className="h-24 w-full border border-line bg-surface"
      >
        {/* Midline, so a reader can judge halfway without an axis. */}
        <line
          x1={0}
          y1={y(max / 2)}
          x2={W}
          y2={y(max / 2)}
          className="stroke-line"
          strokeDasharray="4 4"
          vectorEffect="non-scaling-stroke"
        />
        {series.map((s) => (
          <path
            key={s.key}
            d={s.values
              .map(
                (v, i) =>
                  `${i === 0 ? 'M' : 'L'}${x(i, s.values.length).toFixed(1)},${y(v).toFixed(1)}`,
              )
              .join(' ')}
            fill="none"
            strokeWidth={1.5}
            strokeDasharray={s.dash}
            vectorEffect="non-scaling-stroke"
            className={s.className}
          />
        ))}
      </svg>
      <figcaption className="mt-1 flex flex-wrap items-center justify-between gap-2">
        <ul className="flex flex-wrap items-center gap-3">
          {series.map((s) => (
            <li key={s.key} className="flex items-center gap-1 text-meta">
              <svg width="18" height="6" aria-hidden className="shrink-0">
                <line
                  x1="0"
                  y1="3"
                  x2="18"
                  y2="3"
                  strokeWidth={1.5}
                  strokeDasharray={s.dash}
                  className={s.className}
                />
              </svg>
              {s.label}
            </li>
          ))}
        </ul>
        <span className="id-chip">
          {first} → {last} · đỉnh {num(max)}
        </span>
      </figcaption>
    </figure>
  )
}
