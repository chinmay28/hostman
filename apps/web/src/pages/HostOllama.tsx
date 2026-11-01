import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import {
  Badge,
  Banner,
  Card,
  Copyable,
  Empty,
  Field,
  Loading,
  Meter,
  SectionTitle,
  Sheet,
  TailLog,
  useLoader,
} from '../components/ui'
import { bytes } from '../lib/format'
import type { OllamaAction, OllamaHost, OllamaModel, OllamaPull } from '../types'

/** How to get ollama onto a Linux host. HostMan will not do it: a model
 *  server is a decision about what a machine is for — gigabytes of disk and
 *  whatever GPU it has — and its installer is a `curl | sh`. */
const INSTALL = 'curl -fsSL https://ollama.com/install.sh | sh'

/** Where the models come from, for somebody who does not know a name. */
const LIBRARY = 'https://ollama.com/library'

/** How often to ask while a pull is running. Every answer is an SSH session,
 *  so a screen with nothing moving does not ask at all; a progress bar that
 *  only moved when you tapped it would not be a progress bar. */
const PULL_POLL_MS = 3000

/** A few names worth offering to a finger that would rather tap than type.
 *  Small ones first: a host that is a Pi should not be offered a 40 GB pull
 *  as its first idea. */
const SUGGESTIONS = ['llama3.2:3b', 'qwen2.5-coder:7b', 'gemma3:4b', 'mistral:7b']

/**
 * Ollama: the models on the host, driven from the phone.
 *
 * The screen does what anybody does with a model server from a phone: sees
 * what is on the disk and what is loaded, pulls another model, drops one,
 * unloads one, and talks to one — which is the Terminal screen with
 * `ollama run` typed into it, because a chat is a terminal program and the
 * terminal is already there. Removing a model is the one thing here that
 * cannot be undone short of pulling it again, so it confirms.
 */
export default function HostOllama() {
  const { id } = useParams()
  const hostId = Number(id)
  const navigate = useNavigate()

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  const [pace, setPace] = useState<number | undefined>(undefined)
  const { data: status, error, offline, loading, reload } = useLoader(
    () => api.ollama(hostId),
    [hostId],
    pace,
  )

  const [busy, setBusy] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [removing, setRemoving] = useState<OllamaModel | null>(null)

  const pulling = status?.pull?.state === 'running'
  useEffect(() => {
    setPace(pulling ? PULL_POLL_MS : undefined)
  }, [pulling])

  const act = async (action: OllamaAction, model = '') => {
    setBusy(model ? `${action}:${model}` : action)
    setFailure(null)
    try {
      await api.ollamaAction(hostId, action, model)
      setRemoving(null)
      reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const pull = async (model: string) => {
    setBusy('pull')
    setFailure(null)
    try {
      await api.pullOllama(hostId, model)
      reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const talk = (model: OllamaModel) => navigate(chatPath(hostId, model.name))

  const hostName = host?.name ?? 'the host'

  return (
    <Page
      title="Ollama"
      back={`/hosts/${hostId}`}
      action={
        <button className="ghost" onClick={reload} disabled={loading}>
          Refresh
        </button>
      }
    >
      <Loading error={error} offline={offline} hasData={!!status} />
      {failure && <Banner tone="bad">{failure}</Banner>}

      {!status && !error && (
        <Card>
          <div className="sub">Asking {hostName} about ollama…</div>
        </Card>
      )}

      {status && !status.installed && <NotInstalled hostName={hostName} onRefresh={reload} />}

      {status && status.installed && (
        <>
          {!status.running && (
            <ServerDown status={status} hostName={hostName} busy={busy} onStart={() => act('start')} />
          )}

          <PullCard status={status} busy={busy} onPull={pull} onAct={act} />

          {status.running && status.loaded.length > 0 && (
            <>
              <SectionTitle>In memory</SectionTitle>
              {status.loaded.map((model) => (
                <Card key={model.name}>
                  <div className="row between">
                    <div className="grow">
                      <div className="title">{model.name}</div>
                      <div className="sub">{describeLoaded(model)}</div>
                    </div>
                    <Badge tone="good" dot>
                      loaded
                    </Badge>
                  </div>
                  <div className="row" style={{ marginTop: 10, gap: 8 }}>
                    <button
                      className="secondary"
                      onClick={() => act('stop', model.name)}
                      disabled={busy !== null}
                    >
                      {busy === `stop:${model.name}` ? 'Unloading…' : 'Unload'}
                    </button>
                  </div>
                </Card>
              ))}
            </>
          )}

          {status.running && (
            <>
              <SectionTitle>On the disk</SectionTitle>
              {status.models.length === 0 ? (
                <Empty
                  message={`No models on ${hostName} yet. Pull one above — llama3.2:3b is a small place to start.`}
                />
              ) : (
                <>
                  {status.models.map((model) => (
                    <ModelCard
                      key={model.name}
                      model={model}
                      loaded={status.loaded.some((l) => l.name === model.name)}
                      busy={busy}
                      onTalk={() => talk(model)}
                      onRemove={() => setRemoving(model)}
                    />
                  ))}
                  <p className="sub" style={{ marginTop: 4 }}>
                    {summarise(status.models)} on {hostName}.
                  </p>
                </>
              )}
            </>
          )}

          <Card>
            <div className="title">How this works</div>
            <p className="sub" style={{ marginTop: 4 }}>
              Everything here goes through the <code>ollama</code> command as {status.user} on{' '}
              {hostName}
              {status.version ? ` (ollama ${status.version})` : ''}, which talks to the server
              over localhost — so the models it lists are the server's, whoever it runs as.
              {status.unit
                ? ` The server is a systemd service, ${status.unit}.`
                : ' There is no systemd service for the server here; it runs however it was started.'}
            </p>
            <p className="sub">
              Talking to a model opens the Terminal with <code>ollama run</code> typed in. Type
              a message and press Enter; <code>/bye</code> or Ctrl-D ends it, and the shell stays.
            </p>
          </Card>
        </>
      )}

      {removing && (
        <Sheet
          title={`Remove ${removing.name}?`}
          subtitle={`${removing.size} comes off the disk on ${hostName}. Getting it back is pulling it again.`}
          onClose={() => setRemoving(null)}
        >
          <button
            className="danger block"
            onClick={() => act('remove', removing.name)}
            disabled={busy !== null}
          >
            {busy ? 'Removing…' : 'Remove it'}
          </button>
          <button className="secondary block" onClick={() => setRemoving(null)}>
            Keep it
          </button>
        </Sheet>
      )}
    </Page>
  )
}

/** Ollama is the one thing on this screen HostMan will not install, so this
 *  is the whole of the screen until somebody has. The host page would not
 *  normally lead here — the card appears only where the probe found it — so
 *  this is what a bookmark finds after ollama was removed. */
function NotInstalled({ hostName, onRefresh }: { hostName: string; onRefresh: () => void }) {
  return (
    <Card>
      <div className="title">Ollama is not installed on {hostName}</div>
      <p className="sub" style={{ marginTop: 4 }}>
        HostMan found no <code>ollama</code> command there. It will not install one for you — a
        model server is a decision about what the machine is for, with gigabytes of disk behind
        it. Run this on {hostName} once, on the Terminal screen or over SSH, and come back:
      </p>
      <Copyable text={INSTALL} />
      <p className="sub">The card on the host's page appears by itself once it is there.</p>
      <button className="secondary block" onClick={onRefresh}>
        Look again
      </button>
    </Card>
  )
}

/** The command is there and the server is not. Where the installer left a
 *  unit, starting it is one tap through sudo; where it did not, HostMan
 *  starts `ollama serve` as the SSH user and it runs on after this screen. */
function ServerDown({
  status,
  hostName,
  busy,
  onStart,
}: {
  status: OllamaHost
  hostName: string
  busy: string | null
  onStart: () => void
}) {
  return (
    <Card>
      <div className="row between">
        <div className="title">The server is not running</div>
        <Badge tone="warn" dot>
          down
        </Badge>
      </div>
      <p className="sub" style={{ marginTop: 4 }}>
        {status.error ?? 'ollama did not answer.'}
      </p>
      <p className="sub">
        {status.unit
          ? `ollama.service is ${status.unit} on ${hostName}. Starting it needs passwordless sudo, like any service.`
          : `There is no ollama.service on ${hostName}, so HostMan would start ollama serve as ${status.user} and leave it running.`}
      </p>
      <button className="block" onClick={onStart} disabled={busy !== null}>
        {busy === 'start' ? 'Starting…' : 'Start the server'}
      </button>
    </Card>
  )
}

/** Pulling: a name to type or tap, and — once one is under way — the bar.
 *  One pull at a time: the server refuses a second while the first runs,
 *  because two downloads on one link are each slower than one and the
 *  screen can only follow one bar. */
function PullCard({
  status,
  busy,
  onPull,
  onAct,
}: {
  status: OllamaHost
  busy: string | null
  onPull: (model: string) => void
  onAct: (action: OllamaAction) => void
}) {
  const [name, setName] = useState('')
  const pull = status.pull
  const running = pull?.state === 'running'
  const have = new Set(status.models.map((m) => m.name))

  const submit = () => {
    const model = name.trim()
    if (!model) return
    onPull(model)
    setName('')
  }

  return (
    <Card>
      <div className="title">Pull a model</div>
      {pull && <PullProgress pull={pull} busy={busy} onAct={onAct} />}
      {!running && (
        <>
          <Field
            label="Model"
            help={`As ollama names it: name:tag. It downloads onto ${status.user}'s server, not onto this phone.`}
          >
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && submit()}
              placeholder="llama3.2:3b"
              autoCapitalize="off"
              autoCorrect="off"
              autoComplete="off"
              spellCheck={false}
              disabled={!status.running}
            />
          </Field>
          <div className="row" style={{ flexWrap: 'wrap', gap: 6, marginBottom: 12 }}>
            {SUGGESTIONS.filter((s) => !have.has(s) && !have.has(s + ':latest')).map((s) => (
              <button key={s} type="button" className="secondary" onClick={() => setName(s)}>
                {s}
              </button>
            ))}
            <a className="sub" href={LIBRARY} target="_blank" rel="noreferrer" style={{ alignSelf: 'center' }}>
              More at ollama.com ›
            </a>
          </div>
          <button
            className="primary block"
            onClick={submit}
            disabled={busy !== null || !status.running || name.trim() === ''}
          >
            {busy === 'pull' ? 'Starting…' : 'Pull'}
          </button>
        </>
      )}
    </Card>
  )
}

/** Where the pull got to: the bar while it runs, and what became of it
 *  afterwards until it is dismissed. The runner lives on the host, so a pull
 *  that was started from one phone shows on another. */
function PullProgress({
  pull,
  busy,
  onAct,
}: {
  pull: OllamaPull
  busy: string | null
  onAct: (action: OllamaAction) => void
}) {
  const [showLog, setShowLog] = useState(false)
  const running = pull.state === 'running'
  return (
    <div style={{ marginBottom: 12 }}>
      <div className="row between" style={{ marginBottom: 6 }}>
        <div className="grow">
          <div className="title">{pull.model}</div>
          <div className="sub">{pull.status ?? (running ? 'Starting…' : '')}</div>
        </div>
        <PullBadge pull={pull} />
      </div>
      {running && (
        <Meter
          label="Downloaded"
          value={Math.max(0, pull.percent)}
          display={pull.percent < 0 ? '…' : `${pull.percent}%`}
          tone="progress"
        />
      )}
      {pull.log && (
        <button
          type="button"
          className="ghost"
          style={{ padding: '6px 0' }}
          onClick={() => setShowLog((v) => !v)}
        >
          {showLog ? 'Hide the log' : 'Show the log'}
        </button>
      )}
      {showLog && <TailLog log={pull.log} />}
      <div className="row" style={{ marginTop: 8, gap: 8 }}>
        {running ? (
          <button className="danger" onClick={() => onAct('cancel')} disabled={busy !== null}>
            {busy === 'cancel' ? 'Cancelling…' : 'Cancel'}
          </button>
        ) : (
          <button className="secondary" onClick={() => onAct('clear')} disabled={busy !== null}>
            {busy === 'clear' ? 'Clearing…' : 'Dismiss'}
          </button>
        )}
      </div>
    </div>
  )
}

function PullBadge({ pull }: { pull: OllamaPull }) {
  switch (pull.state) {
    case 'running':
      return <Badge tone="accent" dot pulse>pulling</Badge>
    case 'ok':
      return <Badge tone="good" dot>pulled</Badge>
    case 'failed':
      return <Badge tone="bad" dot>failed{pull.exit ? ` (exit ${pull.exit})` : ''}</Badge>
    case 'cancelled':
      return <Badge tone="neutral">cancelled</Badge>
    case 'lost':
      return <Badge tone="warn" dot>lost</Badge>
  }
}

/** One model on the disk: what it is, how big, how old, and the two things
 *  to do with it — talk to it, or drop it. */
function ModelCard({
  model,
  loaded,
  busy,
  onTalk,
  onRemove,
}: {
  model: OllamaModel
  loaded: boolean
  busy: string | null
  onTalk: () => void
  onRemove: () => void
}) {
  return (
    <Card>
      <div className="row between">
        <div className="grow">
          <div className="title">{model.name}</div>
          <div className="sub">{describeModel(model)}</div>
        </div>
        {loaded && (
          <Badge tone="good" dot>
            loaded
          </Badge>
        )}
      </div>
      <div className="row" style={{ marginTop: 10, gap: 8 }}>
        <button onClick={onTalk} disabled={busy !== null}>
          Talk to it
        </button>
        <button className="danger" onClick={onRemove} disabled={busy !== null}>
          Remove
        </button>
      </div>
    </Card>
  )
}

/** The subtitle answers "what is it, and how much is it?": ollama's own id,
 *  size and age, as it reports them. */
export function describeModel(model: OllamaModel): string {
  return [model.id, model.size, model.modified].filter((part) => part).join(' · ')
}

/** A loaded model's line: where it runs, with what window, for how long. */
export function describeLoaded(model: {
  size: string
  processor: string
  context?: string
  until: string
}): string {
  const parts = [model.size, model.processor]
  if (model.context) parts.push(`${model.context} context`)
  if (model.until) parts.push(`until ${model.until}`)
  return parts.filter((part) => part).join(' · ')
}

/** "3 models, 8.7 GB" — the disk a collection takes, which is the figure a
 *  host with a small card cares about. */
export function summarise(models: OllamaModel[]): string {
  const total = models.reduce((sum, m) => sum + (m.sizeBytes || 0), 0)
  const count = `${models.length} model${models.length === 1 ? '' : 's'}`
  return total > 0 ? `${count}, ${bytes(total)}` : count
}

/** The Terminal screen with `ollama run <model>` typed into it. The name is
 *  one argument to ollama and one query value here; it is checked on the
 *  server against the shape of a name before it is typed anywhere. */
export function chatPath(hostId: number, model: string): string {
  return `/hosts/${hostId}/shell?run=${encodeURIComponent(`ollama run ${model}`)}`
}
