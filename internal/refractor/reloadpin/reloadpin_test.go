package reloadpin_test

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/reloadpin"
)

const baseSpec = `{
  "id": "lens-1",
  "canonicalName": "appointmentReminders",
  "targetType": "nats_kv",
  "cypherRule": "MATCH (i:identity) RETURN i.key AS actorKey",
  "projectionKind": "actorAggregate",
  "targetConfig": {"bucket": "weaver-targets", "key": ["key"]},
  "output": {
    "anchorType": "identity",
    "outputKeyPattern": "appointmentReminders.{actorSuffix}",
    "bodyColumns": ["reminders"],
    "emptyBehavior": "delete",
    "freshness": "auto"
  }
}`

func TestRefusedChange_IdenticalSpecIsNotAChange(t *testing.T) {
	if got := reloadpin.RefusedChange([]byte(baseSpec), []byte(baseSpec)); got != "" {
		t.Fatalf("an unchanged spec must not warn, got %q", got)
	}
}

// The cypher is the edit a hot reload EXISTS to carry. Warning on it would train
// operators to ignore the warning that matters.
func TestRefusedChange_CypherOnlyEditIsHotReloadable(t *testing.T) {
	edited := strings.Replace(baseSpec,
		"MATCH (i:identity) RETURN i.key AS actorKey",
		"MATCH (i:identity) WHERE i.active RETURN i.key AS actorKey", 1)
	if got := reloadpin.RefusedChange([]byte(baseSpec), []byte(edited)); got != "" {
		t.Fatalf("a MATCH-only edit is hot-reloadable and must not warn, got %q", got)
	}
}

// The commonest package-lens edit there is. Refractor carries it by
// re-activating the lens, so predicting a refusal here would send an operator to
// a remedy for a change that has already applied — and train them to ignore the
// warnings that still mean something.
func TestRefusedChange_OutputDescriptorEditIsCarriedByReactivation(t *testing.T) {
	edited := strings.Replace(baseSpec, `"bodyColumns": ["reminders"]`,
		`"bodyColumns": ["reminders", "escalations"]`, 1)
	if got := reloadpin.RefusedChange([]byte(baseSpec), []byte(edited)); got != "" {
		t.Fatalf("an Output edit re-activates the lens and must not warn, got %q", got)
	}
}

func TestRefusedChange_GuardSourceEditsAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, from, to, want string }{
		{"grantTable", `"key": ["key"]`, `"key": ["key"], "grantTable": true`, "grantTable"},
		{"protected", `"key": ["key"]`, `"key": ["key"], "protected": true`, "protected"},
		{"secureColumns", `"key": ["key"]`, `"key": ["key"], "secureColumns": [{"name":"ssn"}]`, "secureColumns"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := strings.Replace(baseSpec, tc.from, tc.to, 1)
			got := reloadpin.RefusedChange([]byte(baseSpec), []byte(edited))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("editing %s must be predicted as refused, got %q", tc.name, got)
			}
		})
	}
}

// The write-surface pins are runtime-conditional (they bind only for a lens
// whose built adapter carries the guard), so predicting them from a document
// pair would warn about edits that are legitimately applied.
func TestRefusedChange_WriteSurfaceEditIsNotPredicted(t *testing.T) {
	edited := strings.Replace(baseSpec, `"bucket": "weaver-targets"`, `"bucket": "other-targets"`, 1)
	if got := reloadpin.RefusedChange([]byte(baseSpec), []byte(edited)); got != "" {
		t.Fatalf("a target move is refused only for a guarded lens at runtime; predicting it would over-warn, got %q", got)
	}
}

func TestRefusedChange_UnparseableSpecIsNotAWarning(t *testing.T) {
	if got := reloadpin.RefusedChange([]byte(`{`), []byte(baseSpec)); got != "" {
		t.Fatalf("an unparseable spec is the installer's to reject, not this predictor's to guess at: %q", got)
	}
}

// Absence and an explicit zero must compare equal to themselves, or every
// re-serialized spec would warn.
func TestRefusedChange_AbsentVersusExplicitFalseIsNotAChange(t *testing.T) {
	explicit := strings.Replace(baseSpec, `"key": ["key"]`, `"key": ["key"], "grantTable": false`, 1)
	if got := reloadpin.RefusedChange([]byte(baseSpec), []byte(explicit)); got != "" {
		t.Fatalf("spelling out a default is not an edit, got %q", got)
	}
}
