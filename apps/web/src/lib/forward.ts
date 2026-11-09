import type { Forward } from '../types'

/**
 * Port forwarding: HostMan listens on a port of its own machine and relays to
 * the app on its host, over SSH. The point is reaching an app from a phone that
 * can reach HostMan but not the app's host, so the link is built from the
 * address this page was loaded from: whatever the phone used to reach HostMan
 * also reaches the forward.
 */

/** The form's two fields, as typed. Empty means not set. */
export interface ForwardFields {
  enabled: boolean
  listen: string
  target: string
}

/** What the API takes: zeros turn forwarding off. A field that isn't a whole
 *  number is passed as-is (NaN becomes 0) and the server says what's wrong. */
export function forwardInput(f: ForwardFields): { forwardPort: number; forwardTargetPort: number } {
  if (!f.enabled) return { forwardPort: 0, forwardTargetPort: 0 }
  const n = (s: string) => {
    const v = Number(s.trim())
    return Number.isInteger(v) ? v : 0
  }
  return { forwardPort: n(f.listen), forwardTargetPort: n(f.target) }
}

/** The form's fields for an app as saved. */
export function forwardFields(app: { forwardPort: number; forwardTargetPort: number }): ForwardFields {
  return {
    enabled: app.forwardPort > 0,
    listen: app.forwardPort > 0 ? String(app.forwardPort) : '',
    target: app.forwardTargetPort > 0 ? String(app.forwardTargetPort) : '',
  }
}

/** Where the forward is reached from this browser: HostMan's own host name,
 *  the forward's port. */
export function forwardURL(port: number, hostname: string = window.location.hostname): string {
  const host = hostname.includes(':') && !hostname.startsWith('[') ? `[${hostname}]` : hostname
  return port === 80 ? `http://${host}/` : `http://${host}:${port}/`
}

/** One line on how the forward is doing. */
export function forwardSummary(f: Forward): { tone: 'good' | 'warn' | 'bad'; text: string } {
  const where = f.hostName ? `${f.hostName}:${f.targetPort}` : `port ${f.targetPort}`
  switch (f.state) {
    case 'listening':
      if (f.detail) return { tone: 'warn', text: `Relaying to ${where}, but the last connection failed: ${f.detail}` }
      return {
        tone: 'good',
        text: `Relaying to ${where}` + (f.active > 0 ? ` · ${f.active} open connection${f.active === 1 ? '' : 's'}` : ''),
      }
    case 'waiting':
      return { tone: 'warn', text: `Waiting: ${f.detail || 'nothing to relay to yet'}` }
    default:
      return { tone: 'bad', text: f.detail || 'Not running' }
  }
}
