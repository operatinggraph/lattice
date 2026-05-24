package query

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// TestQueryCap_HappyPath verifies that a capability document can be read
// from Capability KV given an actor key in vtx.identity.<NanoID> form.
func TestQueryCap_HappyPath(t *testing.T) {
	ctx, conn := setupQueryEnv(t)

	actorID := "testQueryCapActor00001"
	actorKey := "vtx.identity." + actorID
	capKey := "cap.identity." + actorID

	now := time.Now().UTC()
	doc := &processor.CapabilityDoc{
		Key:                    capKey,
		Actor:                  actorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{actorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants:        []processor.EphemeralGrant{},
	}
	data, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, bootstrap.CapabilityKVBucket, capKey, data); err != nil {
		t.Fatalf("KVPut cap doc: %v", err)
	}

	// Verify deriveCapKey converts vtx.identity.* correctly.
	derived := deriveCapKey(actorKey)
	if derived != capKey {
		t.Errorf("deriveCapKey(%q) = %q, want %q", actorKey, derived, capKey)
	}

	// Read it back.
	entry, err := conn.KVGet(ctx, bootstrap.CapabilityKVBucket, derived)
	if err != nil {
		t.Fatalf("KVGet cap: %v", err)
	}

	var got processor.CapabilityDoc
	if err := json.Unmarshal(entry.Value, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Actor != actorKey {
		t.Errorf("Actor = %q, want %q", got.Actor, actorKey)
	}
}

// TestQueryPostgres_DML_Rejected verifies that DML statements are rejected
// before any connection is made to Postgres.
func TestQueryPostgres_DML_Rejected(t *testing.T) {
	dmlStatements := []string{
		"INSERT INTO foo VALUES (1)",
		"UPDATE foo SET bar = 1",
		"DELETE FROM foo",
		"DROP TABLE foo",
		"CREATE TABLE foo (id int)",
		"TRUNCATE foo",
		"ALTER TABLE foo ADD COLUMN bar int",
	}
	for _, stmt := range dmlStatements {
		stmt := stmt // capture
		truncated := stmt
		if len(truncated) > 20 {
			truncated = truncated[:20]
		}
		t.Run(truncated, func(t *testing.T) {
			if err := rejectDML(stmt); err == nil {
				t.Errorf("expected DML rejection for %q, got nil", stmt)
			}
		})
	}
}

// TestQueryPostgres_DML_CommentBypass verifies that DML hidden behind SQL
// comments and CTEs is also rejected by rejectDML.
func TestQueryPostgres_DML_CommentBypass(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"line comment before INSERT", "-- bypass\nINSERT INTO foo VALUES (1)"},
		{"block comment before INSERT", "/* bypass */ INSERT INTO foo VALUES (1)"},
		{"CTE with INSERT", "WITH cte AS (SELECT 1) INSERT INTO foo SELECT * FROM cte"},
		{"DO block", "DO $$ BEGIN INSERT INTO foo VALUES (1); END $$"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			if err := rejectDML(c.sql); err == nil {
				t.Errorf("expected DML rejection for %q, got nil", c.sql)
			}
		})
	}
}

// TestDeriveCapKey verifies all three input forms produce the correct key.
func TestDeriveCapKey(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"vtx.identity.myNanoID00000000001", "cap.identity.myNanoID00000000001"},
		{"cap.identity.myNanoID00000000001", "cap.identity.myNanoID00000000001"},
		{"myNanoID00000000001", "cap.identity.myNanoID00000000001"},
	}
	for _, c := range cases {
		got := deriveCapKey(c.input)
		if got != c.want {
			t.Errorf("deriveCapKey(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func setupQueryEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "query-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}
