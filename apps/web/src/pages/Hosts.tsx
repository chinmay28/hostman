import { Link } from 'react-router-dom'
import { api } from '../api'
import { HostMeters } from '../components/host'
import { TabPage } from '../components/Layout'
import { HomeBadge, HostBadge } from '../components/status'
import { HostTools } from '../components/tools'
import { Empty, Loading, useLoader } from '../components/ui'
import { ago, osName, parts, uptime } from '../lib/format'
import type { Host } from '../types'

/** The Hosts tab is the way onto a machine. Overview says what each host is
 *  running; this says how the machine is doing and gets you to its files,
 *  services, terminal and jobs in one tap. */
export default function Hosts() {
  const { data, error, loading, offline } = useLoader(() => api.hosts(), [], 10000)

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {!data && loading && <div className="empty">Loading…</div>}

      {data?.length === 0 && (
        <Empty
          message="Add a machine HostMan can reach over SSH — a hostname like nakedpi.local, or an IP address."
          action={
            <Link to="/hosts/new">
              <button className="primary">Add a host</button>
            </Link>
          }
        />
      )}

      {data?.map((host) => <HostCard key={host.id} host={host} />)}
    </TabPage>
  )
}

function HostCard({ host }: { host: Host }) {
  const sample = host.latest
  return (
    <Link className="card" to={`/hosts/${host.id}`}>
      <div className="row between">
        <div className="grow">
          <div className="title">{host.name}</div>
          <div className="sub">
            {host.username}@{host.address}
            {host.port !== 22 ? `:${host.port}` : ''}
          </div>
        </div>
        <div className="row" style={{ gap: 6 }}>
          {host.isSelf && <HomeBadge />}
          <HostBadge status={host.status} />
        </div>
      </div>

      {sample && <HostMeters sample={sample} />}

      {/* One line about the machine. The kernel and architecture are on the
          host's page, and the Home badge already says where HostMan runs;
          here the system's name, how long it has been up and how warm it is
          are what tell one host from another. A host that has no reading says
          why instead, in red where the reason is an error. */}
      {sample ? (
        <div className="sub" style={{ marginTop: 8 }}>
          {parts(
            osName(host.os),
            `up ${uptime(sample.uptimeS)}`,
            sample.tempC != null && `${sample.tempC.toFixed(1)}\u00a0°C`,
            host.status === 'online' && !host.sudoOk && 'no passwordless sudo',
          )}
        </div>
      ) : (
        <div className="sub" style={{ marginTop: 10, color: host.lastError ? 'var(--bad)' : undefined }}>
          {parts(`seen ${ago(host.lastSeenAt)}`, host.lastError || 'Waiting for the first reading…')}
        </div>
      )}

      <HostTools hostId={host.id} enabled={host.status === 'online'} />
    </Link>
  )
}
