import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import {
  Badge,
  Banner,
  Card,
  Copyable,
  Field,
  Loading,
  Meter,
  SectionTitle,
  Sheet,
  useLoader,
} from '../components/ui'
import { bytes, uptime } from '../lib/format'
import type { Torrent, TorrentDaemon, TorrentSeeding } from '../types'

/** What a host without deluge needs run on it once. HostMan will not do it: a
 *  BitTorrent client is a decision about what a machine does on a network, and
 *  on plenty of machines somebody has already made it. */
const INSTALL = 'sudo apt install -y deluged deluge-console'

/** The largest file worth carrying to a host, matching what the server will
 *  accept. A torrent file is a list of hashes: a few kilobytes is ordinary and
 *  a megabyte is a very large one. */
const MAX_TORRENT_BYTES = 4 * 1024 * 1024

/** How often to ask while something is downloading. Every other host screen
 *  answers when you ask it to, because each answer is an SSH session — but a
 *  progress bar that only moved when you tapped it would not be a progress
 *  bar. */
const BUSY_POLL_MS = 5000

/** And how often while the daemon is up but nothing is downloading — seeding,
 *  paused, waiting. The numbers still move, so the screen should still follow
 *  them, but a torrent that has been seeding for a week does not need a Pi
 *  woken every five seconds to say so. */
const CALM_POLL_MS = 15000

/** The ratios worth a tap. Zero is deluge's own default: seed until told to
 *  stop. Two is the number a private tracker usually asks for. */
const RATIOS = [0, 1, 2, 5]

/** The torrent limits worth a tap. -1 is deluge's own word for no limit at
 *  all. Its defaults are small — three downloading at once — which reads as a
 *  stuck download the first time a fourth torrent sits at 0%. */
const LIMITS = [3, 5, 10, 20, -1]

/** The states that mean the numbers are changing quickly. Seeding is not among
 *  them on purpose: it changes slowly, so it is watched slowly. */
const MOVING = ['downloading', 'checking', 'moving', 'allocating']

/**
 * Torrents: deluge on the host, driven from the phone.
 *
 * The whole point is where the files end up. A torrent opened on a phone
 * downloads to the phone, over the phone's connection, onto the phone's
 * storage, and then has to be moved to the machine that was always supposed to
 * have it. Handing the .torrent file to the host instead skips all of that: the
 * host has the disk, the wired connection and the uptime, and the phone is only
 * ever the remote control.
 *
 * So the screen is laid out as the sequence rather than as a control panel:
 * hand it a torrent at the top, watch the bars underneath, and everything about
 * the daemon itself is below both because it is not what anybody came here for.
 */
export default function HostTorrents() {
  const { id } = useParams()
  const hostId = Number(id)

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  const [pace, setPace] = useState<number | undefined>(undefined)
  const {
    data: daemon,
    error,
    offline,
    reload,
  } = useLoader(() => api.torrents(hostId), [hostId], pace)

  const [busy, setBusy] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [removing, setRemoving] = useState<Torrent | null>(null)
  const [dropping, setDropping] = useState(false)
  const [folder, setFolder] = useState('')

  /**
   * The last list deluge actually gave.
   *
   * A probe that could not ask — the daemon busy, deluge slow to start, the
   * command cut short — comes back with no torrents, and showing that as
   * "nothing is downloading" would take a running download off the screen and
   * make somebody go back and return to see it again. So an answer that did not
   * reach deluge leaves the last one on the screen, and says it is the last
   * one.
   */
  const known = useRef<Torrent[]>([])
  useEffect(() => {
    known.current = []
  }, [hostId])
  useEffect(() => {
    if (daemon?.asked) known.current = daemon.torrents
  }, [daemon])
  const torrents = daemon?.asked ? daemon.torrents : known.current
  const stale = !!daemon && !daemon.asked && known.current.length > 0

  // A phone that is locked or on another app should not be asking a Pi
  // anything, and a phone coming back should not have to be told to refresh:
  // stopping and starting the timer does both, because starting it asks at
  // once.
  const visible = useVisible()
  const moving = torrents.some((t) => MOVING.includes(t.state.toLowerCase()))

  // Following it while it is only seeding is the point of the slower pace: the
  // ratio and the upload figure are what somebody is watching then, and they
  // are still moving after the download has finished.
  useEffect(() => {
    if (!visible || !daemon?.running) {
      setPace(undefined)
      return
    }
    setPace(moving ? BUSY_POLL_MS : CALM_POLL_MS)
  }, [visible, daemon?.running, moving])

  // The folder field opens on the folder the host named — the one it is using,
  // or, before setup, the one it would use. It follows the host rather than
  // every render, so a field cleared to type a new path stays cleared.
  useEffect(() => {
    if (daemon?.downloads) setFolder((current) => current || daemon.downloads)
  }, [daemon?.downloads])

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

  const setup = () => act('setup', () => api.setupTorrents(hostId, { downloads: folder }))
  const start = () => act('start', () => api.torrentAction(hostId, 'start'))
  const stop = () => act('stop', () => api.torrentAction(hostId, 'stop'))
  const add = (input: { source?: string; file?: string; name?: string }) =>
    act('add', () => api.addTorrent(hostId, input))
  const remove = (torrent: Torrent, data: boolean) =>
    act('remove', async () => {
      await api.torrentAction(hostId, 'remove', torrent.id, data)
      setRemoving(null)
    })
  const drop = () =>
    act('drop', async () => {
      await api.removeTorrents(hostId)
      setDropping(false)
    })

  return (
    <Page title="Torrents" back={`/hosts/${hostId}`}>
      <Loading error={error} offline={offline} hasData={!!daemon} />
      {failure && <Banner tone="bad">{failure}</Banner>}

      {!daemon ? (
        <Card>
          <div className="sub">Asking {host?.name ?? 'the host'} about deluge…</div>
        </Card>
      ) : !daemon.installed ? (
        <NotInstalled daemon={daemon} host={host?.name ?? 'this host'} onRefresh={reload} />
      ) : !daemon.configured ? (
        <SetUp
          host={host?.name ?? 'this host'}
          user={daemon.user}
          sudo={host?.sudoOk !== false}
          folder={folder}
          onFolder={setFolder}
          busy={busy}
          onSetup={setup}
        />
      ) : (
        <>
          {daemon.stale && (
            <Card>
              <Banner tone="warn">
                This host is running a unit an older HostMan wrote. Updating HostMan does not
                reach back and rewrite it — setting the downloader up again does.
              </Banner>
              <p className="sub" style={{ marginTop: 0 }}>
                Nothing is lost by it: deluge keeps its state, and everything downloading carries
                on from where it is.
              </p>
              <button className="secondary block" onClick={setup} disabled={!!busy}>
                {busy === 'setup' ? 'Setting up…' : 'Set it up again'}
              </button>
            </Card>
          )}

          <AddTorrent daemon={daemon} busy={busy} onAdd={add} onFail={setFailure} />

          {daemon.trouble && !stale && (
            <Banner tone="bad">
              Deluge is set up here but would not answer: {daemon.trouble}. Its journal is on the{' '}
              <Link to={`/hosts/${hostId}/service?name=${encodeURIComponent(daemon.unit)}`}>
                Services
              </Link>{' '}
              screen.
            </Banner>
          )}

          <Torrents
            daemon={daemon}
            torrents={torrents}
            stale={stale}
            busy={busy}
            onPause={(t) => act('pause', () => api.torrentAction(hostId, 'pause', t.id))}
            onResume={(t) => act('resume', () => api.torrentAction(hostId, 'resume', t.id))}
            onRemove={setRemoving}
          />

          <Where daemon={daemon} hostId={hostId} />

          <SectionTitle>The downloader</SectionTitle>
          <Card>
            <div className="row between" style={{ marginBottom: 10 }}>
              <div className="grow">
                <div className="title">
                  {daemon.running ? 'Running' : 'Stopped'}
                  {daemon.active === 'failed' ? ' — it failed' : ''}
                </div>
                <div className="sub">
                  deluged {daemon.version ?? ''} as {daemon.user}
                  {daemon.enabled ? ', back after a reboot' : ''}
                </div>
              </div>
              <Badge tone={daemon.running ? 'good' : daemon.active === 'failed' ? 'bad' : 'neutral'} dot>
                {daemon.sub ?? (daemon.running ? 'running' : 'stopped')}
              </Badge>
            </div>
            <p className="sub" style={{ marginTop: 0 }}>
              Stopping it pauses everything; deluge writes down where each torrent got to and picks
              it up again from there. Adding a torrent starts it if it is off, so there is usually
              no reason to touch this.
            </p>
            <button
              className="secondary block"
              onClick={daemon.running ? stop : start}
              disabled={!!busy}
            >
              {busy === 'start' || busy === 'stop'
                ? 'Asking systemd…'
                : daemon.running
                  ? 'Stop the downloader'
                  : 'Start the downloader'}
            </button>
            <p className="sub" style={{ marginBottom: 0 }}>
              It is an ordinary service, so its log is on the{' '}
              <Link to={`/hosts/${hostId}/service?name=${encodeURIComponent(daemon.unit)}`}>
                Services
              </Link>{' '}
              screen.
            </p>
          </Card>

          <SectionTitle>Settings</SectionTitle>
          <SeedingRule
            seeding={daemon.seeding}
            busy={busy}
            onSave={(ratio, removeIt) =>
              act('seeding', () => api.torrentSeeding(hostId, ratio, removeIt))
            }
          />
          <ActiveLimit
            limit={daemon.activeLimit ?? 0}
            busy={busy}
            onSave={(limit) => act('limit', () => api.torrentLimit(hostId, limit))}
          />
          <Card>
            <FolderField folder={folder} onFolder={setFolder} />
            <p className="sub" style={{ marginTop: 0 }}>
              Changing it is where the next torrent goes; the ones already downloading stay where
              they were put. Writing the downloader again is also how a host takes a newer
              HostMan's unit.
            </p>
            <button className="secondary block" onClick={setup} disabled={!!busy}>
              {busy === 'setup' ? 'Saving…' : 'Save and write it again'}
            </button>
          </Card>

          <SectionTitle>Danger zone</SectionTitle>
          <Card>
            <p className="sub" style={{ marginTop: 0 }}>
              Removing takes the service and deluge's own state off {host?.name ?? 'the host'}, and
              with it the list of what was downloading. Deluge stays installed, and every file
              already downloaded stays exactly where it is.
            </p>
            <button className="danger block" onClick={() => setDropping(true)} disabled={!!busy}>
              Remove the downloader
            </button>
          </Card>
        </>
      )}

      {removing && (
        <Sheet
          title="Remove this torrent?"
          subtitle={removing.name}
          onClose={() => setRemoving(null)}
        >
          <p className="sub">
            Deluge stops working on it either way. What is being asked is whether the part already
            downloaded goes too — {bytes(removing.done)} of it — which cannot be undone.
          </p>
          <p className="sub">
            It takes a few seconds: deluge's own client does the work and then refuses to exit, so
            HostMan waits it out and asks for the list again rather than taking its word for it.
          </p>
          <div className="actions">
            <button className="secondary" onClick={() => remove(removing, false)} disabled={!!busy}>
              {busy === 'remove' ? 'Removing…' : 'Keep the files'}
            </button>
            <button className="danger" onClick={() => remove(removing, true)} disabled={!!busy}>
              {busy === 'remove' ? 'Removing…' : 'Delete them too'}
            </button>
          </div>
        </Sheet>
      )}

      {dropping && (
        <Sheet
          title="Remove the downloader?"
          subtitle="The service and deluge's state. Nothing that was downloaded."
          onClose={() => setDropping(false)}
        >
          <p className="sub">
            Anything still downloading stops and is forgotten — a half-finished file stays on the
            disk, but nothing will carry on with it. Setting the downloader up again later starts
            from an empty list.
          </p>
          <div className="actions">
            <button className="secondary" onClick={() => setDropping(false)} disabled={!!busy}>
              Keep it
            </button>
            <button className="danger" onClick={drop} disabled={!!busy}>
              {busy === 'drop' ? 'Removing…' : 'Remove it'}
            </button>
          </div>
        </Sheet>
      )}
    </Page>
  )
}

/**
 * Whether the page is the one being looked at.
 *
 * A phone locked, or on another app, should not be asking a Raspberry Pi
 * anything — and a phone coming back should not have to be told to refresh.
 * Both fall out of stopping the timer when the page is hidden, because
 * starting it again asks straight away.
 */
function useVisible(): boolean {
  const [visible, setVisible] = useState(!document.hidden)
  useEffect(() => {
    const onChange = () => setVisible(!document.hidden)
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [])
  return visible
}

/** Deluge is the one thing on this screen HostMan will not install, so this is
 *  the whole of the screen until somebody has. It says the line to run rather
 *  than describing it: the answer to "what do I type" should be copyable. */
function NotInstalled({
  daemon,
  host,
  onRefresh,
}: {
  daemon: TorrentDaemon
  host: string
  onRefresh: () => void
}) {
  return (
    <Card>
      <div className="title">Deluge is not installed on {host}</div>
      <p className="sub">
        HostMan drives deluge rather than shipping one: a BitTorrent client is a decision about
        what a machine does on a network, and it is not HostMan's to make on your behalf. Run this
        on {host} once — on the Terminal screen, or over SSH — and come back.
      </p>
      <Copyable text={INSTALL} />
      <p className="sub">
        {daemon.missing && daemon.missing.length === 1
          ? `Only ${daemon.missing[0]} is missing, but installing both is the same command.`
          : 'The daemon does the downloading; the console is how HostMan talks to it.'}
      </p>
      <button className="secondary block" onClick={onRefresh}>
        Look again
      </button>
    </Card>
  )
}

/** Deluge is there and HostMan's daemon is not written yet. One field and one
 *  button: where the files go, and go. */
function SetUp({
  host,
  user,
  sudo,
  folder,
  onFolder,
  busy,
  onSetup,
}: {
  host: string
  user: string
  sudo: boolean
  folder: string
  onFolder: (folder: string) => void
  busy: string | null
  onSetup: () => void
}) {
  return (
    <Card>
      <div className="title">Download torrents on {host}</div>
      <p className="sub">
        Deluge is installed here. Setting up gives it a service of its own, running as {user}, on a
        port of its own — so a deluge somebody already runs on this machine carries on untouched,
        with its own torrents.
      </p>
      <FolderField folder={folder} onFolder={onFolder} />
      {!sudo && (
        <Banner tone="warn">
          {user} needs passwordless sudo here first — a systemd unit wants root. Set up access from
          the host's page.
        </Banner>
      )}
      <p className="sub">
        The service is enabled, so a machine that restarts in the middle of a download carries on
        with it afterwards.
      </p>
      <button className="primary block" onClick={onSetup} disabled={!!busy || !sudo}>
        {busy === 'setup' ? 'Setting up…' : 'Set up the downloader'}
      </button>
    </Card>
  )
}

/** The top of the screen, and the reason for it: hand the host a torrent.
 *
 * Both ways of naming one are here because a phone has both. A magnet link is
 * something you copied, and pasting it is the whole of the interaction. A
 * .torrent file is something you were given — mailed, or downloaded by the
 * browser on the host itself — and picking it hands its bytes over as they are,
 * rather than asking the host to go and fetch a link that may need a login.
 */
function AddTorrent({
  daemon,
  busy,
  onAdd,
  onFail,
}: {
  daemon: TorrentDaemon
  busy: string | null
  onAdd: (input: { source?: string; file?: string; name?: string }) => void
  onFail: (message: string) => void
}) {
  const [source, setSource] = useState('')
  const picker = useRef<HTMLInputElement>(null)

  const addLink = () => {
    onAdd({ source: source.trim() })
    setSource('')
  }

  const addFile = async (file: File) => {
    // A picker that still holds last time's file will not fire for the same one
    // again, and picking the same torrent twice is a reasonable thing to do.
    const done = () => {
      if (picker.current) picker.current.value = ''
    }
    // Nothing that size is a torrent file, and a phone should not spend a
    // minute uploading a video to be told so by the host.
    if (file.size > MAX_TORRENT_BYTES) {
      onFail(`${file.name} is too large to be a .torrent file.`)
      done()
      return
    }
    let picked: { text: string; base64: string }
    try {
      picked = await readFile(file)
    } catch {
      onFail(`${file.name} could not be read.`)
      done()
      return
    }
    if (!looksLikeATorrent(picked.text)) {
      onFail(`${file.name} is not a .torrent file.`)
      done()
      return
    }
    onAdd({ file: picked.base64, name: file.name })
    done()
  }

  return (
    <Card>
      <div className="title">Add a torrent</div>
      <Field
        label="Magnet link or .torrent address"
        help={`It downloads onto ${daemon.downloads}, on the host — not onto this phone.`}
      >
        <textarea
          value={source}
          onChange={(e) => setSource(e.target.value)}
          placeholder="magnet:?xt=urn:btih:… or https://…/thing.torrent"
          rows={2}
          autoCapitalize="off"
          autoCorrect="off"
          autoComplete="off"
          spellCheck={false}
        />
      </Field>
      <div className="actions" style={{ marginTop: 0 }}>
        <button className="primary" onClick={addLink} disabled={!!busy || source.trim() === ''}>
          {busy === 'add' ? 'Adding…' : 'Add'}
        </button>
        <button className="secondary" onClick={() => picker.current?.click()} disabled={!!busy}>
          Pick a file
        </button>
      </div>
      {/* No accept list on purpose. It is the obvious thing to write and it
          makes the picker useless: iOS Files and Android's picker both filter
          by the type the system knows a file as, ".torrent" maps to nothing
          either of them has heard of, and the result is a picker where every
          file is greyed out and the one you want cannot be chosen. So anything
          can be picked, and what it is is decided by reading it. */}
      <input
        ref={picker}
        type="file"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0]
          if (file) void addFile(file)
        }}
      />
    </Card>
  )
}

/** What the host is working on. The bar is the point of the screen, so it is
 *  the widest thing on the card and everything else is a line under it. */
function Torrents({
  daemon,
  torrents,
  stale,
  busy,
  onPause,
  onResume,
  onRemove,
}: {
  daemon: TorrentDaemon
  /** What deluge last said, which is not always what it said this time. */
  torrents: Torrent[]
  /** True where this is the last answer rather than a fresh one. */
  stale: boolean
  busy: string | null
  onPause: (torrent: Torrent) => void
  onResume: (torrent: Torrent) => void
  onRemove: (torrent: Torrent) => void
}) {
  if (torrents.length === 0) {
    return (
      <Card>
        <div className="sub">
          {daemon.running
            ? 'Nothing is downloading. Paste a magnet link above, or pick a .torrent file.'
            : 'Nothing is downloading, and the daemon is stopped. Adding a torrent starts it.'}
        </div>
      </Card>
    )
  }
  return (
    <>
      <SectionTitle>
        {torrents.length} {torrents.length === 1 ? 'torrent' : 'torrents'}
      </SectionTitle>
      {stale && (
        <Banner tone="warn">
          {daemon.running
            ? 'Deluge did not answer just now — this is what it last said.'
            : 'The downloader is stopped. This is what it was working on.'}
        </Banner>
      )}
      {torrents.map((torrent) => (
        <TorrentCard
          key={torrent.id}
          torrent={torrent}
          folder={daemon.downloads}
          busy={busy}
          onPause={() => onPause(torrent)}
          onResume={() => onResume(torrent)}
          onRemove={() => onRemove(torrent)}
        />
      ))}
    </>
  )
}

function TorrentCard({
  torrent,
  folder,
  busy,
  onPause,
  onResume,
  onRemove,
}: {
  torrent: Torrent
  /** The folder the downloader is using now, so a torrent that was added when
   *  it was something else is the only one that has to name its own. */
  folder: string
  busy: string | null
  onPause: () => void
  onResume: () => void
  onRemove: () => void
}) {
  const state = torrent.state.toLowerCase()
  const paused = state === 'paused'
  const done = torrent.progress >= 100
  return (
    <Card>
      <div className="row between" style={{ marginBottom: 8 }}>
        <div className="grow" style={{ overflow: 'hidden' }}>
          <div className="title truncate">{torrent.name}</div>
          <div className="sub">
            {bytes(torrent.done)} of {bytes(torrent.size)}
            {torrent.ratio ? ` · ratio ${torrent.ratio.toFixed(2)}` : ''}
          </div>
          {torrent.folder && torrent.folder !== folder && (
            <div className="sub mono truncate">{torrent.folder}</div>
          )}
        </div>
        <Badge tone={tone(state)} dot pulse={state === 'downloading'}>
          {torrent.state}
        </Badge>
      </div>

      <Meter
        label={
          state === 'downloading'
            ? `${bytes(torrent.down)}/s down`
            : done
              ? 'Finished'
              : torrent.state
        }
        value={torrent.progress}
        display={`${torrent.progress.toFixed(1)}%`}
        tone="progress"
      />

      <div className="stats pairs">
        <Stat label="Left" value={left(torrent)} />
        <Stat label="Up" value={`${bytes(torrent.up)}/s`} />
        <Stat label="Seeds" value={`${torrent.seeds} of ${torrent.seedsTotal}`} />
        <Stat label="Peers" value={`${torrent.peers} of ${torrent.peersTotal}`} />
      </div>

      <div className="actions">
        <button className="secondary" onClick={paused ? onResume : onPause} disabled={!!busy}>
          {paused ? 'Resume' : 'Pause'}
        </button>
        <button className="danger" onClick={onRemove} disabled={!!busy}>
          Remove
        </button>
      </div>
    </Card>
  )
}

/**
 * What becomes of a torrent once it has finished downloading.
 *
 * Left alone deluge seeds for ever, which is the polite thing to do and is also
 * how a list nobody is watching fills up with things that finished last month.
 * A ratio is what a torrent has to upload against what it downloaded, and it is
 * the honest way to say "seed it for a while": a slow torrent gets longer than
 * a fast one, which is the same fairness a stopwatch would get wrong.
 *
 * It is deluge's own setting rather than a rule HostMan enforces, so it holds
 * when nobody is looking at this screen — which is when torrents finish.
 */
function SeedingRule({
  seeding,
  busy,
  onSave,
}: {
  seeding: TorrentSeeding
  busy: string | null
  onSave: (ratio: number, remove: boolean) => void
}) {
  const [ratio, setRatio] = useState(seeding.ratio)
  const [removeIt, setRemoveIt] = useState(seeding.remove)

  // The controls follow the host rather than every render, so a rule being
  // chosen is not snatched back by the next poll.
  useEffect(() => {
    setRatio(seeding.ratio)
    setRemoveIt(seeding.remove)
  }, [seeding.ratio, seeding.remove])

  const changed = ratio !== seeding.ratio || removeIt !== seeding.remove
  return (
    <Card>
      <Field
        label="When a torrent has finished"
        help="A ratio is what it uploads against what it downloaded — 2.0 is twice over."
      >
        <div className="chips">
          {RATIOS.map((option) => (
            <button
              key={option}
              className={`chip ${option === ratio ? 'on' : ''}`}
              onClick={() => {
                setRatio(option)
                if (option === 0) setRemoveIt(false)
              }}
            >
              {option === 0 ? 'Keep seeding' : `At ${option.toFixed(1)}`}
            </button>
          ))}
        </div>
      </Field>
      <label className="checkbox">
        <input
          type="checkbox"
          checked={removeIt}
          disabled={ratio === 0}
          onChange={(e) => setRemoveIt(e.target.checked)}
        />
        Take the entry out of the list too
      </label>
      <p className="sub">
        {ratio === 0
          ? 'It seeds until you stop it, and stays in the list.'
          : removeIt
            ? `At ${ratio.toFixed(1)} deluge stops seeding and drops the torrent from the list. The downloaded files stay on the host — it removes the entry, never the download.`
            : `At ${ratio.toFixed(1)} deluge stops seeding and leaves the torrent in the list, paused.`}
      </p>
      <button className="secondary block" onClick={() => onSave(ratio, removeIt)} disabled={!!busy || !changed}>
        {busy === 'seeding' ? 'Telling deluge…' : changed ? 'Save the rule' : 'Saved'}
      </button>
    </Card>
  )
}

/**
 * How many torrents deluge works on at once. The rest wait as Queued until a
 * slot opens, which is deluge's queue doing what queues do — but its defaults
 * are small, and a torrent sitting at 0% under a limit nobody chose reads as
 * a download that is stuck rather than one that is waiting its turn.
 *
 * It is one number over deluge's three: HostMan sets the total and both
 * per-kind limits to it, so the knob means what it says — this many at once,
 * whatever they are doing.
 */
function ActiveLimit({
  limit,
  busy,
  onSave,
}: {
  /** What deluge last said, or 0 where it could not be asked. */
  limit: number
  busy: string | null
  onSave: (limit: number) => void
}) {
  const [choice, setChoice] = useState(limit)

  // The control follows the host rather than every render, so a limit being
  // chosen is not snatched back by the next poll.
  useEffect(() => {
    setChoice(limit)
  }, [limit])

  const changed = choice !== limit && choice !== 0
  return (
    <Card>
      <Field
        label="Torrents at once"
        help="Everything past this waits as Queued until one finishes or is paused."
      >
        <div className="chips">
          {LIMITS.map((option) => (
            <button
              key={option}
              className={`chip ${option === choice ? 'on' : ''}`}
              onClick={() => setChoice(option)}
            >
              {option === -1 ? 'No limit' : option}
            </button>
          ))}
        </div>
      </Field>
      <p className="sub">
        {choice === -1
          ? 'Deluge works on everything it is given, all at the same time.'
          : choice === 0
            ? 'Deluge has not said what its limit is — it answers when the daemon is running.'
            : `Deluge works on up to ${choice} at once, downloading or seeding.`}
      </p>
      <button
        className="secondary block"
        onClick={() => onSave(choice)}
        disabled={!!busy || !changed}
      >
        {busy === 'limit' ? 'Telling deluge…' : changed ? 'Save the limit' : 'Saved'}
      </button>
    </Card>
  )
}

/** Where the files are, and whether there is room for them. A torrent that
 *  fills a Pi's card is the ordinary way this goes wrong, so the figure is on
 *  the screen before the download rather than after it. */
function Where({ daemon, hostId }: { daemon: TorrentDaemon; hostId: number }) {
  const tight = daemon.capacity ? (daemon.free ?? 0) / daemon.capacity < 0.1 : false
  return (
    <>
      <SectionTitle>Where they land</SectionTitle>
      {daemon.capacity ? (
        <Card>
          <Meter
            label="Disk"
            used={daemon.capacity - (daemon.free ?? 0)}
            total={daemon.capacity}
            display={`${bytes(daemon.free ?? 0)} free`}
          />
          {tight && (
            <p className="sub" style={{ marginBottom: 0 }}>
              There is not much room left on that disk. Deluge will keep going until there is none.
            </p>
          )}
        </Card>
      ) : null}
      <Link
        className="card"
        to={`/hosts/${hostId}/files?path=${encodeURIComponent(daemon.downloads)}`}
      >
        <div className="row between">
          <div className="grow">
            <div className="title">Open in Files</div>
            <div className="sub mono truncate">{daemon.downloads}</div>
          </div>
          <span className="chevron">›</span>
        </div>
      </Link>
    </>
  )
}

function FolderField({
  folder,
  onFolder,
}: {
  folder: string
  onFolder: (folder: string) => void
}) {
  return (
    <Field label="Download folder" help="An absolute path on the host. It is created if it isn't there.">
      <input
        value={folder}
        onChange={(e) => onFolder(e.target.value)}
        placeholder="/home/pi/Downloads/torrents"
        autoCapitalize="off"
        autoCorrect="off"
        autoComplete="off"
        spellCheck={false}
      />
    </Field>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="k">{label}</div>
      <div className="v">{value}</div>
    </div>
  )
}

/** How long is left, in the words that are true: deluge's own where it has
 *  them, nothing where it is not saying, and never a zero pretending to be an
 *  estimate. */
function left(torrent: Torrent): string {
  if (torrent.progress >= 100) return 'Done'
  if (torrent.eta) return uptime(torrent.eta)
  return torrent.etaText && torrent.etaText !== '0s' ? torrent.etaText : '—'
}

function tone(state: string): 'good' | 'warn' | 'bad' | 'neutral' | 'accent' {
  if (state === 'downloading') return 'accent'
  if (state === 'seeding') return 'good'
  if (state === 'error') return 'bad'
  if (state === 'paused' || state === 'queued') return 'warn'
  return 'neutral'
}

/**
 * A picked file, read once: its bytes as a string for the check above, and the
 * base64 that goes in the request body.
 *
 * The encoding is done a chunk at a time because String.fromCharCode is given
 * the bytes as arguments, and a megabyte of them in one call is how a browser
 * is made to throw "too many arguments" on a file that is otherwise perfectly
 * ordinary.
 */
async function readFile(file: File): Promise<{ text: string; base64: string }> {
  const bytes = new Uint8Array(await file.arrayBuffer())
  const chunk = 8192
  let binary = ''
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return { text: binary, base64: btoa(binary) }
}

/**
 * The same rule the server applies: a torrent file is a bencoded dictionary
 * with an info dictionary inside it.
 *
 * Asking here as well is not distrust of the server — it still refuses
 * everything this does. It is that the picker now offers every file on the
 * phone, so picking the wrong one is easy, and being told which file it was
 * beats a round trip to the host to be told the same thing about none.
 */
function looksLikeATorrent(text: string): boolean {
  return text.startsWith('d') && text.includes('4:info')
}
