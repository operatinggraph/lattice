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
// gate through the handler main.go actually serves: every state-changing
// endpoint rejects a request whose Origin names another site, before any other
// processing (a nil conn would otherwise answer 502, and an unauthenticated
// request 401 — a 403 proves the gate ran first).
func TestMutatingEndpoints_CrossOriginBlocked(t *testing.T) {
	_, gated := testGatedServer()
	cases := []struct{ name, method, path string }{
		{"op submit", http.MethodPost, "/api/op"},
		{"control mutate", http.MethodPost, "/api/control/loom/main/pause"},
		{"object upload", http.MethodPost, "/api/objects"},
		{"object detach", http.MethodDelete, "/api/objects/OID1?targetKey=vtx.identity.I1&linkName=p"},
		{"package install", http.MethodPost, "/api/packages/install"},
		{"package upgrade", http.MethodPost, "/api/packages/upgrade"},
		{"package uninstall", http.MethodPost, "/api/packages/uninstall"},
		{"vault decrypt", http.MethodPost, "/api/vault/decrypt"},
		{"capability approve", http.MethodPost, "/api/review/capability/P1/approve"},
		// The credential-exchange endpoints are exempt from the AUTH gate but
		// not from this one: a forced mint, login or logout is a state change a
		// hostile page must not be able to trigger either.
		{"dev-token mint", http.MethodPost, operatorDevTokenPath},
		{"session exchange", http.MethodPost, operatorSessionPath},
		{"logout", http.MethodPost, operatorLogoutPath},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.Header.Set("Origin", "http://evil.example")
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
			}
		})
	}

	// The loopback same-origin form passes the gate (and proceeds to the auth
	// gate, which this unconfigured server answers 401 — anything but a 403).
	t.Run("loopback same-origin passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Origin", "http://127.0.0.1:7777")
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
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
		gated.ServeHTTP(rec, req)
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
		gated.ServeHTTP(rec, req)
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

	// The bind host is normalized on both sides, so an FQDN-rooting trailing
	// dot — which a browser sends when the operator typed one — is the same
	// host, not a 403.
	t.Run("bind host trailing dot normalized", func(t *testing.T) {
		s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), bindHost: "console.internal"}
		req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		req.Host = "console.internal.:7777"
		req.Header.Set("Origin", "http://console.internal.:7777")
		rec := httptest.NewRecorder()
		if s.crossOriginBlocked(rec, req) {
			t.Fatalf("a trailing-dot bind host was blocked: %q", rec.Body.String())
		}
	})
}

// TestChokePoint_CoversEveryRegisteredRoute asserts the gate for every route
// registerRoutes mounts today, plus the static UI, without any route having to
// opt in.
//
// What makes a route added tomorrow safe is the CHOKE POINT itself, not this
// list: http.ServeMux exposes no route enumeration, so `paths` is hand-kept and
// will silently stop covering new routes. Read it as a census of the routes
// that exist, not as a guarantee about the ones that don't yet.
func TestChokePoint_CoversEveryRegisteredRoute(t *testing.T) {
	_, gated := testGatedServer()
	paths := []string{
		"/", "/login", "/api/corekv", "/api/corekv/entry", "/api/vertices",
		"/api/vertex", "/api/health", "/api/demo", "/api/systemmap",
		"/api/component/loom", "/api/lenses", "/api/events/stream", "/api/lens/L1",
		"/api/tasks", "/api/flows", "/api/history/timeline",
		"/api/gateway/revocations", "/api/edge/fleet", "/api/vault/shreds",
		"/api/vault/decrypt", "/api/review/capability", "/api/control/loom",
		"/api/packages", "/api/package", "/api/packages/install",
		"/api/packages/upgrade", "/api/packages/uninstall", "/api/ops", "/api/op",
		"/api/objects", "/api/objects/OID1",
		operatorDevTokenPath, operatorSessionPath, operatorLogoutPath,
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			// The positive vector first, so the negative below cannot pass for
			// some unrelated reason: the SAME request from the console's own
			// page must not be refused by this gate.
			ok := httptest.NewRequest(http.MethodGet, p, nil)
			ok.Host = "127.0.0.1:7777"
			ok.Header.Set("Sec-Fetch-Site", "same-origin")
			ok.Header.Set("Sec-Fetch-Mode", "cors")
			okRec := httptest.NewRecorder()
			gated.ServeHTTP(okRec, ok)
			if strings.Contains(okRec.Body.String(), "cross-origin request blocked") {
				t.Fatalf("same-origin request refused by the gate: %q", okRec.Body.String())
			}

			// A cross-origin scripted fetch — the shape Sec-Fetch-Site catches
			// and a bare Origin check on a GET would miss entirely. The body
			// assertion pins that THIS gate refused it, not the auth gate or a
			// handler's own validation.
			req := httptest.NewRequest(http.MethodGet, p, nil)
			req.Host = "127.0.0.1:7777"
			req.Header.Set("Sec-Fetch-Site", "same-site")
			req.Header.Set("Sec-Fetch-Mode", "cors")
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden ||
				!strings.Contains(rec.Body.String(), "cross-origin request blocked (Sec-Fetch-Site same-site)") {
				t.Fatalf("status = %d, want a 403 from the cross-origin gate — route is ungated (body %q)",
					rec.Code, rec.Body.String())
			}
		})
	}
}

// TestFetchMetadataGate pins the resource-isolation policy the Origin header
// alone cannot express. The cross-origin subresource GET is the case that
// matters: a bare <img src> on a sibling app's page is same-SITE (cookies are
// scoped by host, not port), carries the operator's session cookie, and sends
// no Origin at all.
func TestFetchMetadataGate(t *testing.T) {
	_, gated := testGatedServer()
	cases := []struct {
		name, site, mode, method string
		blocked                  bool
	}{
		{"same-origin fetch", "same-origin", "cors", http.MethodGet, false},
		{"no initiator (typed URL, bookmark)", "none", "navigate", http.MethodGet, false},
		{"same-origin post", "same-origin", "cors", http.MethodPost, false},
		// An ordinary link into the console from a co-hosted app.
		{"cross-site top-level navigation", "cross-site", "navigate", http.MethodGet, false},
		{"same-site top-level navigation", "same-site", "navigate", http.MethodGet, false},
		// The exposures.
		{"same-site subresource GET", "same-site", "no-cors", http.MethodGet, true},
		{"cross-site scripted GET", "cross-site", "cors", http.MethodGet, true},
		{"same-site scripted POST", "same-site", "cors", http.MethodPost, true},
		// A navigation cannot launder a state change.
		{"cross-site navigate POST", "cross-site", "navigate", http.MethodPost, true},
		// Case-insensitive per the Fetch spec's token matching.
		{"mixed-case same-origin", "Same-Origin", "cors", http.MethodPost, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/systemmap", nil)
			req.Host = "127.0.0.1:7777"
			req.Header.Set("Sec-Fetch-Site", tc.site)
			req.Header.Set("Sec-Fetch-Mode", tc.mode)
			if tc.mode == "navigate" {
				// A top-level navigation; the nested case is its own test.
				req.Header.Set("Sec-Fetch-Dest", "document")
			}
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)
			got := rec.Code == http.StatusForbidden &&
				strings.Contains(rec.Body.String(), "Sec-Fetch-Site")
			if got != tc.blocked {
				t.Fatalf("blocked = %v, want %v (status %d, body %q)",
					got, tc.blocked, rec.Code, rec.Body.String())
			}
		})
	}

	// A browser that sends no Fetch-Metadata at all (Safari before 16.4) still
	// falls through to the Origin check, and a non-browser caller with neither
	// header carries nobody's ambient cookie and passes.
	t.Run("no metadata headers falls through to Origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Origin") {
			t.Fatalf("status = %d, want a 403 naming Origin (body %q)", rec.Code, rec.Body.String())
		}
	})
}

// TestFetchMetadata_NestedNavigationRefused is the clickjacking vector the
// navigate exemption admits when it reads Sec-Fetch-Mode alone: `navigate` is
// sent for a NESTED navigation too, so an <iframe src="http://127.0.0.1:7777/">
// on a co-hosted vertical app's page is same-site, carries the operator's
// session cookie (SameSite=Strict is port-agnostic), and would render the
// authenticated console — approve, apply, uninstall, pause, decrypt and all —
// inside a hostile frame. Sec-Fetch-Dest is what separates the two.
func TestFetchMetadata_NestedNavigationRefused(t *testing.T) {
	_, gated := testGatedServer()
	// Every nested destination the platform can be framed in.
	for _, dest := range []string{"iframe", "frame", "object", "embed"} {
		t.Run(dest, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = "127.0.0.1:7777"
			req.Header.Set("Sec-Fetch-Site", "same-site")
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			req.Header.Set("Sec-Fetch-Dest", dest)
			rec := httptest.NewRecorder()
			gated.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 — the console is framable from a co-hosted app (body %q)",
					rec.Code, rec.Body.String())
			}
		})
	}

	// The positive vector: a real top-level navigation from a co-hosted app is
	// an ordinary link and must still land.
	t.Run("top-level document navigation admitted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Sec-Fetch-Site", "same-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "cross-origin request blocked") {
			t.Fatalf("an ordinary link into the console was refused: %q", rec.Body.String())
		}
	})

	// A navigation with no Sec-Fetch-Dest at all cannot prove it is top-level.
	t.Run("navigate without a Dest refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Sec-Fetch-Site", "same-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
		}
	})
}

// TestFetchMetadata_HeaderShape pins that the gate keys on the header KEY's
// presence, not on a non-empty value. An empty Sec-Fetch-Site must not fall
// through to the weaker Origin check (a GET carries no Origin, so it would pass
// unconditionally), and a repeated header — a shape no browser produces, since
// Sec-* is a forbidden header name — must not be resolved by silently taking
// the first value, which is what Header.Get does.
func TestFetchMetadata_HeaderShape(t *testing.T) {
	_, gated := testGatedServer()

	t.Run("empty value blocks", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Sec-Fetch-Site", "")
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
		}
	})

	t.Run("repeated header blocks even when the first value admits", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
		req.Host = "127.0.0.1:7777"
		req.Header["Sec-Fetch-Site"] = []string{"same-origin", "cross-site"}
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 — Header.Get would have taken %q (body %q)",
				rec.Code, "same-origin", rec.Body.String())
		}
	})

	t.Run("unknown token blocks", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
		req.Host = "127.0.0.1:7777"
		req.Header.Set("Sec-Fetch-Site", "same-party")
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
		}
	})
}

// TestCrossOriginGate_HTTPSOriginOverPlainConnection pins the deployment shape
// a connection-derived scheme would break: a TLS-terminating proxy in front of
// a NON-loopback bind, preserving Host, with no LOUPE_PUBLIC_ORIGIN declared.
// The browser's Origin says https while the proxied hop arrives as plain http,
// so pinning the scheme to r.TLS would 403 every write — including login — with
// no boot-time signal. Both scheme forms of r.Host are accepted; the rebinding
// hardening lives in the loopback/bind-host requirement, not in the scheme.
func TestCrossOriginGate_HTTPSOriginOverPlainConnection(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), bindHost: "console.internal"}
	req := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
	req.Host = "console.internal"
	req.Header.Set("Origin", "https://console.internal")
	rec := httptest.NewRecorder()
	if s.crossOriginBlocked(rec, req) {
		t.Fatalf("an https Origin over a proxied plain connection was blocked: %q", rec.Body.String())
	}

	// The hardening the branch actually rests on is unaffected: a host that is
	// neither loopback nor the configured bind host is still refused, in either
	// scheme form.
	for _, origin := range []string{"https://evil.example", "http://evil.example"} {
		bad := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader("{}"))
		bad.Host = "evil.example"
		bad.Header.Set("Origin", origin)
		badRec := httptest.NewRecorder()
		if !s.crossOriginBlocked(badRec, bad) {
			t.Fatalf("origin %q agreeing with Host was accepted; the bind-host requirement must block it", origin)
		}
	}
}
