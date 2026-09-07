import { useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ApiError, api } from '../api'
import { Page } from '../components/Layout'
import { Badge, Banner, Card, Empty, Field, Loading, Sheet, useLoader } from '../components/ui'
import { ago, bytes } from '../lib/format'
import type { DirEntry, DirUsage } from '../types'

/**
 * The file browser. One directory at a time, the path in the query string so
 * the back button walks back up the way you came and a link to a directory is
 * a link someone can keep.
 *
 * HostMan already holds a key that can run anything on this machine, so the
 * listing is taken as root wherever sudo allows it — a browser that could not
 * open /etc would be hiding the reality rather than limiting it. Which account
 * it acted as is shown rather than assumed.
 */
export default function HostFiles() {
  const { id } = useParams()
  const hostId = Number(id)
  const [params, setParams] = useSearchParams()
  const path = params.get('path') ?? ''
  const navigate = useNavigate()

  const {
    data: listing,
    error,
    offline,
    reload,
  } = useLoader(() => api.files(hostId, path), [hostId, path])
  const [acting, setActing] = useState<DirEntry | null>(null)
  const [creating, setCreating] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)

  const go = (to: string) => setParams({ path: to })

  const open = (entry: DirEntry) => {
    const target = join(listing?.path ?? '/', entry.name)
    if (isDirectory(entry)) {
      go(target)
      return
    }
    if (entry.linkType === 'broken') {
      setNotice(`${entry.name} points at ${entry.target}, which isn't there.`)
      return
    }
    navigate(`/hosts/${hostId}/file?path=${encodeURIComponent(target)}`)
  }

  // Back walks up the tree, which is what a file browser's back means. At the
  // top there is nowhere further up, so it returns to the host.
  const up =
    listing && listing.path !== '/'
      ? `/hosts/${hostId}/files?path=${encodeURIComponent(listing.parent)}`
      : `/hosts/${hostId}`

  return (
    <Page
      title={listing ? name(listing.path) : 'Files'}
      back={up}
      action={
        <button className="ghost" onClick={() => setCreating(true)}>
          New
        </button>
      }
    >
      <Loading error={error} offline={offline} hasData={!!listing} />
      {notice && <Banner tone="warn">{notice}</Banner>}

      {listing && (
        <>
          <Card>
            <Crumbs path={listing.path} onGo={go} />
            <div className="row between sub" style={{ marginTop: 10 }}>
              <span>
                {listing.entries.length} item{listing.entries.length === 1 ? '' : 's'}
              </span>
              {/* Root is the difference between reading a config and editing
                  it, so it is stated rather than left to be discovered. */}
              <Badge tone={listing.asUser === 'root' ? 'accent' : 'neutral'}>
                as {listing.asUser || 'unknown'}
              </Badge>
            </div>
          </Card>

          {listing.truncated && (
            <Banner tone="warn">
              This directory has more in it than HostMan will list. Only the first{' '}
              {listing.entries.length} are shown.
            </Banner>
          )}

          {listing.entries.length === 0 ? (
            <Empty message="Nothing in here." />
          ) : (
            listing.entries.map((entry) => (
              <div className="card file-row" key={entry.name}>
                <button className="file-open" onClick={() => open(entry)}>
                  <FileIcon entry={entry} />
                  <span className="grow">
                    <span className="title">{entry.name}</span>
                    <span className="sub truncate">{describe(entry)}</span>
                  </span>
                  {isDirectory(entry) && <span className="chevron">›</span>}
                </button>
                <button
                  className="file-more"
                  onClick={() => setActing(entry)}
                  aria-label={`Actions for ${entry.name}`}
                >
                  ⋯
                </button>
              </div>
            ))
          )}
        </>
      )}

      {acting && listing && (
        <EntrySheet
          hostId={hostId}
          dir={listing.path}
          entry={acting}
          onClose={() => setActing(null)}
          onDone={(message) => {
            setActing(null)
            setNotice(message)
            reload()
          }}
        />
      )}

      {creating && listing && (
        <CreateSheet
          hostId={hostId}
          dir={listing.path}
          onClose={() => setCreating(false)}
          onFolder={() => {
            setCreating(false)
            reload()
          }}
          onFile={(target) =>
            navigate(`/hosts/${hostId}/file?path=${encodeURIComponent(target)}&new=1`)
          }
        />
      )}
    </Page>
  )
}

/** Crumbs turn the path into taps: every ancestor is one, root included. */
function Crumbs({ path, onGo }: { path: string; onGo: (to: string) => void }) {
  const parts = path.split('/').filter(Boolean)
  return (
    <div className="crumbs">
      <button className="crumb" onClick={() => onGo('/')}>
        /
      </button>
      {parts.map((part, i) => (
        <button
          key={i}
          className="crumb"
          onClick={() => onGo('/' + parts.slice(0, i + 1).join('/'))}
        >
          {part}
        </button>
      ))}
    </div>
  )
}

function FileIcon({ entry }: { entry: DirEntry }) {
  const stroke = {
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.6,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
  }
  return (
    <svg className={`file-icon ${isDirectory(entry) ? 'dir' : ''}`} viewBox="0 0 24 24" {...stroke}>
      {isDirectory(entry) ? (
        <path d="M3 7.5a2 2 0 0 1 2-2h3.6l1.8 2H19a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
      ) : (
        <>
          <path d="M6 3.5h7.5L18 8v12.5H6z" />
          <path d="M13.5 3.5V8H18" />
        </>
      )}
      {entry.type === 'link' && <path d="M9.5 14.5l5-5" />}
    </svg>
  )
}

/** describe is the one line under a name: what it is, how big, who owns it. */
function describe(entry: DirEntry): string {
  const parts: string[] = []
  if (entry.type === 'link') {
    parts.push(entry.linkType === 'broken' ? '↳ broken link' : `↳ ${entry.target}`)
  } else if (entry.type === 'dir') {
    parts.push('folder')
  } else if (entry.type === 'other') {
    parts.push('special file')
  } else {
    parts.push(bytes(entry.size))
  }
  if (entry.mode) parts.push(entry.mode)
  if (entry.owner) parts.push(entry.owner)
  if (entry.modifiedAt) parts.push(ago(entry.modifiedAt))
  return parts.join(' · ')
}

/** EntrySheet is what can be done to one entry from a phone: change its
 *  permissions, rename it, delete it. */
function EntrySheet({
  hostId,
  dir,
  entry,
  onClose,
  onDone,
}: {
  hostId: number
  dir: string
  entry: DirEntry
  onClose: () => void
  onDone: (message: string) => void
}) {
  const path = join(dir, entry.name)
  const [renaming, setRenaming] = useState(false)
  const [permissions, setPermissions] = useState(false)
  const [newName, setNewName] = useState(entry.name)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  // A directory with something in it is refused once, and only deleted when
  // the answer to "everything inside as well?" is yes.
  const [needsRecursive, setNeedsRecursive] = useState(false)

  const rename = async () => {
    setBusy(true)
    try {
      await api.renameFile(hostId, path, join(dir, newName.trim()))
      onDone(`Renamed to ${newName.trim()}.`)
    } catch (e) {
      setFailure(message(e))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (recursive: boolean) => {
    setBusy(true)
    try {
      await api.deleteFile(hostId, path, recursive)
      onDone(`Deleted ${entry.name}.`)
    } catch (e) {
      // 409 is the host saying the directory is not empty, which is a question
      // rather than a failure.
      if (e instanceof ApiError && e.status === 409) {
        setNeedsRecursive(true)
        setFailure(null)
      } else {
        setFailure(message(e))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet title={entry.name} subtitle={path} onClose={onClose}>
      {failure && <Banner tone="bad">{failure}</Banner>}

      {permissions ? (
        <Permissions
          hostId={hostId}
          entry={entry}
          path={path}
          onCancel={() => setPermissions(false)}
          onDone={onDone}
        />
      ) : renaming ? (
        <>
          <Field label="New name">
            <input value={newName} onChange={(e) => setNewName(e.target.value)} autoFocus />
          </Field>
          <div className="actions">
            <button className="secondary" onClick={() => setRenaming(false)}>
              Cancel
            </button>
            <button
              className="primary"
              onClick={rename}
              disabled={busy || !newName.trim() || newName.trim() === entry.name}
            >
              {busy ? 'Renaming…' : 'Rename'}
            </button>
          </div>
        </>
      ) : needsRecursive ? (
        <>
          <Banner tone="warn">
            {entry.name} is not empty. Deleting it takes everything inside with it.
          </Banner>
          <div className="actions">
            <button className="secondary" onClick={onClose}>
              Keep it
            </button>
            <button className="danger" onClick={() => remove(true)} disabled={busy}>
              {busy ? 'Deleting…' : 'Delete everything'}
            </button>
          </div>
        </>
      ) : confirming ? (
        <>
          <p className="sub">This deletes it on the host. There is no undo.</p>
          <div className="actions">
            <button className="secondary" onClick={() => setConfirming(false)}>
              Cancel
            </button>
            <button className="danger" onClick={() => remove(false)} disabled={busy}>
              {busy ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </>
      ) : (
        <>
          <div className="sub" style={{ marginBottom: 12 }}>
            {entry.mode && `mode ${entry.mode} · `}
            {entry.owner}
            {entry.group ? `:${entry.group}` : ''}
            {entry.type === 'file' ? ` · ${bytes(entry.size)}` : ''}
          </div>
          {isDirectory(entry) && <FolderUsage hostId={hostId} path={path} />}
          <button className="secondary block" onClick={() => setPermissions(true)}>
            Permissions
          </button>
          <div style={{ height: 10 }} />
          <button className="secondary block" onClick={() => setRenaming(true)}>
            Rename
          </button>
          <div style={{ height: 10 }} />
          <button className="danger block" onClick={() => setConfirming(true)}>
            Delete
          </button>
        </>
      )}
    </Sheet>
  )
}

/**
 * FolderUsage is the line a listing cannot give a folder: what is under it, all
 * the way down, and how much disk that takes. It is counted when the sheet
 * opens rather than with the listing, because a walk over every entry in a
 * tree is work worth doing for the one folder someone is looking at and not
 * for the forty they scrolled past.
 */
function FolderUsage({ hostId, path }: { hostId: number; path: string }) {
  const { data: usage, error, offline } = useLoader(() => api.usage(hostId, path), [hostId, path])
  if (offline) return null
  if (error) {
    return (
      <p className="sub" style={{ marginTop: -4, marginBottom: 12 }}>
        Could not measure it: {error}
      </p>
    )
  }
  if (!usage) {
    return (
      <p className="sub" style={{ marginTop: -4, marginBottom: 12 }}>
        Measuring…
      </p>
    )
  }
  return (
    <p className="sub" style={{ marginTop: -4, marginBottom: 12 }}>
      {describeUsage(usage)}
    </p>
  )
}

/** describeUsage is "12 files · 3 folders · 1.2 GB", with a caveat where the
 *  walk could not see everything. */
function describeUsage(usage: DirUsage): string {
  const line = `${plural(usage.files, 'file')} · ${plural(usage.dirs, 'folder')} · ${bytes(usage.bytes)} on disk`
  if (usage.unreadable > 0) {
    return `${line} · at least: ${plural(usage.unreadable, 'place')} could not be read`
  }
  return line
}

function plural(n: number, noun: string): string {
  return `${n.toLocaleString()} ${noun}${n === 1 ? '' : 's'}`
}

/**
 * Permissions is chmod, laid out as the three-by-three grid the octal digits
 * already are: who, then what they may do. The digits stay visible and stay
 * editable, because someone who came here knowing they want 640 should not have
 * to find it by tapping.
 *
 * On a folder, "everything inside it too" is `chmod -R`, and it means what it
 * says: the same mode reaches the files as well as the directories under it.
 * That is what makes 755 into 777 in one go, and it is also how a folder full
 * of configs becomes a folder full of executable configs, so it is off unless
 * it is asked for and the sheet says what it will do.
 */
function Permissions({
  hostId,
  entry,
  path,
  onCancel,
  onDone,
}: {
  hostId: number
  entry: DirEntry
  path: string
  onCancel: () => void
  onDone: (message: string) => void
}) {
  const [mode, setMode] = useState(() => normalizeMode(entry.mode, isDirectory(entry)))
  const [deep, setDeep] = useState(false)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)

  const folder = isDirectory(entry)
  const valid = /^[0-7]{3,4}$/.test(mode)
  // A four-digit mode carries setuid, setgid or the sticky bit in front. The
  // grid cannot show it, so it is named rather than silently carried along.
  const special = mode.length === 4 ? mode[0] : ''
  const bits = mode.slice(-3)

  const toggle = (who: number, bit: number) => {
    const digits = bits.split('')
    const value = Number(digits[who] ?? 0) ^ bit
    digits[who] = String(value)
    setMode(special + digits.join(''))
  }

  const apply = async () => {
    setBusy(true)
    setFailure(null)
    try {
      const result = await api.chmod(hostId, path, mode, folder && deep)
      onDone(
        folder && deep
          ? `${entry.name} and everything inside it are now ${result.mode}.`
          : `${entry.name} is now ${result.mode}.`,
      )
    } catch (e) {
      setFailure(message(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {failure && <Banner tone="bad">{failure}</Banner>}
      {entry.type === 'link' && (
        <Banner tone="warn">
          This is a symlink. Its own permissions mean nothing, so the mode goes to{' '}
          {entry.target} instead.
        </Banner>
      )}

      <div className="perms">
        {WHO.map((who, i) => (
          <div className="perm-row" key={who.key}>
            <span className="perm-who">{who.label}</span>
            <div className="perm-bits">
              {BITS.map((bit) => {
                const on = (Number(bits[i] ?? 0) & bit.value) !== 0
                return (
                  <button
                    key={bit.key}
                    className={`perm-bit ${on ? 'on' : ''}`}
                    aria-pressed={on}
                    aria-label={`${bit.label} for ${who.label}`}
                    onClick={() => toggle(i, bit.value)}
                    disabled={!valid}
                  >
                    {bit.key}
                  </button>
                )
              })}
            </div>
          </div>
        ))}
      </div>

      <Field
        label="Mode"
        help={valid ? symbolic(bits) : 'Three octal digits, or four with a special bit.'}
      >
        <input
          value={mode}
          inputMode="numeric"
          maxLength={4}
          onChange={(e) => setMode(e.target.value.replace(/[^0-7]/g, '').slice(0, 4))}
          aria-label="Mode in octal"
        />
      </Field>

      {special && special !== '0' && (
        <p className="sub">
          The leading {special} is a setuid, setgid or sticky bit. It stays unless you edit the
          digits yourself.
        </p>
      )}

      {folder && (
        <label className="checkbox">
          <input type="checkbox" checked={deep} onChange={(e) => setDeep(e.target.checked)} />
          Everything inside it too
        </label>
      )}
      {folder && deep && (
        <Banner tone="warn">
          Every file under {entry.name} gets {mode} as well, not just the folders.
        </Banner>
      )}

      <div className="actions">
        <button className="secondary" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary" onClick={apply} disabled={busy || !valid}>
          {busy ? 'Applying…' : 'Apply'}
        </button>
      </div>
    </>
  )
}

const WHO = [
  { key: 'owner', label: 'Owner' },
  { key: 'group', label: 'Group' },
  { key: 'others', label: 'Others' },
]

const BITS = [
  { key: 'r', label: 'read', value: 4 },
  { key: 'w', label: 'write', value: 2 },
  { key: 'x', label: 'execute', value: 1 },
]

/** normalizeMode makes what the host reported into digits the grid can edit,
 *  falling back to the usual mode for the kind of thing it is where the host
 *  said nothing — a blank grid would read as "no permissions at all". */
function normalizeMode(mode: string, folder: boolean): string {
  const digits = (mode ?? '').replace(/[^0-7]/g, '').slice(-4)
  if (digits.length < 3) return folder ? '755' : '644'
  return digits
}

/** symbolic turns 755 into rwxr-xr-x, the form the same bits are read in. */
function symbolic(bits: string): string {
  return bits
    .split('')
    .map((digit) => {
      const value = Number(digit)
      return BITS.map((bit) => ((value & bit.value) !== 0 ? bit.key : '-')).join('')
    })
    .join('')
}

/** CreateSheet makes an empty file or a folder here. A new file opens straight
 *  into the editor rather than being written empty and found later. */
function CreateSheet({
  hostId,
  dir,
  onClose,
  onFolder,
  onFile,
}: {
  hostId: number
  dir: string
  onClose: () => void
  onFolder: () => void
  onFile: (path: string) => void
}) {
  const [kind, setKind] = useState<'file' | 'folder'>('file')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)

  const create = async () => {
    const target = join(dir, name.trim())
    if (kind === 'file') {
      onFile(target)
      return
    }
    setBusy(true)
    try {
      await api.mkdir(hostId, target)
      onFolder()
    } catch (e) {
      setFailure(message(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet title="New" subtitle={`in ${dir}`} onClose={onClose}>
      {failure && <Banner tone="bad">{failure}</Banner>}
      <div className="segmented">
        <button className={kind === 'file' ? 'on' : ''} onClick={() => setKind('file')}>
          File
        </button>
        <button className={kind === 'folder' ? 'on' : ''} onClick={() => setKind('folder')}>
          Folder
        </button>
      </div>
      <Field label="Name">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={kind === 'file' ? 'notes.conf' : 'backups'}
          autoFocus
        />
      </Field>
      <div className="actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button className="primary" onClick={create} disabled={busy || !name.trim()}>
          {busy ? 'Creating…' : 'Create'}
        </button>
      </div>
    </Sheet>
  )
}

/** A symlink to a directory opens as one; a broken one opens as nothing. */
function isDirectory(entry: DirEntry): boolean {
  return entry.type === 'dir' || (entry.type === 'link' && entry.linkType === 'dir')
}

export function join(dir: string, name: string): string {
  return dir === '/' ? `/${name}` : `${dir}/${name}`
}

export function name(path: string): string {
  const parts = path.split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1] : '/'
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
