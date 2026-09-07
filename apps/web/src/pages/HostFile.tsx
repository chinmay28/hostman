import { useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api } from '../api'
import { Page } from '../components/Layout'
import { Badge, Banner, Card, Loading, useLoader } from '../components/ui'
import { bytes, time } from '../lib/format'
import type { HostFile as HostFileData } from '../types'

/**
 * One file: read it, and edit it if it is text.
 *
 * Editing is deliberately narrow. A file that came back truncated cannot be
 * saved from here — the editor only holds the first part of it, and writing
 * that back would silently throw the rest away — and a binary file is shown as
 * what it is rather than run through a textarea that would mangle it.
 */
export default function HostFile() {
  const { id } = useParams()
  const hostId = Number(id)
  const [params] = useSearchParams()
  const path = params.get('path') ?? ''
  // A file created from the browser does not exist yet, so there is nothing to
  // fetch: it opens as an empty editor and comes into being when it is saved.
  const isNew = params.get('new') === '1'
  const navigate = useNavigate()

  // The loaded file is tagged with the path it was asked for. A failed reload
  // leaves the last good answer in place — which is right for a screen that
  // keeps showing what it knows — but that answer must never end up in an
  // editor labelled with a different file, because saving would then write one
  // file's contents over another.
  const {
    data: loaded,
    error,
    offline,
    reload,
  } = useLoader(
    async () => ({ forPath: path, file: isNew ? blank(path) : await api.file(hostId, path) }),
    [hostId, path, isNew],
  )
  const file = loaded?.forPath === path ? loaded.file : null

  // draft is null when reading, and the working copy when editing.
  const [draft, setDraft] = useState<string | null>(isNew ? '' : null)
  const [saving, setSaving] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const editable = !!file && !file.binary && !file.truncated
  const dirty = draft !== null && draft !== (file?.content ?? '')

  const save = async () => {
    if (draft === null) return
    setSaving(true)
    setFailure(null)
    try {
      await api.saveFile(hostId, path, draft)
      setDraft(null)
      setSaved(true)
      // Drop ?new so a reload of this screen reads the real file.
      if (isNew)
        navigate(`/hosts/${hostId}/file?path=${encodeURIComponent(path)}`, { replace: true })
      else reload()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  const folder = parent(path)

  return (
    <Page
      title={base(path)}
      back={`/hosts/${hostId}/files?path=${encodeURIComponent(folder)}`}
      action={
        draft === null ? (
          editable ? (
            <button className="ghost" onClick={() => setDraft(file?.content ?? '')}>
              Edit
            </button>
          ) : undefined
        ) : (
          <button className="ghost" onClick={save} disabled={saving || !dirty}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        )
      }
    >
      <Loading error={error} offline={offline} hasData={!!file} />
      {failure && <Banner tone="bad">{failure}</Banner>}
      {saved && draft === null && <Banner tone="good">Saved to {path}.</Banner>}

      {file && (
        <>
          <Card>
            <div className="row between">
              <div className="grow">
                <div className="sub mono truncate">{file.path}</div>
                <div className="sub" style={{ marginTop: 4 }}>
                  {isNew ? (
                    'New file — it is created when you save.'
                  ) : (
                    <>
                      {bytes(file.size)} · {file.mode} · {file.owner}
                      {file.group ? `:${file.group}` : ''}
                      {file.modifiedAt ? ` · ${time(file.modifiedAt)}` : ''}
                    </>
                  )}
                </div>
              </div>
              {!isNew && (
                <Badge tone={file.asUser === 'root' ? 'accent' : 'neutral'}>
                  as {file.asUser || 'unknown'}
                </Badge>
              )}
            </div>
          </Card>

          {file.binary && (
            <Banner tone="warn">
              This isn't text, so there is nothing safe to show or edit. HostMan leaves it alone.
            </Banner>
          )}
          {file.truncated && (
            <Banner tone="warn">
              Only the first {bytes(file.content.length)} of {bytes(file.size)} are shown, so this
              file can't be edited from here — saving would throw the rest away.
            </Banner>
          )}

          {draft !== null ? (
            <>
              <textarea
                className="editor"
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                spellCheck={false}
                autoCapitalize="off"
                autoCorrect="off"
                autoComplete="off"
                aria-label={`Contents of ${path}`}
              />
              <div className="actions">
                <button
                  className="secondary"
                  onClick={() => {
                    setDraft(null)
                    setFailure(null)
                    if (isNew) navigate(`/hosts/${hostId}/files?path=${encodeURIComponent(folder)}`)
                  }}
                  disabled={saving}
                >
                  {dirty ? 'Discard' : 'Cancel'}
                </button>
                <button className="primary" onClick={save} disabled={saving || !dirty}>
                  {saving ? 'Saving…' : 'Save'}
                </button>
              </div>
              <p className="sub" style={{ marginTop: 12 }}>
                Saving writes through a temporary file in the same directory, so the file is
                replaced whole and keeps its permissions and owner.
              </p>
            </>
          ) : (
            !file.binary && <pre className="log">{file.content || '(empty file)'}</pre>
          )}
        </>
      )}
    </Page>
  )
}

/** blank is the file a "new file" opens on, before it exists on the host. */
function blank(path: string): HostFileData {
  return {
    path,
    size: 0,
    mode: '',
    owner: '',
    group: '',
    modifiedAt: '',
    content: '',
    truncated: false,
    binary: false,
    asUser: '',
  }
}

function base(path: string): string {
  const parts = path.split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1] : path
}

function parent(path: string): string {
  const parts = path.split('/').filter(Boolean)
  parts.pop()
  return parts.length ? '/' + parts.join('/') : '/'
}
