package pkgmgr

import (
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
)

// fullCypherParser adapts ruleengine/full.Engine to CypherParser. Living in a
// _test.go file (not pkgmgr's production code) is what avoids the import
// cycle CypherParser's doc explains — full's own test binary transitively
// imports pkgmgr, but pkgmgr's *test* binary importing full (prod) has no such
// path back, so this is safe here (and would be safe in any other package's
// production code too — just not pkgmgr's).
type fullCypherParser struct{}

func (fullCypherParser) Parse(ruleBody string) error {
	_, err := full.New().Parse(ruleBody)
	return err
}

func lensContent(t *testing.T, lc LensArtifactContent) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(lc)
	if err != nil {
		t.Fatalf("marshal lens content: %v", err)
	}
	return b
}

func TestValidateCapabilityArtifact_DisabledKind(t *testing.T) {
	report, err := ValidateCapabilityArtifact("grant", json.RawMessage(`{}`), fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid report for a disabled kind, got valid")
	}
	if len(report.Errors) != 1 {
		t.Fatalf("expected exactly one error, got %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_MalformedContent(t *testing.T) {
	_, err := ValidateCapabilityArtifact("lens", json.RawMessage(`not-json`), fullCypherParser{})
	if err == nil {
		t.Fatalf("expected a caller-contract error for malformed content")
	}
}

func TestValidateCapabilityArtifact_ValidLens(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "activeProvidersBySpecialty",
		Adapter:       "nats-kv",
		Bucket:        "active-providers",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("expected a valid report, got errors: %v", report.Errors)
	}
}

func TestValidateCapabilityArtifact_UnparseableCypher(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "brokenLens",
		Adapter:       "nats-kv",
		Bucket:        "broken-lens",
		Spec:          "MATCH (p:provider RETURN p.key AS key", // missing close paren
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for unparseable cypher")
	}
}

func TestValidateCapabilityArtifact_MissingCanonicalName(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		Adapter: "nats-kv",
		Bucket:  "no-name",
		Spec:    "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a missing canonicalName")
	}
}

func TestValidateCapabilityArtifact_CoreKVAdapterRejected(t *testing.T) {
	// P5: a lens may never target Core KV directly — validateLensAdapters
	// already rejects any Adapter other than "" / "nats-kv" / "postgres", so an
	// AI-authored artifact cannot smuggle a core-kv-shaped adapter through.
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "sneakyLens",
		Adapter:       "core-kv",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a core-kv-shaped adapter")
	}
}

func TestValidateCapabilityArtifact_ReservedBucketAliasRejected(t *testing.T) {
	// The reserved short alias guard (bucketguard.go) must apply identically to
	// an AI-authored lens — reused validateAll, not a weaker copy.
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "phantomLens",
		Adapter:       "nats-kv",
		Bucket:        "capability",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for the reserved 'capability' bucket alias")
	}
}

func TestValidateCapabilityArtifact_OutOfScopeFieldRejected(t *testing.T) {
	// A raw content payload that smuggles a field this increment doesn't expose
	// (e.g. "protected") must be caught, not silently dropped by json.Unmarshal
	// and downgraded to a plain lens.
	content := json.RawMessage(`{"canonicalName":"sneakyProtected","adapter":"postgres","table":"sneaky","spec":"MATCH (p:provider) RETURN p.key AS key","protected":true}`)
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for an out-of-scope 'protected' field")
	}
}

func TestValidateCapabilityArtifact_MissingBucketRejected(t *testing.T) {
	content := lensContent(t, LensArtifactContent{
		CanonicalName: "noBucketLens",
		Adapter:       "nats-kv",
		Spec:          "MATCH (p:provider) RETURN p.key AS key",
	})
	report, err := ValidateCapabilityArtifact("lens", content, fullCypherParser{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected an invalid report for a nats-kv lens with no Bucket")
	}
}
