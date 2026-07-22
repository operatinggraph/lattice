// Package bootstrap implements the lattice bootstrap command group.
package bootstrap

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/output"
	internalbootstrap "github.com/operatinggraph/lattice/internal/bootstrap"
)

// NewCommand returns the cobra.Command for the bootstrap command group.
func NewCommand(natsURL, outputFmt *string) *cobra.Command {
	var bootstrapJSONPath string

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Verify and inspect the Lattice kernel bootstrap state",
	}
	cmd.PersistentFlags().StringVar(&bootstrapJSONPath, "bootstrap-json", "./lattice.bootstrap.json",
		"path to lattice.bootstrap.json (env: BOOTSTRAP_JSON_PATH)")

	cmd.AddCommand(newVerifyCommand(natsURL, outputFmt, &bootstrapJSONPath))
	cmd.AddCommand(newInspectCommand(natsURL, outputFmt, &bootstrapJSONPath))
	cmd.AddCommand(newProbeEmptyCommand(natsURL, outputFmt))
	return cmd
}

// newProbeEmptyCommand reports whether Core KV is empty (a recreated or
// never-seeded bucket) as opposed to holding a populated graph. It is the
// file-independent discriminator `make up` uses after a `verify` mismatch to
// decide the recovery: an empty Core KV means keep lattice.bootstrap.json so
// the bootstrap binary re-seeds the primordial set at its committed stable
// NanoIDs; a populated Core KV holding a different set means discard the stale
// file and mint fresh.
//
// Exit 0 if Core KV is empty (or absent). Exit 1 if it holds any entries.
// Exit 2 on a connection or probe error — an outcome `make up` treats like
// "empty" (keep the file), so a transient fault never triggers the destructive
// discard-and-mint path. It reads no file, so it takes no --bootstrap-json.
func newProbeEmptyCommand(natsURL, outputFmt *string) *cobra.Command {
	return &cobra.Command{
		Use:   "probe-empty",
		Short: "Report whether Core KV is empty; exit 0 if empty/absent, 1 if populated, 2 on error",
		Long: `probe-empty is the file-independent discriminator make up uses after a
bootstrap verify mismatch. It answers whether Core KV holds any entries at all,
without consulting lattice.bootstrap.json.

Exit 0 if Core KV is empty or absent (a recreated stack — the surviving
bootstrap file should be kept so the primordial set re-seeds at its stable
NanoIDs). Exit 1 if Core KV is populated (a different set is live — the stale
file should be discarded and a fresh set minted). Exit 2 on a connection or
probe error, which callers should treat conservatively as "keep the file".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), output.DefaultTimeout)
			defer cancel()

			conn, err := output.Connect(ctx, *natsURL)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ConnectionError", err.Error())
				} else {
					fmt.Fprintf(os.Stderr, "probe-empty: connection error: %v\n", err)
				}
				os.Exit(2)
			}
			defer conn.Close()

			empty, err := internalbootstrap.CoreKVEmpty(ctx, conn)
			if err != nil {
				if *outputFmt == "json" {
					_ = output.PrintJSONError("ProbeError", err.Error())
				} else {
					fmt.Fprintf(os.Stderr, "probe-empty: %v\n", err)
				}
				os.Exit(2)
			}

			if *outputFmt == "json" {
				_ = output.PrintJSON(map[string]interface{}{"empty": empty})
			} else if empty {
				fmt.Println("Core KV empty (recreated or never seeded)")
			} else {
				fmt.Println("Core KV populated")
			}

			if !empty {
				os.Exit(1)
			}
			return nil
		},
	}
}

func newVerifyCommand(natsURL, outputFmt, bootstrapJSONPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Assert primordial Core KV state; exit 0 on success, non-zero on failures",
		Long: `verify runs assertions equivalent to make verify-kernel: checks that
all post-Story-5.3 kernel Core KV keys exist with correct envelopes
and that all required JetStream streams and KV buckets are present.

Exit 0 if all assertions pass. Exit 1 with a summary of failures otherwise.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve bootstrap JSON path from flag or env.
			jsonPath := *bootstrapJSONPath
			if envPath := os.Getenv("BOOTSTRAP_JSON_PATH"); envPath != "" {
				jsonPath = envPath
			}

			if err := internalbootstrap.Load(jsonPath); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("BootstrapLoadError", err.Error())
				}
				return fmt.Errorf("load bootstrap IDs from %s: %w", jsonPath, err)
			}

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

			failures := internalbootstrap.VerifyKernel(ctx, conn)

			if *outputFmt == "json" {
				return output.PrintJSON(map[string]interface{}{
					"passed":   len(failures) == 0,
					"failures": failures,
				})
			}

			if len(failures) == 0 {
				fmt.Println("verify-kernel: ALL ASSERTIONS PASSED")
				return nil
			}
			fmt.Printf("verify-kernel: %d FAILURE(S)\n\n", len(failures))
			for _, f := range failures {
				fmt.Printf("  - %s\n", f)
			}
			fmt.Println("\nSuggestion: run `make down && make up` to re-bootstrap from clean state.")
			os.Exit(1)
			return nil
		},
	}
}

func newInspectCommand(natsURL, outputFmt, bootstrapJSONPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect",
		Short: "Read and print selected primordial kernel entries",
		Long: `inspect reads and prints the primordial kernel entries (top-level vertex
keys) from Core KV in a human-readable table. Does not modify state.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonPath := *bootstrapJSONPath
			if envPath := os.Getenv("BOOTSTRAP_JSON_PATH"); envPath != "" {
				jsonPath = envPath
			}

			if err := internalbootstrap.Load(jsonPath); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("BootstrapLoadError", err.Error())
				}
				return fmt.Errorf("load bootstrap IDs from %s: %w", jsonPath, err)
			}

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

			entries, err := internalbootstrap.InspectKernel(ctx, conn)
			if err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("InspectError", err.Error())
				}
				return err
			}

			if *outputFmt == "json" {
				return output.PrintJSON(entries)
			}

			fmt.Printf("%-60s %-20s %s\n", "KEY", "CLASS", "IS_DELETED")
			for _, e := range entries {
				if e.Missing {
					fmt.Printf("%-60s %-20s %s\n", e.Key, "<MISSING>", "?")
					continue
				}
				class, _ := e.Doc["class"].(string)
				isDeleted, _ := e.Doc["isDeleted"].(bool)
				fmt.Printf("%-60s %-20s %v\n", e.Key, class, isDeleted)
			}
			return nil
		},
	}
}

