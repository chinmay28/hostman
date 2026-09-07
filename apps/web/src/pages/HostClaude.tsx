import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import ClaudeChat from '../components/ClaudeChat'
import { Page } from '../components/Layout'
import { Badge, Banner, Card, Copyable, Field, Loading, SectionTitle, useLoader } from '../components/ui'
import { MODELS, MODES, dollars, modelName } from '../lib/claude'
import { time } from '../lib/format'
import type { ClaudeHost, ClaudeSession } from '../types'

/** The installer, for somebody who would rather run it themselves. */
const INSTALL = 'curl -fsSL https://claude.ai/install.sh | bash'

/** How often to ask while an install runs or a sign-in waits. */
const BUSY_POLL_MS = 4000

/** The last folder and model used, remembered on the phone: the next session
 *  on the same host usually wants the same ones. */
const PREFS_KEY = 'deployer.claude.start'

/**
 * Claude Code on a host.
 *
 * Getting it there is the top of the screen and happens once: install it for
 * the SSH user, sign that user in. Both take longer than a request — a
 * download onto a Pi, a person with a phone — so both run on the host by
 * themselves and this screen follows along. The sign-in is the interesting
 * one: the CLI prints a link and waits for a code, the phone is what opens
 * the link, and the code the phone is shown goes back through HostMan to the
 * process waiting for it. The token that results is written by the CLI into
 * the user's home on the host, and HostMan never sees it.
 *
 * Talking to it is the rest. A session belongs to HostMan, not to this
 * screen, exactly as a shell does: leaving keeps it open, coming back rejoins
 * it with the whole conversation, and ending it is a thing you do on purpose.
 */
export default function HostClaude() {
  const { id } = useParams()
  const hostId = Number(id)

  const [session, setSession] = useState<ClaudeSession | null>(null)
  const [pace, setPace] = useState<number | undefined>(undefined)
  const [busy, setBusy] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const host = useLoader(() => api.host(hostId), [hostId])
  const status = useLoader(() => api.claude(hostId), [hostId], pace)
  const open = useLoader(() => api.claudeSessions(hostId), [hostId], session ? undefined : 5000)

  // Polling belongs to an install or a sign-in in progress and to nothing
  // else: it stops the moment either is settled.
  useEffect(() => {
    const c = status.data
    const following = !!c && (c.install === 'running' || c.login === 'running' || c.login === 'waiting')
    setPace(following && !session ? BUSY_POLL_MS : undefined)
  }, [status.data, session])

  const act = async (label: string, run: () => Promise<unknown>) => {
    setBusy(label)
    setFailure(null)
    try {
      await run()
      status.reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const leave = useCallback(() => {
    setSession(null)
    open.reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const end = async () => {
    if (session) {
      try {
        await api.closeClaude(session.id)
      } catch {
        // Already gone is the outcome asked for.
      }
    }
    leave()
  }

  useEffect(() => {
    if (!session) return
    document.body.classList.add('terminal-open')
    return () => document.body.classList.remove('terminal-open')
  }, [session])

  if (session) {
    return (
      <ClaudeChat
        session={session}
        host={host.data?.name ?? 'the host'}
        onSession={setSession}
        onLeave={leave}
        onEnd={end}
      />
    )
  }

  const c = status.data
  const name = host.data?.name ?? 'this host'
  const running = (open.data ?? []).filter((s) => s.running)
  const ended = (open.data ?? []).filter((s) => !s.running)

  return (
    <Page title="Claude" back={`/hosts/${hostId}`}>
      <Loading error={status.error} offline={status.offline} hasData={!!c} />
      {failure && <Banner tone="bad">{failure}</Banner>}

      {!c ? (
        <Card>
          <div className="sub">Asking {name} about Claude…</div>
        </Card>
      ) : !c.installed ? (
        <NotInstalled c={c} host={name} busy={busy} onInstall={() => act('install', () => api.installClaude(hostId))} />
      ) : !c.signedIn ? (
        <SignIn
          c={c}
          host={name}
          busy={busy}
          onLogin={(console) => act('login', () => api.claudeLogin(hostId, console))}
          onCode={(code) => act('code', () => api.claudeLoginCode(hostId, code))}
          onCancel={() => act('cancel', () => api.cancelClaudeLogin(hostId))}
          onKey={(key) => act('key', () => api.claudeKey(hostId, key))}
        />
      ) : (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">Claude Code on {name}</div>
                <div className="sub">
                  v{c.version}
                  {c.account ? ` · signed in as ${c.account}` : c.auth === 'api_key' ? ' · using an API key' : ''}
                  {c.plan ? ` · ${c.plan} plan` : ''}
                </div>
              </div>
              <Badge tone="good" dot>
                Ready
              </Badge>
            </div>
          </Card>

          {running.length > 0 && (
            <>
              <p className="sub" style={{ margin: '2px 4px 8px' }}>
                {running.length === 1 ? 'A session is' : `${running.length} sessions are`} already open on this
                host. Rejoining one puts you back in the conversation where it was left.
              </p>
              {running.map((s) => (
                <SessionRow key={s.id} s={s} onOpen={() => setSession(s)} />
              ))}
            </>
          )}

          <Start
            host={name}
            user={c.user}
            another={running.length > 0}
            busy={busy}
            onStart={(input) =>
              act('start', async () => {
                const s = await api.openClaude(hostId, input)
                setSession(s)
              })
            }
          />

          {ended.length > 0 && (
            <>
              <SectionTitle>Ended</SectionTitle>
              {ended.map((s) => (
                <SessionRow key={s.id} s={s} onOpen={() => setSession(s)} />
              ))}
            </>
          )}

          <SectionTitle>Claude itself</SectionTitle>
          <Card>
            <p className="sub" style={{ marginTop: 0 }}>
              Installed for {c.user} in <span className="mono">{c.path}</span>. It keeps itself up to date;
              running the installer again is how to make it now.
            </p>
            <div className="actions" style={{ marginTop: 8 }}>
              <button className="secondary" onClick={() => act('install', () => api.installClaude(hostId))} disabled={!!busy}>
                {busy === 'install' || c.install === 'running' ? 'Updating…' : 'Update now'}
              </button>
              <button className="secondary" onClick={() => act('login', () => api.claudeLogin(hostId, false))} disabled={!!busy}>
                Sign in again
              </button>
            </div>
            {c.install === 'running' && <Log log={c.installLog} />}
            {c.login === 'waiting' && (
              <Banner tone="warn">
                A sign-in is waiting for a code. Sign out on the host first if you mean to switch accounts; otherwise
                cancel it below.
              </Banner>
            )}
            {c.login === 'waiting' && (
              <button className="secondary block" onClick={() => act('cancel', () => api.cancelClaudeLogin(hostId))} disabled={!!busy}>
                Cancel the sign-in
              </button>
            )}
          </Card>

          <Card>
            <div className="title">Other ways in</div>
            <Link className="sub link-row" to={`/hosts/${hostId}/shell`}>
              Typing the commands yourself is the <b>Terminal</b> ›
            </Link>
            <Link className="sub link-row" to={`/hosts/${hostId}/files`}>
              Reading what Claude changed is <b>Files</b> ›
            </Link>
          </Card>
        </>
      )}
    </Page>
  )
}

function SessionRow({ s, onOpen }: { s: ClaudeSession; onOpen: () => void }) {
  return (
    <button className="card row between" onClick={onOpen}>
      <div className="grow" style={{ textAlign: 'left' }}>
        <div className="title">{s.name || 'Untitled'}</div>
        <div className="sub">
          {s.dir} · {modelName(s.model)} · started {time(s.startedAt)} · {s.turns} {s.turns === 1 ? 'turn' : 'turns'} ·{' '}
          {dollars(s.cost)}
          {s.watchers > 0 ? ` · ${s.watchers} watching` : ''}
        </div>
      </div>
      {!s.running ? (
        <Badge tone="neutral">Ended</Badge>
      ) : s.pending > 0 ? (
        <Badge tone="warn" dot pulse>
          Asking
        </Badge>
      ) : s.busy ? (
        <Badge tone="good" dot pulse>
          Working
        </Badge>
      ) : (
        <Badge tone="good" dot>
          Open
        </Badge>
      )}
    </button>
  )
}

/** Starting a session: where, which model, how much it may do by itself. */
function Start({
  host,
  user,
  another,
  busy,
  onStart,
}: {
  host: string
  user: string
  another: boolean
  busy: string | null
  onStart: (input: { dir: string; model: string; mode: string }) => void
}) {
  const [prefs] = useState(() => {
    try {
      return JSON.parse(localStorage.getItem(PREFS_KEY) ?? '{}') as { dir?: string; model?: string }
    } catch {
      return {}
    }
  })
  const [dir, setDir] = useState(prefs.dir ?? '~')
  const [model, setModel] = useState(prefs.model ?? 'sonnet')
  const [mode, setMode] = useState('default')
  const modeInfo = MODES.find((m) => m.mode === mode)

  const start = () => {
    try {
      localStorage.setItem(PREFS_KEY, JSON.stringify({ dir, model }))
    } catch {
      // A phone that will not remember is a phone that asks again.
    }
    onStart({ dir: dir.trim() || '~', model, mode })
  }

  return (
    <Card>
      <div className="title">{another ? 'Or start another' : 'Start a session'}</div>
      <p className="sub" style={{ margin: '4px 0 14px' }}>
        A conversation with Claude Code running on {host}, as <b>{user}</b>. It reads, edits and runs things
        there; you talk to it here.
      </p>
      <Field label="Start in" help="The folder Claude works in. Its CLAUDE.md and settings apply.">
        <input className="mono" value={dir} onChange={(e) => setDir(e.target.value)} placeholder="~" spellCheck={false} autoCapitalize="off" />
      </Field>
      <Field label="Model">
        <div className="chips" style={{ marginTop: 4 }}>
          {MODELS.filter((m) => m.alias !== 'opusplan').map((m) => (
            <button key={m.alias} className={`chip ${model === m.alias ? 'on' : ''}`} onClick={() => setModel(m.alias)}>
              {m.name}
            </button>
          ))}
        </div>
      </Field>
      <Field label="Permissions" help={modeInfo?.about}>
        <div className="trio">
          {MODES.filter((m) => m.mode !== 'plan').map((m) => (
            <button key={m.mode} className={`trio-s ${mode === m.mode ? 'on' : ''}`} onClick={() => setMode(m.mode)}>
              {m.name}
            </button>
          ))}
        </div>
      </Field>
      {mode === 'bypassPermissions' && (
        <Banner tone="bad">
          Skipping permissions is the CLI's <span className="mono">--dangerously-skip-permissions</span>: Claude runs
          commands and changes files on {host} without asking, as {user}, with sudo where {user} has it. The
          session wears a red stripe the whole way through.
        </Banner>
      )}
      <button className="primary block" onClick={start} disabled={!!busy}>
        {busy === 'start' ? 'Starting…' : mode === 'bypassPermissions' ? 'Start, skipping permissions' : 'Start a session'}
      </button>
    </Card>
  )
}

/** Before the CLI is there: what installing does, one button, and the same
 *  command for anybody who would rather run it themselves. */
function NotInstalled({ c, host, busy, onInstall }: { c: ClaudeHost; host: string; busy: string | null; onInstall: () => void }) {
  const running = c.install === 'running'
  return (
    <>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">Claude Code is not on {host} yet</div>
            <div className="sub">
              {c.os} · {c.arch} · for {c.user}
            </div>
          </div>
          <Badge tone="neutral">Not installed</Badge>
        </div>
        <p className="sub" style={{ marginTop: 10 }}>
          HostMan installs it for the user it signs in as, <b>{c.user}</b>, with Anthropic's own installer. It lands
          in <span className="mono">~/.local/bin</span> and keeps itself up to date from there. Nothing is installed
          system-wide, and no sudo is needed.
        </p>
        {c.install === 'failed' && (
          <Banner tone="bad">
            The install did not finish{c.installExit ? ` (exit ${c.installExit})` : ''}. The end of its log is below.
          </Banner>
        )}
        {!running && (
          <button className="primary block" onClick={onInstall} disabled={!!busy}>
            {busy === 'install' ? 'Starting…' : c.install === 'failed' ? 'Try again' : 'Install Claude Code'}
          </button>
        )}
      </Card>
      {(running || c.install === 'failed') && (
        <Card>
          <div className="title">{running ? 'Installing…' : 'What happened'}</div>
          {running && (
            <p className="sub" style={{ marginTop: 4 }}>
              A download of some tens of megabytes onto {host}. It carries on if you leave this screen or lock your
              phone.
            </p>
          )}
          <Log log={c.installLog} />
        </Card>
      )}
      <Card>
        <div className="title">Or do it yourself</div>
        <p className="sub" style={{ margin: '4px 0 10px' }}>
          The same command, run on {host} as {c.user}. HostMan notices on its next look.
        </p>
        <Copyable text={INSTALL} />
        <p className="sub" style={{ marginTop: 10 }}>
          Claude Code needs a Claude subscription or an API key. Signing in comes after the install, and happens on this
          screen.
        </p>
      </Card>
    </>
  )
}

/** Installed, not signed in: the link-and-code flow, or an API key. */
function SignIn({
  c,
  host,
  busy,
  onLogin,
  onCode,
  onCancel,
  onKey,
}: {
  c: ClaudeHost
  host: string
  busy: string | null
  onLogin: (console: boolean) => void
  onCode: (code: string) => void
  onCancel: () => void
  onKey: (key: string) => void
}) {
  const [code, setCode] = useState('')
  const [key, setKey] = useState('')
  const [console_, setConsole] = useState(false)
  const waiting = c.login === 'waiting'
  const starting = c.login === 'running'

  return (
    <>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">Installed, not signed in</div>
            <div className="sub">
              v{c.version} on {host} · for {c.user}
            </div>
          </div>
          <Badge tone="warn">Sign in</Badge>
        </div>
        <p className="sub" style={{ marginTop: 10 }}>
          The sign-in belongs to {host}: Claude keeps it in {c.user}'s home folder, and HostMan never sees the
          token. This phone only carries the sign-in code across.
        </p>
      </Card>

      <Card>
        <div className="title">Sign in with your Claude account</div>
        <div style={{ marginTop: 8 }}>
          <Step
            label={`Ask Claude on ${host} for a sign-in link`}
            state={waiting || starting ? 'done' : c.login === 'failed' ? 'failed' : 'now'}
          />
          <Step label="Open the link and approve, here on this phone" state={waiting ? 'now' : starting ? 'soon' : 'later'} />
          <Step label="Paste the code claude.ai shows you" state={waiting ? 'then' : 'later'} />
        </div>
        {c.login === 'failed' && (
          <Banner tone="bad">
            The sign-in did not finish{c.loginExit && c.loginExit > 0 ? ` (exit ${c.loginExit})` : ''}.
            {c.loginLog ? ' The end of what it said is below.' : ''}
          </Banner>
        )}
        {c.login === 'failed' && <Log log={c.loginLog} />}

        {!waiting && !starting && (
          <>
            <label className="checkbox" style={{ marginTop: 10 }}>
              <input type="checkbox" checked={console_} onChange={(e) => setConsole(e.target.checked)} />
              <span className="sub">Use an Anthropic Console account (billed per use) rather than a Claude subscription</span>
            </label>
            <button className="primary block" onClick={() => onLogin(console_)} disabled={!!busy}>
              {busy === 'login' ? 'Asking…' : c.login === 'failed' ? 'Try again' : 'Start signing in'}
            </button>
          </>
        )}
        {starting && <p className="sub">Waiting for {host} to print the link…</p>}
        {waiting && (
          <>
            <a className="primary block btn-link" href={c.loginUrl} target="_blank" rel="noreferrer">
              <OpenIcon /> Open claude.ai to approve
            </a>
            <Field label="Sign-in code" help="The link is good for a few minutes. Cancel and ask for another if it runs out.">
              <input
                className="mono"
                value={code}
                onChange={(e) => setCode(e.target.value)}
                placeholder="Paste the code from claude.ai"
                spellCheck={false}
                autoCapitalize="off"
                autoCorrect="off"
              />
            </Field>
            <button className="primary block" onClick={() => onCode(code)} disabled={!!busy || !code.trim()} style={{ marginTop: 0 }}>
              {busy === 'code' ? 'Signing in…' : 'Finish signing in'}
            </button>
            <button className="secondary block" onClick={onCancel} disabled={!!busy}>
              Cancel
            </button>
          </>
        )}
      </Card>

      <Card>
        <div className="title">Or use an API key</div>
        <p className="sub" style={{ margin: '4px 0 10px' }}>
          Billed per use to a Console account instead of a subscription. Stored on {host} in Claude's own settings,
          for {c.user} only.
        </p>
        <input
          className="mono"
          type="password"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="sk-ant-…"
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
        />
        <button className="secondary block" onClick={() => onKey(key)} disabled={!!busy || !key.trim()}>
          {busy === 'key' ? 'Storing…' : `Save the key on ${host}`}
        </button>
      </Card>
    </>
  )
}

function Step({ label, state }: { label: string; state: 'done' | 'now' | 'then' | 'soon' | 'later' | 'failed' }) {
  return (
    <div className="row between" style={{ padding: '5px 0' }}>
      <span className="sub grow">{label}</span>
      {state === 'done' && <Badge tone="good">Done</Badge>}
      {state === 'now' && (
        <Badge tone="accent" dot pulse>
          Now
        </Badge>
      )}
      {state === 'soon' && <Badge tone="accent">Soon</Badge>}
      {state === 'then' && <Badge tone="neutral">Then</Badge>}
      {state === 'later' && <Badge tone="neutral">Later</Badge>}
      {state === 'failed' && <Badge tone="bad">Failed</Badge>}
    </div>
  )
}

/** The tail of a log, followed as it is written. */
function Log({ log }: { log?: string }) {
  if (!log) return null
  return (
    <pre className="log" style={{ marginTop: 10 }}>
      {log}
    </pre>
  )
}

function OpenIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M14 4h6v6" />
      <path d="M20 4 10 14" />
      <path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5" />
    </svg>
  )
}
