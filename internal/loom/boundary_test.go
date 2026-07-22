package loom_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/loom"
)

// TestStart_EmptyActorKeyFails asserts Loom fails LOUD at Start when ActorKey is
// empty, rather than silently publishing instance/result ops under actor:""
// (which the Processor rejects off-stream with no signal). The guard fires
// before any consumer attaches, so a nil conn is never dereferenced.
func TestStart_EmptyActorKeyFails(t *testing.T) {
	t.Parallel()
	eng := loom.NewEngine(nil, loom.Config{ActorKey: ""})
	err := eng.Start(context.Background())
	if err == nil {
		t.Fatal("Start must error on empty ActorKey, got nil")
	}
	if !strings.Contains(err.Error(), "ActorKey") {
		t.Errorf("Start error should name ActorKey, got %q", err.Error())
	}
}

// TestModuleBoundary_OnlySubstrate enforces AC #7/#8: internal/loom never
// imports internal/processor, internal/weaver, or internal/refractor anywhere in
// its dependency tree. The check uses `go list -deps` (transitive).
func TestModuleBoundary_OnlySubstrate(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "github.com/operatinggraph/lattice/internal/loom").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"github.com/operatinggraph/lattice/internal/processor",
		"github.com/operatinggraph/lattice/internal/weaver",
		"github.com/operatinggraph/lattice/internal/refractor",
	}
	for _, line := range strings.Split(string(out), "\n") {
		dep := strings.TrimSpace(line)
		for _, f := range forbidden {
			if dep == f || strings.HasPrefix(dep, f+"/") {
				t.Errorf("internal/loom must not import %q (AC #7 module boundary)", dep)
			}
		}
	}
}

// TestModuleBoundary_NoRawNATS enforces AC #8: internal/loom carries no raw
// nats.io/jetstream handle of its own — every NATS interaction goes through a
// substrate primitive (the command-outbox relay publishes via substrate.Publish;
// consumers via RunDurableConsumer/SubscribeKVChanges). The check is on DIRECT
// imports only: substrate itself legitimately depends on nats.go transitively,
// so a transitive (`-deps`) check would false-positive.
func TestModuleBoundary_NoRawNATS(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}",
		"github.com/operatinggraph/lattice/internal/loom").Output()
	if err != nil {
		t.Fatalf("go list imports: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		dep := strings.TrimSpace(line)
		if strings.HasPrefix(dep, "github.com/nats-io/") {
			t.Errorf("internal/loom must not directly import %q (AC #8: no raw NATS handle — "+
				"use a substrate primitive)", dep)
		}
	}
}
