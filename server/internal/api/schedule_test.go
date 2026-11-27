package api

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// sleeper is a host a schedule can be set on: it can sleep, and HostMan knows
// where to send the packet that wakes it.
func sleeper(t *testing.T, s *Server, h http.Handler) int64 {
	t.Helper()
	id := newHost(t, h)
	facts := store.HostFacts{Hostname: "tower", CanSleep: true, WakeMAC: "a8:a1:59:0c:1f:2e"}
	if err := s.DB.MarkHostOnline(context.Background(), id, facts); err != nil {
		t.Fatal(err)
	}
	return id
}

const nightly = `{"sleepAt":"23:00","wakeAt":"07:00","days":[1,2,3,4,5]}`

func TestPowerScheduleSetChangeAndUnset(t *testing.T) {
	s, h := testServer(t, "")
	id := sleeper(t, s, h)
	path := fmt.Sprintf("/api/hosts/%d/power/schedule", id)

	// None to begin with, and the answer still carries HostMan's clock.
	w := do(t, h, "GET", path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d %s", w.Code, w.Body)
	}
	got := decode[scheduleView](t, w)
	if got.Schedule != nil {
		t.Fatalf("a new host has a schedule: %+v", got.Schedule)
	}
	if _, err := time.Parse("15:04", got.Clock); err != nil || got.Zone == "" {
		t.Errorf("clock = %q, zone = %q, want HostMan's time of day and zone", got.Clock, got.Zone)
	}

	// Set.
	w = do(t, h, "PUT", path, nightly)
	if w.Code != http.StatusOK {
		t.Fatalf("set = %d %s", w.Code, w.Body)
	}
	got = decode[scheduleView](t, w)
	if got.Schedule == nil || got.Schedule.SleepAt != "23:00" || got.Schedule.WakeAt != "07:00" ||
		!reflect.DeepEqual(got.Schedule.Days, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("after set: %+v", got.Schedule)
	}
	if got.Schedule.Next == nil || !got.Schedule.Next.At.After(time.Now()) {
		t.Errorf("next = %+v, want something still to come", got.Schedule.Next)
	}
	if got.Schedule.Last != nil {
		t.Errorf("last = %+v on a schedule that has not run", got.Schedule.Last)
	}

	// Change: the same request with other values, and one schedule still.
	w = do(t, h, "PUT", path, `{"sleepAt":"01:30","wakeAt":"06:15","days":[6,0]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("change = %d %s", w.Code, w.Body)
	}
	got = decode[scheduleView](t, do(t, h, "GET", path, ""))
	if got.Schedule == nil || got.Schedule.SleepAt != "01:30" || got.Schedule.WakeAt != "06:15" ||
		!reflect.DeepEqual(got.Schedule.Days, []int{0, 6}) {
		t.Fatalf("after change: %+v", got.Schedule)
	}
	if list, _ := s.DB.ListPowerSchedules(context.Background()); len(list) != 1 {
		t.Errorf("%d schedules stored, want the one", len(list))
	}

	// Unset, and unsetting again is no different.
	for i := 0; i < 2; i++ {
		if w := do(t, h, "DELETE", path, ""); w.Code != http.StatusNoContent {
			t.Fatalf("unset = %d %s, want 204", w.Code, w.Body)
		}
	}
	if got := decode[scheduleView](t, do(t, h, "GET", path, "")); got.Schedule != nil {
		t.Errorf("after unset: %+v", got.Schedule)
	}
}

func TestPowerScheduleShowsWhatItLastDid(t *testing.T) {
	s, h := testServer(t, "")
	id := sleeper(t, s, h)
	path := fmt.Sprintf("/api/hosts/%d/power/schedule", id)
	do(t, h, "PUT", path, nightly)

	ran := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := s.DB.RecordPowerScheduleRun(context.Background(), id, "sleep", ran, "pi needs passwordless sudo"); err != nil {
		t.Fatal(err)
	}
	got := decode[scheduleView](t, do(t, h, "GET", path, ""))
	last := got.Schedule.Last
	if last == nil || last.Action != "sleep" || !last.At.Equal(ran) || !strings.Contains(last.Error, "sudo") {
		t.Errorf("last = %+v, want the failed sleep", last)
	}
}

func TestPowerScheduleRefusesWhatMakesNoSense(t *testing.T) {
	s, h := testServer(t, "")
	id := sleeper(t, s, h)
	path := fmt.Sprintf("/api/hosts/%d/power/schedule", id)

	for _, body := range []string{
		``, `{}`,
		`{"sleepAt":"23:00","wakeAt":"07:00","days":[]}`,
		`{"sleepAt":"23:00","wakeAt":"07:00"}`,
		`{"sleepAt":"23:00","wakeAt":"23:00","days":[1]}`,
		`{"sleepAt":"11pm","wakeAt":"07:00","days":[1]}`,
		`{"sleepAt":"23:00","wakeAt":"7:00","days":[1]}`,
		`{"sleepAt":"24:00","wakeAt":"07:00","days":[1]}`,
		`{"sleepAt":"23:00","wakeAt":"07:00","days":[7]}`,
		`{"sleepAt":"23:00","wakeAt":"07:00","days":[1],"action":"shutdown"}`,
	} {
		if w := do(t, h, "PUT", path, body); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, w.Code)
		}
	}
	if list, _ := s.DB.ListPowerSchedules(context.Background()); len(list) != 0 {
		t.Errorf("a refused schedule was stored: %+v", list[0])
	}
}

// A schedule is only set where both halves can work.
func TestPowerScheduleNeedsAHostThatCanSleepAndBeWoken(t *testing.T) {
	mac := "a8:a1:59:0c:1f:2e"
	cases := map[string]struct {
		facts store.HostFacts
		want  string
	}{
		"no sleep state":      {store.HostFacts{WakeMAC: mac}, "no sleep state"},
		"nothing to wake":     {store.HostFacts{CanSleep: true}, "never wake it"},
		"HostMan's own host":  {store.HostFacts{CanSleep: true, WakeMAC: mac, IsSelf: true}, "HostMan runs on"},
		"never probed at all": {store.HostFacts{}, "no sleep state"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, h := testServer(t, "")
			id := newHost(t, h)
			if err := s.DB.MarkHostOnline(context.Background(), id, c.facts); err != nil {
				t.Fatal(err)
			}
			w := do(t, h, "PUT", fmt.Sprintf("/api/hosts/%d/power/schedule", id), nightly)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("set = %d %s, want 400 saying %q", w.Code, w.Body, c.want)
			}
		})
	}
}

func TestPowerScheduleOnAMissingHost(t *testing.T) {
	_, h := testServer(t, "")
	for _, c := range []struct{ method, body string }{{"GET", ""}, {"PUT", nightly}, {"DELETE", ""}} {
		if w := do(t, h, c.method, "/api/hosts/99/power/schedule", c.body); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", c.method, w.Code)
		}
	}
}

func TestPowerScheduleNeedsTheSession(t *testing.T) {
	_, h := testServer(t, "1234")
	for _, c := range []struct{ method, body string }{{"GET", ""}, {"PUT", nightly}, {"DELETE", ""}} {
		if w := do(t, h, c.method, "/api/hosts/1/power/schedule", c.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", c.method, w.Code)
		}
	}
}

// The schedule wakes a host by what the Wake button does, and is told why
// where there is nothing to send.
func TestScheduledWakeIsTheWakeButton(t *testing.T) {
	s, h := testServer(t, "")
	waker := &recordingWaker{}
	s.Wake = waker
	id := sleeper(t, s, h)
	ctx := context.Background()
	host, _ := s.DB.GetHost(ctx, id)

	if err := s.ScheduledPower().Wake(ctx, host); err != nil {
		t.Fatal(err)
	}
	if len(waker.woke) != 1 || waker.woke[0] != "a8:a1:59:0c:1f:2e" {
		t.Errorf("woke %v, want the host's recorded address once", waker.woke)
	}

	host.WakeMAC = ""
	if err := s.ScheduledPower().Wake(ctx, host); err == nil || !strings.Contains(err.Error(), "wired network card") {
		t.Errorf("waking a host with no address: err = %v, want the reason", err)
	}
	if len(waker.woke) != 1 {
		t.Errorf("a packet was sent to nowhere: %v", waker.woke)
	}
}
