package main

import "testing"

func TestMutateSubject(t *testing.T) {
	tests := []struct {
		name     string
		comp     string
		ctlName  string
		op       string
		want     string
		wantErr  bool
	}{
		{name: "loom pause", comp: "loom", ctlName: "loom-widget", op: "pause", want: "lattice.ctrl.loom.loom-widget.pause"},
		{name: "loom inspect", comp: "loom", ctlName: "abc123", op: "inspect", want: "lattice.ctrl.loom.abc123.inspect"},
		{name: "weaver disable", comp: "weaver", ctlName: "t1", op: "disable", want: "lattice.ctrl.weaver.t1.disable"},
		{name: "weaver revoke", comp: "weaver", ctlName: "t1", op: "revoke", want: "lattice.ctrl.weaver.t1.revoke"},
		{name: "refractor rebuild", comp: "refractor", ctlName: "lensA", op: "rebuild", want: "lattice.ctrl.refractor.lensA.rebuild"},
		{name: "refractor validate", comp: "refractor", ctlName: "lensA", op: "validate", want: "lattice.ctrl.refractor.lensA.validate"},

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
