package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventJSONRoundTrip(t *testing.T) {
	e := Event{
		Time:       time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
		PID:        4711,
		App:        "payment-api",
		Duration:   4*time.Minute + 500*time.Millisecond,
		Query:      "UPDATE accounts SET balance = 0",
		Terminated: true,
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Guard the wire format: duration must be fractional seconds under
	// duration_s, not Go's default nanosecond integer.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	durS, ok := raw["duration_s"].(float64)
	if !ok {
		t.Fatalf("duration_s missing or not a number: %v", raw)
	}
	if durS != 240.5 {
		t.Errorf("duration_s = %v, want 240.5", durS)
	}
	for _, key := range []string{"ts", "pid", "app", "query", "terminated"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q in wire format: %s", key, data)
		}
	}

	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Time.Equal(e.Time) || got.PID != e.PID || got.App != e.App ||
		got.Duration != e.Duration || got.Query != e.Query || got.Terminated != e.Terminated {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, e)
	}

	// Nanosecond durations that aren't exactly representable as float64
	// seconds must still round-trip (rounded, not truncated).
	odd := Event{Time: e.Time, Duration: 6816617178 * time.Nanosecond}
	oddData, oddErr := json.Marshal(odd)
	if oddErr != nil {
		t.Fatalf("marshal odd duration: %v", oddErr)
	}
	var oddGot Event
	if err := json.Unmarshal(oddData, &oddGot); err != nil {
		t.Fatalf("unmarshal odd duration: %v", err)
	}
	if oddGot.Duration != odd.Duration {
		t.Errorf("odd duration round-trip: got %d, want %d", oddGot.Duration, odd.Duration)
	}
}

func TestOngoingLeakJSON(t *testing.T) {
	data, err := json.Marshal(OngoingLeak{PID: 1, App: "a", Duration: 90 * time.Second, Query: "SELECT 1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["duration_s"] != 90.0 {
		t.Errorf("duration_s = %v, want 90", raw["duration_s"])
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "state", "events.jsonl"))
}

func TestStoreAppendReadSince(t *testing.T) {
	s := testStore(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		e := Event{Time: base.AddDate(0, 0, i), PID: 100 + i, App: "app", Duration: time.Minute}
		if err := s.Append(e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	all, err := s.ReadSince(base)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d events, want 3", len(all))
	}

	// Boundary: events at exactly t are included.
	since, err := s.ReadSince(base.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("read since: %v", err)
	}
	if len(since) != 2 {
		t.Errorf("got %d events since day 1, want 2", len(since))
	}
	if since[0].PID != 101 {
		t.Errorf("first event PID = %d, want 101", since[0].PID)
	}
}

func TestStoreReadSinceMissingFile(t *testing.T) {
	s := testStore(t)
	events, err := s.ReadSince(time.Time{})
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}

func TestStoreSkipsMalformedLines(t *testing.T) {
	s := testStore(t)
	if err := s.Append(Event{Time: time.Now(), PID: 1, App: "a"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	f, openErr := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	if openErr != nil {
		t.Fatalf("open: %v", openErr)
	}
	if _, err := f.WriteString("{not json\n\n"); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Append(Event{Time: time.Now(), PID: 2, App: "b"}); err != nil {
		t.Fatalf("append after garbage: %v", err)
	}

	events, err := s.ReadSince(time.Time{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("got %d events, want 2 (malformed line skipped)", len(events))
	}
}

func TestStorePrune(t *testing.T) {
	s := testStore(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		if err := s.Append(Event{Time: base.AddDate(0, 0, i), PID: i, App: "a"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	cutoff := base.AddDate(0, 0, 3)
	if err := s.Prune(cutoff); err != nil {
		t.Fatalf("prune: %v", err)
	}

	events, err := s.ReadSince(time.Time{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events after prune, want 2", len(events))
	}
	for _, e := range events {
		if e.Time.Before(cutoff) {
			t.Errorf("event %d at %v survived prune cutoff %v", e.PID, e.Time, cutoff)
		}
	}

	// No stray temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, ent := range entries {
		if strings.Contains(ent.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", ent.Name())
		}
	}
}

func TestStorePruneMissingFile(t *testing.T) {
	s := testStore(t)
	if err := s.Prune(time.Now()); err != nil {
		t.Fatalf("prune on missing file should be a no-op: %v", err)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Errorf("prune created the file: %v", err)
	}
}

func TestAggregate(t *testing.T) {
	d := func(m int) time.Duration { return time.Duration(m) * time.Minute }
	events := []Event{
		{App: "payment-api", Duration: d(1), Query: "UPDATE accounts"},
		{App: "payment-api", Duration: d(3), Query: "UPDATE accounts", Terminated: true},
		{App: "payment-api", Duration: d(10), Query: "SELECT 1"},
		{App: "batch-job", Duration: d(2), Query: "DELETE FROM x"},
		{App: "batch-job", Duration: d(4), Query: "DELETE FROM y"},
		{App: "", Duration: d(5), Query: "COMMIT"},
	}

	got := Aggregate(events)
	if len(got) != 3 {
		t.Fatalf("got %d summaries, want 3", len(got))
	}

	// Sort: count desc, then app asc.
	if got[0].App != "payment-api" || got[1].App != "batch-job" || got[2].App != "" {
		t.Errorf("sort order wrong: %q, %q, %q", got[0].App, got[1].App, got[2].App)
	}

	pa := got[0]
	if pa.Count != 3 {
		t.Errorf("count = %d, want 3", pa.Count)
	}
	if pa.MedianDuration != d(3) { // odd count: middle value
		t.Errorf("median = %v, want 3m", pa.MedianDuration)
	}
	if pa.MaxDuration != d(10) {
		t.Errorf("max = %v, want 10m", pa.MaxDuration)
	}
	if pa.TerminatedCount != 1 {
		t.Errorf("terminated = %d, want 1", pa.TerminatedCount)
	}
	if pa.TopQuery != "UPDATE accounts" {
		t.Errorf("top query = %q, want UPDATE accounts", pa.TopQuery)
	}

	bj := got[1]
	if bj.MedianDuration != d(3) { // even count: mean of 2m and 4m
		t.Errorf("even-count median = %v, want 3m", bj.MedianDuration)
	}
	if bj.TopQuery != "DELETE FROM x" { // tie broken by first seen
		t.Errorf("tie-break top query = %q, want DELETE FROM x", bj.TopQuery)
	}
}

func TestAggregateEmpty(t *testing.T) {
	if got := Aggregate(nil); len(got) != 0 {
		t.Errorf("got %d summaries for empty input, want 0", len(got))
	}
}

func TestParseWeekday(t *testing.T) {
	if d, err := ParseWeekday(" Monday "); err != nil || d != time.Monday {
		t.Errorf("ParseWeekday(Monday) = %v, %v", d, err)
	}
	if d, err := ParseWeekday("FRIDAY"); err != nil || d != time.Friday {
		t.Errorf("ParseWeekday(FRIDAY) = %v, %v", d, err)
	}
	if _, err := ParseWeekday("someday"); err == nil {
		t.Error("ParseWeekday(someday) should fail")
	}
}

func TestParseHHMM(t *testing.T) {
	h, m, err := ParseHHMM("09:30")
	if err != nil || h != 9 || m != 30 {
		t.Errorf("ParseHHMM(09:30) = %d, %d, %v", h, m, err)
	}
	for _, bad := range []string{"25:00", "9am", "12:60", ""} {
		if _, _, err := ParseHHMM(bad); err == nil {
			t.Errorf("ParseHHMM(%q) should fail", bad)
		}
	}
}

func TestNextRun(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}

	tests := []struct {
		name string
		now  time.Time
		day  time.Weekday
		hhmm string
		want time.Time
	}{
		{
			name: "same day before slot",
			now:  time.Date(2026, 6, 8, 8, 0, 0, 0, time.UTC), // Monday
			day:  time.Monday,
			hhmm: "09:00",
			want: time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "same day after slot rolls a week",
			now:  time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC), // Monday
			day:  time.Monday,
			hhmm: "09:00",
			want: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "exactly at slot rolls a week",
			now:  time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC), // Monday
			day:  time.Monday,
			hhmm: "09:00",
			want: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "crosses week boundary",
			now:  time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC), // Friday
			day:  time.Tuesday,
			hhmm: "07:15",
			want: time.Date(2026, 6, 16, 7, 15, 0, 0, time.UTC),
		},
		{
			name: "across spring-forward DST",
			// Sat Mar 7 2026; DST starts Sun Mar 8 in America/New_York.
			now:  time.Date(2026, 3, 7, 12, 0, 0, 0, ny),
			day:  time.Monday,
			hhmm: "09:00",
			want: time.Date(2026, 3, 9, 9, 0, 0, 0, ny),
		},
		{
			name: "across fall-back DST",
			// Sat Oct 31 2026; DST ends Sun Nov 1 in America/New_York.
			now:  time.Date(2026, 10, 31, 12, 0, 0, 0, ny),
			day:  time.Monday,
			hhmm: "09:00",
			want: time.Date(2026, 11, 2, 9, 0, 0, 0, ny),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextRun(tt.now, tt.day, tt.hhmm)
			if !got.Equal(tt.want) {
				t.Errorf("NextRun() = %v, want %v", got, tt.want)
			}
			if got.Weekday() != tt.day {
				t.Errorf("NextRun() weekday = %v, want %v", got.Weekday(), tt.day)
			}
			if !got.After(tt.now) {
				t.Errorf("NextRun() = %v, not after now %v", got, tt.now)
			}
		})
	}
}
