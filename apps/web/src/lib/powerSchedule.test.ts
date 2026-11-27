import { describe, expect, it } from 'vitest'
import type { PowerSchedule } from '../types'
import {
  NEW_SCHEDULE,
  WEEK,
  daysNote,
  describeDays,
  describeSchedule,
  draftProblem,
  eventWhen,
  lastLine,
  nextLine,
  sameSchedule,
  scheduleUnavailable,
  toggleDay,
  type ScheduleHost,
} from './powerSchedule'

const tower: ScheduleHost = { name: 'tower', isSelf: false, canSleep: true, wakeMac: 'a8:a1:59:0c:1f:2e' }
const nightly: PowerSchedule = { sleepAt: '23:00', wakeAt: '07:00', days: [1, 2, 3, 4, 5], next: null, last: null }

describe('who can have a schedule', () => {
  it('a host that can sleep and be woken', () => {
    expect(scheduleUnavailable(tower)).toBeNull()
  })
  it('not the machine HostMan runs on', () => {
    expect(scheduleUnavailable({ ...tower, isSelf: true })).toContain('HostMan runs on tower')
  })
  it('not one with no sleep state', () => {
    expect(scheduleUnavailable({ ...tower, canSleep: false })).toContain('no sleep state')
  })
  it('not one that could never be woken', () => {
    expect(scheduleUnavailable({ ...tower, wakeMac: '' })).toContain('never wake it')
  })
})

describe('the form', () => {
  it('starts as a sound schedule', () => {
    expect(draftProblem(NEW_SCHEDULE)).toBeNull()
  })
  it('shows the whole week once, Monday first', () => {
    expect(WEEK.map((d) => d.day)).toEqual([1, 2, 3, 4, 5, 6, 0])
  })
  it('switches a day on and off, keeping them in order', () => {
    const without = toggleDay(nightly, 3)
    expect(without.days).toEqual([1, 2, 4, 5])
    expect(toggleDay(without, 0).days).toEqual([0, 1, 2, 4, 5])
    expect(toggleDay(without, 3).days).toEqual(nightly.days)
    expect(nightly.days).toEqual([1, 2, 3, 4, 5])
  })
  it('refuses what the server would', () => {
    expect(draftProblem({ ...nightly, days: [] })).toContain('at least one day')
    expect(draftProblem({ ...nightly, wakeAt: '23:00' })).toContain('same minute')
    expect(draftProblem({ ...nightly, sleepAt: '' })).toContain('both')
    expect(draftProblem({ ...nightly, wakeAt: '7:00' })).toContain('both')
    expect(draftProblem({ ...nightly, wakeAt: '24:00' })).toContain('both')
  })
  it('knows when nothing was changed', () => {
    expect(sameSchedule(nightly, { sleepAt: '23:00', wakeAt: '07:00', days: [5, 4, 3, 2, 1] })).toBe(true)
    expect(sameSchedule(nightly, { ...nightly, wakeAt: '07:01' })).toBe(false)
    expect(sameSchedule(nightly, { ...nightly, days: [1, 2, 3, 4] })).toBe(false)
  })
})

describe('the schedule in words', () => {
  it('names the sets that have names', () => {
    expect(describeDays([0, 1, 2, 3, 4, 5, 6])).toBe('every day')
    expect(describeDays([1, 2, 3, 4, 5])).toBe('on weekdays')
    expect(describeDays([6, 0])).toBe('at weekends')
  })
  it('lists the rest from Monday', () => {
    expect(describeDays([0, 3, 1])).toBe('on Mon, Wed, Sun')
  })
  it('says the whole thing in a sentence', () => {
    expect(describeSchedule(nightly)).toBe('Sleeps at 23:00 and wakes at 07:00, on weekdays.')
  })
  it('explains the days only where some are left out', () => {
    expect(daysNote(nightly)).toContain('its own day')
    expect(daysNote(NEW_SCHEDULE)).toBeNull()
  })
})

describe('what it will do and did', () => {
  // 2026-11-23 is a Monday.
  it('reads the time off HostMan’s clock, not the browser’s', () => {
    expect(eventWhen({ action: 'sleep', at: '2026-11-23T23:00:00+01:00' })).toBe('Mon 23:00')
    expect(eventWhen({ action: 'wake', at: '2026-11-29T07:00:00-08:00' })).toBe('Sun 07:00')
    expect(eventWhen({ action: 'wake', at: '2026-11-24T00:30:00Z' })).toBe('Tue 00:30')
  })
  it('says what comes next', () => {
    expect(nextLine({ ...nightly, next: { action: 'sleep', at: '2026-11-23T23:00:00+01:00' } })).toBe(
      'Next: sleep, Mon 23:00.',
    )
    expect(nextLine(nightly)).toBeNull()
  })
  it('says nothing of a schedule that has not run', () => {
    expect(lastLine(nightly)).toBeNull()
  })
  it('says what it last did', () => {
    const slept = lastLine({ ...nightly, last: { action: 'sleep', at: '2026-11-23T23:00:00+01:00' } })
    expect(slept).toEqual({ text: 'Last: put to sleep, Mon 23:00.', failed: false })
    const woke = lastLine({ ...nightly, last: { action: 'wake', at: '2026-11-24T07:00:00+01:00' } })
    expect(woke?.text).toBe('Last: wake packet sent, Tue 07:00.')
  })
  it('says why where it could not', () => {
    const last = lastLine({
      ...nightly,
      last: { action: 'sleep', at: '2026-11-23T23:00:00+01:00', error: 'pi needs passwordless sudo on tower' },
    })
    expect(last?.failed).toBe(true)
    expect(last?.text).toBe('The sleep due Mon 23:00 did not happen: pi needs passwordless sudo on tower')
  })
})
