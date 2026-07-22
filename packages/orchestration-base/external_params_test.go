package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

const epReqID = "BBresolveHJKMNPQRSTU"
const epSubject = "vtx.identity.BBsubjectHJKMNPQRS"

// epWrapperScript embeds the shared resolver helper and exposes its output as a
// single test.resolved event whose data is the resolved params map, so a test
// can assert the resolution result (or a loud fail()).
const epWrapperScript = orchestrationbase.ResolveSubjectParamsHelper + `
def execute(state, op):
    p = op.payload
    resolved = resolve_subject_params(p.params, p.subjectKey)
    return {"mutations": [], "events": [{"class": "test.resolved", "data": resolved}]}
`

// epHydrated is the subject root + demographics aspect the kv.Read calls resolve
// against (served from the OCC snapshot cache, no reader needed).
func epHydrated() map[string]processor.VertexDoc {
	return map[string]processor.VertexDoc{
		epSubject: {
			Key:   epSubject,
			Class: "identity",
			Data:  map[string]interface{}{"fullName": "Root Name"},
		},
		epSubject + ".demographics": {
			Key:       epSubject + ".demographics",
			Class:     "demographics",
			VertexKey: epSubject,
			LocalName: "demographics",
			Data:      map[string]interface{}{"fullName": "Dana Lopez", "dob": "1991-04-02"},
		},
	}
}

func runResolve(t *testing.T, params interface{}, hydrated map[string]processor.VertexDoc) (map[string]interface{}, error) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"params":     params,
		"subjectKey": epSubject,
	})
	sc := processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     epReqID,
			Lane:          processor.LaneDefault,
			OperationType: "ResolveTest",
			Actor:         "vtx.identity.BBcallerHJKMNPQRSTU",
			SubmittedAt:   "2026-06-04T00:00:00Z",
			Payload:       payload,
		},
		Hydrated:     hydrated,
		ScriptSource: epWrapperScript,
		ScriptClass:  "resolveTest",
	}
	res, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), sc)
	if err != nil {
		return nil, err
	}
	if len(res.Events) != 1 || res.Events[0].Class != "test.resolved" {
		t.Fatalf("expected one test.resolved event, got %+v", res.Events)
	}
	return res.Events[0].Data, nil
}

// TestResolveSubjectParams_LiteralPassThrough: every non-template value (string,
// number, bool) passes through verbatim — only subject.* string values resolve.
func TestResolveSubjectParams_LiteralPassThrough(t *testing.T) {
	got, err := runResolve(t, map[string]interface{}{
		"family":  "backgroundCheck",
		"retries": 3,
		"async":   true,
	}, epHydrated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["family"] != "backgroundCheck" {
		t.Fatalf("family: got %v", got["family"])
	}
	if got["retries"] != int64(3) {
		t.Fatalf("retries: got %v (%T)", got["retries"], got["retries"])
	}
	if got["async"] != true {
		t.Fatalf("async: got %v", got["async"])
	}
}

// TestResolveSubjectParams_AspectToken: subject.<aspect>.data.<field> resolves to
// the named aspect field.
func TestResolveSubjectParams_AspectToken(t *testing.T) {
	got, err := runResolve(t, map[string]interface{}{
		"fullName": "subject.demographics.data.fullName",
		"dob":      "subject.demographics.data.dob",
	}, epHydrated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["fullName"] != "Dana Lopez" {
		t.Fatalf("fullName: got %v", got["fullName"])
	}
	if got["dob"] != "1991-04-02" {
		t.Fatalf("dob: got %v", got["dob"])
	}
}

// TestResolveSubjectParams_RootToken: subject.data.<field> resolves to the
// subject root vertex's own data field.
func TestResolveSubjectParams_RootToken(t *testing.T) {
	got, err := runResolve(t, map[string]interface{}{
		"name": "subject.data.fullName",
	}, epHydrated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["name"] != "Root Name" {
		t.Fatalf("name: got %v", got["name"])
	}
}

// TestResolveSubjectParams_Mixed: literals and tokens coexist.
func TestResolveSubjectParams_Mixed(t *testing.T) {
	got, err := runResolve(t, map[string]interface{}{
		"family":   "backgroundCheck",
		"fullName": "subject.demographics.data.fullName",
	}, epHydrated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["family"] != "backgroundCheck" || got["fullName"] != "Dana Lopez" {
		t.Fatalf("mixed: got %+v", got)
	}
}

// TestResolveSubjectParams_NilParams: None params resolves to an empty map.
func TestResolveSubjectParams_NilParams(t *testing.T) {
	got, err := runResolve(t, nil, epHydrated())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %+v", got)
	}
}

// TestResolveSubjectParams_MissingField_LoudFail: a token whose field is absent
// (or JSON null) is a loud data error — never a blank value to a vendor.
func TestResolveSubjectParams_MissingField_LoudFail(t *testing.T) {
	_, err := runResolve(t, map[string]interface{}{
		"x": "subject.demographics.data.notAField",
	}, epHydrated())
	if err == nil {
		t.Fatal("expected MissingSubjectData error, got nil")
	}
	if !strings.Contains(err.Error(), "MissingSubjectData") {
		t.Fatalf("error %q does not contain MissingSubjectData", err.Error())
	}
}

// TestResolveSubjectParams_TombstonedAspect_LoudFail: a token whose aspect
// vertex is logically deleted is a loud data error.
func TestResolveSubjectParams_TombstonedAspect_LoudFail(t *testing.T) {
	h := epHydrated()
	tomb := h[epSubject+".demographics"]
	tomb.IsDeleted = true
	h[epSubject+".demographics"] = tomb
	_, err := runResolve(t, map[string]interface{}{
		"fullName": "subject.demographics.data.fullName",
	}, h)
	if err == nil {
		t.Fatal("expected MissingSubjectData (tombstoned) error, got nil")
	}
	if !strings.Contains(err.Error(), "MissingSubjectData") {
		t.Fatalf("error %q does not contain MissingSubjectData", err.Error())
	}
}

// TestResolveSubjectParams_SensitiveRefPassThrough_FieldAppended: a
// contextHint.egressReads-hydrated sensitive aspect carries a $sensitiveRef
// marker instead of plaintext (design sensitive-param-egress §3.2/§3.3) — the
// resolver recognizes it BEFORE the field lookup and returns a
// {"$sensitiveRef": {...marker, "field": <name>}} dict for the bridge's
// post-decrypt extraction. The plaintext absent-field check never fires: the
// field is legitimately not there (the aspect never decrypted).
func TestResolveSubjectParams_SensitiveRefPassThrough_FieldAppended(t *testing.T) {
	h := epHydrated()
	ssnKey := epSubject + ".ssn"
	h[ssnKey] = processor.VertexDoc{
		Key: ssnKey, Class: "ssn", VertexKey: epSubject, LocalName: "ssn",
		Data: map[string]interface{}{
			"$sensitiveRef": map[string]interface{}{
				"ref":        ssnKey,
				"ciphertext": map[string]interface{}{"ct": "Y2lwaGVy", "nonce": "bm9uY2U=", "keyId": "k1"},
			},
		},
	}
	got, err := runResolve(t, map[string]interface{}{
		"ssn": "subject.ssn.data.value",
	}, h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sref, ok := got["ssn"].(map[string]interface{})["$sensitiveRef"].(map[string]interface{})
	if !ok {
		t.Fatalf("ssn: got %+v, want a $sensitiveRef dict", got["ssn"])
	}
	if sref["ref"] != ssnKey {
		t.Fatalf("$sensitiveRef.ref = %v, want %q", sref["ref"], ssnKey)
	}
	if sref["field"] != "value" {
		t.Fatalf("$sensitiveRef.field = %v, want %q (the requested plaintext field name)", sref["field"], "value")
	}
	ct, ok := sref["ciphertext"].(map[string]interface{})
	if !ok || ct["keyId"] != "k1" {
		t.Fatalf("$sensitiveRef.ciphertext = %+v, want the hydrated ciphertext verbatim", sref["ciphertext"])
	}
}

// TestResolveSubjectParams_MalformedTemplate_LoudFail: a subject.* value that is
// not a §10.5 path is rejected (defense-in-depth — Loom's inferExternalTaskReads
// rejects it first at submit, but the resolver also validates).
func TestResolveSubjectParams_MalformedTemplate_LoudFail(t *testing.T) {
	cases := []string{
		"subject.demographics.fullName", // no .data.
		"subject.a.b.data.c",            // too many segments
		"subject.",                      // empty
		"subject.data.",                 // empty field
	}
	for _, tok := range cases {
		t.Run(tok, func(t *testing.T) {
			_, err := runResolve(t, map[string]interface{}{"x": tok}, epHydrated())
			if err == nil {
				t.Fatalf("expected InvalidParamTemplate error for %q, got nil", tok)
			}
			if !strings.Contains(err.Error(), "InvalidParamTemplate") {
				t.Fatalf("error %q does not contain InvalidParamTemplate", err.Error())
			}
		})
	}
}
