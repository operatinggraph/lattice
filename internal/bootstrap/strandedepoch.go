package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// strandedScanPageSize is the granularity at which a walk hands enumerated keys
// to its visitor and re-checks the context.
//
// It is not a bound on the enumeration: listDistinctKeys collects every
// matching key before paging begins, and the pages are re-slices of that one
// collection. Its only effect is that a long read pass notices an expired
// deadline partway through rather than at the end.
const strandedScanPageSize = 500

// strandedReportKeysShown caps how many keys one report line names before it
// elides the rest by count, so a role with a large degree cannot produce an
// unreadable gate line.
const strandedReportKeysShown = 5

// StrandedOperatorEpoch names one live role whose canonicalName is `operator`
// and whose id this deployment's primordial table does not name.
//
// The usual author is an id-file rotation: regenerating lattice.bootstrap.json
// mints a fresh roleOperator NanoID, the next boot seeds a new role, and the
// previous one is left live in Core KV because the seed path is create-only and
// reconcile classifies every non-vtx.meta.* entry as retained
// (primordial-epoch-stranded-authority-design.md §1). It is not the only
// possible author — rbac-domain's CreateRole takes an arbitrary canonicalName
// (packages/rbac-domain/ddls.go:263-269) — which is why the predicate claims
// only what it proves: an `operator`-named role this table does not name.
//
// Three lenses decide what a finding means, and they read the graph three
// different ways. Any claim here about consequence is grounded in all three:
//
//   - internal/bootstrap/lenses.go's CapabilityReadWildcardGrantsLens
//     (lenses.go:353-366) matches holders of ANY role NAMED `operator` and
//     projects ('*', 'cap-read.root') — installation-wide read, no grant
//     required.
//   - internal/bootstrap/lenses.go's CapabilityLens (lenses.go:135-148) also
//     matches on the role NAME, and returns a fixed LITERAL kernel grant set,
//     so it confers the same thing whichever `operator` role is held.
//   - packages/rbac-domain/lenses.go's capabilityRolesSpec (lenses.go:91-104)
//     matches on NOTHING — no canonicalName filter, no id filter — and walks
//     `(identity)-[:holdsRole]->(role)<-[:grantedBy]-(perm)`, materializing
//     every permission the held role grants into the actor's cap.roles.<actor>
//     document. This is the one that makes a stranded role's GRANTS reachable
//     by whoever holds it, and it is why grants are severity-bearing the moment
//     any holder exists.
type StrandedOperatorEpoch struct {
	// RoleKey is the role's vertex key, vtx.role.<id>.
	RoleKey string
	// Holders holds the sorted vtx.identity.<id> keys of live identities that
	// hold this role and are NOT verified holders of the current operator role.
	//
	// Any entry here is a failure on its own. Through the two name-matching
	// lenses above, such a holder has installation-wide read and the kernel
	// meta-mutation grant set, and this deployment cannot account for it.
	//
	// The keys are reported, never just counted: each one names the holdsRole
	// edge to tombstone, so they are the remedy.
	Holders []string
	// ReachableVia holds the sorted holdsRole link keys by which identities
	// VERIFIED to hold this deployment's current operator role reach this one.
	//
	// Verified means observed in the graph — a live holdsRole edge into the
	// current role, both endpoints live — intersected with the primordial
	// identity set. It is not inferred from the id file: a primordial identity
	// whose current-role edge was revoked is an ordinary identity, and one
	// holding a stranded `operator` role instead would be re-acquiring root
	// through the name-matching lenses, not retaining it.
	//
	// These demote a finding only when the stranded role confers NO live
	// grants. With grants present, capabilityRolesSpec materializes them into
	// the holder's capability document — grants the current role does not
	// carry, by construction — so the finding stays a failure.
	//
	// They are recorded rather than dropped because the edge that produces one
	// is an ordinary link create, which the commit-time kernel guard does not
	// cover (it skips creates, step8_commit.go:728-729, and protectedRootKey
	// returns "" for a link key anyway). A single AssignRole must not be able
	// to switch this check off.
	ReachableVia []string
	// GrantedBy holds the sorted vtx.permission.<id> keys this role still
	// confers through a live grantedBy edge to a live permission vertex.
	//
	// Severity-bearing whenever any holder exists, via capabilityRolesSpec
	// above: those permissions land in the holder's capability document
	// verbatim. Only with zero live holders are they inert — nothing walks a
	// grantedBy edge except through a holder.
	GrantedBy []string
	// UnreadableEdges counts the link or endpoint documents that could not be
	// classified while building the three lists above — a body that would not
	// parse, or a link key that would not.
	//
	// Non-zero means the lists are a LOWER BOUND, and the finding is ranked at
	// least NoAddedAuthority for that reason: an unparseable holdsRole edge
	// might be a holder, and "unknown" ranked as "inert" would be a clean exit
	// status over an unread fact.
	UnreadableEdges int
	// Protected mirrors the role vertex's data.protected, and predicts
	// repairability rather than authority: with it set, rejectProtectedMutations
	// refuses any tombstone of this role's vertex or aspects
	// (step8_commit.go:729-735), so the residue cannot be retired through the
	// Processor and only its links can be revoked. It is not an authorization
	// input — primordial.go records that meaning as retired — and never gates
	// this predicate.
	Protected bool
}

// StrandedSeverity ranks one finding by CONSEQUENCE — what holding this role
// yields that its holders do not already have — rather than by who holds it.
type StrandedSeverity int

const (
	// StrandedSeverityInert is a role no live identity holds, whose edges were
	// all readable. Nothing walks a grantedBy edge except through a holder, so
	// its grants authorize nobody.
	StrandedSeverityInert StrandedSeverity = iota
	// StrandedSeverityNoAddedAuthority is a role that confers nothing beyond
	// what its holders already hold: held only by verified current-operator
	// identities, and carrying no live grants. It is reported, never dropped,
	// so that an ordinary AssignRole cannot silence the check. An
	// unclassifiable edge also lands here — an unread fact is not an all-clear.
	StrandedSeverityNoAddedAuthority
	// StrandedSeverityLiveAuthority is a role that yields authority somebody
	// does not otherwise have: a holder this deployment cannot account for, or
	// any holder at all combined with live grants that capabilityRolesSpec
	// materializes into that holder's capability document.
	StrandedSeverityLiveAuthority
)

// Severity ranks the finding.
//
// It keys on holders AND grants together, because the three lenses split the
// question. The two name-matching lenses make ANY holder of an `operator`-named
// role root-equivalent, which is why an unaccounted-for holder is a failure
// with no grants at all. capabilityRolesSpec matches on nothing and walks
// grantedBy, which is why grants become a failure the moment any holder exists
// — including a verified current-operator one, whose current role by
// construction does not carry the stranded role's accumulated grants.
func (e StrandedOperatorEpoch) Severity() StrandedSeverity {
	switch {
	case len(e.Holders) > 0:
		return StrandedSeverityLiveAuthority
	case len(e.ReachableVia) > 0 && len(e.GrantedBy) > 0:
		return StrandedSeverityLiveAuthority
	case len(e.ReachableVia) > 0, e.UnreadableEdges > 0:
		return StrandedSeverityNoAddedAuthority
	default:
		return StrandedSeverityInert
	}
}

// Report renders the finding as one line for a gate's output, naming the keys
// an operator would act on rather than counting them.
func (e StrandedOperatorEpoch) Report() string {
	const preamble = "STRANDED OPERATOR ROLE: %s is named `operator` but is not this deployment's operator role"
	const remedy = " Remedy: tombstone the holdsRole edge(s) into %s — link mutations are not kernel-protected, so this needs no wipe."
	var b strings.Builder
	switch {
	case len(e.Holders) > 0:
		fmt.Fprintf(&b, preamble+
			", and is held by %d identity(ies) this deployment cannot account for: %s."+
			" Holding an `operator`-NAMED role is root-equivalent on both planes — the wildcard read-grant"+
			" lens gives installation-wide read and the capability lens gives the kernel meta-grant set,"+
			" neither of which reads a grantedBy edge. Live grants (%d): %s."+remedy,
			e.RoleKey, len(e.Holders), summarizeKeys(e.Holders),
			len(e.GrantedBy), summarizeKeys(e.GrantedBy), e.RoleKey)
	case len(e.ReachableVia) > 0 && len(e.GrantedBy) > 0:
		fmt.Fprintf(&b, preamble+
			", and is held by current-operator identities via %s while still conferring %d live grant(s): %s."+
			" rbac-domain's cap.roles lens matches ANY held role and walks grantedBy, so those grants are"+
			" materialized into the holders' capability documents — authority the current operator role"+
			" does not carry."+remedy,
			e.RoleKey, summarizeKeys(e.ReachableVia), len(e.GrantedBy), summarizeKeys(e.GrantedBy), e.RoleKey)
	case len(e.ReachableVia) > 0:
		fmt.Fprintf(&b, preamble+
			", and is held only by verified current-operator identities, via %s, and confers no live grants."+
			" Reported rather than dropped: the holdsRole create that produces this is not kernel-protected,"+
			" so it must not be able to silence the check.",
			e.RoleKey, summarizeKeys(e.ReachableVia))
	case e.UnreadableEdges > 0:
		fmt.Fprintf(&b, preamble+
			", and no holder could be established — but %d edge document(s) would not parse, so this is"+
			" a LOWER BOUND, not an all-clear. Live grants (%d): %s.",
			e.RoleKey, e.UnreadableEdges, len(e.GrantedBy), summarizeKeys(e.GrantedBy))
		return b.String()
	default:
		fmt.Fprintf(&b, preamble+
			", and no live identity holds it. Live grants (%d): %s — unreachable without a holder.",
			e.RoleKey, len(e.GrantedBy), summarizeKeys(e.GrantedBy))
	}
	if e.UnreadableEdges > 0 {
		fmt.Fprintf(&b, " NOTE: %d edge document(s) could not be read, so these counts are a lower bound.",
			e.UnreadableEdges)
	}
	return b.String()
}

// summarizeKeys renders a key list for a one-line report, eliding the tail by
// count past strandedReportKeysShown.
func summarizeKeys(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	if len(keys) <= strandedReportKeysShown {
		return strings.Join(keys, ", ")
	}
	return fmt.Sprintf("%s, +%d more",
		strings.Join(keys[:strandedReportKeysShown], ", "), len(keys)-strandedReportKeysShown)
}

// StrandedOperatorEpochs scans Core KV for roles named `operator` that this
// deployment's primordial table does not name. It is the cross-epoch orphan
// class scanKernelOrphans structurally cannot see: that census lists vtx.meta.>
// and keys on the CURRENT bootstrap op, while every key of a prior epoch is a
// vtx.role/vtx.permission carrying the PRIOR epoch's provenance (§2). Nothing
// here writes.
//
// Every candidate that clears the predicate is REPORTED, and the caller acts on
// its Severity. Nothing is suppressed: a scan that dropped a candidate on the
// strength of one holdsRole edge could be switched off for good by a single
// ordinary AssignRole.
//
// The set of identities that count as accounted-for is read from the GRAPH, not
// from the id file — one listing of live holdsRole edges into the current
// operator role, intersected with the primordial identity set, exactly the
// question SystemActorKeys asks. Built once, and only once a candidate exists,
// so a single-epoch deployment never pays for it.
//
// Reachability is a question about the current epoch, not about holders in
// general. A re-bootstrap on a regenerated id file deletes nothing — the seed
// path is create-only, and reconcile classifies every non-vtx.meta.* entry as
// retained — so the prior epoch's admin and service identities and their
// holdsRole edges into the prior operator role are all still live. The whole
// epoch strands as one island, and its own holders are part of that island
// rather than evidence against it.
//
// Returns ErrPrimordialIDsUnloaded, before reading the graph at all, when the
// primordial identifier table has not been loaded. Both halves of the predicate
// are keyed on that table: an unloaded one (empty string) makes the id filter
// match EVERY role and leaves the accounted-for set empty, so the live kernel
// role would be reported with its own holders counted as strangers — the
// inverse of the truth (§5 dossier). Mirrors SystemActorKeys.
//
// A read that fails for any reason other than "key absent" aborts the scan with
// an error rather than being absorbed as "not live". Absorbing it would let an
// expired context — scripts/verify-kernel.go runs the whole gate under 15s —
// skip every candidate and return an empty, authoritative-looking "nothing
// stranded" over a bucket full of live grants. The error lands in
// StrandedScanErr, whose posture is already a fail-safe notice.
//
// A candidate whose own aspect or vertex is present but unparseable is skipped
// silently. That direction is safe — a missed report, never an invented one,
// the same posture scanKernelOrphans documents — and nothing here writes, so an
// unreadable role cannot be made worse by being left alone. Unreadable EDGES of
// a role that IS reported are counted instead, and raise its rank.
func StrandedOperatorEpochs(ctx context.Context, kv jetstream.KeyValue) ([]StrandedOperatorEpoch, error) {
	if RoleOperatorID == "" {
		return nil, fmt.Errorf("%w: roleOperator", ErrPrimordialIDsUnloaded)
	}

	var accountedFor map[string]bool

	var out []StrandedOperatorEpoch
	err := walkDistinctKeys(ctx, kv, "vtx.role.*.canonicalName", func(page []string) error {
		for _, aspectKey := range page {
			roleKey, _, roleID, _, ok := substrate.ParseAspectKey(aspectKey)
			if !ok || roleID == RoleOperatorID {
				continue
			}
			aspect, state, err := readDocument(ctx, kv, aspectKey)
			if err != nil {
				return err
			}
			if state != docLive {
				continue
			}
			if name, _ := aspect.Data["value"].(string); name != "operator" {
				continue
			}
			role, state, err := readDocument(ctx, kv, roleKey)
			if err != nil {
				return err
			}
			if state != docLive {
				continue
			}

			if accountedFor == nil {
				accountedFor, err = currentOperatorHolders(ctx, kv)
				if err != nil {
					return err
				}
			}

			holdsRole, holderDrops, err := liveEdgesInto(ctx, kv, "lnk.identity.*.holdsRole.role."+roleID, "identity")
			if err != nil {
				return err
			}
			var holders, reachableVia []string
			for _, edge := range holdsRole {
				if accountedFor[edge.sourceKey] {
					reachableVia = append(reachableVia, edge.linkKey)
					continue
				}
				holders = append(holders, edge.sourceKey)
			}

			grantEdges, grantDrops, err := liveEdgesInto(ctx, kv, "lnk.permission.*.grantedBy.role."+roleID, "permission")
			if err != nil {
				return err
			}
			var grants []string
			for _, edge := range grantEdges {
				grants = append(grants, edge.sourceKey)
			}

			protected, _ := role.Data["protected"].(bool)
			out = append(out, StrandedOperatorEpoch{
				RoleKey:         roleKey,
				Holders:         sortedUnique(holders),
				ReachableVia:    sortedUnique(reachableVia),
				GrantedBy:       sortedUnique(grants),
				UnreadableEdges: holderDrops + grantDrops,
				Protected:       protected,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(out, func(i, j int) bool { return out[i].RoleKey < out[j].RoleKey })
	return out, nil
}

// currentOperatorHolders returns the identities that VERIFIABLY hold this
// deployment's current operator role: a live holdsRole edge into it, both
// endpoints live, from an identity the primordial table names.
//
// Read from the graph rather than assumed from the id table, because the
// premise being tested is exactly the one an id table cannot answer. A
// primordial identity's holdsRole edge is an ordinary link (§4.1) and can be
// revoked like any other; an identity whose current-role edge is gone but which
// holds a stranded `operator` role is ACQUIRING root through the name-matching
// lenses, not retaining it, and must not be counted as accounted-for.
//
// The intersection with the primordial set is what keeps this from being
// circular: a prior epoch's admin also holds an `operator` role, and only the
// id table can say which identities this deployment is entitled to.
//
// Unreadable edges on this listing are not counted into any finding: a dropped
// edge here can only SHRINK the accounted-for set, which promotes findings
// rather than hiding them.
func currentOperatorHolders(ctx context.Context, kv jetstream.KeyValue) (map[string]bool, error) {
	edges, _, err := liveEdgesInto(ctx, kv, "lnk.identity.*.holdsRole.role."+RoleOperatorID, "identity")
	if err != nil {
		return nil, err
	}
	primordial := primordialIdentityKeys()
	holders := make(map[string]bool, len(edges))
	for _, edge := range edges {
		if primordial[edge.sourceKey] {
			holders[edge.sourceKey] = true
		}
	}
	return holders, nil
}

// primordialIdentityKeys returns the vertex keys of the six identities this
// deployment's primordial table seeds with a holdsRole edge into the operator
// role: the admin plus the Loom, Weaver, Bridge, object-store-manager and
// privacy service actors (primordial.go:800-809 and the link keys at
// nanoid.go:620-625).
//
// The Gateway identity is deliberately absent. Contract #7 §7.2 states it does
// NOT hold the operator role and is scoped narrow precisely because it is
// internet-facing (primordial.go:492-502), so a holdsRole edge from it into an
// `operator` role is the most serious finding this scan can make, never
// evidence of health.
//
// Each id is re-checked before use because substrate.VertexKey panics on a
// malformed NanoID, and a reporting scan must not be able to take a boot down.
func primordialIdentityKeys() map[string]bool {
	ids := []string{
		BootstrapIdentityID,
		LoomIdentityID,
		WeaverIdentityID,
		BridgeIdentityID,
		ObjmgrIdentityID,
		PrivacyIdentityID,
	}
	keys := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !substrate.IsValidNanoID(id) {
			continue
		}
		keys[substrate.VertexKey("identity", id)] = true
	}
	return keys
}

// liveEdge is one live link whose source vertex is itself live.
type liveEdge struct {
	linkKey   string
	sourceKey string
}

// liveEdgesInto enumerates a target-bounded link filter and returns the edges
// that are live at BOTH ends: the link document is present and untombstoned,
// and so is the vertex the link key names as its source. unreadable counts the
// edges whose classification could not be established at all.
//
// Both ends are required because the link alone does not establish that
// anything exists to exercise it. A grantedBy edge to a tombstoned permission
// confers nothing, and a holdsRole edge from a tombstoned identity reaches
// nothing — counting either would report authority that is not there. This is
// also the single live set the accounted-for census reads, so a tombstoned
// service identity whose edge survived cannot launder a finding.
//
// The source is derived from the link KEY, never from a body field: under
// Contract #1 §1.1 the source is the key's first (type, id) pair, so the key is
// the authoritative statement of what the edge relates, and a body that
// disagreed with it would be corruption rather than a second opinion.
func liveEdgesInto(ctx context.Context, kv jetstream.KeyValue, filter, sourceType string) (edges []liveEdge, unreadable int, err error) {
	walkErr := walkDistinctKeys(ctx, kv, filter, func(page []string) error {
		for _, linkKey := range page {
			_, sourceID, _, _, _, ok := substrate.ParseLinkKey(linkKey)
			if !ok {
				unreadable++
				continue
			}
			_, linkState, err := readDocument(ctx, kv, linkKey)
			if err != nil {
				return err
			}
			if linkState == docUnreadable {
				unreadable++
				continue
			}
			if linkState != docLive {
				continue
			}
			sourceKey := substrate.VertexKey(sourceType, sourceID)
			_, sourceState, err := readDocument(ctx, kv, sourceKey)
			if err != nil {
				return err
			}
			if sourceState == docUnreadable {
				unreadable++
				continue
			}
			if sourceState != docLive {
				continue
			}
			edges = append(edges, liveEdge{linkKey: linkKey, sourceKey: sourceKey})
		}
		return nil
	})
	if walkErr != nil {
		return nil, 0, walkErr
	}
	return edges, unreadable, nil
}

// walkDistinctKeys enumerates the keys matching a KV subject filter and hands
// them to visit in sorted, de-duplicated pages of at most strandedScanPageSize,
// re-checking the context between pages so a long read pass cannot run on past
// its deadline and then be mistaken for a complete one.
func walkDistinctKeys(ctx context.Context, kv jetstream.KeyValue, filter string, visit func(page []string) error) error {
	keys, err := listDistinctKeys(ctx, kv, filter)
	if err != nil {
		return err
	}
	for start := 0; start < len(keys); start += strandedScanPageSize {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("scan %q: interrupted (partial result discarded): %w", filter, err)
		}
		if err := visit(keys[start:min(start+strandedScanPageSize, len(keys))]); err != nil {
			return err
		}
	}
	return nil
}

// listDistinctKeys returns the sorted, de-duplicated keys matching a KV subject
// filter.
//
// Two guards it never omits. The lister's feed goroutine closes its channel on
// ctx expiry exactly as it does on completion, so a timed-out listing is
// indistinguishable from a complete one — and a truncated set read as "no
// stranded epoch" is the exact wrong answer, so the context error is returned
// and the partial result discarded. And the pinned NATS KV lister may report
// duplicate keys, which would double-count a role's holders; keys are unique in
// the store, so de-duplicating the sorted enumeration is exact
// (substrate/kv.go:308-317).
func listDistinctKeys(ctx context.Context, kv jetstream.KeyValue, filter string) ([]string, error) {
	lister, err := kv.ListKeysFiltered(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", filter, err)
	}
	defer lister.Stop()
	var collected []string
	for k := range lister.Keys() {
		collected = append(collected, k)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list %q: interrupted (partial result discarded): %w", filter, err)
	}
	return sortedUnique(collected), nil
}

// sortedUnique returns the sorted, duplicate-free keys of the input. The input
// slice is left untouched.
func sortedUnique(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	out := sorted[:1]
	for _, k := range sorted[1:] {
		if k != out[len(out)-1] {
			out = append(out, k)
		}
	}
	return out
}

// docState is what one read established about a key.
type docState int

const (
	// docLive is present, parseable, and carrying no soft tombstone.
	docLive docState = iota
	// docTombstoned is present and parseable, with isDeleted set.
	docTombstoned
	// docAbsent is no such key — an answer, not a failure.
	docAbsent
	// docUnreadable is present but the body would not parse, so nothing about
	// it was established.
	docUnreadable
)

// storedDocument is the slice of a stored envelope this scan reads: whether it
// carries a soft tombstone, and its data payload.
type storedDocument struct {
	IsDeleted bool           `json:"isDeleted"`
	Data      map[string]any `json:"data"`
}

// readDocument reads one key and reports what the read established.
//
// An absent key is docAbsent, not an error — the graph legitimately lacks keys
// this scan probes for. Every other read failure IS returned, because "could
// not read" and "is not there" are opposite answers to the question this scan
// asks, and collapsing them lets one expired context turn a bucket full of
// stranded authority into a confident all-clear.
//
// An unparseable body is docUnreadable rather than an error: nothing is written
// here, so a corrupt document can only cost a report, and propagating it would
// let one bad key take down a scan that runs on every boot. Callers that can be
// made less alarming by such a skip count it instead of ignoring it.
//
// A tombstoned document is returned with its data intact alongside
// docTombstoned, so a caller reading a field cannot accidentally satisfy the
// tombstone check by finding the field empty.
func readDocument(ctx context.Context, kv jetstream.KeyValue, key string) (doc storedDocument, state docState, err error) {
	stored, getErr := kv.Get(ctx, key)
	if getErr != nil {
		if errors.Is(getErr, jetstream.ErrKeyNotFound) {
			return storedDocument{}, docAbsent, nil
		}
		return storedDocument{}, docUnreadable, fmt.Errorf("read %s: %w", key, getErr)
	}
	if json.Unmarshal(stored.Value(), &doc) != nil {
		return storedDocument{}, docUnreadable, nil
	}
	if doc.IsDeleted {
		return doc, docTombstoned, nil
	}
	return doc, docLive, nil
}
