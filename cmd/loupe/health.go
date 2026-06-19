package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// healthComponent is one component card the UI renders: a status label, a
// human-readable freshness string, and any issue lines.
type healthComponent struct {
	Key       string   `json:"key"`
	Group     string   `json:"group"`
	Status    string   `json:"status"`
	Freshness string   `json:"freshness"`
	Issues    []string `json:"issues,omitempty"`
}

// healthRollup is the GET /api/health response.
type healthRollup struct {
	Overall    string            `json:"overall"`
	Components []healthComponent `json:"components"`
	Alerts     []string          `json:"alerts"`
}

// classifyHealthKey groups a Health KV key into a component bucket. Mirrors the
// shape of cmd/lattice/health.classifyKey: a bare heartbeat key (no dot after
// the component segment) is the component's heartbeat; a deeper key is an event;
// bootstrap / alert keys are recognized explicitly; everything else is a lens
// reporter (a bare NanoID).
func classifyHealthKey(key string) string {
	switch {
	case strings.HasPrefix(key, "health.processor.") && !strings.Contains(strings.TrimPrefix(key, "health.processor."), "."):
		return "processor"
	case strings.HasPrefix(key, "health.processor."):
		return "processor-event"
	case strings.HasPrefix(key, "health.refractor.") && !strings.Contains(strings.TrimPrefix(key, "health.refractor."), "."):
		return "refractor"
	case strings.HasPrefix(key, "health.bootstrap."):
		return "bootstrap"
	case strings.HasPrefix(key, "health.gates."):
		return "gate"
	case strings.HasPrefix(key, "health.alerts."):
		return "alert"
	default:
		return "lens"
	}
}

// freshness formats time-since as "Xs ago", clamping a future timestamp (clock
// skew between emitter and Loupe host) to "0s ago".
func freshness(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%ds ago", int64(d.Seconds()))
}

// parseHealthTime reads an RFC3339 timestamp out of a JSON value map.
func parseHealthTime(doc map[string]any, key string) (time.Time, bool) {
	v, ok := doc[key].(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

// computeHealth evaluates every Health KV entry into component cards plus an
// overall rollup (green/yellow/red). readEntry returns the decoded JSON doc for
// a key (and false to skip). staleThreshold is the heartbeat age past which a
// component is "stale" (yellow). It mirrors the rollup logic in
// cmd/lattice/health but emits the per-card shape the Loupe UI renders.
func computeHealth(keys []string, readEntry func(string) (map[string]any, bool), staleThreshold time.Duration) healthRollup {
	const (
		green  = 0
		yellow = 1
		red    = 2
	)
	overall := green
	worse := func(lvl int) {
		if lvl > overall {
			overall = lvl
		}
	}

	components := make([]healthComponent, 0, len(keys))
	alerts := make([]string, 0)
	bootstrapPresent := false

	for _, k := range keys {
		doc, ok := readEntry(k)
		if !ok {
			continue
		}
		group := classifyHealthKey(k)
		switch group {
		case "processor", "refractor":
			c := healthComponent{Key: k, Group: group}
			if ts, ok := parseHealthTime(doc, "heartbeatAt"); ok {
				c.Freshness = freshness(ts)
				if time.Since(ts) > staleThreshold {
					c.Status = "stale"
					c.Issues = append(c.Issues, "heartbeat older than "+staleThreshold.String())
					worse(yellow)
				} else {
					c.Status = "green"
				}
			} else {
				c.Status = "unknown"
				c.Freshness = "-"
				worse(yellow)
			}
			components = append(components, c)

		case "lens":
			c := healthComponent{Key: k + " (lens)", Group: group, Freshness: "-"}
			status, _ := doc["status"].(string)
			consumerLag, _ := doc["consumerLag"].(float64)
			errorCount, _ := doc["errorCount"].(float64)
			switch status {
			case "active":
				if consumerLag > 0 {
					c.Status = "yellow"
					c.Issues = append(c.Issues, fmt.Sprintf("consumerLag=%.0f", consumerLag))
					worse(yellow)
				} else {
					c.Status = "active"
				}
			case "paused", "rebuilding":
				c.Status = status
				worse(yellow)
			default:
				c.Status = "unknown"
				worse(yellow)
			}
			if errorCount > 0 {
				c.Issues = append(c.Issues, fmt.Sprintf("errorCount=%.0f", errorCount))
				worse(yellow)
			}
			components = append(components, c)

		case "bootstrap":
			bootstrapPresent = true
			components = append(components, healthComponent{
				Key:       k,
				Group:     group,
				Status:    "green",
				Freshness: "-",
			})

		case "alert":
			severity, _ := doc["severity"].(string)
			msg, _ := doc["message"].(string)
			alerts = append(alerts, fmt.Sprintf("[%s] %s: %s", severity, k, msg))
			switch severity {
			case "error":
				worse(red)
			case "warning":
				worse(yellow)
			}
		}
	}

	if !bootstrapPresent {
		worse(red)
	}

	sort.Slice(components, func(i, j int) bool { return components[i].Key < components[j].Key })

	return healthRollup{
		Overall:    [...]string{"green", "yellow", "red"}[overall],
		Components: components,
		Alerts:     alerts,
	}
}
