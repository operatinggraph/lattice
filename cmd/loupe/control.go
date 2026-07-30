package main

import (
	"fmt"
	"sort"
	"strings"
)

// controlComponent describes one orchestration component's control plane as
// Loupe exposes it: the read subjects the UI may GET and the per-name mutate
// ops the UI may POST. Loupe is a thin proxy — it forwards the component's raw
// JSON reply to the browser and never decodes into the component's typed
// control structs, so this map (subjects + op allow-list) is the entire
// contract Loupe holds with each plane.
type controlComponent struct {
	// subjectPrefix is the NATS control-plane root, e.g. "lattice.ctrl.loom".
	subjectPrefix string
	// reads maps a read name (the UI's list button) to a fixed control subject.
	reads map[string]string
	// mutateOps is the set of per-name operations the UI may invoke. A mutate
	// subject is built as "<subjectPrefix>.<name>.<op>".
	mutateOps map[string]struct{}
	// readOnlyOps is the subset of mutateOps that only inspects. The control
	// planes tunnel a few pure reads through the same POST shape, so the demo
	// posture's method rule would otherwise refuse them along with the real
	// mutations; this is the classification that lets exactly those through
	// (demoControlReadAllowed).
	//
	// Omission DENIES: an op absent here is a mutate as far as the demo gate is
	// concerned, so a plane that grows an op stays closed until someone
	// deliberately classifies it. Two tests pin the set — readOnlyOps ⊆
	// mutateOps, and an exact expected set — so widening it is a visible act.
	readOnlyOps map[string]struct{}
}

// controlComponents is the hardcoded per-component map of allowed read-subjects
// and mutate-ops. Subjects mirror the canonical builders in
// internal/{loom,weaver}/control and internal/refractor/control; the ops mirror
// each plane's supported set. Anything outside this map is rejected before a
// subject is built (defense against subject injection through the UI).
var controlComponents = map[string]controlComponent{
	"loom": {
		subjectPrefix: "lattice.ctrl.loom",
		reads: map[string]string{
			"list":      "lattice.ctrl.loom.list",
			"consumers": "lattice.ctrl.loom.consumers",
		},
		mutateOps:   setOf("inspect", "pause", "resume", "redrive"),
		readOnlyOps: setOf("inspect"),
	},
	"weaver": {
		subjectPrefix: "lattice.ctrl.weaver",
		reads: map[string]string{
			"list": "lattice.ctrl.weaver.list",
		},
		// All three weaver ops mutate — no read-only carve-out.
		mutateOps:   setOf("disable", "enable", "revoke"),
		readOnlyOps: setOf(),
	},
	// Refractor serves only per-lens subjects (no fixed component-wide list);
	// the UI discovers lens ids through the Health tab. The op set mirrors the
	// Refractor control plane's actual supportedOps — note it exposes "health"
	// (the per-lens inspect read) and "delete", not an "inspect" op.
	"refractor": {
		subjectPrefix: "lattice.ctrl.refractor",
		reads:         map[string]string{},
		mutateOps:     setOf("health", "validate", "rebuild", "pause", "resume", "delete"),
		readOnlyOps:   setOf("health", "validate"),
	},
}

// demoControlReadAllowed reports whether path is a control-plane POST that only
// inspects, and may therefore pass the demo posture's method default-deny.
//
// It parses with splitNonEmpty — the same helper handleControl routes with — so
// the gate and the handler can never disagree about which op is about to
// execute. It only ever NARROWS: a permitted request still goes through
// mutateSubject's full validation downstream.
func demoControlReadAllowed(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/control/")
	if !ok {
		return false
	}
	parts := splitNonEmpty(rest)
	if len(parts) != 3 {
		return false
	}
	c, ok := controlComponents[parts[0]]
	if !ok {
		return false
	}
	if err := validateControlName(parts[1]); err != nil {
		return false
	}
	_, ok = c.readOnlyOps[parts[2]]
	return ok
}

// controlReadOnlyOps renders the per-component read-only classification for
// /api/demo, so the console hides the control buttons demo mode will refuse
// without duplicating the table in JavaScript. Sorted for a stable response.
func controlReadOnlyOps() map[string][]string {
	out := make(map[string][]string, len(controlComponents))
	for comp, c := range controlComponents {
		ops := make([]string, 0, len(c.readOnlyOps))
		for op := range c.readOnlyOps {
			ops = append(ops, op)
		}
		sort.Strings(ops)
		out[comp] = ops
	}
	return out
}

// splitNonEmpty splits a slash-delimited path tail into its non-empty
// segments, so a trailing slash or a doubled slash does not yield phantom empty
// tokens. Used to route /api/control/<comp>[/<name>/<op>].
func splitNonEmpty(path string) []string {
	out := make([]string, 0, 3)
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func setOf(items ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(items))
	for _, it := range items {
		m[it] = struct{}{}
	}
	return m
}

// readSubjects returns the component's read name → subject map, or false if the
// component is unknown.
func readSubjects(comp string) (map[string]string, bool) {
	c, ok := controlComponents[comp]
	if !ok {
		return nil, false
	}
	return c.reads, true
}

// validateControlName rejects a name that is empty or contains a ".". The
// per-name mutate subject is "<prefix>.<name>.<op>" and each plane subscribes a
// single-token wildcard for <name>, so a dotted or empty name builds a subject
// no endpoint matches — the request would otherwise hang to the client timeout
// with an opaque "no responders". Registered ids (lens/instance/target ids) are
// dot-free single tokens, so this mirrors the server-side shape.
func validateControlName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("name %q must not contain '.' (a control name is a single dot-free token)", name)
	}
	return nil
}

// mutateSubject validates comp/name/op against the per-component allow-list and
// builds the canonical mutate subject "<prefix>.<name>.<op>". Returns an error
// (not a subject) for an unknown component, a malformed name, or an op outside
// the component's allow-list — so an out-of-list op can never reach NATS.
func mutateSubject(comp, name, op string) (string, error) {
	c, ok := controlComponents[comp]
	if !ok {
		return "", fmt.Errorf("unknown control component %q", comp)
	}
	if err := validateControlName(name); err != nil {
		return "", err
	}
	if _, ok := c.mutateOps[op]; !ok {
		return "", fmt.Errorf("operation %q is not allowed for component %q", op, comp)
	}
	return c.subjectPrefix + "." + name + "." + op, nil
}
