import { describe as group, expect, it } from 'vitest'
import { actionsFor, describe } from './HostVMs'
import type { VM } from '../types'

group('the verbs a machine takes', () => {
  it('starts what is off or saved, and nothing else', () => {
    expect(actionsFor('poweroff')).toEqual(['start'])
    expect(actionsFor('aborted')).toEqual(['start'])
    expect(actionsFor('saved')).toEqual(['start'])
  })

  it('gives a running machine the full set, with the gentle stop before the hard one', () => {
    const verbs = actionsFor('running')
    expect(verbs).toContain('pause')
    expect(verbs).toContain('save')
    expect(verbs.indexOf('stop')).toBeLessThan(verbs.indexOf('poweroff'))
    expect(verbs).not.toContain('start')
  })

  it('lets a paused machine resume or be powered off, not shut down', () => {
    expect(actionsFor('paused')).toEqual(['resume', 'poweroff'])
  })

  it('offers nothing to a machine between states or one that cannot be opened', () => {
    for (const state of ['starting', 'stopping', 'saving', 'restoring', 'inaccessible']) {
      expect(actionsFor(state)).toEqual([])
    }
  })
})

group('a machine described in one line', () => {
  const now = Date.parse('2026-10-25T12:00:00Z')
  const vm: VM = {
    id: '6c0a3a1e-5a7f-4c1c-9d3a-1f0b2c3d4e5f',
    name: 'ubuntu server',
    state: 'running',
    osType: 'Ubuntu_64',
    cpus: 2,
    memoryMb: 2048,
    since: '2026-10-25T09:30:00Z',
  }

  it('says the guest, what it was given, and how long it has been up', () => {
    expect(describe(vm, now)).toBe('Ubuntu (64-bit) · 2 CPUs · 2 GB · up 2h 30m')
  })

  it('names the state when it is not running, and MB where the memory is not whole GB', () => {
    expect(describe({ ...vm, state: 'paused', cpus: 1, memoryMb: 512 }, now)).toBe(
      'Ubuntu (64-bit) · 1 CPU · 512 MB · paused 2h 30m',
    )
  })

  it('falls back to the id when VirtualBox could say nothing about it', () => {
    expect(describe({ id: vm.id, name: vm.id, state: 'poweroff' }, now)).toBe(vm.id)
    expect(describe({ id: vm.id, name: vm.id, state: 'inaccessible' }, now)).toBe(
      'VirtualBox cannot open its files',
    )
  })

  it('leaves out a state change under a minute old, which reads as noise', () => {
    expect(describe({ ...vm, since: '2026-10-25T11:59:30Z' }, now)).toBe('Ubuntu (64-bit) · 2 CPUs · 2 GB')
  })
})
