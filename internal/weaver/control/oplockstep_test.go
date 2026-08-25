package control_test

import (
	"context"
	"strings"
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
// The served set is read from the LIVE registration (ServedEndpoints, off
// micro's published endpoint list), not from the vars the registration loops
// range over. The distinction is the whole strength of the guard: an endpoint
// added by a direct AddEndpoint call outside those loops is just as routable,
// just as authorized, and just as capable of being a permanent deny — and a
// var-derived guard cannot see it. Reading what NATS actually publishes leaves
// no registration path outside the check.
//
// Three directions fall out, and each is its own failure: an op served without
// ever reaching Authorize is an unauthorized surface; an op that reaches
// Authorize but is absent from the table is a permanent deny; a table entry no
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

	endpoints, err := svc.ServedEndpoints()
	if err != nil {
		t.Fatalf("read the published endpoints: %v", err)
	}
	if len(endpoints) == 0 {
		t.Fatal("the service published no endpoints; the guard would pass over an empty set")
	}
	// One request per published endpoint. The bodies do not matter: every
	// handler authorizes BEFORE it reads its arguments, which is what makes the
	// op token the first thing the control plane must be able to resolve.
	var served []string
	for _, ep := range endpoints {
		served = append(served, ep.Op)
		subject := strings.Replace(ep.Subject, ".*.", ".t1.", 1)
		if _, err := nc.Request(subject, nil, 2*time.Second); err != nil {
			t.Fatalf("request %q on %q: %v", ep.Op, subject, err)
		}
	}

	// The declared sets must still describe the published one — a registration
	// loop whose var no longer matches what it registers is its own drift.
	declared := map[string]struct{}{}
	for _, op := range append(control.RegisteredExactOps(), control.RegisteredTargetOps()...) {
		declared[op] = struct{}{}
	}
	for _, op := range served {
		if _, ok := declared[op]; !ok {
			t.Errorf("the service publishes an endpoint for op %q that neither registration var names", op)
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
