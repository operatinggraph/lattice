package loom

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestParseGuard_GrammarShapes exercises the §10.5 guard parser: valid
// declarative atoms/composites parse, malformed shapes reject with
// errMalformedGuard, and the {reads, starlark} escape hatch parses when it
// compile-checks clean (a well-formed `def guard(subject)`) and rejects
// (errMalformedGuard) when it doesn't. Starlark eval-time behavior (the
// actual bool result, absence semantics, hydration dedup, determinism) is
// TestEvalGuard_Starlark* below — this test is parse/load only.
func TestParseGuard_GrammarShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantErr error // nil = accept; else errors.Is target
	}{
		// --- valid atoms ---
		{"absent root", `{"absent":"subject.data.name"}`, nil},
		{"absent aspect", `{"absent":"subject.profile.data.name"}`, nil},
		{"present aspect", `{"present":"subject.profile.data.phone"}`, nil},
		{"equals string", `{"equals":{"path":"subject.data.status","value":"active"}}`, nil},
		{"equals number", `{"equals":{"path":"subject.data.count","value":3}}`, nil},
		{"equals bool", `{"equals":{"path":"subject.data.flag","value":false}}`, nil},
		{"equals explicit null", `{"equals":{"path":"subject.data.x","value":null}}`, nil},
		// --- valid composites ---
		{"allOf", `{"allOf":[{"absent":"subject.profile.data.name"},{"present":"subject.data.id"}]}`, nil},
		{"anyOf", `{"anyOf":[{"absent":"subject.data.a"},{"absent":"subject.data.b"}]}`, nil},
		{"not", `{"not":{"absent":"subject.data.name"}}`, nil},
		{"nested composite", `{"allOf":[{"not":{"present":"subject.data.x"}},{"anyOf":[{"absent":"subject.data.y"}]}]}`, nil},

		// --- malformed shapes ---
		{"unknown top key", `{"exists":"subject.data.name"}`, errMalformedGuard},
		{"multi-key object", `{"absent":"subject.data.a","present":"subject.data.b"}`, errMalformedGuard},
		{"zero-key object", `{}`, errMalformedGuard},
		{"bare string", `"subject.data.name"`, errMalformedGuard},
		{"empty allOf", `{"allOf":[]}`, errMalformedGuard},
		{"empty anyOf", `{"anyOf":[]}`, errMalformedGuard},
		{"allOf not array", `{"allOf":{"absent":"subject.data.x"}}`, errMalformedGuard},
		{"equals missing value", `{"equals":{"path":"subject.data.x"}}`, errMalformedGuard},
		{"equals missing path", `{"equals":{"value":"x"}}`, errMalformedGuard},
		{"equals unknown field", `{"equals":{"path":"subject.data.x","value":1,"extra":2}}`, errMalformedGuard},
		{"equals object comparand", `{"equals":{"path":"subject.data.x","value":{"nested":true}}}`, errMalformedGuard},
		{"equals array comparand", `{"equals":{"path":"subject.data.x","value":[1,2,3]}}`, errMalformedGuard},
		{"absent wrong type", `{"absent":123}`, errMalformedGuard},
		{"composite child malformed", `{"allOf":[{"exists":"subject.data.a"}]}`, errMalformedGuard},

		// --- bad path shapes ---
		{"no subject prefix", `{"absent":"profile.data.name"}`, errMalformedGuard},
		{"aspect without data", `{"absent":"subject.profile.name"}`, errMalformedGuard},
		{"too deep", `{"absent":"subject.profile.data.addr.city"}`, errMalformedGuard},
		{"root empty field", `{"absent":"subject.data."}`, errMalformedGuard},
		{"bare subject", `{"absent":"subject"}`, errMalformedGuard},

		// --- starlark escape hatch: valid ---
		{"starlark full", `{"reads":["profile"],"starlark":"def guard(subject): return True"}`, nil},
		{"starlark no reads (root-only)", `{"starlark":"def guard(s): return True"}`, nil},
		{"starlark empty reads array", `{"reads":[],"starlark":"def guard(subject): return True"}`, nil},
		{"starlark multiple reads", `{"reads":["profile","lease"],"starlark":"def guard(subject): return subject.profile != None and subject.lease != None"}`, nil},
		{"starlark uses pure modules", `{"starlark":"def guard(subject): return crypto.sha256('x') != '' and time.rfc3339_utc('2026-01-01T00:00:00Z') != '' and json.encode({'a':1}) != ''"}`, nil},

		// --- starlark escape hatch: malformed ---
		{"reads only key (no starlark)", `{"reads":["profile"]}`, errMalformedGuard},
		{"starlark empty string", `{"starlark":""}`, errMalformedGuard},
		{"starlark syntax error", `{"starlark":"def guard(subject)\n    return True"}`, errMalformedGuard},
		{"starlark sandbox violation (os)", `{"starlark":"def guard(subject): return os.getenv('X') != ''"}`, errMalformedGuard},
		{"starlark sandbox violation (load)", `{"starlark":"load('x.star','y')\ndef guard(subject): return True"}`, errMalformedGuard},
		{"starlark missing guard func", `{"starlark":"def notguard(subject): return True"}`, errMalformedGuard},
		{"starlark guard not callable", `{"starlark":"guard = 1"}`, errMalformedGuard},
		{"starlark guard wrong arity (0)", `{"starlark":"def guard(): return True"}`, errMalformedGuard},
		{"starlark guard wrong arity (2)", `{"starlark":"def guard(subject, extra): return True"}`, errMalformedGuard},
		{"starlark reads non-string entry", `{"reads":[1,2],"starlark":"def guard(subject): return True"}`, errMalformedGuard},
		{"starlark reads empty entry", `{"reads":[""],"starlark":"def guard(subject): return True"}`, errMalformedGuard},
		{"starlark reads not array", `{"reads":"profile","starlark":"def guard(subject): return True"}`, errMalformedGuard},

		// --- duplicate keys (BH-1): encoding/json silently last-wins on a
		// repeated object key; reject at load time instead. ---
		{"duplicate atom key", `{"absent":"subject.data.a","absent":"subject.data.b"}`, errMalformedGuard},
		{"duplicate key inside nested composite", `{"allOf":[{"absent":"subject.data.a","absent":"subject.data.b"}]}`, errMalformedGuard},
		{"duplicate key inside equals body", `{"equals":{"path":"subject.data.a","path":"subject.data.b","value":1}}`, errMalformedGuard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := parseGuard(json.RawMessage(tc.raw))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("parseGuard(%s) err=%v, want accept", tc.raw, err)
				}
				if g == nil {
					t.Fatalf("parseGuard(%s) returned nil guard with no error", tc.raw)
				}
				return
			}
			if err == nil {
				t.Fatalf("parseGuard(%s) accepted, want %v", tc.raw, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("parseGuard(%s) err=%v, want errors.Is %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

// TestParseGuard_StarlarkUnboundedTopLevelStatementFailsFast proves parseGuard
// never hangs on a pathological Starlark guard: Validate's Init phase runs a
// script's TOP-LEVEL statements (not just the `def guard(subject):` body), and
// parseGuard re-parses (so re-validates) a Starlark guard on EVERY
// step-transition attempt (engine.go's advanceToRunnableStep), not just once
// at pattern-install time — so an unbudgeted Validate would hang the whole
// engine transition loop on every single attempt against a script with an
// infinite top-level loop. The wall/step budget must catch this at PARSE
// time, same as it does at eval time.
func TestParseGuard_StarlarkUnboundedTopLevelStatementFailsFast(t *testing.T) {
	t.Parallel()
	raw := `{"starlark":"x = 0\nfor i in range(100000000):\n    x += i\ndef guard(subject): return True"}`
	start := time.Now()
	_, err := parseGuard(json.RawMessage(raw))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("parseGuard accepted a script with an unbounded top-level statement, want rejection")
	}
	if !errors.Is(err, errMalformedGuard) {
		t.Fatalf("parseGuard err=%v, want errMalformedGuard", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("parseGuard took %s to reject a pathological top-level statement, want it bounded by the wall/step budget", elapsed)
	}
}

// TestParseGuardPath_Shapes pins the two legal path shapes and their (aspect,
// field) decomposition.
func TestParseGuardPath_Shapes(t *testing.T) {
	t.Parallel()
	root, err := parseGuardPath("subject.data.name")
	if err != nil || root.aspect != "" || root.field != "name" {
		t.Fatalf("root path = %+v, err=%v; want {aspect:\"\", field:\"name\"}", root, err)
	}
	asp, err := parseGuardPath("subject.profile.data.phone")
	if err != nil || asp.aspect != "profile" || asp.field != "phone" {
		t.Fatalf("aspect path = %+v, err=%v; want {aspect:\"profile\", field:\"phone\"}", asp, err)
	}
}
