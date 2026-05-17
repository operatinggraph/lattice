// Unit tests for starlark_builtins.go — specifically the cryptoModule().
//
// Deliverable #3 from Story 4.2: crypto.sha256 known-digest test,
// wrong-arity rejection, non-string argument rejection.
package processor

import (
	"strings"
	"testing"

	starlarklib "go.starlark.net/starlark"
)

// --- crypto.sha256 ---

// TestCryptoSha256_KnownDigest verifies that crypto.sha256("") equals the
// known SHA-256 hash of the empty string.
func TestCryptoSha256_KnownDigest(t *testing.T) {
	// sha256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	const wantEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	mod := cryptoModule()
	fn, err := mod.Attr("sha256")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256 attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}
	result, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("")}, nil)
	if err != nil {
		t.Fatalf("crypto.sha256(''): %v", err)
	}
	got, ok := result.(starlarklib.String)
	if !ok {
		t.Fatalf("crypto.sha256('') returned %T, want String", result)
	}
	if string(got) != wantEmpty {
		t.Fatalf("crypto.sha256('') = %q, want %q", string(got), wantEmpty)
	}

	// "hello" → 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	const wantHello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	result2, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("hello")}, nil)
	if err != nil {
		t.Fatalf("crypto.sha256('hello'): %v", err)
	}
	got2, _ := result2.(starlarklib.String)
	if string(got2) != wantHello {
		t.Fatalf("crypto.sha256('hello') = %q, want %q", string(got2), wantHello)
	}

	// Output is always 64 hex chars.
	if len(string(got2)) != 64 {
		t.Fatalf("crypto.sha256 output length = %d, want 64", len(string(got2)))
	}
}

// TestCryptoSha256_WrongArity checks that calling sha256 with 0 or 2
// arguments returns an error.
func TestCryptoSha256_WrongArity(t *testing.T) {
	mod := cryptoModule()
	fn, err := mod.Attr("sha256")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256 attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}

	// zero args
	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{}, nil)
	if err == nil {
		t.Fatal("crypto.sha256() with 0 args: expected error, got nil")
	}

	// two args
	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("a"), starlarklib.String("b")}, nil)
	if err == nil {
		t.Fatal("crypto.sha256(a, b) with 2 args: expected error, got nil")
	}
}

// TestCryptoSha256_NonString verifies that passing a non-string argument
// (e.g. an integer) returns an error with a descriptive message.
func TestCryptoSha256_NonString(t *testing.T) {
	mod := cryptoModule()
	fn, err := mod.Attr("sha256")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256 attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}

	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.MakeInt(42)}, nil)
	if err == nil {
		t.Fatal("crypto.sha256(42): expected error for non-string, got nil")
	}
	if !strings.Contains(err.Error(), "int") {
		t.Fatalf("error message should mention type 'int', got: %v", err)
	}
}

// --- crypto.sha256NanoID ---

// TestCryptoSha256NanoID_Deterministic checks that sha256NanoID returns a
// 20-char NanoID-alphabet string and is deterministic (same input → same output).
func TestCryptoSha256NanoID_Deterministic(t *testing.T) {
	mod := cryptoModule()
	fn, err := mod.Attr("sha256NanoID")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256NanoID attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}

	call := func(s string) string {
		t.Helper()
		result, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(s)}, nil)
		if err != nil {
			t.Fatalf("crypto.sha256NanoID(%q): %v", s, err)
		}
		got, ok := result.(starlarklib.String)
		if !ok {
			t.Fatalf("crypto.sha256NanoID(%q) returned %T, want String", s, result)
		}
		return string(got)
	}

	id1 := call("email:test@example.com")
	id2 := call("email:test@example.com")
	if id1 != id2 {
		t.Fatalf("sha256NanoID not deterministic: %q != %q", id1, id2)
	}
	if len(id1) != 20 {
		t.Fatalf("sha256NanoID length = %d, want 20", len(id1))
	}

	// Different inputs must produce different outputs.
	idPhone := call("phone:+15551234567")
	if idPhone == id1 {
		t.Fatalf("sha256NanoID collision: email and phone prefixes produced same ID")
	}

	// All chars must be in the NanoID alphabet (no 0, I, l, O).
	for _, c := range id1 {
		if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789", c) {
			t.Fatalf("sha256NanoID(%q) contains invalid char %q: %q", "email:test@example.com", c, id1)
		}
	}
}
