import { describe, expect, it } from 'vitest'
import {
  MAX_TORRENT_BYTES,
  addPicked,
  binaryString,
  looksLikeATorrent,
  mergePicks,
  torrentsIn,
  troubles,
  type PickedTorrent,
  type TorrentOutcome,
  type TorrentProgress,
} from './torrentPick'

const TORRENT = 'd8:announce20:http://t.example/a4:infod4:name5:thingee'

interface Fake extends PickedTorrent {
  body: string | Error
}

const fake = (name: string, body: string | Error = TORRENT, size?: number): Fake => ({
  name,
  body,
  size: size ?? (typeof body === 'string' ? body.length : 10),
})

const read = async (file: Fake) => {
  if (file.body instanceof Error) throw file.body
  return Uint8Array.from(file.body, (c) => c.charCodeAt(0))
}

describe('looksLikeATorrent', () => {
  it('takes a bencoded dictionary with an info dictionary', () => {
    expect(looksLikeATorrent(TORRENT)).toBe(true)
  })

  it('refuses anything else', () => {
    expect(looksLikeATorrent('')).toBe(false)
    expect(looksLikeATorrent('\xff\xd8\xff\xe0 a photo with 4:info in it')).toBe(false)
    expect(looksLikeATorrent('d8:announce3:abce')).toBe(false)
  })
})

describe('binaryString', () => {
  it('keeps every byte, across more than one chunk', () => {
    const bytes = new Uint8Array(20000).map((_, i) => i % 256)
    const text = binaryString(bytes)
    expect(text.length).toBe(bytes.length)
    expect(text.charCodeAt(255)).toBe(255)
    expect(text.charCodeAt(19999)).toBe(19999 % 256)
  })
})

describe('mergePicks', () => {
  const file = (name: string, size = 10, lastModified = 1) => ({ name, size, lastModified })

  it('adds a later pick after an earlier one', () => {
    expect(mergePicks([file('a')], [file('b'), file('c')]).map((f) => f.name)).toEqual(['a', 'b', 'c'])
  })

  it('keeps the same file picked twice once, within a pick and across them', () => {
    expect(mergePicks([file('a')], [file('a'), file('b'), file('b')]).map((f) => f.name)).toEqual(['a', 'b'])
  })

  it('keeps two different files that share a name', () => {
    expect(mergePicks([file('a', 10)], [file('a', 11), file('a', 10, 2)])).toHaveLength(3)
  })

  it('leaves the list it was given alone', () => {
    const existing = [file('a')]
    mergePicks(existing, [file('b')])
    expect(existing).toHaveLength(1)
  })
})

describe('torrentsIn', () => {
  const file = (name: string) => ({ name, size: 10 })

  it('keeps the torrent files of a folder and leaves the rest', () => {
    const files = [file('a.torrent'), file('film.mkv'), file('B.TORRENT'), file('notes.txt'), file('torrent')]
    expect(torrentsIn(files).map((f) => f.name)).toEqual(['a.torrent', 'B.TORRENT'])
  })

  it('finds nothing in a folder with none', () => {
    expect(torrentsIn([file('film.mkv')])).toEqual([])
    expect(torrentsIn([])).toEqual([])
  })
})

describe('addPicked', () => {
  it('sends every file, in the order picked, as base64 of its bytes', async () => {
    const sent: [string, string][] = []
    const outcomes = await addPicked([fake('a.torrent'), fake('b.torrent'), fake('c.torrent')], read, async (file, base64) => {
      sent.push([file.name, base64])
    })
    expect(sent).toEqual([
      ['a.torrent', btoa(TORRENT)],
      ['b.torrent', btoa(TORRENT)],
      ['c.torrent', btoa(TORRENT)],
    ])
    expect(outcomes).toEqual([
      { name: 'a.torrent', state: 'added' },
      { name: 'b.torrent', state: 'added' },
      { name: 'c.torrent', state: 'added' },
    ])
  })

  it('sends one at a time', async () => {
    let going = 0
    let most = 0
    await addPicked([fake('a'), fake('b'), fake('c')], read, async () => {
      most = Math.max(most, ++going)
      await Promise.resolve()
      going--
    })
    expect(most).toBe(1)
  })

  it('never sends a file that is not a torrent, and carries on past it', async () => {
    const sent: string[] = []
    const outcomes = await addPicked(
      [
        fake('film.mkv', TORRENT, MAX_TORRENT_BYTES + 1),
        fake('photo.jpg', '\xff\xd8 not a torrent'),
        fake('gone.torrent', new Error('NotReadableError')),
        fake('good.torrent'),
      ],
      read,
      async (file) => {
        sent.push(file.name)
      },
    )
    expect(sent).toEqual(['good.torrent'])
    expect(outcomes).toEqual([
      { name: 'film.mkv', state: 'rejected', reason: 'is too large to be a .torrent file' },
      { name: 'photo.jpg', state: 'rejected', reason: 'is not a .torrent file' },
      { name: 'gone.torrent', state: 'rejected', reason: 'could not be read' },
      { name: 'good.torrent', state: 'added' },
    ])
  })

  it('does not read a file that is too large', async () => {
    let reads = 0
    await addPicked(
      [fake('film.mkv', TORRENT, MAX_TORRENT_BYTES + 1)],
      async (file) => {
        reads++
        return read(file)
      },
      async () => {},
    )
    expect(reads).toBe(0)
  })

  it('carries on past a file the host refuses', async () => {
    const outcomes = await addPicked([fake('a.torrent'), fake('b.torrent'), fake('c.torrent')], read, async (file) => {
      if (file.name === 'b.torrent') throw new Error('deluge would not take that')
    })
    expect(outcomes).toEqual([
      { name: 'a.torrent', state: 'added' },
      { name: 'b.torrent', state: 'failed', reason: 'deluge would not take that' },
      { name: 'c.torrent', state: 'added' },
    ])
  })

  it('says which file is going', async () => {
    const seen: TorrentProgress[] = []
    await addPicked([fake('a'), fake('b', 'nope')], read, async () => {}, (p) => seen.push(p))
    expect(seen).toEqual([
      { current: 1, total: 2 },
      { current: 2, total: 2 },
    ])
  })

  it('makes nothing of an empty pick', async () => {
    expect(await addPicked([], read, async () => {})).toEqual([])
  })
})

describe('troubles', () => {
  const added = (name: string): TorrentOutcome => ({ name, state: 'added' })
  const rejected = (name: string): TorrentOutcome => ({ name, state: 'rejected', reason: 'is not a .torrent file' })
  const failed = (name: string, reason = 'Torrent is invalid'): TorrentOutcome => ({ name, state: 'failed', reason })

  it('has nothing to say when everything was added', () => {
    expect(troubles([])).toBeNull()
    expect(troubles([added('a'), added('b')])).toBeNull()
  })

  it('says of a single file only what was wrong with it', () => {
    expect(troubles([rejected('photo.jpg')])).toBe('photo.jpg is not a .torrent file.')
    expect(troubles([failed('a.torrent', 'this host has no downloader set up yet')])).toBe(
      'a.torrent: this host has no downloader set up yet.',
    )
  })

  it('says how many went before saying which did not', () => {
    expect(troubles([added('a'), rejected('photo.jpg'), added('c'), failed('d.torrent')])).toBe(
      'Added 2 of 4 torrents. photo.jpg is not a .torrent file; d.torrent: Torrent is invalid.',
    )
  })

  it('does not end a sentence the host already ended', () => {
    expect(troubles([failed('a.torrent', 'No route to host.')])).toBe('a.torrent: No route to host.')
  })

  it('names the first few and counts the rest', () => {
    const outcomes = ['a', 'b', 'c', 'd', 'e'].map((n) => rejected(n))
    expect(troubles(outcomes)).toBe(
      'Added 0 of 5 torrents. a is not a .torrent file; b is not a .torrent file; c is not a .torrent file. 2 more could not be added either.',
    )
  })
})
