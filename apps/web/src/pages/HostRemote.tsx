import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import { LaunchIcon } from '../components/launch'
import { Page } from '../components/Layout'
import { Banner, Card, Copyable, Field, Loading, SectionTitle, Sheet, useLoader } from '../components/ui'
import { ago, bytes } from '../lib/format'
import { openApp, reachable } from '../lib/launch'
import type { RemoteSession } from '../types'

/** The screen sizes worth a tap. The default is desktop-width on purpose: a
 *  narrow screen makes sites serve their phone layout, which is the layout the
 *  phone in your hand already has. noVNC scales whatever it is down to fit. */
const SIZES = ['1024x768', '1280x800', '1440x900']

/** How often to ask while an install is running. Every other screen here
 *  refreshes when you ask it to, because each answer is an SSH session — but
 *  apt takes minutes, and a screen that made you tap to find out whether it had
 *  finished would be worse company than one that asks for you. */
const INSTALL_POLL_MS = 5000

/**
 * A browser running on the host, driven from the phone.
 *
 * The thing it is for is the sequence a file behind a login needs: open the
 * site, sign in, click download — with the file landing on the host, in the
 * host's own Downloads directory, because that is the machine that was supposed
 * to end up with it. The browser keeps its profile between sessions, so the
 * signing in usually only happens once.
 *
 * Everything here is sized for a thumb: full-width buttons, no typing where a
 * tap will do, and the address bar of the session replaced by one field on this
 * screen — typing a URL into a browser over VNC on a phone is the worst part of
 * doing this by hand.
 */
export default function HostRemote() {
  const { id } = useParams()
  const hostId = Number(id)

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  const [installing, setInstalling] = useState(false)
  const {
    data: session,
    error,
    offline,
    reload,
  } = useLoader(() => api.remote(hostId), [hostId], installing ? INSTALL_POLL_MS : undefined)

  const [page, setPage] = useState('')
  const [size, setSize] = useState(SIZES[1])
  const [busy, setBusy] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [removing, setRemoving] = useState(false)

  // Polling belongs to an install and to nothing else: it stops the moment apt
  // is finished, and the screen goes back to answering when it is asked.
  useEffect(() => {
    setInstalling(session?.setup === 'running')
  }, [session?.setup])

  // The field opens on the page the session last had, which for the second
  // visit to a site is the whole of the typing.
  useEffect(() => {
    if (session?.homepage) setPage((current) => current || session.homepage || '')
    if (session?.geometry) setSize((current) => (SIZES.includes(session.geometry) ? session.geometry : current))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.homepage, session?.geometry])

  const act = async (label: string, run: () => Promise<unknown>) => {
    setBusy(label)
    setFailure(null)
    try {
      await run()
      reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const setup = () => act('setup', () => api.setupRemote(hostId, { geometry: size, homepage: page }))
  const start = () =>
    act('start', async () => {
      // Nothing is running at this moment, so this is the safe point to bring
      // the host's copy of the scripts up to date. A session that keeps running
      // last month's script is how a fix somebody installed changes nothing.
      if (session?.stale) {
        await api.setupRemote(hostId, {
          geometry: session.geometry,
          port: session.port,
          homepage: page,
        })
      }
      await api.remoteAction(hostId, 'start', page)
    })
  const stop = () => act('stop', () => api.remoteAction(hostId, 'stop'))
  const remove = (purge: boolean) =>
    act('remove', async () => {
      await api.removeRemote(hostId, purge)
      setRemoving(false)
    })

  // The server names the address; the one thing it cannot know is which of the
  // machine's addresses this phone used to get here, which is what makes a
  // session on the home host openable at all.
  const sessionUrl = reachable(session?.url)

  return (
    <Page title="Remote session" back={`/hosts/${hostId}`}>
      <Loading error={error} offline={offline} hasData={!!session} />
      {failure && <Banner tone="bad">{failure}</Banner>}

      {session?.stale && (
        <Card>
          <Banner tone="warn">
            This host is running a session script an older HostMan wrote. Updating HostMan does
            not reach back and rewrite it — setting the session up again does.
          </Banner>
          <p className="sub" style={{ marginTop: 0 }}>
            Nothing is lost by it: the password and the browser profile stay, so you stay signed
            into whatever you were. A session that is running keeps the old script until you stop
            and start it.
          </p>
          <button className="secondary block" onClick={setup} disabled={!!busy}>
            {busy === 'setup' ? 'Setting up…' : 'Set it up again'}
          </button>
        </Card>
      )}

      {session?.noSandbox && (
        <Banner tone="warn">
          The browser here runs without its sandbox: this host's kernel would not give it one, and
          the alternative was a session that never started. That is a weaker browser than the one on
          your phone — worth a thought before you sign into something with it.
        </Banner>
      )}

      {!session ? (
        <Card>
          <div className="sub">Asking {host?.name ?? 'the host'} about its browser…</div>
        </Card>
      ) : (
        <>
          {session.setup === 'running' && <Installing session={session} />}
          {session.setup === 'failed' && <Failed session={session} />}

          {session.ready ? (
            session.running ? (
              <Live
                session={session}
                url={sessionUrl}
                busy={busy}
                onStop={stop}
              />
            ) : (
              <Idle
                session={session}
                page={page}
                onPage={setPage}
                busy={busy}
                onStart={start}
              />
            )
          ) : (
            session.setup !== 'running' && (
              <Absent
                session={session}
                host={host?.name ?? 'this host'}
                sudo={host?.sudoOk !== false}
                size={size}
                onSize={setSize}
                page={page}
                onPage={setPage}
                busy={busy}
                onSetup={setup}
              />
            )
          )}

          {(session.running || session.active === 'failed') && (
            <SessionLog hostId={hostId} unit={session.unit} />
          )}

          <Downloads session={session} hostId={hostId} />

          {(session.ready || session.setup === 'failed') && (
            <>
              <SectionTitle>Settings</SectionTitle>
              <Card>
                <Field
                  label="Screen size"
                  help="The size the sites see. noVNC scales it to your phone."
                >
                  <div className="chips">
                    {SIZES.map((option) => (
                      <button
                        key={option}
                        className={`chip ${option === size ? 'on' : ''}`}
                        onClick={() => setSize(option)}
                      >
                        {option}
                      </button>
                    ))}
                  </div>
                </Field>
                <p className="sub" style={{ marginTop: 0 }}>
                  Writing the session again is also how a host takes a newer HostMan's script. It
                  keeps the password and the browser profile, so nothing signs out; a running
                  session picks the change up when it is stopped and started.
                </p>
                <button className="secondary block" onClick={setup} disabled={!!busy}>
                  {busy === 'setup' ? 'Setting up…' : 'Set it up again'}
                </button>
              </Card>

              <SectionTitle>The session</SectionTitle>
              <Card>
                <Detail label="Runs as" value={session.user} />
                <Detail label="Browser" value={session.browser ?? 'not installed yet'} />
                <Detail label="Screen" value={session.geometry} />
                <Detail label="Answers on" value={`port ${session.port}`} />
                <Detail label="Service" value={session.unit} />
                <p className="sub" style={{ marginBottom: 0 }}>
                  It is an ordinary service, so its log is on the{' '}
                  <Link to={`/hosts/${hostId}/service?name=${encodeURIComponent(session.unit)}`}>
                    Services
                  </Link>{' '}
                  screen. It has no [Install] section and cannot be enabled: a browser holding your
                  logins should run while you are using it and not after.
                </p>
              </Card>

              <SectionTitle>Danger zone</SectionTitle>
              <Card>
                <p className="sub" style={{ marginTop: 0 }}>
                  Removing takes the service and its settings off {host?.name ?? 'the host'}. The
                  packages stay, and so does everything already downloaded.
                </p>
                <button className="danger block" onClick={() => setRemoving(true)} disabled={!!busy}>
                  Remove the session
                </button>
              </Card>
            </>
          )}
        </>
      )}

      {removing && (
        <Sheet
          title="Remove the session?"
          subtitle="The service, the scripts and the VNC password go. The downloads stay where they are."
          onClose={() => setRemoving(false)}
        >
          <p className="sub">
            The browser profile is where the sites you signed into are still signed in. Keeping it
            means a session set up later picks up where this one left off.
          </p>
          <div className="actions">
            <button className="secondary" onClick={() => remove(false)} disabled={!!busy}>
              Keep the logins
            </button>
            <button className="danger" onClick={() => remove(true)} disabled={!!busy}>
              Delete them too
            </button>
          </div>
        </Sheet>
      )}
    </Page>
  )
}

/** Before anything is installed: what this is, what it costs, and one button. */
function Absent({
  session,
  host,
  sudo,
  size,
  onSize,
  page,
  onPage,
  busy,
  onSetup,
}: {
  session: RemoteSession
  host: string
  sudo: boolean
  size: string
  onSize: (size: string) => void
  page: string
  onPage: (page: string) => void
  busy: string | null
  onSetup: () => void
}) {
  const again = session.setup === 'ok' || session.setup === 'failed'
  return (
    <Card>
      <div className="title">{again ? 'Finish setting this up' : 'Open a browser on this host'}</div>
      <p className="sub">
        A browser runs on {host}, on a screen of its own, and you drive it from here. Sign in, click
        the download, and the file is on {host} — in {session.user}'s Downloads — rather than on your
        phone.
      </p>
      {session.missing && session.missing.length > 0 && (
        <p className="sub">
          Setting up installs {list(session.missing)} with apt. On a Raspberry Pi that is a few
          minutes and a few hundred megabytes, mostly the browser.
        </p>
      )}
      {session.snapBrowser && (
        <p className="sub">
          The {session.snapBrowser} here is a snap, or a wrapper for one, and cannot run in a
          session like this — it is walled out of the browser profile, and a service has no runtime
          directory for snapd to work in.
        </p>
      )}
      {session.brokenBrowser && (
        <p className="sub">
          The {session.brokenBrowser} here is installed and will not run: it cannot report even its
          own version.
        </p>
      )}
      {(session.snapBrowser || session.brokenBrowser) && (
        <p className="sub">Setting up fetches a browser that is an ordinary package instead.</p>
      )}
      {!sudo && (
        <Banner tone="warn">
          {session.user} needs passwordless sudo here first — apt and a unit file both want root.
          Set up access from the host's page.
        </Banner>
      )}

      <Field label="Screen size" help="The size the sites see. noVNC scales it to your phone.">
        <div className="chips">
          {SIZES.map((option) => (
            <button
              key={option}
              className={`chip ${option === size ? 'on' : ''}`}
              onClick={() => onSize(option)}
            >
              {option}
            </button>
          ))}
        </div>
      </Field>

      <SiteField page={page} onPage={onPage} />

      <button className="primary block" onClick={onSetup} disabled={!!busy || !sudo}>
        {busy === 'setup' ? 'Starting…' : again ? 'Try again' : 'Set up the session'}
      </button>
    </Card>
  )
}

/** While apt works. It says how long this takes and that leaving is fine,
 *  because the install is detached and outlives both this screen and the SSH
 *  session that started it. */
function Installing({ session }: { session: RemoteSession }) {
  return (
    <>
      <Banner tone="warn">
        Installing on the host — a few minutes on a Pi. It carries on if you leave this screen or
        lock your phone.
      </Banner>
      <SetupLog log={session.setupLog} />
    </>
  )
}

/** The tail of the install log, followed as it is written. What apt is doing
 *  now is at the bottom, so that is where this opens — unless somebody has
 *  scrolled up to read something, which is a decision to leave alone. */
function SetupLog({ log }: { log?: string }) {
  const element = useRef<HTMLPreElement>(null)
  const pinned = useRef(true)

  useEffect(() => {
    const node = element.current
    if (node && pinned.current) node.scrollTop = node.scrollHeight
  }, [log])

  if (!log) return null
  return (
    <pre
      className="log"
      ref={element}
      onScroll={() => {
        const node = element.current
        if (!node) return
        pinned.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40
      }}
    >
      {log}
    </pre>
  )
}

/** When it did not finish. The exit status and the end of the log are the two
 *  things worth having, and apt's own last line is usually the answer. */
function Failed({ session }: { session: RemoteSession }) {
  return (
    <>
      <Banner tone="bad">
        The install stopped{session.setupExit ? ` (exit ${session.setupExit})` : ''}. The end of its
        log is below — apt's last line is usually the reason.
      </Banner>
      <SetupLog log={session.setupLog} />
    </>
  )
}

/** Set up, not running: name a page and start it. */
function Idle({
  session,
  page,
  onPage,
  busy,
  onStart,
}: {
  session: RemoteSession
  page: string
  onPage: (page: string) => void
  busy: string | null
  onStart: () => void
}) {
  return (
    <Card>
      <div className="title">The session is off</div>
      <p className="sub">
        Starting it brings up the screen, {session.browser ?? 'the browser'} and the way in.
        Whatever you were signed into last time, you are still signed into.
      </p>
      <SiteField page={page} onPage={onPage} />
      <button className="primary block" onClick={onStart} disabled={!!busy}>
        {busy === 'start'
          ? session.stale
            ? 'Updating and starting…'
            : 'Starting…'
          : session.stale
            ? 'Update and start the session'
            : 'Start the session'}
      </button>
    </Card>
  )
}

/** Running: the way in, the password, and the way out. */
function Live({
  session,
  url,
  busy,
  onStop,
}: {
  session: RemoteSession
  url: string | null
  busy: string | null
  onStop: () => void
}) {
  return (
    <>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="title">The session is running</div>
            <div className="sub">
              {session.browser ?? 'A browser'} on a {session.geometry} screen
              {session.homepage ? `, opened at ${hostOf(session.homepage)}` : ''}
            </div>
          </div>
        </div>
        {url ? (
          <div className="actions">
            <button className="primary" onClick={() => openApp(url)}>
              <LaunchIcon />
              Open the session
            </button>
          </div>
        ) : (
          <Banner tone="warn">
            HostMan can't work out an address for this host, so there is no link to open.
          </Banner>
        )}
        <p className="sub" style={{ marginBottom: 0 }}>
          It opens outside HostMan, in your phone's browser. Turn the phone sideways: a desktop
          screen on a portrait phone is mostly scrolling. Downloads need no dialog — they go
          straight to {session.downloads ?? 'the host'}. A screen that is black with only a mouse
          pointer is the browser failing to start, and the journal below says why.
        </p>
      </Card>

      {session.password && (
        <Card>
          <div className="title">VNC password</div>
          <p className="sub" style={{ marginTop: 4 }}>
            The link carries it, so the session should connect on its own. This is for when it asks
            anyway.
          </p>
          <Copyable text={session.password} />
        </Card>
      )}

      <Card>
        <p className="sub" style={{ marginTop: 0 }}>
          Stopping closes the browser and takes the screen down. It does not sign you out — the
          profile keeps the logins for next time.
        </p>
        <button className="secondary block" onClick={onStop} disabled={!!busy}>
          {busy === 'stop' ? 'Stopping…' : 'Stop the session'}
        </button>
      </Card>
    </>
  )
}

/**
 * What the session is saying, while it is running or after it has failed.
 *
 * A session that comes up black — an X screen with nothing drawn on it — has
 * exactly one symptom and any number of causes, and the browser's own output is
 * what tells them apart. The Services screen has the same journal, but somebody
 * looking at an empty screen should not have to know that.
 */
function SessionLog({ hostId, unit }: { hostId: number; unit: string }) {
  const { data, error, reload } = useLoader(() => api.serviceLog(hostId, unit, 100), [hostId, unit])
  return (
    <>
      <SectionTitle>What it is saying</SectionTitle>
      <Card>
        <div className="row between" style={{ marginBottom: 10 }}>
          <span className="sub">The last of the session's journal</span>
          <button className="ghost" onClick={reload}>
            Refresh
          </button>
        </div>
        {error ? (
          <Banner tone="bad">{error}</Banner>
        ) : !data ? (
          <div className="sub">Reading it…</div>
        ) : data.content.trim() === '' ? (
          <div className="sub">Nothing yet.</div>
        ) : (
          <pre className="log">{data.content}</pre>
        )}
      </Card>
    </>
  )
}

/** What has been downloaded — the point of the exercise, so it is on the screen
 *  rather than left for somebody to go and look for in the file browser. */
function Downloads({ session, hostId }: { session: RemoteSession; hostId: number }) {
  if (!session.downloads || (!session.ready && session.files.length === 0)) return null
  return (
    <>
      <SectionTitle>Downloads</SectionTitle>
      <Card>
        {session.files.length === 0 ? (
          <div className="sub">Nothing here yet.</div>
        ) : (
          session.files.map((file) => (
            <div key={file.name} className="row between" style={{ padding: '6px 0' }}>
              <span className="grow" style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {file.name}
              </span>
              <span className="sub" style={{ flex: 'none', marginLeft: 10 }}>
                {bytes(file.size)} · {ago(new Date(Date.now() - file.ageS * 1000).toISOString())}
              </span>
            </div>
          ))
        )}
      </Card>
      <Link
        className="card"
        to={`/hosts/${hostId}/files?path=${encodeURIComponent(session.downloads)}`}
      >
        <div className="row between">
          <div className="grow">
            <div className="title">Open in Files</div>
            <div className="sub mono">{session.downloads}</div>
          </div>
          <span className="chevron">›</span>
        </div>
      </Link>
    </>
  )
}

/** The address bar, moved out of the session and onto this screen. */
function SiteField({ page, onPage }: { page: string; onPage: (page: string) => void }) {
  return (
    <Field label="Site to open" help="Left empty, the session opens wherever it did last.">
      <input
        type="url"
        inputMode="url"
        value={page}
        onChange={(e) => onPage(e.target.value)}
        placeholder="https://example.com/login"
        autoCapitalize="off"
        autoCorrect="off"
        autoComplete="off"
        spellCheck={false}
      />
    </Field>
  )
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="row between" style={{ padding: '5px 0' }}>
      <span className="sub">{label}</span>
      <span style={{ fontSize: 14 }}>{value}</span>
    </div>
  )
}

/** "a, b and c" — a list somebody would read out, rather than one with a comma
 *  where the "and" belongs. */
function list(items: string[]): string {
  if (items.length < 2) return items.join('')
  return `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`
}

/** A URL is too long for a phone-width subtitle, and the host is the part that
 *  says which site it is. */
function hostOf(address: string): string {
  try {
    return new URL(address).host
  } catch {
    return address
  }
}
