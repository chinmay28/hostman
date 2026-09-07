import { useCallback, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { ApiError } from '../api'
import { percent, severity } from '../lib/format'

export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`card ${className}`}>{children}</div>
}

export function SectionTitle({ children }: { children: ReactNode }) {
  return <h2 className="section-title">{children}</h2>
}

type Tone = 'good' | 'warn' | 'bad' | 'neutral' | 'accent'

export function Badge({
  tone = 'neutral',
  children,
  dot = false,
  pulse = false,
}: {
  tone?: Tone
  children: ReactNode
  dot?: boolean
  pulse?: boolean
}) {
  return (
    <span className={`badge ${tone}`}>
      {dot && <span className={`dot ${pulse ? 'pulse' : ''}`} />}
      {children}
    </span>
  )
}

/** Meter is a labelled usage bar: the main way host load is read at a glance. */
export function Meter({
  label,
  used,
  total,
  value,
  display,
  tone = 'usage',
}: {
  /** Usually a word, but a row about one process names it and its pid. */
  label: ReactNode
  used?: number
  total?: number
  value?: number
  display: string
  /** How a full bar reads. A usage bar goes amber and then red as it fills,
   *  because a full disk is trouble. A progress bar means the opposite — full
   *  is the thing you were waiting for — so it stays the accent colour all the
   *  way to the end. */
  tone?: 'usage' | 'progress'
}) {
  const pct = value ?? percent(used ?? 0, total ?? 0)
  return (
    <div>
      <div className="meter-label">
        <span>{label}</span>
        <b>{display}</b>
      </div>
      <div
        className={`bar ${tone === 'usage' ? severity(pct) : ''}`}
        role="meter"
        aria-valuenow={Math.round(pct)}
      >
        <span style={{ width: `${Math.max(pct, 1.5)}%` }} />
      </div>
    </div>
  )
}

/** RangeBar shows where a metric sat over a window rather than where it is now:
 *  a band from its low to its high, with a tick at the average. The colour
 *  follows the high, since that is the figure that means trouble. */
export function RangeBar({
  label,
  min,
  max,
  avg,
}: {
  label: string
  min: number
  max: number
  avg: number
}) {
  const lo = clampPct(min)
  const hi = clampPct(max)
  return (
    <div
      className={`bar range ${severity(hi)}`}
      role="meter"
      aria-label={label}
      aria-valuenow={Math.round(clampPct(avg))}
      aria-valuemin={Math.round(lo)}
      aria-valuemax={Math.round(hi)}
    >
      {/* A flat metric would otherwise be an invisible band, so the span keeps a
          sliver of width whatever the range. */}
      <span style={{ marginLeft: `${lo}%`, width: `${Math.max(hi - lo, 1.5)}%` }} />
      <i style={{ left: `${clampPct(avg)}%` }} />
    </div>
  )
}

function clampPct(v: number): number {
  return Math.min(100, Math.max(0, v || 0))
}

export function Empty({ message, action }: { message: string; action?: ReactNode }) {
  return (
    <div className="empty">
      <p>{message}</p>
      {action}
    </div>
  )
}

export function Banner({ tone, children }: { tone: 'good' | 'bad' | 'warn'; children: ReactNode }) {
  return <div className={`banner ${tone}`}>{children}</div>
}

/** Sheet is the bottom-sheet modal used for confirmations and forms. */
export function Sheet({
  title,
  subtitle,
  onClose,
  children,
}: {
  title: string
  subtitle?: string
  onClose: () => void
  children: ReactNode
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    document.addEventListener('keydown', onKey)
    // Stop the page behind the sheet from scrolling on iOS.
    const previous = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = previous
    }
  }, [onClose])

  return (
    <div className="scrim" onClick={onClose} role="dialog" aria-modal="true" aria-label={title}>
      <div className="sheet" onClick={(e) => e.stopPropagation()}>
        <div className="grabber" />
        <h2>{title}</h2>
        {subtitle && <p className="sub" style={{ marginTop: 0 }}>{subtitle}</p>}
        {children}
      </div>
    </div>
  )
}

/** Copyable shows a command with a one-tap copy button. */
export function Copyable({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      // Clipboard access needs a secure context; over plain http on the LAN
      // the user can still select the text manually.
      return
    }
    setCopied(true)
    setTimeout(() => setCopied(false), 1600)
  }
  return (
    <div className="copyable">
      <pre>{text}</pre>
      <button className="secondary" onClick={copy}>
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  )
}

export function Field({
  label,
  help,
  children,
}: {
  label: string
  help?: string
  children: ReactNode
}) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {help && <div className="help">{help}</div>}
    </div>
  )
}

/**
 * useLoader fetches once and then on an interval, without flashing a spinner
 * on every refresh — only the first load is "loading".
 */
export function useLoader<T>(
  load: () => Promise<T>,
  deps: unknown[],
  intervalMs?: number,
): {
  data: T | null
  error: string | null
  loading: boolean
  /** True when HostMan could not be reached at all, as opposed to refusing. */
  offline: boolean
  reload: () => void
} {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [offline, setOffline] = useState(false)
  const [loading, setLoading] = useState(true)
  const loadRef = useRef(load)
  loadRef.current = load
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    let cancelled = false
    const run = async () => {
      try {
        const result = await loadRef.current()
        if (!cancelled) {
          setData(result)
          setError(null)
          setOffline(false)
        }
      } catch (e) {
        if (!cancelled) {
          setError(e instanceof Error ? e.message : String(e))
          // Status 0 means the request never landed — HostMan is restarting,
          // or the network dropped. Either way it is worth waiting out.
          setOffline(e instanceof ApiError && e.status === 0)
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    run()
    if (!intervalMs) return () => {
      cancelled = true
    }
    const timer = setInterval(run, intervalMs)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce, intervalMs])

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { data, error, loading, offline, reload }
}

/**
 * Loading tells the user what is wrong, and — importantly — what isn't.
 * HostMan being briefly unreachable is normal while it updates itself, so
 * that reads as "reconnecting", not as a failure.
 */
export function Loading({
  error,
  offline,
  hasData,
}: {
  error: string | null
  offline?: boolean
  hasData: boolean
}) {
  if (!error) return null
  if (offline) {
    return (
      <Banner tone="warn">
        Can't reach HostMan — reconnecting…
        {hasData ? ' Showing the last state it reported.' : ''}
      </Banner>
    )
  }
  return <Banner tone="bad">{error}</Banner>
}

/** Sparkline draws a small trend line; flat when there is nothing to show. */
export function Sparkline({ values, max = 100 }: { values: number[]; max?: number }) {
  if (values.length < 2) {
    return <div className="sub" style={{ height: 44, display: 'grid', placeItems: 'center' }}>Not enough history yet</div>
  }
  const width = 300
  const height = 44
  const peak = Math.max(max, ...values) || 1
  const step = width / (values.length - 1)
  const points = values.map((v, i) => `${(i * step).toFixed(1)},${(height - (v / peak) * height).toFixed(1)}`)
  const area = `M0,${height} L${points.join(' L')} L${width},${height} Z`
  return (
    <svg className="sparkline" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
      <path d={area} fill="var(--accent-soft)" />
      <polyline
        points={points.join(' ')}
        fill="none"
        stroke="var(--accent)"
        strokeWidth="2"
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  )
}
