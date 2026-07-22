package processor

import (
	"context"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestLaneSpecs(t *testing.T) {
	handler := func(context.Context, substrate.Message) (substrate.Decision, error) {
		return substrate.Ack, nil
	}
	consumers := map[string]int{"default": 2, "urgent": 4, "system": 2, "meta": 8}
	specs := LaneSpecs("core-operations", handler, 30*time.Second, consumers, nil)

	if len(specs) != 4 {
		t.Fatalf("got %d specs, want 4 (one per lane)", len(specs))
	}
	wantWorkers := map[string]int{
		"processor-default": 2,
		"processor-urgent":  4,
		"processor-system":  2,
		"processor-meta":    1, // forced to 1 even though the map asked for 8
	}

	want := map[string]string{ // durable → ops.<lane> subject
		"processor-default": "ops.default",
		"processor-urgent":  "ops.urgent",
		"processor-system":  "ops.system",
		"processor-meta":    "ops.meta",
	}
	seen := map[string]bool{}
	for _, s := range specs {
		subj, ok := want[s.Name]
		if !ok {
			t.Fatalf("unexpected durable %q", s.Name)
		}
		seen[s.Name] = true
		if s.FilterSubject != subj {
			t.Fatalf("durable %q FilterSubject = %q, want %q", s.Name, s.FilterSubject, subj)
		}
		if len(s.FilterSubjects) != 0 {
			t.Fatalf("durable %q set FilterSubjects %v; want single FilterSubject only", s.Name, s.FilterSubjects)
		}
		if s.Stream != "core-operations" {
			t.Fatalf("durable %q Stream = %q, want core-operations", s.Name, s.Stream)
		}
		if s.AckWait != 30*time.Second {
			t.Fatalf("durable %q AckWait = %v, want 30s", s.Name, s.AckWait)
		}
		if s.DeliverPolicy != substrate.DeliverAll {
			t.Fatalf("durable %q DeliverPolicy = %v, want DeliverAll", s.Name, s.DeliverPolicy)
		}
		if s.Handler == nil {
			t.Fatalf("durable %q has nil Handler", s.Name)
		}
		// Only the meta lane is serialized (Contract #2 §3.7); all others leave
		// MaxAckPending at the JetStream default (0).
		wantMAP := 0
		if s.Name == "processor-meta" {
			wantMAP = 1
		}
		if s.MaxAckPending != wantMAP {
			t.Fatalf("durable %q MaxAckPending = %d, want %d", s.Name, s.MaxAckPending, wantMAP)
		}
		if s.Workers != wantWorkers[s.Name] {
			t.Fatalf("durable %q Workers = %d, want %d", s.Name, s.Workers, wantWorkers[s.Name])
		}
	}
	if len(seen) != 4 {
		t.Fatalf("missing lane durables; saw %v", seen)
	}
}

func TestLaneConsumers(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		got := LaneConsumers(func(string) string { return "" })
		want := map[string]int{"default": 2, "urgent": 4, "system": 2, "meta": 1}
		for lane, n := range want {
			if got[lane] != n {
				t.Errorf("lane %q = %d, want default %d", lane, got[lane], n)
			}
		}
	})

	t.Run("override honored per lane", func(t *testing.T) {
		env := map[string]string{
			"LATTICE_PROCESSOR_LANES_DEFAULT_CONSUMERS": "6",
			"LATTICE_PROCESSOR_LANES_URGENT_CONSUMERS":  "1",
		}
		got := LaneConsumers(func(k string) string { return env[k] })
		if got["default"] != 6 {
			t.Errorf("default = %d, want 6 (override)", got["default"])
		}
		if got["urgent"] != 1 {
			t.Errorf("urgent = %d, want 1 (override)", got["urgent"])
		}
		if got["system"] != 2 {
			t.Errorf("system = %d, want 2 (default, no override)", got["system"])
		}
	})

	t.Run("meta clamps to 1 regardless of override", func(t *testing.T) {
		env := map[string]string{"LATTICE_PROCESSOR_LANES_META_CONSUMERS": "16"}
		got := LaneConsumers(func(k string) string { return env[k] })
		if got["meta"] != 1 {
			t.Fatalf("meta = %d, want 1 (fail-closed serialization clamp)", got["meta"])
		}
	})

	t.Run("malformed or sub-1 override keeps default", func(t *testing.T) {
		for _, bad := range []string{"abc", "0", "-3", "  "} {
			env := map[string]string{"LATTICE_PROCESSOR_LANES_DEFAULT_CONSUMERS": bad}
			got := LaneConsumers(func(k string) string { return env[k] })
			if got["default"] != 2 {
				t.Errorf("override %q: default = %d, want 2 (default preserved)", bad, got["default"])
			}
		}
	})

	t.Run("whitespace-padded numeric override parses", func(t *testing.T) {
		env := map[string]string{"LATTICE_PROCESSOR_LANES_SYSTEM_CONSUMERS": "  5 "}
		got := LaneConsumers(func(k string) string { return env[k] })
		if got["system"] != 5 {
			t.Fatalf("padded override = %d, want 5", got["system"])
		}
	})
}

func TestLaneDurablesIsACopy(t *testing.T) {
	a := LaneDurables()
	if len(a) != 4 {
		t.Fatalf("LaneDurables len = %d, want 4", len(a))
	}
	a["default"] = "tampered"
	b := LaneDurables()
	if b["default"] != "processor-default" {
		t.Fatalf("LaneDurables returned a shared map; mutation leaked: %q", b["default"])
	}
}
