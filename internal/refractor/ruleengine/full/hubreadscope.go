package full

import (
	"fmt"
	"sync/atomic"
)

// HubReadScopeMode is the posture deciding whether a typed relationship hop
// may read an overflow-marked node at the hop's own relation instead of
// draining the node's whole link keyspace out of Core KV
// (refractor-hub-walk-and-periodic-load-design.md §9.1).
type HubReadScopeMode int

const (
	// HubReadScopeModeUnset means "take the package default", and is the zero
	// value deliberately: an engine carries the mode as a plain field whose
	// unset state is zero, so zero must mean unset rather than a real mode.
	HubReadScopeModeUnset HubReadScopeMode = iota
	HubReadScopeModeOff
	HubReadScopeModeOn
)

func (m HubReadScopeMode) String() string {
	switch m {
	case HubReadScopeModeOff:
		return "off"
	case HubReadScopeModeOn:
		return "on"
	default:
		return "unset"
	}
}

// ParseHubReadScopeMode maps an operator-supplied string onto a mode,
// rejecting rather than guessing — a typo resolving silently to `off` would
// put every typed hop back on the whole-hub drain with nothing saying so.
func ParseHubReadScopeMode(s string) (HubReadScopeMode, error) {
	switch s {
	case "on":
		return HubReadScopeModeOn, nil
	case "off":
		return HubReadScopeModeOff, nil
	default:
		return HubReadScopeModeUnset, fmt.Errorf("full engine: unknown hub read-scope mode %q (want on or off)", s)
	}
}

// defaultHubReadScopeMode is the process-wide posture every engine without its
// own override uses. Package-level for the same reason the pipeline's walk
// scope is: the operator decision is one per process (cmd/refractor reads
// REFRACTOR_HUB_READ_SCOPE once) while engines are constructed wherever a
// pipeline is built, and threading a startup flag through every construction
// site makes it possible to miss one.
//
// LIFETIME: written once at boot and by tests; read per evaluation, at
// executor construction. It is an operator posture, not evaluation state, so
// it is deliberately NOT reset or re-derived at rebuild, replay, reconnect,
// tombstone or rule hot-reload — a rule swap silently re-arming a narrowing an
// operator had turned off is the failure this placement avoids. It does not
// survive the process, which is correct: the env var is re-read at the next
// boot.
var defaultHubReadScopeMode atomic.Int64

// SetDefaultHubReadScopeMode sets the posture every engine without its own
// override uses. HubReadScopeModeUnset restores the built-in.
func SetDefaultHubReadScopeMode(m HubReadScopeMode) { defaultHubReadScopeMode.Store(int64(m)) }

// DefaultHubReadScopeMode reports that posture resolved to a real mode rather
// than to Unset, so a host can state at boot which behaviour it runs.
func DefaultHubReadScopeMode() HubReadScopeMode {
	if m := HubReadScopeMode(defaultHubReadScopeMode.Load()); m != HubReadScopeModeUnset {
		return m
	}
	return HubReadScopeModeOn
}

// hubReadScopeEnabled resolves this engine's posture: its own override when it
// carries one, the package default otherwise.
func (e *Engine) hubReadScopeEnabled() bool {
	if e.hubReadScope != HubReadScopeModeUnset {
		return e.hubReadScope == HubReadScopeModeOn
	}
	return DefaultHubReadScopeMode() == HubReadScopeModeOn
}
