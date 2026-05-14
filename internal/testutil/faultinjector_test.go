package testutil

import (
	"errors"
	"testing"
)

func TestFailAfterN_TripsExactlyOnce(t *testing.T) {
	trip := FailAfterN(2, FaultStep8Commit)
	if err := trip(); err != nil {
		t.Fatalf("call 1 should pass, got %v", err)
	}
	err := trip()
	if err == nil {
		t.Fatalf("call 2 should fail")
	}
	var fe *FaultError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *FaultError, got %T", err)
	}
	if fe.Label != FaultStep8Commit || fe.Call != 2 {
		t.Fatalf("FaultError = %+v, want label=%s call=2", fe, FaultStep8Commit)
	}
	if !errors.Is(err, ErrFaultInjected) {
		t.Fatalf("FaultError should wrap ErrFaultInjected")
	}
	// Subsequent calls pass.
	if err := trip(); err != nil {
		t.Fatalf("call 3 should pass, got %v", err)
	}
}

func TestFailAfterN_NeverTripsForLargeN(t *testing.T) {
	trip := FailAfterN(1000, FaultStep1Consume)
	for i := 0; i < 10; i++ {
		if err := trip(); err != nil {
			t.Fatalf("call %d should pass, got %v", i+1, err)
		}
	}
}
