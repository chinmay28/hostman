import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { CauseBadge, ConfidenceBadge } from '../components/status'
import { Badge, Banner, Card, Loading, SectionTitle, useLoader } from '../components/ui'
import { ago, time, uptime } from '../lib/format'
import type { BootReport, BootSign, Restart } from '../types'

/**
 * Why did it restart?
 *
 * The screen is built around one claim and the evidence for it, in that order,
 * because that is the order the question is asked in: what happened, then how
 * do you know. The verdict is a guess and never pretends otherwise — the
 * confidence badge sits next to it, the reasoning is listed rather than
 * summarised, and every sign keeps the log line it came from so the guess can
 * be checked rather than believed.
 *
 * Nothing here polls. Each load is an SSH session that reads wtmp, walks the
 * previous boot out of the journal and asks the firmware about its power, which
 * is a lot to do every five seconds for an answer that changes once a month.
 */
export default function HostBoot() {
  const { id } = useParams()
  const hostId = Number(id)

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  const { data: report, error, offline, loading, reload } = useLoader(() => api.boot(hostId), [hostId])

  return (
    <Page
      title="Why it restarted"
      back={`/hosts/${hostId}`}
      action={
        <button className="ghost" onClick={reload} disabled={loading}>
          Refresh
        </button>
      }
    >
      <Loading error={error} offline={offline} hasData={!!report} />

      {!report && !error && (
        <Card>
          <div className="sub">
            Asking {host?.name ?? 'the host'} what it remembers. Walking a boot out of the journal
            takes a moment on an SD card.
          </div>
        </Card>
      )}

      {report && (
        <>
          <Verdict report={report} />
          <Reasons report={report} />
          <Signs report={report} />
          <History report={report} />
          <LastWords report={report} />
          <Record hostId={hostId} report={report} onChanged={reload} />
        </>
      )}
    </Page>
  )
}

/** The claim itself, and when. */
function Verdict({ report }: { report: BootReport }) {
  const booted = report.bootedAt && !report.bootedAt.startsWith('0001') ? report.bootedAt : null
  return (
    <Card>
      <div className="row between">
        <div className="grow">
          <div className="title">{report.headline}</div>
          <div className="sub">
            {booted ? whenItCameBack(booted) : 'HostMan could not date the restart'}
          </div>
        </div>
        <CauseBadge cause={report.cause} />
      </div>

      <p className="sub" style={{ marginTop: 12 }}>
        {report.detail}
      </p>

      <div className="stats">
        <Stat label="Up for" value={report.uptimeS > 0 ? uptime(report.uptimeS) : '—'} />
        <Stat
          label="Was up for"
          value={report.previousUpS ? uptime(report.previousUpS) : '—'}
        />
        <Stat
          label="Temperature"
          value={report.tempC != null ? `${report.tempC.toFixed(1)} °C` : '—'}
        />
      </div>

      <div className="row" style={{ gap: 6, marginTop: 12, flexWrap: 'wrap' }}>
        <ConfidenceBadge confidence={report.confidence} />
        {report.model && <Badge tone="neutral">{report.model}</Badge>}
      </div>
    </Card>
  )
}

/** When the machine came back. "3 hours ago, at 09:12" answers both questions
 *  at once, but past the point where ago() gives up on relative time it would
 *  read as the same date said twice — so beyond a month it is just the date. */
function whenItCameBack(booted: string): string {
  const relative = ago(booted)
  const days = (Date.now() - new Date(booted).getTime()) / 86_400_000
  return days > 30 || days < 0 ? relative : `${relative}, at ${time(booted)}`
}

/** The steps to the verdict. Listing them rather than folding them into a
 *  sentence is what lets someone disagree with one of them. */
function Reasons({ report }: { report: BootReport }) {
  if (report.reasons.length === 0) return null
  return (
    <>
      <SectionTitle>Why HostMan thinks so</SectionTitle>
      <Card>
        {report.reasons.map((reason, i) => (
          <div key={reason} className="row" style={{ gap: 10, padding: '6px 0' }}>
            <span className="sub" style={{ minWidth: 16, textAlign: 'right' }}>
              {i + 1}
            </span>
            <span style={{ fontSize: 14 }}>{reason}</span>
          </div>
        ))}
      </Card>
    </>
  )
}

/** Everything the pattern table matched, nearest the restart first, each with
 *  the line it was found in. The ones further out are kept rather than hidden:
 *  an under-voltage warning four hours before the restart is not the cause and
 *  is still the most useful thing on the screen. */
function Signs({ report }: { report: BootReport }) {
  if (report.signs.length === 0) return null
  const near = report.signs.filter((s) => s.near)
  return (
    <>
      <SectionTitle>What was in the log</SectionTitle>
      {report.signs.map((sign) => (
        <Card key={sign.label}>
          <SignRow sign={sign} />
        </Card>
      ))}
      {near.length === 0 && (
        <Card>
          <p className="sub" style={{ margin: 0 }}>
            None of these is close enough to the restart to have caused it. They describe the boot
            that ended rather than the moment it ended.
          </p>
        </Card>
      )}
    </>
  )
}

function SignRow({ sign }: { sign: BootSign }) {
  return (
    <>
      <div className="row between">
        <div className="grow">
          <div className="title">{sign.label}</div>
          <div className="sub">{when(sign)}</div>
        </div>
        {sign.near ? (
          <Badge tone="warn">At the restart</Badge>
        ) : (
          <Badge tone="neutral">Earlier</Badge>
        )}
      </div>
      {sign.detail && (
        <div className="mono" style={{ marginTop: 8 }}>
          {sign.detail}
        </div>
      )}
      <pre className="log" style={{ marginTop: 10, maxHeight: '20vh' }}>
        {sign.line}
      </pre>
    </>
  )
}

/** How a sign is placed in time. A count is worth saying because "this happened
 *  forty times" is a different problem from "this happened". */
function when(sign: BootSign): string {
  const parts = [
    sign.beforeS
      ? `${gap(sign.beforeS)} before it came back`
      : 'at some point during that boot',
  ]
  if (sign.count > 1) parts.push(`${sign.count} times`)
  return parts.join(' · ')
}

function gap(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`
  return `${Math.round(seconds / 3600)}h`
}

/** The pattern behind the question. One unexplained restart is bad luck; four
 *  in a week is the thing that made someone open this screen. */
function History({ report }: { report: BootReport }) {
  if (report.restarts.length === 0) return null
  return (
    <>
      <SectionTitle>Restarts it remembers</SectionTitle>
      <Card>
        {!report.cleanKnown && (
          <Banner tone="warn">
            `last` on this host does not show shutdown records, so HostMan cannot tell which of
            these were asked for.
          </Banner>
        )}
        {report.restarts.map((restart, i) => (
          <div key={i}>
            {i > 0 && <div className="list-divider" />}
            <RestartRow restart={restart} cleanKnown={report.cleanKnown} />
          </div>
        ))}
        {report.cleanKnown && report.unclean > 0 && (
          <>
            <div className="list-divider" />
            <p className="sub" style={{ margin: 0 }}>
              {report.unclean} of these {report.restarts.length} nothing asked for. The oldest is
              not counted either way — there is nothing below it in the record to judge it by.
            </p>
          </>
        )}
      </Card>
    </>
  )
}

function RestartRow({ restart, cleanKnown }: { restart: Restart; cleanKnown: boolean }) {
  return (
    <div className="row between" style={{ padding: '8px 0' }}>
      <div className="grow">
        <div style={{ fontSize: 14 }}>
          {restart.timed ? time(restart.bootedAt) : 'An undated restart'}
          {restart.current ? ' · running now' : ''}
        </div>
        <div className="sub">
          {restart.upS ? `up ${uptime(restart.upS)}` : 'length unknown'}
          {restart.kernel ? ` · ${restart.kernel}` : ''}
        </div>
      </div>
      {!cleanKnown ? null : restart.clean ? (
        <Badge tone="good">Asked for</Badge>
      ) : (
        <Badge tone="warn">Unasked</Badge>
      )}
    </div>
  )
}

/** The end of the previous boot, whatever it was. On an unexplained restart
 *  this is the whole of the evidence: the point is that it stops mid-sentence. */
function LastWords({ report }: { report: BootReport }) {
  return (
    <>
      <SectionTitle>The last thing it said</SectionTitle>
      <Card>
        {report.truncated && (
          <Banner tone="warn">
            Only the end of that boot's log fits. The oldest of it was dropped.
          </Banner>
        )}
        <pre className="log">
          {report.logTail.trimEnd() ||
            'Nothing — there is no log from before the restart on this host.'}
        </pre>
      </Card>
    </>
  )
}

/**
 * Where the evidence came from, and the offer to make there be more of it next
 * time.
 *
 * This is the most useful card on the screen when the verdict is "unexplained",
 * because the commonest reason for that verdict is not a mysterious fault but a
 * journal systemd keeps in memory and throws away on every restart. That is one
 * directory away from being fixed, and the button fixes it.
 */
function Record({
  hostId,
  report,
  onChanged,
}: {
  hostId: number
  report: BootReport
  onChanged: () => void
}) {
  const [working, setWorking] = useState(false)
  const [done, setDone] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [blocked, setBlocked] = useState<string | null>(null)

  const keep = async () => {
    setWorking(true)
    setFailure(null)
    setBlocked(null)
    try {
      const storage = await api.keepJournal(hostId)
      setBlocked(storage.blocked ?? null)
      setDone(
        storage.already
          ? 'This host already keeps its journal across restarts.'
          : 'Done. The journal now survives a restart, so the next one can be explained.',
      )
      onChanged()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setWorking(false)
    }
  }

  return (
    <>
      <SectionTitle>Where this came from</SectionTitle>
      <Card>
        <Detail label="Evidence" value={SOURCE_WORD[report.source]} />
        {report.logFile && <Detail label="Log file" value={report.logFile} />}
        {report.bootsKept ? (
          <Detail label="Boots in the journal" value={String(report.bootsKept)} />
        ) : null}
        <Detail label="Read as" value={report.asUser || 'unknown'} />
        {report.kernel && <Detail label="Kernel" value={report.kernel} />}
        {report.throttle && <Detail label="Firmware flags" value={report.throttle.raw} />}

        {failure && <Banner tone="bad">{failure}</Banner>}
        {done && <Banner tone="good">{done}</Banner>}
        {blocked && (
          <Banner tone="warn">
            {blocked} Edit it in the{' '}
            <Link to={`/hosts/${hostId}/file?path=${encodeURIComponent('/etc/systemd/journald.conf')}`}>
              file browser
            </Link>{' '}
            and restart systemd-journald.
          </Banner>
        )}

        {!report.persistent && report.journal && (
          <>
            <div className="list-divider" />
            <div className="title">Keep the journal across restarts</div>
            <p className="sub" style={{ marginTop: 4 }}>
              systemd keeps this host's log in memory, which is the default on Debian and so on
              Raspberry Pi OS. It means the log of the boot that crashed dies with the boot that
              crashed — which is why this screen has less to go on than it should. Creating{' '}
              <span className="mono">/var/log/journal</span> is the whole of the fix.
            </p>
            <p className="sub">
              journald then keeps up to a tenth of the filesystem;{' '}
              <span className="mono">SystemMaxUse=</span> in{' '}
              <span className="mono">/etc/systemd/journald.conf</span> caps it lower if that is too
              much for the card.
            </p>
            <button className="primary block" onClick={keep} disabled={working}>
              {working ? 'Turning it on…' : 'Keep the journal'}
            </button>
          </>
        )}

        {!report.journal && (
          <>
            <div className="list-divider" />
            <p className="sub" style={{ margin: 0 }}>
              This host has no systemd journal, so all HostMan can read is wtmp and whatever
              rsyslog happens to keep.
            </p>
          </>
        )}
      </Card>
    </>
  )
}

const SOURCE_WORD: Record<BootReport['source'], string> = {
  journal: "the previous boot's journal",
  logfile: 'an rsyslog file',
  none: 'no log at all',
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="row between" style={{ padding: '5px 0', gap: 12 }}>
      <span className="sub">{label}</span>
      <span className="truncate" style={{ fontSize: 14 }}>
        {value}
      </span>
    </div>
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
