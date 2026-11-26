import { describe, expect, it } from 'vitest'
import { NOTHING, chosen, downloaded, everything, prune, toggle } from './torrentSelect'

const list = [
  { id: 'a', done: 10 },
  { id: 'b', done: 20 },
  { id: 'c', done: 0 },
]

describe('toggle', () => {
  it('ticks what was not ticked and unticks what was', () => {
    const one = toggle(NOTHING, 'a')
    expect([...one]).toEqual(['a'])
    expect([...toggle(one, 'b')]).toEqual(['a', 'b'])
    expect([...toggle(one, 'a')]).toEqual([])
  })

  it('leaves the selection it was given alone', () => {
    const one = toggle(NOTHING, 'a')
    toggle(one, 'b')
    expect(one.size).toBe(1)
    expect(NOTHING.size).toBe(0)
  })
})

describe('everything', () => {
  it('selects every listed torrent', () => {
    expect([...everything(list)]).toEqual(['a', 'b', 'c'])
    expect(everything([]).size).toBe(0)
  })
})

describe('prune', () => {
  it('drops a torrent that has left the list', () => {
    const selection = new Set(['a', 'gone', 'c'])
    expect([...prune(selection, list)]).toEqual(['a', 'c'])
  })

  it('answers with the same selection when nothing left', () => {
    const selection = new Set(['a', 'b'])
    expect(prune(selection, list)).toBe(selection)
  })

  it('empties a selection made from a list that is now empty', () => {
    expect(prune(new Set(['a']), []).size).toBe(0)
  })
})

describe('chosen', () => {
  it('is the selected torrents, in the order of the list', () => {
    expect(chosen(new Set(['c', 'a']), list).map((t) => t.id)).toEqual(['a', 'c'])
  })

  it('leaves out an id the list no longer has', () => {
    expect(chosen(new Set(['gone', 'b']), list).map((t) => t.id)).toEqual(['b'])
  })
})

describe('downloaded', () => {
  it('adds up what the torrents have downloaded', () => {
    expect(downloaded(list)).toBe(30)
    expect(downloaded([])).toBe(0)
  })
})
