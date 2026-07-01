package lens

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/substrate"
)

// EmitReadPathDDL enumerates the installed protected/grant Postgres lenses in
// Core KV and returns the ordered DDL statements that provision their read-path
// tables out-of-band (Contract #6 §6.14, verify-and-pause provisioning).
//
// Refractor no longer issues this DDL at activation — it verifies the posture
// and pauses fail-closed (VerifyProtectedTable / VerifyGrantTable). This emitter
// is the operator-side counterpart: it derives the exact Build*TableDDL each
// installed lens expects (the same generators the verifier checks against), so
// the operator (or `make provision-readpath`) can apply them and the lenses
// resume. It is read-only against Core KV and connects to no Postgres.
//
// Ordering is load-bearing: the shared actor_read_grants table is emitted FIRST
// (every protected table's RLS policy references it), then each protected
// business table, in deterministic (table-name) order. The grant table is
// emitted whenever any grant OR protected lens is installed — a protected
// policy depends on it even when no explicit grant lens is present. Tombstoned
// (isDeleted) lenses and non-postgres / public lenses are skipped.
func EmitReadPathDDL(ctx context.Context, conn *substrate.Conn, coreKVBucket string) ([]string, error) {
	keys, err := conn.KVListKeys(ctx, coreKVBucket)
	if err != nil {
		return nil, fmt.Errorf("emit read-path DDL: list core KV keys: %w", err)
	}

	type protectedTable struct {
		table string
		ddl   []string
	}
	var protectedTables []protectedTable
	seenTables := make(map[string]struct{})
	needGrantTable := false

	for _, key := range keys {
		// Lens specs live at the meta-vertex aspect key vtx.meta.<id>.spec.
		_, _, _, localName, ok := substrate.ParseAspectKey(key)
		if !ok || localName != "spec" {
			continue
		}
		entry, err := conn.KVGet(ctx, coreKVBucket, key)
		if err != nil {
			continue
		}
		// KVGet returns a tombstoned spec normally (Core KV holds logically-
		// deleted entries by design — substrate.Conn.KVGet's doc comment); this
		// is exactly the "raw Core KV consumer" that must inspect isDeleted
		// itself. TombstoneMetaVertex tombstones a lens's .spec aspect alongside
		// its root (meta_ddl.go), so checking the spec envelope alone is enough.
		var deletedProbe struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &deletedProbe); err == nil && deletedProbe.IsDeleted {
			continue
		}
		specBody, err := unwrapSpecBody(entry.Value)
		if err != nil {
			continue
		}
		var spec LensSpec
		if err := json.Unmarshal(specBody, &spec); err != nil {
			continue
		}
		if spec.TargetType != "postgres" {
			continue
		}
		var cfg TargetPostgresConfig
		if err := json.Unmarshal(spec.TargetConfig, &cfg); err != nil {
			return nil, fmt.Errorf("emit read-path DDL: lens %q targetConfig: %w", spec.ID, err)
		}
		if !cfg.Protected && !cfg.GrantTable {
			continue
		}
		needGrantTable = true
		if cfg.GrantTable {
			// The grant table itself is emitted once, below.
			continue
		}
		// A protected business table — derive the same DDL the verifier checks.
		table := cfg.Table
		if table == "" {
			return nil, fmt.Errorf("emit read-path DDL: protected lens %q has empty table", spec.ID)
		}
		if _, dup := seenTables[table]; dup {
			continue
		}
		dm, err := adapter.ParseDeleteMode(cfg.DeleteMode)
		if err != nil {
			return nil, fmt.Errorf("emit read-path DDL: lens %q: targetConfig.deleteMode: %w", spec.ID, err)
		}
		if err := validateProtectedDeleteMode(spec.ID, cfg.Protected, dm); err != nil {
			return nil, fmt.Errorf("emit read-path DDL: %w", err)
		}
		cols, _, err := translatePostgresColumns(spec.ID, cfg)
		if err != nil {
			return nil, fmt.Errorf("emit read-path DDL: lens %q: %w", spec.ID, err)
		}
		ddl, err := adapter.BuildProtectedTableDDL(table, []string(cfg.Key), cols)
		if err != nil {
			return nil, fmt.Errorf("emit read-path DDL: lens %q: %w", spec.ID, err)
		}
		seenTables[table] = struct{}{}
		protectedTables = append(protectedTables, protectedTable{table: table, ddl: ddl})
	}

	if !needGrantTable {
		return nil, nil
	}

	sort.Slice(protectedTables, func(i, j int) bool {
		return protectedTables[i].table < protectedTables[j].table
	})

	out := make([]string, 0, len(protectedTables)*5+1)
	out = append(out, adapter.BuildGrantTableDDL()...)
	for _, pt := range protectedTables {
		out = append(out, pt.ddl...)
	}
	return out, nil
}

// ReadPathDDLScript renders the EmitReadPathDDL statements as one semicolon-
// terminated SQL script suitable for piping to psql. Each statement is
// terminated with ";\n"; an empty result yields an empty string.
func ReadPathDDLScript(stmts []string) string {
	if len(stmts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range stmts {
		b.WriteString(s)
		b.WriteString(";\n")
	}
	return b.String()
}
