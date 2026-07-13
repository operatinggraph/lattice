package loom

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPatternValidate_AcceptsSystemOpAndUserTask_RejectsGuardsAndUnknownKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		pattern Pattern
		wantErr bool
	}{
		{
			name:    "valid two-systemOp pattern",
			pattern: Pattern{PatternID: "p1", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "SetName"}, {Kind: "systemOp", Operation: "SetPhone"}}},
			wantErr: false,
		},
		{
			name:    "userTask accepted",
			pattern: Pattern{PatternID: "p2", SubjectType: "identity", Steps: []Step{{Kind: "userTask", Operation: "SetName"}}},
			wantErr: false,
		},
		{
			name:    "userTask without operation rejected",
			pattern: Pattern{PatternID: "p2b", SubjectType: "identity", Steps: []Step{{Kind: "userTask", Operation: ""}}},
			wantErr: true,
		},
		{
			name:    "unknown kind rejected",
			pattern: Pattern{PatternID: "p2c", SubjectType: "identity", Steps: []Step{{Kind: "decision", Operation: "X"}}},
			wantErr: true,
		},
		{
			name:    "externalTask with adapter/instanceOp/replyOp accepted",
			pattern: Pattern{PatternID: "px1", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", InstanceOp: "CreateCharge", ReplyOp: "ResolveCharge"}}},
			wantErr: false,
		},
		{
			name:    "externalTask with params accepted",
			pattern: Pattern{PatternID: "px1b", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", InstanceOp: "CreateCharge", ReplyOp: "ResolveCharge", Params: json.RawMessage(`{"amount":100}`)}}},
			wantErr: false,
		},
		{
			name:    "externalTask without params accepted",
			pattern: Pattern{PatternID: "px1c", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", InstanceOp: "CreateCharge", ReplyOp: "ResolveCharge"}}},
			wantErr: false,
		},
		{
			name:    "externalTask missing adapter rejected",
			pattern: Pattern{PatternID: "px2", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", InstanceOp: "CreateCharge", ReplyOp: "ResolveCharge"}}},
			wantErr: true,
		},
		{
			name:    "externalTask missing instanceOp rejected",
			pattern: Pattern{PatternID: "px3", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", ReplyOp: "ResolveCharge"}}},
			wantErr: true,
		},
		{
			name:    "externalTask missing replyOp rejected",
			pattern: Pattern{PatternID: "px4", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", InstanceOp: "CreateCharge"}}},
			wantErr: true,
		},
		{
			name:    "systemOp with stray instanceOp rejected",
			pattern: Pattern{PatternID: "px7", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "SetName", InstanceOp: "CreateCharge"}}},
			wantErr: true,
		},
		{
			name:    "userTask with stray adapter rejected",
			pattern: Pattern{PatternID: "px8", SubjectType: "identity", Steps: []Step{{Kind: "userTask", Operation: "SetName", Adapter: "stripe"}}},
			wantErr: true,
		},
		{
			name:    "externalTask with stray operation rejected",
			pattern: Pattern{PatternID: "px9", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", InstanceOp: "CreateCharge", ReplyOp: "ResolveCharge", Operation: "SetName"}}},
			wantErr: true,
		},
		{
			name:    "guarded externalTask accepted",
			pattern: Pattern{PatternID: "px5", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", InstanceOp: "CreateCharge", ReplyOp: "ResolveCharge", Guard: json.RawMessage(`{"absent":"subject.data.charged"}`)}}},
			wantErr: false,
		},
		{
			name:    "externalTask with bad guard rejected",
			pattern: Pattern{PatternID: "px6", SubjectType: "identity", Steps: []Step{{Kind: "externalTask", Adapter: "stripe", InstanceOp: "CreateCharge", ReplyOp: "ResolveCharge", Guard: json.RawMessage(`{"exists":"subject.data.charged"}`)}}},
			wantErr: true,
		},
		{
			name:    "valid guarded systemOp accepted",
			pattern: Pattern{PatternID: "p3", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "SetName", Guard: json.RawMessage(`{"absent":"subject.data.name"}`)}}},
			wantErr: false,
		},
		{
			name:    "valid guarded userTask accepted",
			pattern: Pattern{PatternID: "p3b", SubjectType: "identity", Steps: []Step{{Kind: "userTask", Operation: "SetName", Guard: json.RawMessage(`{"absent":"subject.profile.data.name"}`)}}},
			wantErr: false,
		},
		{
			name:    "guard with unknown top-level key rejected",
			pattern: Pattern{PatternID: "p3c", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "X", Guard: json.RawMessage(`{"exists":"subject.data.name"}`)}}},
			wantErr: true,
		},
		{
			name:    "multi-key guard object rejected",
			pattern: Pattern{PatternID: "p3d", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "X", Guard: json.RawMessage(`{"absent":"subject.data.a","present":"subject.data.b"}`)}}},
			wantErr: true,
		},
		{
			name:    "empty allOf rejected",
			pattern: Pattern{PatternID: "p3e", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "X", Guard: json.RawMessage(`{"allOf":[]}`)}}},
			wantErr: true,
		},
		{
			name:    "bad path shape rejected",
			pattern: Pattern{PatternID: "p3f", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "X", Guard: json.RawMessage(`{"absent":"subject.profile.name"}`)}}},
			wantErr: true,
		},
		{
			name:    "well-formed starlark guard accepted",
			pattern: Pattern{PatternID: "p3g", SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "X", Guard: json.RawMessage(`{"reads":["profile"],"starlark":"def guard(subject): return True"}`)}}},
			wantErr: false,
		},
		{
			name:    "empty subjectType rejected",
			pattern: Pattern{PatternID: "p4", Steps: []Step{{Kind: "systemOp", Operation: "SetName"}}},
			wantErr: true,
		},
		{
			name:    "no steps rejected",
			pattern: Pattern{PatternID: "p5", SubjectType: "identity"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.pattern.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestPatternValidate_StarlarkGuardCompilesAtLoad asserts a well-formed
// {reads, starlark} guard is ACCEPTED by pattern validate() — it compiles at
// pattern-load time (§10.5, guard.go's parseStarlarkGuard) rather than being
// rejected as reserved.
func TestPatternValidate_StarlarkGuardCompilesAtLoad(t *testing.T) {
	t.Parallel()
	p := Pattern{PatternID: "ps", SubjectType: "identity", Steps: []Step{
		{Kind: "systemOp", Operation: "X", Guard: json.RawMessage(`{"reads":["profile"],"starlark":"def guard(subject): return True"}`)},
	}}
	if err := p.validate(); err != nil {
		t.Fatalf("validate() with a well-formed starlark guard err=%v, want accept", err)
	}
}

// TestPatternValidate_StarlarkGuardSyntaxErrorRejected asserts a starlark
// guard that fails to compile is rejected at pattern-load time, same doctrine
// as a malformed declarative guard.
func TestPatternValidate_StarlarkGuardSyntaxErrorRejected(t *testing.T) {
	t.Parallel()
	p := Pattern{PatternID: "ps", SubjectType: "identity", Steps: []Step{
		{Kind: "systemOp", Operation: "X", Guard: json.RawMessage(`{"starlark":"def guard(subject)\n    return True"}`)},
	}}
	err := p.validate()
	if err == nil {
		t.Fatalf("validate() accepted a syntax-error starlark guard, want rejection")
	}
	if !errors.Is(err, errMalformedGuard) {
		t.Fatalf("validate() err=%v, want errMalformedGuard", err)
	}
}

func TestPatternDomains_DefaultsToSubjectTypeWhenOmitted(t *testing.T) {
	t.Parallel()
	p := Pattern{SubjectType: "identity"}
	got := p.Domains()
	if len(got) != 1 || got[0] != "identity" {
		t.Fatalf("Domains() with no completionDomains = %v, want [identity]", got)
	}
}

func TestPatternDomains_UsesDeclaredSetVerbatim(t *testing.T) {
	t.Parallel()
	// When completionDomains is present it is used as-is (NOT unioned with
	// subjectType): a cross-domain flow lists exactly the domains it completes on.
	p := Pattern{SubjectType: "identity", CompletionDomains: []string{"org", "org", " "}}
	got := p.Domains()
	if len(got) != 1 || got[0] != "org" {
		t.Fatalf("Domains()=%v, want [org] (declared set, deduped, subjectType not unioned)", got)
	}
}

func TestBindingRegistry_DedupesDomainsAcrossPatterns(t *testing.T) {
	t.Parallel()
	patterns := []*Pattern{
		{SubjectType: "identity", Steps: []Step{{Kind: "systemOp", Operation: "A"}}},
		{SubjectType: "identity", CompletionDomains: []string{"org"}},
		{SubjectType: "lease"},
	}
	got := bindingRegistry(patterns)
	for _, d := range []string{"identity", "org", "lease"} {
		if _, ok := got[d]; !ok {
			t.Fatalf("expected domain %q in registry %v", d, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("registry should dedupe to 3 domains, got %d: %v", len(got), got)
	}
}

func TestUserTaskCompletionUnobservable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    Pattern
		want bool
	}{
		{
			name: "userTask omitting orchestration domain is unobservable",
			p: Pattern{SubjectType: "identity", CompletionDomains: []string{"identity"},
				Steps: []Step{{Kind: StepKindUserTask, Operation: "SetName"}}},
			want: true,
		},
		{
			name: "userTask defaulting to subjectType (no orchestration domain) is unobservable",
			p: Pattern{SubjectType: "identity",
				Steps: []Step{{Kind: StepKindUserTask, Operation: "SetName"}}},
			want: true,
		},
		{
			name: "userTask listing orchestration domain is observable",
			p: Pattern{SubjectType: "identity", CompletionDomains: []string{"orchestration"},
				Steps: []Step{{Kind: StepKindUserTask, Operation: "SetName"}}},
			want: false,
		},
		{
			name: "systemOp-only pattern is never flagged",
			p: Pattern{SubjectType: "identity", CompletionDomains: []string{"identity"},
				Steps: []Step{{Kind: StepKindSystemOp, Operation: "StepA"}}},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.userTaskCompletionUnobservable(); got != c.want {
				t.Fatalf("userTaskCompletionUnobservable()=%v, want %v", got, c.want)
			}
		})
	}
}

func TestHasExternalTaskStep(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    Pattern
		want bool
	}{
		{
			name: "pattern with an externalTask step",
			p: Pattern{SubjectType: "widget",
				Steps: []Step{{Kind: StepKindExternalTask, Adapter: "a", InstanceOp: "Create", ReplyOp: "Resolve"}}},
			want: true,
		},
		{
			name: "externalTask among other kinds",
			p: Pattern{SubjectType: "identity",
				Steps: []Step{
					{Kind: StepKindSystemOp, Operation: "StepA"},
					{Kind: StepKindExternalTask, Adapter: "a", InstanceOp: "Create", ReplyOp: "Resolve"},
				}},
			want: true,
		},
		{
			name: "no externalTask step",
			p: Pattern{SubjectType: "identity",
				Steps: []Step{
					{Kind: StepKindSystemOp, Operation: "StepA"},
					{Kind: StepKindUserTask, Operation: "SetName"},
				}},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.hasExternalTaskStep(); got != c.want {
				t.Fatalf("hasExternalTaskStep()=%v, want %v", got, c.want)
			}
		})
	}
}

func TestExternalTaskCompletionUnobservable(t *testing.T) {
	t.Parallel()
	ext := Step{Kind: StepKindExternalTask, Adapter: "a", InstanceOp: "Create", ReplyOp: "Resolve"}
	cases := []struct {
		name string
		p    Pattern
		want bool
	}{
		{
			name: "externalTask omitting orchestration domain is unobservable",
			p:    Pattern{SubjectType: "widget", CompletionDomains: []string{"widget"}, Steps: []Step{ext}},
			want: true,
		},
		{
			name: "externalTask defaulting to subjectType (no orchestration domain) is unobservable",
			p:    Pattern{SubjectType: "widget", Steps: []Step{ext}},
			want: true,
		},
		{
			name: "externalTask listing orchestration domain is observable",
			p:    Pattern{SubjectType: "widget", CompletionDomains: []string{"orchestration"}, Steps: []Step{ext}},
			want: false,
		},
		{
			name: "systemOp-only pattern is never flagged",
			p: Pattern{SubjectType: "identity", CompletionDomains: []string{"identity"},
				Steps: []Step{{Kind: StepKindSystemOp, Operation: "StepA"}}},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.externalTaskCompletionUnobservable(); got != c.want {
				t.Fatalf("externalTaskCompletionUnobservable()=%v, want %v", got, c.want)
			}
		})
	}
}

func TestPatternIDFromRef(t *testing.T) {
	t.Parallel()
	if got := patternIDFromRef("vtx.meta.abc"); got != "abc" {
		t.Fatalf("patternIDFromRef(vtx.meta.abc)=%q, want abc", got)
	}
	if got := patternIDFromRef("abc"); got != "abc" {
		t.Fatalf("patternIDFromRef(abc)=%q, want abc", got)
	}
}
