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
