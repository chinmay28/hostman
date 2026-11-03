/**
 * Uploading is a queue: the files someone picked, sent to the host one at a
 * time, each on its own request. One request per file keeps every request
 * small enough to fail and be retried on its own, and lets the screen say
 * which file is going and how far it has got. This module is the queue —
 * what goes, in what order, how it went — kept apart from both the picker
 * and the wire so it can be tested without either.
 */

/** What the browser hands over for one picked file: enough of File to plan
 *  an upload. A file picked as part of a folder carries its path inside it. */
export interface Picked {
  name: string
  size: number
  webkitRelativePath?: string
}

/** One file to send, and the name it takes under the destination folder. */
export interface UploadItem<F extends Picked = File> {
  file: F
  /** Relative to the folder being uploaded into: "photo.jpg", or
   *  "trip/day1/photo.jpg" for a file inside a picked folder. */
  name: string
}

/**
 * plan turns a pick into the queue. A file from a folder keeps the folder's
 * structure in its name, so the host rebuilds the folder as the files arrive;
 * a file picked on its own goes by its own name. Picking the same name twice
 * sends it once.
 */
export function plan<F extends Picked>(files: Iterable<F>): UploadItem<F>[] {
  const seen = new Set<string>()
  const items: UploadItem<F>[] = []
  for (const file of files) {
    const name = relativeName(file)
    if (!name || seen.has(name)) continue
    seen.add(name)
    items.push({ file, name })
  }
  return items
}

/**
 * merge adds a later pick to an earlier one. The picker on a phone often
 * hands over one file at a time, so a list that is replaced by each pick
 * could never hold two; this one grows, and a name picked twice stays once.
 */
export function merge<F extends Picked>(existing: UploadItem<F>[], added: UploadItem<F>[]): UploadItem<F>[] {
  const seen = new Set(existing.map((item) => item.name))
  const items = existing.slice()
  for (const item of added) {
    if (seen.has(item.name)) continue
    seen.add(item.name)
    items.push(item)
  }
  return items
}

/** relativeName is the path a file takes under the destination: what the
 *  folder picker said, with any leading slashes gone, or else the file name. */
export function relativeName(file: Picked): string {
  const rel = (file.webkitRelativePath ?? '').replace(/^\/+/, '')
  return rel || file.name
}

export function totalBytes(items: UploadItem<Picked>[]): number {
  return items.reduce((sum, item) => sum + item.file.size, 0)
}

/** How one file's upload ended. */
export type Outcome<F extends Picked = File> =
  | { item: UploadItem<F>; state: 'done' }
  /** The host already has a file by that name and was not told to replace it. */
  | { item: UploadItem<F>; state: 'exists' }
  | { item: UploadItem<F>; state: 'failed'; error: string }
  /** The queue was stopped before this one started. */
  | { item: UploadItem<F>; state: 'skipped' }

/** Where the queue has got to, for the bar on the screen. */
export interface Progress {
  /** Files finished, however they ended. */
  finished: number
  total: number
  bytesSent: number
  bytesTotal: number
  /** The name of the file going now, if one is. */
  current?: string
}

/**
 * Sender puts one file on the wire. It resolves 'done' or 'exists' — the two
 * endings the queue carries on past — and throws for anything else. It is
 * told how many bytes have gone so far, as often as the transport knows.
 */
export type Sender<F extends Picked> = (
  item: UploadItem<F>,
  onBytes: (sent: number) => void,
) => Promise<'done' | 'exists'>

/**
 * run sends the queue in order and reports how each file ended. A failure
 * does not stop the rest — the one photo that would not go is not a reason
 * to abandon the other forty — but a stop asked for through the signal does,
 * and the files not yet begun are listed as skipped so nothing is left in
 * doubt.
 */
export async function run<F extends Picked>(
  items: UploadItem<F>[],
  send: Sender<F>,
  onProgress: (p: Progress) => void = () => {},
  signal?: AbortSignal,
): Promise<Outcome<F>[]> {
  const bytesTotal = totalBytes(items)
  const outcomes: Outcome<F>[] = []
  let finishedBytes = 0

  const report = (current: UploadItem<F> | undefined, sent: number) =>
    onProgress({
      finished: outcomes.length,
      total: items.length,
      bytesSent: finishedBytes + sent,
      bytesTotal,
      current: current?.name,
    })

  for (const item of items) {
    if (signal?.aborted) {
      outcomes.push({ item, state: 'skipped' })
      continue
    }
    report(item, 0)
    try {
      const state = await send(item, (sent) => report(item, Math.min(sent, item.file.size)))
      outcomes.push({ item, state })
    } catch (e) {
      if (signal?.aborted) {
        outcomes.push({ item, state: 'skipped' })
        continue
      }
      outcomes.push({ item, state: 'failed', error: e instanceof Error ? e.message : String(e) })
    }
    finishedBytes += item.file.size
  }
  report(undefined, 0)
  return outcomes
}

/**
 * summarize is the one line said when the queue is done: what went, and what
 * did not and why, in the order someone would want to hear it.
 */
export function summarize(outcomes: Outcome<Picked>[]): string {
  const count = (state: Outcome['state']) => outcomes.filter((o) => o.state === state).length
  const done = count('done')
  const exists = count('exists')
  const failed = count('failed')
  const skipped = count('skipped')

  const parts: string[] = []
  if (done > 0 || (exists === 0 && failed === 0 && skipped === 0)) {
    parts.push(`Uploaded ${files(done)}.`)
  }
  if (exists > 0) {
    parts.push(
      exists === 1
        ? `1 file was already there and was left alone.`
        : `${exists} files were already there and were left alone.`,
    )
  }
  if (failed > 0) {
    const first = outcomes.find((o) => o.state === 'failed')
    const reason = first && first.state === 'failed' ? first.error : ''
    parts.push(
      failed === 1
        ? `1 file could not be uploaded${reason ? `: ${reason}` : '.'}`
        : `${failed} files could not be uploaded${reason ? `. The first said: ${reason}` : '.'}`,
    )
  }
  if (skipped > 0) {
    parts.push(`Stopped with ${files(skipped)} still to go.`)
  }
  return parts.join(' ')
}

function files(n: number): string {
  return `${n} file${n === 1 ? '' : 's'}`
}
