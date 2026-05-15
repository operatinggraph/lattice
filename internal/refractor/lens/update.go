package lens

// UpdateKind classifies a rule update as either an INTO-only reconfiguration
// (no rebuild required) or a MATCH change (rebuild required).
type UpdateKind uint8

const (
	// IntoOnly means only the INTO clause changed — the durable consumer continues
	// without interruption; only the adapter needs to be hot-reloaded (FR4).
	IntoOnly UpdateKind = iota
	// MatchChange means the MATCH clause changed — a full rebuild is required before
	// the target state is correct (FR5).
	MatchChange
)

// ClassifyUpdate compares two versions of the same rule and returns the update kind.
// If the Match clause is identical (case-sensitive string comparison), the update is
// IntoOnly; otherwise it is MatchChange.
// Panics if either argument is nil.
func ClassifyUpdate(old, new *Rule) UpdateKind {
	if old == nil || new == nil {
		panic("rule: ClassifyUpdate called with nil Rule")
	}
	if old.Match == new.Match {
		return IntoOnly
	}
	return MatchChange
}
