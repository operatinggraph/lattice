package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/operatinggraph/lattice/internal/processor"
)

// --- ValidReadModelName ------------------------------------------------

func TestValidReadModelName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"landlordApplications", true},
		{"a-b_9", true},
		{"", false},
		{"operations", false}, // must not shadow the write-path keystone
		{"has/slash", false},
		{"has space", false},
		{"has.dot", false},
	}
	for _, tc := range cases {
		if got := ValidReadModelName(tc.name); got != tc.want {
			t.Errorf("ValidReadModelName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- fakePgPool: no real Postgres, proves the auth/wiring path -----------

type fakeTx struct {
	execs    []string
	execArgs [][]any
	rows     []map[string]any
	commit   bool
}

func (f *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) { panic("not used") }
func (f *fakeTx) Commit(ctx context.Context) error          { f.commit = true; return nil }
func (f *fakeTx) Rollback(ctx context.Context) error        { return nil }
func (f *fakeTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	panic("not used")
}
func (f *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { panic("not used") }
func (f *fakeTx) LargeObjects() pgx.LargeObjects                               { panic("not used") }
func (f *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	panic("not used")
}
func (f *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	f.execArgs = append(f.execArgs, args)
	return pgconn.CommandTag{}, nil
}
func (f *fakeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return &fakeRows{rows: f.rows}, nil
}
func (f *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { panic("not used") }
func (f *fakeTx) Conn() *pgx.Conn                                               { panic("not used") }

// fakeRows implements pgx.Rows over an in-memory row set (map[string]any per
// row) — enough for queryReadModel's FieldDescriptions()+Values() scan.
type fakeRows struct {
	rows []map[string]any
	i    int
	keys []string
}

func (r *fakeRows) Close()                        {}
func (r *fakeRows) Err() error                    { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	if len(r.rows) == 0 {
		return nil
	}
	fds := make([]pgconn.FieldDescription, 0, len(r.rows[0]))
	for k := range r.rows[0] {
		fds = append(fds, pgconn.FieldDescription{Name: k})
	}
	r.keys = make([]string, len(fds))
	for i, fd := range fds {
		r.keys[i] = fd.Name
	}
	return fds
}
func (r *fakeRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}
func (r *fakeRows) Scan(dest ...any) error { panic("not used") }
func (r *fakeRows) Values() ([]any, error) {
	if r.keys == nil {
		r.FieldDescriptions()
	}
	row := r.rows[r.i-1]
	out := make([]any, len(r.keys))
	for i, k := range r.keys {
		out[i] = row[k]
	}
	return out, nil
}
func (r *fakeRows) RawValues() [][]byte { panic("not used") }
func (r *fakeRows) Conn() *pgx.Conn     { panic("not used") }

type fakePgPool struct {
	tx *fakeTx
}

func (p *fakePgPool) Begin(ctx context.Context) (pgx.Tx, error) { return p.tx, nil }

// --- handleReadModel: auth wiring + generic scan (no real Postgres) ------

func TestHandleReadModel_Unauthenticated_401(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: testReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(&fakePgPool{tx: &fakeTx{}}, map[string]ReadModel{"widgets": {Query: "SELECT 1"}})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/widgets", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestHandleReadModel_PoolNil_502(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "NTLJmwGKScNmwzUpeB5J")
	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: testReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(nil, map[string]ReadModel{"widgets": {Query: "SELECT 1"}})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/widgets", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestHandleReadModel_UnregisteredName_404(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: testReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(&fakePgPool{tx: &fakeTx{}}, map[string]ReadModel{"widgets": {Query: "SELECT 1"}})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/notregistered", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no route mounted for an unregistered name)", w.Code)
	}
}

func TestHandleReadModel_GETScopesActorAndScansRowsGenerically(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "oC8heCGu6HFWpp37xcpS")

	tx := &fakeTx{rows: []map[string]any{
		{"unit_key": "vtx.unit.1", "unit_rent": 1500.0},
		{"unit_key": "vtx.unit.2", "unit_rent": 2000.0},
	}}
	pool := &fakePgPool{tx: tx}
	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: testReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(pool, map[string]ReadModel{"landlordApplications": {Query: "SELECT unit_key, unit_rent FROM x"}})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/landlordApplications", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !tx.commit {
		t.Fatal("transaction was never committed")
	}
	foundSetConfig := false
	for _, sql := range tx.execs {
		if sql == "SELECT set_config('lattice.actor_id', $1, true)" {
			foundSetConfig = true
		}
	}
	if !foundSetConfig {
		t.Fatalf("txn never set the lattice.actor_id session var, execs=%v", tx.execs)
	}

	var resp struct {
		Rows  []map[string]any `json:"rows"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Count != 2 || len(resp.Rows) != 2 {
		t.Fatalf("rows = %+v, want 2", resp.Rows)
	}
}

func TestHandleReadModel_POSTNotAllowed(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: testReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(&fakePgPool{tx: &fakeTx{}}, map[string]ReadModel{"widgets": {Query: "SELECT 1"}})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/widgets", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// TestRegisterRoutes_InvalidNameSkipped proves an invalid read-model name
// (e.g. one colliding with "operations") never gets mounted — the write
// path keeps its own handler, never shadowed by a misconfigured read-model.
func TestRegisterRoutes_InvalidNameSkipped(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "WK3wrHzkvsDmsQTU5WLx")
	fake := func(_ context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
		return &processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted}, nil
	}
	s := newTestServer(t, authn, fake)
	s.ConfigureReadModels(&fakePgPool{tx: &fakeTx{}}, map[string]ReadModel{"operations": {Query: "SELECT 1"}})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	// A GET to /v1/operations must still hit the write-path handler (405 for
	// GET, since the write path only accepts POST) — never the read-model
	// handler's own 405-for-non-GET, and never a 404 as if unmounted.
	r := httptest.NewRequest(http.MethodGet, "/v1/operations", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (the write-path handler, not shadowed/removed)", w.Code)
	}
}

// TestHandleReadModel_CredentialBinding_ScopesToClaimedIdentity proves a
// bound credential actor's read scopes lattice.actor_id to the claimed
// business identity, not the raw credential subject
// (gateway-claim-flow-identity-provisioning-design.md §11.0/§11.5 R1) — the
// Gateway's own read-model routes get the same resolution as the write path.
func TestHandleReadModel_CredentialBinding_ScopesToClaimedIdentity(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "5K2w3V5zERE4oNUuu71w")

	tx := &fakeTx{}
	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: testReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(&fakePgPool{tx: tx}, map[string]ReadModel{"widgets": {Query: "SELECT 1"}})
	s.ConfigureCredentialBindings(fakeCredentialResolver{identityKey: "vtx.identity.CLAIMEDBUSINESS0000", bound: true})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/widgets", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(tx.execArgs) == 0 || len(tx.execArgs[0]) == 0 {
		t.Fatal("set_config never received an actor argument")
	}
	if got := tx.execArgs[0][0]; got != "CLAIMEDBUSINESS0000" {
		t.Fatalf("lattice.actor_id = %v, want the resolved business identity's bare id", got)
	}
}

// TestHandleReadModel_CredentialBinding_Unbound_UsesRawSubject proves an
// unbound (or unconfigured) actor reads exactly as before — the raw JWT
// subject, unchanged.
func TestHandleReadModel_CredentialBinding_Unbound_UsesRawSubject(t *testing.T) {
	priv := newTestKey(t)
	authn := testAuthenticator(t, priv, "k1")
	token := signToken(t, priv, "k1", "5K2w3V5zERE4oNUuu71w")

	tx := &fakeTx{}
	s := &Server{authn: authn, logger: nopLogger{}, reqTimeout: testReqTimeout, metrics: &Metrics{}}
	s.ConfigureReadModels(&fakePgPool{tx: tx}, map[string]ReadModel{"widgets": {Query: "SELECT 1"}})
	s.ConfigureCredentialBindings(fakeCredentialResolver{bound: false})

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, "/v1/widgets", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := tx.execArgs[0][0]; got != "5K2w3V5zERE4oNUuu71w" {
		t.Fatalf("lattice.actor_id = %v, want the raw subject (unbound)", got)
	}
}

const testReqTimeout = defaultReqTimeout
