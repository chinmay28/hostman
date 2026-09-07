import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../api'
import { ClaudeStream, MODELS, MODES, dollars, modeName, modelName, tokens, type ConnectionState } from '../lib/claude'
import { time } from '../lib/format'
import type { ClaudeBlock, ClaudeEntry, ClaudeSession } from '../types'
import { Badge, Sheet } from './ui'

/**
 * A conversation with Claude Code on a host, on a phone.
 *
 * The session lives on HostMan and this is a view of it: the history arrives
 * on a stream that resumes from where this screen got to, and what the user
 * says and answers goes back as ordinary requests. Two phones can look at the
 * same conversation; the one that answers a permission question settles it
 * for both, and the other sees the answer arrive.
 *
 * What Claude does is drawn as cards rather than as a terminal: a command it
 * ran with its output folded under it, a file it changed, a question it is
 * waiting on with the buttons right there. The permission card is the point
 * of the screen. It shows the exact command, and "Always" is for this session
 * only — the CLI's own rule, handed back to it.
 */

/** How many lines of a tool's output to show before folding the rest. */
const FOLD_LINES = 6

export default function ClaudeChat({
  session: initial,
  host,
  onSession,
  onLeave,
  onEnd,
}: {
  session: ClaudeSession
  host: string
  onSession: (s: ClaudeSession) => void
  onLeave: () => void
  onEnd: () => void
}) {
  const [session, setSession] = useState(initial)
  const [entries, setEntries] = useState<ClaudeEntry[]>([])
  const [gap, setGap] = useState(0)
  const [conn, setConn] = useState<ConnectionState>('connecting')
  const [error, setError] = useState<string | null>(null)
  const [text, setText] = useState('')
  const [sending, setSending] = useState(false)
  const [sheet, setSheet] = useState<'menu' | 'model' | 'mode' | 'end' | null>(null)
  const [changing, setChanging] = useState(false)

  const list = useRef<HTMLDivElement>(null)
  const pinned = useRef(true)

  const update = useCallback(
    (s: ClaudeSession) => {
      setSession(s)
      onSession(s)
    },
    [onSession],
  )

  // The whole history, from the start: a conversation is read from the top,
  // unlike a terminal's scrollback, and HostMan keeps it.
  useEffect(() => {
    const stream = new ClaudeStream(initial.id, 0, {
      onSession: update,
      onEntries: (batch) =>
        setEntries((current) => {
          const seen = new Set(current.map((e) => e.seq))
          const fresh = batch.filter((e) => !seen.has(e.seq))
          return fresh.length ? [...current, ...fresh] : current
        }),
      onGap: (missed) => setGap((g) => g + missed),
      onState: setConn,
      onExit: () => undefined,
    })
    stream.start()
    const onVisible = () => {
      if (!document.hidden) stream.refresh()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      document.removeEventListener('visibilitychange', onVisible)
      stream.stop()
    }
  }, [initial.id, update])

  // New things appear at the bottom, so that is where the screen stays —
  // unless somebody has scrolled up to read something.
  useEffect(() => {
    const node = list.current
    if (node && pinned.current) node.scrollTop = node.scrollHeight
  }, [entries, session.busy])

  const act = async (run: () => Promise<ClaudeSession | void>) => {
    setError(null)
    try {
      const s = await run()
      if (s) update(s)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const send = async () => {
    const body = text.trim()
    if (!body || sending) return
    setSending(true)
    try {
      await act(() => api.claudeSay(session.id, body))
      setText('')
      pinned.current = true
    } finally {
      setSending(false)
    }
  }

  const answer = (requestId: string, allow: boolean, always = false) =>
    act(() => api.claudeAnswer(session.id, { requestId, allow, always }))

  const change = async (run: () => Promise<ClaudeSession>) => {
    setChanging(true)
    try {
      await act(run)
      setSheet(null)
    } finally {
      setChanging(false)
    }
  }

  // Which permission questions are still open: asked since the turn began,
  // and neither answered nor withdrawn since. A turn that ended, and a session
  // that has, leave nothing to answer.
  const open = useMemo(() => {
    let pending = new Set<string>()
    for (const e of entries) {
      if (e.kind === 'permission' && e.requestId) pending.add(e.requestId)
      if ((e.kind === 'answered' || e.kind === 'permission_cancelled') && e.requestId) pending.delete(e.requestId)
      if (e.kind === 'result' || e.kind === 'exit') pending = new Set()
    }
    return session.running ? pending : new Set<string>()
  }, [entries, session.running])

  // Tool results by the use they answer, so a command card can fold its
  // output under it.
  const results = useMemo(() => {
    const m = new Map<string, ClaudeEntry>()
    for (const e of entries) if (e.kind === 'tool_result' && e.toolUseId) m.set(e.toolUseId, e)
    return m
  }, [entries])

  const modeInfo = MODES.find((m) => m.mode === session.mode)
  const bypass = session.mode === 'bypassPermissions'
  const lastKind = entries.length ? entries[entries.length - 1].kind : ''
  const working = session.running && session.busy && open.size === 0

  return (
    <div className="term-screen">
      <header className="term-bar">
        <button className="back" onClick={onLeave} aria-label="Back">
          ‹
        </button>
        <div className="term-title">
          <b>
            {host} · {session.dir}
          </b>
          <span>
            Claude · {modelName(session.model)} · {modeName(session.mode).toLowerCase()}
            {session.watchers > 1 ? ` · ${session.watchers} screens` : ''}
          </span>
        </div>
        <button className="term-end secondary" onClick={() => setSheet('menu')} aria-label="This session">
          ···
        </button>
      </header>

      {bypass && session.running && (
        <div className="chat-stripe">
          <WarnIcon />
          Permission checks are off. Claude runs whatever it decides to.
        </div>
      )}
      {conn === 'reconnecting' && <div className="term-state reconnecting">Reconnecting…</div>}
      {error && <div className="term-state reconnecting">{error}</div>}

      <div
        className="chat"
        ref={list}
        onScroll={() => {
          const node = list.current
          if (!node) return
          pinned.current = node.scrollHeight - node.scrollTop - node.clientHeight < 60
        }}
      >
        {gap > 0 && (
          <div className="chat-line">
            {gap} earlier {gap === 1 ? 'event is' : 'events are'} no longer kept.
          </div>
        )}
        {entries.length === 0 && conn === 'connecting' && <div className="chat-line">Connecting…</div>}
        {entries.length === 0 && conn === 'live' && !session.busy && (
          <div className="chat-line">
            Claude is ready in {session.dir} on {host}. Say what you want done.
          </div>
        )}
        {entries.map((e) => (
          <Entry
            key={e.seq}
            entry={e}
            result={e.kind === 'assistant' ? results : undefined}
            open={e.kind === 'permission' && !!e.requestId && open.has(e.requestId)}
            onAnswer={answer}
          />
        ))}
        {working && (
          <div className="chat-working">
            <i />
            <i />
            <i />
            {lastKind === 'user' ? 'Thinking' : 'Working'}
          </div>
        )}
        {!session.running && (
          <div className="chat-line ended">
            This session ended: {session.exit || 'Claude exited'}.
          </div>
        )}
      </div>

      <div className="composer">
        <div className="composer-line">
          <button className="composer-round secondary" onClick={() => setSheet('menu')} aria-label="Session menu">
            <SlashIcon />
          </button>
          <textarea
            className="composer-text"
            rows={1}
            value={text}
            placeholder={
              !session.running
                ? 'The session has ended'
                : open.size > 0
                  ? 'Answer above, or say something…'
                  : session.busy
                    ? 'Add to what Claude is doing…'
                    : 'Tell Claude what to do on ' + host + '…'
            }
            disabled={!session.running}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              // Enter sends on a keyboard with a Shift; a phone keyboard has
              // its own send button, so Enter there is a new line.
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                e.preventDefault()
                void send()
              }
            }}
          />
          {session.busy && session.running ? (
            <button
              className="composer-round stop"
              onClick={() => act(() => api.claudeInterrupt(session.id))}
              aria-label="Stop"
              title="Stop what Claude is doing"
            >
              <StopIcon />
            </button>
          ) : (
            <button
              className="composer-round send"
              onClick={send}
              disabled={!text.trim() || sending || !session.running}
              aria-label="Send"
            >
              <SendIcon />
            </button>
          )}
        </div>
        <div className="composer-meta">
          <button className="pill" onClick={() => setSheet('model')} disabled={!session.running}>
            {modelName(session.model)}
          </button>
          <button className={`pill ${bypass ? 'bad' : ''}`} onClick={() => setSheet('mode')} disabled={!session.running}>
            {modeInfo?.short ?? session.mode}
          </button>
          <span className="pill dim">
            {dollars(session.cost)} · {session.turns} {session.turns === 1 ? 'turn' : 'turns'}
          </span>
        </div>
      </div>

      {sheet === 'menu' && (
        <Sheet title="This session" subtitle={`${session.name || 'Untitled'} · started ${time(session.startedAt)} on ${host} as ${session.user}`} onClose={() => setSheet(null)}>
          <Row label="Context used" value={session.contextWindow ? `${tokens(session.context)} of ${tokens(session.contextWindow)} · ${Math.round((100 * session.context) / session.contextWindow)}%` : tokens(session.context)} />
          <Row label="Cost so far" value={`${dollars(session.cost)} · ${session.turns} ${session.turns === 1 ? 'turn' : 'turns'}`} />
          <Row label="Permissions" value={modeInfo?.name ?? session.mode} />
          <Row label="Watching" value={session.watchers === 1 ? 'This phone' : `${session.watchers} screens`} />
          <Row label="Resume on the host" value={`claude --resume ${session.cliSessionId.slice(0, 8)}…`} />
          <button className="secondary block" onClick={() => setSheet('model')} disabled={!session.running} style={{ marginTop: 14 }}>
            Model · {modelName(session.model)}
          </button>
          <button className="secondary block" onClick={() => setSheet('mode')} disabled={!session.running} style={{ marginTop: 8 }}>
            Permissions · {modeInfo?.name ?? session.mode}
          </button>
          <button className="danger block" onClick={() => setSheet('end')} style={{ marginTop: 8 }}>
            {session.running ? 'End this session' : 'Forget this session'}
          </button>
          <p className="sub">
            You do not need to end it to leave. Going back keeps it on {host}, and Claude finishes what it was
            doing. Left alone with nothing running, it closes after an hour.
          </p>
        </Sheet>
      )}

      {sheet === 'model' && (
        <Sheet title="Model" subtitle="Takes effect from the next message. The conversation so far carries over." onClose={() => setSheet(null)}>
          {MODELS.map((m) => {
            const current = modelName(session.model).startsWith(m.name)
            return (
              <button
                key={m.alias}
                className="opt"
                disabled={changing}
                onClick={() => change(() => api.claudeModel(session.id, m.alias))}
              >
                <span className={`radio ${current ? 'on' : ''}`} />
                <span className="grow" style={{ textAlign: 'left' }}>
                  <span className="opt-t">{m.name}</span>
                  <span className="opt-d">{m.about}</span>
                </span>
                {current && <Badge tone="accent">Current</Badge>}
              </button>
            )
          })}
        </Sheet>
      )}

      {sheet === 'mode' && (
        <Sheet title="Permissions" subtitle="Changes at once, for the rest of the session." onClose={() => setSheet(null)}>
          {MODES.map((m) => (
            <button
              key={m.mode}
              className="opt"
              disabled={changing}
              onClick={() => change(() => api.claudeMode(session.id, m.mode))}
            >
              <span className={`radio ${session.mode === m.mode ? 'on' : ''}`} />
              <span className="grow" style={{ textAlign: 'left' }}>
                <span className="opt-t">{m.name}</span>
                <span className="opt-d">{m.about}</span>
              </span>
              {m.mode === 'bypassPermissions' && <Badge tone="bad">Risky</Badge>}
            </button>
          ))}
        </Sheet>
      )}

      {sheet === 'end' && (
        <Sheet
          title={session.running ? 'End this session?' : 'Forget this session?'}
          subtitle={session.running ? 'Whatever Claude is doing stops with it.' : 'Its history goes from this screen.'}
          onClose={() => setSheet(null)}
        >
          <p className="sub">
            The conversation stays on {host}: <code>claude --resume</code> there picks it up again. What
            is on this screen does not.
          </p>
          <button className="danger block" onClick={onEnd}>
            {session.running ? 'End the session' : 'Forget it'}
          </button>
          <button className="secondary block" onClick={() => setSheet(null)}>
            Keep it
          </button>
        </Sheet>
      )}
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="chat-kv">
      <span className="sub">{label}</span>
      <b>{value}</b>
    </div>
  )
}

/** One event, drawn as what it is. */
function Entry({
  entry,
  result,
  open,
  onAnswer,
}: {
  entry: ClaudeEntry
  result?: Map<string, ClaudeEntry>
  open: boolean
  onAnswer: (requestId: string, allow: boolean, always?: boolean) => void
}) {
  switch (entry.kind) {
    case 'user':
      return <div className="msg user">{entry.text}</div>
    case 'assistant':
      return (
        <>
          {(entry.blocks ?? []).map((b, i) =>
            b.type === 'tool_use' ? (
              <ToolCard key={b.id ?? i} block={b} result={b.id ? result?.get(b.id) : undefined} />
            ) : b.type === 'text' ? (
              <div key={i} className="msg claude">
                <Prose text={b.text ?? ''} />
              </div>
            ) : null,
          )}
        </>
      )
    case 'permission':
      return open ? (
        <div className="ask">
          <div className="ask-who">Claude wants to {verb(entry.toolName)}</div>
          <pre>{summary(entry.toolName, entry.input, true)}</pre>
          {entry.description && <div className="sub">{entry.description}</div>}
          <div className="ask-btns">
            <button className="primary" onClick={() => entry.requestId && onAnswer(entry.requestId, true)}>
              Allow
            </button>
            <button className="secondary" onClick={() => entry.requestId && onAnswer(entry.requestId, true, true)} title="Allow this for the rest of the session">
              Always
            </button>
            <button className="danger" onClick={() => entry.requestId && onAnswer(entry.requestId, false)}>
              Deny
            </button>
          </div>
        </div>
      ) : null
    case 'answered':
      return (
        <div className="chat-line">
          {entry.behavior === 'allow' ? 'Allowed' : 'Denied'}
          {entry.text ? `: ${entry.text}` : ''}
        </div>
      )
    case 'permission_cancelled':
      return <div className="chat-line">Claude withdrew a question.</div>
    case 'result':
      return entry.isError && entry.text ? <div className="chat-line bad">{entry.text}</div> : null
    case 'notice':
      return <div className="chat-line">{entry.text}</div>
    case 'model':
      return <div className="chat-line">Model changed to {modelName(entry.model ?? '')}.</div>
    case 'mode':
      return <div className="chat-line">Permissions: {MODES.find((m) => m.mode === entry.mode)?.name ?? entry.mode}.</div>
    case 'init':
      return entry.cwd ? <div className="chat-line">Working in {entry.cwd} · {modelName(entry.model ?? '')}</div> : null
    default:
      return null
  }
}

/** A command or a file operation, with what came back folded under it. */
function ToolCard({ block, result }: { block: ClaudeBlock; result?: ClaudeEntry }) {
  const [shown, setShown] = useState(false)
  const lines = (result?.content ?? '').split('\n')
  const folded = !shown && lines.length > FOLD_LINES
  const body = folded ? lines.slice(0, FOLD_LINES).join('\n') : result?.content ?? ''
  const state = !result ? 'running…' : result.isError ? 'failed' : `${lines.length} ${lines.length === 1 ? 'line' : 'lines'}`
  return (
    <div className={`cc-tool ${result?.isError ? 'failed' : ''}`}>
      <div className="cc-tool-head">
        <ToolIcon name={block.name ?? ''} />
        <span className="cc-tool-what">{summary(block.name, block.input, false)}</span>
        <span className="cc-tool-state">{state}</span>
      </div>
      {result && result.content && (
        <>
          <div className="cc-tool-body">{body}</div>
          {lines.length > FOLD_LINES && (
            <button className="cc-tool-more" onClick={() => setShown(!shown)}>
              {shown ? 'Show less' : `Show ${lines.length - FOLD_LINES} more lines`}
            </button>
          )}
        </>
      )}
    </div>
  )
}

/** Text from Claude: paragraphs, with fenced code kept as code. Not a markdown
 *  renderer — a phone screen wants the words, and the code blocks are the one
 *  thing that goes wrong when they are not set apart. */
function Prose({ text }: { text: string }) {
  const parts = text.split(/```[a-z]*\n?/)
  return (
    <>
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <pre key={i} className="msg-code">
            {part.replace(/\n$/, '')}
          </pre>
        ) : (
          part
            .split(/\n{2,}/)
            .filter((p) => p.trim())
            .map((p, j) => (
              <p key={`${i}-${j}`}>
                <Inline text={p} />
              </p>
            ))
        ),
      )}
    </>
  )
}

/** `code` spans and **bold**, which is most of what Claude's prose carries. */
function Inline({ text }: { text: string }) {
  const bits = text.split(/(`[^`]+`|\*\*[^*]+\*\*)/)
  return (
    <>
      {bits.map((b, i) =>
        b.startsWith('`') && b.endsWith('`') && b.length > 1 ? (
          <code key={i}>{b.slice(1, -1)}</code>
        ) : b.startsWith('**') && b.endsWith('**') && b.length > 3 ? (
          <b key={i}>{b.slice(2, -2)}</b>
        ) : (
          b
        ),
      )}
    </>
  )
}

/** What a tool use is, in one line: the command, the file, or the input. */
function summary(name: string | undefined, input: unknown, full: boolean): string {
  const o = (input ?? {}) as Record<string, unknown>
  const str = (k: string) => (typeof o[k] === 'string' ? (o[k] as string) : '')
  switch (name) {
    case 'Bash':
      return str('command') || str('description')
    case 'Read':
      return `Read ${str('file_path')}`
    case 'Edit':
      return full ? `Edit ${str('file_path')}\n- ${str('old_string')}\n+ ${str('new_string')}` : `Edit ${str('file_path')}`
    case 'Write':
      return full ? `Write ${str('file_path')}\n${str('content')}` : `Write ${str('file_path')}`
    case 'Glob':
    case 'Grep':
      return `${name} ${str('pattern')}${str('path') ? ' in ' + str('path') : ''}`
    case 'WebFetch':
      return `Fetch ${str('url')}`
    case 'WebSearch':
      return `Search ${str('query')}`
    default: {
      const json = JSON.stringify(input ?? {})
      return `${name ?? 'Tool'} ${full ? json : json.slice(0, 80)}`
    }
  }
}

function verb(name?: string): string {
  switch (name) {
    case 'Bash':
      return 'run'
    case 'Edit':
    case 'Write':
      return 'change a file'
    case 'Read':
      return 'read a file'
    case 'WebFetch':
    case 'WebSearch':
      return 'use the web'
    default:
      return `use ${name ?? 'a tool'}`
  }
}

const stroke = {
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
}

function ToolIcon({ name }: { name: string }) {
  switch (name) {
    case 'Edit':
    case 'Write':
      return (
        <svg viewBox="0 0 24 24" {...stroke}>
          <path d="M12 20h9" />
          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z" />
        </svg>
      )
    case 'Read':
    case 'Glob':
    case 'Grep':
      return (
        <svg viewBox="0 0 24 24" {...stroke}>
          <path d="M3 6.5A1.5 1.5 0 0 1 4.5 5h4l2 2h9A1.5 1.5 0 0 1 21 8.5v9a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 17.5z" />
        </svg>
      )
    default:
      return (
        <svg viewBox="0 0 24 24" {...stroke}>
          <path d="M5 8l4 4-4 4" />
          <path d="M12 16h7" />
        </svg>
      )
  }
}

function SlashIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke} strokeWidth={2}>
      <path d="M15 4 9 20" />
    </svg>
  )
}

function SendIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke} strokeWidth={2.2}>
      <path d="M12 19V5" />
      <path d="m6 11 6-6 6 6" />
    </svg>
  )
}

function StopIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor">
      <rect x="6" y="6" width="12" height="12" rx="2" />
    </svg>
  )
}

function WarnIcon() {
  return (
    <svg viewBox="0 0 24 24" {...stroke} strokeWidth={2.2}>
      <path d="M12 3 2.5 20h19z" />
      <path d="M12 10v4M12 17.5h.01" />
    </svg>
  )
}
