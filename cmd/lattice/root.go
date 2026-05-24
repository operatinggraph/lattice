package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/asolgan/lattice/cmd/lattice/authtrace"
	"github.com/asolgan/lattice/cmd/lattice/bootstrap"
	"github.com/asolgan/lattice/cmd/lattice/candidates"
	"github.com/asolgan/lattice/cmd/lattice/config"
	"github.com/asolgan/lattice/cmd/lattice/creds"
	"github.com/asolgan/lattice/cmd/lattice/graph"
	"github.com/asolgan/lattice/cmd/lattice/health"
	"github.com/asolgan/lattice/cmd/lattice/identity"
	"github.com/asolgan/lattice/cmd/lattice/lens"
	"github.com/asolgan/lattice/cmd/lattice/op"
	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/cmd/lattice/query"
)

// Global persistent flag values shared across all subcommands.
var (
	flagNATSURL  string
	flagConfig   string
	flagOutput   string
	flagActorKey string // loaded from credential file; overridable per-command
)

// rootCmd is the top-level cobra command.
var rootCmd = &cobra.Command{
	Use:   "lattice",
	Short: "Lattice CLI — operator and developer access to Phase 1 Lattice capabilities",
	Long: `lattice is the unified CLI tool for the Lattice platform.

It provides access to operations submission, graph inspection, projection
query surfaces, health monitoring, and identity management — all via NATS.

All commands accept --nats-url (or NATS_URL env) and --config (or
LATTICE_CONFIG env). Use --output json for machine-readable output.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command. Called from main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// ErrJSONError means the error envelope is already on stdout;
		// suppress the stderr echo to avoid double-printing.
		if !errors.Is(err, output.ErrJSONError) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initCredentials)

	rootCmd.PersistentFlags().StringVar(&flagNATSURL, "nats-url", "", "NATS server URL (env: NATS_URL, default nats://localhost:4222)")
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "", "config file (env: LATTICE_CONFIG, default ~/.lattice/config.json)")
	rootCmd.PersistentFlags().StringVarP(&flagOutput, "output", "o", "", "output format: json (default: human-readable table)")

	// Register all 9 command groups.
	rootCmd.AddCommand(config.NewCommand(&flagNATSURL, &flagOutput))
	rootCmd.AddCommand(op.NewCommand(&flagNATSURL, &flagOutput, &flagActorKey))
	rootCmd.AddCommand(graph.NewCommand(&flagNATSURL, &flagOutput))
	rootCmd.AddCommand(lens.NewCommand(&flagNATSURL, &flagOutput, &flagActorKey))
	rootCmd.AddCommand(query.NewCommand(&flagNATSURL, &flagOutput))
	rootCmd.AddCommand(health.NewCommand(&flagNATSURL, &flagOutput))
	rootCmd.AddCommand(identity.NewCommand(&flagNATSURL, &flagOutput, &flagActorKey))
	rootCmd.AddCommand(candidates.NewCommand(&flagNATSURL, &flagOutput, &flagActorKey))
	rootCmd.AddCommand(authtrace.NewCommand(&flagNATSURL, &flagOutput))
	rootCmd.AddCommand(bootstrap.NewCommand(&flagNATSURL, &flagOutput))
}

// initCredentials loads the credential file and populates the default
// actor key. Runs before every subcommand. Errors are non-fatal —
// subcommands that require an actor key will fail with a clear message.
func initCredentials() {
	// Resolve NATS URL: flag > env > default.
	if flagNATSURL == "" {
		if v := os.Getenv("NATS_URL"); v != "" {
			flagNATSURL = v
		} else {
			flagNATSURL = "nats://localhost:4222"
		}
	}

	// Load credential file for actorKey default.
	credPath := creds.CredentialFilePath()
	data, err := os.ReadFile(credPath)
	if err != nil {
		return // credential file absent — not an error at startup
	}
	var cf creds.CredentialFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return // malformed credential file — non-fatal; subcommand will fail if it needs actorKey
	}
	if len(cf.Credentials) > 0 {
		flagActorKey = cf.Credentials[0].ActorKey
		if flagNATSURL == "nats://localhost:4222" && cf.Credentials[0].NATSURL != "" {
			flagNATSURL = cf.Credentials[0].NATSURL
		}
	}
}
