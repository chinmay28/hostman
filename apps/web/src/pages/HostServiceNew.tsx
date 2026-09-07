import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { Banner, Card, Field, Loading, SectionTitle, useLoader } from '../components/ui'

/** What systemd should do when the program exits. "On failure" is the one a
 *  home server almost always wants and the one people forget to write. */
const RESTART = [
  { value: 'on-failure', label: 'If it fails' },
  { value: 'always', label: 'Always' },
  { value: 'no', label: 'Never' },
]

interface Draft {
  name: string
  description: string
  command: string
  workingDir: string
  user: string
  restart: string
}

const EMPTY: Draft = {
  name: '',
  description: '',
  command: '',
  workingDir: '',
  user: '',
  restart: 'on-failure',
}

/**
 * A new service, from six fields rather than from a blank unit file.
 *
 * Writing a unit file from memory on a phone is how you end up with a service
 * that starts before the network and dies. So the form asks the questions that
 * have answers — what to run, as whom, where, and what to do when it stops —
 * and writes the file. The file is on screen the whole time, and anyone who
 * would rather type it can take it over.
 */
export default function HostServiceNew() {
  const { id } = useParams()
  const hostId = Number(id)
  const navigate = useNavigate()

  const { data: host } = useLoader(() => api.host(hostId), [hostId])

  const [draft, setDraft] = useState<Draft>(EMPTY)
  // Once the file is edited by hand the fields stop driving it: two things
  // writing the same text is how an edit silently disappears.
  const [raw, setRaw] = useState<string | null>(null)
  const [enable, setEnable] = useState(true)
  const [start, setStart] = useState(true)

  const [busy, setBusy] = useState<string | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const set = (field: keyof Draft) => (value: string) => setDraft((d) => ({ ...d, [field]: value }))

  const unitName = fullName(draft.name)
  const content = raw ?? buildUnit(draft, unitName)
  const ready = draft.name.trim() !== '' && (raw !== null || draft.command.trim() !== '')

  const create = async () => {
    setFailure(null)
    setBusy('Writing the unit file…')
    try {
      const unit = await api.createService(hostId, unitName, content)

      // Three calls rather than one, so a service that is created and then
      // refuses to start says exactly that instead of looking like a create
      // that failed. Whatever happens, the service now exists.
      if (enable) {
        setBusy('Setting it to start at boot…')
        await api.serviceAction(hostId, unit.name, 'enable')
      }
      if (start) {
        setBusy('Starting it…')
        await api.serviceAction(hostId, unit.name, 'start')
      }
      navigate(`/hosts/${hostId}/service?name=${encodeURIComponent(unit.name)}`, { replace: true })
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
      setBusy(null)
    }
  }

  return (
    <Page
      title="New service"
      back={`/hosts/${hostId}/services`}
      action={
        <button className="ghost" onClick={create} disabled={!ready || busy !== null}>
          Create
        </button>
      }
    >
      <Loading error={null} offline={false} hasData={!!host} />
      {failure && <Banner tone="bad">{failure}</Banner>}
      {busy && <Banner tone="warn">{busy}</Banner>}

      {host && host.status === 'online' && !host.sudoOk && (
        <Banner tone="warn">
          {host.username} doesn't have passwordless sudo on {host.name}, so writing into
          /etc/systemd/system will be refused. Set up access from the host's page first.
        </Banner>
      )}

      <Card>
        <Field label="Name" help={draft.name.trim() ? `Installed as ${unitName}` : 'Letters, digits, dashes'}>
          <input
            value={draft.name}
            onChange={(e) => set('name')(e.target.value)}
            placeholder="photo-sync"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            aria-label="Service name"
          />
        </Field>
        <Field label="What it is" help="Shown next to the service everywhere systemd names it.">
          <input
            value={draft.description}
            onChange={(e) => set('description')(e.target.value)}
            placeholder="Sync photos to the NAS"
            aria-label="Description"
          />
        </Field>
      </Card>

      <SectionTitle>What it runs</SectionTitle>
      <Card>
        <Field
          label="Command"
          help="A full path. systemd runs this directly — no shell, so no pipes, globs or $HOME."
        >
          <input
            value={draft.command}
            onChange={(e) => set('command')(e.target.value)}
            placeholder="/usr/local/bin/photo-sync --daemon"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            aria-label="Command"
          />
        </Field>
        <Field label="Run as" help={`Left empty it runs as root.`}>
          <input
            value={draft.user}
            onChange={(e) => set('user')(e.target.value)}
            placeholder={host?.username ?? 'root'}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            aria-label="User to run as"
          />
        </Field>
        <Field label="Working directory" help="Optional. Where the command starts from.">
          <input
            value={draft.workingDir}
            onChange={(e) => set('workingDir')(e.target.value)}
            placeholder="/srv/photos"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            aria-label="Working directory"
          />
        </Field>

        <label>Restart it</label>
        <div className="chips" style={{ marginTop: 0 }}>
          {RESTART.map((option) => (
            <button
              key={option.value}
              className={`chip ${draft.restart === option.value ? 'on' : ''}`}
              aria-pressed={draft.restart === option.value}
              onClick={() => set('restart')(option.value)}
            >
              {option.label}
            </button>
          ))}
        </div>
      </Card>

      <SectionTitle>Once it exists</SectionTitle>
      <Card>
        <label className="checkbox">
          <input type="checkbox" checked={start} onChange={(e) => setStart(e.target.checked)} />
          Start it now
        </label>
        <label className="checkbox">
          <input type="checkbox" checked={enable} onChange={(e) => setEnable(e.target.checked)} />
          Start it at boot
        </label>
      </Card>

      <SectionTitle>Unit file</SectionTitle>
      <Card>
        <p className="sub" style={{ marginTop: 0 }}>
          {raw === null
            ? `This is what will be written to /etc/systemd/system/${unitName}. systemd reads it before HostMan keeps it: anything it refuses to load is taken straight back off the disk.`
            : 'Edited by hand, so the fields above no longer change it.'}
        </p>
        <textarea
          className="editor"
          value={content}
          onChange={(e) => setRaw(e.target.value)}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          autoComplete="off"
          aria-label="Unit file"
        />
        {raw !== null && (
          <button className="secondary block" onClick={() => setRaw(null)}>
            Go back to the fields
          </button>
        )}
      </Card>

      <div className="actions">
        <button
          className="secondary"
          onClick={() => navigate(`/hosts/${hostId}/services`)}
          disabled={busy !== null}
        >
          Cancel
        </button>
        <button className="primary" onClick={create} disabled={!ready || busy !== null}>
          {busy ? 'Working…' : 'Create service'}
        </button>
      </div>
    </Page>
  )
}

/** systemd wants a suffix; nobody types one. */
function fullName(name: string): string {
  const trimmed = name.trim()
  if (trimmed === '') return 'service.service'
  return trimmed.endsWith('.service') ? trimmed : `${trimmed}.service`
}

/**
 * The unit file the fields describe.
 *
 * After=network-online.target is here because almost everything a home server
 * runs wants the network up, and a service that starts too early fails in a way
 * that looks like the program's fault. RestartSec keeps a crash loop from
 * hammering the machine.
 */
function buildUnit(draft: Draft, unitName: string): string {
  const lines = [
    '[Unit]',
    `Description=${draft.description.trim() || unitName.replace(/\.service$/, '')}`,
    'After=network-online.target',
    'Wants=network-online.target',
    '',
    '[Service]',
    'Type=simple',
    `ExecStart=${draft.command.trim() || '/usr/local/bin/your-command'}`,
  ]
  if (draft.workingDir.trim()) lines.push(`WorkingDirectory=${draft.workingDir.trim()}`)
  if (draft.user.trim()) lines.push(`User=${draft.user.trim()}`)
  if (draft.restart !== 'no') lines.push(`Restart=${draft.restart}`, 'RestartSec=3')
  lines.push('', '[Install]', 'WantedBy=multi-user.target', '')
  return lines.join('\n')
}
