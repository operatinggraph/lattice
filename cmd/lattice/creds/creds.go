// Package creds provides the shared credential file types and path helper
// used by all lattice CLI commands that load or store actor credentials.
package creds

import (
	"os"
	"path/filepath"
)

// CredentialFile is the on-disk shape of the credentials file.
type CredentialFile struct {
	Credentials []Credential `json:"credentials"`
}

// Credential is a single actor entry in the credentials file.
type Credential struct {
	ActorKey string `json:"actorKey"`
	NATSURL  string `json:"natsURL"`
}

// CredentialFilePath returns the credentials file path, respecting
// XDG_CONFIG_HOME if set, falling back to ~/.lattice/credentials.json.
func CredentialFilePath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "lattice", "credentials.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lattice/credentials.json"
	}
	return filepath.Join(home, ".lattice", "credentials.json")
}
