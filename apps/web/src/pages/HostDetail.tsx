import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { SetupSheet } from '../components/provision'
import { DeploymentBadge, HomeBadge, HostBadge } from '../components/status'
import {
  Badge,
  Banner,
  Card,
  Loading,
  Meter,
  RangeBar,
  SectionTitle,
  Sheet,
  Sparkline,
  useLoader,
} from '../components/ui'
import { realDisks } from '../lib/disk'
import { ago, bytes, percent, time, uptime } from '../lib/format'
import type { HostProcess, HostTestResult, Stat } from '../types'

export default function HostDetail() {
  const { id } = useParams()
  const hostId = Number(id)
  const navigate = useNavigate()

  // Asking for metrics also tells the server someone is watching, which raises
  // the sampling rate for this host.
  const { data: host, error, offline, reload } = useLoader(() => api.host(hostId), [hostId], 5000)
  const { data: metrics } = useLoader(() => api.hostMetrics(hostId, 60), [hostId], 5000)
  const { data: deployments } = useLoader(() => api.deployments({ hostId, limit: 8 }), [hostId], 10000)

  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<HostTestResult | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [settingUp, setSettingUp] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const [confirmReboot, setConfirmReboot] = useState(false)
  const [rebooting, setRebooting] = useState(false)
  const [rebootNotice, setRebootNotice] = useState<string | null>(null)

  const test = async () => {
    setTesting(true)
    setResult(null)
    try {
      setResult(await api.testHost(hostId))
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setTesting(false)
    }
  }

  const remove = async () => {
    try {
      await api.deleteHost(hostId)
      navigate('/hosts', { replace: true })
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
      setConfirmDelete(false)
    }
  }

  // The host goes down a few seconds after it answers, so the confirmation is
  // the last thing it will say. The poller finding it gone afterwards is what
  // is meant to happen, not a failure.
  const reboot = async () => {
    setRebooting(true)
    setActionError(null)
    try {
      await api.reboot(hostId)
      setRebootNotice(`${host?.name} is restarting. It will show as offline until it answers again.`)
      reload()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setRebooting(false)
      setConfirmReboot(false)
    }
  }

  const sample = host?.latest
  const cpuHistory = metrics?.samples.map((s) => s.cpuPct) ?? []
  const summary = metrics?.summary
  const processes = metrics?.processes

  return (
    <Page
      title={host?.name ?? 'Host'}
      back="/hosts"
      action={
        <Link to={`/hosts/${hostId}/edit`}>
          <button className="ghost">Edit</button>
        </Link>
      }
    >
      <Loading error={error} offline={offline} hasData={!!host} />
      {actionError && <Banner tone="bad">{actionError}</Banner>}
      {rebootNotice && <Banner tone="warn">{rebootNotice}</Banner>}

      {host && (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">
                  {host.username}@{host.address}
                  {host.port !== 22 ? `:${host.port}` : ''}
                </div>
                <div className="sub">
                  {host.os || 'Not yet identified'} · seen {ago(host.lastSeenAt)}
                </div>
              </div>
              <div className="row" style={{ gap: 6 }}>
                {host.isSelf && <HomeBadge />}
                <HostBadge status={host.status} />
              </div>
            </div>

            {host.isSelf && (
              <div className="sub" style={{ marginTop: 10 }}>
                HostMan itself runs on this machine. It still connects over SSH, so it needs its
                own key authorized here like any other host.
              </div>
            )}

            {host.status !== 'online' && host.lastError && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="bad">{host.lastError}</Banner>
              </div>
            )}
            {host.status === 'online' && !host.sudoOk && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="warn">
                  Passwordless sudo isn't set up for {host.username}. Deployments that need root will
                  fail — set up access with the host's password, or run the command in Settings.
                </Banner>
              </div>
            )}

            <div className="actions">
              <button className="secondary" onClick={test} disabled={testing}>
                {testing ? 'Testing…' : 'Test connection'}
              </button>
              {/* A working host needs nothing, so setup only fronts up when
                  something is actually missing. */}
              {(host.status !== 'online' || !host.sudoOk) && (
                <button className="primary" onClick={() => setSettingUp(true)}>
                  Set up access
                </button>
              )}
            </div>
          </Card>

          {sample && (
            <Card>
              <div className="meters">
                <Meter label="CPU" value={sample.cpuPct} display={`${Math.round(sample.cpuPct)}%`} />
                {/* Just the used figure: three columns on a phone have no room
                    for "902 MB / 15.7 GB" without wrapping. */}
                <Meter
                  label="Memory"
                  used={sample.memUsed}
                  total={sample.memTotal}
                  display={bytes(sample.memUsed)}
                />
                <Meter
                  label="Load"
                  value={Math.min(100, sample.load1 * 25)}
                  display={sample.load1.toFixed(2)}
                />
              </div>
              <div className="sub" style={{ marginTop: 8 }}>
                {bytes(sample.memUsed)} of {bytes(sample.memTotal)} memory used
              </div>

              <div className="list-divider" />
              <div className="meter-label">
                <span>CPU, last hour</span>
                <b>{metrics ? `${metrics.samples.length} samples` : ''}</b>
              </div>
              <Sparkline values={cpuHistory} />

              {/* The reading above is one moment, which says nothing about
                  whether it is normal for this machine. The day's range and
                  average are what answer that. */}
              {summary && summary.samples > 0 && (
                <>
                  <div className="list-divider" />
                  <div className="meter-label" style={{ marginBottom: 10 }}>
                    <span>Last 24 hours</span>
                    <b>{summary.samples.toLocaleString()} samples</b>
                  </div>
                  <Range label="CPU" stat={summary.cpuPct} />
                  <Range
                    label="Memory"
                    stat={summary.memPct}
                    detail={`${span(summary.memUsed.min, summary.memUsed.max, bytes)} of ${bytes(
                      summary.memTotal,
                    )} used, ${bytes(summary.memUsed.avg)} on average`}
                  />
                </>
              )}

              <div className="list-divider" />
              {realDisks(sample.disks).map((disk) => (
                <div key={disk.mount} style={{ marginBottom: 10 }}>
                  <Meter
                    label={disk.mount}
                    used={disk.usedBytes}
                    total={disk.totalBytes}
                    display={`${bytes(disk.usedBytes)} / ${bytes(disk.totalBytes)}`}
                  />
                </div>
              ))}
              {sample.swapTotal > 0 && (
                <Meter
                  label="Swap"
                  used={sample.swapUsed}
                  total={sample.swapTotal}
                  display={`${Math.round(percent(sample.swapUsed, sample.swapTotal))}%`}
                />
              )}

              <div className="list-divider" />
              <div className="row between sub">
                <span>Uptime {uptime(sample.uptimeS)}</span>
                {sample.tempC != null && <span>{sample.tempC.toFixed(1)} °C</span>}
              </div>
            </Card>
          )}

          {/* The meters above say whether the machine is busy. This says what
              it is busy with, which is the next question and until now needed
              a terminal. A host that is up but has not been probed since
              HostMan started is seconds from having an answer; one that is
              down is not, so it is not left waiting for something. */}
          {sample && (processes || host.status === 'online') && (
            <>
              <SectionTitle>What's using it</SectionTitle>
              <Card>
                {!processes ? (
                  <div className="sub">Waiting for the next reading…</div>
                ) : (
                  <>
                    <div className="meter-label">
                      <span>Most CPU</span>
                      <b>{ago(processes.takenAt)}</b>
                    </div>
                    {processes.topCpu.length === 0 ? (
                      <div className="sub" style={{ marginBottom: 10 }}>
                        Nothing was using the CPU in that second.
                      </div>
                    ) : (
                      processes.topCpu.map((proc) => (
                        <ProcessRow
                          key={`cpu-${proc.pid}`}
                          proc={proc}
                          value={proc.cpuPct}
                          display={share(proc.cpuPct)}
                        />
                      ))
                    )}
                    {/* A process's figure is its share of the whole machine, so
                        it can be read against the CPU meter above rather than
                        against one core, which is what top would show. */}
                    <div className="sub" style={{ fontSize: 11 }}>
                      Measured over the same second as the reading above, as a share of the whole
                      machine.
                    </div>

                    <div className="list-divider" />
                    <div className="meter-label">
                      <span>Most memory</span>
                      <b>{bytes(sample.memTotal)} in all</b>
                    </div>
                    {processes.topMem.map((proc) => (
                      <ProcessRow
                        key={`mem-${proc.pid}`}
                        proc={proc}
                        value={proc.memPct}
                        display={bytes(proc.memBytes)}
                      />
                    ))}
                  </>
                )}
              </Card>
            </>
          )}

          <SectionTitle>Details</SectionTitle>
          <Card>
            <Detail label="Hostname" value={host.hostname || '—'} />
            <Detail label="Kernel" value={host.kernel || '—'} />
            <Detail label="Architecture" value={host.arch || '—'} />
            <Detail label="Passwordless sudo" value={host.sudoOk ? 'Yes' : 'No'} />
          </Card>

          {deployments && deployments.length > 0 && (
            <>
              <SectionTitle>Deployments here</SectionTitle>
              {deployments.map((deployment) => (
                <Link key={deployment.id} className="card" to={`/deployments/${deployment.id}`}>
                  <div className="row between">
                    <div className="grow">
                      <div className="title">{deployment.appName}</div>
                      <div className="sub">{time(deployment.startedAt)}</div>
                    </div>
                    <DeploymentBadge status={deployment.status} />
                  </div>
                </Link>
              ))}
            </>
          )}

          <SectionTitle>Manage</SectionTitle>
          <Link className="card" to={`/hosts/${hostId}/files`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Files</div>
                <div className="sub">Browse the disk, read a config, edit it</div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>
          <Link className="card" to={`/hosts/${hostId}/services`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Services</div>
                <div className="sub">
                  Start, stop and edit what was installed here by hand
                </div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>
          {/* Under Files and Services because it is the answer when neither of
              them is: the general way in, for the thing nobody built a screen
              for. It is last of the three on purpose — a tap that edits a
              config is better than a shell that could have. */}
          <Link className="card" to={`/hosts/${hostId}/shell`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Terminal</div>
                <div className="sub">
                  A login shell as {host.username}, with the keys a phone keyboard is missing
                </div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>
          {/* After the terminal because it is the terminal's other half: the
              same machine, the same user, and instead of typing the commands
              you say what you want and watch them be typed. */}
          <Link className="card" to={`/hosts/${hostId}/claude`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Claude</div>
                <div className="sub">
                  Tell Claude Code what you want done on {host.name}, and watch it do it
                </div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>
          {/* Between the file browser and the crontab because it is the third
              answer to "I need to do something on that machine": some things
              can only be done by a person in front of a browser, and the file
              they end up with belongs on the host rather than on the phone. */}
          <Link className="card" to={`/hosts/${hostId}/remote`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Remote session</div>
                <div className="sub">
                  A browser on this host, driven from here — sign in, download, and the file is on
                  {' '}
                  {host.name}
                </div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>
          {/* After the remote session, because it is the same errand ending
              in the same place: a file that belongs on the host rather than on
              the phone. A browser is for the ones behind a login; this is for
              the ones behind a magnet link, which a phone would otherwise
              download twice. */}
          <Link className="card" to={`/hosts/${hostId}/torrents`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Torrents</div>
                <div className="sub">
                  Hand {host.name} a torrent and let it do the downloading
                </div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>
          <Link className="card" to={`/hosts/${hostId}/cron`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Scheduled jobs</div>
                <div className="sub">
                  The crontab for {host.username}
                  {host.username === 'root' ? '' : ' and for root'}
                </div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>

          {/* Sits next to the Restart button on purpose: the question "why did
              it restart?" is the one asked immediately after a machine does it
              by itself, and this is where the answer is. */}
          <Link className="card" to={`/hosts/${hostId}/boot`}>
            <div className="row between">
              <div className="grow">
                <div className="title">Why it restarted</div>
                <div className="sub">
                  {sample
                    ? `Up ${uptime(sample.uptimeS)} — what took it down last, and how sure HostMan is`
                    : 'What took it down last, and how sure HostMan is'}
                </div>
              </div>
              <span className="chevron">›</span>
            </div>
          </Link>

          <Card>
            <div className="title">Restart</div>
            <p className="sub" style={{ marginTop: 4 }}>
              {host.isSelf
                ? 'This is the machine HostMan runs on, so restarting it takes HostMan with it. It comes back when the machine does.'
                : 'The host goes down a few seconds after you confirm, and HostMan reports it offline until it answers again.'}
            </p>
            {!host.sudoOk && (
              <Banner tone="warn">
                {host.username} needs passwordless sudo here before HostMan can restart the machine.
              </Banner>
            )}
            <button
              className="secondary block"
              onClick={() => setConfirmReboot(true)}
              disabled={host.status !== 'online'}
            >
              Reboot
            </button>
          </Card>

          <SectionTitle>Danger zone</SectionTitle>
          <Card>
            <p className="sub" style={{ marginTop: 0 }}>
              Removing a host deletes its monitoring history from HostMan. Nothing on the machine
              itself is changed.
              {host.isSelf && ' Removing this one also turns off updating HostMan from the UI.'}
            </p>
            <button className="danger block" onClick={() => setConfirmDelete(true)}>
              Remove host
            </button>
          </Card>
        </>
      )}

      {result && (
        <Sheet title={result.ok ? 'Connection works' : 'Could not connect'} onClose={() => setResult(null)}>
          {result.ok ? (
            <>
              <Banner tone="good">
                Connected to {result.hostname} ({result.os})
              </Banner>
              <div className="row" style={{ marginBottom: 12 }}>
                <span className="sub grow">Passwordless sudo</span>
                {result.sudoOk ? <Badge tone="good">Working</Badge> : <Badge tone="warn">Not set up</Badge>}
              </div>
            </>
          ) : (
            <Banner tone="bad">{result.error}</Banner>
          )}
          {result.hints?.map((hint) => (
            <p key={hint} className="sub">
              {hint}
            </p>
          ))}
          {!result.ok || !result.sudoOk ? (
            <div className="actions">
              <button className="secondary" onClick={() => setResult(null)}>
                Done
              </button>
              <button
                className="primary"
                onClick={() => {
                  setResult(null)
                  setSettingUp(true)
                }}
              >
                Set up access
              </button>
            </div>
          ) : (
            <button className="primary block" onClick={() => setResult(null)}>
              Done
            </button>
          )}
        </Sheet>
      )}

      {settingUp && host && (
        <SetupSheet
          hostId={hostId}
          username={host.username}
          address={host.address}
          port={host.port}
          onClose={() => setSettingUp(false)}
          onFinished={reload}
        />
      )}

      {confirmReboot && host && (
        <Sheet
          title={`Reboot ${host.name}?`}
          subtitle="Everything running on the machine stops and starts again."
          onClose={() => setConfirmReboot(false)}
        >
          {host.isSelf && (
            <Banner tone="warn">HostMan runs on this machine. It goes down with it.</Banner>
          )}
          <div className="actions">
            <button className="secondary" onClick={() => setConfirmReboot(false)} disabled={rebooting}>
              Cancel
            </button>
            <button className="danger" onClick={reboot} disabled={rebooting}>
              {rebooting ? 'Restarting…' : 'Reboot'}
            </button>
          </div>
        </Sheet>
      )}

      {confirmDelete && (
        <Sheet
          title={`Remove ${host?.name}?`}
          subtitle="HostMan will forget this host and its monitoring history. The machine is left alone."
          onClose={() => setConfirmDelete(false)}
        >
          <div className="actions">
            <button className="secondary" onClick={() => setConfirmDelete(false)}>
              Cancel
            </button>
            <button className="danger" onClick={remove}>
              Remove
            </button>
          </div>
        </Sheet>
      )}
    </Page>
  )
}

/** Range is one metric's day: the band it moved through, and its average. */
function Range({ label, stat, detail }: { label: string; stat: Stat; detail?: string }) {
  return (
    <div style={{ marginBottom: 10 }}>
      <div className="meter-label">
        <span>
          {label} {span(stat.min, stat.max, pct)}
        </span>
        <b>avg {pct(stat.avg)}</b>
      </div>
      <RangeBar
        label={`${label} over the last 24 hours`}
        min={stat.min}
        max={stat.max}
        avg={stat.avg}
      />
      {detail && (
        <div className="sub" style={{ marginTop: 4, fontSize: 11 }}>
          {detail}
        </div>
      )}
    </div>
  )
}

/** ProcessRow is one process in a top-five list: what it is, and how much of
 *  the resource in question it is holding. */
function ProcessRow({
  proc,
  value,
  display,
}: {
  proc: HostProcess
  value: number
  display: string
}) {
  return (
    <div style={{ marginBottom: 10 }}>
      <Meter
        label={
          <span>
            {proc.name} <span className="pid">{proc.pid}</span>
          </span>
        }
        value={value}
        display={display}
      />
    </div>
  )
}

const pct = (n: number) => `${Math.round(n)}%`

/** share keeps a decimal only where rounding would erase the figure: 0.4% is
 *  worth saying, 41.2% is not worth reading, and a process too small to write
 *  in a tenth of a percent on a machine with many cores says so rather than
 *  claiming to be idle. */
function share(n: number): string {
  if (n >= 10) return `${Math.round(n)}%`
  if (n >= 0.1) return `${n.toFixed(1)}%`
  return '<0.1%'
}

/** span writes a low and a high as one figure where they read the same, so a
 *  machine that has not moved says "12%" rather than "12–12%". */
function span(min: number, max: number, format: (n: number) => string): string {
  const low = format(min)
  const high = format(max)
  return low === high ? low : `${low}–${high}`
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="row between" style={{ padding: '5px 0' }}>
      <span className="sub">{label}</span>
      <span style={{ fontSize: 14 }}>{value}</span>
    </div>
  )
}
