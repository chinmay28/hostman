package powersched

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// recordingActor remembers what it was asked to do, as "sleep tower".
type recordingActor struct {
	mu       sync.Mutex
	did      []string
	sleepErr error
	wakeErr  error
}

func (a *recordingActor) Sleep(_ context.Context, h *store.Host) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.did = append(a.did, "sleep "+h.Name)
	return a.sleepErr
}

func (a *recordingActor) Wake(_ context.Context, h *store.Host) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.did = append(a.did, "wake "+h.Name)
	return a.wakeErr
}

func (a *recordingActor) take() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.did
	a.did = nil
	sort.Strings(out)
	return out
}

type fixture struct {
	t     *testing.T
	db    *store.DB
	actor *recordingActor
	r     *Runner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hostman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	actor := &recordingActor{}
	return &fixture{t: t, db: db, actor: actor, r: &Runner{DB: db, Actor: actor, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
}

// host adds a host that is online or offline, with the nightly schedule:
// asleep at 23:00, awake at 07:00, every day.
func (f *fixture) host(name string, online bool, facts store.HostFacts) int64 {
	f.t.Helper()
	ctx := context.Background()
	h, err := f.db.CreateHost(ctx, &store.Host{Name: name, Address: name, Port: 22, Username: "u"})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.MarkHostOnline(ctx, h.ID, facts); err != nil {
		f.t.Fatal(err)
	}
	if !online {
		if err := f.db.MarkHostFailed(ctx, h.ID, store.StatusOffline, "asleep"); err != nil {
			f.t.Fatal(err)
		}
	}
	nightly := Schedule{SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}, Days: EveryDay}
	if err := f.db.SetPowerSchedule(ctx, nightly.ToStore(h.ID)); err != nil {
		f.t.Fatal(err)
	}
	return h.ID
}

func (f *fixture) tick(when string) {
	f.t.Helper()
	f.r.Tick(context.Background(), at(f.t, time.UTC, when))
}

func (f *fixture) last(id int64) *store.PowerSchedule {
	f.t.Helper()
	p, err := f.db.GetPowerSchedule(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestRunnerSleepsAHostWhenItsMinuteComes(t *testing.T) {
	f := newFixture(t)
	id := f.host("tower", true, store.HostFacts{})

	f.tick("2026-11-23 22:59")
	if did := f.actor.take(); len(did) != 0 {
		t.Fatalf("the first look did %v, want nothing: it only marks the start", did)
	}
	f.tick("2026-11-23 23:00")
	if did := f.actor.take(); !reflect.DeepEqual(did, []string{"sleep tower"}) {
		t.Fatalf("at 23:00 did %v, want the sleep", did)
	}
	got := f.last(id)
	if got.LastAction != "sleep" || got.LastError != "" || got.LastAt == nil || !got.LastAt.Equal(at(t, time.UTC, "2026-11-23 23:00")) {
		t.Errorf("recorded %+v, want a clean sleep at 23:00", got)
	}
	// Once, not once a look.
	f.tick("2026-11-23 23:01")
	if did := f.actor.take(); len(did) != 0 {
		t.Errorf("a minute later did %v, want nothing more", did)
	}
}

func TestRunnerWakesAHostThatIsNotAnswering(t *testing.T) {
	f := newFixture(t)
	id := f.host("tower", false, store.HostFacts{})

	f.tick("2026-11-24 06:59")
	f.tick("2026-11-24 07:00")
	if did := f.actor.take(); !reflect.DeepEqual(did, []string{"wake tower"}) {
		t.Fatalf("at 07:00 did %v, want the wake", did)
	}
	if got := f.last(id); got.LastAction != "wake" || got.LastError != "" {
		t.Errorf("recorded %+v, want a clean wake", got)
	}
}

// A host already up at its wake time needs nothing, and the last thing the
// schedule really did stays on its record.
func TestRunnerLeavesAnAwakeHostAlone(t *testing.T) {
	f := newFixture(t)
	id := f.host("tower", true, store.HostFacts{})

	f.tick("2026-11-24 06:59")
	f.tick("2026-11-24 07:00")
	if did := f.actor.take(); len(did) != 0 {
		t.Errorf("woke a host that was up: %v", did)
	}
	if got := f.last(id); got.LastAction != "" {
		t.Errorf("recorded %+v, want nothing", got)
	}
}

// A host that is down cannot be put to sleep, and says so rather than failing
// on a dial that was never going to work.
func TestRunnerDoesNotSleepAHostThatIsNotAnswering(t *testing.T) {
	f := newFixture(t)
	id := f.host("tower", false, store.HostFacts{})

	f.tick("2026-11-23 22:59")
	f.tick("2026-11-23 23:00")
	if did := f.actor.take(); len(did) != 0 {
		t.Errorf("asked a silent host to sleep: %v", did)
	}
	if got := f.last(id); got.LastAction != "sleep" || !strings.Contains(got.LastError, "not answering") {
		t.Errorf("recorded %+v, want the reason", got)
	}
}

// Setting a schedule on the home host is refused at the door, but a host can
// turn out to be the home host afterwards. It is still never put to sleep.
func TestRunnerNeverSleepsTheMachineHostManRunsOn(t *testing.T) {
	f := newFixture(t)
	id := f.host("pi", true, store.HostFacts{IsSelf: true})

	f.tick("2026-11-23 22:59")
	f.tick("2026-11-23 23:00")
	if did := f.actor.take(); len(did) != 0 {
		t.Fatalf("put HostMan's own machine to sleep: %v", did)
	}
	if got := f.last(id); !strings.Contains(got.LastError, "HostMan runs on pi") {
		t.Errorf("recorded %+v, want the reason", got)
	}
}

func TestRunnerRecordsWhyAnActionFailed(t *testing.T) {
	f := newFixture(t)
	id := f.host("tower", true, store.HostFacts{})
	f.actor.sleepErr = errors.New("u needs passwordless sudo on tower to do that")

	f.tick("2026-11-23 22:59")
	f.tick("2026-11-23 23:00")
	if got := f.last(id); got.LastAction != "sleep" || !strings.Contains(got.LastError, "passwordless sudo") {
		t.Errorf("recorded %+v, want the actor's reason", got)
	}
}

// Looks can be far apart: HostMan's own machine may have been suspended. What
// is a few minutes late still runs; what is hours late does not.
func TestRunnerRunsWhatIsALittleLateAndDropsWhatIsVeryLate(t *testing.T) {
	f := newFixture(t)
	f.host("tower", true, store.HostFacts{})

	f.tick("2026-11-23 22:58")
	f.tick("2026-11-23 23:04")
	if did := f.actor.take(); !reflect.DeepEqual(did, []string{"sleep tower"}) {
		t.Errorf("four minutes late did %v, want the sleep", did)
	}

	g := newFixture(t)
	g.host("tower", true, store.HostFacts{})
	g.tick("2026-11-23 22:58")
	g.tick("2026-11-24 10:00")
	if did := g.actor.take(); len(did) != 0 {
		t.Errorf("eleven hours late did %v, want last night's sleep dropped", did)
	}
}

// A clock set back must not run the evening again.
func TestRunnerIgnoresAClockSetBack(t *testing.T) {
	f := newFixture(t)
	f.host("tower", true, store.HostFacts{})

	f.tick("2026-11-23 23:30")
	f.tick("2026-11-23 22:30")
	if did := f.actor.take(); len(did) != 0 {
		t.Errorf("after the clock went back did %v, want nothing", did)
	}
	f.tick("2026-11-23 23:00")
	if did := f.actor.take(); !reflect.DeepEqual(did, []string{"sleep tower"}) {
		t.Errorf("when 23:00 came round did %v, want the sleep", did)
	}
}

// Each look reads the schedules afresh, so one that was changed or removed a
// moment ago is the one that counts, and hosts do not wait on each other.
func TestRunnerFollowsSchedulesAsTheyChange(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tower := f.host("tower", true, store.HostFacts{})
	attic := f.host("attic", true, store.HostFacts{})
	cellar := f.host("cellar", true, store.HostFacts{})

	f.tick("2026-11-23 21:59")
	// tower now sleeps at 22:00; attic no longer sleeps at all.
	early := Schedule{SleepAt: Clock{22, 0}, WakeAt: Clock{7, 0}, Days: EveryDay}
	if err := f.db.SetPowerSchedule(ctx, early.ToStore(tower)); err != nil {
		t.Fatal(err)
	}
	if err := f.db.DeletePowerSchedule(ctx, attic); err != nil {
		t.Fatal(err)
	}
	f.tick("2026-11-23 22:00")
	if did := f.actor.take(); !reflect.DeepEqual(did, []string{"sleep tower"}) {
		t.Errorf("at 22:00 did %v, want only tower's new time", did)
	}
	f.tick("2026-11-23 23:00")
	if did := f.actor.take(); !reflect.DeepEqual(did, []string{"sleep cellar"}) {
		t.Errorf("at 23:00 did %v, want only cellar: tower moved and attic was unset", did)
	}
	_ = cellar
}

func TestScheduleSurvivesBeingStored(t *testing.T) {
	days, _ := DaysOf([]int{0, 3, 6})
	s := Schedule{SleepAt: Clock{23, 45}, WakeAt: Clock{6, 5}, Days: days}
	if got := FromStore(s.ToStore(7)); got != s {
		t.Errorf("came back as %+v, want %+v", got, s)
	}
	if s.ToStore(7).HostID != 7 {
		t.Error("stored for the wrong host")
	}
}
