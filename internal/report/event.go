// Package report records completed idle-in-transaction leaks and aggregates
// them into per-application summaries for the weekly digest and the
// `pguard report` command.
package report

import (
	"encoding/json"
	"math"
	"time"
)

// MaxQueryChars is the single truncation limit for query text captured for
// reporting. It is applied once, at capture time in the daemon.
const MaxQueryChars = 200

// Event is one completed leak: an idle-in-transaction connection that crossed
// the warning threshold and then resolved or was terminated.
type Event struct {
	Time       time.Time // when the event was recorded (resolution/termination)
	PID        int
	App        string        // application_name; empty string allowed
	Duration   time.Duration // max observed idle duration (from pg state_change)
	Query      string        // truncated to MaxQueryChars
	Terminated bool          // true if pguard auto-terminated it
}

// eventWire is the JSONL representation. Durations are fractional seconds
// (not Go's default nanosecond integers) so the event file stays friendly to
// jq and spreadsheets; changing this after shipping is a format migration.
type eventWire struct {
	Time       time.Time `json:"ts"`
	PID        int       `json:"pid"`
	App        string    `json:"app"`
	DurationS  float64   `json:"duration_s"`
	Query      string    `json:"query"`
	Terminated bool      `json:"terminated"`
}

// MarshalJSON implements json.Marshaler.
func (e Event) MarshalJSON() ([]byte, error) {
	return json.Marshal(eventWire{
		Time:       e.Time,
		PID:        e.PID,
		App:        e.App,
		DurationS:  e.Duration.Seconds(),
		Query:      e.Query,
		Terminated: e.Terminated,
	})
}

// UnmarshalJSON implements json.Unmarshaler.
func (e *Event) UnmarshalJSON(data []byte) error {
	var w eventWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*e = Event{
		Time:       w.Time,
		PID:        w.PID,
		App:        w.App,
		Duration:   time.Duration(math.Round(w.DurationS * float64(time.Second))),
		Query:      w.Query,
		Terminated: w.Terminated,
	}
	return nil
}

// OngoingLeak is a leak that is still open. Ongoing leaks are read live from
// the daemon's tracking state (or pg_stat_activity) and never written to the
// store, so they cannot be double-counted when they eventually resolve.
type OngoingLeak struct {
	PID      int
	App      string
	Duration time.Duration // current idle duration (state_change-based)
	Query    string        // truncated to MaxQueryChars
}

type ongoingWire struct {
	PID       int     `json:"pid"`
	App       string  `json:"app"`
	DurationS float64 `json:"duration_s"`
	Query     string  `json:"query"`
}

// MarshalJSON implements json.Marshaler with fractional-second durations,
// matching the Event wire format.
func (o OngoingLeak) MarshalJSON() ([]byte, error) {
	return json.Marshal(ongoingWire{
		PID:       o.PID,
		App:       o.App,
		DurationS: o.Duration.Seconds(),
		Query:     o.Query,
	})
}

// DisplayName renders an application_name for output; backends that never set
// one are shown as "(unknown)".
func DisplayName(app string) string {
	if app == "" {
		return "(unknown)"
	}
	return app
}
