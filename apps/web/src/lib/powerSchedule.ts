import type { Host, PowerSchedule, PowerScheduleEvent } from '../types'

/** What the schedule card needs to know about a host. */
export type ScheduleHost = Pick<Host, 'name' | 'isSelf' | 'canSleep' | 'wakeMac'>

/** A schedule as the form holds it and the server takes it. */
export type ScheduleDraft = Pick<PowerSchedule, 'sleepAt' | 'wakeAt' | 'days'>

/** The week as the form shows it: Monday first, by weekday number. */
export const WEEK: { day: number; label: string }[] = [
  { day: 1, label: 'Mon' },
  { day: 2, label: 'Tue' },
  { day: 3, label: 'Wed' },
  { day: 4, label: 'Thu' },
  { day: 5, label: 'Fri' },
  { day: 6, label: 'Sat' },
  { day: 0, label: 'Sun' },
]

const label = (day: number) => WEEK.find((d) => d.day === day)?.label ?? '?'

/** Where the form starts for a host with no schedule: the night, every night. */
export const NEW_SCHEDULE: ScheduleDraft = { sleepAt: '23:00', wakeAt: '07:00', days: [0, 1, 2, 3, 4, 5, 6] }

/** Why a host cannot be given a schedule, or null where it can. The server
 *  refuses the same hosts for the same reasons; this says so before the form
 *  is filled in. A schedule needs both halves to work — a host put to sleep
 *  that HostMan cannot wake is one somebody has to walk to. */
export function scheduleUnavailable(host: ScheduleHost): string | null {
  if (host.isSelf) return `HostMan runs on ${host.name}: asleep, it would not be there to wake it.`
  if (!host.canSleep) return `${host.name} has no sleep state to enter, so there is nothing to schedule.`
  if (host.wakeMac === '')
    return `HostMan has not seen a wired network card on ${host.name}, so it could put it to sleep and never wake it.`
  return null
}

/** The draft with one day switched on or off. */
export function toggleDay(draft: ScheduleDraft, day: number): ScheduleDraft {
  const days = draft.days.includes(day) ? draft.days.filter((d) => d !== day) : [...draft.days, day]
  return { ...draft, days: days.sort((a, b) => a - b) }
}

const CLOCK = /^([01]\d|2[0-3]):[0-5]\d$/

/** What is wrong with a draft, or null where it can be saved. */
export function draftProblem(draft: ScheduleDraft): string | null {
  if (!CLOCK.test(draft.sleepAt) || !CLOCK.test(draft.wakeAt)) return 'Set both a sleep time and a wake time.'
  if (draft.sleepAt === draft.wakeAt) return 'The host cannot sleep and wake at the same minute.'
  if (draft.days.length === 0) return 'Pick at least one day.'
  return null
}

/** Whether a draft says what a schedule already says, so saving would change
 *  nothing. */
export function sameSchedule(a: ScheduleDraft, b: ScheduleDraft): boolean {
  const days = (s: ScheduleDraft) => [...new Set(s.days)].sort((x, y) => x - y).join()
  return a.sleepAt === b.sleepAt && a.wakeAt === b.wakeAt && days(a) === days(b)
}

/** The days in words: the three sets with a name get it, the rest are listed
 *  from Monday. */
export function describeDays(days: number[]): string {
  const set = new Set(days)
  const only = (...want: number[]) => set.size === want.length && want.every((d) => set.has(d))
  if (set.size === 7) return 'every day'
  if (only(1, 2, 3, 4, 5)) return 'on weekdays'
  if (only(0, 6)) return 'at weekends'
  return (
    'on ' +
    WEEK.filter((d) => set.has(d.day))
      .map((d) => d.label)
      .join(', ')
  )
}

/** The whole schedule in a sentence. */
export function describeSchedule(s: ScheduleDraft): string {
  return `Sleeps at ${s.sleepAt} and wakes at ${s.wakeAt}, ${describeDays(s.days)}.`
}

/** What a schedule spanning midnight means for its days, which is the one
 *  thing about it that is easy to get wrong: each time is taken on its own
 *  day, so "weekdays" with a night-time sleep leaves the host asleep from
 *  Friday night to Monday morning. Null where every day is chosen and there
 *  is nothing to explain. */
export function daysNote(s: ScheduleDraft): string | null {
  if (new Set(s.days).size === 7 || s.days.length === 0) return null
  return 'Each time is taken on its own day: on a day that is not chosen the host is neither put to sleep nor woken.'
}

/** When an event is, read off the server's own timestamp instead of through
 *  the browser's zone: a schedule set for 23:00 on HostMan's clock should say
 *  23:00 on a phone that is somewhere else. */
export function eventWhen(e: PowerScheduleEvent): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}:\d{2})/.exec(e.at)
  if (!m) return e.at
  const weekday = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]))).getUTCDay()
  return `${label(weekday)} ${m[4]}`
}

/** "Next: sleep, Mon 23:00". */
export function nextLine(s: PowerSchedule): string | null {
  return s.next ? `Next: ${s.next.action}, ${eventWhen(s.next)}.` : null
}

/** What the schedule last did, and whether that is bad news. */
export function lastLine(s: PowerSchedule): { text: string; failed: boolean } | null {
  if (!s.last) return null
  const what = s.last.action === 'sleep' ? 'sleep' : 'wake'
  if (s.last.error) return { text: `The ${what} due ${eventWhen(s.last)} did not happen: ${s.last.error}`, failed: true }
  return {
    text:
      s.last.action === 'sleep'
        ? `Last: put to sleep, ${eventWhen(s.last)}.`
        : `Last: wake packet sent, ${eventWhen(s.last)}.`,
    failed: false,
  }
}
