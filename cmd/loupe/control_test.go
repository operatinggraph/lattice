package main

import (
	"strings"
	"testing"
)

func TestMutateSubject(t *testing.T) {
	tests := []struct {
		name    string
		comp    string
		ctlName string
		op      string
		want    string
		wantErr bool
	}{
		{name: "loom pause", comp: "loom", ctlName: "loom-widget", op: "pause", want: "lattice.ctrl.loom.loom-widget.pause"},
		{name: "loom inspect", comp: "loom", ctlName: "abc123", op: "inspect", want: "lattice.ctrl.loom.abc123.inspect"},
		{name: "loom redrive", comp: "loom", ctlName: "inst1", op: "redrive", want: "lattice.ctrl.loom.inst1.redrive"},
		{name: "weaver disable", comp: "weaver", ctlName: "t1", op: "disable", want: "lattice.ctrl.weaver.t1.disable"},
		{name: "weaver revoke", comp: "weaver", ctlName: "t1", op: "revoke", want: "lattice.ctrl.weaver.t1.revoke"},
		{name: "weaver resetConfidence", comp: "weaver", ctlName: "t1", op: "resetConfidence", want: "lattice.ctrl.weaver.t1.resetConfidence"},
		{name: "weaver replayTarget", comp: "weaver", ctlName: "t1", op: "replayTarget", want: "lattice.ctrl.weaver.t1.replayTarget"},
		// resetBudget is a real weaver control op, deliberately NOT allow-listed
		// here: its entityId + gapColumn ride the request body, and controlMutate
		// forwards no body, so every proxied invocation would come back as the
		// plane's body-parse error. It stays CLI-only until the proxy forwards a
		// body — this row is what makes that omission a decision rather than a
		// gap someone closes by reflex.
		{name: "weaver resetBudget is body-carrying and not proxied", comp: "weaver", ctlName: "t1", op: "resetBudget", wantErr: true},
		{name: "refractor rebuild", comp: "refractor", ctlName: "lensA", op: "rebuild", want: "lattice.ctrl.refractor.lensA.rebuild"},
		{name: "refractor validate", comp: "refractor", ctlName: "lensA", op: "validate", want: "lattice.ctrl.refractor.lensA.validate"},
		{name: "refractor delete (the lens page's typed-confirm surface)", comp: "refractor", ctlName: "lensA", op: "delete", want: "lattice.ctrl.refractor.lensA.delete"},

		{name: "unknown component", comp: "bridge", ctlName: "x", op: "pause", wantErr: true},
		{name: "empty name", comp: "loom", ctlName: "", op: "pause", wantErr: true},
		{name: "dotted name", comp: "loom", ctlName: "a.b", op: "pause", wantErr: true},
		{name: "op not in loom allow-list", comp: "loom", ctlName: "x", op: "disable", wantErr: true},
		{name: "op not in weaver allow-list", comp: "weaver", ctlName: "x", op: "pause", wantErr: true},
		{name: "op not in refractor allow-list", comp: "refractor", ctlName: "x", op: "disable", wantErr: true},
		{name: "empty op", comp: "loom", ctlName: "x", op: "", wantErr: true},
		// A subject-injection attempt via op must be rejected by the allow-list,
		// never echoed into a subject.
		{name: "injection via op", comp: "loom", ctlName: "x", op: "pause.evil", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mutateSubject(tt.comp, tt.ctlName, tt.op)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got subject %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("subject = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateControlName(t *testing.T) {
	for _, c := range []struct {
		name    string
		wantErr bool
	}{
		{"abc", false},
		{"loom-widget", false},
		{"", true},
		{"a.b", true},
		{"trailing.", true},
	} {
		err := validateControlName(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("validateControlName(%q) err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

func TestReadSubjects(t *testing.T) {
	loom, ok := readSubjects("loom")
	if !ok {
		t.Fatal("loom should be a known component")
	}
	if loom["list"] != "lattice.ctrl.loom.list" {
		t.Errorf("loom list subject = %q", loom["list"])
	}
	if loom["consumers"] != "lattice.ctrl.loom.consumers" {
		t.Errorf("loom consumers subject = %q", loom["consumers"])
	}
	weaver, _ := readSubjects("weaver")
	if weaver["list"] != "lattice.ctrl.weaver.list" {
		t.Errorf("weaver list subject = %q", weaver["list"])
	}
	// Refractor exposes no top-level read subjects (per-lens only).
	refr, ok := readSubjects("refractor")
	if !ok || len(refr) != 0 {
		t.Errorf("refractor reads = %v (want empty, ok)", refr)
	}
	if _, ok := readSubjects("nope"); ok {
		t.Error("unknown component should not be ok")
	}
}

func TestSplitNonEmpty(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"loom", []string{"loom"}},
		{"loom/widget/pause", []string{"loom", "widget", "pause"}},
		{"loom/", []string{"loom"}},
		{"loom//pause", []string{"loom", "pause"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := splitNonEmpty(tt.in)
		if len(got) != len(tt.want) {
			t.Fatalf("splitNonEmpty(%q) = %v, want %v", tt.in, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("splitNonEmpty(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

// armedOpsInComponentView extracts the `armedOps` set literal from the shipped
// component view — the ops whose button arms on the first click and only runs on
// a confirming second one. Parsed from the asset rather than restated here, so
// this test reads what actually ships.
func armedOpsInComponentView(t *testing.T) map[string]bool {
	t.Helper()
	src, err := webFS.ReadFile("web/js/views/component.js")
	if err != nil {
		t.Fatalf("read views/component.js: %v", err)
	}
	const marker = "const armedOps = new Set(["
	i := strings.Index(string(src), marker)
	if i < 0 {
		t.Fatalf("views/component.js declares no armedOps set; every control button now fires on one click")
	}
	rest := string(src)[i+len(marker):]
	j := strings.Index(rest, "]")
	if j < 0 {
		t.Fatalf("armedOps literal is unterminated")
	}
	out := map[string]bool{}
	for _, tok := range strings.Split(rest[:j], ",") {
		if op := strings.Trim(strings.TrimSpace(tok), `"`); op != "" {
			out[op] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("armedOps parsed empty from %q — the extractor is broken, not the asset", rest[:j])
	}
	return out
}

// TestWeaverControlButtonsArmBeforeTheyFire pins the confirm step on the Weaver
// row's destructive buttons, and derives WHICH ops need it from the server's own
// allow-list rather than a second hand-kept copy — so a weaver op added to
// mutateOps without arming its button fails here rather than shipping as a
// one-click whole-target action.
//
// The Weaver row renders its ops side by side next to an enable/disable toggle an
// operator clicks routinely. A misclick one button off that toggle would
// otherwise re-dispatch a whole target's violating backlog (replayTarget), delete
// its confidence windows (resetConfidence) or tear it down (revoke) outright.
//
// The toggle itself is deliberately exempt and asserted as such: it is the
// reversible op — its own opposite undoes it — and arming every button in the row
// trains an operator to double-click all of them, which is what defeats arming on
// the ones that matter.
func TestWeaverControlButtonsArmBeforeTheyFire(t *testing.T) {
	armed := armedOpsInComponentView(t)
	const toggleA, toggleB = "disable", "enable"

	weaver, ok := controlComponents["weaver"]
	if !ok {
		t.Fatal("no weaver control component")
	}
	for op := range weaver.mutateOps {
		if op == toggleA || op == toggleB {
			if armed[op] {
				t.Errorf("%q is the reversible toggle and must NOT arm — arming every button in the row "+
					"trains a double-click habit that defeats arming on the destructive ones", op)
			}
			continue
		}
		if !armed[op] {
			t.Errorf("weaver op %q is a one-click button: it is allow-listed for the console but absent from "+
				"armedOps, so a misclick beside the enable/disable toggle fires it outright", op)
		}
	}
}
