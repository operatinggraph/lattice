package lens

import (
	"encoding/json"
	"os"
	"time"
)

// BootstrapLensEnvVar gates the hardcoded bootstrap lens. When set to a
// non-empty value (typically "1") and no `meta.lens`-class vertices exist
// at startup, the Refractor activates BootstrapLens() so the e2e test of
// AC #10 can run without a prior Processor lens write.
//
// In normal operation a real lens is written via Processor to Core KV
// at `vtx.meta.<NanoID>` (with envelope class "meta.lens") and CDC
// delivers it through CoreKVSource; the bootstrap path is a development
// convenience only (handoff brief Decision #7).
const BootstrapLensEnvVar = "REFRACTOR_BOOTSTRAP_LENS"

// BootstrapLensNanoID is the fixed sentinel NanoID for the hardcoded
// development bootstrap lens. 20 chars from the Lattice 58-char alphabet
// (no I/l/O/0). MUST NOT collide with any operator-generated lens NanoID
// — collision probability with random 58^20 is negligible.
//
// Per data-contracts.md §1.2 line 70 the lens lives as a meta-vertex,
// keyed `vtx.meta.<NanoID>` with envelope class `meta.lens`.
const BootstrapLensNanoID = "RfxBootstrap12345678"

// BootstrapLensID is the runtime identifier used by the pipeline registry.
// It maps to BootstrapLensNanoID so log lines / control endpoints can find
// the lens by ID.
const BootstrapLensID = BootstrapLensNanoID

// BootstrapLensKey is the Contract-correct Core KV key for the bootstrap
// lens meta-vertex (3-segment vertex shape, type = `meta`).
const BootstrapLensKey = "vtx.meta." + BootstrapLensNanoID

// BootstrapEnabled returns true iff the env var gating the bootstrap
// lens is set to a non-empty value.
func BootstrapEnabled() bool {
	return os.Getenv(BootstrapLensEnvVar) != ""
}

// BootstrapLens returns a trivial single-aspect projection lens:
// MATCH all `contract` vertices and project to Postgres table
// `contract_view`. The DSN is sourced from the env var
// REFRACTOR_PG_DSN at lens-load time. See handoff brief Decision #7.
func BootstrapLens() *Rule {
	dsn := os.Getenv("REFRACTOR_PG_DSN")
	if dsn == "" {
		dsn = "postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable"
	}
	return &Rule{
		ID:    BootstrapLensID,
		Team:  "lattice",
		Match: "MATCH (c:contract) RETURN c.id AS contract_id, c.name AS name",
		Into: IntoConfig{
			Target:          "postgres",
			DSN:             dsn,
			Table:           "contract_view",
			Key:             KeyField{"contract_id"},
			QueryTimeoutRaw: "5s",
			QueryTimeout:    5 * time.Second,
		},
	}
}

// BootstrapLensSpecJSON returns the JSON `LensSpec` body the bootstrap
// lens would emit if it were written via the standard Processor path.
// Used by integration tests that exercise the CoreKVSource end-to-end.
func BootstrapLensSpecJSON() ([]byte, error) {
	spec := LensSpec{
		ID:            BootstrapLensNanoID,
		CanonicalName: "lens.bootstrap-contract-view",
		TargetType:    "postgres",
		CypherRule:    "MATCH (c:contract) RETURN c.id AS contract_id, c.name AS name",
	}
	// TargetConfig embedded inline below; allows zero-arg call sites.
	cfg := map[string]any{
		"dsn":          getEnvOrDefault("REFRACTOR_PG_DSN", "postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable"),
		"table":        "contract_view",
		"key":          []string{"contract_id"},
		"queryTimeout": "5s",
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	spec.TargetConfig = cfgJSON
	return json.Marshal(spec)
}

func getEnvOrDefault(k, dflt string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return dflt
}
