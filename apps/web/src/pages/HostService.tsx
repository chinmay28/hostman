import { useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { BootBadge, ServiceBadge } from '../components/status'
import { Banner, Card, Loading, SectionTitle, Sheet, useLoader } from '../components/ui'
import { bytes, serviceName, uptime } from '../lib/format'
import type { ServiceAction, ServiceUnit } from '../types'

/** How much journal to fetch. The first is what the page opens on; the rest are
 *  a tap away, because the answer is usually in the last twenty lines and
 *  occasionally a thousand back. */
const LOG_LINES = [50, 200, 1000]

/** Stopping something is worth a second tap; starting it is not. Disabling is
 *  the one that bites later — a service that no longer comes back after a
 *  reboot fails silently, months from now. */
type Confirmation = { title: (what: string) => string; body: string; verb: string }

const CONFIRM: Partial<Record<ServiceAction, Confirmation>> = {
  stop: {
    title: (what) => `Stop ${what}?`,
    body: 'It stays stopped until something starts it again — a reboot, if it is enabled, or you.',
    verb: 'Stop',
  },
  disable: {
    title: () => 'Stop starting this at boot?',
    body: 'It keeps running now, and will not come back on its own the next time the machine restarts.',
    verb: 'Disable',
  },
}

/**
 * One systemd service or timer: what it is doing, the buttons that change that,
 * the tail of its journal, and its unit file.
 *
 * A timer borrows this screen rather than getting one of its own. It is the
 * same unit file, the same journal and the same buttons; what differs is that
 * it runs nothing itself, so where a service has memory and a PID it has a
 * schedule and the name of the unit it starts.
 *
 * Saving the unit file runs `systemctl daemon-reload` straight after, because a
 * unit file edited and not reloaded is a change that has not happened — the
 * single most common way an edit from a phone appears to do nothing.
 */
export default function HostService() {
  const { id } = useParams()
  const hostId = Number(id)
  const [params] = useSearchParams()
  const name = params.get('name') ?? ''
  const navigate = useNavigate()

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  const {
    data: unit,
    error,
    offline,
    reload,
  } = useLoader(() => api.service(hostId, name), [hostId, name])

  const [busy, setBusy] = useState<ServiceAction | null>(null)
  const [confirm, setConfirm] = useState<ServiceAction | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const [lines, setLines] = useState(LOG_LINES[0])
  const {
    data: log,
    error: logError,
    loading: logLoading,
    reload: reloadLog,
  } = useLoader(() => api.serviceLog(hostId, name, lines), [hostId, name, lines])

  const short = serviceName(name)
  // Every noun on this screen was written for a service. A timer borrows the
  // same screen, and the words have to follow it.
  const kind = unit?.timer ? 'timer' : 'service'

  // systemctl waits for the service, and HostMan waits for systemctl: a unit
  // that takes half a minute to come up takes half a minute to answer.
  const run = async (action: ServiceAction) => {
    setBusy(action)
    setConfirm(null)
    setNotice(null)
    setFailure(null)
    try {
      await api.serviceAction(hostId, name, action)
      setNotice(`${DONE[action]} ${short}.`)
      reload()
      reloadLog()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const ask = (action: ServiceAction) => (CONFIRM[action] ? setConfirm(action) : run(action))

  const remove = async () => {
    setDeleting(true)
    setFailure(null)
    try {
      await api.deleteService(hostId, name)
      navigate(`/hosts/${hostId}/services`, { replace: true })
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
      setDeleting(false)
      setConfirmDelete(false)
    }
  }

  const active = unit?.active === 'active' || unit?.active === 'reloading'
  const usable = !!unit && !unit.template && unit.load !== 'not-found' && unit.load !== 'masked'
  const working = busy !== null

  return (
    <Page
      title={short || 'Service'}
      back={`/hosts/${hostId}/services`}
      action={
        <button className="ghost" onClick={reload} disabled={working}>
          Refresh
        </button>
      }
    >
      <Loading error={error} offline={offline} hasData={!!unit} />
      {failure && <Banner tone="bad">{failure}</Banner>}
      {notice && <Banner tone="good">{notice}</Banner>}

      {!unit && !error && (
        <Card>
          <div className="sub">Asking {host?.name ?? 'the host'} about {name}…</div>
        </Card>
      )}

      {unit && (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">{unit.name}</div>
                <div className="sub">{unit.description || 'No description in the unit file'}</div>
              </div>
              <ServiceBadge unit={unit} />
            </div>

            <div className="row" style={{ gap: 6, marginTop: 10, flexWrap: 'wrap' }}>
              <BootBadge unit={unit} />
            </div>

            {/* A timer runs nothing, so it has no memory figure and no PID.
                What it has is a schedule, which is the only reason to open
                one. */}
            {unit.timer ? (
              <div className="stats">
                <Stat label="Next run" value={nextRun(unit)} />
                <Stat label="Last run" value={unit.lastS ? `${uptime(unit.lastS)} ago` : 'Never'} />
                <Stat
                  label={active ? 'Waiting for' : 'Stopped for'}
                  value={unit.sinceS > 0 ? uptime(unit.sinceS) : '—'}
                />
              </div>
            ) : (
              <>
                {/* Four figures, each in its own tile. Restarts used to take
                    the PID's place whenever it was above zero, which hid the
                    PID on exactly the services worth looking one up for. */}
                <div className="stats pairs">
                  <Stat
                    label={active ? 'Running for' : 'Stopped for'}
                    value={unit.sinceS > 0 ? uptime(unit.sinceS) : '—'}
                  />
                  <Stat
                    label="Memory"
                    value={unit.memory > 0 ? `${unit.memoryFrom === 'rss' ? '≈' : ''}${bytes(unit.memory)}` : '—'}
                  />
                  <Stat label="Main PID" value={unit.mainPid > 0 ? String(unit.mainPid) : '—'} />
                  <Stat label="Restarts" value={String(unit.restarts)} />
                </div>
                {figureNotes(unit, active).map((note) => (
                  <p key={note} className="sub" style={{ marginTop: 8, marginBottom: 0 }}>
                    {note}
                  </p>
                ))}
              </>
            )}

            {/* The other half of the pair. A timer on its own says when and
                never what, so the unit it starts is a tap away. */}
            {unit.timer && unit.triggers && (
              <div style={{ marginTop: 12 }}>
                <div className="sub">Starts this unit:</div>
                <div className="unit-refs">
                  <UnitRef hostId={hostId} name={unit.triggers} />
                </div>
              </div>
            )}

            {unit.active === 'failed' && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="bad">
                  {short} failed{unit.result && unit.result !== 'success' ? ` (${unit.result})` : ''}.
                  The last lines of its journal are below.
                </Banner>
              </div>
            )}
            {unit.load === 'not-found' && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="bad">
                  systemd has no unit file for {name}. It may have been deleted since this list was
                  read — reload the unit files from the services screen.
                </Banner>
              </div>
            )}
            {unit.load === 'masked' && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="warn">
                  This unit is masked: systemd refuses to start it at all. Deleting the link at{' '}
                  <span className="mono">/etc/systemd/system/{name}</span> in the file browser
                  unmasks it.
                </Banner>
              </div>
            )}
            {unit.template && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="warn">
                  This is a template. Nothing starts it directly — systemd makes a{' '}
                  {unit.timer ? 'timer' : 'service'} out of it per instance, named{' '}
                  {unit.name.replace('@.', '@something.')}.
                </Banner>
              </div>
            )}
            {host && host.status === 'online' && !host.sudoOk && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="warn">
                  {host.username} doesn't have passwordless sudo here, so systemd will refuse these
                  buttons. Set up access from the host's page first.
                </Banner>
              </div>
            )}
          </Card>

          {usable && (
            <>
              <SectionTitle>Manage</SectionTitle>
              <Card>
                <div className="actions" style={{ marginTop: 0 }}>
                  <button
                    className="primary"
                    onClick={() => ask('start')}
                    disabled={working || active}
                  >
                    {busy === 'start' ? 'Starting…' : 'Start'}
                  </button>
                  <button className="secondary" onClick={() => ask('restart')} disabled={working}>
                    {busy === 'restart' ? 'Restarting…' : 'Restart'}
                  </button>
                  <button className="danger" onClick={() => ask('stop')} disabled={working || !active}>
                    {busy === 'stop' ? 'Stopping…' : 'Stop'}
                  </button>
                </div>
                <p className="sub" style={{ marginBottom: 0 }}>
                  HostMan waits for systemd to finish, so a unit that takes its time starting
                  takes its time answering.
                </p>
                {/* Reload is a signal to a running program to re-read its
                    configuration. A timer is not a program, and systemd
                    refuses — so the button is not offered rather than offered
                    and refused. */}
                {!unit.timer && (
                  <>
                    <button
                      className="secondary block"
                      style={{ marginTop: 10 }}
                      onClick={() => ask('reload')}
                      disabled={working}
                    >
                      {busy === 'reload' ? 'Reloading…' : 'Reload its configuration'}
                    </button>
                    <p className="sub" style={{ marginBottom: 0, marginTop: 6 }}>
                      Only services written to handle it can reload without stopping; systemd says
                      so if this one can't.
                    </p>
                  </>
                )}
              </Card>

              <Card>
                <div className="title">At boot</div>
                <p className="sub" style={{ marginTop: 4, marginBottom: 0 }}>
                  {unit.fileState === 'enabled'
                    ? unit.timer
                      ? `${short} starts counting again whenever the machine does.`
                      : `${short} starts by itself when the machine does.`
                    : unit.fileState === 'static'
                      ? `${short} has no [Install] section, so there is nothing to enable: it runs when another unit pulls it in, or when you start it here.`
                      : unit.timer
                        ? `${short} is not armed at boot, so nothing it schedules will run until something starts it.`
                        : `${short} only runs when something starts it. It will not come back after a reboot.`}
                </p>
                <StartedBy hostId={hostId} unit={unit} />
                {unit.fileState !== 'static' && (
                  <button
                    style={{ marginTop: 10 }}
                    className="secondary block"
                    onClick={() => ask(unit.fileState === 'enabled' ? 'disable' : 'enable')}
                    disabled={working}
                  >
                    {busy === 'enable' || busy === 'disable'
                      ? 'Working…'
                      : unit.fileState === 'enabled'
                        ? 'Stop starting at boot'
                        : 'Start at boot'}
                  </button>
                )}
              </Card>
            </>
          )}

          <SectionTitle>Log</SectionTitle>
          <Card>
            <div className="row between">
              <div className="chips" style={{ margin: 0 }}>
                {LOG_LINES.map((count) => (
                  <button
                    key={count}
                    className={`chip ${lines === count ? 'on' : ''}`}
                    aria-pressed={lines === count}
                    onClick={() => setLines(count)}
                  >
                    {count}
                  </button>
                ))}
              </div>
              <button className="ghost" onClick={reloadLog} disabled={logLoading}>
                Refresh
              </button>
            </div>

            {logError && <Banner tone="bad">{logError}</Banner>}
            {log?.truncated && (
              <Banner tone="warn">
                Only the end of this log fits. The oldest of the {log.lines} lines were dropped.
              </Banner>
            )}
            <pre className="log" style={{ marginTop: 10 }}>
              {log
                ? log.content.trimEnd() || `Nothing in the journal for this ${kind}.`
                : 'Reading the journal…'}
            </pre>
          </Card>

          <UnitFile hostId={hostId} unit={unit} onChanged={reload} />

          {/* Deleting takes a unit file off the disk, so it is only offered for
              the ones an administrator put there. The distribution's, in
              /usr/lib, belong to the package manager, and the server refuses
              those whatever this decides. */}
          {ownUnitFile(unit.path) && (
            <>
              <SectionTitle>Danger zone</SectionTitle>
              <Card>
                <p className="sub" style={{ marginTop: 0 }}>
                  Deleting removes <span className="mono">{unit.path}</span>, the links that
                  start it at boot, and any drop-in overrides. Whatever it runs stays where it
                  is — this removes systemd's knowledge of the {kind}, not the program.
                </p>
                {active && (
                  <Banner tone="warn">
                    {short} is still running. Stop it first: deleting the unit of something
                    still running leaves the process up with nothing left to describe or stop
                    it.
                  </Banner>
                )}
                <button
                  className="danger block"
                  onClick={() => setConfirmDelete(true)}
                  disabled={active || working || deleting}
                >
                  Delete this {kind}
                </button>
              </Card>
            </>
          )}
        </>
      )}

      {confirmDelete && unit && (
        <Sheet
          title={`Delete ${short}?`}
          subtitle="The unit file, the links that start it at boot, and its drop-in overrides all go. Nothing it installed or wrote is touched."
          onClose={() => setConfirmDelete(false)}
        >
          <Banner tone="warn">
            There is no undo. The unit file is not kept anywhere — copy it out first if you
            might want it again.
          </Banner>
          <div className="actions">
            <button
              className="secondary"
              onClick={() => setConfirmDelete(false)}
              disabled={deleting}
            >
              Keep it
            </button>
            <button className="danger" onClick={remove} disabled={deleting}>
              {deleting ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </Sheet>
      )}

      {confirm && CONFIRM[confirm] && (
        <Sheet
          title={CONFIRM[confirm]!.title(short)}
          subtitle={CONFIRM[confirm]!.body}
          onClose={() => setConfirm(null)}
        >
          <div className="actions">
            <button className="secondary" onClick={() => setConfirm(null)} disabled={working}>
              Cancel
            </button>
            <button className="danger" onClick={() => run(confirm)} disabled={working}>
              {CONFIRM[confirm]!.verb}
            </button>
          </div>
        </Sheet>
      )}
    </Page>
  )
}

/**
 * Which units pull this one in — the answer "started by another unit" raises
 * and does not give.
 *
 * systemd only names units it has loaded, so an empty answer is "nothing is
 * pulling it in right now" rather than "nothing ever will". That distinction
 * matters on a static unit, where an empty answer otherwise reads as a
 * contradiction of the badge above it, so it is spelled out there; on an
 * ordinary enabled or disabled unit it is not a question anyone asked, and
 * nothing is said at all.
 */
function StartedBy({ hostId, unit }: { hostId: number; unit: ServiceUnit }) {
  const starters = unit.startedBy ?? []

  if (starters.length === 0) {
    if (unit.fileState !== 'static') return null
    return (
      <p className="sub" style={{ marginTop: 10, marginBottom: 0 }}>
        systemd does not currently name anything that pulls it in. A unit that
        only appears in something else's <span className="mono">Wants=</span> stays
        invisible here until that something is loaded.
      </p>
    )
  }

  return (
    <div style={{ marginTop: 10 }}>
      <div className="sub">
        Started by {starters.length === 1 ? 'this unit' : `these ${starters.length} units`}:
      </div>
      <div className="unit-refs">
        {starters.map((name) => (
          <UnitRef key={name} hostId={hostId} name={name} />
        ))}
      </div>
    </div>
  )
}

/** Another unit by name. Services and timers have a screen of their own and are
 *  a tap away; the socket or target that started one is named but goes nowhere,
 *  which is the truth — HostMan manages those two kinds and does not pretend
 *  to manage the rest. */
function UnitRef({ hostId, name }: { hostId: number; name: string }) {
  if (!name.endsWith('.service') && !name.endsWith('.timer')) return <span>{name}</span>
  return <Link to={`/hosts/${hostId}/service?name=${encodeURIComponent(name)}`}>{name}</Link>
}

/**
 * What to say under the figures when one of them is missing or is not quite the
 * measure it looks like.
 *
 * A dash on a running service is a question, and the answer is never "HostMan
 * could not be bothered" — it is that this host does not count that unit's
 * memory, or that this unit has no process to have a PID. Both have a cause
 * worth naming and a fix worth naming with it. On a stopped service neither is
 * a puzzle, and nothing is said.
 */
function figureNotes(unit: ServiceUnit, active: boolean): string[] {
  const notes: string[] = []

  if (unit.memoryFrom === 'rss') {
    notes.push(
      'Memory is what its processes have resident, added up: nothing on this host counts the ' +
        'service’s cgroup, so anything they share is counted more than once.',
    )
  } else if (active && unit.memory === 0) {
    notes.push(
      'Nothing on this host is counting this service’s memory. Adding MemoryAccounting=yes to ' +
        'its unit file turns it on from the next restart.',
    )
  }

  if (active && unit.mainPid === 0) {
    notes.push(
      unit.sub === 'exited'
        ? 'It is active with nothing running, so there is no process to have a PID. A ' +
          'Type=oneshot service with RemainAfterExit=yes stays this way on purpose.'
        : 'systemd is not watching a process for this one, and its cgroup is empty. A ' +
          'Type=forking service needs PIDFile= before systemd can tell which process is the daemon.',
    )
  }

  return notes
}

/** How long until a timer next fires. A timer that is not running has no next
 *  run to count down to, which is not the same as one that is due. */
function nextRun(unit: ServiceUnit): string {
  if (unit.active !== 'active') return '—'
  if (!unit.nextS) return 'Due'
  if (unit.nextS < 60) return '< 1m'
  return uptime(unit.nextS)
}

/** Where an administrator's own unit files live. The server decides this for
 *  real; this only decides whether to offer the button. */
const OWN_UNIT_DIRS = ['/etc/systemd/system', '/usr/local/lib/systemd/system']

function ownUnitFile(path: string): boolean {
  if (!path) return false
  return OWN_UNIT_DIRS.includes(path.slice(0, path.lastIndexOf('/')))
}

/** DONE is what to say afterwards, in the past tense the button was in. */
const DONE: Record<ServiceAction, string> = {
  start: 'Started',
  stop: 'Stopped',
  restart: 'Restarted',
  reload: 'Reloaded',
  enable: 'Enabled',
  disable: 'Disabled',
}

/**
 * The unit file, read and written through the same endpoints the file browser
 * uses — an atomic write that keeps the file's mode and owner. What is added
 * here is the daemon-reload afterwards, without which the edit is a change to a
 * file and not to a service.
 */
function UnitFile({
  hostId,
  unit,
  onChanged,
}: {
  hostId: number
  unit: ServiceUnit
  onChanged: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const path = unit.path
  // Nothing is fetched until the editor is opened: a unit file is one more SSH
  // session, and most visits to this screen are to read the log.
  const { data: file, reload: reloadFile } = useLoader(
    () => (editing && path ? api.file(hostId, path) : Promise.resolve(null)),
    [hostId, path, editing],
  )

  const content = draft ?? file?.content ?? ''
  const dirty = draft !== null && draft !== (file?.content ?? '')

  const save = async () => {
    setSaving(true)
    setFailure(null)
    setSaved(null)
    try {
      await api.saveFile(hostId, path, draft ?? '')
      // Two calls on purpose: the write is the file browser's, the reload is
      // systemd's, and either can fail on its own terms.
      await api.reloadServices(hostId)
      setDraft(null)
      setSaved(`Saved, and systemd re-read it. Restart ${serviceName(unit.name)} to run the new one.`)
      reloadFile()
      onChanged()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  if (!path) {
    return (
      <>
        <SectionTitle>Unit file</SectionTitle>
        <Card>
          <p className="sub" style={{ margin: 0 }}>
            systemd did not report a unit file for this service, so there is nothing to edit.
          </p>
        </Card>
      </>
    )
  }

  return (
    <>
      <SectionTitle>Unit file</SectionTitle>
      <Card>
        <div className="row between">
          <div className="grow">
            <div className="mono truncate">{path}</div>
            <div className="sub">
              {file ? `${file.mode} · ${file.owner}:${file.group}` : 'systemd read this file'}
            </div>
          </div>
          <button className="ghost" onClick={() => setEditing((open) => !open)}>
            {editing ? 'Close' : 'Edit'}
          </button>
        </div>

        {failure && (
          <div style={{ marginTop: 10 }}>
            <Banner tone="bad">{failure}</Banner>
          </div>
        )}
        {saved && (
          <div style={{ marginTop: 10 }}>
            <Banner tone="good">{saved}</Banner>
          </div>
        )}

        {editing && (
          <>
            {!file ? (
              <div className="sub" style={{ marginTop: 10 }}>
                Reading {path}…
              </div>
            ) : (
              <>
                <textarea
                  className="editor"
                  style={{ marginTop: 10 }}
                  value={content}
                  onChange={(e) => setDraft(e.target.value)}
                  spellCheck={false}
                  autoCapitalize="off"
                  autoCorrect="off"
                  autoComplete="off"
                  aria-label={`${unit.name} unit file`}
                />
                <p className="sub">
                  Saving writes the file and runs <span className="mono">daemon-reload</span>. The
                  service keeps running the old settings until it is restarted.
                </p>
                <div className="actions">
                  <button className="secondary" onClick={() => setDraft(null)} disabled={!dirty || saving}>
                    Discard
                  </button>
                  <button className="primary" onClick={save} disabled={!dirty || saving}>
                    {saving ? 'Saving…' : 'Save and reload'}
                  </button>
                </div>
              </>
            )}
          </>
        )}

        <div className="list-divider" />
        <Link className="sub" to={`/hosts/${hostId}/file?path=${encodeURIComponent(path)}`}>
          Open in the file browser ›
        </Link>
      </Card>
    </>
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
