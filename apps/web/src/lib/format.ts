/** Formatting helpers, tuned for a small screen: short and unambiguous. */

export function bytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = n
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value >= 100 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

export function percent(used: number, total: number): number {
  if (!total) return 0
  return Math.min(100, Math.max(0, (used / total) * 100))
}

export function uptime(seconds: number): string {
  if (!seconds) return '—'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

/** duration between two instants, or from one until now. */
export function duration(from: string, to?: string | null): string {
  const start = new Date(from).getTime()
  const end = to ? new Date(to).getTime() : Date.now()
  const seconds = Math.max(0, Math.round((end - start) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

export function ago(iso: string | null | undefined): string {
  if (!iso) return 'never'
  const seconds = Math.round((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 0) return 'just now'
  if (seconds < 45) return 'just now'
  if (seconds < 90) return 'a minute ago'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.round(hours / 24)
  if (days < 30) return `${days}d ago`
  return new Date(iso).toLocaleDateString()
}

export function time(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  })
}

/**
 * shellQuote mirrors the server's quoting so the preview shows exactly what
 * will run: ordinary values are left alone, anything the shell could act on is
 * wrapped in single quotes. Keep this in step with deploy.ShellQuote.
 */
const shellSafe = /^[A-Za-z0-9._:/@=+,-]+$/

export function shellQuote(value: string): string {
  if (shellSafe.test(value)) return value
  return `'${value.replaceAll("'", `'\\''`)}'`
}

/**
 * renderCommand fills {{name}} placeholders the way the server will, so a
 * confirmation sheet shows exactly what is about to run rather than an
 * approximation of it. A placeholder nothing answers is left as written — the
 * server rejects it, and showing the raw braces is how that reads.
 */
export function renderCommand(template: string, values: Record<string, string>): string {
  return template.replace(/\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g, (match, name) =>
    name in values ? shellQuote(values[name]) : match,
  )
}

/** "port 8899", or "ports 8080, 8443" — and nothing at all when HostMan has
 *  no port to name, which reads better than an empty label. */
export function ports(list: number[] | undefined): string {
  if (!list || list.length === 0) return ''
  return `${list.length === 1 ? 'port' : 'ports'} ${list.join(', ')}`
}

/** The version an app is running across the hosts it is on: the one they
 *  agree on, or "2 versions" when they don't, because which host is behind is
 *  a question for the app's own page. Hosts that name no version are not
 *  counted — and when none of them do, nothing is shown. */
export function versions(list: (string | undefined)[]): string {
  const distinct = [...new Set(list.filter((v): v is string => !!v))]
  if (distinct.length === 0) return ''
  if (distinct.length === 1) return distinct[0]
  return `${distinct.length} versions`
}

/** parts joins the pieces of a card's subtitle, dropping the ones that had
 *  nothing to say. */
export function parts(...pieces: (string | false | null | undefined)[]): string {
  return pieces.filter(Boolean).join(' · ')
}

/** Nobody calls it "photos.service" out loud, so a systemd unit is shown by
 *  the part of its name that means something. */
export function serviceName(unit: string): string {
  return unit.replace(/\.service$/, '')
}

/** severity turns a usage percentage into a bar colour class. */
export function severity(pct: number): '' | 'warn' | 'bad' {
  if (pct >= 90) return 'bad'
  if (pct >= 75) return 'warn'
  return ''
}

/** The system as it is said out loud: "Debian 13 (trixie)", not the
 *  "Debian GNU/Linux 13 (trixie)" os-release spells out. Ubuntu and the rest
 *  already say it that way and pass through untouched. */
export function osName(os: string): string {
  return os.replace(' GNU/Linux', '')
}
