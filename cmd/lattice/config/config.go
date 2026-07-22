// Package config implements the lattice config command group.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/operatinggraph/lattice/cmd/lattice/creds"
	"github.com/operatinggraph/lattice/cmd/lattice/output"
)

// NewCommand returns the cobra.Command for the config group.
func NewCommand(natsURL, outputFmt *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage lattice CLI configuration and credentials",
	}

	cmd.AddCommand(newSetCredentialCommand(natsURL, outputFmt))
	return cmd
}

func newSetCredentialCommand(natsURL, outputFmt *string) *cobra.Command {
	var actorKey string

	cmd := &cobra.Command{
		Use:   "set-credential",
		Short: "Write actor credentials to the local credential store",
		Long: `set-credential writes an actor key and NATS URL to
~/.lattice/credentials.json (or $XDG_CONFIG_HOME/lattice/credentials.json).

The file is created if absent. Existing entries with the same actorKey
are updated; new actorKeys are appended.

File permissions are set to 0600 (owner read/write only).`,
		Example: `  lattice config set-credential --actor-key vtx.identity.<NanoID> --nats-url nats://localhost:4222`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if actorKey == "" {
				return fmt.Errorf("--actor-key is required")
			}
			url := *natsURL
			if err := writeCredential(actorKey, url); err != nil {
				if *outputFmt == "json" {
					return output.PrintJSONError("ConfigError", err.Error())
				}
				return err
			}
			if *outputFmt == "json" {
				return output.PrintJSON(map[string]string{
					"actorKey": actorKey,
					"natsURL":  url,
					"path":     creds.CredentialFilePath(),
				})
			}
			fmt.Printf("credential written: %s\n  actorKey: %s\n  natsURL: %s\n",
				creds.CredentialFilePath(), actorKey, url)
			return nil
		},
	}

	cmd.Flags().StringVar(&actorKey, "actor-key", "", "actor key (e.g. vtx.identity.<NanoID>)")
	return cmd
}

// writeCredential writes or updates the credential for actorKey at natsURL.
// Existing entries are merged by actorKey; new entries are appended.
// The write is atomic: data is written to a temp file in the same directory
// then renamed over the final path so a crash mid-write cannot truncate it.
func writeCredential(actorKey, natsURL string) error {
	path := creds.CredentialFilePath()

	// Load existing file if present.
	var cf creds.CredentialFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cf) // ignore parse errors — will overwrite
	}

	// Merge: update existing actorKey or append.
	found := false
	for i, c := range cf.Credentials {
		if c.ActorKey == actorKey {
			cf.Credentials[i].NATSURL = natsURL
			found = true
			break
		}
	}
	if !found {
		cf.Credentials = append(cf.Credentials, creds.Credential{
			ActorKey: actorKey,
			NATSURL:  natsURL,
		})
	}

	// Ensure directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create credential dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	// Write atomically via a temp file in the same directory, then rename.
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename credential file: %w", err)
	}
	return nil
}
