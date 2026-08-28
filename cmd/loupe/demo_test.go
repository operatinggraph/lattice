package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The hosted-demo posture (F20). The assertions that matter are the two
// fail-closed ones: an unknown/future route is denied by METHOD rather than by
// a path list that could go stale, and the boot guard REFUSES (not warns) when
// the console would run the demo as the bootstrap admin.

func TestDemoWriteDeniedByMethodNotPath(t *testing.T) {
	// Reads pass, whatever the path.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/systemmap"},
		{http.MethodGet, "/"},
		{http.MethodHead, "/api/health"},
		{http.MethodGet, "/api/events/stream"},
	} {
		if demoWriteDenied(tc.method, tc.path) {
			t.Errorf("demoWriteDenied(%s, %s) = true, want false (a read)", tc.method, tc.path)
		}
	}

	// Every known write is denied.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/op"},
		{http.MethodPost, "/api/packages/install"},
		{http.MethodPost, "/api/packages/uninstall"},
		{http.MethodPost, "/api/vault/decrypt"},
		{http.MethodPost, "/api/review/capability/x/approve"},
		{http.MethodPost, "/api/control/weaver"},
		{http.MethodPost, "/api/objects"},
		{http.MethodDelete, "/api/objects/abc"},
	} {
		if !demoWriteDenied(tc.method, tc.path) {
			t.Errorf("demoWriteDenied(%s, %s) = false, want true (a write)", tc.method, tc.path)
		}
	}

	// The point of the method rule: an unregistered route is denied too.
	// A path allowlist would fail OPEN here.
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		if !demoWriteDenied(m, "/api/some/future/write") {
			t.Errorf("demoWriteDenied(%s, /api/some/future/write) = false, want true (fail-closed for unknown routes)", m)
		}
	}
}

func TestDemoAllowsCredentialExchange(t *testing.T) {
	// A visitor must still be able to log in and out, so exactly these three
	// POSTs pass. None mutates platform state.
	for _, p := range []string{operatorDevTokenPath, operatorSessionPath, operatorLogoutPath} {
		if demoWriteDenied(http.MethodPost, p) {
			t.Errorf("demoWriteDenied(POST, %s) = true, want false (credential exchange)", p)
		}
	}
	// A near-miss path must not inherit the exemption.
	if !demoWriteDenied(http.MethodPost, operatorSessionPath+"/elevate") {
		t.Error("a path merely prefixed by an allowed one must still be denied")
	}
}

func TestDemoReadOnlyMiddleware(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	// Demo off: the middleware is a pass-through, writes reach the handler.
	off := &server{demoMode: false}
	rec := httptest.NewRecorder()
	off.demoReadOnly(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/op", nil))
	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("demo off: want the write to pass through, got code=%d reached=%v", rec.Code, reached)
	}

	// Demo on: the write is refused before the handler runs.
	reached = false
	on := &server{demoMode: true}
	rec = httptest.NewRecorder()
	on.demoReadOnly(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/op", nil))
	if reached {
		t.Error("demo on: the handler ran; the write must be refused before any work")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("demo on: code = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "read-only demo") {
		t.Errorf("denial should identify the demo posture, got %q", rec.Body.String())
	}

	// Demo on: reads still work.
	reached = false
	rec = httptest.NewRecorder()
	on.demoReadOnly(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/systemmap", nil))
	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("demo on: want the read to pass, got code=%d reached=%v", rec.Code, reached)
	}
}

func TestDemoOperatorGuard(t *testing.T) {
	const admin = "vtx.identity.AAAAAAAAAAAAAAAAAAAA"
	const demo = "vtx.identity.BBBBBBBBBBBBBBBBBBBB"

	// Demo off: the guard never blocks a normal console, however it is configured.
	if err := demoOperatorGuard(false, "", admin); err != nil {
		t.Errorf("demo off must never block boot, got %v", err)
	}

	// Demo on with no explicit operator: the console would fall back to the
	// bootstrap admin, so boot is refused.
	if err := demoOperatorGuard(true, "", admin); err == nil {
		t.Error("demo mode with no LOUPE_OPERATOR_ACTOR_KEY must refuse to boot")
	}
	if err := demoOperatorGuard(true, "   ", admin); err == nil {
		t.Error("a whitespace-only operator key must refuse to boot")
	}

	// Demo on explicitly naming the bootstrap admin: refused, and the error
	// says what to do instead.
	err := demoOperatorGuard(true, admin, admin)
	if err == nil {
		t.Fatal("demo mode as the bootstrap admin must refuse to boot")
	}
	if !strings.Contains(err.Error(), "LOUPE_OPERATOR_ACTOR_KEY") {
		t.Errorf("the refusal should name the fix, got %q", err)
	}

	// Demo on with a distinct explicit identity: boots.
	if err := demoOperatorGuard(true, demo, admin); err != nil {
		t.Errorf("a distinct demo operator must boot, got %v", err)
	}
	// No bootstrap file loaded (adminActor empty): an explicit key still boots,
	// and the empty admin must not accidentally match an empty-ish key.
	if err := demoOperatorGuard(true, demo, ""); err != nil {
		t.Errorf("explicit demo operator with no bootstrap admin must boot, got %v", err)
	}
}

func TestHandleDemo(t *testing.T) {
	for _, mode := range []bool{false, true} {
		s := &server{demoMode: mode}
		rec := httptest.NewRecorder()
		s.handleDemo(rec, httptest.NewRequest(http.MethodGet, "/api/demo", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("demoMode=%v: code = %d, want 200", mode, rec.Code)
		}
		var body struct {
			DemoMode bool   `json:"demoMode"`
			Notice   string `json:"notice"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.DemoMode != mode {
			t.Errorf("demoMode = %v, want %v", body.DemoMode, mode)
		}
		if body.Notice == "" {
			t.Error("notice should always be present so the banner and the 403 agree")
		}
	}

	// A write to the posture endpoint itself is method-gated.
	rec := httptest.NewRecorder()
	(&server{}).handleDemo(rec, httptest.NewRequest(http.MethodPost, "/api/demo", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/demo = %d, want 405", rec.Code)
	}
}

func TestDemoModeEnabledFailsClosedOnGarbage(t *testing.T) {
	// Unset / explicitly-off values disable the posture quietly.
	for _, v := range []string{"", "   ", "0", "false", "no", "off", "OFF"} {
		on, err := demoModeEnabled(v)
		if err != nil || on {
			t.Errorf("demoModeEnabled(%q) = (%v, %v), want (false, nil)", v, on, err)
		}
	}
	// Recognized truthy values enable it.
	for _, v := range []string{"1", "true", "yes", "on", " TRUE "} {
		on, err := demoModeEnabled(v)
		if err != nil || !on {
			t.Errorf("demoModeEnabled(%q) = (%v, %v), want (true, nil)", v, on, err)
		}
	}
	// A SET but unrecognizable value must stop the process rather than
	// silently serving a writable admin console on a public URL.
	for _, v := range []string{"enabled", "Y", "2", "demo", "readonly"} {
		if _, err := demoModeEnabled(v); err == nil {
			t.Errorf("demoModeEnabled(%q) returned no error; a typo must fail closed", v)
		}
	}
}

// F20.2 §7.1 — the control-plane read carve-out. The classification can only
// ever NARROW the method rule, so the tests pin both directions: the three
// inspect-only reads pass, and everything else — including a malformed path that
// merely looks like one — stays denied.

func TestDemoAllowsControlPlaneReads(t *testing.T) {
	for _, p := range []string{
		"/api/control/loom/main/inspect",
		"/api/control/refractor/abc123/health",
		"/api/control/refractor/abc123/validate",
	} {
		if demoWriteDenied(http.MethodPost, p) {
			t.Errorf("demoWriteDenied(POST, %s) = true, want false (a control-plane read)", p)
		}
	}

	// Every op the classification does not name stays denied — including every
	// weaver op, which mutate without exception.
	for _, p := range []string{
		"/api/control/loom/main/pause",
		"/api/control/loom/main/resume",
		"/api/control/loom/main/redrive",
		"/api/control/weaver/t1/disable",
		"/api/control/weaver/t1/enable",
		"/api/control/weaver/t1/revoke",
		"/api/control/weaver/t1/resetConfidence",
		"/api/control/weaver/t1/replayTarget",
		"/api/control/refractor/abc/rebuild",
		"/api/control/refractor/abc/pause",
		"/api/control/refractor/abc/resume",
		"/api/control/refractor/abc/delete",
		// An op name borrowed from another component's read set must not pass.
		"/api/control/weaver/t1/inspect",
		"/api/control/loom/main/health",
	} {
		if !demoWriteDenied(http.MethodPost, p) {
			t.Errorf("demoWriteDenied(POST, %s) = false, want true (a mutate)", p)
		}
	}

	// Shape: the carve-out only recognizes exactly <comp>/<name>/<op>, parsed
	// with the same helper handleControl routes with, so the gate cannot
	// permit a request the handler would route somewhere else.
	for _, p := range []string{
		"/api/control/loom/inspect",                  // too short — no name
		"/api/control/loom/main/inspect/extra",       // too long
		"/api/control/loom//inspect",                 // empty name collapses to 2 parts
		"/api/control/nosuch/main/inspect",           // unknown component
		"/api/control/loom/main.sub/inspect",         // dotted name (subject-injection shape)
		"/api/control/loom/main/INSPECT",             // case-sensitive, like the op table
		"/api/controlx/loom/main/inspect",            // prefix near-miss
		"/api/control/loom/main/inspect/../../pause", // path games
	} {
		if !demoWriteDenied(http.MethodPost, p) {
			t.Errorf("demoWriteDenied(POST, %s) = false, want true (malformed shape)", p)
		}
	}

	// The carve-out is POST-only: handleControl answers any other method with a
	// 400 shape error, so the carve-out has nothing to say about them.
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if !demoWriteDenied(m, "/api/control/loom/main/inspect") {
			t.Errorf("demoWriteDenied(%s, control read path) = false, want true (POST only)", m)
		}
	}
}

// TestReadOnlyOpsSubsetOfMutateOps pins the invariant that makes the carve-out
// safe to reason about: a read-only op is always an op the component actually
// accepts, so the gate can never permit a request mutateSubject would reject.
func TestReadOnlyOpsSubsetOfMutateOps(t *testing.T) {
	for comp, c := range controlComponents {
		for op := range c.readOnlyOps {
			if _, ok := c.mutateOps[op]; !ok {
				t.Errorf("%s: readOnlyOps has %q, which is not in mutateOps", comp, op)
			}
		}
	}
}

// TestReadOnlyOpsExactSet pins the classification itself. Widening it is the
// one way the demo posture could start permitting a real mutation, so it must
// be a deliberate, test-visible act rather than a quiet table edit.
func TestReadOnlyOpsExactSet(t *testing.T) {
	want := map[string][]string{
		"loom":      {"inspect"},
		"weaver":    {},
		"refractor": {"health", "validate"},
	}
	got := controlReadOnlyOps()
	if len(got) != len(want) {
		t.Fatalf("controlReadOnlyOps() covers %d components, want %d", len(got), len(want))
	}
	for comp, wantOps := range want {
		gotOps, ok := got[comp]
		if !ok {
			t.Errorf("no entry for %s", comp)
			continue
		}
		if strings.Join(gotOps, ",") != strings.Join(wantOps, ",") {
			t.Errorf("%s read-only ops = %v, want %v — widening this set permits a real mutation in demo mode",
				comp, gotOps, wantOps)
		}
	}
}

// TestDemoPayloadCarriesReadOnlyOps: the console hides control buttons from
// this list rather than from a second copy of the table in JavaScript, so the
// buttons shown and the ops permitted cannot drift apart.
func TestDemoPayloadCarriesReadOnlyOps(t *testing.T) {
	rec := httptest.NewRecorder()
	(&server{demoMode: true}).handleDemo(rec, httptest.NewRequest(http.MethodGet, "/api/demo", nil))
	var body struct {
		DemoMode           bool                `json:"demoMode"`
		ReadOnlyControlOps map[string][]string `json:"readOnlyControlOps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /api/demo: %v", err)
	}
	if !body.DemoMode {
		t.Fatal("demoMode should be true")
	}
	if got := body.ReadOnlyControlOps["loom"]; len(got) != 1 || got[0] != "inspect" {
		t.Errorf("loom read-only ops = %v, want [inspect]", got)
	}
	if got, ok := body.ReadOnlyControlOps["weaver"]; !ok || len(got) != 0 {
		t.Errorf("weaver read-only ops = %v (present=%v), want an empty list, not an absent key — "+
			"the client hides every op for a component it cannot find", got, ok)
	}
	// Asserted on the wire, not on the decoded map: a JSON null also decodes to
	// a present key holding a nil slice, so the check above cannot tell [] from
	// null. The client reads the raw JSON, so that is the level to pin.
	if !strings.Contains(rec.Body.String(), `"weaver":[]`) {
		t.Errorf("body = %s, want weaver serialized as [] — a component with no "+
			"inspect-only ops still gets a list", rec.Body.String())
	}
}

// demoHideMarker is the attribute api.js's demoHide() sets and style.css's
// shell rule keys off. A view module never writes it itself.
const demoHideMarker = "data-demo-hide"

var (
	// A three-argument el(tag, cls, text) call — the only form that carries a
	// button's visible label. cls is `null` or a class string.
	elButtonCall = regexp.MustCompile(`\bel\(\s*"([a-z0-9]+)"\s*,\s*(?:null|"[^"]*")\s*,\s*"([^"]*)"\s*\)`)
	// The same call bound to a local name, which is what a later demoHide(name)
	// refers to.
	elButtonBinding = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:demoHide\(\s*)?` +
		`el\(\s*"([a-z0-9]+)"\s*,\s*(?:null|"[^"]*")\s*,\s*"([^"]*)"\s*\)`)
)

// demoAffordances reports which button labels a view module builds and which of
// them it marks demo-hidden, resolving each demoHide() argument through the
// module's LOCAL BINDINGS rather than matching one spelling of the call.
//
// `demoHide(el("button", null, "X"))` and a `const b = el("button", null, "X")`
// marked by a later `demoHide(b)` are the same fact about the shipped page. An
// assertion that recognizes only the first says nothing the day someone splits
// the line, which is exactly when a control meant to stay visible gets hidden
// without anyone noticing.
func demoAffordances(src string) (declared, hidden map[string]bool) {
	declared = map[string]bool{}
	for _, m := range elButtonCall.FindAllStringSubmatch(src, -1) {
		if m[1] == "button" {
			declared[m[2]] = true
		}
	}
	byName := map[string]string{}
	for _, m := range elButtonBinding.FindAllStringSubmatch(src, -1) {
		if m[2] == "button" {
			byName[m[1]] = m[3]
		}
	}
	hidden = map[string]bool{}
	for _, arg := range callArguments(src, "demoHide") {
		arg = strings.TrimSpace(arg)
		if strings.HasPrefix(arg, "el(") {
			if m := elButtonCall.FindStringSubmatch(arg); m != nil && m[1] == "button" {
				hidden[m[2]] = true
			}
			continue
		}
		if label, ok := byName[arg]; ok {
			hidden[label] = true
		}
	}
	return declared, hidden
}

// callArguments returns the argument text of every fn(...) call in src, matched
// on balanced parentheses so a nested call is returned whole. Parentheses inside
// a string literal are balanced within it and so do not disturb the depth count.
func callArguments(src, fn string) []string {
	var out []string
	needle := fn + "("
	for i := 0; ; {
		j := strings.Index(src[i:], needle)
		if j < 0 {
			return out
		}
		start := i + j + len(needle)
		depth, k := 1, start
		for ; k < len(src) && depth > 0; k++ {
			switch src[k] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth == 0 {
			out = append(out, src[start:k-1])
		}
		i = start
	}
}

// The authoring path is SHOWN in the demo and REFUSED by the server. Both
// halves have to hold, and they are independent: suppression has only ever been
// cosmetic (api.js — "demoWriteDenied is enforcement"), so leaving an affordance
// up is a product decision about what a visitor gets to see, honest exactly as
// long as the server still says no. Nothing else in the suite pins an
// affordance's VISIBILITY, so a later fire re-adding the marker as tidiness
// would otherwise pass unnoticed.
func TestDemoShowsAuthoringPathAndStillRefusesTheWrite(t *testing.T) {
	// Half one — the shipped assets put the two affordances on screen.
	index, err := webFS.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	page := string(index)
	if !strings.Contains(page, `<a href="#/weaver/author">Author</a>`) {
		t.Error("the Author nav link is not present unmarked — a demo visitor cannot reach the authoring page")
	}
	if strings.Contains(page, `href="#/weaver/author" data-demo-hide`) {
		t.Error("the Author nav link carries data-demo-hide again; the demo is meant to show this path")
	}

	view, err := webFS.ReadFile("web/js/views/weaverauthor.js")
	if err != nil {
		t.Fatalf("read views/weaverauthor.js: %v", err)
	}
	src := string(view)

	// The analyzer's own positive vector, first: both spellings of one fact must
	// resolve, or every "is not hidden" assertion below is a negative test with
	// nothing showing it can see a hidden button at all.
	if _, probe := demoAffordances(`const a = el("button", null, "Inline");
		demoHide(el("button", null, "Inline"));
		const b = el("button", "ghost-btn", "Bound");
		demoHide(b);
		const c = el("button", null, "Visible");`); !probe["Inline"] || !probe["Bound"] || probe["Visible"] {
		t.Fatalf("demoAffordances resolves %v on its own probe, want Inline and Bound hidden and Visible not", probe)
	}

	declared, hidden := demoAffordances(src)
	if !declared["Submit"] {
		t.Fatal("the Describe panel's Submit button is gone; the rest of this test asserts nothing")
	}
	if hidden["Submit"] {
		t.Error("the Describe Submit is demo-hidden again; it is meant to be visible and answer the server's 403")
	}
	// The split is deliberate, not an oversight: the buttons that were never
	// part of this posture stay marked.
	for _, marked := range []string{"Run checks", "Propose"} {
		if !hidden[marked] {
			t.Errorf("expected the %q button to stay demo-hidden", marked)
		}
	}
	// demoHide() is the only sanctioned way to set the marker, which is what
	// makes the resolution above complete. A view writing the attribute itself
	// would hide a control this test cannot see.
	if strings.Contains(src, demoHideMarker) {
		t.Errorf("views/weaverauthor.js sets %s directly; the marker belongs to api.js's demoHide()", demoHideMarker)
	}

	// Half two — the POST those affordances lead to is refused, with the demo's
	// own message. The path is resolved through registerRoutes first: the method
	// rule denies UNKNOWN routes too, so a typo here would otherwise pass by
	// falling through to that default-deny while pinning nothing.
	const authorRequestPath = "/api/weaver/author/request"
	mux := http.NewServeMux()
	(&server{}).registerRoutes(mux)
	if _, pattern := mux.Handler(httptest.NewRequest(http.MethodPost, authorRequestPath, nil)); pattern != authorRequestPath {
		t.Fatalf("%s routes to pattern %q — the denial below would be about a route that does not exist", authorRequestPath, pattern)
	}

	reached := false
	demo := &server{demoMode: true}
	rec := httptest.NewRecorder()
	demo.demoReadOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).
		ServeHTTP(rec, httptest.NewRequest(http.MethodPost, authorRequestPath, nil))
	if reached {
		t.Error("the authoring request reached its handler in demo mode")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST %s in demo mode = %d, want 403 — visible is not permitted", authorRequestPath, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), demoDenialMessage) {
		t.Errorf("denial body = %q, want the demo's own message so the panel can render it verbatim", rec.Body.String())
	}
}
