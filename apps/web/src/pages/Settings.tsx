import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api'
import { TabPage } from '../components/Layout'
import { HomeBadge, HostBadge } from '../components/status'
import { Banner, Card, Copyable, Field, Loading, SectionTitle, Sheet, useLoader } from '../components/ui'
import type { SelfInfo } from '../types'

export default function Settings() {
  const { data, error, offline, reload } = useLoader(() => api.sshKey(), [])
  // Refreshed on a timer so a running update surfaces without a manual reload.
  const { data: self, reload: reloadSelf } = useLoader(() => api.self(), [], 5000)
  const [confirmRotate, setConfirmRotate] = useState(false)
  const [rotating, setRotating] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)

  const rotate = async () => {
    setRotating(true)
    try {
      await api.rotateSSHKey()
      setNotice('New key generated. Add it to every host before the next deployment.')
      reload()
    } catch (e) {
      setNotice(e instanceof Error ? e.message : String(e))
    } finally {
      setRotating(false)
      setConfirmRotate(false)
    }
  }

  return (
    <TabPage>
      <Loading error={error} offline={offline} hasData={!!data} />
      {notice && <Banner tone="warn">{notice}</Banner>}

      <SectionTitle>This machine</SectionTitle>
      {self && <SelfCard info={self} onStarted={reloadSelf} />}

      <SectionTitle>Adding a host</SectionTitle>
      <p className="sub" style={{ margin: '0 4px 12px' }}>
        Adding a host can do both of these for you, if you give it the host's password once — it is
        used for that one connection and never stored. These are the same two steps by hand, run on
        the machine, signed in as the user HostMan will connect as.
      </p>

      {data && (
        <>
          <Card>
            <div className="meter-label">
              <span>1 · Trust HostMan's key</span>
            </div>
            <Copyable text={data.authorizeCommand} />
          </Card>

          <Card>
            <div className="meter-label">
              <span>2 · Allow unattended installs</span>
            </div>
            <p className="sub" style={{ marginTop: 0 }}>
              Install scripts end in <span className="mono">| sudo bash</span>, so the user needs sudo
              without a password prompt.
            </p>
            <Copyable text={data.sudoCommand} />
          </Card>

          <SectionTitle>HostMan's SSH key</SectionTitle>
          <Card>
            <div className="meter-label">
              <span>Public key</span>
            </div>
            <Copyable text={data.publicKey} />
            <div className="sub" style={{ marginTop: 10 }}>
              Fingerprint <span className="mono">{data.fingerprint}</span>
            </div>
          </Card>

          <Card>
            <p className="sub" style={{ marginTop: 0 }}>
              Rotating generates a new keypair. Every host stops accepting HostMan until you add the
              new public key to it, so keep this for when a key may have leaked.
            </p>
            <button className="danger block" onClick={() => setConfirmRotate(true)}>
              Rotate key
            </button>
          </Card>
        </>
      )}

      <SectionTitle>About</SectionTitle>
      <Card>
        <p className="sub" style={{ marginTop: 0 }}>
          HostMan holds a key that can run commands as root on your hosts. Keep it on your LAN or
          Tailscale network — don't expose it to the internet. Its database also holds the private
          key, so treat backups of it as a secret.
        </p>
        {self && (
          <div className="row between sub" style={{ marginTop: 10 }}>
            <span>Version</span>
            <span className="mono">{self.version}</span>
          </div>
        )}
      </Card>

      {confirmRotate && (
        <Sheet
          title="Rotate the SSH key?"
          subtitle="Every host will need the new public key added before it will accept HostMan again."
          onClose={() => setConfirmRotate(false)}
        >
          <div className="actions">
            <button className="secondary" onClick={() => setConfirmRotate(false)}>
              Cancel
            </button>
            <button className="danger" onClick={rotate} disabled={rotating}>
              {rotating ? 'Rotating…' : 'Rotate'}
            </button>
          </div>
        </Sheet>
      )}
    </TabPage>
  )
}

/**
 * SelfCard is where HostMan updates itself: it shows the machine it runs on,
 * whether an update can start, and the version to build.
 */
function SelfCard({ info, onStarted }: { info: SelfInfo; onStarted: () => void }) {
  const navigate = useNavigate()
  const [confirming, setConfirming] = useState(false)
  const [ref, setRef] = useState(info.ref)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Follow the server's suggestion until the user types their own.
  useEffect(() => setRef(info.ref), [info.ref])

  const start = async () => {
    setBusy(true)
    setError(null)
    try {
      const deployment = await api.selfUpdate(ref.trim())
      onStarted()
      navigate(`/deployments/${deployment.id}`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  if (!info.host) {
    return (
      <Card>
        <p className="sub" style={{ marginTop: 0 }}>
          HostMan hasn't registered the machine it runs on, so it can't update itself from here.
          Add it as a host with the address <span className="mono">127.0.0.1</span>.
        </p>
        <Link to="/hosts/new">
          <button className="secondary block">Add this machine</button>
        </Link>
      </Card>
    )
  }

  return (
    <Card>
      <div className="row between">
        <div className="grow">
          <div className="title">{info.host.name}</div>
          <div className="sub">
            {info.host.username}@{info.host.address} · version <span className="mono">{info.version}</span>
          </div>
        </div>
        <div className="row" style={{ gap: 6 }}>
          <HomeBadge />
          <HostBadge status={info.host.status} />
        </div>
      </div>

      {error && (
        <div style={{ marginTop: 12 }}>
          <Banner tone="bad">{error}</Banner>
        </div>
      )}

      {info.runningDeploymentId ? (
        <div className="actions">
          <button className="primary" onClick={() => navigate(`/deployments/${info.runningDeploymentId}`)}>
            Watch the update
          </button>
        </div>
      ) : (
        <>
          {!info.ready && info.blocked && (
            <div style={{ marginTop: 12 }}>
              <Banner tone="warn">{info.blocked}</Banner>
            </div>
          )}
          <div className="actions">
            <button className="primary" onClick={() => setConfirming(true)} disabled={!info.ready}>
              Update HostMan
            </button>
          </div>
        </>
      )}

      {confirming && (
        <Sheet
          title="Update HostMan"
          subtitle={`Rebuilds and restarts HostMan on ${info.host.name}.`}
          onClose={() => setConfirming(false)}
        >
          <Field label="Version" help="Branch, tag or commit to build from.">
            <input
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              placeholder="main"
              autoCapitalize="none"
              autoCorrect="off"
            />
          </Field>
          <Banner tone="warn">
            HostMan restarts itself part-way through. The update keeps running on the host and this
            page picks the log back up once it is available again.
          </Banner>
          <div className="actions">
            <button className="secondary" onClick={() => setConfirming(false)} disabled={busy}>
              Cancel
            </button>
            <button className="primary" onClick={start} disabled={busy || ref.trim() === ''}>
              {busy ? 'Starting…' : 'Update'}
            </button>
          </div>
        </Sheet>
      )}
    </Card>
  )
}
