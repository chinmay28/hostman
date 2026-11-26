import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { PowerChoice, PowerHost } from '../lib/power'
import { PowerCard } from './power'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const pc: PowerHost = {
  name: 'tower',
  status: 'online',
  isSelf: false,
  canSleep: true,
}

describe('the power card', () => {
  let el: HTMLDivElement
  let root: Root
  let confirmed: string[]

  beforeEach(() => {
    el = document.createElement('div')
    document.body.appendChild(el)
    root = createRoot(el)
    confirmed = []
  })

  afterEach(() => {
    act(() => root.unmount())
    el.remove()
  })

  const render = (host: PowerHost, sudoOk = true) =>
    act(() => {
      root.render(
        <PowerCard
          host={host}
          username="pi"
          sudoOk={sudoOk}
          onConfirm={async (c: PowerChoice) => {
            confirmed.push(c.action)
          }}
        />,
      )
    })

  const button = (action: string) => el.querySelector<HTMLButtonElement>(`button[data-power="${action}"]`)!
  const confirmButton = () => el.querySelector<HTMLButtonElement>('button[data-power-confirm]')
  const click = (b: Element) => act(async () => (b as HTMLButtonElement).click())

  it('shows reboot, shut down and sleep', () => {
    render(pc)
    expect(['reboot', 'shutdown', 'sleep'].map((a) => button(a).textContent)).toEqual([
      'Reboot',
      'Shut down',
      'Sleep',
    ])
  })

  for (const action of ['reboot', 'shutdown', 'sleep']) {
    it(`asks before a ${action}, and does nothing until answered`, async () => {
      render(pc)
      expect(confirmButton()).toBeNull()

      await click(button(action))
      expect(confirmed).toEqual([])
      expect(confirmButton()?.dataset.powerConfirm).toBe(action)
      expect(el.textContent).toMatch(/tower[^.]*\?/)

      await click(confirmButton()!)
      expect(confirmed).toEqual([action])
      expect(confirmButton()).toBeNull()
    })
  }

  it('does nothing when the question is cancelled', async () => {
    render(pc)
    await click(button('shutdown'))
    const cancel = [...el.querySelectorAll('button')].find((b) => b.textContent === 'Cancel')!
    await click(cancel)
    expect(confirmed).toEqual([])
    expect(confirmButton()).toBeNull()
  })

  it('disables sleep, and says why, where the machine cannot', () => {
    render({ ...pc, name: 'nakedpi', canSleep: false })
    expect(button('sleep').disabled).toBe(true)
    expect(button('reboot').disabled).toBe(false)
    expect(button('shutdown').disabled).toBe(false)
    expect(el.textContent).toContain('nakedpi has no sleep state to enter')
  })

  it('disables everything while the host is offline', () => {
    render({ ...pc, status: 'offline' })
    for (const a of ['reboot', 'shutdown', 'sleep']) expect(button(a).disabled).toBe(true)
  })

  it('warns when HostMan runs on the machine being asked', async () => {
    render({ ...pc, isSelf: true })
    await click(button('shutdown'))
    expect(el.textContent).toContain('stays down until the machine is turned on again')
  })

  it('says sudo is missing before the press, not after', () => {
    render(pc, false)
    expect(el.textContent).toContain('pi needs passwordless sudo')
  })
})
