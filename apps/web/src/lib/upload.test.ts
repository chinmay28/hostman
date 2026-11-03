import { describe, expect, it } from 'vitest'
import {
  merge,
  plan,
  relativeName,
  run,
  summarize,
  totalBytes,
  type Outcome,
  type Picked,
  type UploadItem,
} from './upload'

const pick = (name: string, size = 10, webkitRelativePath?: string): Picked => ({ name, size, webkitRelativePath })

describe('plan', () => {
  it('sends a file picked on its own by its name', () => {
    expect(plan([pick('photo.jpg')]).map((i) => i.name)).toEqual(['photo.jpg'])
  })

  it('keeps the folder structure of a picked folder', () => {
    const items = plan([
      pick('a.jpg', 1, 'trip/day1/a.jpg'),
      pick('b.jpg', 2, 'trip/day2/b.jpg'),
      pick('notes.txt', 3, 'trip/notes.txt'),
    ])
    expect(items.map((i) => i.name)).toEqual(['trip/day1/a.jpg', 'trip/day2/b.jpg', 'trip/notes.txt'])
  })

  it('sends a name picked twice once', () => {
    expect(plan([pick('a.jpg'), pick('a.jpg'), pick('b.jpg')]).map((i) => i.name)).toEqual(['a.jpg', 'b.jpg'])
  })

  it('drops a leading slash a browser may put on a relative path', () => {
    expect(relativeName(pick('a.jpg', 1, '/trip/a.jpg'))).toBe('trip/a.jpg')
    expect(relativeName(pick('a.jpg', 1, ''))).toBe('a.jpg')
  })

  it('grows by a later pick, keeping a name picked twice once', () => {
    const first = plan([pick('a.jpg', 1)])
    const second = plan([pick('b.jpg', 2), pick('a.jpg', 3)])
    expect(merge(first, second).map((i) => [i.name, i.file.size])).toEqual([
      ['a.jpg', 1],
      ['b.jpg', 2],
    ])
    expect(merge([], second).map((i) => i.name)).toEqual(['b.jpg', 'a.jpg'])
    // Merging leaves the earlier list as it was.
    expect(first).toHaveLength(1)
  })

  it('adds the sizes up', () => {
    expect(totalBytes(plan([pick('a', 5), pick('b', 7)]))).toBe(12)
  })
})

describe('run', () => {
  const items = plan([pick('a', 10), pick('b', 20), pick('c', 30)])

  it('sends in order and reports how each ended', async () => {
    const sent: string[] = []
    const outcomes = await run(items, async (item) => {
      sent.push(item.name)
      return item.name === 'b' ? 'exists' : 'done'
    })
    expect(sent).toEqual(['a', 'b', 'c'])
    expect(outcomes.map((o) => o.state)).toEqual(['done', 'exists', 'done'])
  })

  it('carries on past a failure and says why it failed', async () => {
    const outcomes = await run(items, async (item) => {
      if (item.name === 'b') throw new Error('the disk is full')
      return 'done'
    })
    expect(outcomes.map((o) => o.state)).toEqual(['done', 'failed', 'done'])
    expect(outcomes[1]).toMatchObject({ state: 'failed', error: 'the disk is full' })
  })

  it('counts bytes across files as they go', async () => {
    const seen: number[] = []
    await run(
      items,
      async (item, onBytes) => {
        onBytes(item.file.size / 2)
        onBytes(item.file.size)
        return 'done'
      },
      (p) => seen.push(p.bytesSent),
    )
    // Each file reports at its start, halfway and at its end; the last report
    // is the queue finishing.
    expect(seen).toEqual([0, 5, 10, 10, 20, 30, 30, 45, 60, 60])
  })

  it('names the file going now and clears it at the end', async () => {
    const current: (string | undefined)[] = []
    await run(items, async () => 'done', (p) => current.push(p.current))
    expect(current[0]).toBe('a')
    expect(current[current.length - 1]).toBeUndefined()
  })

  it('does not report more than a file holds, however the transport counts', async () => {
    let most = 0
    await run(
      items.slice(0, 1),
      async (item, onBytes) => {
        onBytes(item.file.size + 500) // a transport counting headers too
        return 'done'
      },
      (p) => (most = Math.max(most, p.bytesSent)),
    )
    expect(most).toBe(10)
  })

  it('stops when asked and lists what was not begun as skipped', async () => {
    const controller = new AbortController()
    const outcomes = await run(
      items,
      async (item) => {
        if (item.name === 'a') controller.abort()
        if (controller.signal.aborted) throw new Error('aborted')
        return 'done'
      },
      () => {},
      controller.signal,
    )
    // The first file was cut off by the stop, so it is skipped rather than
    // failed; the others never started.
    expect(outcomes.map((o) => o.state)).toEqual(['skipped', 'skipped', 'skipped'])
  })
})

describe('summarize', () => {
  const item = (name: string): UploadItem<Picked> => ({ file: pick(name), name })
  const o = (name: string, state: 'done' | 'exists' | 'skipped'): Outcome<Picked> => ({ item: item(name), state })
  const failed = (name: string, error: string): Outcome<Picked> => ({ item: item(name), state: 'failed', error })

  it('counts what went', () => {
    expect(summarize([o('a', 'done')])).toBe('Uploaded 1 file.')
    expect(summarize([o('a', 'done'), o('b', 'done')])).toBe('Uploaded 2 files.')
    expect(summarize([])).toBe('Uploaded 0 files.')
  })

  it('says what was left alone and why', () => {
    expect(summarize([o('a', 'done'), o('b', 'exists')])).toBe(
      'Uploaded 1 file. 1 file was already there and was left alone.',
    )
    expect(summarize([o('a', 'exists'), o('b', 'exists')])).toBe(
      '2 files were already there and were left alone.',
    )
  })

  it('gives the reason for a failure, and the first reason for several', () => {
    expect(summarize([o('a', 'done'), failed('b', 'the disk is full')])).toBe(
      'Uploaded 1 file. 1 file could not be uploaded: the disk is full',
    )
    expect(summarize([failed('a', 'no such directory'), failed('b', 'the disk is full')])).toBe(
      '2 files could not be uploaded. The first said: no such directory',
    )
  })

  it('says how many were still to go after a stop', () => {
    expect(summarize([o('a', 'done'), o('b', 'skipped'), o('c', 'skipped')])).toBe(
      'Uploaded 1 file. Stopped with 2 files still to go.',
    )
  })
})
