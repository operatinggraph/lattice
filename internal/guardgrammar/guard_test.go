package guardgrammar

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestParse_GrammarShapes exercises the §10.5 declarative grammar parser:
// valid atoms/composites parse, malformed shapes reject with ErrMalformedGuard,
// and the reserved Starlark pair rejects with ErrStarlarkReserved (distinct).
func TestParse_GrammarShapes(t *testing.T) {
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
		{"unknown top key", `{"exists":"subject.data.name"}`, ErrMalformedGuard},
		{"multi-key object", `{"absent":"subject.data.a","present":"subject.data.b"}`, ErrMalformedGuard},
		{"zero-key object", `{}`, ErrMalformedGuard},
		{"bare string", `"subject.data.name"`, ErrMalformedGuard},
		{"empty allOf", `{"allOf":[]}`, ErrMalformedGuard},
		{"empty anyOf", `{"anyOf":[]}`, ErrMalformedGuard},
		{"allOf not array", `{"allOf":{"absent":"subject.data.x"}}`, ErrMalformedGuard},
		{"equals missing value", `{"equals":{"path":"subject.data.x"}}`, ErrMalformedGuard},
		{"equals missing path", `{"equals":{"value":"x"}}`, ErrMalformedGuard},
		{"equals unknown field", `{"equals":{"path":"subject.data.x","value":1,"extra":2}}`, ErrMalformedGuard},
		{"equals object comparand", `{"equals":{"path":"subject.data.x","value":{"nested":true}}}`, ErrMalformedGuard},
		{"equals array comparand", `{"equals":{"path":"subject.data.x","value":[1,2,3]}}`, ErrMalformedGuard},
		{"absent wrong type", `{"absent":123}`, ErrMalformedGuard},
		{"composite child malformed", `{"allOf":[{"exists":"subject.data.a"}]}`, ErrMalformedGuard},

		// --- bad path shapes ---
		{"no subject prefix", `{"absent":"profile.data.name"}`, ErrMalformedGuard},
		{"aspect without data", `{"absent":"subject.profile.name"}`, ErrMalformedGuard},
		{"too deep", `{"absent":"subject.profile.data.addr.city"}`, ErrMalformedGuard},
		{"root empty field", `{"absent":"subject.data."}`, ErrMalformedGuard},
		{"bare subject", `{"absent":"subject"}`, ErrMalformedGuard},

		// --- reserved starlark ---
		{"starlark full", `{"reads":["profile"],"starlark":"def guard(subject): return True"}`, ErrStarlarkReserved},
		{"starlark only key", `{"starlark":"def guard(s): return True"}`, ErrStarlarkReserved},
		{"reads only key", `{"reads":["profile"]}`, ErrStarlarkReserved},

		// --- duplicate keys (BH-1): encoding/json silently last-wins on a
		// repeated object key; reject at load time instead. ---
		{"duplicate atom key", `{"absent":"subject.data.a","absent":"subject.data.b"}`, ErrMalformedGuard},
		{"duplicate key inside nested composite", `{"allOf":[{"absent":"subject.data.a","absent":"subject.data.b"}]}`, ErrMalformedGuard},
		{"duplicate key inside equals body", `{"equals":{"path":"subject.data.a","path":"subject.data.b","value":1}}`, ErrMalformedGuard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := Parse(json.RawMessage(tc.raw))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Parse(%s) err=%v, want accept", tc.raw, err)
				}
				if g == nil {
					t.Fatalf("Parse(%s) returned nil guard with no error", tc.raw)
				}
				return
			}
			if err == nil {
				t.Fatalf("Parse(%s) accepted, want %v", tc.raw, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Parse(%s) err=%v, want errors.Is %v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

// TestParsePath_Shapes pins the two legal path shapes and their (aspect,
// field) decomposition.
func TestParsePath_Shapes(t *testing.T) {
	t.Parallel()
	root, err := ParsePath("subject.data.name")
	if err != nil || root.Aspect != "" || root.Field != "name" {
		t.Fatalf("root path = %+v, err=%v; want {Aspect:\"\", Field:\"name\"}", root, err)
	}
	asp, err := ParsePath("subject.profile.data.phone")
	if err != nil || asp.Aspect != "profile" || asp.Field != "phone" {
		t.Fatalf("aspect path = %+v, err=%v; want {Aspect:\"profile\", Field:\"phone\"}", asp, err)
	}
}
