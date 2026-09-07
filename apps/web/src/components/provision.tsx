import { useState } from 'react'
import { api } from '../api'
import { Badge, Banner, Field, Sheet } from './ui'
import type { ProvisionResult } from '../types'

/**
 * Setting a host up needs a password once — to authorize HostMan's key and to
 * grant passwordless sudo. It is held only for the length of that request:
 * typed here, sent once, cleared as soon as the answer comes back, and never
 * written down on either side.
 */

/** PasswordField is the one-time credential input, shared by both entry points. */
export function PasswordField({
  value,
  onChange,
  username,
  required = false,
}: {
  value: string
  onChange: (value: string) => void
  username: string
  required?: boolean
}) {
  return (
    <Field
      label={`Password for ${username || 'the SSH user'}`}
      help="Used once to authorize HostMan's key and enable passwordless sudo. It isn't stored — not in the database, not in the log."
    >
      <input
        type="password"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="••••••••"
        autoCapitalize="none"
        autoCorrect="off"
        autoComplete="off"
        required={required}
      />
    </Field>
  )
}

/** Steps is the record of what setup did, in the order it happened. */
export function Steps({ result }: { result: ProvisionResult }) {
  // The step that failed is what the banner is already quoting, so showing its
  // detail again would just print the same line twice.
  const detailOf = (detail: string | undefined) => (detail === result.error ? undefined : detail)
  return (
    <>
      {result.ok ? (
        <Banner tone={result.sudoOk ? 'good' : 'warn'}>
          {result.sudoOk
            ? 'The host is set up. HostMan can reach it with its own key.'
            : 'HostMan can reach the host, but passwordless sudo is still missing.'}
        </Banner>
      ) : (
        <Banner tone="bad">{result.error ?? 'Setup did not finish.'}</Banner>
      )}

      <div style={{ margin: '14px 0' }}>
        {result.steps.map((step, i) => (
          <div key={`${step.name}-${i}`} style={{ padding: '5px 0' }}>
            <div className="row between">
              <span className="sub grow">{step.name}</span>
              <Badge tone={step.ok ? 'good' : 'bad'}>{step.ok ? 'Done' : 'Failed'}</Badge>
            </div>
            {detailOf(step.detail) && (
              <div className="sub mono" style={{ marginTop: 4, fontSize: 12 }}>
                {step.detail}
              </div>
            )}
          </div>
        ))}
      </div>

      {result.hints?.map((hint) => (
        <p key={hint} className="sub">
          {hint}
        </p>
      ))}
    </>
  )
}

/**
 * SetupSheet asks for the password and runs setup, for a host that already
 * exists — one added before, or one whose setup needs another go.
 */
export function SetupSheet({
  hostId,
  username,
  address,
  port,
  onClose,
  onFinished,
}: {
  hostId: number
  username: string
  address: string
  port: number
  onClose: () => void
  onFinished?: () => void
}) {
  const where = `${username}@${address}${port !== 22 ? `:${port}` : ''}`
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<ProvisionResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  const run = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const outcome = await api.provisionHost(hostId, password)
      setResult(outcome)
      onFinished?.()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      // Whatever happened, the password has served its purpose.
      setPassword('')
      setBusy(false)
    }
  }

  if (result) {
    return (
      <Sheet title={result.ok ? 'Host is set up' : 'Setup did not finish'} onClose={onClose}>
        <Steps result={result} />
        <button className="primary block" onClick={onClose}>
          Done
        </button>
      </Sheet>
    )
  }

  return (
    <Sheet
      title="Set up access"
      subtitle={`Signs in to ${where} once, authorizes HostMan's key, and enables passwordless sudo.`}
      onClose={onClose}
    >
      <form onSubmit={run}>
        {error && <Banner tone="bad">{error}</Banner>}
        <PasswordField value={password} onChange={setPassword} username={username} required />
        <div className="actions">
          <button className="secondary" type="button" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button className="primary" type="submit" disabled={busy || password === ''}>
            {busy ? 'Setting up…' : 'Set up'}
          </button>
        </div>
      </form>
    </Sheet>
  )
}
