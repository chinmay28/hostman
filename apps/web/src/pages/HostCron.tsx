import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { Banner, Card, Loading, SectionTitle, useLoader } from '../components/ui'

/** Starters for the schedules a home server actually uses. Tapping one appends
 *  a line to fill in, which beats typing five fields on a phone keyboard. */
const SCHEDULES = [
  { label: 'Every hour', spec: '0 * * * *' },
  { label: 'Every day 3am', spec: '0 3 * * *' },
  { label: 'Every Sunday', spec: '0 4 * * 0' },
  { label: 'Every 15 min', spec: '*/15 * * * *' },
  { label: 'At boot', spec: '@reboot' },
]

/**
 * The crontab editor. The whole file is edited at once, the way `crontab -e`
 * does it, and cron is what validates it: a crontab it refuses is not
 * installed, and its complaint is what comes back.
 */
export default function HostCron() {
  const { id } = useParams()
  const hostId = Number(id)

  const { data: host } = useLoader(() => api.host(hostId), [hostId])
  // "" means the account HostMan signs in as, which needs no privileges.
  const [user, setUser] = useState('')
  // The crontab that comes back names the user it belongs to. A read that fails
  // — asking for root's without sudo, say — leaves the last good one in place,
  // and installing that under the other name would move someone's jobs from one
  // account to another. So the editor only opens on a crontab that matches.
  const {
    data: loaded,
    error,
    offline,
    reload,
  } = useLoader(() => api.crontab(hostId, user), [hostId, user])
  const cron = loaded && loaded.user === (user || host?.username) ? loaded : null

  const [draft, setDraft] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const [saved, setSaved] = useState<string | null>(null)

  // Switching users switches crontabs; an unsaved draft belongs to the one it
  // was typed into, so it does not follow.
  useEffect(() => {
    setDraft(null)
    setFailure(null)
    setSaved(null)
  }, [user])

  const content = draft ?? cron?.content ?? ''
  const dirty = draft !== null && draft !== (cron?.content ?? '')

  const append = (spec: string) => {
    const base = content.endsWith('\n') || content === '' ? content : content + '\n'
    setDraft(`${base}${spec} /path/to/command\n`)
  }

  const save = async () => {
    setSaving(true)
    setFailure(null)
    setSaved(null)
    try {
      const updated = await api.saveCrontab(hostId, user, draft ?? '')
      setDraft(null)
      setSaved(`Installed ${updated.user}'s crontab.`)
      reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  // Where HostMan already signs in as root, "the SSH user" and "root" are the
  // same crontab, and offering both would be offering a choice that isn't one.
  const own = host?.username ?? 'this user'
  const separateRoot = !!host && host.username !== 'root'

  return (
    <Page
      title="Scheduled jobs"
      back={`/hosts/${hostId}`}
      action={
        <button className="ghost" onClick={save} disabled={!dirty || saving}>
          {saving ? 'Saving…' : 'Save'}
        </button>
      }
    >
      <Loading error={error} offline={offline} hasData={!!cron} />
      {failure && <Banner tone="bad">{failure}</Banner>}
      {saved && <Banner tone="good">{saved}</Banner>}

      {separateRoot && (
        <div className="segmented">
          <button className={user === '' ? 'on' : ''} onClick={() => setUser('')}>
            {own}
          </button>
          <button className={user === 'root' ? 'on' : ''} onClick={() => setUser('root')}>
            root
          </button>
        </div>
      )}

      {user === 'root' && host && !host.sudoOk && (
        <Banner tone="warn">
          {host.username} doesn't have passwordless sudo here, so root's crontab can't be read or
          written. Set up access from the host's page first.
        </Banner>
      )}

      {cron && !cron.exists && !dirty && (
        <Banner tone="warn">
          {cron.user} has no crontab yet. Adding a line and saving creates one.
        </Banner>
      )}

      {!cron ? (
        <Card>
          <div className="sub">Reading {user || own}'s crontab…</div>
        </Card>
      ) : (
        <>
          <textarea
            className="editor"
            value={content}
            onChange={(e) => setDraft(e.target.value)}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            autoComplete="off"
            placeholder={
              '# minute hour day month weekday  command\n0 3 * * * /usr/local/bin/backup.sh'
            }
            aria-label="Crontab"
          />

          <div className="chips">
            {SCHEDULES.map((schedule) => (
              <button key={schedule.spec} className="chip" onClick={() => append(schedule.spec)}>
                {schedule.label}
              </button>
            ))}
          </div>

          <div className="actions">
            <button
              className="secondary"
              onClick={() => setDraft(null)}
              disabled={!dirty || saving}
            >
              Discard
            </button>
            <button className="primary" onClick={save} disabled={!dirty || saving}>
              {saving ? 'Saving…' : 'Save'}
            </button>
          </div>
        </>
      )}

      <SectionTitle>How a line reads</SectionTitle>
      <Card>
        <pre className="mono" style={{ margin: 0, overflowX: 'auto' }}>
          {
            '┌── minute (0-59)\n│ ┌── hour (0-23)\n│ │ ┌── day of month (1-31)\n│ │ │ ┌── month (1-12)\n│ │ │ │ ┌── day of week (0-6, Sunday is 0)\n│ │ │ │ │\n0 3 * * *  /usr/local/bin/backup.sh'
          }
        </pre>
        <p className="sub" style={{ marginBottom: 0 }}>
          Jobs run with a bare environment and a short PATH, so use full paths to commands. A
          percent sign means "newline" to cron and has to be written{' '}
          <span className="mono">\%</span>. Cron checks the whole file when it is saved: if it
          refuses one line, nothing changes.
        </p>
      </Card>
    </Page>
  )
}
