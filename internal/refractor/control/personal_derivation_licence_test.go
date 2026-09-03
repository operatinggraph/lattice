package control_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/health"
)

// TestControl_Health_AnswersThePersonalDerivationLicence pins the operator
// surface for the personal narrowing licence
// (personal-lens-derivation-licence-design.md §4.4).
//
// It exists because the licence's refusal is otherwise reachable ONLY through a
// log line the pipeline emits at most once per distinct reason. A lens quietly
// running on the enumerator, hours after that line scrolled past, has nothing
// that says why — and the health KV entry cannot answer it either: its
// personalSweepVerdict is one shared sweeper's plane-wide pass report, which
// says nothing about the wiring conjuncts or the per-lens registration clause. A
// lens can read `clean` on the entry and be refused.
func TestControl_Health_AnswersThePersonalDerivationLicence(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const ruleID = "rule-personal-licence"
	kv := makeKV(t, nc, js, "HEALTH-LICENCE")
	reporter := health.New(kv, ruleID)
	require.NoError(t, reporter.SetActive(ctx))

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.Register(ruleID, &mockResumer{}, reporter)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	callHealth := func(t *testing.T) control.ControlResponse {
		t.Helper()
		msg, err := nc.Request(control.ControlSubject(ruleID, "health"), nil, 2*time.Second)
		require.NoError(t, err)
		var resp control.ControlResponse
		require.NoError(t, json.Unmarshal(msg.Data, &resp))
		return resp
	}

	t.Run("a lens with no licence registered omits the field entirely", func(t *testing.T) {
		// Every non-personal lens in the corpus. Answering `licensed:false` for
		// one would read as a refusal rather than as an inapplicable question.
		resp := callHealth(t)
		require.Nil(t, resp.PersonalDerivation)
		require.NotNil(t, resp.Entry, "the entry itself must still be answered")
	})

	t.Run("a refused lens reports the conjunct by name", func(t *testing.T) {
		svc.RegisterPersonalDerivationLicence(ruleID, func() (bool, string) {
			return false, "the personal-plane healer's last pass failed for at least one actor"
		})
		resp := callHealth(t)
		require.NotNil(t, resp.PersonalDerivation)
		assert.False(t, resp.PersonalDerivation.Licensed)
		assert.Equal(t, "the personal-plane healer's last pass failed for at least one actor",
			resp.PersonalDerivation.Refusal,
			"the refusal must be the same sentence the log line carries, or an operator reading one cannot match it to the other")
	})

	t.Run("it is derived LIVE at request time, not stored", func(t *testing.T) {
		// Every input is process wiring or the healer's current verdict, so a
		// value captured at registration would be a snapshot of a question whose
		// whole point is that its answer moves.
		licensed := false
		svc.RegisterPersonalDerivationLicence(ruleID, func() (bool, string) {
			if licensed {
				return true, ""
			}
			return false, "more than one Refractor instance is live"
		})
		require.False(t, callHealth(t).PersonalDerivation.Licensed)

		licensed = true
		resp := callHealth(t)
		require.NotNil(t, resp.PersonalDerivation)
		assert.True(t, resp.PersonalDerivation.Licensed)
		assert.Empty(t, resp.PersonalDerivation.Refusal, "a granted licence names no conjunct")
	})

	t.Run("the plane-wide entry verdict and the per-lens answer are separate facts", func(t *testing.T) {
		// The reason this field exists rather than a new column on the entry:
		// the shared sweeper's pass can be clean while THIS lens is refused, and
		// an operator reading only the entry would conclude the opposite.
		require.NoError(t, reporter.SetPersonalSweepProgress(ctx, "Hj4kPmRtw9nbCxz5vQ2y", time.Now(), 0, "clean"))
		svc.RegisterPersonalDerivationLicence(ruleID, func() (bool, string) {
			return false, "the personal-plane healer has not completed a pass begun after this lens registered"
		})
		resp := callHealth(t)
		require.NotNil(t, resp.Entry)
		assert.Equal(t, "clean", resp.PersonalSweepVerdict,
			"the plane's healer really is clean — that is the point")
		assert.False(t, resp.PersonalDerivation.Licensed)
		assert.Contains(t, resp.PersonalDerivation.Refusal, "begun after this lens registered")
	})

	t.Run("a torn-down lens stops being answered for", func(t *testing.T) {
		// The accessor closes over a cancelled pipeline, so answering a
		// narrowing verdict for a lens that no longer runs is the misleading
		// direction. Unregister is the one place every control registration for
		// a lens is dropped, and a map it forgets leaks the accessor.
		svc.RegisterPersonalDerivationLicence(ruleID, func() (bool, string) {
			return false, "a stale accessor over a cancelled pipeline"
		})
		require.NotNil(t, callHealth(t).PersonalDerivation, "precondition: it is answered before the teardown")

		svc.Unregister(ruleID)

		// Re-registering only the REPORTER is what exposes the leak: without it
		// the health op fails on the missing reporter and never reaches the
		// licence, so a forgotten delete would pass unnoticed.
		svc.Register(ruleID, &mockResumer{}, reporter)
		resp := callHealth(t)
		require.NotNil(t, resp.Entry, "the lens is answerable again")
		require.Nil(t, resp.PersonalDerivation,
			"Unregister must drop the licence accessor with every other per-lens registration — a leaked one answers a narrowing verdict for a pipeline that no longer runs")
	})
}
