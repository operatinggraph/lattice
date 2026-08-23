package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dop251/goja"
)

// F20.2 §7.2 — the affordance-suppression decision, against the shipped
// embedded asset. The governing assertion is that the client never restates
// the server's read-only classification: it reads it off /api/demo, and any
// shape it does not recognize hides the button rather than showing one whose
// only outcome is a 403.

func TestDemoPostureOnRequiresExplicitTrue(t *testing.T) {
	vm := logicVM(t, "demo.js")

	for _, p := range []any{
		nil,
		map[string]any{},
		map[string]any{"demoMode": false},
		map[string]any{"demoMode": "true"},
		map[string]any{"demoMode": 1},
		map[string]any{"error": "request failed: offline"},
	} {
		if got := call(t, vm, "demoPostureOn", p); got != false {
			t.Errorf("demoPostureOn(%v) = %v, want false", p, got)
		}
	}
	if got := call(t, vm, "demoPostureOn", map[string]any{"demoMode": true}); got != true {
		t.Errorf("demoPostureOn({demoMode:true}) = %v, want true", got)
	}
}

// demoPayload is the shape the server actually sends (pinned Go-side by
// TestDemoPayloadCarriesReadOnlyOps).
func demoPayload() map[string]any {
	return map[string]any{
		"demoMode": true,
		"readOnlyControlOps": map[string]any{
			"loom":      []any{"inspect"},
			"weaver":    []any{},
			"refractor": []any{"health", "validate"},
		},
	}
}

func TestDemoControlOpHiddenMatchesServerClassification(t *testing.T) {
	vm := logicVM(t, "demo.js")
	payload := demoPayload()

	// The three inspect-only reads stay on screen — they work in demo mode.
	for _, tc := range []struct{ comp, op string }{
		{"loom", "inspect"},
		{"refractor", "health"},
		{"refractor", "validate"},
	} {
		if got := call(t, vm, "demoControlOpHidden", payload, tc.comp, tc.op); got != false {
			t.Errorf("demoControlOpHidden(%s/%s) = %v, want false (an inspect-only read)", tc.comp, tc.op, got)
		}
	}

	// Everything the server refuses is hidden — including an op name borrowed
	// from a different component's read set.
	for _, tc := range []struct{ comp, op string }{
		{"loom", "pause"},
		{"loom", "resume"},
		{"loom", "health"},
		{"weaver", "disable"},
		{"weaver", "enable"},
		{"weaver", "revoke"},
		{"weaver", "inspect"},
		{"refractor", "rebuild"},
		{"refractor", "pause"},
		{"refractor", "resume"},
		{"refractor", "delete"},
		{"gateway", "anything"}, // a component with no entry at all
	} {
		if got := call(t, vm, "demoControlOpHidden", payload, tc.comp, tc.op); got != true {
			t.Errorf("demoControlOpHidden(%s/%s) = %v, want true (a mutate)", tc.comp, tc.op, got)
		}
	}
}

func TestDemoControlOpHiddenIsInertOutsideDemoMode(t *testing.T) {
	vm := logicVM(t, "demo.js")

	// The ordinary operator console must be untouched: nothing is hidden, and
	// a missing classification never leaks into it.
	for _, p := range []any{
		nil,
		map[string]any{"demoMode": false},
		map[string]any{"demoMode": false, "readOnlyControlOps": map[string]any{"loom": []any{}}},
	} {
		for _, op := range []string{"pause", "inspect", "revoke"} {
			if got := call(t, vm, "demoControlOpHidden", p, "loom", op); got != false {
				t.Errorf("demoControlOpHidden(%v, loom/%s) = %v, want false outside demo mode", p, op, got)
			}
		}
	}
}

func TestDemoControlOpHiddenOmissionDenies(t *testing.T) {
	vm := logicVM(t, "demo.js")

	// In demo mode, any classification shape the client cannot read hides the
	// button. Degrading to "too little shown" keeps the suppression honest;
	// degrading the other way would put a button on screen that can only 403.
	for _, p := range []any{
		map[string]any{"demoMode": true},                                                          // field absent entirely
		map[string]any{"demoMode": true, "readOnlyControlOps": nil},                               // null
		map[string]any{"demoMode": true, "readOnlyControlOps": "loom:inspect"},                    // wrong type
		map[string]any{"demoMode": true, "readOnlyControlOps": map[string]any{}},                  // no components
		map[string]any{"demoMode": true, "readOnlyControlOps": map[string]any{"loom": "inspect"}}, // not a list
		map[string]any{"demoMode": true, "readOnlyControlOps": map[string]any{"loom": nil}},
	} {
		if got := call(t, vm, "demoControlOpHidden", p, "loom", "inspect"); got != true {
			t.Errorf("demoControlOpHidden(%v, loom/inspect) = %v, want true (omission denies)", p, got)
		}
	}
}

// An affordance the demo leaves on screen exists so a visitor can trigger the
// server's denial — so that denial is the posture working, not a fault, and it
// must not render as the red error text an unexpected failure earns. What the
// notice says is always the SERVER's own message; the console never restates
// the rule, the same way demoControlOpHidden reads the classification off
// /api/demo instead of duplicating it.
func TestDemoRefusalNotice(t *testing.T) {
	vm := logicVM(t, "demo.js")
	const denial = "read-only demo: this console accepts reads only — write actions and PII reveals are refused"

	notice := call(t, vm, "demoRefusalNotice", demoPayload(),
		map[string]any{"httpStatus": 403, "error": denial}).(map[string]any)
	if notice["title"] != "Read-only demo" {
		t.Errorf("title = %v, want the demo posture named", notice["title"])
	}
	if notice["text"] != denial {
		t.Errorf("text = %v, want the server's own message verbatim", notice["text"])
	}

	// A 403 the demo posture did not author (the cross-origin gate) is still
	// reported as the refusal it is — claiming the demo for it would be a lie
	// about why the console said no.
	other := call(t, vm, "demoRefusalNotice", map[string]any{"demoMode": false},
		map[string]any{"httpStatus": 403, "error": "cross-origin request refused"}).(map[string]any)
	if other["title"] == "Read-only demo" {
		t.Errorf("a non-demo 403 was labelled %v", other["title"])
	}
	if other["text"] != "cross-origin request refused" {
		t.Errorf("text = %v, want the server's reason", other["text"])
	}

	// The same refusal WITH the demo posture on — the case a posture-only title
	// gets wrong, and the deployment where it happens: the cross-origin gate runs
	// inside requireOperator, outside demoReadOnly entirely, and the demo is
	// precisely the console served on a public origin. Demo mode does not make
	// every 403 the demo's.
	inDemo := call(t, vm, "demoRefusalNotice", demoPayload(),
		map[string]any{"httpStatus": 403, "error": "cross-origin request blocked (Origin https://evil.example)"}).(map[string]any)
	if inDemo["title"] == "Read-only demo" {
		t.Errorf("a cross-origin 403 in demo mode was labelled %v — the demo posture did not author it", inDemo["title"])
	}
	if inDemo["text"] != "cross-origin request blocked (Origin https://evil.example)" {
		t.Errorf("text = %v, want the gate's own reason", inDemo["text"])
	}

	// Everything else stays on the ordinary error path: a success, a fault, a
	// transport failure (no status at all), and a 403 with no message to show.
	for _, body := range []any{
		nil,
		map[string]any{"proposalId": "p1"},
		map[string]any{"httpStatus": 502, "error": "submit op: dial tcp"},
		map[string]any{"error": "request failed: offline"},
		map[string]any{"httpStatus": 403},
		map[string]any{"httpStatus": 403, "error": "   "},
	} {
		if got := call(t, vm, "demoRefusalNotice", demoPayload(), body); got != nil {
			t.Errorf("demoRefusalNotice(%v) = %v, want null", body, got)
		}
	}
}

// apiBody drives the SHIPPED api() against one canned HTTP response and returns
// the body it hands its caller. fetch and location are the only browser globals
// that path reaches: location.replace records the redirect rather than
// navigating, so the 401 branch is observable instead of exploding. goja runs
// the promise jobs when the driving script returns, so nothing here waits.
func apiBody(t *testing.T, vm *goja.Runtime, status int, payload string) (map[string]any, string) {
	t.Helper()

	redirect := ""
	loc := vm.NewObject()
	if err := loc.Set("replace", func(to string) { redirect = to }); err != nil {
		t.Fatalf("stub location.replace: %v", err)
	}
	if err := vm.Set("location", loc); err != nil {
		t.Fatalf("stub location: %v", err)
	}
	if err := vm.Set("fetch", func(goja.FunctionCall) goja.Value {
		res := vm.NewObject()
		for k, v := range map[string]any{"status": status, "ok": status >= 200 && status < 300} {
			if err := res.Set(k, v); err != nil {
				t.Fatalf("stub response.%s: %v", k, err)
			}
		}
		if err := res.Set("text", func() *goja.Promise {
			p, resolve, _ := vm.NewPromise()
			resolve(vm.ToValue(payload))
			return p
		}); err != nil {
			t.Fatalf("stub response.text: %v", err)
		}
		p, resolve, _ := vm.NewPromise()
		resolve(res)
		return vm.ToValue(p)
	}); err != nil {
		t.Fatalf("stub fetch: %v", err)
	}

	var body map[string]any
	rejected := ""
	if err := vm.Set("__resolved", func(v goja.Value) { body, _ = v.Export().(map[string]any) }); err != nil {
		t.Fatalf("install resolve hook: %v", err)
	}
	if err := vm.Set("__rejected", func(v goja.Value) { rejected = v.String() }); err != nil {
		t.Fatalf("install reject hook: %v", err)
	}
	if _, err := vm.RunString(`api("/api/weaver/author/request", { method: "POST" }).then(__resolved, __rejected);`); err != nil {
		t.Fatalf("api() threw: %v", err)
	}
	if rejected != "" {
		t.Fatalf("api() rejected: %s — it is written to resolve to an object on every path", rejected)
	}
	if body == nil {
		t.Fatal("api() resolved to no object")
	}
	return body, redirect
}

// refusalBody returns the 403 bytes one of Loupe's OWN gates writes, so the
// chain below is driven by what a browser actually receives rather than by a
// hand-written stand-in that can drift from it.
func refusalBody(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/weaver/author/request", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("gate answered %d, want 403 — this test cannot pin a refusal that did not happen", rec.Code)
	}
	return rec.Body.String()
}

// The refusal chain has three links and the middle one is api.js: the server
// answers 403, api() stamps the status onto the parsed body, and
// demoRefusalNotice reads that stamp to decide it is looking at a refusal at
// all. Pinning only the ends leaves the stamp deletable with the suite green and
// the Describe panel silently back to red error text, so this drives the shipped
// api.js into the shipped logic module, on bytes the real gates wrote.
//
// Both refusals run under the demo posture, because that is the deployment where
// the two are told apart: one is the demo's standing rule, the other is not.
func TestRefusalChainFromServerBytesToNotice(t *testing.T) {
	vm := webModuleVM(t, "web/js/api.js", "web/js/logic/demo.js")

	reached := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a refused request reached its handler")
	})
	demo := &server{demoMode: true}
	plain := &server{}

	for _, tc := range []struct{ name, body, wantTitle string }{
		{"the demo's own denial", refusalBody(t, demo.demoReadOnly(reached)), "Read-only demo"},
		{"the cross-origin gate's", refusalBody(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !plain.crossOriginBlocked(w, r) {
				t.Error("the origin gate admitted a cross-origin POST")
			}
		})), "Refused"},
	} {
		body, redirect := apiBody(t, vm, http.StatusForbidden, tc.body)
		if redirect != "" {
			t.Errorf("%s: api() redirected to %q on a 403; only a 401 ends the session", tc.name, redirect)
		}
		if toFloat(body["httpStatus"]) != 403 {
			t.Fatalf("%s: api() body = %v, want httpStatus 403 stamped — without it the notice below is unreachable", tc.name, body)
		}
		notice, _ := call(t, vm, "demoRefusalNotice", demoPayload(), body).(map[string]any)
		if notice == nil {
			t.Fatalf("%s: demoRefusalNotice(%v) = null — a refused write renders as a red fault instead of the rule it is", tc.name, body)
		}
		if notice["title"] != tc.wantTitle {
			t.Errorf("%s: title = %v, want %q", tc.name, notice["title"], tc.wantTitle)
		}
	}

	// The stamp is confined to failures: a 2xx body keeps exactly the fields the
	// handler sent, and never reads as a refusal.
	okBody, _ := apiBody(t, vm, http.StatusOK, `{"proposalId":"p1"}`)
	if _, stamped := okBody["httpStatus"]; stamped {
		t.Errorf("api() stamped a 2xx body: %v", okBody)
	}
	if got := call(t, vm, "demoRefusalNotice", demoPayload(), okBody); got != nil {
		t.Errorf("demoRefusalNotice(success) = %v, want null", got)
	}

	// A 401 is the session ending, not a refusal to render: api() sends the
	// operator back to /login and the notice never sees it.
	expired, redirect := apiBody(t, vm, http.StatusUnauthorized, `{"error":"operator login required"}`)
	if redirect != "/login" {
		t.Errorf("401 redirected to %q, want /login", redirect)
	}
	if got := call(t, vm, "demoRefusalNotice", demoPayload(), expired); got != nil {
		t.Errorf("demoRefusalNotice(401) = %v, want null", got)
	}
}
