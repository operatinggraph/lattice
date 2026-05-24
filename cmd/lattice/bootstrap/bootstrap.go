// Package bootstrap implements the lattice bootstrap command group.
package bootstrap

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/output"
	internalbootstrap "github.com/asolgan/lattice/internal/bootstrap"
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
	return cmd
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

