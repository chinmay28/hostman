/**
 * Picking .torrent files is a queue too: however many were picked, each one
 * read, checked, and handed to the host on a request of its own. One request
 * per file keeps the server's contract what it was — one torrent, one answer —
 * and means a file deluge will not take costs that file and not the nine
 * picked with it. This module is the queue and what is said about it
 * afterwards, kept apart from the picker and the wire so it can be tested
 * without either.
 */

/** The largest file worth carrying to a host, matching what the server will
 *  accept. A torrent file is a list of hashes: a few kilobytes is ordinary and
 *  a megabyte is a very large one. */
export const MAX_TORRENT_BYTES = 4 * 1024 * 1024

/** What the browser hands over for one picked file: enough of File to decide
 *  whether it is worth reading. */
export interface PickedTorrent {
  name: string
  size: number
}

/** What tells two picks of the same file apart from two files: its name, its
 *  size, and when it was last changed where the browser says. */
export interface Pick extends PickedTorrent {
  lastModified?: number
}

/**
 * mergePicks adds a later pick to an earlier one. A phone's picker cannot be
 * relied on to hand over several files at once — some hand over one, and some
 * hand over nothing when several were ticked — so the list on the screen grows
 * with every pick rather than being replaced by it, and what is finally sent
 * is whatever it holds. The same file picked twice stays once.
 */
export function mergePicks<F extends Pick>(existing: F[], added: F[]): F[] {
  const key = (file: Pick) => `${file.name}\0${file.size}\0${file.lastModified ?? ''}`
  const seen = new Set(existing.map(key))
  const files = existing.slice()
  for (const file of added) {
    if (seen.has(key(file))) continue
    seen.add(key(file))
    files.push(file)
  }
  return files
}

/** How one picked file ended. */
export type TorrentOutcome =
  | { name: string; state: 'added' }
  /** It never left the phone: too large, unreadable, or not a torrent. */
  | { name: string; state: 'rejected'; reason: string }
  /** It was sent, and the host or deluge would not take it. */
  | { name: string; state: 'failed'; reason: string }

/** Where the queue has got to, for the button on the screen. */
export interface TorrentProgress {
  /** The file going now, counted from one. */
  current: number
  total: number
}

/** Reader gives a picked file's bytes. */
export type TorrentReader<F extends PickedTorrent> = (file: F) => Promise<Uint8Array>

/** Adder hands one file to the host, base64, and throws if it is refused. */
export type TorrentAdder<F extends PickedTorrent> = (file: F, base64: string) => Promise<unknown>

/**
 * The same rule the server applies: a torrent file is a bencoded dictionary
 * with an info dictionary inside it.
 *
 * Asking here as well is not distrust of the server — it still refuses
 * everything this does. It is that the picker offers every file on the phone,
 * so picking the wrong one is easy, and being told which file it was beats a
 * round trip to the host to be told the same thing about none.
 */
export function looksLikeATorrent(text: string): boolean {
  return text.startsWith('d') && text.includes('4:info')
}

/**
 * A file's bytes as a string, one character a byte: what the check above reads
 * and what btoa takes.
 *
 * It is built a chunk at a time because String.fromCharCode is given the bytes
 * as arguments, and a megabyte of them in one call is how a browser is made to
 * throw "too many arguments" on a file that is otherwise perfectly ordinary.
 */
export function binaryString(bytes: Uint8Array): string {
  const chunk = 8192
  let binary = ''
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return binary
}

/**
 * addPicked sends the picked files in order and reports how each one ended,
 * one outcome per file and in the order they were picked.
 *
 * A file that is plainly not a torrent is never sent: nothing that size is a
 * torrent file, and a phone should not spend a minute uploading a video to be
 * told so by the host. A failure does not stop the rest — they are separate
 * torrents, and the one deluge refused says nothing about the others.
 */
export async function addPicked<F extends PickedTorrent>(
  files: F[],
  read: TorrentReader<F>,
  add: TorrentAdder<F>,
  onProgress: (p: TorrentProgress) => void = () => {},
): Promise<TorrentOutcome[]> {
  const outcomes: TorrentOutcome[] = []
  for (const [index, file] of files.entries()) {
    const name = file.name
    onProgress({ current: index + 1, total: files.length })

    if (file.size > MAX_TORRENT_BYTES) {
      outcomes.push({ name, state: 'rejected', reason: 'is too large to be a .torrent file' })
      continue
    }
    let binary: string
    try {
      binary = binaryString(await read(file))
    } catch {
      outcomes.push({ name, state: 'rejected', reason: 'could not be read' })
      continue
    }
    if (!looksLikeATorrent(binary)) {
      outcomes.push({ name, state: 'rejected', reason: 'is not a .torrent file' })
      continue
    }
    try {
      await add(file, btoa(binary))
      outcomes.push({ name, state: 'added' })
    } catch (e) {
      outcomes.push({ name, state: 'failed', reason: e instanceof Error ? e.message : String(e) })
    }
  }
  return outcomes
}

/** How many files that went wrong are named before the rest are a number. */
const NAMED_TROUBLES = 3

/**
 * troubles is what is said when the queue is done and not everything went: null
 * when every file was added — the torrents appearing in the list is the
 * success message — and otherwise which files did not go and why, after how
 * many did, so nobody has to count bars to find out.
 */
export function troubles(outcomes: TorrentOutcome[]): string | null {
  const wrong = outcomes.filter((o) => o.state !== 'added')
  if (wrong.length === 0) return null

  const said = wrong.slice(0, NAMED_TROUBLES).map((o) =>
    // What the phone decided reads as a sentence about the file; what the host
    // said is its own sentence, so it is quoted after the name instead.
    o.state === 'rejected' ? `${o.name} ${o.reason}` : `${o.name}: ${o.reason}`,
  )
  if (outcomes.length === 1) return endSentence(said[0])

  const added = outcomes.length - wrong.length
  const parts = [`Added ${added} of ${outcomes.length} torrents.`, endSentence(said.join('; '))]
  const unnamed = wrong.length - said.length
  if (unnamed > 0) parts.push(`${unnamed} more could not be added either.`)
  return parts.join(' ')
}

function endSentence(text: string): string {
  return /[.!?]$/.test(text) ? text : `${text}.`
}
