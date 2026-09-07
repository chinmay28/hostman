import { Suspense, lazy, useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { Badge, Banner, Card, Loading, Sheet, useLoader } from '../components/ui'
import { time } from '../lib/format'
import type { ShellSession } from '../types'

// xterm is the biggest thing in the bundle and every other screen manages
// without it, so it arrives when a terminal does.
const TerminalView = lazy(() => import('../components/Terminal'))

/**
 * A shell on a host.
 *
 * The shell belongs to HostMan, not to this screen. That is what the whole
 * design turns on: a phone drops its connection every time it locks, and a
 * terminal that lived in the page would lose the directory it was in, the
 * command half typed, and whatever was running. So leaving this screen leaves
 * the shell open, coming back rejoins it with its scrollback, and ending it is
 * a thing you do on purpose — by typing `exit`, or with End here.
 *
 * It runs as the user HostMan signs in as. Everything else in HostMan quietly
 * becomes root where it can, because a file browser that could not open /etc
 * would be hiding the reality; a shell is the opposite case. A prompt that says
 * one thing and runs as another is how a machine gets broken by somebody who
 * was being careful, so this is the one screen that does not elevate. `sudo` is
 * right there, and typing it is a decision.
 */
export default function HostShell() {
  const { id } = useParams()
  const hostId = Number(id)

  const [session, setSession] = useState<ShellSession | null>(null)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [ended, setEnded] = useState<string | null>(null)
  const [confirmEnd, setConfirmEnd] = useState(false)

  const host = useLoader(() => api.host(hostId), [hostId])
  // Only while nothing is attached: once a terminal is up, the stream is what
  // says how the session is doing, and polling behind it would be noise.
  const open = useLoader(() => api.shells(hostId), [hostId], session ? undefined : 5000)

  const start = async () => {
    setStarting(true)
    setError(null)
    setEnded(null)
    try {
      // A size worth having before the terminal has measured itself: it fits
      // properly a frame later, and the host is told again when it does.
      setSession(await api.openShell(hostId, 80, 24))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setStarting(false)
    }
  }

  const attach = (existing: ShellSession) => {
    setEnded(null)
    setError(null)
    setSession(existing)
  }

  const leave = useCallback(() => {
    setSession(null)
    setEnded(null)
    open.reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const end = async () => {
    setConfirmEnd(false)
    if (session) {
      try {
        await api.closeShell(session.id)
      } catch {
        // Already gone is the outcome asked for.
      }
    }
    leave()
  }

  // The tab bar and the page behind it are in the way of a terminal, and a
  // terminal is the one screen that wants the whole phone.
  useEffect(() => {
    if (!session) return
    document.body.classList.add('terminal-open')
    return () => document.body.classList.remove('terminal-open')
  }, [session])

  if (session) {
    return (
      <div className="term-screen">
        <header className="term-bar">
          <button className="back" onClick={leave} aria-label="Back">
            ‹
          </button>
          <div className="term-title">
            <b>{host.data?.name ?? 'Host'}</b>
            <span>
              {session.user}
              {session.watchers > 1 ? ` · ${session.watchers} screens` : ''}
            </span>
          </div>
          <button className="term-end" onClick={() => setConfirmEnd(true)}>
            End
          </button>
        </header>

        {error && <div className="term-state reconnecting">{error}</div>}

        <Suspense fallback={<div className="term-state connecting">Loading the terminal…</div>}>
          <TerminalView
            session={session}
            onSession={setSession}
            onExit={(reason) => setEnded(reason)}
            onError={setError}
          />
        </Suspense>

        {ended && (
          <Sheet
            title="The shell ended"
            subtitle={ended}
            onClose={leave}
          >
            <p className="sub">
              What it printed is still on the screen behind this. Starting another one is a fresh
              login: a new shell, in the home directory, knowing nothing of this one.
            </p>
            <button className="block" onClick={() => { setEnded(null); void start() }}>
              Start another
            </button>
            <button className="secondary block" onClick={leave}>
              Back to the host
            </button>
          </Sheet>
        )}

        {confirmEnd && (
          <Sheet
            title="End this shell?"
            subtitle="Whatever is running in it stops with it."
            onClose={() => setConfirmEnd(false)}
          >
            <p className="sub">
              You do not need to end it to leave: going back keeps it open on {host.data?.name},
              and coming back rejoins it. It closes itself after being left alone for a while.
            </p>
            <button className="danger block" onClick={end}>
              End the shell
            </button>
            <button className="secondary block" onClick={() => setConfirmEnd(false)}>
              Keep it open
            </button>
          </Sheet>
        )}
      </div>
    )
  }

  const running = (open.data ?? []).filter((s) => s.running)
  const user = host.data?.username ?? 'the SSH user'
  const isRoot = host.data?.username === 'root'

  return (
    <Page title="Terminal" back={`/hosts/${hostId}`}>
      <Loading error={host.error} offline={host.offline} hasData={!!host.data} />
      {error && <Banner tone="bad">{error}</Banner>}

      {host.data && host.data.status !== 'online' && (
        <Banner tone="warn">
          {host.data.name} is {host.data.status}. Opening a shell will try to reach it anyway.
        </Banner>
      )}

      {running.length > 0 && (
        <>
          <p className="sub">
            {running.length === 1 ? 'A shell is' : `${running.length} shells are`} already open on
            this host. Rejoining one puts you back where it was left.
          </p>
          {running.map((s) => (
            <button key={s.id} className="card row between" onClick={() => attach(s)}>
              <div className="grow" style={{ textAlign: 'left' }}>
                <div className="title">
                  {s.user}@{host.data?.name}
                </div>
                <div className="sub">
                  Started {time(s.startedAt)} · {s.cols}×{s.rows}
                  {s.watchers > 0 ? ` · ${s.watchers} watching` : ''}
                </div>
              </div>
              <Badge tone="good" dot pulse>
                Open
              </Badge>
            </button>
          ))}
        </>
      )}

      <Card>
        <div className="title">
          {running.length > 0 ? 'Or start another' : 'Start a shell'}
        </div>
        <p className="sub" style={{ marginTop: 4 }}>
          A login shell on {host.data?.name ?? 'this host'} as <b>{user}</b> — the same thing{' '}
          <code>
            ssh {user}@{host.data?.address}
          </code>{' '}
          would give you, with the keys a phone keyboard is missing along the bottom.
        </p>
        <p className="sub">
          It stays open when you leave this screen, so locking your phone mid-command is survivable.
          {/* Everything else in HostMan takes root where it can, so the one
              screen that does not is worth saying out loud — and saying the
              opposite to somebody who signs in as root would be a lie. */}
          {isRoot
            ? ' HostMan signs in to this host as root, so the shell is root: there is no sudo to forget.'
            : ' It is not root, whatever HostMan does elsewhere: sudo is there when you mean it.'}
        </p>
        <button className="block" onClick={start} disabled={starting || !host.data}>
          {starting ? 'Opening…' : 'Start a shell'}
        </button>
      </Card>

      <Card>
        <div className="title">Other ways in</div>
        <p className="sub" style={{ marginTop: 4 }}>
          A shell is the general answer, and often not the quickest one.
        </p>
        <Link className="sub link-row" to={`/hosts/${hostId}/files`}>
          Editing a config is fewer taps in <b>Files</b> ›
        </Link>
        <Link className="sub link-row" to={`/hosts/${hostId}/services`}>
          Restarting something is one tap in <b>Services</b> ›
        </Link>
        <Link className="sub link-row" to={`/hosts/${hostId}/cron`}>
          A scheduled job is a form in <b>Scheduled jobs</b> ›
        </Link>
      </Card>
    </Page>
  )
}
