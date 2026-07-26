package processor

import (
	"strconv"
	"strings"
	"testing"
)

const (
	testNanoID1 = "Hj4kPmRtw9nbCxz5vQ2y"
	testNanoID2 = "St6mP3qBn4rT8wYxK7Vc"
)

func TestParseEnvelope_HappyPath(t *testing.T) {
	raw := []byte(`{
        "requestId": "` + testNanoID1 + `",
        "lane": "default",
        "operationType": "CreateIdentity",
        "actor": "vtx.identity.` + testNanoID2 + `",
        "submittedAt": "2026-05-13T10:00:00Z",
        "payload": {"name": "Andrew"}
    }`)
	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.RequestID != testNanoID1 || env.Lane != LaneDefault {
		t.Fatalf("envelope fields wrong: %+v", env)
	}
}

func TestParseEnvelope_RejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"missing requestId":     `{"lane":"default","operationType":"X","actor":"a","submittedAt":"t","payload":{}}`,
		"missing lane":          `{"requestId":"` + testNanoID1 + `","operationType":"X","actor":"a","submittedAt":"t","payload":{}}`,
		"missing operationType": `{"requestId":"` + testNanoID1 + `","lane":"default","actor":"a","submittedAt":"t","payload":{}}`,
		"missing actor":         `{"requestId":"` + testNanoID1 + `","lane":"default","operationType":"X","submittedAt":"t","payload":{}}`,
		"missing submittedAt":   `{"requestId":"` + testNanoID1 + `","lane":"default","operationType":"X","actor":"a","payload":{}}`,
		"missing payload":       `{"requestId":"` + testNanoID1 + `","lane":"default","operationType":"X","actor":"a","submittedAt":"t"}`,
		"bad lane":              `{"requestId":"` + testNanoID1 + `","lane":"banana","operationType":"X","actor":"a","submittedAt":"t","payload":{}}`,
		"bad requestId":         `{"requestId":"too-short","lane":"default","operationType":"X","actor":"a","submittedAt":"t","payload":{}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseEnvelope([]byte(raw)); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
}

func TestParseEnvelope_ToleratesUnknownFields(t *testing.T) {
	// ParseEnvelope is lenient about unknown fields for forward-compatibility
	// with contract-additive envelope fields. Unknown fields are silently
	// ignored on the runtime hot path; strictness lives in contract tests only.
	raw := `{
        "requestId":"` + testNanoID1 + `","lane":"default","operationType":"X",
        "actor":"a","submittedAt":"t","payload":{},
        "bogusField": 42
    }`
	_, err := ParseEnvelope([]byte(raw))
	if err != nil {
		t.Fatalf("expected unknown field to be tolerated, got error: %v", err)
	}
}

func TestParseEnvelope_EmptyInput(t *testing.T) {
	if _, err := ParseEnvelope(nil); err == nil {
		t.Fatalf("expected error on empty input")
	}
}

// TestParseEnvelope_ContextHintEnumerations pins the Contract #2 §2.5
// `contextHint.enumerations` metadata shape (class (e) — declared, never
// hydrated): a well-formed declaration parses; a missing hub/relation or a
// direction outside {out,in} is EnvelopeMalformed at step 1.
func TestParseEnvelope_ContextHintEnumerations(t *testing.T) {
	base := func(enums string) []byte {
		return []byte(`{
			"requestId": "` + testNanoID1 + `",
			"lane": "default",
			"operationType": "ClaimTask",
			"actor": "vtx.identity.` + testNanoID2 + `",
			"submittedAt": "2026-07-06T10:00:00Z",
			"payload": {"taskKey": "vtx.task.` + testNanoID2 + `"},
			"contextHint": {
				"reads": ["vtx.task.` + testNanoID2 + `"],
				"enumerations": ` + enums + `
			}
		}`)
	}

	env, err := ParseEnvelope(base(`[{"hub":"vtx.task.` + testNanoID2 + `","relation":"queuedFor","direction":"out"}]`))
	if err != nil {
		t.Fatalf("valid enumerations declaration must parse: %v", err)
	}
	if len(env.ContextHint.Enumerations) != 1 ||
		env.ContextHint.Enumerations[0].Relation != "queuedFor" ||
		env.ContextHint.Enumerations[0].Direction != "out" {
		t.Fatalf("enumerations not carried: %+v", env.ContextHint.Enumerations)
	}

	for name, enums := range map[string]string{
		"missing hub":      `[{"relation":"queuedFor","direction":"out"}]`,
		"missing relation": `[{"hub":"vtx.task.` + testNanoID2 + `","direction":"out"}]`,
		"bad direction":    `[{"hub":"vtx.task.` + testNanoID2 + `","relation":"queuedFor","direction":"sideways"}]`,
	} {
		if _, err := ParseEnvelope(base(enums)); err == nil {
			t.Fatalf("%s: expected a parse rejection, got nil", name)
		}
	}
}

// TestParseEnvelope_OptionalReadsCarried — optionalReads is an ordinary
// additive contextHint field: parsed and carried, with no per-key validation
// beyond JSON shape (per-key semantics live in the Hydrator; the only
// envelope-level check is the summed count ceiling).
func TestParseEnvelope_OptionalReadsCarried(t *testing.T) {
	raw := []byte(`{
		"requestId": "` + testNanoID1 + `",
		"lane": "default",
		"operationType": "CreateTask",
		"actor": "vtx.identity.` + testNanoID2 + `",
		"submittedAt": "2026-07-06T10:00:00Z",
		"payload": {},
		"contextHint": {"optionalReads": ["vtx.task.` + testNanoID2 + `"]}
	}`)
	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if len(env.ContextHint.OptionalReads) != 1 {
		t.Fatalf("optionalReads not carried: %+v", env.ContextHint)
	}
}

// declaredReadsEnvelope builds an envelope declaring the given number of
// distinct keys in each read class. The three prefixes keep egressReads
// disjoint from the other two, so a rejection can only come from the count.
func declaredReadsEnvelope(reads, optional, egress int) []byte {
	key := func(prefix string, i int) string {
		return `"vtx.` + prefix + `.` + testNanoID2 + `.a` + strconv.Itoa(i) + `"`
	}
	list := func(prefix string, n int) string {
		out := make([]string, 0, n)
		for i := range n {
			out = append(out, key(prefix, i))
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	return []byte(`{
		"requestId": "` + testNanoID1 + `",
		"lane": "default",
		"operationType": "MergeIdentity",
		"actor": "vtx.identity.` + testNanoID2 + `",
		"submittedAt": "2026-07-25T10:00:00Z",
		"payload": {},
		"contextHint": {
			"reads": ` + list("r", reads) + `,
			"optionalReads": ` + list("o", optional) + `,
			"egressReads": ` + list("e", egress) + `
		}
	}`)
}

// TestParseEnvelope_DeclaredReadCeiling pins the Contract #2 §2.5 bound on the
// declared read set. Step 4 pays one sequential Core KV GET per declared key
// and an absent key is recorded rather than faulting, so without this ceiling
// the number of round trips an envelope buys is whatever a client typed —
// unpriced, since step 3 authorizes without inspecting contextHint. The bound
// is on the SUM across the three classes because the cost is the sum: a
// per-class limit would be trivially cleared by spreading the keys.
func TestParseEnvelope_DeclaredReadCeiling(t *testing.T) {
	t.Run("at the ceiling is accepted", func(t *testing.T) {
		third := MaxDeclaredReads / 3
		env, err := ParseEnvelope(declaredReadsEnvelope(MaxDeclaredReads-2*third, third, third))
		if err != nil {
			t.Fatalf("a set exactly at the ceiling must parse: %v", err)
		}
		got := len(env.ContextHint.Reads) + len(env.ContextHint.OptionalReads) + len(env.ContextHint.EgressReads)
		if got != MaxDeclaredReads {
			t.Fatalf("declared set not carried whole: got %d, want %d", got, MaxDeclaredReads)
		}
	})

	t.Run("one over the ceiling is rejected", func(t *testing.T) {
		_, err := ParseEnvelope(declaredReadsEnvelope(MaxDeclaredReads+1, 0, 0))
		if err == nil {
			t.Fatal("expected rejection one key over the ceiling")
		}
		if !strings.Contains(err.Error(), "contextHint declares") {
			t.Fatalf("rejection does not name the ceiling: %v", err)
		}
	})

	// Each class must be pinned INTO the sum separately: an implementation
	// that summed only two of the three passes any test whose over-limit
	// weight sits in the other two.
	for _, tc := range []struct {
		name                            string
		reads, optionalReads, egressRds int
	}{
		{"reads + optionalReads", MaxDeclaredReads/2 + 1, MaxDeclaredReads/2 + 1, 0},
		{"reads + egressReads", MaxDeclaredReads/2 + 1, 0, MaxDeclaredReads/2 + 1},
		{"optionalReads + egressReads", 0, MaxDeclaredReads/2 + 1, MaxDeclaredReads/2 + 1},
	} {
		t.Run("the ceiling is the sum, not per class: "+tc.name, func(t *testing.T) {
			// No single class is over, so a per-class bound would admit this.
			_, err := ParseEnvelope(declaredReadsEnvelope(tc.reads, tc.optionalReads, tc.egressRds))
			if err == nil {
				t.Fatalf("expected rejection: %d+%d+%d exceeds the %d ceiling with no class over it",
					tc.reads, tc.optionalReads, tc.egressRds, MaxDeclaredReads)
			}
			// Assert the REASON: the three prefixes keep the classes disjoint,
			// so the pre-existing ambiguous-disposition check must not be what
			// rejected this.
			if !strings.Contains(err.Error(), "contextHint declares") {
				t.Fatalf("rejected for the wrong reason: %v", err)
			}
		})
	}

	t.Run("a realistic set is untouched", func(t *testing.T) {
		if _, err := ParseEnvelope(declaredReadsEnvelope(4, 2, 1)); err != nil {
			t.Fatalf("an ordinary declared set must parse: %v", err)
		}
	})
}
