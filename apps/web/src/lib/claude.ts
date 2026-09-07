import type { ClaudeEntry, ClaudeSession } from '../types'

/**
 * Following a conversation that lives on the server.
 *
 * Events arrive on an EventSource that HostMan can be asked to resume from an
 * offset, so a phone that locked its screen for ten minutes reconnects and
 * gets exactly what it missed. Messages and answers go the other way as
 * ordinary POSTs, which need no connection to have stayed up.
 */

export type ConnectionState = 'connecting' | 'live' | 'reconnecting' | 'ended'

export interface StreamHandlers {
  /** The session as HostMan sees it: first on every connect, and again after
   *  every batch of entries, so busy, pending and cost never lag the screen. */
  onSession: (session: ClaudeSession) => void
  onEntries: (entries: ClaudeEntry[]) => void
  /** Events happened while nobody was watching and have fallen off the end of
   *  what HostMan keeps. */
  onGap: (missed: number) => void
  onState: (state: ConnectionState) => void
  onExit: (reason: string) => void
}

const RETRY_MS = [500, 1000, 2000, 4000, 8000]

export class ClaudeStream {
  private source: EventSource | null = null
  private timer: number | null = null
  private attempt = 0
  private stopped = false
  private offset: number

  constructor(
    private readonly id: string,
    from: number,
    private readonly on: StreamHandlers,
  ) {
    this.offset = from
  }

  start(): void {
    this.stopped = false
    this.open()
  }

  stop(): void {
    this.stopped = true
    this.clearTimer()
    this.source?.close()
    this.source = null
  }

  /** Reopen now rather than on the backoff, for a tab coming back to the front. */
  refresh(): void {
    if (this.stopped) return
    this.attempt = 0
    this.open()
  }

  private open(): void {
    this.clearTimer()
    this.source?.close()
    this.on.onState(this.attempt === 0 ? 'connecting' : 'reconnecting')

    const source = new EventSource(`/api/claude/${this.id}/stream?from=${this.offset}`)
    this.source = source

    source.addEventListener('session', (e) => {
      this.attempt = 0
      this.on.onState('live')
      this.on.onSession(JSON.parse((e as MessageEvent).data) as ClaudeSession)
    })

    source.addEventListener('entries', (e) => {
      const chunk = JSON.parse((e as MessageEvent).data) as {
        from: number
        next: number
        entries: ClaudeEntry[]
      }
      if (chunk.from > this.offset) this.on.onGap(chunk.from - this.offset)
      this.offset = chunk.next
      this.on.onEntries(chunk.entries)
    })

    source.addEventListener('exit', (e) => {
      const { exit } = JSON.parse((e as MessageEvent).data) as { exit: string }
      this.stop()
      this.on.onState('ended')
      this.on.onExit(exit)
    })

    source.onerror = () => {
      if (this.stopped) return
      source.close()
      this.on.onState('reconnecting')
      const wait = RETRY_MS[Math.min(this.attempt, RETRY_MS.length - 1)]
      this.attempt++
      this.timer = window.setTimeout(() => this.open(), wait)
    }
  }

  private clearTimer(): void {
    if (this.timer !== null) {
      window.clearTimeout(this.timer)
      this.timer = null
    }
  }
}

/** The models worth a tap, by the alias the CLI takes. The name is what the
 *  screen shows; the ids are what the CLI reports back once it has resolved
 *  the alias, so a session can be matched to its row. */
export const MODELS: { alias: string; name: string; about: string; ids: string[] }[] = [
  { alias: 'fable', name: 'Fable 5.1', about: 'Long, hard jobs. Slowest and most expensive.', ids: ['claude-fable-5-1', 'claude-fable-5'] },
  { alias: 'opus', name: 'Opus 5', about: 'Complex reasoning across a lot of files.', ids: ['claude-opus-5'] },
  { alias: 'sonnet', name: 'Sonnet 5', about: 'Everyday work. Quick and capable.', ids: ['claude-sonnet-5'] },
  { alias: 'haiku', name: 'Haiku 4.5', about: 'Small questions, fast and cheap.', ids: ['claude-haiku-4-5', 'claude-haiku-4-5-20251001'] },
  { alias: 'opusplan', name: 'Opus plans, Sonnet does', about: 'Opus while planning, Sonnet once it starts changing things.', ids: [] },
]

/** What to call a model the CLI reported: the row's name where it matches one,
 *  the id as it came otherwise. A "[1m]" suffix is the long context window. */
export function modelName(model: string): string {
  if (!model) return 'Default model'
  const long = model.endsWith('[1m]')
  const base = long ? model.slice(0, -4) : model
  const row = MODELS.find((m) => m.alias === base || m.ids.includes(base))
  const name = row ? row.name : base
  return long ? `${name} · 1M` : name
}

/** The permission modes, in the words a screen uses. */
export const MODES: { mode: string; name: string; short: string; about: string }[] = [
  { mode: 'default', name: 'Ask first', short: 'Asks first', about: 'Claude asks in the chat before running a command or changing a file.' },
  { mode: 'acceptEdits', name: 'Accept edits', short: 'Edits freely', about: 'File edits go ahead; commands still ask.' },
  { mode: 'plan', name: 'Plan only', short: 'Planning', about: 'Reads and thinks, changes nothing until you switch.' },
  { mode: 'bypassPermissions', name: 'Skip all', short: 'Skips permissions', about: 'Runs whatever it decides to, without asking. For a machine you can afford to rebuild.' },
]

export function modeName(mode: string): string {
  return MODES.find((m) => m.mode === mode)?.short ?? mode
}

/** Dollars, to the cent, or to the tenth of a cent under a dime. */
export function dollars(cost: number): string {
  if (cost >= 0.1) return `$${cost.toFixed(2)}`
  return `$${cost.toFixed(3)}`
}

/** Tokens in thousands: 41k, 1.2M. */
export function tokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}k`
  return String(n)
}
