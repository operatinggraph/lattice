package main

import "testing"

// The lease-document GET's behavior (auth, RLS scoping, the pointer-absent
// "being generated" answer) is exercised against the real protected read model
// in applications_rls_test.go; the artifact's rendering lives with the docGen
// vendor adapter (internal/bridge). Only the local helpers are unit-tested
// here.

func TestShortKeyServer(t *testing.T) {
	if got := shortKeyServer("vtx.leaseapp.abcdefghijklmnop"); got != "leaseapp.abcdefgh" {
		t.Errorf("got %q", got)
	}
	if got := shortKeyServer("not-a-key"); got != "not-a-key" {
		t.Errorf("non-key passes through: got %q", got)
	}
}

func TestStrDeref(t *testing.T) {
	if got := strDeref(nil); got != "" {
		t.Errorf("nil derefs to empty: got %q", got)
	}
	v := "value"
	if got := strDeref(&v); got != "value" {
		t.Errorf("got %q", got)
	}
}
