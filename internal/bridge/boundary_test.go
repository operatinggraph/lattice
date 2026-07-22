package bridge_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bridge"
)

// TestStart_EmptyActorKeyFails asserts the bridge fails LOUD at Start when
// ActorKey is empty, rather than silently publishing result ops under actor:""
// (which the Processor rejects off-stream with no signal). The guard fires
// before any consumer attaches, so a nil conn never gets dereferenced; there is
// no fabricated default identity.
func TestStart_EmptyActorKeyFails(t *testing.T) {
	t.Parallel()
	eng := bridge.NewEngine(nil, bridge.Config{ActorKey: ""})
	err := eng.Start(context.Background())
	if err == nil {
		t.Fatal("Start must error on empty ActorKey, got nil")
	}
	if !strings.Contains(err.Error(), "ActorKey") {
		t.Errorf("Start error should name ActorKey, got %q", err.Error())
	}
}

// TestModuleBoundary_OnlySubstrate enforces the module-boundary rule
// (docs/components/bridge.md Principles): internal/bridge imports only
// internal/substrate. It never imports internal/processor, internal/loom,
// internal/refractor, or internal/weaver (incl. internal/weaver/nudge) anywhere
// in its dependency tree — the bridge is a leaf on substrate, owning the adapter
// contract with no dependency on any orchestration engine. All cross-component
// interaction is over NATS.
func TestModuleBoundary_OnlySubstrate(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "github.com/operatinggraph/lattice/internal/bridge").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"github.com/operatinggraph/lattice/internal/processor",
		"github.com/operatinggraph/lattice/internal/loom",
		"github.com/operatinggraph/lattice/internal/refractor",
		"github.com/operatinggraph/lattice/internal/weaver",
		"github.com/operatinggraph/lattice/internal/weaver/nudge",
	}
	for _, line := range strings.Split(string(out), "\n") {
		dep := strings.TrimSpace(line)
		for _, f := range forbidden {
			if dep == f || strings.HasPrefix(dep, f+"/") {
				t.Errorf("internal/bridge must not import %q (module boundary)", dep)
			}
		}
	}
}

// TestModuleBoundary_NoRawNATS enforces that internal/bridge carries no raw
// nats.io/jetstream handle of its own — every NATS interaction goes through a
// substrate primitive. DIRECT imports only: substrate itself legitimately
// depends on nats.go transitively, so a transitive check would false-positive.
func TestModuleBoundary_NoRawNATS(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}",
		"github.com/operatinggraph/lattice/internal/bridge").Output()
	if err != nil {
		t.Fatalf("go list imports: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		dep := strings.TrimSpace(line)
		if strings.HasPrefix(dep, "github.com/nats-io/") {
			t.Errorf("internal/bridge must not directly import %q (no raw NATS handle — "+
				"use a substrate primitive)", dep)
		}
	}
}
