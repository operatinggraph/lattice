package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testServer builds a server with a NIL connection (the NATS-unreachable
// posture) and a discard logger, wired through the real route mux. It exercises
// the HTTP layer — registerRoutes, requireConn, writeError/writeJSON, and the
// method-routers — without a live NATS, matching the package's pure-helper test
// style (no embedded server).
func testServer() *http.ServeMux {
	s := &server{
		conn:        nil,
		adminActor:  "vtx.identity.admin1",
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		natsTimeout: time.Second,
		uploadCap:   1 << 20,
	}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

// TestHandlers_NilConn_ReturnsBadGateway pins the load-bearing requireConn
// guard: with no NATS connection every data handler must answer a JSON 502, not
// panic on the nil *substrate.Conn. This also drives each handler's routing
// (mux registration, path/method dispatch) up to the conn check.
func TestHandlers_NilConn_ReturnsBadGateway(t *testing.T) {
	mux := testServer()
	cases := []struct {
		name, method, path, body string
	}{
		{"corekv list", http.MethodGet, "/api/corekv", ""},
		{"corekv entry", http.MethodGet, "/api/corekv/entry?key=vtx.meta.x", ""},
		{"vertices", http.MethodGet, "/api/vertices", ""},
		{"vertex", http.MethodGet, "/api/vertex?key=vtx.meta.x", ""},
		{"health", http.MethodGet, "/api/health", ""},
		{"systemmap", http.MethodGet, "/api/systemmap", ""},
		{"tasks", http.MethodGet, "/api/tasks", ""},
		{"packages", http.MethodGet, "/api/packages", ""},
		{"ops", http.MethodGet, "/api/ops", ""},
		{"op submit", http.MethodPost, "/api/op", `{"operationType":"X","class":"y"}`},
		{"control read", http.MethodGet, "/api/control/loom", ""},
		{"control mutate", http.MethodPost, "/api/control/loom/main/pause", ""},
		{"object upload", http.MethodPost, "/api/objects", ""},
		{"object get", http.MethodGet, "/api/objects/OID1", ""},
		{"object detach", http.MethodDelete, "/api/objects/OID1?targetKey=vtx.identity.I1&linkName=profilePhoto", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, body))

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
			}
			if _, ok := resp["error"]; !ok {
				t.Errorf("response carries no error field: %v", resp)
			}
		})
	}
}

// TestHandlers_RouterBranches_BadRequest covers the method/path branches that
// decide BEFORE requireConn, so a nil conn still proves the router's own
// dispatch: handleOp's POST gate, the handleObjects method-router default
// (objects.go:73), and the handleControl shape default.
func TestHandlers_RouterBranches_BadRequest(t *testing.T) {
	mux := testServer()
	cases := []struct{ name, method, path string }{
		// handleOp gates on POST before touching the connection.
		{"op non-POST", http.MethodGet, "/api/op"},
		// handleObjects method-router default — no verb/shape match.
		{"objects PUT", http.MethodPut, "/api/objects"},
		{"objects GET bare (no oid)", http.MethodGet, "/api/objects"},
		{"objects POST with oid", http.MethodPost, "/api/objects/OID1"},
		{"objects DELETE bare (no oid)", http.MethodDelete, "/api/objects"},
		// handleControl shape default — parts parsed before requireConn.
		{"control GET two parts", http.MethodGet, "/api/control/loom/extra"},
		{"control POST two parts", http.MethodPost, "/api/control/loom/x"},
		{"control DELETE one part", http.MethodDelete, "/api/control/loom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %q)", rec.Code, rec.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
			}
			if resp["error"] == "" {
				t.Errorf("response carries no error field: %v", resp)
			}
		})
	}
}

// TestWriteHelpers pins the status/shape/Content-Type of the two response
// writers every handler funnels through.
func TestWriteHelpers(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	t.Run("writeJSON encodes with the given status", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.writeJSON(rec, http.StatusTeapot, map[string]int{"n": 7})
		if rec.Code != http.StatusTeapot {
			t.Errorf("code = %d, want 418", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var got map[string]int
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		if got["n"] != 7 {
			t.Errorf("n = %d, want 7", got["n"])
		}
	})

	t.Run("writeError wraps the message in {error}", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.writeError(rec, http.StatusBadRequest, "boom")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("code = %d, want 400", rec.Code)
		}
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		if got["error"] != "boom" {
			t.Errorf("error = %q, want boom", got["error"])
		}
	})
}

// TestRequireConn checks the guard in isolation: a nil conn writes a 502 and
// reports not-ok, so a handler short-circuits instead of dereferencing nil.
func TestRequireConn(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	conn, ok := s.requireConn(rec)
	if ok || conn != nil {
		t.Fatalf("requireConn(nil) = (%v, %v), want (nil, false)", conn, ok)
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", rec.Code)
	}
}

// TestMustJSON confirms the marshal helper produces valid JSON for the control
// fan-out error wrapper.
func TestMustJSON(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal(mustJSON(map[string]string{"error": "x"}), &m); err != nil {
		t.Fatalf("mustJSON produced invalid JSON: %v", err)
	}
	if m["error"] != "x" {
		t.Errorf("error = %q, want x", m["error"])
	}
}
