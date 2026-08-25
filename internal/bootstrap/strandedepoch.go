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

// strandedScanPageSize bounds how many enumerated keys the scan holds in flight
// per read pass, and gives the walk a place to re-check the context between
// pages.
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
type StrandedOperatorEpoch struct {
	// RoleKey is the role's vertex key, vtx.role.<id>.
	RoleKey string
	// Holders holds the sorted vtx.identity.<id> keys of live identities that
	// hold this role and that the current primordial table does not name.
	//
	// This is what decides severity. The wildcard read-grant lens
	// (lenses.go:353-366) selects holders of ANY role whose canonicalName is
	// `operator` — by NAME, reading no grantedBy edge at all — and projects
	// ('*', 'cap-read.root'); the RLS policy accepts an anchor_id='*' row with
	// no grant_source filter (internal/refractor/adapter/rls.go:198-201). So a
	// single live holder here is installation-wide read of every RLS-protected
	// table, held by an actor this deployment's id file does not name.
	//
	// The keys are reported, never just counted: each one names the holdsRole
	// edge to tombstone, so they are the remedy, and an operator has to see that
	// the holder is itself residue to understand the island.
	Holders []string
	// ReachableVia holds the sorted holdsRole link keys by which identities the
	// current primordial table DOES name reach this role.
	//
	// They demote an otherwise-inert finding to a notice — and are named in it
	// rather than deleting it. The edge that produces one is an ordinary link
	// create, which the commit-time kernel guard does not cover (it skips
	// creates, step8_commit.go:728-729, and protectedRootKey returns "" for a
	// link key anyway), so a single AssignRole could otherwise silence this scan
	// permanently while removing none of the residue. A finding that still has
	// live Holders stays severe regardless of what is recorded here.
	ReachableVia []string
	// GrantedBy holds the sorted vtx.permission.<id> keys this role still
	// confers through a live grantedBy edge to a live permission vertex.
	//
	// Detail, never severity. Every consumer of a permission reaches it through
	// the holder's holdsRole edge (lenses.go:136-137,
	// packages/rbac-domain/lenses.go:92-93), so grants on a role no live
	// identity holds authorize nothing: a long GrantedBy beside an empty
	// Holders is inert residue, and the reverse — holders with no grants — is
	// the dangerous state, because the wildcard lens above needs no grant.
	GrantedBy []string
	// UnreadableEdges counts the link or endpoint documents that could not be
	// classified while building the three lists above — a body that would not
	// parse, or a link key that would not.
	//
	// It is surfaced because every such skip can only shrink Holders, and
	// severity keys on Holders: an unreported drop would silently downgrade a
	// failure to a notice. Non-zero means the counts are a lower bound and the
	// report says so.
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

// StrandedSeverity ranks one finding for the gates that report it.
type StrandedSeverity int

const (
	// StrandedSeverityInert is a role no live identity holds. Its grants
	// authorize nothing, because every path to a permission runs through a
	// holder's holdsRole edge.
	StrandedSeverityInert StrandedSeverity = iota
	// StrandedSeverityReachable is a role held only by identities the current
	// primordial table names. They already hold the current operator role, so
	// the wildcard lens grants them nothing they did not have; the finding is
	// still reported so that an ordinary AssignRole cannot silence the check.
	StrandedSeverityReachable
	// StrandedSeverityUnreachableAuthority is a role held by at least one
	// identity the current primordial table does not name — live root-equivalent
	// read access in the hands of an actor this deployment cannot account for.
	StrandedSeverityUnreachableAuthority
)

// Severity ranks the finding. It keys on HOLDERS, not on grants: the wildcard
// read-grant lens selects on the role's canonicalName and reads no grantedBy
// edge, so a holder with zero grants already has installation-wide read, while
// grants with no holder are unreachable. An unaccounted-for holder outranks
// any number of current-epoch ones — otherwise a single AssignRole would demote
// a live escalation to a notice.
func (e StrandedOperatorEpoch) Severity() StrandedSeverity {
	switch {
	case len(e.Holders) > 0:
		return StrandedSeverityUnreachableAuthority
	case len(e.ReachableVia) > 0:
		return StrandedSeverityReachable
	default:
		return StrandedSeverityInert
	}
}

// Report renders the finding as one line for a gate's output, naming the keys
// an operator would act on rather than counting them.
func (e StrandedOperatorEpoch) Report() string {
	const preamble = "STRANDED OPERATOR ROLE: %s is named `operator` but is not this deployment's operator role"
	var b strings.Builder
	switch e.Severity() {
	case StrandedSeverityUnreachableAuthority:
		fmt.Fprintf(&b, preamble+
			", and is held by %d identity(ies) the primordial table does not name: %s."+
			" The wildcard read-grant lens selects on the role NAME and needs no grant, so each holder has"+
			" installation-wide read of every RLS-protected table. Live grants (%d): %s."+
			" Remedy: tombstone those holdsRole edges into %s — link mutations are not kernel-protected,"+
			" so this needs no wipe.",
			e.RoleKey, len(e.Holders), summarizeKeys(e.Holders), len(e.GrantedBy), summarizeKeys(e.GrantedBy), e.RoleKey)
	case StrandedSeverityReachable:
		fmt.Fprintf(&b, preamble+
			", and is held only by current-epoch identities, via %s. Live grants (%d): %s."+
			" Reported rather than dropped: the holdsRole create that produces this is not kernel-protected,"+
			" so it must not be able to silence the check.",
			e.RoleKey, summarizeKeys(e.ReachableVia), len(e.GrantedBy), summarizeKeys(e.GrantedBy))
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
// Every candidate that clears the predicate is REPORTED, including one a
// current-epoch identity holds. Classification, not suppression, is what the
// caller acts on: Holders drives severity, ReachableVia demotes. A scan that
// silently dropped a candidate on the strength of one holdsRole edge could be
// switched off for good by a single ordinary AssignRole.
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
// match EVERY role and leaves the current-epoch identity set empty, so the live
// kernel role would be reported with its own holders counted as strangers — the
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
// a role that IS reported are counted instead of being dropped quietly, because
// those can only shrink the holder list severity keys on.
func StrandedOperatorEpochs(ctx context.Context, kv jetstream.KeyValue) ([]StrandedOperatorEpoch, error) {
	if RoleOperatorID == "" {
		return nil, fmt.Errorf("%w: roleOperator", ErrPrimordialIDsUnloaded)
	}

	currentEpochIdentities := currentEpochIdentityKeys()

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

			holdsRole, holderDrops, err := liveEdgesInto(ctx, kv, "lnk.identity.*.holdsRole.role."+roleID, "identity")
			if err != nil {
				return err
			}
			var holders, reachableVia []string
			for _, edge := range holdsRole {
				if currentEpochIdentities[edge.sourceKey] {
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

// currentEpochIdentityKeys returns the vertex keys of the identities this
// deployment's loaded primordial table names as holders of the operator role.
//
// Contract #7 §7.2 fixes the membership at six: the primordial admin plus the
// Loom, Weaver, Bridge, object-store-manager and privacy service actors. The
// Gateway identity is deliberately excluded — the contract states it does NOT
// hold the operator role and is scoped narrow precisely because it is
// internet-facing, so a holdsRole edge from it into an `operator` role is the
// most serious finding this scan can make, never evidence of health.
//
// Each id is re-checked before use because substrate.VertexKey panics on a
// malformed NanoID, and a reporting scan must not be able to take a boot down.
func currentEpochIdentityKeys() map[string]bool {
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
// also the single live set the current-epoch reachability test reads, so a
// tombstoned service identity whose edge survived cannot suppress a finding.
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
// re-checking the context between pages so a long walk cannot run on past its
// deadline and then be mistaken for a complete one.
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
