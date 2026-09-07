import { useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import { Fab, Page } from '../components/Layout'
import { ServiceBadge } from '../components/status'
import { Badge, Banner, Card, Empty, Loading, useLoader } from '../components/ui'
import { serviceName, uptime } from '../lib/format'
import type { ServiceUnit } from '../types'

/** The four ways to cut the list. Which one you want depends entirely on why
 *  you opened the phone: something is broken, or something needs starting. */
const FILTERS = [
  { key: 'all', label: 'All' },
  { key: 'failed', label: 'Failed' },
  { key: 'running', label: 'Running' },
  { key: 'stopped', label: 'Stopped' },
] as const

type Filter = (typeof FILTERS)[number]['key']

/** Searching only earns its keyboard once there is more than a screenful. */
const SEARCH_FROM = 8

/**
 * Every service and timer someone installed on this host by hand.
 *
 * A machine runs hundreds of units and cares about a handful, so the list is
 * the unit files under /etc/systemd/system and /usr/local/lib/systemd/system —
 * the ones a person wrote — rather than everything systemd knows about. The
 * distribution's own services are not what you reach for a phone to fix.
 *
 * Timers are here because a scheduled job is written as a pair, and the timer
 * is the half that says when. Listing only services showed the other half of
 * such a job and, where the thing being scheduled was the distribution's,
 * nothing at all.
 *
 * Nothing here polls. Each of these screens is an SSH session to the host, so
 * they refresh when you ask them to and not on a timer.
 */
export default function HostServices() {
  const { id } = useParams()
  const hostId = Number(id)

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  const { data: list, error, offline, loading, reload } = useLoader(() => api.services(hostId), [hostId])

  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [reloading, setReloading] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const units = useMemo(() => list?.units ?? [], [list])
  const counts = useMemo(
    () => ({
      all: units.length,
      failed: units.filter((u) => u.active === 'failed').length,
      running: units.filter((u) => u.active === 'active').length,
      stopped: units.filter((u) => u.active !== 'active' && u.active !== 'failed').length,
    }),
    [units],
  )

  const needle = query.trim().toLowerCase()
  const shown = units.filter(
    (unit) =>
      inFilter(unit, filter) &&
      (needle === '' || `${unit.name} ${unit.description}`.toLowerCase().includes(needle)),
  )

  // Editing a unit file from the file browser changes the file and nothing
  // else until systemd is told to read it again, so the button is here too.
  const reloadUnits = async () => {
    setReloading(true)
    setNotice(null)
    setFailure(null)
    try {
      await api.reloadServices(hostId)
      setNotice('systemd re-read the unit files on disk.')
      reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setReloading(false)
    }
  }

  return (
    <Page
      title="Services"
      back={`/hosts/${hostId}`}
      action={
        <button className="ghost" onClick={reload} disabled={loading}>
          Refresh
        </button>
      }
    >
      <Loading error={error} offline={offline} hasData={!!list} />
      {failure && <Banner tone="bad">{failure}</Banner>}
      {notice && <Banner tone="good">{notice}</Banner>}

      {!list && !error && (
        <Card>
          <div className="sub">Asking {host?.name ?? 'the host'} what it runs…</div>
        </Card>
      )}

      {list && (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">{tally(units)}</div>
                <div className="sub">
                  {counts.failed > 0
                    ? `${counts.failed} of them failed`
                    : `${counts.running} running, ${counts.stopped} stopped`}
                </div>
              </div>
              {/* Root is the difference between reading the state and being
                  able to change it, so it is stated rather than discovered. */}
              <Badge tone={list.asUser === 'root' ? 'accent' : 'neutral'}>
                as {list.asUser || 'unknown'}
              </Badge>
            </div>

            {counts.all > 0 && (
              <div className="chips">
                {FILTERS.map((option) => (
                  <button
                    key={option.key}
                    className={`chip ${filter === option.key ? 'on' : ''}`}
                    aria-pressed={filter === option.key}
                    onClick={() => setFilter(option.key)}
                  >
                    {option.label}
                    <span className="count">{counts[option.key]}</span>
                  </button>
                ))}
              </div>
            )}
          </Card>

          {counts.all >= SEARCH_FROM && (
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Find a unit"
              aria-label="Find a unit"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              style={{ marginBottom: 12 }}
            />
          )}

          {list.truncated && (
            <Banner tone="warn">
              This host has more hand-installed units than HostMan will list. Only the first{' '}
              {units.length} are shown.
            </Banner>
          )}

          {host && host.status === 'online' && !host.sudoOk && (
            <Banner tone="warn">
              {host.username} doesn't have passwordless sudo here, so starting and stopping
              services will be refused. Set up access from the host's page first.
            </Banner>
          )}

          {counts.all === 0 ? (
            <Empty
              message={`Nothing hand-installed on ${host?.name ?? 'this host'}. Services and timers in /etc/systemd/system show up here — the distribution's own don't.`}
            />
          ) : shown.length === 0 ? (
            <Empty message="Nothing matches that." />
          ) : (
            shown.map((unit) => (
              <Link
                key={unit.name}
                className="card"
                to={`/hosts/${hostId}/service?name=${encodeURIComponent(unit.name)}`}
              >
                <div className="row between">
                  <div className="grow">
                    <div className="title">{serviceName(unit.name)}</div>
                    <div className="sub truncate">{summarize(unit)}</div>
                  </div>
                  <ServiceBadge unit={unit} />
                  <span className="chevron">›</span>
                </div>
              </Link>
            ))
          )}

          <Card>
            <div className="title">Unit files changed on disk?</div>
            <p className="sub" style={{ marginTop: 4 }}>
              Editing a unit file from the file browser changes the file, and nothing else, until
              systemd reads it again.
            </p>
            <button className="secondary block" onClick={reloadUnits} disabled={reloading}>
              {reloading ? 'Reloading…' : 'Reload unit files'}
            </button>
          </Card>
        </>
      )}

      {/* Adding sits in the corner the thumb already rests in, the way it does
          on the tabs that can add things. */}
      <Fab to={`/hosts/${hostId}/service/new`} label="Add a service" />
    </Page>
  )
}

function inFilter(unit: ServiceUnit, filter: Filter): boolean {
  switch (filter) {
    case 'running':
      return unit.active === 'active'
    case 'failed':
      return unit.active === 'failed'
    case 'stopped':
      return unit.active !== 'active' && unit.active !== 'failed'
    default:
      return true
  }
}

/** The header counts the two kinds separately where there are both. A machine
 *  with four services and four timers has four jobs on a schedule, and rolling
 *  them into "8 services" says neither number. */
function tally(units: ServiceUnit[]): string {
  const timers = units.filter((unit) => unit.timer).length
  const services = units.length - timers
  const parts: string[] = []
  if (services > 0 || timers === 0) parts.push(`${services} service${services === 1 ? '' : 's'}`)
  if (timers > 0) parts.push(`${timers} timer${timers === 1 ? '' : 's'}`)
  return parts.join(' · ')
}

/** The subtitle answers "and what about it?" — what it is, and how long it has
 *  been the way it is. A unit with no description at least has a name. */
function summarize(unit: ServiceUnit): string {
  const parts: string[] = []
  if (unit.description && unit.description !== unit.name) parts.push(unit.description)
  if (unit.template) parts.push('template')
  else if (unit.load === 'not-found') parts.push('no unit file')
  else if (unit.load === 'masked') parts.push('masked')
  // How long a timer has been waiting is not what anyone came to find out.
  // When it next goes off is.
  else if (unit.timer) parts.push(schedule(unit))
  else if (unit.sinceS > 0) parts.push(`${unit.active === 'active' ? 'up' : 'since'} ${uptime(unit.sinceS)}`)
  return parts.join(' · ') || unit.name
}

/** When a timer next fires. A stopped one has no next run at all, which is a
 *  different thing from one that is due. */
function schedule(unit: ServiceUnit): string {
  if (unit.active !== 'active') return 'not scheduled'
  if (!unit.nextS) return 'due now'
  if (unit.nextS < 60) return 'due in under a minute'
  return `next in ${uptime(unit.nextS)}`
}
