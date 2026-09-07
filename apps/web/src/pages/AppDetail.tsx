import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { LaunchButton } from '../components/launch'
import { Page } from '../components/Layout'
import { DeploymentBadge, HealthBadge } from '../components/status'
import { Banner, Card, Empty, Field, Loading, SectionTitle, Sheet, useLoader } from '../components/ui'
import { ago, parts, ports, renderCommand, time } from '../lib/format'
import type { App, Host, Installation } from '../types'

export default function AppDetail() {
  const { id } = useParams()
  const appId = Number(id)
  const navigate = useNavigate()

  const { data: app, error, offline } = useLoader(() => api.app(appId), [appId], 10000)
  const { data: hosts } = useLoader(() => api.hosts(), [], 15000)
  const { data: installs, reload: reloadInstalls } = useLoader(
    () => api.installations(),
    [],
    10000,
  )
  const { data: history, reload: reloadHistory } = useLoader(
    () => api.deployments({ appId, limit: 10 }),
    [appId],
    10000,
  )

  const [deployTarget, setDeployTarget] = useState<Installation | 'new' | null>(null)
  const [uninstallTarget, setUninstallTarget] = useState<Installation | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)

  const mine = useMemo(() => installs?.filter((i) => i.appId === appId) ?? [], [installs, appId])

  const removeApp = async () => {
    try {
      await api.deleteApp(appId)
      navigate('/apps', { replace: true })
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
      setConfirmDelete(false)
    }
  }

  /**
   * Which action is in flight, as `action:installationId`. Check goes over SSH
   * or out to the app's own URL and can take seconds, so the button has to say
   * it is working — a press that visibly does nothing reads as a press that
   * did not land, and gets made again.
   */
  const [pending, setPending] = useState<string | null>(null)
  const running = (action: string, installation: Installation) =>
    pending === `${action}:${installation.id}`

  const act = async (action: string, installation: Installation, run: () => Promise<unknown>) => {
    setPending(`${action}:${installation.id}`)
    setActionError(null)
    try {
      await run()
      reloadInstalls()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setPending(null)
    }
  }

  const check = (installation: Installation) =>
    act('check', installation, () => api.checkInstallation(installation.id))

  const forget = (installation: Installation) =>
    act('forget', installation, () => api.forgetInstallation(installation.id))

  return (
    <Page
      title={app?.name ?? 'App'}
      back="/apps"
      action={
        <Link to={`/apps/${appId}/edit`}>
          <button className="ghost">Edit</button>
        </Link>
      }
    >
      <Loading error={error} offline={offline} hasData={!!app} />
      {actionError && <Banner tone="bad">{actionError}</Banner>}

      {app && (
        <>
          <Card>
            {app.description && <p style={{ marginTop: 0 }}>{app.description}</p>}
            <div className="meter-label">
              <span>Install command</span>
            </div>
            <pre className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}>
              {app.installCommand}
            </pre>
            {/* The other half of what this app is. An app that declared none
                says nothing here rather than showing an empty block. */}
            {app.uninstallCommand && (
              <>
                <div className="meter-label" style={{ marginTop: 12 }}>
                  <span>Uninstall command</span>
                </div>
                <pre
                  className="mono"
                  style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0 }}
                >
                  {app.uninstallCommand}
                </pre>
              </>
            )}
            <div className="actions">
              <button className="primary" onClick={() => setDeployTarget('new')}>
                Deploy
              </button>
              {/* Where the app is running somewhere, opening it is the thing
                  most often wanted from this page. */}
              <LaunchButton installations={mine} className="secondary" label="Open app" />
            </div>
          </Card>

          <SectionTitle>Deployed on</SectionTitle>
          {mine.length === 0 ? (
            <Empty message="Not deployed anywhere yet." />
          ) : (
            mine.map((installation) => (
              <Card key={installation.id}>
                <div className="row between">
                  <div className="grow">
                    <div className="title">{installation.hostName}</div>
                    <div className="sub">
                      {parts(
                        installation.hostAddress,
                        installation.version,
                        ports(installation.ports),
                        `updated ${ago(installation.updatedAt)}`,
                      )}
                    </div>
                  </div>
                  <div className="row" style={{ gap: 6 }}>
                    <LaunchButton installations={[installation]} />
                    <HealthBadge status={installation.healthStatus} />
                  </div>
                </div>
                {/* What the last check found, and when it ran. The time is
                    what a Check that changes nothing has to show for itself —
                    without it, confirming a healthy app still looks like a
                    button that did nothing. */}
                {(installation.healthDetail || installation.healthCheckedAt) && (
                  <div className="sub" style={{ marginTop: 8 }}>
                    {parts(
                      installation.healthDetail,
                      installation.healthCheckedAt &&
                        `checked ${ago(installation.healthCheckedAt)}`,
                    )}
                  </div>
                )}
                <div className="actions">
                  <button
                    className="primary"
                    onClick={() => setDeployTarget(installation)}
                    disabled={!!pending}
                  >
                    Redeploy
                  </button>
                  <button
                    className="secondary"
                    onClick={() => check(installation)}
                    disabled={!!pending}
                  >
                    {running('check', installation) ? 'Checking…' : 'Check'}
                  </button>
                </div>
                {/* The two ways of getting rid of it, on a row of their own:
                    four buttons do not fit across a phone, and these two are
                    the pair worth telling apart rather than the pair to put
                    beside Redeploy. Uninstall is absent, not disabled, for an
                    app that never said how to remove itself — there is nothing
                    the user could do here to make it work. */}
                <div className="actions">
                  {app.uninstallCommand && (
                    <button
                      className="danger"
                      onClick={() => setUninstallTarget(installation)}
                      disabled={!!pending}
                    >
                      Uninstall
                    </button>
                  )}
                  <button
                    className="secondary"
                    onClick={() => forget(installation)}
                    disabled={!!pending}
                  >
                    {running('forget', installation) ? 'Forgetting…' : 'Forget'}
                  </button>
                </div>
              </Card>
            ))
          )}

          <SectionTitle>History</SectionTitle>
          {history?.length === 0 ? (
            <Empty message="No deployments yet." />
          ) : (
            history?.map((deployment) => (
              <Link key={deployment.id} className="card" to={`/deployments/${deployment.id}`}>
                <div className="row between">
                  <div className="grow">
                    <div className="title">{deployment.hostName}</div>
                    <div className="sub">
                      {parts(time(deployment.startedAt), deployment.kind === 'uninstall' && 'uninstall')}
                    </div>
                  </div>
                  <DeploymentBadge status={deployment.status} />
                </div>
              </Link>
            ))
          )}

          <SectionTitle>Danger zone</SectionTitle>
          <Card>
            <p className="sub" style={{ marginTop: 0 }}>
              Deleting an app removes it and its deployment history from HostMan. Anything
              already installed on a host keeps running there — take it off each host first if
              that is not what you want.
            </p>
            <button className="danger block" onClick={() => setConfirmDelete(true)}>
              Delete app
            </button>
          </Card>
        </>
      )}

      {app && uninstallTarget && (
        <UninstallSheet
          app={app}
          host={hosts?.find((h) => h.id === uninstallTarget.hostId) ?? null}
          installation={uninstallTarget}
          onClose={() => setUninstallTarget(null)}
          onStarted={(deploymentId) => {
            reloadHistory()
            reloadInstalls()
            navigate(`/deployments/${deploymentId}`)
          }}
        />
      )}

      {app && deployTarget && (
        <DeploySheet
          app={app}
          hosts={hosts ?? []}
          installation={deployTarget === 'new' ? null : deployTarget}
          onClose={() => setDeployTarget(null)}
          onStarted={(deploymentId) => {
            reloadHistory()
            navigate(`/deployments/${deploymentId}`)
          }}
        />
      )}

      {confirmDelete && (
        <Sheet
          title={`Delete ${app?.name}?`}
          subtitle="This removes the app and its history from HostMan only."
          onClose={() => setConfirmDelete(false)}
        >
          {mine.length > 0 && (
            <Banner tone="warn">
              {mine.length === 1
                ? `${app?.name} is still installed on ${mine[0].hostName} and will stay there, with nothing left in HostMan to remove it.`
                : `${app?.name} is still installed on ${mine.length} hosts and will stay there, with nothing left in HostMan to remove it.`}
            </Banner>
          )}
          <div className="actions">
            <button className="secondary" onClick={() => setConfirmDelete(false)}>
              Cancel
            </button>
            <button className="danger" onClick={removeApp}>
              Delete
            </button>
          </div>
        </Sheet>
      )}
    </Page>
  )
}

/**
 * withHost adds the built-in placeholders a command can always use. They go in
 * last, exactly as the server does it, so an app cannot shadow them with a
 * parameter of the same name.
 */
function withHost(values: Record<string, string>, host: Host | null | undefined) {
  if (!host) return { ...values }
  return { ...values, host: host.address, hostname: host.name, user: host.username }
}

/**
 * UninstallSheet is the confirmation for taking an app off a host. It runs a
 * command on the machine and cannot be undone, so it shows exactly what will
 * run — with the parameters that install was given, which is what the server
 * will use — before it will do anything.
 */
function UninstallSheet({
  app,
  host,
  installation,
  onClose,
  onStarted,
}: {
  app: App
  host: Host | null
  installation: Installation
  onClose: () => void
  onStarted: (deploymentId: number) => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const preview = useMemo(
    () => renderCommand(app.uninstallCommand, withHost(installation.params, host)),
    [app.uninstallCommand, installation.params, host],
  )

  const start = async () => {
    setBusy(true)
    setError(null)
    try {
      const deployment = await api.uninstall(installation.id)
      onStarted(deployment.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <Sheet
      title={`Uninstall ${app.name}?`}
      subtitle={`from ${installation.hostName}`}
      onClose={onClose}
    >
      {error && <Banner tone="bad">{error}</Banner>}

      <p className="sub" style={{ marginTop: 0 }}>
        This runs on {installation.hostName} and removes the app from it. HostMan forgets the
        installation once the command succeeds; the log is kept either way.
      </p>

      {host && host.status !== 'online' && (
        <Banner tone="warn">{host.name} is not reachable right now. This will likely fail.</Banner>
      )}

      <div className="meter-label">
        <span>This will run on the host</span>
      </div>
      <pre className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
        {preview}
      </pre>

      <div className="actions">
        <button className="secondary" onClick={onClose} disabled={busy}>
          Cancel
        </button>
        <button className="danger" onClick={start} disabled={busy}>
          {busy ? 'Starting…' : 'Uninstall'}
        </button>
      </div>
    </Sheet>
  )
}

/**
 * DeploySheet is the confirmation step: pick the host, review the parameters,
 * see exactly what will run, then deploy. Redeploys land here prefilled, which
 * is what makes them one tap plus a confirm.
 */
function DeploySheet({
  app,
  hosts,
  installation,
  onClose,
  onStarted,
}: {
  app: App
  hosts: Host[]
  installation: Installation | null
  onClose: () => void
  onStarted: (deploymentId: number) => void
}) {
  const [hostId, setHostId] = useState<number>(installation?.hostId ?? hosts[0]?.id ?? 0)
  const [values, setValues] = useState<Record<string, string>>(() => {
    const initial: Record<string, string> = {}
    for (const param of app.params) {
      initial[param.name] = installation?.params[param.name] ?? param.default
    }
    return initial
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const host = hosts.find((h) => h.id === hostId)
  const preview = useMemo(
    () => renderCommand(app.installCommand, withHost(values, host)),
    [app.installCommand, values, host],
  )

  const start = async () => {
    setBusy(true)
    setError(null)
    try {
      const deployment = installation
        ? await api.redeploy(installation.id, values)
        : await api.deploy(app.id, hostId, values)
      onStarted(deployment.id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setBusy(false)
    }
  }

  return (
    <Sheet
      title={installation ? `Redeploy ${app.name}` : `Deploy ${app.name}`}
      subtitle={installation ? `to ${installation.hostName}` : undefined}
      onClose={onClose}
    >
      {error && <Banner tone="bad">{error}</Banner>}

      {!installation && (
        <Field label="Host">
          {hosts.length === 0 ? (
            <p className="sub">Add a host first.</p>
          ) : (
            <select value={hostId} onChange={(e) => setHostId(Number(e.target.value))}>
              {hosts.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name} ({h.address}){h.status !== 'online' ? ' — offline' : ''}
                </option>
              ))}
            </select>
          )}
        </Field>
      )}

      {host && host.status !== 'online' && (
        <Banner tone="warn">{host.name} is not reachable right now. The deploy will likely fail.</Banner>
      )}
      {host && host.status === 'online' && !host.sudoOk && (
        <Banner tone="warn">
          {host.username} doesn't have passwordless sudo on {host.name}. Commands that need root will
          fail.
        </Banner>
      )}

      {app.params.map((param) => (
        <Field key={param.name} label={param.label || param.name} help={param.help}>
          <input
            value={values[param.name] ?? ''}
            onChange={(e) => setValues((current) => ({ ...current, [param.name]: e.target.value }))}
            placeholder={param.default}
            autoCapitalize="none"
            autoCorrect="off"
          />
        </Field>
      ))}

      <div className="meter-label">
        <span>This will run on the host</span>
      </div>
      <pre className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
        {preview}
      </pre>

      <div className="actions">
        <button className="secondary" onClick={onClose} disabled={busy}>
          Cancel
        </button>
        <button className="primary" onClick={start} disabled={busy || !hostId}>
          {busy ? 'Starting…' : installation ? 'Redeploy' : 'Deploy'}
        </button>
      </div>
    </Sheet>
  )
}
