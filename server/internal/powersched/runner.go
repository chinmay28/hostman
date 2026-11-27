package powersched

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// FromStore reads a schedule out of its stored form.
func FromStore(p *store.PowerSchedule) Schedule {
	return Schedule{SleepAt: ClockAt(p.SleepAt), WakeAt: ClockAt(p.WakeAt), Days: Days(p.Days) & EveryDay}
}

// ToStore is the schedule in its stored form, for one host.
func (s Schedule) ToStore(hostID int64) *store.PowerSchedule {
	return &store.PowerSchedule{HostID: hostID, SleepAt: s.SleepAt.Minutes(), WakeAt: s.WakeAt.Minutes(), Days: int(s.Days)}
}

// Actor does to a host what a schedule asks. Both are what the buttons on the
// host's page do, with everything that goes with them; an error is why the
// host could not be asked, in words fit to show.
type Actor interface {
	// Sleep puts a host that is answering to sleep.
	Sleep(ctx context.Context, h *store.Host) error
	// Wake sends for a host that is not answering. No error means the
	// packet left, not that the machine woke.
	Wake(ctx context.Context, h *store.Host) error
}

// Store is the part of the database the runner reads and writes.
type Store interface {
	ListPowerSchedules(ctx context.Context) ([]*store.PowerSchedule, error)
	GetHost(ctx context.Context, id int64) (*store.Host, error)
	RecordPowerScheduleRun(ctx context.Context, hostID int64, action string, at time.Time, failure string) error
}

const (
	// tickInterval is how often the schedules are looked at. An event runs
	// within this long of its minute.
	tickInterval = 20 * time.Second
	// MaxLate is how long after its minute an event is still worth running.
	// Past that it is dropped: a HostMan that was itself off, or asleep on a
	// laptop, should not put a machine to sleep at ten in the morning
	// because it missed eleven the night before.
	MaxLate = 5 * time.Minute
	// actTimeout bounds one sleep or wake.
	actTimeout = 45 * time.Second
)

// Runner acts on every host's schedule as its times come round. It keeps no
// schedules itself: each look reads them from the store, so one that was set,
// changed or removed a moment ago is the one that counts.
type Runner struct {
	DB    Store
	Actor Actor
	Log   *slog.Logger

	// last is the moment up to which events have been run.
	last time.Time
}

// Run looks at the schedules until ctx is cancelled. Nothing that fell due
// before Run started is run.
func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	r.Tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Tick(ctx, time.Now())
		}
	}
}

// Tick runs what fell due since the last Tick and up to now, on now's own
// clock, and returns when it has all been done. The first Tick only marks the
// start. Each host gets at most one action — the latest it is owed — and
// nothing more than MaxLate old. Tick is not safe to call concurrently.
func (r *Runner) Tick(ctx context.Context, now time.Time) {
	from := r.last
	r.last = now
	// The first look, or a clock that was set back: nothing is owed.
	if from.IsZero() || !now.After(from) {
		return
	}
	if earliest := now.Add(-MaxLate); from.Before(earliest) {
		from = earliest
	}
	schedules, err := r.DB.ListPowerSchedules(ctx)
	if err != nil {
		r.Log.Error("power schedule: list", "err", err)
		return
	}
	var wg sync.WaitGroup
	for _, p := range schedules {
		event, due := FromStore(p).Latest(from, now)
		if !due {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.act(ctx, p.HostID, event)
		}()
	}
	wg.Wait()
}

// act runs one event against one host and records how it went.
func (r *Runner) act(ctx context.Context, hostID int64, e Event) {
	ctx, cancel := context.WithTimeout(ctx, actTimeout)
	defer cancel()
	h, err := r.DB.GetHost(ctx, hostID)
	if err != nil {
		r.Log.Error("power schedule: read host", "host", hostID, "err", err)
		return
	}

	var failure string
	switch {
	case e.Action == Wake && h.Status == store.StatusOnline:
		// Awake already: there is nothing to do and nothing to report.
		return
	case e.Action == Wake:
		if err := r.Actor.Wake(ctx, h); err != nil {
			failure = err.Error()
		}
	case h.IsSelf:
		failure = fmt.Sprintf("HostMan runs on %s, and would not be there to wake it", h.Name)
	case h.Status != store.StatusOnline:
		failure = fmt.Sprintf("%s was not answering, so it could not be asked to sleep", h.Name)
	default:
		if err := r.Actor.Sleep(ctx, h); err != nil {
			failure = err.Error()
		}
	}

	if failure == "" {
		r.Log.Info("power schedule: ran", "host", h.Name, "action", string(e.Action))
	} else {
		r.Log.Warn("power schedule: did not run", "host", h.Name, "action", string(e.Action), "why", failure)
	}
	// Detached: the record matters most when ctx is what ran out.
	recCtx, cancelRec := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelRec()
	if err := r.DB.RecordPowerScheduleRun(recCtx, hostID, string(e.Action), e.At, failure); err != nil {
		r.Log.Error("power schedule: record run", "host", h.Name, "err", err)
	}
}
