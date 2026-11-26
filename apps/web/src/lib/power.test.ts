import { describe, expect, it } from 'vitest'
import { powerChoices, powerDone, wakeChoice, type PowerHost } from './power'

const pc: PowerHost = {
  name: 'tower',
  status: 'online',
  isSelf: false,
  canSleep: true,
  wakeMac: 'a8:a1:59:0c:1f:2e',
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

  it('says what will bring the machine back, before sending it away', () => {
    const wakeable = byAction(pc)
    expect(wakeable.shutdown.confirmBody).toMatch(/can try to wake it over the network/)
    expect(wakeable.sleep.confirmBody).toMatch(/can try to wake it over the network/)

    const onWifi = byAction({ ...pc, wakeMac: '' })
    expect(onWifi.shutdown.confirmBody).toMatch(/cannot bring it back/)
    expect(onWifi.sleep.confirmBody).toMatch(/cannot bring it back/)

    for (const c of [...powerChoices(pc), ...powerChoices({ ...pc, wakeMac: '' })]) {
      expect(c.confirmBody).not.toContain('{back}')
    }
  })

  it('does not promise to wake the machine HostMan itself runs on', () => {
    expect(byAction({ ...pc, isSelf: true }).sleep.confirmBody).toMatch(/cannot bring it back/)
  })
})

describe('powerDone', () => {
  const c = byAction(pc)

  it('says whether the card was left listening after a sleep or a shutdown', () => {
    expect(powerDone(c.sleep, pc, true)).toMatch(/Wake can bring it back/)
    expect(powerDone(c.shutdown, pc, false)).toMatch(/Wake may not work/)
  })

  it('says nothing about waking after a reboot, or where there is nothing to wake', () => {
    expect(powerDone(c.reboot, pc, false)).toBe(c.reboot.done)
    expect(powerDone(c.sleep, { ...pc, wakeMac: '' }, false)).toBe(c.sleep.done)
  })
})

describe('wakeChoice', () => {
  it('is offered only while the host is not answering', () => {
    expect(wakeChoice(pc).offered).toBe(false)
    for (const status of ['offline', 'error', 'unknown'] as const) {
      expect(wakeChoice({ ...pc, status }).offered).toBe(true)
    }
  })

  it('can be pressed where the probe recorded a wired card', () => {
    expect(wakeChoice({ ...pc, status: 'offline' }).unavailable).toBeNull()
  })

  it('is disabled, with the reason, where there is no address to send to', () => {
    expect(wakeChoice({ ...pc, status: 'offline', wakeMac: '' }).unavailable).toMatch(
      /not seen a wired network card/,
    )
  })

  it('is disabled for the machine HostMan runs on', () => {
    expect(wakeChoice({ ...pc, status: 'offline', isSelf: true }).unavailable).toMatch(
      /HostMan runs on tower/,
    )
  })
})
