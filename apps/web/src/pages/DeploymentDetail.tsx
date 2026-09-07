import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { DeploymentBadge } from '../components/status'
import { Banner, Card, useLoader } from '../components/ui'
import { duration, time } from '../lib/format'
import type { Deployment, DeploymentStatus } from '../types'

export default function DeploymentDetail() {
  const { id } = useParams()
  const deploymentId = Number(id)

  const { data: initial, error } = useLoader(
    () => api.deployment(deploymentId),
    [deploymentId],
    // HostMan may be restarting mid-deployment; keep re-checking so the page
    // recovers on its own once it is back.
    5000,
  )
  const [log, setLog] = useState('')
  const [status, setStatus] = useState<DeploymentStatus | null>(null)
  const [finishedAt, setFinishedAt] = useState<string | null>(null)
  const [exitCode, setExitCode] = useState<number | null>(null)
  const [failure, setFailure] = useState('')
  const [canceling, setCanceling] = useState(false)
  const logRef = useRef<HTMLPreElement>(null)
  const pinnedToBottom = useRef(true)

  // The stream replays everything from the start, so it is the only source of
  // log text — no need to merge it with the initial fetch.
  useEffect(() => {
    const source = new EventSource(`/api/deployments/${deploymentId}/stream`)
    // A self-update restarts HostMan underneath this page. EventSource
    // reconnects on its own and the server replays the log from the start, so
    // clear what is on screen rather than appending a second copy.
    source.onopen = () => setLog('')
    source.addEventListener('log', (event) => {
      setLog((current) => current + (event as MessageEvent).data + '\n')
    })
    source.addEventListener('status', (event) => {
      const payload = JSON.parse((event as MessageEvent).data) as {
        status: DeploymentStatus
        exitCode: number | null
        error: string
        finishedAt: string | null
      }
      // Defensive: only a finished deployment ends the stream. A "running"
      // status would mean the deployment was handed to another process.
      if (payload.status === 'running') return
      setStatus(payload.status)
      setExitCode(payload.exitCode)
      setFailure(payload.error)
      setFinishedAt(payload.finishedAt)
      source.close()
    })
    // The stream ends without a verdict when HostMan hands the deployment
    // over mid-restart. EventSource reconnects on its own.
    source.addEventListener('handover', () => setLog((current) => current))
    source.onerror = () => {
      // EventSource retries on its own; a closed stream after the status event
      // is the normal ending.
      if (source.readyState === EventSource.CLOSED) source.close()
    }
    return () => source.close()
  }, [deploymentId])

  // Follow the output unless the user has scrolled up to read something.
  useEffect(() => {
    const element = logRef.current
    if (element && pinnedToBottom.current) element.scrollTop = element.scrollHeight
  }, [log])

  const onScroll = () => {
    const element = logRef.current
    if (!element) return
    const distance = element.scrollHeight - element.scrollTop - element.clientHeight
    pinnedToBottom.current = distance < 40
  }

  const cancel = async () => {
    setCanceling(true)
    try {
      await api.cancelDeployment(deploymentId)
    } catch {
      setCanceling(false)
    }
  }

  const deployment: Deployment | null = initial
    ? {
        ...initial,
        status: status ?? initial.status,
        exitCode: status ? exitCode : initial.exitCode,
        error: status ? failure : initial.error,
        finishedAt: status ? finishedAt : initial.finishedAt,
      }
    : null
  const running = deployment?.status === 'running'

  return (
    <Page
      title={deployment ? `${deployment.appName ?? 'Deployment'}` : 'Deployment'}
      back={deployment ? `/apps/${deployment.appId}` : '/'}
    >
      {error && !deployment && <Banner tone="bad">{error}</Banner>}
      {error && deployment && running && (
        <Banner tone="warn">
          Lost contact with HostMan — it may be restarting as part of this update. Reconnecting…
        </Banner>
      )}

      {deployment && (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="title">
                  {/* An uninstall runs the other way round, and reads as a
                      sentence rather than an arrow: it is the one entry in
                      this history nobody wants to mistake for a deploy. */}
                  {deployment.kind === 'uninstall'
                    ? `Uninstall ${deployment.appName} from ${deployment.hostName}`
                    : `${deployment.appName} → ${deployment.hostName}`}
                </div>
                <div className="sub">
                  {time(deployment.startedAt)} · {duration(deployment.startedAt, deployment.finishedAt)}
                  {deployment.exitCode != null && deployment.status !== 'succeeded'
                    ? ` · exit ${deployment.exitCode}`
                    : ''}
                </div>
              </div>
              <DeploymentBadge status={deployment.status} />
            </div>

            {deployment.error && deployment.status !== 'canceled' && (
              <div style={{ marginTop: 12 }}>
                <Banner tone="bad">{deployment.error}</Banner>
              </div>
            )}

            {running && (
              <div className="actions">
                <button className="danger" onClick={cancel} disabled={canceling}>
                  {canceling ? 'Canceling…' : 'Cancel deployment'}
                </button>
              </div>
            )}
            {deployment.status === 'failed' && (
              <div className="actions">
                <Link to={`/apps/${deployment.appId}`} style={{ flex: 1 }}>
                  <button className="primary block">
                    {deployment.kind === 'uninstall' ? 'Back to the app' : 'Try again'}
                  </button>
                </Link>
              </div>
            )}
          </Card>

          <pre className="log" ref={logRef} onScroll={onScroll} aria-live="polite">
            {log || 'Waiting for output…'}
          </pre>
        </>
      )}
    </Page>
  )
}
