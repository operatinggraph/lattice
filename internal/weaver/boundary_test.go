package weaver_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/weaver"
)

// TestStart_EmptyActorKeyFails asserts Weaver fails LOUD at Start when ActorKey
// is empty, rather than silently publishing remediation ops under actor:"" (which
// the Processor rejects off-stream with no signal). Instance/Lane are set to valid
// tokens so Start reaches the ActorKey guard (it runs after the token checks).
func TestStart_EmptyActorKeyFails(t *testing.T) {
	t.Parallel()
	eng := weaver.NewEngine(nil, weaver.Config{ActorKey: "", Instance: "test", Lane: "system"})
	err := eng.Start(context.Background())
	if err == nil {
		t.Fatal("Start must error on empty ActorKey, got nil")
	}
	if !strings.Contains(err.Error(), "ActorKey") {
		t.Errorf("Start error should name ActorKey, got %q", err.Error())
	}
}

// TestModuleBoundary_OnlySubstrate enforces AC #9: internal/weaver never
// imports internal/processor, internal/loom, or internal/refractor anywhere in
// its dependency tree. It also never imports internal/bridge or the retired
// internal/weaver/nudge package — external idempotent I/O is carried entirely by
// Loom's externalTask + the bridge, with no dependency back into Weaver. The
// check uses `go list -deps` (transitive).
func TestModuleBoundary_OnlySubstrate(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "github.com/operatinggraph/lattice/internal/weaver").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"github.com/operatinggraph/lattice/internal/processor",
		"github.com/operatinggraph/lattice/internal/loom",
		"github.com/operatinggraph/lattice/internal/refractor",
		"github.com/operatinggraph/lattice/internal/bridge",
		"github.com/operatinggraph/lattice/internal/weaver/nudge",
	}
	for _, line := range strings.Split(string(out), "\n") {
		dep := strings.TrimSpace(line)
		for _, f := range forbidden {
			if dep == f || strings.HasPrefix(dep, f+"/") {
				t.Errorf("internal/weaver must not import %q (AC #9 module boundary)", dep)
			}
		}
	}
}

// TestModuleBoundary_NoRawNATS enforces AC #9: internal/weaver carries no raw
// nats.io/jetstream handle of its own — every NATS interaction goes through a
// substrate primitive (the Actuator publishes via substrate.Publish; consumers
// via the ConsumerSupervisor / SubscribeKVChanges). The check is on DIRECT
// imports only: substrate itself legitimately depends on nats.go transitively,
// so a transitive (`-deps`) check would false-positive.
func TestModuleBoundary_NoRawNATS(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-f", "{{ join .Imports \"\\n\" }}",
		"github.com/operatinggraph/lattice/internal/weaver").Output()
	if err != nil {
		t.Fatalf("go list imports: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		dep := strings.TrimSpace(line)
		if strings.HasPrefix(dep, "github.com/nats-io/") {
			t.Errorf("internal/weaver must not directly import %q (AC #9: no raw NATS handle — "+
				"use a substrate primitive)", dep)
		}
	}
}
