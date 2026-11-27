import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { ScheduleDraft, ScheduleHost } from '../lib/powerSchedule'
import type { PowerScheduleView } from '../types'
import { PowerScheduleCard } from './powerSchedule'
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const tower: ScheduleHost = { name: 'tower', isSelf: false, canSleep: true, wakeMac: 'a8:a1:59:0c:1f:2e' }
const none: PowerScheduleView = { schedule: null, clock: '21:04', zone: 'CET' }
const nightly: PowerScheduleView = {
  ...none,
  schedule: {
    sleepAt: '23:00',
    wakeAt: '07:00',
    days: [1, 2, 3, 4, 5],
    next: { action: 'sleep', at: '2026-11-23T23:00:00+01:00' },
    last: null,
  },
}

describe('the schedule card', () => {
  let el: HTMLDivElement
  let root: Root
  let saved: ScheduleDraft[]
  let removed: number
  let refuse: string | null

  beforeEach(() => {
    el = document.createElement('div')
    document.body.appendChild(el)
    root = createRoot(el)
    saved = []
    removed = 0
    refuse = null
  })

  afterEach(() => {
    act(() => root.unmount())
    el.remove()
  })

  const render = (view: PowerScheduleView, host: ScheduleHost = tower) =>
    act(() => {
      root.render(
        <PowerScheduleCard
          host={host}
          view={view}
          onSave={async (d) => {
            if (refuse) throw new Error(refuse)
            saved.push(d)
          }}
          onRemove={async () => {
            if (refuse) throw new Error(refuse)
            removed++
          }}
        />,
      )
    })

  const part = (name: string) => el.querySelector<HTMLElement>(`[data-schedule="${name}"]`)
  const day = (n: number) => el.querySelector<HTMLButtonElement>(`[data-schedule-day="${n}"]`)!
  const click = (b: Element) => act(async () => (b as HTMLButtonElement).click())
  // React listens for the native event, and tracks the value it last set.
  const type = (input: HTMLInputElement, value: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })

  it('offers to set one where there is none', async () => {
    render(none)
    expect(part('summary')?.textContent).toContain('None set')
    expect(part('edit')?.textContent).toBe('Set a schedule')
    expect(part('remove')).toBeNull()
  })

  it('sets one: nights, every day, unless told otherwise', async () => {
    render(none)
    await click(part('edit')!)
    expect(el.textContent).toContain('HostMan’s clock, which reads 21:04 CET')
    await click(part('save')!)
    expect(saved).toEqual([{ sleepAt: '23:00', wakeAt: '07:00', days: [0, 1, 2, 3, 4, 5, 6] }])
    expect(part('save')).toBeNull()
  })

  it('says what is set, and what comes next', () => {
    render(nightly)
    expect(part('summary')?.textContent).toBe('Sleeps at 23:00 and wakes at 07:00, on weekdays. Next: sleep, Mon 23:00.')
    expect(part('edit')?.textContent).toBe('Change')
  })

  it('changes one, starting from what is set', async () => {
    render(nightly)
    await click(part('edit')!)
    const save = part('save') as HTMLButtonElement
    expect((part('wakeAt') as HTMLInputElement).value).toBe('07:00')
    expect(day(1).getAttribute('aria-pressed')).toBe('true')
    expect(day(6).getAttribute('aria-pressed')).toBe('false')
    // Nothing changed yet, so there is nothing to save.
    expect(save.disabled).toBe(true)

    await type(part('wakeAt') as HTMLInputElement, '06:30')
    await click(day(6))
    await click(day(1))
    expect(save.disabled).toBe(false)
    await click(save)
    expect(saved).toEqual([{ sleepAt: '23:00', wakeAt: '06:30', days: [2, 3, 4, 5, 6] }])
  })

  it('will not save a schedule with no days, and says why', async () => {
    render({ ...nightly, schedule: { ...nightly.schedule!, days: [1] } })
    await click(part('edit')!)
    await click(day(1))
    expect((part('save') as HTMLButtonElement).disabled).toBe(true)
    expect(el.textContent).toContain('Pick at least one day')
  })

  it('saves nothing when cancelled', async () => {
    render(nightly)
    await click(part('edit')!)
    await click(day(6))
    await click([...el.querySelectorAll('button')].find((b) => b.textContent === 'Cancel')!)
    expect(saved).toEqual([])
    expect(part('save')).toBeNull()
  })

  it('keeps the form, with the reason, when the server refuses', async () => {
    refuse = 'tower has no sleep state to enter'
    render(none)
    await click(part('edit')!)
    await click(part('save')!)
    expect(saved).toEqual([])
    expect(part('save')).not.toBeNull()
    expect(el.textContent).toContain('tower has no sleep state to enter')
  })

  it('removes one', async () => {
    render(nightly)
    await click(part('remove')!)
    expect(removed).toBe(1)
  })

  it('shows what the schedule last failed to do', () => {
    render({
      ...nightly,
      schedule: {
        ...nightly.schedule!,
        last: { action: 'sleep', at: '2026-11-20T23:00:00+01:00', error: 'pi needs passwordless sudo on tower' },
      },
    })
    expect(el.querySelector('.banner.bad')?.textContent).toContain('did not happen: pi needs passwordless sudo')
  })

  it('offers no schedule to a host that could not keep one, and says why', () => {
    render(none, { ...tower, wakeMac: '' })
    expect(part('summary')?.textContent).toContain('never wake it')
    expect(part('edit')).toBeNull()
    expect(part('remove')).toBeNull()
  })

  it('still lets a schedule be removed from a host that can no longer keep it', () => {
    render(nightly, { ...tower, canSleep: false })
    expect(el.textContent).toContain('cannot run as things are')
    expect(part('edit')).toBeNull()
    expect(part('remove')).not.toBeNull()
  })
})
