package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// browserEngineConfig holds the browser-native serving mode's wiring (EDGE.5
// W4 inc 4, edge-browser-node-design.md §3.4). When enabled (FACET_BROWSER_
// ENGINE), cmd/facet stops being the engine host and becomes a static file
// server: it serves the wasm engine artifact + the JS transport shell, and
// injects a per-session window.__EDGE_BOOT__ so the browser runs the engine
// IN-PAGE over WebSocket — the "no local binary" half of Facet Fire 4's green
// bar. Absent this config, cmd/facet is the shipped Go host, byte-for-byte
// unchanged: nothing here is registered and no page is rewritten.
type browserEngineConfig struct {
	// wasmDir holds the build-edge-wasm output (edge.wasm + wasm_exec.js),
	// served at /edge.wasm + /wasm_exec.js. The two are a version-locked pair
	// (wasm_exec.js is tied to the compiler that built the module).
	wasmDir string
	// shellDir holds internal/edge/browser/shell (shell.mjs + its vendored
	// nats.js.mjs + leader.mjs), served under /shell/. shell.mjs imports its
	// siblings relatively, so they must be co-served from one prefix.
	shellDir string
	// wsURL is the NATS WebSocket listener (the W1 listener, natsperm
	// WebsocketPort) the in-page shell dials. Injected into __EDGE_BOOT__.
	wsURL string
}

// edgeBootConfig is the window.__EDGE_BOOT__ payload boot.mjs reads to start
// the in-page engine. deviceId is deliberately omitted: it is browser-local
// (design §3.5 — persisted in localStorage so a reload resumes the SAME
// durable consumer), resolved client-side by boot.mjs's resolveDeviceId, not
// handed down by this server which has no stable per-browser id to give.
type edgeBootConfig struct {
	IdentityID string `json:"identityId"`
	WsURL      string `json:"wsUrl"`
	GatewayURL string `json:"gatewayUrl"`
	Token      string `json:"token"`
}

// bootScriptTag is the exact index.html marker the __EDGE_BOOT__ script is
// spliced in FRONT of, so the config global exists before boot.mjs's module
// top level reads it. Kept in sync with web/index.html by injectedIndex
// failing loudly if the marker is gone.
const bootScriptTag = `<script type="module" src="/boot.mjs"></script>`

// registerBrowserEngineRoutes serves the browser-native assets under the paths
// boot.mjs defaults to (/edge.wasm, /wasm_exec.js, /shell/shell.mjs). Only
// called in browser-native mode.
func (s *server) registerBrowserEngineRoutes(inner *http.ServeMux) {
	inner.Handle("/edge.wasm", s.serveWasmAsset("edge.wasm", "application/wasm"))
	inner.Handle("/wasm_exec.js", s.serveWasmAsset("wasm_exec.js", "text/javascript; charset=utf-8"))
	inner.Handle("/shell/", http.StripPrefix("/shell/", fixMimeFileServer(http.Dir(s.browserEngine.shellDir))))
}

// serveWasmAsset serves one file out of wasmDir with an explicit Content-Type.
// The type MUST be set: WebAssembly.instantiateStreaming rejects a wasm module
// served as anything but application/wasm, and a module .mjs served as
// octet-stream is refused by the browser's module loader. http.ServeFile only
// fills Content-Type when unset, so pre-setting it here wins.
func (s *server) serveWasmAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		http.ServeFile(w, r, filepath.Join(s.browserEngine.wasmDir, name))
	}
}

// fixMimeFileServer wraps a directory file server, forcing the Content-Type for
// the module (.mjs) and wasm (.wasm) extensions Go's mime table does not
// reliably register — set before delegating so http.ServeContent keeps it.
func fixMimeFileServer(root http.FileSystem) http.Handler {
	fileServer := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".mjs"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(r.URL.Path, ".wasm"):
			w.Header().Set("Content-Type", "application/wasm")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveBrowserIndex serves the app shell in browser-native mode. For the index
// itself it rewrites the page to carry window.__EDGE_BOOT__ for the resolved
// session; every other path (app.js, style.css, boot.mjs, …) delegates to the
// embedded file server unchanged. Reached only after requireSession, so an
// unauthenticated navigation was already redirected to /login and never lands
// here.
func (s *server) serveBrowserIndex(fileServer http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			fileServer.ServeHTTP(w, r)
			return
		}
		cfg, ok := s.bootConfigForSession(r)
		if !ok {
			// No injectable per-session credential (e.g. a boot-env fallback
			// with no token to hand the browser). Serve the page verbatim:
			// boot.mjs sees no __EDGE_BOOT__ and app.js falls back to the SSE
			// source, which the Go host still answers.
			fileServer.ServeHTTP(w, r)
			return
		}
		page, err := injectedIndex(cfg)
		if err != nil {
			s.logger.Error("facet: inject __EDGE_BOOT__ failed; serving the un-injected page (SSE fallback)", "error", err)
			fileServer.ServeHTTP(w, r)
			return
		}
		// The page now carries a bearer JWT in its body (the browser needs it
		// in JS to open the WS connection — it cannot stay HttpOnly-cookie-only
		// once nats.js is the client). Never cache a token-bearing document.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write(page); err != nil {
			s.logger.Error("facet: write injected index", "error", err)
		}
	}
}

// bootConfigForSession builds the __EDGE_BOOT__ payload for the request's
// resolved session, or reports !ok when there is no per-browser credential to
// inject. The token is exactly the credential the Go host would have used as
// this identity's NATS connection token: a cookie session's own JWT, or — for
// the boot-env single-user fallback — the process's EDGE_TOKEN.
func (s *server) bootConfigForSession(r *http.Request) (edgeBootConfig, bool) {
	if s.browserEngine == nil {
		return edgeBootConfig{}, false
	}
	identityID, ok := appsession.Identity(r.Context())
	if !ok {
		return edgeBootConfig{}, false
	}
	var token string
	switch {
	case appsession.ViaCookie(r.Context()):
		token = s.session.CookieToken(r)
	case identityID == s.bootIdentityID:
		token = s.bootToken
	}
	if token == "" {
		return edgeBootConfig{}, false
	}
	return edgeBootConfig{
		IdentityID: identityID,
		WsURL:      s.browserEngine.wsURL,
		GatewayURL: s.gatewayURL,
		Token:      token,
	}, true
}

// injectedIndex splices a window.__EDGE_BOOT__ script in front of the boot.mjs
// tag in the embedded index.html. The config is json.Marshal'd, whose default
// HTML escaping (< > & → \u00xx) makes it safe to inline inside <script>, so a
// token can never break out of the element. Fails loudly if the marker moved,
// so a future index.html edit can't silently strand the injection.
func injectedIndex(cfg edgeBootConfig) ([]byte, error) {
	raw, err := webFS.ReadFile("web/index.html")
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	inject := []byte("<script>window.__EDGE_BOOT__ = " + string(payload) + ";</script>\n" + bootScriptTag)
	out := bytes.Replace(raw, []byte(bootScriptTag), inject, 1)
	if bytes.Equal(out, raw) {
		return nil, fmt.Errorf("boot.mjs script tag marker not found in index.html")
	}
	return out, nil
}
