package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// The Fire 3 headline proof: the generic GET /v1/<name> handler enforces RLS
// on a REAL protected Postgres read model — same fixture idiom as
// cmd/loftspace-app's landlord_applications_rls_test.go (BuildProtectedTableDDL
// / BuildGrantTableDDL, a dedicated dropped-at-end schema, a non-superuser
// reader role) — but driven entirely through the Gateway's config-only
// registry (a ReadModel{Query}), never a compiled struct.
//
// Gated: skipped unless POSTGRES_TEST_DSN is set and -short is not active
// (CI has no Postgres).

const (
	gwRLSTestSchema = "gateway_rls_test"
	gwRLSTestRole   = "gateway_rls_test_reader"
	gwSubOwnerA     = "OWNERAAAAAAAAAAAAAAA"
	gwSubOwnerB     = "OWNERBBBBBBBBBBBBBBB"
)

func skipIfNoPostgresRLS(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}
	return dsn
}

func gwPoolInSchema(t *testing.T, dsn, role string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		if _, err := c.Exec(ctx, "SET search_path TO "+gwRLSTestSchema+", public"); err != nil {
			return err
		}
		if role != "" {
			if _, err := c.Exec(ctx, "SET ROLE "+role); err != nil {
				return err
			}
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	return pool
}

func TestReadModel_RLS_Enforcement(t *testing.T) {
	dsn := skipIfNoPostgresRLS(t)
	ctx := context.Background()

	owner := gwPoolInSchema(t, dsn, "")
	defer owner.Close()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	exec("DROP SCHEMA IF EXISTS " + gwRLSTestSchema + " CASCADE")
	exec("CREATE SCHEMA " + gwRLSTestSchema)
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, "DROP SCHEMA IF EXISTS "+gwRLSTestSchema+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP OWNED BY "+gwRLSTestRole+" CASCADE")
		_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+gwRLSTestRole)
	})

	for _, stmt := range adapter.BuildGrantTableDDL() {
		exec(stmt)
	}
	body := []adapter.ColumnDef{
		{Name: "widget_name", Type: "text"},
		{Name: "widget_price", Type: "double precision"},
	}
	ddl, err := adapter.BuildProtectedTableDDL("read_widgets", []string{"widget_id"}, body)
	if err != nil {
		t.Fatalf("build protected DDL: %v", err)
	}
	for _, stmt := range ddl {
		exec(stmt)
	}

	_, _ = owner.Exec(ctx, "DROP OWNED BY "+gwRLSTestRole+" CASCADE")
	_, _ = owner.Exec(ctx, "DROP ROLE IF EXISTS "+gwRLSTestRole)
	exec("CREATE ROLE " + gwRLSTestRole + " NOSUPERUSER NOLOGIN")
	exec("GRANT USAGE ON SCHEMA " + gwRLSTestSchema + " TO " + gwRLSTestRole)
	exec("GRANT SELECT ON " + gwRLSTestSchema + ".read_widgets TO " + gwRLSTestRole)
	exec("GRANT SELECT ON " + gwRLSTestSchema + ".actor_read_grants TO " + gwRLSTestRole)

	exec(`INSERT INTO read_widgets (widget_id, widget_name, widget_price, authz_anchors, projection_seq)
	      VALUES ($1, $2, $3, $4, 1)`, "w-a", "Widget A", 9.99, []string{gwSubOwnerA})
	exec(`INSERT INTO read_widgets (widget_id, widget_name, widget_price, authz_anchors, projection_seq)
	      VALUES ($1, $2, $3, $4, 1)`, "w-b", "Widget B", 19.99, []string{gwSubOwnerB})

	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, gwSubOwnerA)
	exec(`INSERT INTO actor_read_grants (actor_id, anchor_id, grant_source, projection_seq, is_deleted)
	      VALUES ($1, $1, 'cap-read', 1, false)`, gwSubOwnerB)

	reader := gwPoolInSchema(t, dsn, gwRLSTestRole)
	defer reader.Close()

	t.Run("reader role is not a superuser", func(t *testing.T) {
		var isSuper string
		if err := reader.QueryRow(ctx, "SELECT current_setting('is_superuser')").Scan(&isSuper); err != nil {
			t.Fatalf("is_superuser: %v", err)
		}
		if isSuper != "off" {
			t.Fatalf("reader must be non-superuser (else RLS is bypassed), got is_superuser=%s", isSuper)
		}
	})

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	v, err := auth.NewVerifier(auth.Config{Keys: map[string]crypto.PublicKey{"k1": &priv.PublicKey}})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	authn := auth.NewAuthenticator(v, nil)

	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: defaultReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(reader, map[string]ReadModel{
		"widgets": {Query: "SELECT widget_id, widget_name, widget_price FROM read_widgets ORDER BY widget_id"},
	})
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	get := func(t *testing.T, sub string) (int, []map[string]any) {
		t.Helper()
		token := signToken(t, priv, "k1", sub)
		r := httptest.NewRequest(http.MethodGet, "/v1/widgets", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var resp struct {
			Rows []map[string]any `json:"rows"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Rows
	}

	t.Run("owner A sees only widget A", func(t *testing.T) {
		code, rows := get(t, gwSubOwnerA)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0]["widget_id"] != "w-a" {
			t.Fatalf("rows = %+v, want exactly widget w-a", rows)
		}
	})

	t.Run("owner B sees only widget B", func(t *testing.T) {
		code, rows := get(t, gwSubOwnerB)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 1 || rows[0]["widget_id"] != "w-b" {
			t.Fatalf("rows = %+v, want exactly widget w-b", rows)
		}
	})

	t.Run("an actor with no grant sees nothing (no 403/404 oracle)", func(t *testing.T) {
		code, rows := get(t, "NOGRANTACTOR00000000")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("rows = %+v, want none", rows)
		}
	})

	t.Run("revoked grant hides the row", func(t *testing.T) {
		exec("UPDATE actor_read_grants SET is_deleted = true WHERE actor_id = $1", gwSubOwnerA)
		defer exec("UPDATE actor_read_grants SET is_deleted = false WHERE actor_id = $1", gwSubOwnerA)
		code, rows := get(t, gwSubOwnerA)
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
		if len(rows) != 0 {
			t.Fatalf("rows = %+v, want none once the grant is revoked", rows)
		}
	})

	t.Run("pooling safety: the actor var does not leak across requests", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			_, rowsA := get(t, gwSubOwnerA)
			if len(rowsA) != 1 {
				t.Fatalf("iter %d: owner A leaked/lost rows, got %d", i, len(rowsA))
			}
			_, rowsB := get(t, gwSubOwnerB)
			if len(rowsB) != 1 {
				t.Fatalf("iter %d: owner B leaked/lost rows, got %d", i, len(rowsB))
			}
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/widgets", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})
}
