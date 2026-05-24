// Package health implements the lattice health command group.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

func newSummaryCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "summary",
		Short: "Show a summary of all Health KV entries",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				fmt.Println("(no health entries)")
				return nil
			}
			fmt.Printf("%-60s %s\n", "KEY", "VALUE_SNIPPET")
			for _, e := range entries {
				snippet := snippetOf(e.Value, 80)
				fmt.Printf("%-60s %s\n", e.Key, snippet)
			}
			return nil
		},
	}
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

// snippetOf returns a JSON snippet of val truncated to maxLen chars.
func snippetOf(val interface{}, maxLen int) string {
	b, err := json.Marshal(val)
	if err != nil {
		return "<error>"
	}
	s := string(b)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
