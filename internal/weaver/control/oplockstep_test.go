package control_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/weaver"
	"github.com/operatinggraph/lattice/internal/weaver/control"
)

// opRecorder allows every request and records the op token the service handed
// to Authorize — the exact string production's CapabilityKVChecker resolves
// against its op table.
type opRecorder struct {
	mu  sync.Mutex
	ops []string
}

func (r *opRecorder) Authorize(_ context.Context, _, op, _ string) error {
	r.mu.Lock()
	r.ops = append(r.ops, op)
	r.mu.Unlock()
	return nil
}

func (r *opRecorder) seen() map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]struct{}, len(r.ops))
	for _, op := range r.ops {
		out[op] = struct{}{}
	}
	return out
}

// TestControl_EveryServedOpIsAuthorizable holds the weaver control service and
// controlauth.WeaverOps in lockstep, in the direction only the service can
// answer.
//
// CapabilityKVChecker resolves an op token against WeaverOps and returns
// ErrUnknownControlOp for anything absent, so an op the service serves but the
// table does not name is authorized for NOBODY, in production, forever. The
// package-side drift guards cannot see that: they run TABLE → grants, and a
// table missing an entry is simply a smaller table every grant still matches.
// SERVICE → table has to be checked from the service's own registration.
//
// So this drives a real request at every endpoint the service registers and
// asserts the op token that reaches Authorize resolves in the table. Two
// further directions come free and are checked here too: an op served without
// ever reaching Authorize is an unauthorized surface, and a table entry no
// endpoint serves is a dead grant.
func TestControl_EveryServedOpIsAuthorizable(t *testing.T) {
	nc := startTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rec := &opRecorder{}
	svc := control.NewService(newFakeEngine(weaver.TargetSummary{TargetID: "t1"}), rec, nil)
	if err := svc.StartNATSListener(ctx, nc); err != nil {
		t.Fatalf("start listener: %v", err)
	}

	// One request per registered endpoint. The request bodies do not matter:
	// every handler authorizes BEFORE it reads its arguments, which is what
	// makes the op token the first thing the control plane must be able to
	// resolve.
	var served []string
	for _, op := range control.RegisteredExactOps() {
		served = append(served, op)
		if _, err := nc.Request(control.ExactSubject(op), nil, 2*time.Second); err != nil {
			t.Fatalf("request %q on its exact subject: %v", op, err)
		}
	}
	for _, op := range control.RegisteredTargetOps() {
		served = append(served, op)
		if _, err := nc.Request(control.TargetSubject("t1", op), nil, 2*time.Second); err != nil {
			t.Fatalf("request %q on its per-target subject: %v", op, err)
		}
	}

	seen := rec.seen()
	for _, op := range served {
		if _, ok := seen[op]; !ok {
			t.Errorf("the service registers an endpoint for op %q, but a request to it never reached Authorize — "+
				"an op served without an authorization check", op)
		}
	}
	for op := range seen {
		if _, ok := controlauth.WeaverOps[op]; !ok {
			t.Errorf("the weaver control service serves op %q, but controlauth.WeaverOps does not name it — "+
				"CapabilityKVChecker would return ErrUnknownControlOp for every actor, so the op is a permanent deny", op)
		}
	}
	for op := range controlauth.WeaverOps {
		if _, ok := seen[op]; !ok {
			t.Errorf("controlauth.WeaverOps names op %q, but the weaver control service registers no endpoint for it — "+
				"the grants derived from that entry are dead", op)
		}
	}
}
