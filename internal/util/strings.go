package util

import (
	"fmt"
	"strings"
	"time"
)

// Truncate shortens a string to maxLen characters, adding "..." if truncated
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// TruncateQuery truncates a query string, first normalizing whitespace
func TruncateQuery(s string, maxLen int) string {
	// Remove newlines and extra whitespace
	s = strings.Join(strings.Fields(s), " ")
	return Truncate(s, maxLen)
}

// FormatDuration formats a duration in a human-readable format
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}

	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", m, s)
	}

	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}
