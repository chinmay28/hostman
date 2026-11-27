import { useState } from 'react'
import {
  NEW_SCHEDULE,
  WEEK,
  daysNote,
  describeSchedule,
  draftProblem,
  lastLine,
  nextLine,
  sameSchedule,
  scheduleUnavailable,
  toggleDay,
  type ScheduleDraft,
  type ScheduleHost,
} from '../lib/powerSchedule'
import type { PowerScheduleView } from '../types'
import { Banner, Card, Field, Sheet } from './ui'

/** A host's sleep and wake schedule: what it is, what it will do next and did
 *  last, and the form that sets, changes and removes it.
 *
 *  `onSave` and `onRemove` do the asking and reject with the server's reason.
 *  A refused save is shown in the form, which stays open with what was typed;
 *  a refused removal is shown on the card. */
export function PowerScheduleCard({
  host,
  view,
  onSave,
  onRemove,
}: {
  host: ScheduleHost
  view: PowerScheduleView
  onSave: (draft: ScheduleDraft) => Promise<void>
  onRemove: () => Promise<void>
}) {
  const schedule = view.schedule
  const unavailable = scheduleUnavailable(host)
  const [draft, setDraft] = useState<ScheduleDraft | null>(null)
  const [working, setWorking] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [cardError, setCardError] = useState<string | null>(null)

  const open = () => {
    setFormError(null)
    setDraft(schedule ? { sleepAt: schedule.sleepAt, wakeAt: schedule.wakeAt, days: schedule.days } : NEW_SCHEDULE)
  }

  const save = async (d: ScheduleDraft) => {
    setWorking(true)
    setFormError(null)
    try {
      await onSave(d)
      setDraft(null)
    } catch (e) {
      setFormError(e instanceof Error ? e.message : String(e))
    } finally {
      setWorking(false)
    }
  }

  const remove = async () => {
    setWorking(true)
    setCardError(null)
    try {
      await onRemove()
    } catch (e) {
      setCardError(e instanceof Error ? e.message : String(e))
    } finally {
      setWorking(false)
    }
  }

  const next = schedule && nextLine(schedule)
  const last = schedule && lastLine(schedule)
  const problem = draft && draftProblem(draft)
  const unchanged = !!draft && !!schedule && sameSchedule(draft, schedule)

  return (
    <>
      <Card>
        <div className="title">Sleep and wake schedule</div>
        {schedule ? (
          <>
            <p className="sub" style={{ marginTop: 4 }} data-schedule="summary">
              {describeSchedule(schedule)} {next}
            </p>
            {last &&
              (last.failed ? (
                <Banner tone="bad">{last.text}</Banner>
              ) : (
                <p className="sub" style={{ marginTop: 4 }}>
                  {last.text}
                </p>
              ))}
          </>
        ) : (
          <p className="sub" style={{ marginTop: 4 }} data-schedule="summary">
            {unavailable ??
              'None set. HostMan can put this host to sleep and wake it at the same times each day, by the same means as the buttons above.'}
          </p>
        )}
        {/* A schedule its host can no longer keep still has to be removable. */}
        {schedule && unavailable && <Banner tone="warn">This schedule cannot run as things are. {unavailable}</Banner>}
        {cardError && <Banner tone="bad">{cardError}</Banner>}
        {(schedule || !unavailable) && (
          <div className="actions">
            {!unavailable && (
              <button className="secondary" onClick={open} disabled={working} data-schedule="edit">
                {schedule ? 'Change' : 'Set a schedule'}
              </button>
            )}
            {schedule && (
              <button className="secondary" onClick={remove} disabled={working} data-schedule="remove">
                {working && !draft ? 'Removing…' : 'Remove'}
              </button>
            )}
          </div>
        )}
      </Card>
      {draft && (
        <Sheet
          title={schedule ? `Change ${host.name}’s schedule` : `Set a schedule for ${host.name}`}
          subtitle={`Times are on HostMan’s clock, which reads ${view.clock} ${view.zone}.`}
          onClose={() => !working && setDraft(null)}
        >
          <Field label="Sleep at">
            <input
              type="time"
              value={draft.sleepAt}
              onChange={(e) => setDraft({ ...draft, sleepAt: e.target.value })}
              data-schedule="sleepAt"
            />
          </Field>
          <Field label="Wake at">
            <input
              type="time"
              value={draft.wakeAt}
              onChange={(e) => setDraft({ ...draft, wakeAt: e.target.value })}
              data-schedule="wakeAt"
            />
          </Field>
          <Field label="On" help={daysNote(draft) ?? undefined}>
            <div className="chips">
              {WEEK.map(({ day, label }) => (
                <button
                  key={day}
                  type="button"
                  className={draft.days.includes(day) ? 'chip on' : 'chip'}
                  aria-pressed={draft.days.includes(day)}
                  onClick={() => setDraft(toggleDay(draft, day))}
                  data-schedule-day={day}
                >
                  {label}
                </button>
              ))}
            </div>
          </Field>
          {problem && <p className="sub">{problem}</p>}
          {formError && <Banner tone="bad">{formError}</Banner>}
          <div className="actions">
            <button className="secondary" onClick={() => setDraft(null)} disabled={working}>
              Cancel
            </button>
            <button
              className="primary"
              onClick={() => save(draft)}
              disabled={working || problem !== null || unchanged}
              data-schedule="save"
            >
              {working ? 'Saving…' : 'Save'}
            </button>
          </div>
        </Sheet>
      )}
    </>
  )
}
