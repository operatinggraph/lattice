package processor

import (
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
// additive contextHint field: parsed and carried, no validation beyond JSON
// shape (per-key semantics live in the Hydrator).
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
