import { useCallback, useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { api } from '../api'
import { toClipboard } from '../lib/clipboard'
import { Sender, ShellStream, type ConnectionState } from '../lib/shell'
import type { ShellSession } from '../types'
import { Sheet } from './ui'

/**
 * A terminal on a phone.
 *
 * xterm.js draws the screen — a terminal is a thirty-year-old protocol of
 * escape sequences and there is no point pretending otherwise, because `htop`
 * and `vim` and a coloured prompt are exactly what a shell is for. What is
 * HostMan's own work is everything around it, and all of that is about the
 * phone:
 *
 * The key bar. A phone keyboard has no Ctrl, no Esc, no Tab and no arrows,
 * which between them are most of what a shell is driven by. Ctrl is a sticky
 * modifier rather than a chord, because there is no chording on a touchscreen.
 * The bar has two shapes: with the soft keyboard up every row is terminal
 * lost, so it is one thin row; with the keyboard away the bottom of the phone
 * is free, and the same keys spread into a keypad — arrows and Enter under
 * the right thumb, the rest in keys big enough to hit without looking.
 *
 * The size. The soft keyboard covers half the screen, so the terminal is sized
 * to the visual viewport rather than the window, and the host is told the new
 * window each time — which is what makes a full-screen program redraw into the
 * space that is actually visible instead of behind the keyboard.
 *
 * Copy and paste. Both are keys on the bar, because neither is a gesture a
 * phone offers over a terminal: xterm selects with a mouse and a phone has
 * none, so Copy arms a mode in which a drag picks lines instead of scrolling.
 * And because HostMan is usually plain http on a LAN, where a browser hands a
 * script no clipboard at all, both fall back to a box of text the phone's own
 * Copy and Paste can reach. When the phone does offer Paste over the screen —
 * a long press while the keyboard is up — that lands in the shell too; the
 * keys are the way that always exists.
 */

/** Colours xterm needs as concrete values; it cannot read a CSS variable. Kept
 *  beside the two palettes in styles.css and picked the same way. */
const THEMES = {
  light: {
    background: '#ffffff',
    foreground: '#16161a',
    cursor: '#5b5bd6',
    cursorAccent: '#ffffff',
    selectionBackground: '#c8c8f5',
    black: '#16161a',
    red: '#c0362c',
    green: '#17864b',
    yellow: '#9a6100',
    blue: '#3a3ab8',
    magenta: '#8b3fa8',
    cyan: '#0f7490',
    white: '#d8d8e0',
    brightBlack: '#6b6b76',
    brightRed: '#e0503f',
    brightGreen: '#1fa860',
    brightYellow: '#b87c00',
    brightBlue: '#5b5bd6',
    brightMagenta: '#a855c8',
    brightCyan: '#1291b4',
    brightWhite: '#16161a',
  },
  dark: {
    background: '#0b0b0f',
    foreground: '#f2f2f5',
    cursor: '#8b8bf5',
    cursorAccent: '#0b0b0f',
    selectionBackground: '#33335a',
    black: '#2a2a34',
    red: '#f87171',
    green: '#4ade80',
    yellow: '#fbbf24',
    blue: '#8b8bf5',
    magenta: '#d8a0f0',
    cyan: '#5ed8f0',
    white: '#d8d8e0',
    brightBlack: '#9a9aa8',
    brightRed: '#fca5a5',
    brightGreen: '#86efac',
    brightYellow: '#fcd34d',
    brightBlue: '#a5a5f8',
    brightMagenta: '#e8bff8',
    brightCyan: '#a0e8f8',
    brightWhite: '#ffffff',
  },
} as const

/** Font sizes the size buttons step through. The smallest fits 80 columns on a
 *  phone held sideways, which is the width most command output assumes. */
const SIZES = [8, 9, 10, 11, 12, 13, 14, 16, 18]
const SIZE_KEY = 'deployer.terminal.fontSize'
const EXTRAS_KEY = 'deployer.terminal.extraKeys'

/** How close to the top or bottom edge a finger has to get, while picking
 *  text, before the screen starts scrolling under it. */
const EDGE = 28

/** The keys a phone keyboard does not have, in the order a shell needs them. */
const KEYS: { label: string; bytes: number[]; title: string }[] = [
  { label: 'Esc', bytes: [0x1b], title: 'Escape' },
  { label: 'Tab', bytes: [0x09], title: 'Tab — complete' },
  { label: '^C', bytes: [0x03], title: 'Ctrl-C — stop what is running' },
  { label: '←', bytes: [0x1b, 0x5b, 0x44], title: 'Left' },
  { label: '↑', bytes: [0x1b, 0x5b, 0x41], title: 'Up — previous command' },
  { label: '↓', bytes: [0x1b, 0x5b, 0x42], title: 'Down' },
  { label: '→', bytes: [0x1b, 0x5b, 0x43], title: 'Right' },
]

/** The second row: what a shell is full of and a phone keyboard buries two
 *  layers down, then the keys for moving around a long line. */
const EXTRAS: { label: string; bytes: number[]; title: string }[] = [
  ...['|', '/', '\\', '~', '-', '_', '*', '$', '&', '#', '"', "'", '`', '{', '}'].map((ch) => ({
    label: ch,
    bytes: [ch.charCodeAt(0)],
    title: ch,
  })),
  { label: 'Home', bytes: [0x1b, 0x5b, 0x48], title: 'Start of the line' },
  { label: 'End', bytes: [0x1b, 0x5b, 0x46], title: 'End of the line' },
  { label: 'PgUp', bytes: [0x1b, 0x5b, 0x35, 0x7e], title: 'Page up' },
  { label: 'PgDn', bytes: [0x1b, 0x5b, 0x36, 0x7e], title: 'Page down' },
  { label: '^D', bytes: [0x04], title: 'Ctrl-D — end of input' },
  { label: '^Z', bytes: [0x1a], title: 'Ctrl-Z — suspend' },
  { label: '^L', bytes: [0x0c], title: 'Ctrl-L — clear the screen' },
]

export default function TerminalView({
  session,
  onSession,
  onExit,
  onError,
}: {
  session: ShellSession
  onSession: (s: ShellSession) => void
  onExit: (reason: string) => void
  onError: (message: string) => void
}) {
  const stageRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const senderRef = useRef<Sender | null>(null)
  /** Armed modifiers. A ref as well as state because the handler that consumes
   *  them is installed once and must see the current value, not the one that
   *  was current when xterm was built. */
  const modsRef = useRef({ ctrl: false, alt: false })
  const [mods, setMods] = useState({ ctrl: false, alt: false })
  const [state, setState] = useState<ConnectionState>('connecting')
  const [extras, setExtras] = useState(() => window.localStorage.getItem(EXTRAS_KEY) === 'on')
  /** Whether the soft keyboard is up — what decides the shape of the keys
   *  below. Nothing announces the keyboard directly; how much of the window
   *  the visual viewport is not is the announcement. */
  const [kbOpen, setKbOpen] = useState(false)
  /** A device driven by a mouse has every key already and wants the thin row
   *  whatever the viewport does; the keypad is for fingers. */
  const [coarse] = useState(() => window.matchMedia('(pointer: coarse)').matches)
  const [fontSize, setFontSize] = useState(readFontSize)
  /** What the terminal actually measured. Shown beside the size buttons,
   *  because "how many columns is this" is the only thing they are for. */
  const [cols, setCols] = useState(session.cols)
  /** Whether a drag over the screen picks lines rather than scrolling. A ref
   *  as well, for the same reason as the modifiers: the touch handlers read it
   *  from outside React. */
  const selectingRef = useRef(false)
  const [selecting, setSelecting] = useState(false)
  /** Whether there is anything to copy — what turns the Copy key from arming
   *  the mode into doing the thing. */
  const [picked, setPicked] = useState(false)
  /** A line between the screen and the keys: what the armed mode wants, or
   *  what just happened. */
  const [note, setNote] = useState<string | null>(null)
  const noteTimer = useRef(0)
  /** The way out when the browser will not give a script the clipboard: a box
   *  of text the phone's own Copy and Paste can reach. */
  const [sheet, setSheet] = useState<{ mode: 'copy' | 'paste'; text: string } | null>(null)

  const say = useCallback((message: string | null, hold = 0) => {
    window.clearTimeout(noteTimer.current)
    setNote(message)
    // Held messages are the ones about something that has already happened, so
    // they go away by themselves; a mode's own line stays while the mode does.
    if (message && hold) noteTimer.current = window.setTimeout(() => setNote(null), hold)
  }, [])

  const arm = useCallback((key: 'ctrl' | 'alt', on: boolean) => {
    modsRef.current = { ...modsRef.current, [key]: on }
    setMods({ ...modsRef.current })
  }, [])

  const send = useCallback((bytes: number[]) => {
    // An armed modifier applies to a key from the bar the same as to a typed
    // one — on the keypad, with no keyboard up, the bar is the only place a
    // key can come from at all. Single characters only, the same rule the
    // typed path applies.
    const { ctrl, alt } = modsRef.current
    let out = bytes
    if (bytes.length === 1) {
      if (ctrl) {
        const code = controlCode(String.fromCharCode(bytes[0]))
        if (code !== null) out = [code]
      }
      if (alt) out = [0x1b, ...out]
      if (ctrl || alt) {
        modsRef.current = { ctrl: false, alt: false }
        setMods({ ctrl: false, alt: false })
      }
    }
    senderRef.current?.send(out)
    // Focus follows a key only where it already was: tapping ↑ on the keypad
    // must not raise the keyboard the keypad is standing in for.
    if (stageRef.current?.contains(document.activeElement)) termRef.current?.focus()
  }, [])

  // Built once for the life of the session. Rebuilding it would throw the
  // screen away, and the screen is the thing being kept.
  useEffect(() => {
    const stage = stageRef.current
    if (!stage) return

    const dark = window.matchMedia('(prefers-color-scheme: dark)')
    const term = new Terminal({
      allowTransparency: false,
      cursorBlink: true,
      // A phone cannot show much at once, so the history is where anything
      // scrolled past has to be findable.
      scrollback: 5000,
      fontSize: readFontSize(),
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      lineHeight: 1.1,
      theme: dark.matches ? THEMES.dark : THEMES.light,
      // A tap on a link on a phone is a tap on a character next to it, so
      // nothing here is made clickable.
      convertEol: false,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(stage)
    termRef.current = term
    fitRef.current = fit

    const sender = new Sender(session.id, onError)
    senderRef.current = sender

    // Everything typed, from the soft keyboard or a bluetooth one, arrives
    // here already encoded — so an armed Ctrl is applied to it rather than
    // being a key xterm has to know about.
    const typed = term.onData((data) => {
      const { ctrl, alt } = modsRef.current
      let bytes = Array.from(new TextEncoder().encode(data))
      // A key is one character, or the escape sequence a bluetooth arrow sends.
      // Everything else arrives here whole — a paste, dictation, a word an IME
      // has just committed — and an armed modifier has no business being
      // applied to a block of text, least of all to the brackets around a
      // paste.
      const key = data.length === 1 || (data.startsWith('\x1b') && !data.startsWith('\x1b[200~'))
      if (key) {
        if (ctrl && data.length === 1) {
          const code = controlCode(data)
          if (code !== null) bytes = [code]
        }
        if (alt) bytes = [0x1b, ...bytes]
        if (ctrl || alt) {
          modsRef.current = { ctrl: false, alt: false }
          setMods({ ctrl: false, alt: false })
        }
      }
      sender.send(bytes)
    })
    // Bytes rather than text: mouse reports and the like, which a program that
    // asked for them expects back exactly as they were.
    const binary = term.onBinary((data) => {
      sender.send(Array.from(data, (ch) => ch.charCodeAt(0) & 0xff))
    })

    // The phone's own Paste, long-pressed out of the callout over the screen.
    // xterm forwards a `paste` event itself, but iOS often announces this
    // paste only as an input event on the hidden textarea, of a kind xterm
    // does not read — so the text landed in a box nobody looks at and the
    // shell saw nothing. Both announcements are caught here in the capture
    // phase and walked through the same door as the Paste key, and stopping
    // them there is what keeps the two paths from both delivering.
    const nativePaste = (text: string | null | undefined): boolean => {
      if (!text) return false
      // Modifiers are for keys, and a paste is not a keystroke.
      modsRef.current = { ctrl: false, alt: false }
      setMods({ ctrl: false, alt: false })
      term.paste(text)
      return true
    }
    const onPaste = (e: ClipboardEvent) => {
      if (nativePaste(e.clipboardData?.getData('text/plain'))) {
        e.preventDefault()
        e.stopImmediatePropagation()
      }
    }
    const onBeforeInput = (e: Event) => {
      const ev = e as InputEvent
      if (ev.inputType !== 'insertFromPaste') return
      if (nativePaste(ev.data ?? ev.dataTransfer?.getData('text/plain'))) {
        ev.preventDefault()
        ev.stopImmediatePropagation()
      }
    }
    stage.addEventListener('paste', onPaste, true)
    stage.addEventListener('beforeinput', onBeforeInput, true)

    // Whether there is anything to copy is xterm's to know: a drag here, a
    // mouse on a desktop, or a selection dropped by what the shell printed
    // next all arrive the same way.
    const chosen = term.onSelectionChange(() => setPicked(term.hasSelection()))

    const onTheme = (e: MediaQueryListEvent) => {
      term.options.theme = e.matches ? THEMES.dark : THEMES.light
    }
    dark.addEventListener('change', onTheme)

    // The screen is asked for from the beginning: what HostMan kept is the
    // shell's history, and a client arriving at an hour-old session should see
    // what it has been doing rather than an empty rectangle.
    const stream = new ShellStream(session.id, 0, {
      onSession: (s) => {
        onSession(s)
        // Push this screen's size after every connect: a shell that was resized
        // by another screen, or by this one before it dropped, is otherwise
        // drawing into a window that is not the one here.
        pushSize(term.cols, term.rows)
      },
      onData: (bytes) => term.write(bytes),
      onGap: (missed) => {
        term.write(
          `\r\n\x1b[33m— ${missed.toLocaleString()} bytes scrolled past while this screen was away —\x1b[0m\r\n`,
        )
      },
      onState: setState,
      onExit,
    })
    stream.start()

    // iOS drops connections when the tab goes to the background and does not
    // always admit it, so coming back to the front is a cue to reopen rather
    // than to wait out a backoff on a connection that is already dead.
    const onVisible = () => {
      if (document.visibilityState !== 'visible') return
      stream.refresh()
      fitLater()
    }
    document.addEventListener('visibilitychange', onVisible)

    let sizeTimer = 0
    let lastSize = ''
    const pushSize = (cols: number, rows: number) => {
      const key = `${cols}x${rows}`
      if (key === lastSize) return
      lastSize = key
      window.clearTimeout(sizeTimer)
      // Debounced because turning a phone, or the keyboard sliding up, is a
      // stream of sizes and only the last one is the answer.
      sizeTimer = window.setTimeout(() => {
        api.shellResize(session.id, cols, rows).catch(() => {
          // A shell that has gone is reported by the stream, which is the one
          // place that should say so.
        })
      }, 150)
    }
    const resized = term.onResize(({ cols, rows }) => {
      setCols(cols)
      pushSize(cols, rows)
    })

    let fitTimer = 0
    const fitLater = () => {
      window.clearTimeout(fitTimer)
      fitTimer = window.setTimeout(() => {
        try {
          fit.fit()
        } catch {
          // fit throws while the stage has no size — mid-transition, or on a
          // screen being torn down. The next one will land.
        }
      }, 60)
    }
    const observer = new ResizeObserver(fitLater)
    observer.observe(stage)
    fitLater()

    return () => {
      stage.removeEventListener('paste', onPaste, true)
      stage.removeEventListener('beforeinput', onBeforeInput, true)
      document.removeEventListener('visibilitychange', onVisible)
      dark.removeEventListener('change', onTheme)
      window.clearTimeout(fitTimer)
      window.clearTimeout(sizeTimer)
      window.clearTimeout(noteTimer.current)
      observer.disconnect()
      resized.dispose()
      chosen.dispose()
      typed.dispose()
      binary.dispose()
      stream.stop()
      term.dispose()
      termRef.current = null
      fitRef.current = null
      senderRef.current = null
    }
    // Built for one session and torn down with it; the callbacks are held in
    // refs by the closures above rather than rebuilding the terminal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session.id])

  /**
   * Picking text with a finger.
   *
   * xterm selects with a mouse, and a phone has none: a drag over the screen
   * means scroll, and nothing a finger does says "select this". So picking is
   * a mode, armed from the Copy key, and while it is armed the touches belong
   * here rather than to xterm — taken in the capture phase, before xterm's own
   * handlers see them, which is the same thing that stops the screen scrolling
   * out from under the finger.
   *
   * A line at a time, because that is the precision a finger has and because a
   * command and what it printed are lines anyway.
   */
  useEffect(() => {
    selectingRef.current = selecting
    const term = termRef.current
    const stage = stageRef.current
    if (!selecting || !term || !stage) return

    stage.classList.add('selecting')

    /** The buffer line under a point on the screen. Absolute, counting the
     *  scrollback, so it stays the same line while the view scrolls. */
    const lineAt = (clientY: number): number => {
      const screen = stage.querySelector('.xterm-screen') ?? stage
      const box = screen.getBoundingClientRect()
      const row = Math.floor((clientY - box.top) / (box.height / term.rows))
      return term.buffer.active.viewportY + Math.min(term.rows - 1, Math.max(0, row))
    }

    /** Where the drag started, in buffer lines, and where the finger is, in
     *  pixels — the second is what the edge scroll re-reads as the view moves
     *  under a finger that is holding still. */
    let anchor = 0
    let at = 0
    let edge = 0
    let edgeTimer = 0

    // Which lines are picked is xterm's business; that something is picked
    // reaches the Copy key through the subscription above.
    const extend = (line: number) => {
      term.selectLines(Math.min(anchor, line), Math.max(anchor, line))
    }

    /** Dragging to the top or bottom edge keeps going, because what is worth
     *  copying is usually taller than a phone. */
    const follow = (clientY: number) => {
      const box = stage.getBoundingClientRect()
      const dir = clientY < box.top + EDGE ? -1 : clientY > box.bottom - EDGE ? 1 : 0
      if (dir === edge) return
      edge = dir
      window.clearInterval(edgeTimer)
      if (dir === 0) return
      edgeTimer = window.setInterval(() => {
        term.scrollLines(dir)
        extend(lineAt(at))
      }, 90)
    }

    const begin = (e: TouchEvent) => {
      const touch = e.touches[0]
      if (!touch) return
      e.stopPropagation()
      e.preventDefault()
      at = touch.clientY
      anchor = lineAt(at)
      extend(anchor)
      say('Drag to take more lines, then tap Copy')
    }
    const drag = (e: TouchEvent) => {
      const touch = e.touches[0]
      if (!touch) return
      e.stopPropagation()
      e.preventDefault()
      at = touch.clientY
      extend(lineAt(at))
      follow(at)
    }
    const release = (e: TouchEvent) => {
      e.stopPropagation()
      edge = 0
      window.clearInterval(edgeTimer)
    }

    // Capture, so xterm never sees them: its own touch handling is the scroll
    // this mode exists to replace, and it cannot be turned off.
    const how = { capture: true, passive: false } as const
    stage.addEventListener('touchstart', begin, how)
    stage.addEventListener('touchmove', drag, how)
    stage.addEventListener('touchend', release, how)
    stage.addEventListener('touchcancel', release, how)

    return () => {
      window.clearInterval(edgeTimer)
      stage.classList.remove('selecting')
      stage.removeEventListener('touchstart', begin, how)
      stage.removeEventListener('touchmove', drag, how)
      stage.removeEventListener('touchend', release, how)
      stage.removeEventListener('touchcancel', release, how)
    }
  }, [selecting, say])

  // The soft keyboard covers the bottom half of the screen. Sizing to the
  // visual viewport is what keeps the prompt above it instead of behind it.
  useEffect(() => {
    const vv = window.visualViewport
    if (!vv) return
    const root = document.querySelector<HTMLElement>('.term-screen')
    if (!root) return
    const apply = () => {
      root.style.height = `${vv.height}px`
      root.style.transform = `translateY(${vv.offsetTop}px)`
      // Anything that takes a third of the window is the keyboard; browser
      // chrome coming and going is far smaller.
      setKbOpen(window.innerHeight - vv.height > 140)
    }
    apply()
    vv.addEventListener('resize', apply)
    vv.addEventListener('scroll', apply)
    return () => {
      vv.removeEventListener('resize', apply)
      vv.removeEventListener('scroll', apply)
      root.style.height = ''
      root.style.transform = ''
    }
  }, [])

  useEffect(() => {
    const term = termRef.current
    if (!term) return
    term.options.fontSize = fontSize
    window.localStorage.setItem(SIZE_KEY, String(fontSize))
    try {
      fitRef.current?.fit()
    } catch {
      // Same as above: a stage with no size yet.
    }
  }, [fontSize])

  const showExtras = (on: boolean) => {
    setExtras(on)
    window.localStorage.setItem(EXTRAS_KEY, on ? 'on' : 'off')
  }

  const step = (by: number) => {
    const at = SIZES.indexOf(fontSize)
    const next = SIZES[Math.min(SIZES.length - 1, Math.max(0, (at < 0 ? 3 : at) + by))]
    setFontSize(next)
  }

  /** Text arriving whole, from the clipboard or from the box below. */
  const insert = (text: string) => {
    const term = termRef.current
    if (!term) return
    // Modifiers are for keys. A block of text is not a keystroke.
    modsRef.current = { ctrl: false, alt: false }
    setMods({ ctrl: false, alt: false })
    // xterm's own paste rather than the raw bytes: it turns newlines into
    // carriage returns, and wraps the block in the brackets a shell asks for
    // when it wants to know a paste is a paste — which is what stops a pasted
    // script running itself a line at a time before it has all arrived.
    term.paste(text)
    term.focus()
  }

  const paste = async () => {
    try {
      // Optional because over plain http there is no clipboard object at all,
      // and HostMan is usually plain http on a LAN.
      const text = await navigator.clipboard?.readText()
      if (text) {
        insert(text)
        return
      }
      if (text === '') {
        say('The clipboard is empty', 1600)
        return
      }
    } catch {
      // Refused, or asked for from a page the browser does not trust with it.
    }
    setSheet({ mode: 'paste', text: '' })
  }

  /** Out of the mode with nothing copied — the screen goes back to scrolling
   *  and typing, which is what it is for the rest of the time. */
  const stopPicking = () => {
    termRef.current?.clearSelection()
    setSelecting(false)
    say(null)
  }

  /** Copy is two things, because the first has to happen before the second can:
   *  with nothing picked it arms the mode that picks, and with something picked
   *  it copies that and puts the mode away. */
  const copy = async () => {
    const term = termRef.current
    if (!term) return
    if (!term.hasSelection()) {
      const on = !selectingRef.current
      setSelecting(on)
      say(on ? 'Drag over the screen to pick lines, then tap Copy' : null)
      return
    }
    const text = term.getSelection()
    term.clearSelection()
    setSelecting(false)
    // A pick over blank lines would "copy" nothing and say it had.
    if (!text.trim()) {
      say('Nothing on those lines to copy', 1600)
      return
    }
    if (await toClipboard(text)) {
      say(`Copied ${count(text)}`, 1600)
      term.focus()
      return
    }
    setSheet({ mode: 'copy', text })
  }

  return (
    <>
      {/* Focusing raises the keyboard, which is the last thing wanted while
          text is being picked out from under it. */}
      <div
        className="term-stage"
        ref={stageRef}
        onClick={() => !selectingRef.current && termRef.current?.focus()}
      />

      {state !== 'live' && (
        <div className={`term-state ${state}`} role="status">
          {state === 'connecting' && 'Connecting…'}
          {state === 'reconnecting' && 'Reconnecting — the shell is still open on the host'}
          {state === 'ended' && 'The shell has ended'}
        </div>
      )}

      <div className="term-keys">
        {/* Over the foot of the screen rather than above it: a line that took
            room would resize the terminal, and a terminal that reflows every
            time a mode is armed is a terminal that moved what was being read.
            The way out of the mode lives here too, because a key that comes
            and goes would move the ones beside it. */}
        {(note || selecting) && (
          <div className="term-note">
            <span>{note}</span>
            {selecting && (
              <button type="button" className="term-drop" onClick={stopPicking}>
                Cancel
              </button>
            )}
          </div>
        )}
        {extras && (
          <div className="term-row">
            {/* Text size leads the row. It is the control this screen needs most
                and the one no desktop terminal has: how many columns fit is the
                difference between reading `ls -l` and unpicking it. */}
            <KeyButton title="Smaller text — more columns" onPress={() => step(-1)}>
              A−
            </KeyButton>
            <span className="term-cols" aria-label={`${cols} columns`}>
              {cols}c
            </span>
            <KeyButton title="Bigger text — fewer columns" onPress={() => step(1)}>
              A+
            </KeyButton>
            <KeyButton
              title="Alt — then the next key"
              held={mods.alt}
              onPress={() => arm('alt', !modsRef.current.alt)}
            >
              Alt
            </KeyButton>
            {EXTRAS.map((key) => (
              <KeyButton key={key.label} title={key.title} onPress={() => send(key.bytes)}>
                {key.label}
              </KeyButton>
            ))}
          </div>
        )}

        {kbOpen || !coarse ? (
          <div className="term-row">
            {KEYS.map((key) => (
              <KeyButton key={key.label} title={key.title} onPress={() => send(key.bytes)}>
                {key.label}
              </KeyButton>
            ))}
            <KeyButton
              title="Ctrl — then the next key"
              held={mods.ctrl}
              onPress={() => arm('ctrl', !modsRef.current.ctrl)}
            >
              Ctrl
            </KeyButton>
            <KeyButton
              title={picked ? 'Copy what is picked' : 'Pick lines to copy'}
              held={selecting || picked}
              onPress={copy}
            >
              Copy
            </KeyButton>
            <KeyButton title="Paste into the shell" onPress={paste}>
              Paste
            </KeyButton>
            <KeyButton title="More keys" held={extras} onPress={() => showExtras(!extras)}>
              •••
            </KeyButton>
          </div>
        ) : (
          /* The keypad, standing where the keyboard would: three rows of four
             on the left, and the cluster a shell leans on hardest — arrows
             around Enter — under the right thumb. Tapping the screen raises
             the keyboard and folds this back into the row above. */
          <div className="term-pad">
            <div className="term-pad-main">
              <KeyButton title="Escape" onPress={() => send([0x1b])}>
                Esc
              </KeyButton>
              <KeyButton title="Tab — complete" onPress={() => send([0x09])}>
                Tab
              </KeyButton>
              <KeyButton
                title="Ctrl — then the next key"
                held={mods.ctrl}
                onPress={() => arm('ctrl', !modsRef.current.ctrl)}
              >
                Ctrl
              </KeyButton>
              <KeyButton title="Ctrl-C — stop what is running" onPress={() => send([0x03])}>
                ^C
              </KeyButton>
              <KeyButton
                title="Alt — then the next key"
                held={mods.alt}
                onPress={() => arm('alt', !modsRef.current.alt)}
              >
                Alt
              </KeyButton>
              <KeyButton title="Slash" onPress={() => send([0x2f])}>
                /
              </KeyButton>
              <KeyButton title="Dash" onPress={() => send([0x2d])}>
                -
              </KeyButton>
              <KeyButton
                title={picked ? 'Copy what is picked' : 'Pick lines to copy'}
                held={selecting || picked}
                onPress={copy}
              >
                Copy
              </KeyButton>
              <KeyButton title="More keys" held={extras} onPress={() => showExtras(!extras)}>
                •••
              </KeyButton>
              <KeyButton title="Start of the line" onPress={() => send([0x1b, 0x5b, 0x48])}>
                Home
              </KeyButton>
              <KeyButton title="End of the line" onPress={() => send([0x1b, 0x5b, 0x46])}>
                End
              </KeyButton>
              <KeyButton title="Paste into the shell" onPress={paste}>
                Paste
              </KeyButton>
            </div>
            <div className="term-pad-nav">
              <KeyButton className="up" title="Up — previous command" onPress={() => send([0x1b, 0x5b, 0x41])}>
                ↑
              </KeyButton>
              <KeyButton className="left" title="Left" onPress={() => send([0x1b, 0x5b, 0x44])}>
                ←
              </KeyButton>
              <KeyButton className="enter" title="Enter — run it" onPress={() => send([0x0d])}>
                Enter
              </KeyButton>
              <KeyButton className="right" title="Right" onPress={() => send([0x1b, 0x5b, 0x43])}>
                →
              </KeyButton>
              <KeyButton className="down" title="Down" onPress={() => send([0x1b, 0x5b, 0x42])}>
                ↓
              </KeyButton>
            </div>
          </div>
        )}
      </div>

      {sheet && (
        <ClipboardSheet
          mode={sheet.mode}
          text={sheet.text}
          onSend={(text) => {
            setSheet(null)
            if (text) insert(text)
          }}
          onClose={() => setSheet(null)}
        />
      )}
    </>
  )
}

/**
 * The clipboard the long way round.
 *
 * A browser hands a script the clipboard only on a page it trusts, which means
 * https or localhost — and HostMan is usually plain http on the LAN, where
 * there is no clipboard object at all. But the phone's own Copy and Paste have
 * never needed one: they work on a box of text. So this is that box, filled in
 * to be copied out of, or empty to be pasted into and sent.
 */
function ClipboardSheet({
  mode,
  text,
  onSend,
  onClose,
}: {
  mode: 'copy' | 'paste'
  text: string
  onSend: (text: string) => void
  onClose: () => void
}) {
  const boxRef = useRef<HTMLTextAreaElement>(null)
  const [typed, setTyped] = useState(text)

  useEffect(() => {
    const box = boxRef.current
    if (!box) return
    box.focus()
    // Already selected, so copying it is one long-press rather than a
    // long-press and two handles dragged along a wall of output.
    if (mode === 'copy') box.setSelectionRange(0, box.value.length)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <Sheet
      title={mode === 'copy' ? 'Copy this' : 'Paste into the shell'}
      subtitle={
        mode === 'copy'
          ? 'This page is not one the browser trusts with the clipboard, so it goes through here: long-press the text and choose Copy.'
          : 'Long-press the box and choose Paste, then send it — the shell sees it as one paste rather than as typing.'
      }
      onClose={onClose}
    >
      <textarea
        ref={boxRef}
        className="term-clip"
        value={typed}
        readOnly={mode === 'copy'}
        rows={mode === 'copy' ? 8 : 4}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        onChange={(e) => setTyped(e.target.value)}
      />
      {mode === 'paste' && (
        <button className="block" disabled={!typed} onClick={() => onSend(typed)}>
          Send it to the shell
        </button>
      )}
      <button className="secondary block" onClick={onClose}>
        {mode === 'copy' ? 'Done' : 'Cancel'}
      </button>
    </Sheet>
  )
}

/**
 * A key on the bar.
 *
 * It refuses focus rather than taking it, which is the whole trick: a button
 * that focused itself would dismiss the soft keyboard, and a key bar that
 * closes the keyboard every time you reach for Tab is worse than no key bar.
 */
function KeyButton({
  children,
  title,
  held,
  className,
  onPress,
}: {
  children: React.ReactNode
  title: string
  held?: boolean
  className?: string
  onPress: () => void
}) {
  return (
    <button
      type="button"
      className={`term-key ${className ?? ''} ${held ? 'held' : ''}`}
      title={title}
      aria-label={title}
      aria-pressed={held}
      onPointerDown={(e) => e.preventDefault()}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onPress}
    >
      {children}
    </button>
  )
}

/** What a key becomes with Ctrl held: the bottom five bits of it, which is what
 *  a terminal has always meant by a control character. */
function controlCode(ch: string): number | null {
  const c = ch.toLowerCase()
  if (c >= 'a' && c <= 'z') return c.charCodeAt(0) - 96
  const others: Record<string, number> = {
    '@': 0,
    ' ': 0,
    '[': 27,
    '\\': 28,
    ']': 29,
    '^': 30,
    '_': 31,
    '?': 127,
  }
  return c in others ? others[c] : null
}

/** How much was copied, said the way a person would. */
function count(text: string): string {
  const lines = text.split('\n').length
  return lines === 1 ? 'one line' : `${lines} lines`
}

function readFontSize(): number {
  const stored = Number(window.localStorage.getItem(SIZE_KEY))
  // 11 by default: big enough to read at arm's length, small enough that a
  // phone held upright fits the width most command output assumes it has.
  return SIZES.includes(stored) ? stored : 11
}
