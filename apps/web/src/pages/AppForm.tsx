import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { Banner, Card, Field, SectionTitle } from '../components/ui'
import type { HealthType, Param } from '../types'

const blankParam: Param = { name: '', label: '', default: '', help: '', required: false }

export default function AppForm() {
  const { id } = useParams()
  const appId = id ? Number(id) : null
  const navigate = useNavigate()

  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [installCommand, setInstallCommand] = useState('')
  const [uninstallCommand, setUninstallCommand] = useState('')
  const [params, setParams] = useState<Param[]>([])
  const [healthType, setHealthType] = useState<HealthType>('none')
  const [healthTarget, setHealthTarget] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [loaded, setLoaded] = useState(appId === null)

  useEffect(() => {
    if (appId === null) return
    api
      .app(appId)
      .then((app) => {
        setName(app.name)
        setDescription(app.description)
        setInstallCommand(app.installCommand)
        setUninstallCommand(app.uninstallCommand)
        setParams(app.params)
        setHealthType(app.healthType)
        setHealthTarget(app.healthTarget)
        setLoaded(true)
      })
      .catch((e) => setError(e.message))
  }, [appId])

  const updateParam = (index: number, patch: Partial<Param>) =>
    setParams((current) => current.map((p, i) => (i === index ? { ...p, ...patch } : p)))

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError(null)
    const input = {
      name,
      description,
      installCommand,
      uninstallCommand,
      // A parameter with no name is a row the user started and abandoned.
      params: params.filter((p) => p.name.trim() !== ''),
      healthType,
      healthTarget,
    }
    try {
      const app = appId === null ? await api.createApp(input) : await api.updateApp(appId, input)
      navigate(`/apps/${app.id}`, { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSaving(false)
    }
  }

  return (
    <Page title={appId === null ? 'Add app' : 'Edit app'} back={appId === null ? '/apps' : `/apps/${appId}`}>
      {error && <Banner tone="bad">{error}</Banner>}
      {!loaded ? (
        <div className="empty">Loading…</div>
      ) : (
        <form onSubmit={save}>
          <Card>
            <Field label="Name">
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="CountRoster" required />
            </Field>
            <Field label="Description">
              <input
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Roster counter PWA"
              />
            </Field>
            <Field
              label="Install command"
              help="Runs on the host as the SSH user. Use {{name}} for a parameter, and {{host}}, {{hostname}} or {{user}} for the host itself. Don't quote placeholders — values are quoted for you."
            >
              <textarea
                value={installCommand}
                onChange={(e) => setInstallCommand(e.target.value)}
                placeholder="curl -fsSL https://raw.githubusercontent.com/chinmay28/countroster/main/scripts/quickstart.sh | sudo bash"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                required
              />
            </Field>
            <Field
              label="Uninstall command"
              help="Optional. Runs the same way, with the parameters the install was given. Without one an app can only be forgotten, not removed from a host."
            >
              <textarea
                value={uninstallCommand}
                onChange={(e) => setUninstallCommand(e.target.value)}
                placeholder="curl -fsSL https://raw.githubusercontent.com/chinmay28/countroster/main/scripts/quickstart.sh | sudo bash -s -- --uninstall"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
              />
            </Field>
          </Card>

          <SectionTitle>Parameters</SectionTitle>
          <p className="sub" style={{ margin: '0 4px 12px' }}>
            Values you confirm before each deploy. HostMan remembers what you used last time.
          </p>
          {params.map((param, index) => (
            <Card key={index}>
              <Field label="Placeholder name" help={`Used as {{${param.name || 'name'}}} in the command.`}>
                <input
                  value={param.name}
                  onChange={(e) => updateParam(index, { name: e.target.value })}
                  placeholder="port"
                  autoCapitalize="none"
                  autoCorrect="off"
                />
              </Field>
              <Field label="Label">
                <input
                  value={param.label}
                  onChange={(e) => updateParam(index, { label: e.target.value })}
                  placeholder="Port"
                />
              </Field>
              <Field label="Default value">
                <input
                  value={param.default}
                  onChange={(e) => updateParam(index, { default: e.target.value })}
                  placeholder="8787"
                  autoCapitalize="none"
                  autoCorrect="off"
                />
              </Field>
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={param.required}
                  onChange={(e) => updateParam(index, { required: e.target.checked })}
                />
                Required
              </label>
              <button
                type="button"
                className="danger block"
                onClick={() => setParams((current) => current.filter((_, i) => i !== index))}
              >
                Remove parameter
              </button>
            </Card>
          ))}
          <button
            type="button"
            className="secondary block"
            onClick={() => setParams((current) => [...current, { ...blankParam }])}
          >
            Add a parameter
          </button>

          <SectionTitle>Health check</SectionTitle>
          <Card>
            <Field label="Type" help="How HostMan decides whether the app is actually running.">
              <select value={healthType} onChange={(e) => setHealthType(e.target.value as HealthType)}>
                <option value="none">None</option>
                <option value="http">HTTP request</option>
                <option value="systemd">systemd unit</option>
              </select>
            </Field>
            {healthType === 'http' && (
              <Field
                label="URL"
                help="Checked from HostMan, once per host. Write {HOST} for the machine it's deployed on — {HOST}.local is fine either way — and {PORT} for a parameter."
              >
                <input
                  value={healthTarget}
                  onChange={(e) => setHealthTarget(e.target.value)}
                  placeholder="http://{HOST}:8787/"
                  autoCapitalize="none"
                  autoCorrect="off"
                  inputMode="url"
                />
              </Field>
            )}
            {healthType === 'systemd' && (
              <Field label="Unit" help="Checked with systemctl is-active over SSH. {HOST} and parameters work here too.">
                <input
                  value={healthTarget}
                  onChange={(e) => setHealthTarget(e.target.value)}
                  placeholder="countroster.service"
                  autoCapitalize="none"
                  autoCorrect="off"
                />
              </Field>
            )}
          </Card>

          <button className="primary block" type="submit" disabled={saving}>
            {saving ? 'Saving…' : appId === null ? 'Add app' : 'Save changes'}
          </button>
        </form>
      )}
    </Page>
  )
}
