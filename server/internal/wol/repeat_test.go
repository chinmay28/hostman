package wol

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// sentAt records when each packet left, by address, on the test's own clock.
type sentAt struct {
	mu    sync.Mutex
	start time.Time
	sent  map[string][]time.Duration
	err   error
}

func newSentAt() *sentAt {
	return &sentAt{start: time.Now(), sent: map[string][]time.Duration{}}
}

func (s *sentAt) Wake(_ context.Context, mac net.HardwareAddr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent[mac.String()] = append(s.sent[mac.String()], time.Since(s.start))
	return nil
}

func (s *sentAt) times(mac net.HardwareAddr) []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.sent[mac.String()]...)
}

func (s *sentAt) failWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

var (
	tower = net.HardwareAddr{0xa8, 0xa1, 0x59, 0x0c, 0x1f, 0x2e}
	attic = net.HardwareAddr{0xa8, 0xa1, 0x59, 0x0c, 0x1f, 0x2f}
)

func secs(n ...int) []time.Duration {
	out := make([]time.Duration, len(n))
	for i, s := range n {
		out[i] = time.Duration(s) * time.Second
	}
	return out
}

// The point of repeating: the first packet goes at once, and the rest follow
// on the schedule, so a machine that was still going down when Wake was
// pressed hears one after it has gone.
func TestRepeaterSendsNowAndThenOnTheSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		r := &Repeater{Sender: lan}
		if err := r.Wake(context.Background(), tower); err != nil {
			t.Fatal(err)
		}
		if got := lan.times(tower); !reflect.DeepEqual(got, secs(0)) {
			t.Fatalf("on return, sent at %v, want one packet already gone", got)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		want := append(secs(0), Repeats...)
		if got := lan.times(tower); !reflect.DeepEqual(got, want) {
			t.Errorf("sent at %v, want %v", got, want)
		}
		// Close returning shows the schedule ended by itself.
		r.Close()
	})
}

// The default schedule has to cover a suspend and most of a shutdown, and has
// to end: a packet a quarter of an hour later would wake a machine someone
// has since turned off by hand.
func TestRepeatsCoverTheFirstMinuteAndStopThere(t *testing.T) {
	if len(Repeats) == 0 {
		t.Fatal("nothing is repeated")
	}
	for i := 1; i < len(Repeats); i++ {
		if Repeats[i] <= Repeats[i-1] {
			t.Errorf("Repeats is not ascending at %d: %v", i, Repeats)
		}
	}
	if first := Repeats[0]; first < 4*time.Second {
		t.Errorf("first repeat at %v lands before a host told to sleep has gone", first)
	}
	if last := Repeats[len(Repeats)-1]; last < 30*time.Second || last > 2*time.Minute {
		t.Errorf("last repeat at %v, want it around a minute", last)
	}
}

// A first packet that could not leave is the caller's answer, and nothing is
// tried behind its back afterwards.
func TestRepeaterSchedulesNothingWhenTheFirstSendFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		lan.failWith(errors.New("network is unreachable"))
		r := &Repeater{Sender: lan}
		if err := r.Wake(context.Background(), tower); err == nil {
			t.Fatal("Wake = nil, want the send's error")
		}
		lan.failWith(nil)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if got := lan.times(tower); len(got) != 0 {
			t.Errorf("sent at %v after a failed first send, want nothing", got)
		}
	})
}

// Sending a machine down on purpose cancels what an earlier wake still owed
// it, and leaves every other machine's schedule alone.
func TestCancelStopsOneAddressOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		r := &Repeater{Sender: lan, After: secs(10, 20, 30)}
		for _, mac := range [][]byte{tower, attic} {
			if err := r.Wake(context.Background(), mac); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(15 * time.Second)
		r.Cancel(tower)
		r.Cancel(tower) // nothing left to cancel, and no harm in asking
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := lan.times(tower); !reflect.DeepEqual(got, secs(0, 10)) {
			t.Errorf("cancelled address sent at %v, want it to stop after 10s", got)
		}
		if got := lan.times(attic); !reflect.DeepEqual(got, secs(0, 10, 20, 30)) {
			t.Errorf("other address sent at %v, want its whole schedule", got)
		}
	})
}

// A second press starts the schedule again from that press; it does not run
// two schedules side by side.
func TestWakingAgainRestartsTheSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		r := &Repeater{Sender: lan, After: secs(10, 20)}
		if err := r.Wake(context.Background(), tower); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Second)
		if err := r.Wake(context.Background(), tower); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := lan.times(tower); !reflect.DeepEqual(got, secs(0, 10, 15, 25, 35)) {
			t.Errorf("sent at %v, want the first schedule replaced at 15s", got)
		}
	})
}

// The repeats belong to the Repeater, not to the request that asked: the
// request's context ending must not end them.
func TestRepeatsOutliveTheCallersContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		r := &Repeater{Sender: lan, After: secs(10)}
		ctx, cancel := context.WithCancel(context.Background())
		if err := r.Wake(ctx, tower); err != nil {
			t.Fatal(err)
		}
		cancel()
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := lan.times(tower); !reflect.DeepEqual(got, secs(0, 10)) {
			t.Errorf("sent at %v, want the repeat sent after the caller had gone", got)
		}
	})
}

// A repeat that fails is reported and the schedule carries on: the network
// that was down at five seconds may be back at fifteen.
func TestAFailedRepeatIsReportedAndTheRestStillGo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		var mu sync.Mutex
		var reported []error
		r := &Repeater{Sender: lan, After: secs(10, 20), OnRepeat: func(_ net.HardwareAddr, err error) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, err)
		}}
		if err := r.Wake(context.Background(), tower); err != nil {
			t.Fatal(err)
		}
		down := errors.New("network is unreachable")
		lan.failWith(down)
		time.Sleep(15 * time.Second)
		lan.failWith(nil)
		time.Sleep(time.Minute)
		synctest.Wait()

		mu.Lock()
		defer mu.Unlock()
		if len(reported) != 2 || !errors.Is(reported[0], down) || reported[1] != nil {
			t.Errorf("reported %v, want the failure and then a success", reported)
		}
		if got := lan.times(tower); !reflect.DeepEqual(got, secs(0, 20)) {
			t.Errorf("sent at %v, want the later repeat to have gone", got)
		}
	})
}

// Close ends everything owed, and a Repeater that is closed still sends the
// packet it is asked for — it only stops promising more.
func TestCloseDropsWhatIsOwed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		r := &Repeater{Sender: lan, After: secs(10)}
		if err := r.Wake(context.Background(), tower); err != nil {
			t.Fatal(err)
		}
		r.Close()
		if err := r.Wake(context.Background(), attic); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := lan.times(tower); !reflect.DeepEqual(got, secs(0)) {
			t.Errorf("sent at %v after Close, want only the first packet", got)
		}
		if got := lan.times(attic); !reflect.DeepEqual(got, secs(0)) {
			t.Errorf("a closed Repeater sent at %v, want one packet and no repeats", got)
		}
	})
}

// An explicitly empty schedule is one packet, as before.
func TestAnEmptyScheduleSendsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lan := newSentAt()
		r := &Repeater{Sender: lan, After: []time.Duration{}}
		if err := r.Wake(context.Background(), tower); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if got := lan.times(tower); !reflect.DeepEqual(got, secs(0)) {
			t.Errorf("sent at %v, want one packet", got)
		}
	})
}
