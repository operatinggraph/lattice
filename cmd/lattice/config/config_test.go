package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/operatinggraph/lattice/cmd/lattice/creds"
)

func TestSetCredential_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := writeCredential("vtx.identity.testActor00000000001", "nats://localhost:4222"); err != nil {
		t.Fatalf("writeCredential: %v", err)
	}

	path := filepath.Join(dir, "lattice", "credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var cf creds.CredentialFile
	if err := json.Unmarshal(data, &cf); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(cf.Credentials) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(cf.Credentials))
	}
	if cf.Credentials[0].ActorKey != "vtx.identity.testActor00000000001" {
		t.Errorf("actorKey = %q", cf.Credentials[0].ActorKey)
	}
	if cf.Credentials[0].NATSURL != "nats://localhost:4222" {
		t.Errorf("natsURL = %q", cf.Credentials[0].NATSURL)
	}

	// Verify file permissions are 0600.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("permissions = %o, want 0600", fi.Mode().Perm())
	}
}

func TestSetCredential_MergesExisting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	actorA := "vtx.identity.actorAAAAAAAAAAAAAAAAA"
	actorB := "vtx.identity.actorBBBBBBBBBBBBBBBBB"

	// Write first credential.
	if err := writeCredential(actorA, "nats://localhost:4222"); err != nil {
		t.Fatalf("writeCredential A: %v", err)
	}

	// Write second (different actor) — should append.
	if err := writeCredential(actorB, "nats://remote:4222"); err != nil {
		t.Fatalf("writeCredential B: %v", err)
	}

	path := filepath.Join(dir, "lattice", "credentials.json")
	data, _ := os.ReadFile(path)
	var cf creds.CredentialFile
	json.Unmarshal(data, &cf) //nolint:errcheck

	if len(cf.Credentials) != 2 {
		t.Fatalf("expected 2 credentials after append, got %d", len(cf.Credentials))
	}

	// Update existing actorA entry — should not duplicate.
	if err := writeCredential(actorA, "nats://updated:4222"); err != nil {
		t.Fatalf("writeCredential update A: %v", err)
	}
	data, _ = os.ReadFile(path)
	json.Unmarshal(data, &cf) //nolint:errcheck
	if len(cf.Credentials) != 2 {
		t.Fatalf("expected 2 credentials after update (no dup), got %d", len(cf.Credentials))
	}

	// Verify the actorA entry was updated.
	var foundURL string
	for _, c := range cf.Credentials {
		if c.ActorKey == actorA {
			foundURL = c.NATSURL
		}
	}
	if foundURL != "nats://updated:4222" {
		t.Errorf("actorA natsURL after update = %q, want nats://updated:4222", foundURL)
	}
}
