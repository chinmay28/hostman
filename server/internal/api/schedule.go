package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/chinmay28/hostman/server/internal/hostops"
	"github.com/chinmay28/hostman/server/internal/powersched"
	"github.com/chinmay28/hostman/server/internal/store"
)

// A host's sleep and wake schedule: one a host at most, set and changed with
// PUT, unset with DELETE. The times are on HostMan's own clock, which is why
// every answer carries it — the person setting 23:00 may be looking at a
// phone in another zone, and a phone running HostMan may not share its user's.

// scheduleEvent is one thing a schedule does or did.
type scheduleEvent struct {
	Action string    `json:"action"`
	At     time.Time `json:"at"`
	// Error is why a past event did not happen; empty where it did, and
	// always for one still to come.
	Error string `json:"error,omitempty"`
}

// scheduleBody is a schedule as the API spells it, in both directions.
type scheduleBody struct {
	// SleepAt and WakeAt are HH:MM on a 24-hour clock.
	SleepAt string `json:"sleepAt"`
	WakeAt  string `json:"wakeAt"`
	// Days are weekday numbers, 0 for Sunday to 6 for Saturday. Each time is
	// taken on its own calendar day.
	Days []int `json:"days"`
}

type scheduleSet struct {
	scheduleBody
	// Next is the first thing the schedule will do.
	Next *scheduleEvent `json:"next"`
	// Last is the most recent thing it did or failed to do, or null where
	// it has not run since it was set.
	Last *scheduleEvent `json:"last"`
}

// scheduleView is the answer to every schedule request.
type scheduleView struct {
	// Schedule is null for a host with none.
	Schedule *scheduleSet `json:"schedule"`
	// Clock is the time of day on the machine HostMan runs on, as HH:MM,
	// and Zone the name of its time zone as that machine abbreviates it.
	Clock string `json:"clock"`
	Zone  string `json:"zone"`
}

func viewSchedule(p *store.PowerSchedule, now time.Time) scheduleView {
	zone, _ := now.Zone()
	view := scheduleView{Clock: now.Format("15:04"), Zone: zone}
	if p == nil {
		return view
	}
	sched := powersched.FromStore(p)
	set := &scheduleSet{scheduleBody: scheduleBody{
		SleepAt: sched.SleepAt.String(), WakeAt: sched.WakeAt.String(), Days: sched.Days.List(),
	}}
	if next, ok := sched.Next(now); ok {
		set.Next = &scheduleEvent{Action: string(next.Action), At: next.At}
	}
	if p.LastAction != "" && p.LastAt != nil {
		set.Last = &scheduleEvent{Action: p.LastAction, At: p.LastAt.In(now.Location()), Error: p.LastError}
	}
	view.Schedule = set
	return view
}

func (s *Server) handleGetPowerSchedule(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "power schedule")
		return
	}
	p, err := s.DB.GetPowerSchedule(r.Context(), h.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err, "power schedule")
		return
	}
	writeJSON(w, http.StatusOK, viewSchedule(p, time.Now()))
}

// cannotSchedule is why a host cannot be given a schedule, or nil where it
// can. A schedule is only offered where both halves can work: a host that
// would go to sleep and never be woken is a trap, not a feature. What cannot
// be checked from here — passwordless sudo on the night, wake-on-LAN in the
// firmware — shows up as the schedule's last result instead.
func cannotSchedule(h *store.Host) string {
	switch {
	case h.IsSelf:
		return "HostMan runs on " + h.Name + ": asleep, it would not be there to wake it"
	case !h.CanSleep:
		return h.Name + " has no sleep state to enter, so there is nothing to schedule"
	case cannotWake(h) != nil:
		return "HostMan has not seen a wired network card on " + h.Name + ", so it could put it to sleep and never wake it"
	}
	return ""
}

// handleSetPowerSchedule gives a host a schedule or replaces the one it has.
func (s *Server) handleSetPowerSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sched, err := parseSchedule(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "power schedule")
		return
	}
	if why := cannotSchedule(h); why != "" {
		writeError(w, http.StatusBadRequest, why)
		return
	}
	p := sched.ToStore(h.ID)
	if err := s.DB.SetPowerSchedule(r.Context(), p); err != nil {
		s.writeStoreError(w, err, "set power schedule")
		return
	}
	s.Log.Info("api: set power schedule", "host", h.Name,
		"sleep", sched.SleepAt.String(), "wake", sched.WakeAt.String(), "days", sched.Days.List())
	writeJSON(w, http.StatusOK, viewSchedule(p, time.Now()))
}

func parseSchedule(req scheduleBody) (powersched.Schedule, error) {
	var sched powersched.Schedule
	var err error
	if sched.SleepAt, err = powersched.ParseClock(req.SleepAt); err != nil {
		return sched, err
	}
	if sched.WakeAt, err = powersched.ParseClock(req.WakeAt); err != nil {
		return sched, err
	}
	if sched.Days, err = powersched.DaysOf(req.Days); err != nil {
		return sched, err
	}
	return sched, sched.Validate()
}

// handleDeletePowerSchedule unsets a host's schedule. Unsetting one that is
// not there is the same outcome and the same answer.
func (s *Server) handleDeletePowerSchedule(w http.ResponseWriter, r *http.Request) {
	h, err := s.hostFromPath(r)
	if err != nil {
		s.writeStoreError(w, err, "power schedule")
		return
	}
	if err := s.DB.DeletePowerSchedule(r.Context(), h.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeStoreError(w, err, "remove power schedule")
		return
	}
	s.Log.Info("api: removed power schedule", "host", h.Name)
	w.WriteHeader(http.StatusNoContent)
}

// ScheduledPower is how a schedule sleeps and wakes a host: by exactly what
// the buttons on the host's page do, notice and queued wake packets included.
func (s *Server) ScheduledPower() powersched.Actor { return scheduledPower{s} }

type scheduledPower struct{ s *Server }

func (p scheduledPower) Sleep(ctx context.Context, h *store.Host) error {
	if _, err := p.s.Ops.Power(ctx, h, hostops.PowerSleep); err != nil {
		return err
	}
	// The host has agreed to go, whatever has become of ctx since.
	p.s.hostSentDown(context.WithoutCancel(ctx), h, hostops.PowerSleep)
	return nil
}

func (p scheduledPower) Wake(ctx context.Context, h *store.Host) error {
	_, err := p.s.wakeHost(ctx, h)
	return err
}
