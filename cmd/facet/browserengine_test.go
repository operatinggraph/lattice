package main

import (
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/appsession"
)

// writeAssetDirs lays down a fake wasm dir + shell dir so the serving handlers
// have real files to return without building the 1.7 MB artifact.
func writeAssetDirs(t *testing.T) (wasmDir, shellDir string) {
	t.Helper()
	wasmDir = t.TempDir()
	shellDir = t.TempDir()
	write := func(dir, name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	write(wasmDir, "edge.wasm", "\x00asm\x01\x00\x00\x00fake-module")
	write(wasmDir, "wasm_exec.js", "globalThis.Go = function(){};")
	write(shellDir, "shell.mjs", "export function createShell(){ return {}; }")
	write(shellDir, "nats.js.mjs", "export const wsconnect = () => {};")
	return wasmDir, shellDir
}

func browserModeServer(t *testing.T, wasmDir, shellDir string) *server {
	t.Helper()
	return &server{
		logger:     slog.Default(),
		gatewayURL: "http://gw.example:8080",
		session:    testSession(t, nil),
		browserEngine: &browserEngineConfig{
			wasmDir:  wasmDir,
			shellDir: shellDir,
			wsURL:    "ws://127.0.0.1:9222",
		},
	}
}

func TestBrowserEngine_ServesWasmAndShellAssets(t *testing.T) {
	wasmDir, shellDir := writeAssetDirs(t)
	srv := browserModeServer(t, wasmDir, shellDir)
	// A boot identity lets RequireSession resolve for these non-exempt asset
	// GETs without a cookie (the assets themselves carry no per-user data).
	withBootIdentity(t, srv, "bootident0123456789x")

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	cases := []struct {
		path        string
		wantType    string
		wantBodyHas string
	}{
		{"/edge.wasm", "application/wasm", "fake-module"},
		{"/wasm_exec.js", "text/javascript", "globalThis.Go"},
		{"/shell/shell.mjs", "text/javascript", "createShell"},
		{"/shell/nats.js.mjs", "text/javascript", "wsconnect"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		mux.ServeHTTP(w, r)
		require.Equalf(t, http.StatusOK, w.Code, "path=%s", tc.path)
		require.Containsf(t, w.Header().Get("Content-Type"), tc.wantType, "path=%s content-type", tc.path)
		require.Containsf(t, w.Body.String(), tc.wantBodyHas, "path=%s body", tc.path)
	}
}

func TestBrowserEngine_InjectsBootConfigForCookieSession(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer := appsession.NewSigner(priv, "test", appsession.DevTokenTTL, time.Now)
	authn, err := buildTestVerifier(&priv.PublicKey, "test")
	require.NoError(t, err)

	wasmDir, shellDir := writeAssetDirs(t)
	srv := browserModeServer(t, wasmDir, shellDir)
	srv.devSigner = signer
	srv.session = testSession(t, func(c *appsession.Config) {
		c.Signer = signer
		c.Authn = authn
	})

	identity := testNanoID(t)
	token, _, err := signer.Mint(identity)
	require.NoError(t, err)

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	mux.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	require.Contains(t, body, "window.__EDGE_BOOT__", "the index must carry the boot global")
	require.Contains(t, body, `"identityId":"`+identity+`"`)
	require.Contains(t, body, `"wsUrl":"ws://127.0.0.1:9222"`)
	require.Contains(t, body, `"gatewayUrl":"http://gw.example:8080"`)
	require.Contains(t, body, `"token":"`+token+`"`)
	require.NotContains(t, body, `"deviceId"`, "the device id is browser-local (§3.5), never injected")

	// The config global must precede the boot.mjs module so its top level sees it.
	require.Less(t, strings.Index(body, "window.__EDGE_BOOT__"), strings.Index(body, bootScriptTag),
		"__EDGE_BOOT__ must be injected before the boot.mjs script tag")
	// A token-bearing page must never be cached.
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestBrowserEngine_ModeOffLeavesShippedHostUnchanged(t *testing.T) {
	// browserEngine nil = shipped Go host. The index is served verbatim and
	// the browser-native asset routes do not exist.
	srv := &server{logger: slog.Default()}
	withBootIdentity(t, srv, "bootident0123456789x")
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "window.__EDGE_BOOT__", "no injection when the mode is off")
	require.Contains(t, w.Body.String(), bootScriptTag, "the shipped page still loads boot.mjs")

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/edge.wasm", nil))
	require.Equal(t, http.StatusNotFound, w2.Code, "wasm route is not registered when the mode is off")
}

func TestBrowserEngine_NoInjectionWithoutSessionToken(t *testing.T) {
	// Browser mode on, but the resolved identity is the boot fallback and no
	// EDGE_TOKEN was configured — nothing to inject, so the page is served
	// verbatim and app.js falls back to the SSE source.
	wasmDir, shellDir := writeAssetDirs(t)
	srv := browserModeServer(t, wasmDir, shellDir)
	withBootIdentity(t, srv, "bootident0123456789x") // no bootToken set

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "window.__EDGE_BOOT__")
	require.Contains(t, w.Body.String(), bootScriptTag)
}

func TestBrowserEngine_BootFallbackInjectsProcessToken(t *testing.T) {
	wasmDir, shellDir := writeAssetDirs(t)
	srv := browserModeServer(t, wasmDir, shellDir)
	withBootIdentity(t, srv, "bootident0123456789x")
	srv.bootToken = "process-edge-token"

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"identityId":"bootident0123456789x"`)
	require.Contains(t, w.Body.String(), `"token":"process-edge-token"`)
}

func TestInjectedIndex_EscapesTokenAgainstScriptBreakout(t *testing.T) {
	// A token can never break out of the <script> element: json.Marshal's
	// default HTML escaping turns < > & into \u00xx, so even a hostile value
	// stays inert markup.
	out, err := injectedIndex(edgeBootConfig{
		IdentityID: "abc",
		WsURL:      "ws://x",
		GatewayURL: "http://y",
		Token:      "a</script><script>alert(1)</script>",
	})
	require.NoError(t, err)
	require.NotContains(t, string(out), "</script><script>alert(1)", "the raw breakout must be escaped away")
	require.Contains(t, string(out), "\\u003c/script\\u003e", "the token's < and > are escaped to their \\u00xx form")
}
