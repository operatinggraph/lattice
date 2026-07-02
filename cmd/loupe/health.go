package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// healthComponent is one card the UI renders. Name is the descriptive label
// (a component name, or a lens's canonicalName); Detail is a secondary line
// (the component instance id, or "lens · <description>"); Key is the raw Health
// KV key (kept for reference / control-plane lookups).
type healthComponent struct {
	Key       string   `json:"key"`
	Group     string   `json:"group"`
	Name      string   `json:"name"`
	Detail    string   `json:"detail,omitempty"`
	Status    string   `json:"status"`
	Freshness string   `json:"freshness"`
	Issues    []string `json:"issues,omitempty"`
}

// healthRollup is the GET /api/health response. Bootstrap reports whether the
// health.bootstrap.* kernel-seed marker is present — the shell's alert strip
// renders its absence as the red "bootstrap incomplete" line.
type healthRollup struct {
	Overall    string            `json:"overall"`
	Components []healthComponent `json:"components"`
	Alerts     []string          `json:"alerts"`
	Bootstrap  bool              `json:"bootstrap"`
}

// How a Health KV key is rendered.
const (
	kindComponent = "component"
	kindLens      = "lens"
	kindBootstrap = "bootstrap"
	kindAlert     = "alert"
	kindGate      = "gate"
	kindEvent     = "event"
)

// classifyHealthKey groups a Health KV key. A `health.<component>.<instance>`
// key (no further dots) is that component's Contract #5 heartbeat — this covers
// processor, refractor, loom, weaver, bridge, and object-store-manager
// uniformly. A deeper `health.<component>.…` key is a per-component event;
// bootstrap / gate / alert keys are recognized explicitly. Everything else is a
// bare-NanoID lens reporter (the lens's meta.lens vertex id).
func classifyHealthKey(key string) (group, kind string) {
	switch {
	case strings.HasPrefix(key, "health.bootstrap."):
		return "bootstrap", kindBootstrap
	case strings.HasPrefix(key, "health.gates."):
		return "gate", kindGate
	case strings.HasPrefix(key, "health.alerts."):
		return "alert", kindAlert
	case strings.HasPrefix(key, "health."):
		comp, inst, found := strings.Cut(strings.TrimPrefix(key, "health."), ".")
		if found && comp != "" && inst != "" && !strings.Contains(inst, ".") {
			return comp, kindComponent
		}
		return comp, kindEvent
	default:
		return "lens", kindLens
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

// componentHeartbeat reads a component's heartbeat timestamp. Most daemons stamp
// "heartbeatAt"; object-store-manager stamps "updatedAt" — both are tried.
func componentHeartbeat(doc map[string]any) (time.Time, bool) {
	for _, field := range []string{"heartbeatAt", "updatedAt"} {
		if ts, ok := parseHealthTime(doc, field); ok {
			return ts, true
		}
	}
	return time.Time{}, false
}

// Severity levels shared by computeHealth and computeSystemMap: a component's
// rendered status maps to one of these so the two rollups agree.
const (
	sevGreen  = 0
	sevYellow = 1
	sevRed    = 2
)

// componentIssues flattens a Contract #5 §5.5 issues[] array (objects with
// code/severity/message/since) into readable "[severity] code: message" lines,
// in document order, and reports the worst severity seen (sevGreen if none,
// sevYellow for a "warning", sevRed for an "error" — matching §5.3, where an
// error issue means the component cannot fulfill its primary responsibility).
// Malformed or empty entries are skipped.
func componentIssues(doc map[string]any) (lines []string, sev int) {
	raw, ok := doc["issues"].([]any)
	if !ok {
		return nil, sevGreen
	}
	lines = make([]string, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		code, _ := m["code"].(string)
		severity, _ := m["severity"].(string)
		message, _ := m["message"].(string)
		switch severity {
		case "error":
			if sev < sevRed {
				sev = sevRed
			}
		case "warning":
			if sev < sevYellow {
				sev = sevYellow
			}
		}
		var b strings.Builder
		if severity != "" {
			b.WriteString("[" + severity + "] ")
		}
		b.WriteString(code)
		if message != "" {
			if code != "" {
				b.WriteString(": ")
			}
			b.WriteString(message)
		}
		if s := b.String(); s != "" {
			lines = append(lines, s)
		}
	}
	return lines, sev
}

// componentLiveness derives a component card/node's rendered status, freshness
// string, issue lines, and severity level (sevGreen/sevYellow/sevRed) from a
// Contract #5 heartbeat doc. It fuses three signals, taking the worst:
//   - heartbeat freshness Loupe computes itself (a component that died without
//     shutting down shows "stale" even if it last self-reported healthy);
//   - the self-reported §5.4 status ("degraded" → yellow, "unhealthy" → red);
//   - the worst §5.5 issue severity (an "error" issue → red, "warning" → yellow)
//     — so a component that emits error issues but a stale/lagging status field
//     is still surfaced honestly rather than rendered falsely green.
//
// A missing/unrecognized status on a fresh heartbeat with no issues stays
// "green" (backward-compatible with components that don't yet emit the anomaly
// channel). The status label tracks the final level: red → "unhealthy",
// otherwise stale → "stale", otherwise yellow → "degraded", else "green".
func componentLiveness(doc map[string]any, staleThreshold time.Duration) (status, fresh string, issues []string, level int) {
	var issueSev int
	issues, issueSev = componentIssues(doc)

	ts, ok := componentHeartbeat(doc)
	if !ok {
		level = sevYellow
		if issueSev > level {
			level = issueSev
		}
		return "unknown", "-", issues, level
	}
	fresh = freshness(ts)
	stale := time.Since(ts) > staleThreshold

	switch reported, _ := doc["status"].(string); reported {
	case "unhealthy":
		level = sevRed
	case "degraded":
		level = sevYellow
	}
	if stale {
		issues = append([]string{"heartbeat older than " + staleThreshold.String()}, issues...)
		if level < sevYellow {
			level = sevYellow
		}
	}
	if issueSev > level {
		level = issueSev
	}

	switch {
	case level == sevRed:
		status = "unhealthy"
	case stale:
		status = "stale"
	case level == sevYellow:
		status = "degraded"
	default:
		status = "green"
	}
	return status, fresh, issues, level
}

// computeHealth evaluates every Health KV entry into component cards plus an
// overall rollup (green/yellow/red). readEntry returns the decoded JSON doc for
// a key (and false to skip). resolveLens maps a lens reporter id to its
// (canonicalName, description) for a readable card label; resolveSpec joins the
// lens spec for the renderedState derivation (either may be nil, e.g. in
// tests). staleThreshold is the heartbeat age past which a component is
// "stale" (yellow).
func computeHealth(
	keys []string,
	readEntry func(string) (map[string]any, bool),
	resolveLens func(id string) (name, desc string),
	resolveSpec func(id string) lensSpecInfo,
	staleThreshold time.Duration,
) healthRollup {
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
		group, kind := classifyHealthKey(k)
		switch kind {
		case kindComponent:
			c := healthComponent{Key: k, Group: group, Name: group}
			if comp, ok := doc["component"].(string); ok && comp != "" {
				c.Name = comp
			}
			if inst, ok := doc["instance"].(string); ok && inst != "" {
				c.Detail = inst
			}
			var level int
			c.Status, c.Freshness, c.Issues, level = componentLiveness(doc, staleThreshold)
			worse(level)
			components = append(components, c)

		case kindLens:
			c := healthComponent{Key: k, Group: "lens", Name: k, Detail: "lens", Freshness: "-"}
			if resolveLens != nil {
				if name, desc := resolveLens(k); name != "" {
					c.Name = name
					if desc != "" {
						c.Detail = "lens · " + desc
					}
				}
			}
			var spec lensSpecInfo
			if resolveSpec != nil {
				spec = resolveSpec(k)
			}
			var level int
			c.Status, c.Issues, level = lensRenderedState(doc, spec)
			worse(level)
			components = append(components, c)

		case kindBootstrap:
			bootstrapPresent = true
			components = append(components, healthComponent{
				Key:       k,
				Group:     "bootstrap",
				Name:      "bootstrap",
				Status:    "green",
				Freshness: "-",
			})

		case kindAlert:
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
		// kindGate and kindEvent are not rendered as cards.
	}

	if !bootstrapPresent {
		worse(red)
	}

	sort.Slice(components, func(i, j int) bool {
		if components[i].Name != components[j].Name {
			return components[i].Name < components[j].Name
		}
		return components[i].Key < components[j].Key
	})

	return healthRollup{
		Overall:    [...]string{"green", "yellow", "red"}[overall],
		Components: components,
		Alerts:     alerts,
		Bootstrap:  bootstrapPresent,
	}
}
