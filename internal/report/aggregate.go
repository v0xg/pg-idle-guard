package report

import (
	"encoding/json"
	"sort"
	"time"
)

// AppSummary aggregates a window of events for one application_name.
type AppSummary struct {
	App             string
	Count           int
	MedianDuration  time.Duration
	MaxDuration     time.Duration
	TerminatedCount int
	TopQuery        string // most frequent truncated query text; ties broken by first seen
}

type appSummaryWire struct {
	App             string  `json:"app"`
	Count           int     `json:"count"`
	MedianDurationS float64 `json:"median_duration_s"`
	MaxDurationS    float64 `json:"max_duration_s"`
	TerminatedCount int     `json:"terminated"`
	TopQuery        string  `json:"top_query"`
}

// MarshalJSON implements json.Marshaler with fractional-second durations,
// matching the Event wire format.
func (a AppSummary) MarshalJSON() ([]byte, error) {
	return json.Marshal(appSummaryWire{
		App:             a.App,
		Count:           a.Count,
		MedianDurationS: a.MedianDuration.Seconds(),
		MaxDurationS:    a.MaxDuration.Seconds(),
		TerminatedCount: a.TerminatedCount,
		TopQuery:        a.TopQuery,
	})
}

// Aggregate groups events by App. The result is sorted by Count descending,
// then App ascending for stable output. Empty App is grouped under its real
// (empty) key; rendering it as "(unknown)" is a display concern.
func Aggregate(events []Event) []AppSummary {
	type group struct {
		durations  []time.Duration
		terminated int
		queryCount map[string]int
		queryOrder []string // first-seen order, for deterministic tie-breaking
	}

	byApp := make(map[string]*group)
	for _, e := range events {
		g := byApp[e.App]
		if g == nil {
			g = &group{queryCount: make(map[string]int)}
			byApp[e.App] = g
		}
		g.durations = append(g.durations, e.Duration)
		if e.Terminated {
			g.terminated++
		}
		if _, seen := g.queryCount[e.Query]; !seen {
			g.queryOrder = append(g.queryOrder, e.Query)
		}
		g.queryCount[e.Query]++
	}

	summaries := make([]AppSummary, 0, len(byApp))
	for app, g := range byApp {
		sort.Slice(g.durations, func(i, j int) bool { return g.durations[i] < g.durations[j] })

		topQuery := ""
		best := 0
		for _, q := range g.queryOrder {
			if g.queryCount[q] > best {
				best = g.queryCount[q]
				topQuery = q
			}
		}

		summaries = append(summaries, AppSummary{
			App:             app,
			Count:           len(g.durations),
			MedianDuration:  median(g.durations),
			MaxDuration:     g.durations[len(g.durations)-1],
			TerminatedCount: g.terminated,
			TopQuery:        topQuery,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Count != summaries[j].Count {
			return summaries[i].Count > summaries[j].Count
		}
		return summaries[i].App < summaries[j].App
	})
	return summaries
}

// median returns the middle value of an ascending-sorted slice; for an even
// count it returns the mean of the two middle values.
func median(sorted []time.Duration) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
