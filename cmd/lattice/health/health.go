// Package health implements the lattice health command group.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/bootstrap"
)

// NewCommand returns the cobra.Command for the health command group.
func NewCommand(natsURL, outputFmt *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Inspect platform health and phase-gate statuses",
	}
	cmd.AddCommand(newSummaryCommand(natsURL, outputFmt))
	cmd.AddCommand(newComponentCommand(natsURL, outputFmt))
	cmd.AddCommand(newGatesCommand(natsURL, outputFmt))
	return cmd
}

// healthEntry holds a key/value pair from Health KV for display.
type healthEntry struct {
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

// rollupLevel represents the overall health status.
type rollupLevel int

const (
	rollupGreen  rollupLevel = iota
	rollupYellow             // stale heartbeat, non-zero lag, warning alert
	rollupRed                // absent required key, error alert
)

func (l rollupLevel) String() string {
	switch l {
	case rollupGreen:
		return "GREEN"
	case rollupYellow:
		return "YELLOW"
	default:
		return "RED"
	}
}

// worstOf returns the more severe of two rollup levels.
func worstOf(a, b rollupLevel) rollupLevel {
	if a > b {
		return a
	}
	return b
}

// componentRow holds computed rollup data for one health component row.
type componentRow struct {
	Component string `json:"component"`
	Status    string `json:"status"`
	Freshness string `json:"freshness"`
	Details   string `json:"details"`
	level     rollupLevel
}

// gatesSummary holds the computed gate rollup data.
type gatesSummary struct {
	Passed int            `json:"passed"`
	Total  int            `json:"total"`
	Gates  map[string]any `json:"gates"`
}

// summaryRollup is the JSON shape emitted for --output json.
type summaryRollup struct {
	Overall    string          `json:"overall"`
	Components []componentRow  `json:"components"`
	Alerts     []string        `json:"alerts"`
	Gates      gatesSummary    `json:"gates"`
}

// classifyKey returns the component group for a Health KV key.
func classifyKey(key string) string {
	switch {
	case strings.HasPrefix(key, "health.processor.") && !strings.Contains(strings.TrimPrefix(key, "health.processor."), "."):
		return "processor-heartbeat"
	case strings.HasPrefix(key, "health.processor."):
		return "processor-event"
	case strings.HasPrefix(key, "health.refractor.") && !strings.Contains(strings.TrimPrefix(key, "health.refractor."), "."):
		return "refractor-heartbeat"
	case strings.HasPrefix(key, "health.refractor."):
		return "refractor-event"
	case strings.HasPrefix(key, "health.weaver.") && !strings.Contains(strings.TrimPrefix(key, "health.weaver."), "."):
		return "weaver-heartbeat"
	case strings.HasPrefix(key, "health.weaver."):
		return "weaver-event"
	case strings.HasPrefix(key, "health.loom.") && !strings.Contains(strings.TrimPrefix(key, "health.loom."), "."):
		return "loom-heartbeat"
	case strings.HasPrefix(key, "health.loom."):
		return "loom-event"
	case strings.HasPrefix(key, "health.bootstrap."):
		return "bootstrap"
	case strings.HasPrefix(key, "health.gates.phase1."):
		return "gate"
	case strings.HasPrefix(key, "health.alerts."):
		return "alert"
	default:
		return "lens"
	}
}

// freshnessStr formats time-since as a human-readable "Xs ago" string.
// If t is in the future (clock skew between emitter and CLI host), d is negative
// and is clamped to 0, producing "0s ago". This prevents confusing negative output
// but silently masks diverged clocks; treat "0s ago" on a remote component as a
// possible clock-skew indicator.
func freshnessStr(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%vs ago", int64(d.Seconds()))
}

// parseTimestamp tries to parse an RFC3339 timestamp from a JSON value map.
func parseTimestamp(doc map[string]any, key string) (time.Time, bool) {
	v, ok := doc[key].(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

// issueSeverities extracts the severity of each entry in a heartbeat doc's
// inline issues[] array (Contract #5 §5.2). Malformed entries are skipped.
func issueSeverities(doc map[string]any) []string {
	issues, ok := doc["issues"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(issues))
	for _, it := range issues {
		issue, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if sev, ok := issue["severity"].(string); ok {
			out = append(out, sev)
		}
	}
	return out
}

// heartbeatDetails renders a short metrics summary for a Weaver/Loom heartbeat
// row. It surfaces whichever of the common counters are present.
func heartbeatDetails(doc map[string]any) string {
	metrics, ok := doc["metrics"].(map[string]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, 3)
	if consumers, ok := metrics["consumers"].(map[string]any); ok {
		parts = append(parts, fmt.Sprintf("consumers=%d", len(consumers)))
	}
	if targets, ok := metrics["targets"].(float64); ok {
		parts = append(parts, fmt.Sprintf("targets=%.0f", targets))
	}
	if running, ok := metrics["runningInstances"].(float64); ok {
		parts = append(parts, fmt.Sprintf("runningInstances=%.0f", running))
	}
	return strings.Join(parts, " ")
}

// computeSummaryRollup evaluates all health KV entries and returns structured rollup data.
func computeSummaryRollup(allKeys []string, readEntry func(string) (map[string]any, bool), staleThreshold time.Duration) (summaryRollup, rollupLevel) {
	overall := rollupGreen
	rows := make([]componentRow, 0)
	alertMsgs := make([]string, 0)
	gates := gatesSummary{Gates: map[string]any{}}

	bootstrapPresent := false

	for _, k := range allKeys {
		doc, ok := readEntry(k)
		if !ok {
			continue
		}
		group := classifyKey(k)

		switch group {
		case "processor-heartbeat":
			row := componentRow{Component: k}
			if ts, ok := parseTimestamp(doc, "heartbeatAt"); ok {
				age := time.Since(ts).Round(time.Second)
				row.Freshness = freshnessStr(ts)
				if age > staleThreshold {
					row.Status = "stale"
					row.level = rollupYellow
				} else {
					row.Status = "green"
					row.level = rollupGreen
				}
			} else {
				row.Status = "unknown"
				row.Freshness = "-"
				row.level = rollupYellow
			}
			// Build detail string from metrics.
			if metrics, ok := doc["metrics"].(map[string]any); ok {
				consumed, _ := metrics["ops_consumed_total"].(float64)
				committed, _ := metrics["ops_committed_total"].(float64)
				row.Details = fmt.Sprintf("ops_consumed=%.0f ops_committed=%.0f", consumed, committed)
			}
			overall = worstOf(overall, row.level)
			rows = append(rows, row)

		case "refractor-heartbeat":
			row := componentRow{Component: k}
			if ts, ok := parseTimestamp(doc, "heartbeatAt"); ok {
				age := time.Since(ts).Round(time.Second)
				row.Freshness = freshnessStr(ts)
				if age > staleThreshold {
					row.Status = "stale"
					row.level = rollupYellow
				} else {
					row.Status = "green"
					row.level = rollupGreen
				}
			} else {
				row.Status = "unknown"
				row.Freshness = "-"
				row.level = rollupYellow
			}
			if metrics, ok := doc["metrics"].(map[string]any); ok {
				if lags, ok := metrics["lensLags"].(map[string]any); ok && len(lags) > 0 {
					parts := make([]string, 0, len(lags))
					for lens, lag := range lags {
						lagF, _ := lag.(float64)
						parts = append(parts, fmt.Sprintf("%s=%.0f", lens, lagF))
					}
					row.Details = "lensLags: " + strings.Join(parts, " ")
				}
			}
			overall = worstOf(overall, row.level)
			rows = append(rows, row)

		case "weaver-heartbeat", "loom-heartbeat":
			row := componentRow{Component: k}
			if ts, ok := parseTimestamp(doc, "heartbeatAt"); ok {
				age := time.Since(ts).Round(time.Second)
				row.Freshness = freshnessStr(ts)
				if age > staleThreshold {
					row.Status = "stale"
					row.level = rollupYellow
				} else {
					row.Status = "green"
					row.level = rollupGreen
				}
			} else {
				row.Status = "unknown"
				row.Freshness = "-"
				row.level = rollupYellow
			}
			// Inline issues[] (Contract #5 §5.2): error → red, warning → yellow.
			// Weaver/Loom embed issues in the heartbeat doc rather than as
			// separate health.alerts.* keys, so they are evaluated here.
			for _, it := range issueSeverities(doc) {
				switch it {
				case "error":
					row.level = worstOf(row.level, rollupRed)
					row.Status = "error"
				case "warning":
					row.level = worstOf(row.level, rollupYellow)
					if row.Status == "green" {
						row.Status = "warning"
					}
				}
			}
			row.Details = heartbeatDetails(doc)
			overall = worstOf(overall, row.level)
			rows = append(rows, row)

		case "lens":
			// Per-lens reporter key (bare NanoID).
			row := componentRow{Component: k + " (lens)", Freshness: "-"}
			status, _ := doc["status"].(string)
			consumerLag, _ := doc["consumerLag"].(float64)
			errorCount, _ := doc["errorCount"].(float64)
			row.Details = fmt.Sprintf("consumerLag=%.0f errorCount=%.0f", consumerLag, errorCount)
			switch status {
			case "active":
				if consumerLag > 0 {
					row.Status = "yellow"
					row.level = rollupYellow
				} else {
					row.Status = "active"
					row.level = rollupGreen
				}
			case "paused", "rebuilding":
				row.Status = status
				row.level = rollupYellow
			default:
				row.Status = "unknown"
				row.level = rollupYellow
			}
			overall = worstOf(overall, row.level)
			rows = append(rows, row)

		case "bootstrap":
			bootstrapPresent = true
			row := componentRow{
				Component: k,
				Status:    "green",
				Freshness: "-",
				Details:   "one-shot complete",
				level:     rollupGreen,
			}
			rows = append(rows, row)

		case "gate":
			passed, _ := doc["passed"].(bool)
			shortKey := strings.TrimPrefix(k, "health.gates.phase1.")
			gates.Total++
			if passed {
				gates.Passed++
				gates.Gates[shortKey] = "pass"
			} else {
				gates.Gates[shortKey] = "fail"
				overall = worstOf(overall, rollupYellow)
			}

		case "alert":
			severity, _ := doc["severity"].(string)
			msg, _ := doc["message"].(string)
			alertMsgs = append(alertMsgs, fmt.Sprintf("[%s] %s: %s", severity, k, msg))
			switch severity {
			case "error":
				overall = worstOf(overall, rollupRed)
			case "warning":
				overall = worstOf(overall, rollupYellow)
			}
		}
	}

	if !bootstrapPresent {
		overall = worstOf(overall, rollupRed)
	}

	return summaryRollup{
		Overall:    strings.ToLower(overall.String()),
		Components: rows,
		Alerts:     alertMsgs,
		Gates:      gates,
	}, overall
}

func newSummaryCommand(natsURL, outputFmt *string) *cobra.Command {
	var staleThreshold time.Duration
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show overall platform health with green/yellow/red rollup",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Allow env override when the flag is at its default value.
			threshold := staleThreshold
			if !cmd.Flags().Changed("stale-threshold") {
				if envVal := os.Getenv("LATTICE_HEALTH_STALE_THRESHOLD"); envVal != "" {
					if parsed, err := time.ParseDuration(envVal); err == nil {
						threshold = parsed
					}
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ConnectionError", err.Error())
				}
				return err
			}
			defer conn.Close()

			allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError", err.Error())
				}
				return fmt.Errorf("list health KV: %w", err)
			}

			readEntry := func(k string) (map[string]any, bool) {
				entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, k)
				if err != nil {
					return nil, false
				}
				var doc map[string]any
				if err := json.Unmarshal(entry.Value, &doc); err != nil {
					return nil, false
				}
				return doc, true
			}

			rollup, overallLevel := computeSummaryRollup(allKeys, readEntry, threshold)

			if *outputFmt == "json" {
				return output.PrintJSON(rollup)
			}

			// Human-readable table output.
			if len(rollup.Components) == 0 && len(rollup.Alerts) == 0 && rollup.Gates.Total == 0 {
				fmt.Println("(no health entries)")
				return nil
			}

			fmt.Printf("%-40s %-10s %-14s %s\n", "COMPONENT", "STATUS", "FRESHNESS", "DETAILS")
			for _, row := range rollup.Components {
				fmt.Printf("%-40s %-10s %-14s %s\n", truncate(row.Component, 40), row.Status, row.Freshness, row.Details)
			}

			// Gates line.
			gateDetail := ""
			for gk, gv := range rollup.Gates.Gates {
				gateDetail += fmt.Sprintf(" %s=%v", gk, gv)
			}
			fmt.Printf("Gates passed: %d/%d (%s)\n", rollup.Gates.Passed, rollup.Gates.Total, strings.TrimSpace(gateDetail))

			// Alerts line.
			if len(rollup.Alerts) == 0 {
				fmt.Println("Alerts: none")
			} else {
				for _, a := range rollup.Alerts {
					fmt.Println("Alert:", a)
				}
			}

			fmt.Printf("Overall: %s\n", overallLevel.String())
			return nil
		},
	}
	cmd.Flags().DurationVar(&staleThreshold, "stale-threshold", 60*time.Second,
		"age threshold for stale health entries (env: LATTICE_HEALTH_STALE_THRESHOLD)")
	return cmd
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func newComponentCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "component <name>",
		Short: "Show health entries for a specific component",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			componentName := args[0]
			prefix := "health." + componentName + "."

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError", err.Error())
				}
				return fmt.Errorf("list health KV: %w", err)
			}

			var entries []healthEntry
			for _, k := range allKeys {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, k)
				if err != nil {
					continue
				}
				var val interface{}
				_ = json.Unmarshal(entry.Value, &val)
				entries = append(entries, healthEntry{Key: k, Value: val})
			}

			if *outputFmt == "json" {
				return output.PrintJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Printf("(no health entries for component %q)\n", componentName)
				return nil
			}
			for _, e := range entries {
				valBytes, _ := json.MarshalIndent(e.Value, "  ", "  ")
				fmt.Printf("%s:\n  %s\n\n", e.Key, string(valBytes))
			}
			return nil
		},
	}
}

func newGatesCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "gates",
		Short: "Show Phase 1 gate statuses",
		RunE: func(cmd *cobra.Command, args []string) error {
			gatePrefix := "health.gates.phase1."

			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
					return nil
				}
				return err
			}
			defer conn.Close()

			allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ListError", err.Error())
				}
				return fmt.Errorf("list health KV: %w", err)
			}

			type gateEntry struct {
				Key         string `json:"key"`
				Passed      bool   `json:"passed"`
				CompletedAt string `json:"completedAt,omitempty"`
			}
			var gates []gateEntry
			for _, k := range allKeys {
				if !strings.HasPrefix(k, gatePrefix) {
					continue
				}
				entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, k)
				if err != nil {
					continue
				}
				var doc map[string]interface{}
				if err := json.Unmarshal(entry.Value, &doc); err != nil {
					continue
				}
				passed, _ := doc["passed"].(bool)
				completedAt, _ := doc["completedAt"].(string)
				gates = append(gates, gateEntry{
					Key:         k,
					Passed:      passed,
					CompletedAt: completedAt,
				})
			}

			if *outputFmt == "json" {
				return output.PrintJSON(gates)
			}
			if len(gates) == 0 {
				fmt.Println("(no phase gate entries)")
				return nil
			}
			fmt.Printf("%-45s %-8s %s\n", "GATE", "PASSED", "COMPLETED_AT")
			for _, g := range gates {
				fmt.Printf("%-45s %-8v %s\n", g.Key, g.Passed, g.CompletedAt)
			}
			return nil
		},
	}
}

