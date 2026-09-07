import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { PasswordField, Steps } from '../components/provision'
import { Banner, Card, Field, Sheet } from '../components/ui'
import type { ProvisionResult } from '../types'

/** Add or edit a host. Both use the same fields, so they share a screen. */
export default function HostForm() {
  const { id } = useParams()
  const hostId = id ? Number(id) : null
  const navigate = useNavigate()

  const [name, setName] = useState('')
  const [address, setAddress] = useState('')
  const [port, setPort] = useState('22')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [loaded, setLoaded] = useState(hostId === null)
  // The host exists from the moment it is created, so a retry after a failed
  // setup must not create a second one.
  const [createdId, setCreatedId] = useState<number | null>(null)
  const [setup, setSetup] = useState<ProvisionResult | null>(null)

  useEffect(() => {
    if (hostId === null) return
    api
      .host(hostId)
      .then((host) => {
        setName(host.name)
        setAddress(host.address)
        setPort(String(host.port))
        setUsername(host.username)
        setLoaded(true)
      })
      .catch((e) => setError(e.message))
  }, [hostId])

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError(null)
    const input = { name, address, port: Number(port) || 22, username }
    try {
      let targetId = createdId
      if (hostId !== null) {
        await api.updateHost(hostId, input)
        targetId = hostId
      } else if (targetId === null) {
        targetId = (await api.createHost(input)).id
        setCreatedId(targetId)
      } else {
        // Created on an earlier attempt: keep the record in step with the form.
        await api.updateHost(targetId, input)
      }

      // With a password, do the setup the two commands in Settings would do.
      // Without one, the host is saved as before and the user does it by hand.
      if (hostId === null && password !== '') {
        const result = await api.provisionHost(targetId, password)
        setPassword('')
        setSetup(result)
        setSaving(false)
        return
      }
      navigate(`/hosts/${targetId}`, { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSaving(false)
    }
  }

  return (
    <Page title={hostId === null ? 'Add host' : 'Edit host'} back={hostId === null ? '/hosts' : `/hosts/${hostId}`}>
      {error && <Banner tone="bad">{error}</Banner>}
      {!loaded ? (
        <div className="empty">Loading…</div>
      ) : (
        <form onSubmit={save}>
          <Card>
            <Field label="Name" help="What you'll call it in HostMan.">
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="nakedpi"
                autoCapitalize="none"
                autoCorrect="off"
                required
              />
            </Field>
            <Field label="Address" help="Hostname or IP address, reachable over SSH.">
              <input
                value={address}
                onChange={(e) => setAddress(e.target.value)}
                placeholder="nakedpi.local"
                autoCapitalize="none"
                autoCorrect="off"
                inputMode="url"
                required
              />
            </Field>
            <Field label="SSH user" help="Needs passwordless sudo for installs that require root.">
              <input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="pi"
                autoCapitalize="none"
                autoCorrect="off"
                required
              />
            </Field>
            <Field label="Port">
              <input
                value={port}
                onChange={(e) => setPort(e.target.value)}
                inputMode="numeric"
                pattern="[0-9]*"
                placeholder="22"
              />
            </Field>
          </Card>

          {hostId === null && (
            <Card>
              <div className="meter-label">
                <span>Set it up for me</span>
              </div>
              <p className="sub" style={{ marginTop: 0 }}>
                Give HostMan the host's password once and it will authorize its own key and enable
                passwordless sudo for you. Leave it empty to run the two commands in Settings by hand
                instead.
              </p>
              <PasswordField value={password} onChange={setPassword} username={username} />
            </Card>
          )}

          <p className="sub" style={{ margin: '0 4px 12px' }}>
            {hostId === null && password === ''
              ? "After saving, HostMan will try to connect. If it can't, Settings has the public key and the two commands to run on the host."
              : 'HostMan connects straight after saving, so you can see the result right away.'}
          </p>

          <button className="primary block" type="submit" disabled={saving}>
            {saving ? (password !== '' ? 'Setting up…' : 'Saving…') : hostId === null ? 'Add host' : 'Save changes'}
          </button>
        </form>
      )}

      {setup && createdId !== null && (
        <Sheet
          title={setup.ok ? 'Host is ready' : 'Host saved, setup did not finish'}
          onClose={() => navigate(`/hosts/${createdId}`, { replace: true })}
        >
          <Steps result={setup} />
          {!setup.ok && (
            <p className="sub">
              The host is saved either way. Fix what's above and try again from the host's page, or
              run the two commands in Settings on the machine.
            </p>
          )}
          <button
            className="primary block"
            onClick={() => navigate(`/hosts/${createdId}`, { replace: true })}
          >
            Done
          </button>
        </Sheet>
      )}
    </Page>
  )
}
