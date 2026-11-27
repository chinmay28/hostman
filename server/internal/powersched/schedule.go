// Package powersched puts hosts to sleep and wakes them at set times of day.
//
// A schedule belongs to one host and says three things: when it sleeps, when
// it wakes, and on which days of the week. Both times are wall-clock times on
// the machine HostMan runs on — not the host's, which cannot be asked while it
// is asleep. Each is taken on its own calendar day: "23:00 and 07:00, Monday
// to Friday" sleeps the host on those five nights and wakes it on those five
// mornings, so it sleeps through from Friday night to Monday morning.
//
// This file is the arithmetic, which touches nothing; runner.go is the loop
// that acts on it.
package powersched

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Action is one of the two things a schedule does.
type Action string

const (
	Sleep Action = "sleep"
	Wake  Action = "wake"
)

// Clock is a time of day, to the minute.
type Clock struct{ Hour, Minute int }

// ParseClock reads "HH:MM" on a 24-hour clock, the form an HTML time input
// produces. Anything else — seconds, a missing zero, 24:00 — is refused.
func ParseClock(s string) (Clock, error) {
	bad := fmt.Errorf("%q is not a time of day: write it as HH:MM, like 23:00", s)
	if len(s) != 5 || s[2] != ':' {
		return Clock{}, bad
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return Clock{}, bad
		}
	}
	c := Clock{Hour: int(s[0]-'0')*10 + int(s[1]-'0'), Minute: int(s[3]-'0')*10 + int(s[4]-'0')}
	if c.Hour > 23 || c.Minute > 59 {
		return Clock{}, bad
	}
	return c, nil
}

// ClockAt is the time of day a number of minutes after midnight, which is how
// a schedule is stored.
func ClockAt(minutes int) Clock { return Clock{Hour: minutes / 60 % 24, Minute: minutes % 60} }

// Minutes is the time of day as minutes after midnight.
func (c Clock) Minutes() int { return c.Hour*60 + c.Minute }

func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// on is this time of day on the calendar day of t, in t's zone. A time that a
// clock change skips happens at the instant the clocks jump; one that a clock
// change repeats happens the first time round.
func (c Clock) on(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, c.Hour, c.Minute, 0, 0, t.Location())
}

// Days is a set of weekdays, bit n standing for time.Weekday(n): Sunday is 1.
type Days uint8

// EveryDay is the whole week.
const EveryDay Days = 1<<7 - 1

// DaysOf builds a set from weekday numbers, 0 for Sunday to 6 for Saturday.
func DaysOf(days []int) (Days, error) {
	var set Days
	for _, d := range days {
		if d < 0 || d > 6 {
			return 0, fmt.Errorf("%d is not a day of the week: they run from 0, Sunday, to 6, Saturday", d)
		}
		set |= 1 << d
	}
	return set, nil
}

// Has reports whether the set includes a weekday.
func (d Days) Has(day time.Weekday) bool { return d&(1<<day) != 0 }

// List is the set as ascending weekday numbers, never nil.
func (d Days) List() []int {
	out := []int{}
	for day := 0; day < 7; day++ {
		if d&(1<<day) != 0 {
			out = append(out, day)
		}
	}
	return out
}

// Schedule is when a host sleeps and wakes.
type Schedule struct {
	SleepAt Clock
	WakeAt  Clock
	Days    Days
}

// Validate reports what is wrong with a schedule, in words for whoever set it.
func (s Schedule) Validate() error {
	for _, c := range []Clock{s.SleepAt, s.WakeAt} {
		if c.Hour < 0 || c.Hour > 23 || c.Minute < 0 || c.Minute > 59 {
			return fmt.Errorf("%d:%d is not a time of day", c.Hour, c.Minute)
		}
	}
	if s.SleepAt == s.WakeAt {
		return errors.New("the host cannot sleep and wake at the same minute")
	}
	if s.Days&EveryDay == 0 {
		return errors.New("pick at least one day of the week")
	}
	if s.Days&^EveryDay != 0 {
		return errors.New("there are only seven days in a week")
	}
	return nil
}

// Event is one thing a schedule does at one moment.
type Event struct {
	At     time.Time
	Action Action
}

// events lists what the schedule does on the calendar day of t, in order.
func (s Schedule) events(t time.Time) []Event {
	if !s.Days.Has(t.Weekday()) {
		return nil
	}
	out := []Event{{s.SleepAt.on(t), Sleep}, {s.WakeAt.on(t), Wake}}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Next is the first event strictly after a moment, on that moment's own
// clock. It is false only for a schedule with no days.
func (s Schedule) Next(after time.Time) (Event, bool) {
	// Noon steps a day at a time without a clock change skipping a date.
	y, m, d := after.Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, after.Location())
	for i := 0; i <= 7; i++ {
		for _, e := range s.events(noon.AddDate(0, 0, i)) {
			if e.At.After(after) {
				return e, true
			}
		}
	}
	return Event{}, false
}

// Latest is the last event in the window after from and up to to, on to's own
// clock, or false where the window holds none. It is the latest and not all of
// them because only the latest still means anything: a host owed a sleep and
// then a wake should be woken, not slept first.
func (s Schedule) Latest(from, to time.Time) (Event, bool) {
	var latest Event
	found := false
	from = from.In(to.Location())
	y, m, d := from.Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, to.Location())
	for ; !noon.After(to.Add(24 * time.Hour)); noon = noon.AddDate(0, 0, 1) {
		for _, e := range s.events(noon) {
			if e.At.After(from) && !e.At.After(to) {
				latest, found = e, true
			}
		}
	}
	return latest, found
}
