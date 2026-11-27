/**
 * Selecting torrents to remove together. The selection is a set of torrent
 * ids, kept apart from the list it was made from because the list is asked for
 * again every few seconds and changes under it: a torrent that deluge dropped
 * while it was ticked is not one anybody can still remove. This module is that
 * set and the arithmetic on it, kept apart from the screen so it can be tested
 * without one.
 */

/** As much of a torrent as selecting one needs. */
export interface Selectable {
  id: string
  /** Bytes already downloaded: what "delete the files too" would delete. */
  done: number
}

export type Selection = ReadonlySet<string>

export const NOTHING: Selection = new Set()

/** toggle ticks a torrent that was not ticked, and unticks one that was. */
export function toggle(selection: Selection, id: string): Selection {
  const next = new Set(selection)
  if (!next.delete(id)) next.add(id)
  return next
}

/** everything is the selection with every listed torrent in it. */
export function everything(torrents: Selectable[]): Selection {
  return new Set(torrents.map((t) => t.id))
}

/**
 * prune drops what is no longer in the list. It answers with the selection it
 * was given when nothing went, so a screen that asks on a timer is not told
 * the selection changed every time the list is read.
 */
export function prune(selection: Selection, torrents: Selectable[]): Selection {
  const listed = new Set(torrents.map((t) => t.id))
  const kept = [...selection].filter((id) => listed.has(id))
  return kept.length === selection.size ? selection : new Set(kept)
}

/** chosen is the selected torrents themselves, in the order the list has them. */
export function chosen<T extends Selectable>(selection: Selection, torrents: T[]): T[] {
  return torrents.filter((t) => selection.has(t.id))
}

/** downloaded is how many bytes deleting these torrents' files would delete. */
export function downloaded(torrents: Selectable[]): number {
  return torrents.reduce((sum, t) => sum + t.done, 0)
}
