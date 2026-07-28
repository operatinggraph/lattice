package projection

import (
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// EmptyBehavior names how an actor-aggregate plan handles an empty projection
// result (no real rows for the actor after the realness filter).
type EmptyBehavior string

const (
	// EmptyDelete hard-deletes the actor's output key (the built-in default).
	EmptyDelete EmptyBehavior = "delete"
	// EmptySoftDelete writes a soft tombstone via the §6.2 guard mechanism,
	// reusing the natskv adapter's guarded-delete path (one mechanism, not two).
	EmptySoftDelete EmptyBehavior = "softDelete"
	// EmptyDoc writes an empty document for the actor (key stays present).
	EmptyDoc EmptyBehavior = "emptyDoc"
	// EmptySkip leaves any existing key untouched (declines the row).
	EmptySkip EmptyBehavior = "skip"
)

// ActorSuffixPlaceholder is the only key-template placeholder the constrained
// outputKeyPattern accepts: the actor key with the "vtx." prefix stripped.
const ActorSuffixPlaceholder = "{actorSuffix}"

// DefaultActorField is the top-level envelope field carrying the actor vertex
// key when the descriptor does not override it (the cap.* documents).
const DefaultActorField = "actor"

// FreshnessAuto stamps projectionSeq (§6.2 guard) plus the widened
// projectedFromRevisions (§6.3) on each write.
const FreshnessAuto = "auto"

// OutputDescriptor is the validated, compile-time representation of the §6.13
// Output descriptor plus the envelope-shape options that let the driver emit a
// document byte-identical to each built-in lens's on-wire shape.
type OutputDescriptor struct {
	AnchorType       string
	OutputKeyPattern string
	BodyColumns      []string
	EmptyBehavior    EmptyBehavior
	RealnessFilter   string
	Freshness        string

	// KeyColumn opts the descriptor into the §10.2 Option (b) row-key shape. When
	// non-empty, BuildKey substitutes the anchor's bare-NanoID <entityId> (derived
	// from actorKey) into the {actorSuffix} slot instead of the default
	// <type>.<id> suffix, so a convergence row key stays <targetId>.<entityId>
	// (bare NanoID) and Weaver's splitRowKey accepts it unchanged. Empty leaves
	// the default suffix path byte-for-byte intact.
	KeyColumn string

	// ActorField is the top-level field carrying the actor vertex key
	// ("actor" by default; "assignee" for the my-tasks document).
	ActorField string
	// Lanes, when non-empty, is emitted as the document's `lanes` array.
	Lanes []string
	// StaticEmptyColumns are body columns always materialized as an empty array.
	StaticEmptyColumns []string

	// EntryKeyColumn opts the descriptor into per-entry key emission (§3.3 of
	// cap-read-per-anchor-grant-keys-design.md): the sole BodyColumns list
	// entry's EntryKeyColumn field keys one guarded key per real entry, instead
	// of one document per actor. Empty leaves the byte-identical one-document
	// path (EnvelopeFn) untouched — this field is carried by ParseOutputDescriptor
	// but has no driver-side consumer yet (perEntry emission ships in the design's
	// next Fire 1 increment).
	EntryKeyColumn string
}

// ParseOutputDescriptor validates a raw OutputDescriptorSpec and returns the
// compile-time descriptor. It enforces the §6.13 constraints:
//   - anchorType required;
//   - outputKeyPattern uses only the closed placeholder set ({actorSuffix});
//   - bodyColumns non-empty;
//   - emptyBehavior ∈ {delete, softDelete, emptyDoc, skip};
//   - realnessFilter, when set, names a field;
//   - freshness, when set, is "auto";
//   - entryKeyColumn, when set, is non-blank and bodyColumns names exactly one
//     column (§3.3).
func ParseOutputDescriptor(spec *lens.OutputDescriptorSpec) (OutputDescriptor, error) {
	if spec == nil {
		return OutputDescriptor{}, fmt.Errorf("output descriptor: missing (actorAggregate lens requires an output descriptor)")
	}

	if strings.TrimSpace(spec.AnchorType) == "" {
		return OutputDescriptor{}, fmt.Errorf("output descriptor: anchorType is required")
	}

	if err := validateKeyPattern(spec.OutputKeyPattern); err != nil {
		return OutputDescriptor{}, err
	}

	if len(spec.BodyColumns) == 0 {
		return OutputDescriptor{}, fmt.Errorf("output descriptor: bodyColumns must list at least one RETURN alias")
	}
	for i, c := range spec.BodyColumns {
		if strings.TrimSpace(c) == "" {
			return OutputDescriptor{}, fmt.Errorf("output descriptor: bodyColumns[%d] must not be empty", i)
		}
	}

	eb, err := parseEmptyBehavior(spec.EmptyBehavior)
	if err != nil {
		return OutputDescriptor{}, err
	}

	if spec.Freshness != "" && spec.Freshness != FreshnessAuto {
		return OutputDescriptor{}, fmt.Errorf("output descriptor: freshness must be %q or empty, got %q", FreshnessAuto, spec.Freshness)
	}

	// keyColumn is an opt-in marker (§10.2 Option b): when present it directs
	// BuildKey to emit the anchor's bare-NanoID <entityId> into the {actorSuffix}
	// slot. Its value is the anchor's id derived from actorKey, not a RETURN
	// alias, so it is NOT required to name a bodyColumns member — a present but
	// blank value is the only malformed case and is rejected fail-closed.
	if spec.KeyColumn != "" && strings.TrimSpace(spec.KeyColumn) == "" {
		return OutputDescriptor{}, fmt.Errorf("output descriptor: keyColumn, when set, must not be blank")
	}

	// entryKeyColumn (§3.3): present-but-blank is fail-closed exactly like
	// keyColumn above. When set, bodyColumns must name exactly the one list
	// column being split into per-entry keys — there is no shape for splitting
	// more than one list per actor evaluation. (A zero-column spec is already
	// rejected above, before this check runs.)
	if spec.EntryKeyColumn != "" {
		if strings.TrimSpace(spec.EntryKeyColumn) == "" {
			return OutputDescriptor{}, fmt.Errorf("output descriptor: entryKeyColumn, when set, must not be blank")
		}
		if len(spec.BodyColumns) != 1 {
			return OutputDescriptor{}, fmt.Errorf("output descriptor: entryKeyColumn requires exactly one bodyColumns entry (the list it splits), got %d", len(spec.BodyColumns))
		}
	}

	actorField := spec.ActorField
	if strings.TrimSpace(actorField) == "" {
		actorField = DefaultActorField
	}

	return OutputDescriptor{
		AnchorType:         spec.AnchorType,
		OutputKeyPattern:   spec.OutputKeyPattern,
		BodyColumns:        append([]string(nil), spec.BodyColumns...),
		EmptyBehavior:      eb,
		RealnessFilter:     spec.RealnessFilter,
		Freshness:          spec.Freshness,
		KeyColumn:          spec.KeyColumn,
		ActorField:         actorField,
		Lanes:              append([]string(nil), spec.Lanes...),
		StaticEmptyColumns: append([]string(nil), spec.StaticEmptyColumns...),
		EntryKeyColumn:     strings.TrimSpace(spec.EntryKeyColumn),
	}, nil
}

func parseEmptyBehavior(s string) (EmptyBehavior, error) {
	switch EmptyBehavior(s) {
	case EmptyDelete, EmptySoftDelete, EmptyDoc, EmptySkip:
		return EmptyBehavior(s), nil
	default:
		return "", fmt.Errorf("output descriptor: emptyBehavior must be one of delete|softDelete|emptyDoc|skip, got %q", s)
	}
}

// validateKeyPattern enforces the constrained-placeholder rule: the only
// allowed placeholder is {actorSuffix}. Any other "{...}" run is rejected so a
// typo or an unsupported template can never silently produce a wrong key.
func validateKeyPattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("output descriptor: outputKeyPattern is required")
	}
	rest := pattern
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			break
		}
		closeRel := strings.IndexByte(rest[open:], '}')
		if closeRel < 0 {
			return fmt.Errorf("output descriptor: outputKeyPattern %q has an unterminated placeholder", pattern)
		}
		placeholder := rest[open : open+closeRel+1]
		if placeholder != ActorSuffixPlaceholder {
			return fmt.Errorf("output descriptor: outputKeyPattern %q uses unknown placeholder %q (only %q is allowed)",
				pattern, placeholder, ActorSuffixPlaceholder)
		}
		rest = rest[open+closeRel+1:]
	}
	if !strings.Contains(pattern, ActorSuffixPlaceholder) {
		return fmt.Errorf("output descriptor: outputKeyPattern %q must contain %q", pattern, ActorSuffixPlaceholder)
	}
	return nil
}

// BuildKey renders the constrained outputKeyPattern for one actor key by
// substituting {actorSuffix}. The default suffix is the actor key minus its
// "vtx." prefix (<type>.<id>). When KeyColumn is set (§10.2 Option b), the suffix
// is instead the anchor's bare-NanoID <entityId> derived from actorKey, so a
// convergence row key stays <targetId>.<entityId> (bare NanoID) and Weaver's
// splitRowKey accepts it unchanged.
//
// The value is anchor-derived, not row-sourced, so BuildKey computes the same key
// on both call sites: the project path (driver EnvelopeFn, has the row) and the
// actor-disappearance delete path (SetActorDeleteKey → pipeline, has only
// actorKey, no row). A row-sourced key would diverge on the delete path and leave
// a stale row un-retracted.
func (d OutputDescriptor) BuildKey(actorKey string) string {
	suffix := actorKey
	if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
		suffix = rest
	}
	if d.KeyColumn != "" {
		if _, id, ok := substrate.ParseVertexKey(actorKey); ok {
			// ParseVertexKey enforces IsValidNanoID on the id segment, so the
			// emitted <entityId> is structurally a bare NanoID (no dots) —
			// splitRowKey's single-dot split + NanoID check accept it.
			suffix = id
		}
		// A non-vertex actorKey falls through to the vtx-stripped suffix. The
		// EnvelopeFn rejects a non-vertex actorKey before BuildKey on the project
		// path, so this guards only the delete path's already-validated key.
	}
	return strings.ReplaceAll(d.OutputKeyPattern, ActorSuffixPlaceholder, suffix)
}

// patternParts splits the output key pattern around the actor-suffix
// placeholder into the literal text that brackets it. Reports false for a
// pattern carrying no placeholder, which renders one fixed key for every anchor
// and so has no per-anchor inverse.
func (d OutputDescriptor) patternParts() (prefix, suffix string, ok bool) {
	idx := strings.Index(d.OutputKeyPattern, ActorSuffixPlaceholder)
	if idx < 0 {
		return "", "", false
	}
	return d.OutputKeyPattern[:idx], d.OutputKeyPattern[idx+len(ActorSuffixPlaceholder):], true
}

// KeyPrefix returns the prefix every key this descriptor builds starts with,
// for a caller enumerating the lens's own rows out of a target several lenses
// share. It reports false unless the prefix is usable as a NATS subject filter:
// non-empty, and ending on a "." segment boundary, since the filter it becomes
// is prefix + ">".
//
// A NATS wildcard token in the prefix is refused: `*.{actorSuffix}` would scope
// to every 2-token key in a shared target — the whole-bucket enumeration the
// scoping exists to prevent, reached through the mechanism meant to prevent it.
//
// This SCOPES a listing; it does not decide ownership. The prefix is the same
// literal AnchorFromKey matches first, so a scoped listing can only omit keys
// AnchorFromKey would have rejected — but it can also admit a sibling's (cap.
// is a prefix of cap.roles.), so the exact test still has to run on what comes
// back.
func (d OutputDescriptor) KeyPrefix() (string, bool) {
	prefix, _, ok := d.patternParts()
	if !ok || prefix == "" {
		return "", false
	}
	if !strings.HasSuffix(prefix, ".") {
		return "", false
	}
	if strings.ContainsAny(prefix, "*> ") {
		return "", false
	}
	return prefix, true
}

// SweepAnchorSample is the synthetic anchor key the round-trip probe below is
// run against. It is a well-formed Contract #1 vertex key of the descriptor's
// own anchor type, carrying a NanoID from the platform alphabet.
func (d OutputDescriptor) sweepAnchorSample() string {
	return substrate.VertexPrefix + "." + d.AnchorType + ".ZwqPmRtw9nbCxz5vQ2yH"
}

// KeyOwnershipRoundTrips reports whether AnchorFromKey recovers exactly the
// anchor BuildKey rendered a key for. The convergence sweep's orphan direction
// — the ONLY detector for a row whose anchor is gone, since the deep verify
// walks anchors and an orphan's anchor is by definition not among them — is a
// no-op for a descriptor whose two halves disagree, and it fails silently:
// claiming nothing looks exactly like having nothing to claim.
//
// The pattern grammar permits shapes where they do disagree (the placeholder
// may appear more than once; BuildKey substitutes every occurrence while the
// inverse brackets the first), so the sweep's install gate probes the pair
// rather than trusting that a derivable prefix implies a working inverse.
func (d OutputDescriptor) KeyOwnershipRoundTrips() bool {
	sample := d.sweepAnchorSample()
	recovered, ok := d.AnchorFromKey(d.BuildKey(sample))
	return ok && recovered == sample
}

// AnchorFromKey is the inverse of BuildKey: it recovers the anchor vertex key a
// target key was built for, reporting false for any key this descriptor's
// pattern did not produce. The convergence sweep
// (capability-projection-reconciliation-design.md §3.2) needs it for the orphan
// direction — a target key whose anchor no longer exists — and one NATS-KV
// bucket is shared by every lens targeting it, so the ownership test has to be
// exact rather than a prefix guess.
//
// Exactness comes from three checks: the pattern's literal prefix and suffix
// must both match, and the recovered key must parse as a Contract #1 vertex key
// OF THIS DESCRIPTOR'S AnchorType. A sibling lens's key sharing a literal
// prefix (cap.{actorSuffix} against cap.roles.identity.<id>) recovers a
// four-segment key ParseVertexKey rejects, so neither lens claims the other's
// rows.
func (d OutputDescriptor) AnchorFromKey(targetKey string) (string, bool) {
	prefix, suffix, ok := d.patternParts()
	if !ok {
		return "", false
	}

	rest, ok := strings.CutPrefix(targetKey, prefix)
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, suffix)
	if !ok {
		return "", false
	}

	actorKey := substrate.VertexPrefix + "." + rest
	if d.KeyColumn != "" {
		// §10.2 Option (b): the suffix is the anchor's bare NanoID, so the type
		// segment comes back from the descriptor.
		actorKey = substrate.VertexPrefix + "." + d.AnchorType + "." + rest
	}
	vtxType, _, parsed := substrate.ParseVertexKey(actorKey)
	if !parsed || vtxType != d.AnchorType {
		return "", false
	}
	return actorKey, true
}

// RealnessFiltered returns the subset of a collect array whose entries are
// real. It drops the degenerate null-key collect artifacts an OPTIONAL-match
// cypher produces for an actor with no real rows. When no realnessFilter is
// configured the input is returned unchanged.
//
// A single OutputDescriptor's RealnessFilter names one field, but its
// BodyColumns can collect two different entry shapes from the same query — a
// roster column collects maps (e.g. platformPermissions: {operationType,
// scope, lanes}), while a plain relationship column collects bare keys (e.g.
// roles: role.key). A map entry is checked at the named field; a non-map
// (scalar) entry is checked directly, since there is no field to project into
// — a bare degenerate null is unreal, a bare real key is real.
//
// "Real" is defined by isRealField: a non-empty string keeps the entry; a
// missing field or an empty/whitespace string drops it (the degenerate
// null-taskKey collect artifact). A non-string value is NOT silently dropped
// — that would over-revoke (zero an actor's whole projection on a type the
// cypher never intends). A present non-string value is treated as real and
// kept.
func (d OutputDescriptor) RealnessFiltered(collect any) []any {
	list, ok := collect.([]any)
	if !ok {
		return nil
	}
	if d.RealnessFilter == "" {
		return list
	}
	out := make([]any, 0, len(list))
	for _, e := range list {
		if m, isMap := e.(map[string]any); isMap {
			if isRealField(m[d.RealnessFilter]) {
				out = append(out, e)
			}
			continue
		}
		if isRealField(e) {
			out = append(out, e)
		}
	}
	return out
}

// isRealField reports whether a realness-field value marks its entry as real. A
// non-empty string is real; nil / missing / empty / whitespace-only string is
// not. Any other present (non-nil) value is treated as real rather than dropped:
// the realness filter must never silently zero a projection on an unexpected
// field type — that is over-revocation, not a realness signal.
func isRealField(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	default:
		return true
	}
}
