import { describe, expect, it } from 'vitest'
import { powerChoices, type PowerHost } from './power'

const pc: PowerHost = {
  name: 'tower',
  status: 'online',
  isSelf: false,
  canSleep: true,
}

const byAction = (host: PowerHost) => Object.fromEntries(powerChoices(host).map((c) => [c.action, c]))

describe('powerChoices', () => {
  it('offers reboot, shutdown and sleep, in that order', () => {
    expect(powerChoices(pc).map((c) => c.action)).toEqual(['reboot', 'shutdown', 'sleep'])
  })

  it('lets a machine that can sleep do all three', () => {
    expect(powerChoices(pc).map((c) => c.unavailable)).toEqual([null, null, null])
  })

  it('disables only sleep where the kernel offers no suspend state', () => {
    const c = byAction({ ...pc, name: 'nakedpi', canSleep: false })
    expect(c.reboot.unavailable).toBeNull()
    expect(c.shutdown.unavailable).toBeNull()
    expect(c.sleep.unavailable).toMatch(/nakedpi has no sleep state/)
  })

  it('disables everything on a host that is not online, and says that first', () => {
    for (const status of ['offline', 'error', 'unknown'] as const) {
      for (const c of powerChoices({ ...pc, status, canSleep: false })) {
        expect(c.unavailable).toBe('tower is not online.')
      }
    }
  })

  it('names the host in every question and every answer', () => {
    for (const c of powerChoices(pc)) {
      expect(c.confirmTitle).toContain('tower')
      expect(c.confirmTitle).not.toContain('{name}')
      expect(c.done).toContain('tower')
      expect(c.done).not.toContain('{name}')
    }
  })

  it('says what will not bring the machine back', () => {
    const c = byAction(pc)
    expect(c.shutdown.confirmBody).toMatch(/cannot turn it back on/)
    expect(c.sleep.confirmBody).toMatch(/cannot wake it/)
  })
})
