import { describe, expect, it } from 'vitest'
import { describeJob } from './HostFiles'
import type { FileJob } from '../types'

const base: FileJob = {
  id: 'j1',
  kind: 'copy',
  hostId: 1,
  host: 'pi',
  path: '/media/chinmay/Elements/Umbrel',
  to: '/home/chinmay/backups',
  state: 'running',
  log: '',
  startedAt: '2026-11-01T12:00:00Z',
}

describe('describeJob', () => {
  it('says what is happening to what, and where it is going', () => {
    expect(describeJob(base, 1)).toBe('Copying Umbrel to /home/chinmay/backups…')
    expect(describeJob({ ...base, kind: 'move', state: 'done' }, 1)).toBe('Moved Umbrel to /home/chinmay/backups.')
    expect(describeJob({ ...base, state: 'cancelled' }, 1)).toBe('Cancelled copying Umbrel to /home/chinmay/backups.')
    expect(describeJob({ ...base, state: 'failed', error: 'already exists' }, 1)).toBe(
      'Could not copy Umbrel to /home/chinmay/backups.',
    )
  })

  it('names the other host on a send, and reads as arriving from the far end', () => {
    const send: FileJob = { ...base, kind: 'send', destHostId: 2, destHost: 'nas', to: '/srv/backups' }
    expect(describeJob(send, 1)).toBe('Sending Umbrel to nas:/srv/backups…')
    expect(describeJob({ ...send, state: 'done' }, 1)).toBe('Sent Umbrel to nas:/srv/backups.')
    expect(describeJob(send, 2)).toBe('Receiving Umbrel to /srv/backups from pi…')
    expect(describeJob({ ...send, state: 'failed' }, 2)).toBe('Could not receive Umbrel to /srv/backups from pi.')
  })
})
