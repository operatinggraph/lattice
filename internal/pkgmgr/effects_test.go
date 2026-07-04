package pkgmgr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateEffects_Valid(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName:     "leaseapp",
		PermittedCommands: []string{"CreateLeaseApplication", "SignLease"},
		Effects: map[string][]json.RawMessage{
			"SignLease": {json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
		},
	}}}
	if err := def.validateEffects(); err != nil {
		t.Fatalf("expected valid effects to pass, got: %v", err)
	}
}

func TestValidateEffects_NoEffectsIsValid(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName:     "leaseapp",
		PermittedCommands: []string{"CreateLeaseApplication"},
	}}}
	if err := def.validateEffects(); err != nil {
		t.Fatalf("expected a DDL with no Effects to pass, got: %v", err)
	}
}

func TestValidateEffects_MultipleGuardsPerOp(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName:     "leaseapp",
		PermittedCommands: []string{"DecideLeaseApplication"},
		Effects: map[string][]json.RawMessage{
			"DecideLeaseApplication": {
				json.RawMessage(`{"present":"subject.decision.data.value"}`),
				json.RawMessage(`{"present":"subject.decision.data.decidedAt"}`),
			},
		},
	}}}
	if err := def.validateEffects(); err != nil {
		t.Fatalf("expected multiple valid guards on one op to pass, got: %v", err)
	}
}

func TestValidateEffects_OperationTypeNotPermitted(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName:     "leaseapp",
		PermittedCommands: []string{"CreateLeaseApplication"},
		Effects: map[string][]json.RawMessage{
			"SignLease": {json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
		},
	}}}
	err := def.validateEffects()
	if err == nil {
		t.Fatal("expected error for an Effects key outside PermittedCommands, got nil")
	}
	if !strings.Contains(err.Error(), "SignLease") || !strings.Contains(err.Error(), "PermittedCommands") {
		t.Errorf("error should name the offending operationType and PermittedCommands; got %q", err)
	}
}

func TestValidateEffects_MalformedGuardRejectsWholesale(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName:     "leaseapp",
		PermittedCommands: []string{"SignLease"},
		Effects: map[string][]json.RawMessage{
			"SignLease": {json.RawMessage(`{"exists":"subject.signature.data.signedAt"}`)},
		},
	}}}
	err := def.validateEffects()
	if err == nil {
		t.Fatal("expected error for a malformed guard, got nil")
	}
	if !strings.Contains(err.Error(), "SignLease") {
		t.Errorf("error should name the offending operationType; got %q", err)
	}
}

func TestValidateEffects_StarlarkReservedRejected(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName:     "leaseapp",
		PermittedCommands: []string{"SignLease"},
		Effects: map[string][]json.RawMessage{
			"SignLease": {json.RawMessage(`{"starlark":"def guard(subject): return True"}`)},
		},
	}}}
	if err := def.validateEffects(); err == nil {
		t.Fatal("expected the reserved Starlark escape hatch to reject an effect, got nil")
	}
}

func TestValidateEffects_BadPathShapeRejected(t *testing.T) {
	def := Definition{DDLs: []DDLSpec{{
		CanonicalName:     "leaseapp",
		PermittedCommands: []string{"SignLease"},
		Effects: map[string][]json.RawMessage{
			"SignLease": {json.RawMessage(`{"present":"signature.signedAt"}`)},
		},
	}}}
	if err := def.validateEffects(); err == nil {
		t.Fatal("expected a path missing the subject. prefix to reject, got nil")
	}
}
