package loom

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestBuildOutbox_NeverCarriesEgressReads pins the structural guarantee:
// buildOutbox has no parameter through which an egress declaration can pass,
// so its record's EgressReads is nil no matter what reads, optionalReads, or
// enumerations the caller supplies. The only way to populate EgressReads is
// buildExternalTaskOutbox (TestBuildExternalTaskOutbox_DerivesEgressFromParams).
func TestBuildOutbox_NeverCarriesEgressReads(t *testing.T) {
	t.Parallel()

	rec, err := buildOutbox(
		"req-1", "SomeOp",
		map[string]any{"subjectKey": "vtx.identity.BBsubjectHJKMNPQRSTV"},
		"vtx.meta.BBpatternHJKMNPQRSTU", "core", "identity.system.loom",
		[]string{"vtx.identity.BBsubjectHJKMNPQRSTV"},
		[]string{"vtx.identity.BBsubjectHJKMNPQRSTV.piiKey"},
		[]Enumeration{{Hub: "vtx.identity.BBsubjectHJKMNPQRSTV", Relation: "holdsRole", Direction: "out"}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.EgressReads != nil {
		t.Fatalf("buildOutbox must never carry an egress declaration, got %v", rec.EgressReads)
	}
}

// TestBuildExternalTaskOutbox_DerivesEgressFromParams is the positive vector
// that makes TestBuildOutbox_NeverCarriesEgressReads non-vacuous: it proves
// something CAN reach a populated EgressReads, and that the something is
// exactly buildExternalTaskOutbox deriving it from the step's params via
// inferExternalTaskReads.
func TestBuildExternalTaskOutbox_DerivesEgressFromParams(t *testing.T) {
	t.Parallel()
	const subj = "vtx.identity.BBsubjectHJKMNPQRSTV"

	t.Run("subject.<aspect>.data.<field> template yields Reads + EgressReads", func(t *testing.T) {
		t.Parallel()
		params := json.RawMessage(`{"ssn":"subject.ssn.data.value","note":"literal"}`)
		rec, err := buildExternalTaskOutbox(
			"req-2", "SomeInstanceOp",
			map[string]any{"subjectKey": subj}, "vtx.meta.BBpatternHJKMNPQRSTU",
			"core", "identity.system.loom", subj, params,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantReads := []string{subj}
		if !reflect.DeepEqual(rec.Reads, wantReads) {
			t.Fatalf("Reads mismatch:\n got %v\nwant %v", rec.Reads, wantReads)
		}
		wantEgress := []string{subj + ".ssn"}
		if !reflect.DeepEqual(rec.EgressReads, wantEgress) {
			t.Fatalf("EgressReads mismatch:\n got %v\nwant %v", rec.EgressReads, wantEgress)
		}
	})

	t.Run("empty params yields Reads only, no egress", func(t *testing.T) {
		t.Parallel()
		rec, err := buildExternalTaskOutbox(
			"req-3", "SomeInstanceOp",
			map[string]any{"subjectKey": subj}, "vtx.meta.BBpatternHJKMNPQRSTU",
			"core", "identity.system.loom", subj, nil,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantReads := []string{subj}
		if !reflect.DeepEqual(rec.Reads, wantReads) {
			t.Fatalf("Reads mismatch:\n got %v\nwant %v", rec.Reads, wantReads)
		}
		if rec.EgressReads != nil {
			t.Fatalf("EgressReads must be nil for empty params, got %v", rec.EgressReads)
		}
	})

	t.Run("malformed subject template fails loud", func(t *testing.T) {
		t.Parallel()
		params := json.RawMessage(`{"bad":"subject.ssn.value"}`)
		_, err := buildExternalTaskOutbox(
			"req-4", "SomeInstanceOp",
			map[string]any{"subjectKey": subj}, "vtx.meta.BBpatternHJKMNPQRSTU",
			"core", "identity.system.loom", subj, params,
		)
		if err == nil {
			t.Fatal("expected an error for a malformed subject template, got nil")
		}
		if !strings.Contains(err.Error(), "malformed subject template") {
			t.Fatalf("error %q does not name the malformed template", err.Error())
		}
	})
}
