package powersched

import (
	"reflect"
	"testing"
	"time"
)

func TestParseClock(t *testing.T) {
	good := map[string]Clock{"00:00": {0, 0}, "07:05": {7, 5}, "23:59": {23, 59}, "12:30": {12, 30}}
	for in, want := range good {
		got, err := ParseClock(in)
		if err != nil || got != want {
			t.Errorf("ParseClock(%q) = %v, %v, want %v", in, got, err, want)
		}
		if got.String() != in {
			t.Errorf("%v prints as %q, want %q back", got, got.String(), in)
		}
		if ClockAt(got.Minutes()) != got {
			t.Errorf("%v does not survive being stored as minutes", got)
		}
	}
	for _, in := range []string{"", "7:05", "24:00", "23:60", "23:00:00", "ab:cd", "23-00", " 7:05", "+1:00", "11pm"} {
		if got, err := ParseClock(in); err == nil {
			t.Errorf("ParseClock(%q) = %v, want it refused", in, got)
		}
	}
}

func TestDays(t *testing.T) {
	d, err := DaysOf([]int{5, 1, 1, 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.List(); !reflect.DeepEqual(got, []int{1, 3, 5}) {
		t.Errorf("List = %v, want [1 3 5]", got)
	}
	if !d.Has(time.Monday) || d.Has(time.Sunday) {
		t.Errorf("Has is wrong for %v", d.List())
	}
	if got := Days(0).List(); got == nil || len(got) != 0 {
		t.Errorf("an empty set lists as %#v, want an empty list and not nil", got)
	}
	all, _ := DaysOf([]int{0, 1, 2, 3, 4, 5, 6})
	if all != EveryDay {
		t.Errorf("all seven days = %b, want EveryDay", all)
	}
	for _, bad := range [][]int{{7}, {-1}, {1, 9}} {
		if _, err := DaysOf(bad); err == nil {
			t.Errorf("DaysOf(%v) was accepted", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	weekdays, _ := DaysOf([]int{1, 2, 3, 4, 5})
	ok := Schedule{SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}, Days: weekdays}
	if err := ok.Validate(); err != nil {
		t.Errorf("a sound schedule was refused: %v", err)
	}
	bad := map[string]Schedule{
		"no days":         {SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}},
		"the same minute": {SleepAt: Clock{7, 0}, WakeAt: Clock{7, 0}, Days: weekdays},
		"hour 24":         {SleepAt: Clock{24, 0}, WakeAt: Clock{7, 0}, Days: weekdays},
		"minute 60":       {SleepAt: Clock{23, 0}, WakeAt: Clock{7, 60}, Days: weekdays},
		"an eighth day":   {SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}, Days: 1 << 7},
	}
	for name, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func at(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	got, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// 2026-11-23 is a Monday.
func TestNext(t *testing.T) {
	weekdays, _ := DaysOf([]int{1, 2, 3, 4, 5})
	night := Schedule{SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}, Days: weekdays}
	siesta := Schedule{SleepAt: Clock{13, 0}, WakeAt: Clock{15, 0}, Days: EveryDay}

	cases := []struct {
		name   string
		s      Schedule
		after  string
		want   string
		action Action
	}{
		{"before the morning's wake", night, "2026-11-23 06:00", "2026-11-23 07:00", Wake},
		{"between wake and sleep", night, "2026-11-23 12:00", "2026-11-23 23:00", Sleep},
		{"after the night's sleep", night, "2026-11-23 23:30", "2026-11-24 07:00", Wake},
		{"the minute itself is not after itself", night, "2026-11-23 07:00", "2026-11-23 23:00", Sleep},
		{"Friday night sleeps through to Monday", night, "2026-11-27 23:00", "2026-11-30 07:00", Wake},
		{"on a day off", night, "2026-11-28 12:00", "2026-11-30 07:00", Wake},
		{"sleep first where it comes first", siesta, "2026-11-23 09:00", "2026-11-23 13:00", Sleep},
		{"then the wake", siesta, "2026-11-23 13:00", "2026-11-23 15:00", Wake},
	}
	for _, c := range cases {
		got, ok := c.s.Next(at(t, time.UTC, c.after))
		if !ok || !got.At.Equal(at(t, time.UTC, c.want)) || got.Action != c.action {
			t.Errorf("%s: Next = %v %s (%v), want %s %s", c.name, got.At, got.Action, ok, c.want, c.action)
		}
	}

	// One day a week still finds its day, a full week away at most.
	sundays, _ := DaysOf([]int{0})
	once := Schedule{SleepAt: Clock{22, 0}, WakeAt: Clock{8, 0}, Days: sundays}
	got, ok := once.Next(at(t, time.UTC, "2026-11-22 22:00"))
	if !ok || !got.At.Equal(at(t, time.UTC, "2026-11-29 08:00")) || got.Action != Wake {
		t.Errorf("a week on: Next = %v %s (%v)", got.At, got.Action, ok)
	}
	if _, ok := (Schedule{SleepAt: Clock{22, 0}, WakeAt: Clock{8, 0}}).Next(time.Now()); ok {
		t.Error("a schedule with no days has a next event")
	}
}

// The schedule runs on the wall clock of the time it is asked about, so the
// same instant reads differently from two zones.
func TestNextFollowsTheClockItIsAskedOn(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no zone database here:", err)
	}
	s := Schedule{SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}, Days: EveryDay}
	got, _ := s.Next(at(t, berlin, "2026-11-23 06:30"))
	if want := at(t, berlin, "2026-11-23 07:00"); !got.At.Equal(want) {
		t.Errorf("Next = %v, want 07:00 in Berlin (%v)", got.At, want)
	}
}

// Clocks changing must neither lose a day's events nor run them twice.
func TestNextAcrossAClockChange(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no zone database here:", err)
	}
	// 2026-03-29: 02:00 becomes 03:00, so 02:30 does not happen.
	skipped := Schedule{SleepAt: Clock{2, 30}, WakeAt: Clock{9, 0}, Days: EveryDay}
	got, ok := skipped.Next(at(t, berlin, "2026-03-29 01:00"))
	if !ok || got.Action != Sleep || got.At.Sub(at(t, berlin, "2026-03-29 01:00")) > 2*time.Hour {
		t.Errorf("spring forward: Next = %v %s, want the sleep that night all the same", got.At, got.Action)
	}
	next, _ := skipped.Next(got.At)
	if next.Action != Wake || !next.At.Equal(at(t, berlin, "2026-03-29 09:00")) {
		t.Errorf("after it: Next = %v %s, want the 09:00 wake", next.At, next.Action)
	}

	// 2026-10-25: 03:00 becomes 02:00, so 02:30 happens twice.
	first, _ := skipped.Next(at(t, berlin, "2026-10-25 01:00"))
	second, _ := skipped.Next(first.At)
	if first.Action != Sleep || second.Action != Wake {
		t.Errorf("fall back: %s then %s, want one sleep and then the wake", first.Action, second.Action)
	}
}

func TestLatest(t *testing.T) {
	night := Schedule{SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}, Days: EveryDay}
	weekdays, _ := DaysOf([]int{1, 2, 3, 4, 5})

	cases := []struct {
		name     string
		s        Schedule
		from, to string
		want     string
		action   Action
	}{
		{"nothing fell due", night, "2026-11-23 12:00", "2026-11-23 12:01", "", ""},
		{"the minute arrives", night, "2026-11-23 22:59", "2026-11-23 23:00", "2026-11-23 23:00", Sleep},
		{"and is not run a second time", night, "2026-11-23 23:00", "2026-11-23 23:01", "", ""},
		{"a wake across midnight's window", night, "2026-11-23 23:30", "2026-11-24 07:00", "2026-11-24 07:00", Wake},
		{"of two owed, the later one", night, "2026-11-23 22:00", "2026-11-24 08:00", "2026-11-24 07:00", Wake},
		{"a day that is not scheduled", Schedule{SleepAt: Clock{23, 0}, WakeAt: Clock{7, 0}, Days: weekdays},
			"2026-11-28 22:59", "2026-11-28 23:00", "", ""},
		{"an empty window", night, "2026-11-23 23:00", "2026-11-23 23:00", "", ""},
		{"a window that runs backwards", night, "2026-11-24 08:00", "2026-11-23 22:00", "", ""},
	}
	for _, c := range cases {
		got, ok := c.s.Latest(at(t, time.UTC, c.from), at(t, time.UTC, c.to))
		if c.want == "" {
			if ok {
				t.Errorf("%s: Latest = %v %s, want nothing", c.name, got.At, got.Action)
			}
			continue
		}
		if !ok || !got.At.Equal(at(t, time.UTC, c.want)) || got.Action != c.action {
			t.Errorf("%s: Latest = %v %s (%v), want %s %s", c.name, got.At, got.Action, ok, c.want, c.action)
		}
	}
}

// Stepping minute by minute through a week, Latest must hand out exactly the
// events Next promises: none missed, none twice.
func TestLatestAndNextAgreeOverAWeek(t *testing.T) {
	days, _ := DaysOf([]int{0, 2, 3, 6})
	s := Schedule{SleepAt: Clock{0, 0}, WakeAt: Clock{23, 59}, Days: days}
	start := at(t, time.UTC, "2026-11-22 00:00").Add(-time.Minute)
	end := start.Add(8 * 24 * time.Hour)

	var promised []Event
	for e, ok := s.Next(start); ok && !e.At.After(end); e, ok = s.Next(e.At) {
		promised = append(promised, e)
	}
	var ran []Event
	for t0 := start; t0.Before(end); t0 = t0.Add(time.Minute) {
		if e, ok := s.Latest(t0, t0.Add(time.Minute)); ok {
			ran = append(ran, e)
		}
	}
	if len(promised) != 10 {
		t.Fatalf("Next promised %d events in eight days of four, want 10", len(promised))
	}
	if !reflect.DeepEqual(ran, promised) {
		t.Errorf("ran %v\nwant %v", ran, promised)
	}
}
