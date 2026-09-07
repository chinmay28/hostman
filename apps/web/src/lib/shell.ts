import { api } from '../api'
import type { ShellSession } from '../types'

/**
 * Talking to a shell that lives on the server.
 *
 * The screen goes one way and the keyboard goes the other, over two different
 * things, and that is deliberate. Output arrives on an EventSource, which the
 * browser reopens by itself and which HostMan can be asked to resume from a
 * byte offset — so a phone that locked its screen for ten minutes reconnects
 * and gets exactly what it missed. Keystrokes go as ordinary POSTs, which need
 * no connection to have stayed up at all.
 */

export type ConnectionState = 'connecting' | 'live' | 'reconnecting' | 'ended'

export interface StreamHandlers {
  /** The session as HostMan sees it, sent first on every connect — so a screen
   *  reconnecting into a shell somebody has since resized can correct itself. */
  onSession: (session: ShellSession) => void
  onData: (bytes: Uint8Array) => void
  /** Output was produced while nobody was watching and has fallen off the end
   *  of what HostMan keeps. Said out loud rather than papered over: the screen
   *  is about to jump, and a terminal that jumps silently is a terminal lying
   *  about what ran. */
  onGap: (missed: number) => void
  onState: (state: ConnectionState) => void
  onExit: (reason: string) => void
}

/** How long to wait before reopening a dropped stream, growing to a ceiling.
 *  The first retry is quick because the common cause is a phone waking up. */
const RETRY_MS = [500, 1000, 2000, 4000, 8000]

export class ShellStream {
  private source: EventSource | null = null
  private timer: number | null = null
  private attempt = 0
  private stopped = false
  /** Where the screen has been read up to. The whole of the recovery. */
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

  /** Reopen now rather than on the backoff. Called when the tab comes back to
   *  the front: iOS will have killed the connection while it was away, and the
   *  browser often has not noticed yet. */
  refresh(): void {
    if (this.stopped) return
    this.attempt = 0
    this.open()
  }

  private open(): void {
    this.clearTimer()
    this.source?.close()
    this.on.onState(this.attempt === 0 ? 'connecting' : 'reconnecting')

    const source = new EventSource(`/api/shell/${this.id}/stream?from=${this.offset}`)
    this.source = source

    source.addEventListener('session', (e) => {
      this.attempt = 0
      this.on.onState('live')
      this.on.onSession(JSON.parse((e as MessageEvent).data) as ShellSession)
    })

    source.addEventListener('out', (e) => {
      const chunk = JSON.parse((e as MessageEvent).data) as {
        from: number
        next: number
        data: string
      }
      if (chunk.from > this.offset) this.on.onGap(chunk.from - this.offset)
      this.offset = chunk.next
      this.on.onData(decode(chunk.data))
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
      // EventSource reconnects on its own, but it would reconnect to the URL it
      // was given — with the offset this screen had when it started. Reopening
      // by hand is what lets it ask for the right place.
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

/**
 * Sender carries keystrokes to the shell in order.
 *
 * One request is in flight at a time and anything typed meanwhile is held for
 * the next one. That is not an optimisation: two POSTs racing would reach the
 * host in whichever order the network felt like, and a terminal that reorders
 * keystrokes is worse than one that is slow. Holding them also means a burst —
 * a paste, or a fast typist on a bad connection — becomes one request rather
 * than forty.
 */
export class Sender {
  private pending: number[] = []
  private busy = false

  constructor(
    private readonly id: string,
    private readonly onError: (message: string) => void,
  ) {}

  send(bytes: Uint8Array | number[]): void {
    for (const b of bytes) this.pending.push(b)
    void this.flush()
  }

  /** type is send for text, which is most of what a keyboard produces. */
  type(text: string): void {
    this.send(new TextEncoder().encode(text))
  }

  private async flush(): Promise<void> {
    if (this.busy || this.pending.length === 0) return
    this.busy = true
    try {
      while (this.pending.length > 0) {
        const batch = this.pending
        this.pending = []
        await api.shellInput(this.id, encode(batch))
      }
    } catch (e) {
      this.onError(e instanceof Error ? e.message : String(e))
    } finally {
      this.busy = false
    }
  }
}

/** Base64 to bytes. A pty deals in bytes, and half a character can legitimately
 *  end a chunk — so nothing here decodes text; xterm is what puts them back
 *  together across writes. */
function decode(data: string): Uint8Array {
  const binary = atob(data)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes
}

function encode(bytes: number[]): string {
  let binary = ''
  for (const b of bytes) binary += String.fromCharCode(b)
  return btoa(binary)
}
