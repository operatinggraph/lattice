package main

import (
	"fmt"
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
		mutateOps: setOf("inspect", "pause", "resume"),
	},
	"weaver": {
		subjectPrefix: "lattice.ctrl.weaver",
		reads: map[string]string{
			"list": "lattice.ctrl.weaver.list",
		},
		mutateOps: setOf("disable", "enable", "revoke"),
	},
	// Refractor serves only per-lens subjects (no fixed component-wide list);
	// the UI discovers lens ids through the Health tab. The op set mirrors the
	// Refractor control plane's actual supportedOps — note it exposes "health"
	// (the per-lens inspect read) and "delete", not an "inspect" op.
	"refractor": {
		subjectPrefix: "lattice.ctrl.refractor",
		reads:         map[string]string{},
		mutateOps:     setOf("health", "validate", "rebuild", "pause", "resume", "delete"),
	},
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
