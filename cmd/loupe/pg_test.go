package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

func TestPGResolveTarget(t *testing.T) {
	cases := []struct {
		name     string
		target   map[string]any
		wantTbl  string
		wantKeys []string
		wantErr  bool
	}{
		{
			name:     "table and single key",
			target:   map[string]any{"table": "read_leases", "key": "key"},
			wantTbl:  "read_leases",
			wantKeys: []string{"key"},
		},
		{
			name:     "composite key list",
			target:   map[string]any{"table": "read_slots", "key": []any{"clinic_id", "slot_id"}},
			wantTbl:  "read_slots",
			wantKeys: []string{"clinic_id", "slot_id"},
		},
		{
			name:     "key defaults when absent",
			target:   map[string]any{"table": "read_leases"},
			wantTbl:  "read_leases",
			wantKeys: []string{"key"},
		},
		{
			name:     "grant table fills platform defaults",
			target:   map[string]any{"grantTable": true},
			wantTbl:  adapter.GrantTable,
			wantKeys: adapter.GrantKeyColumns,
		},
		{
			name:     "grant table keeps explicit table",
			target:   map[string]any{"grantTable": true, "table": "actor_read_grants"},
			wantTbl:  "actor_read_grants",
			wantKeys: adapter.GrantKeyColumns,
		},
		{
			name:    "no table is an error",
			target:  map[string]any{"key": "key"},
			wantErr: true,
		},
		{
			name:    "nil target is an error",
			target:  nil,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table, keys, err := pgResolveTarget(lensFullSpec{TargetType: "postgres", Target: tc.target})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got table=%q keys=%v", table, keys)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if table != tc.wantTbl {
				t.Errorf("table = %q, want %q", table, tc.wantTbl)
			}
			if !reflect.DeepEqual(keys, tc.wantKeys) {
				t.Errorf("keys = %v, want %v", keys, tc.wantKeys)
			}
		})
	}
}

func TestPGIdent(t *testing.T) {
	if got, err := pgIdent("table", "read_leases"); err != nil || got != `"read_leases"` {
		t.Errorf("pgIdent = %q, %v; want quoted ident, nil", got, err)
	}
	if _, err := pgIdent("table", ""); err == nil {
		t.Error("empty identifier must error")
	}
	// The break-out-of-quoting vector: an embedded double quote must be
	// rejected, never quoted through.
	if _, err := pgIdent("table", `x" ; DROP TABLE y; --`); err == nil {
		t.Error("identifier with embedded double-quote must error")
	}
}

func TestEscapeILIKE(t *testing.T) {
	got := escapeILIKE(`50%_done\end`)
	want := `50\%\_done\\end`
	if got != want {
		t.Errorf("escapeILIKE = %q, want %q", got, want)
	}
}

func TestBuildLensRowsSQL(t *testing.T) {
	t.Run("no filter", func(t *testing.T) {
		countSQL, rowsSQL, args, err := buildLensRowsSQL("read_leases", []string{"key"}, "", 200)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if countSQL != `SELECT count(*) FROM "read_leases"` {
			t.Errorf("countSQL = %q", countSQL)
		}
		if rowsSQL != `SELECT * FROM "read_leases" ORDER BY "key" LIMIT 200` {
			t.Errorf("rowsSQL = %q", rowsSQL)
		}
		if len(args) != 0 {
			t.Errorf("args = %v, want none", args)
		}
	})
	t.Run("filter binds the escaped pattern", func(t *testing.T) {
		countSQL, rowsSQL, args, err := buildLensRowsSQL("actor_read_grants", []string{"actor_id", "anchor_id"}, "50%", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantWhere := ` WHERE concat_ws('.', "actor_id", "anchor_id") ILIKE $1`
		if !strings.Contains(countSQL, wantWhere) || !strings.Contains(rowsSQL, wantWhere) {
			t.Errorf("WHERE missing: count=%q rows=%q", countSQL, rowsSQL)
		}
		if !strings.HasSuffix(rowsSQL, ` ORDER BY "actor_id", "anchor_id" LIMIT 10`) {
			t.Errorf("rowsSQL tail = %q", rowsSQL)
		}
		if len(args) != 1 || args[0] != `%50\%%` {
			t.Errorf("args = %v, want the escaped pattern", args)
		}
	})
	t.Run("hostile identifiers error", func(t *testing.T) {
		if _, _, _, err := buildLensRowsSQL(`t"x`, []string{"key"}, "", 1); err == nil {
			t.Error("hostile table must error")
		}
		if _, _, _, err := buildLensRowsSQL("t", []string{`k"x`}, "", 1); err == nil {
			t.Error("hostile key column must error")
		}
	})
	t.Run("empty key columns error", func(t *testing.T) {
		if _, _, _, err := buildLensRowsSQL("t", nil, "", 1); err == nil {
			t.Error("empty keyCols must error, not emit malformed SQL")
		}
	})
}

func TestPGRowDoc(t *testing.T) {
	cols := []string{"actor_id", "anchor_id", "grant_source", "projection_seq", "is_deleted"}
	vals := []any{"A1", "R9", "cap-read.leases", int64(7), false}
	key, doc := pgRowDoc(cols, vals, []string{"actor_id", "anchor_id", "grant_source"})
	if key != "A1.R9.cap-read.leases" {
		t.Errorf("key = %q", key)
	}
	if doc["projection_seq"] != int64(7) || doc["is_deleted"] != false {
		t.Errorf("doc = %v", doc)
	}

	// A non-string key value renders via fmt.Sprint; a NULL key column is
	// skipped rather than joined as "<nil>".
	key, _ = pgRowDoc([]string{"a", "b"}, []any{int64(3), nil}, []string{"a", "b"})
	if key != "3" {
		t.Errorf("key = %q, want %q", key, "3")
	}
}

// TestJSONSafeValue pins the non-finite-float normalization: Postgres float
// columns accept NaN/±Infinity, and a raw math.NaN in the doc map would abort
// json.Marshal AFTER the 200 header is written (a silently blank panel).
func TestJSONSafeValue(t *testing.T) {
	doc := map[string]any{
		"nan":  jsonSafeValue(math.NaN()),
		"inf":  jsonSafeValue(math.Inf(1)),
		"ninf": jsonSafeValue(float32(math.Inf(-1))),
		"ok":   jsonSafeValue(1.5),
		"str":  jsonSafeValue("x"),
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal after normalization: %v", err)
	}
	if doc["ok"] != 1.5 || doc["str"] != "x" {
		t.Errorf("finite/other values must pass through unchanged: %v", doc)
	}
	if !strings.Contains(string(out), "NaN") {
		t.Errorf("NaN must render as its text form: %s", out)
	}
}

// TestLensRowsPG_InvalidDSN pins that a set-but-unparseable LOUPE_PG_DSN
// surfaces as an error, never as the friendly pg-pending state (which would
// tell the operator to set a variable that is already set).
func TestLensRowsPG_InvalidDSN(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), pgDSNInvalid: true}
	spec := lensFullSpec{Found: true, TargetType: "postgres", Target: map[string]any{"table": "t1"}}
	rec := httptest.NewRecorder()
	s.lensRowsPG(context.Background(), rec, "L1", spec, 10, "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "pgPending") {
		t.Error("invalid DSN must not answer the pg-pending shape")
	}
}

// TestMutatingEndpoints_CrossOriginBlocked pins the console-wide same-origin
// gate: every state-changing endpoint rejects a request whose Origin names
// another site, before any other processing (a nil conn would otherwise
// answer 502 — a 403 proves the gate ran first).
func TestMutatingEndpoints_CrossOriginBlocked(t *testing.T) {
	mux := testServer()
	cases := []struct{ name, method, path string }{
		{"op submit", http.MethodPost, "/api/op"},
		{"control mutate", http.MethodPost, "/api/control/loom/main/pause"},
		{"object upload", http.MethodPost, "/api/objects"},
		{"object detach", http.MethodDelete, "/api/objects/OID1?targetKey=vtx.identity.I1&linkName=p"},
		{"package install", http.MethodPost, "/api/packages/install"},
		{"package upgrade", http.MethodPost, "/api/packages/upgrade"},
		{"package uninstall", http.MethodPost, "/api/packages/uninstall"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.Header.Set("Origin", "http://evil.example")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
			}
		})
	}

	// The loopback same-origin form passes the gate (and proceeds to the
	// conn guard).
	t.Run("loopback same-origin passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Origin", "http://127.0.0.1:7777")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("loopback same-origin request blocked: %q", rec.Body.String())
		}
	})

	// The DNS-rebinding shape: Origin and Host AGREE (the attacker controls
	// both — their DNS name resolves to 127.0.0.1) but neither is a host the
	// console is served from. Matching Origin against Host alone would pass
	// this; the loopback/bind-host requirement must block it.
	t.Run("dns-rebound matching origin+host blocked", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		req.Host = "evil.example:7777"
		req.Header.Set("Origin", "http://evil.example:7777")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
		}
	})

	// Origin "null" (sandboxed iframe, some redirect chains) fails closed.
	t.Run("null origin blocked", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Origin", "null")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
		}
	})

	// The warned-about non-loopback opt-in: an explicitly-configured bind
	// host is accepted alongside loopback.
	t.Run("configured bind host passes", func(t *testing.T) {
		s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), bindHost: "10.0.0.5"}
		req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		req.Host = "10.0.0.5:7777"
		req.Header.Set("Origin", "http://10.0.0.5:7777")
		rec := httptest.NewRecorder()
		if s.crossOriginBlocked(rec, req) {
			t.Fatalf("configured bind host blocked: %q", rec.Body.String())
		}
	})
}
