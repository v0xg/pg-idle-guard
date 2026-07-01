package report

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

var weekdays = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

// ParseWeekday parses a case-insensitive English weekday name.
func ParseWeekday(s string) (time.Weekday, error) {
	if d, ok := weekdays[strings.ToLower(strings.TrimSpace(s))]; ok {
		return d, nil
	}
	return 0, fmt.Errorf("invalid weekday %q", s)
}

// ParseHHMM parses a 24-hour "HH:MM" string into hour and minute.
func ParseHHMM(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid time %q (want 24h HH:MM): %w", s, err)
	}
	return t.Hour(), t.Minute(), nil
}

// NextRun returns the next occurrence of the configured weekday at hhmm in
// now's location, strictly after now — if now is exactly this week's slot,
// the result is next week's. hhmm must be valid per ParseHHMM (enforced by
// config validation); an invalid value falls back to 09:00 with an error log.
func NextRun(now time.Time, day time.Weekday, hhmm string) time.Time {
	h, m, err := ParseHHMM(hhmm)
	if err != nil {
		slog.Error("invalid report time, falling back to 09:00", "time", hhmm, "error", err)
		h, m = 9, 0
	}

	next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	for next.Weekday() != day || !next.After(now) {
		// Re-normalize via time.Date each step so DST transitions (where the
		// wall-clock slot may not exist or repeats) resolve consistently.
		next = next.AddDate(0, 0, 1)
		next = time.Date(next.Year(), next.Month(), next.Day(), h, m, 0, 0, now.Location())
	}
	return next
}
