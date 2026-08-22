package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// An applied weaverTarget's gap actions become live Weaver convergence
// behavior, and the Weaver dispatches them under its own service actor — which
// holds the operator role at scope:any over the whole rbac / identity /
// capability surface (internal/bootstrap/primordial.go, packages/rbac-domain).
// A gap therefore carries the Weaver's full authority: a directOp naming
// AssignRole (or GrantPermission, CreatePermission, TombstoneRole, …) with
// attacker-chosen params would grant the operator role to an arbitrary identity
// for every violating row; assignTask (dispatches an arbitrary operation) and
// triggerLoom (could fire identityErasure) are the same class.
//
// The install-side platformProtectedPackages guard bounds what an AI-authored
// proposal may INSTALL; it says nothing about what an installed gap DISPATCHES.
// A benign ai-target-* package whose gap dispatches AssignRole installs cleanly
// and escalates at convergence time. This file is the dispatch-side complement:
// an authored weaverTarget may not drive an operation, or a loom pattern,
// declared by a platform-protected package, and may not bind to a
// protected/secure lens. It is scoped to the capability-ARTIFACT path only —
// a package-authored target legitimately dispatches platform operations, so the
// check lives here, never in the shared orchestrationguard.

// protectedDispatchSets are the operations and loom patterns an authored
// weaverTarget's gaps may not dispatch: exactly those a platform-protected
// package declares. It is DERIVED from the live catalog on every apply — never a
// hand-maintained list — so a new privileged operation added to a protected
// package is covered the moment it installs, with nothing to keep in sync.
type protectedDispatchSets struct {
	ops      map[string]bool
	patterns map[string]bool
}

// loadProtectedDispatchSets builds the protected-operation and protected-pattern
// sets by enumerating exactly the platform-protected packages' declared metas.
//
// The authoritative "which package declared this" record is each package
// manifest's declaredKeys list (internal/pkgmgr/build.go): a meta key appears in
// exactly the declaredKeys of the package that created it. So the protected
// packages' declaredKeys name precisely the metas the trust base owns; reading
// them yields every operationType those packages admit (via a DDL's
// permittedCommands or an op-meta's operationType) and every loom pattern they
// declare.
//
// It fails CLOSED: any list, read, or parse failure returns an error, and the
// caller refuses the apply. Building an INCOMPLETE protected set and then
// admitting a gap against it is exactly the escalation this gate exists to stop,
// so an unreadable catalog must block the apply, never wave it through.
func loadProtectedDispatchSets(ctx context.Context, conn *substrate.Conn) (protectedDispatchSets, error) {
	zero := protectedDispatchSets{}
	keys, err := conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return zero, fmt.Errorf("list core-kv for protected-dispatch scope: %w", err)
	}

	// Every package manifest, read to learn which packages are protected and,
	// for those, exactly which meta keys they declared.
	var manifestKeys []string
	for _, k := range keys {
		if strings.HasPrefix(k, "vtx.package.") && strings.HasSuffix(k, ".manifest") {
			manifestKeys = append(manifestKeys, k)
		}
	}
	protectedKeys := map[string]bool{}
	if len(manifestKeys) > 0 {
		manifests, err := conn.KVGetMulti(ctx, CoreBucket, manifestKeys)
		if err != nil {
			return zero, fmt.Errorf("read package manifests for protected-dispatch scope: %w", err)
		}
		for _, mk := range manifestKeys {
			entry, ok := manifests[mk]
			if !ok || entry == nil {
				// A manifest key that listed but did not read back is a torn view
				// of the trust base; refuse rather than proceed half-blind.
				//
				// refusal-sentinel: (transient) a torn multi-key read is not a
				// package state — the same call can read back whole a moment
				// later, so this is one of the few refusals a retry can clear
				// and the caller is right to treat it as one.
				return zero, fmt.Errorf("package manifest %s did not read back; refusing to build a partial protected set", mk)
			}
			name, declaredKeys, perr := parseManifestNameAndKeys(entry.Value)
			if perr != nil {
				return zero, fmt.Errorf("parse package manifest %s: %w", mk, perr)
			}
			if PlatformProtectedPackage(name) {
				for _, dk := range declaredKeys {
					protectedKeys[dk] = true
				}
			}
		}
	}

	sets := protectedDispatchSets{ops: map[string]bool{}, patterns: map[string]bool{}}
	if len(protectedKeys) == 0 {
		// No protected package installed (e.g. a dev stack that never ran
		// install-packages): there is no privileged operation to protect, and
		// no operator role to escalate into. An empty set is the correct answer.
		return sets, nil
	}

	// Read only the declared keys that carry a classification signal — meta
	// roots (for an op-meta's operationType and to recognize a loomPattern by
	// class), permittedCommands aspects (a DDL's admitted operations), and spec
	// aspects (a loomPattern's patternId — its identity lives in the .spec body,
	// not a .canonicalName aspect, which .spec-only metas never have). A
	// protected package also declares roles and permissions; neither bears on
	// which operation or pattern a gap may dispatch.
	toRead := make([]string, 0, len(protectedKeys))
	for k := range protectedKeys {
		if isMetaRootKey(k) || strings.HasSuffix(k, ".permittedCommands") || strings.HasSuffix(k, ".spec") {
			toRead = append(toRead, k)
		}
	}
	sort.Strings(toRead)
	docs, err := conn.KVGetMulti(ctx, CoreBucket, toRead)
	if err != nil {
		return zero, fmt.Errorf("read protected package metas for dispatch scope: %w", err)
	}

	for _, k := range toRead {
		entry, ok := docs[k]
		if !ok || entry == nil {
			// A declared key absent from the store is a tombstoned/retired meta
			// — genuinely not live, safe to skip (unlike a manifest that failed
			// to read back, which is a torn view of what SHOULD be there).
			continue
		}
		if err := classifyProtectedMeta(k, entry.Value, docs, &sets); err != nil {
			return zero, fmt.Errorf("classify protected meta %s: %w", k, err)
		}
	}
	return sets, nil
}

// parseManifestNameAndKeys extracts a package manifest aspect's name and
// declaredKeys. A tombstoned manifest reads as an uninstalled package (no
// declared keys); a document that does not parse is an error (fail closed).
func parseManifestNameAndKeys(raw []byte) (name string, declaredKeys []string, err error) {
	var env struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			Name         string   `json:"name"`
			DeclaredKeys []string `json:"declaredKeys"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(raw, &env); uerr != nil {
		return "", nil, uerr
	}
	if env.IsDeleted {
		return "", nil, nil
	}
	return env.Data.Name, env.Data.DeclaredKeys, nil
}

// classifyProtectedMeta folds one protected-package meta into the protected
// sets. A meta contributes an operation if it is an op-meta (its root carries
// data.operationType) or a permittedCommands aspect (a DDL's write gate); a loom
// pattern contributes BOTH the identities a triggerLoom gap may name it by — its
// meta-vertex NanoID and its .spec body's patternId (the Weaver's registry
// indexes both, internal/weaver/registry.go indexPattern). Any other meta (a
// lens, a role, a vertexType DDL root with no op declaration of its own)
// contributes nothing. A document that does not parse is an error (fail closed).
func classifyProtectedMeta(key string, raw []byte, docs map[string]*substrate.KVEntry, sets *protectedDispatchSets) error {
	// permittedCommands aspect: every operationType the DDL admits.
	if strings.HasSuffix(key, ".permittedCommands") {
		var env struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Commands []string `json:"commands"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return err
		}
		if !env.IsDeleted {
			for _, op := range env.Data.Commands {
				if op != "" {
					sets.ops[op] = true
				}
			}
		}
		return nil
	}

	// Only 3-segment roots below (vtx.meta.<id>); aspects other than
	// permittedCommands carry no classification signal.
	if !isMetaRootKey(key) {
		return nil
	}
	var root struct {
		Class     string `json:"class"`
		IsDeleted bool   `json:"isDeleted"`
		Data      struct {
			OperationType string `json:"operationType"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	if root.IsDeleted {
		return nil
	}
	// Op-meta root: its own operationType (build.go's OpMetas loop puts it on
	// data). Redundant with permittedCommands for most ops, kept so an op-meta
	// whose admitting DDL lives elsewhere is still caught.
	if root.Data.OperationType != "" {
		sets.ops[root.Data.OperationType] = true
	}
	// Loom pattern: a triggerLoom gap names it by either its meta-vertex NanoID
	// or its patternId, and the Weaver resolves both — so collect both. A
	// .spec-only meta carries no .canonicalName; the patternId lives INSIDE the
	// .spec body (build.go loomPatternSpecBody), the same field the Weaver's
	// registry probes (internal/weaver/registry.go indexPattern).
	if root.Class == loomPatternClass {
		if id := strings.TrimPrefix(key, metaVertexPrefix); id != key {
			sets.patterns[id] = true
		}
		pid, err := patternIDFromSpec(key, docs)
		if err != nil {
			return err
		}
		if pid != "" {
			sets.patterns[pid] = true
		}
	}
	return nil
}

// isMetaRootKey reports whether key is a 3-segment meta-vertex root
// (vtx.meta.<id>), as opposed to a 4-segment aspect key under one.
func isMetaRootKey(key string) bool {
	return strings.HasPrefix(key, "vtx.meta.") && strings.Count(key, ".") == 2
}

// patternIDFromSpec reads a loomPattern's patternId from its sibling `.spec`
// aspect within an already-read batch — the same body the Weaver's registry
// probes for the pattern's identity. Returns ("", nil) when the spec is absent
// or tombstoned (the NanoID collected by the caller still protects the pattern),
// and an error when it is present but unparseable (fail closed: a protected
// pattern whose identity cannot be read must not silently drop out of the set).
func patternIDFromSpec(rootKey string, docs map[string]*substrate.KVEntry) (string, error) {
	entry, ok := docs[rootKey+".spec"]
	if !ok || entry == nil {
		return "", nil
	}
	var env struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			PatternID string `json:"patternId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return "", err
	}
	if env.IsDeleted {
		return "", nil
	}
	return env.Data.PatternID, nil
}

// enforceAuthoredWeaverTargetScope refuses an authored weaverTarget whose gaps
// would dispatch a platform-privileged operation or loom pattern, or bind to a
// protected/secure lens. It is the apply-time, non-bypassable boundary — record
// and approve trust a client-asserted verdict, so this is the gate that actually
// contains the escalation — and it sits alongside the protected-package guard in
// CapabilityApplyPlanForProposal for the same reason.
//
// It is default-DENY over the whole gap-action vocabulary, and deliberately so:
// record-time validation (which rejects an unknown action) is client-bypassable,
// so at apply the Action is an arbitrary string. Every action is therefore
// accounted for exactly one way — classified (directOp / assignTask against the
// protected operation set, triggerLoom against the protected pattern set),
// proven inert (surface: raises a Health-KV issue and dispatches nothing —
// internal/weaver/evaluator.go returns before any mark/OCC/episode), barred
// (proposedOp: a row-sourced op that cannot be statically bounded), or refused
// as unrecognized (anything else, including an empty action — an authored
// artifact cannot goal-author, GapActionArtifact exposing no goal).
func enforceAuthoredWeaverTargetScope(ctx context.Context, conn *substrate.Conn, proposalKey string, targets []WeaverTargetSpec) error {
	if len(targets) == 0 {
		return nil
	}
	protected, err := loadProtectedDispatchSets(ctx, conn)
	if err != nil {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s: cannot verify authored dispatch scope (refusing, fail-closed): %w", proposalKey, err)
	}
	for _, wt := range targets {
		cols := make([]string, 0, len(wt.Gaps))
		for col := range wt.Gaps {
			cols = append(cols, col)
		}
		sort.Strings(cols) // deterministic first-violation reporting
		for _, col := range cols {
			ga := wt.Gaps[col]
			switch ga.Action {
			case actionDirectOp, actionAssignTask:
				if protected.ops[ga.Operation] {
					return protectedDispatchError(proposalKey, wt.TargetID, col, "operation", ga.Operation)
				}
			case actionTriggerLoom:
				if protected.patterns[ga.Pattern] {
					return protectedDispatchError(proposalKey, wt.TargetID, col, "loom pattern", ga.Pattern)
				}
			case actionSurface:
				// Inert: a surface gap raises/clears a named Health-KV issue while
				// the gap is open and dispatches NO operation, creates NO mark, and
				// opens NO episode (internal/weaver/evaluator.go's actionSurface
				// arm acks before all of that). It carries none of the Weaver's
				// authority, so there is nothing to bound.
			case actionProposedOp:
				// Total bar: proposedOp dispatches a row-sourced operation
				// (proposedAction / proposedParams off the violation row), which
				// cannot be statically bounded, and it is reserved for the Augur
				// primordial target. An authored artifact emitting it would author
				// a target that files further row-sourced-op proposals — exactly
				// the unbounded-authority recursion this guard exists to stop.
				// There is no legitimate authored-target use of proposedOp.
				return proposedOpBarError(proposalKey, wt.TargetID, col)
			default:
				// Fail-closed on every other action. Apply is the authoritative
				// boundary and the record-time known-action check is bypassable,
				// so an action not proven safe above is refused rather than driven
				// under the Weaver's operator authority. Covers an empty action
				// too: an authored target cannot goal-author (a capability
				// artifact exposes no goal), so a gap with no action has nothing
				// legitimate to do.
				return unrecognizedActionError(proposalKey, wt.TargetID, col, ga.Action)
			}
		}
		if err := refuseProtectedLensBinding(ctx, conn, proposalKey, wt.TargetID, wt.LensRef); err != nil {
			return err
		}
	}
	return nil
}

// protectedDispatchError mirrors the protected-package refusal's shape and
// wording: an AI-authored artifact may not reach the platform trust base, here
// through a gap's dispatch rather than through the package it installs.
func protectedDispatchError(proposalKey, targetID, col, kind, name string) error {
	return fmt.Errorf("pkgmgr: capability apply: proposal %s: weaver target %q gap %q dispatches %s %q, which is declared by a platform-protected package and which no AI-authored weaver target may drive",
		proposalKey, targetID, col, kind, name)
}

// proposedOpBarError refuses a proposedOp gap outright — it dispatches a
// row-sourced operation the static classification cannot bound.
func proposedOpBarError(proposalKey, targetID, col string) error {
	return fmt.Errorf("pkgmgr: capability apply: proposal %s: weaver target %q gap %q uses the proposedOp action, which dispatches a row-sourced operation that cannot be statically bounded and is reserved for the Augur primordial target; no AI-authored weaver target may emit it",
		proposalKey, targetID, col)
}

// unrecognizedActionError refuses any gap action not proven dispatch-safe — the
// default-deny tail that keeps the vocabulary provably covered even against an
// action a bypassed record-time check never rejected.
func unrecognizedActionError(proposalKey, targetID, col, action string) error {
	return fmt.Errorf("pkgmgr: capability apply: proposal %s: weaver target %q gap %q declares action %q, which is not a dispatch-safe action an AI-authored weaver target may drive (allowed: directOp/assignTask/triggerLoom to non-protected targets, or surface)",
		proposalKey, targetID, col, action)
}

// refuseProtectedLensBinding refuses an authored target bound to a
// protected-posture or secure-column lens: a benign op reading such a lens's
// rows into its params is read-amplification past the row-level-security the
// lens declares. The lensRef of a single-artifact target is the installed
// lens's NanoID (resolveLensRef passes only a NanoID through for a
// single-artifact Definition), so an empty or non-NanoID ref is left to install
// to resolve or reject; an absent spec is likewise a dangling reference install
// will catch. A read failure fails closed.
func refuseProtectedLensBinding(ctx context.Context, conn *substrate.Conn, proposalKey, targetID, lensRef string) error {
	if lensRef == "" || !substrate.IsValidNanoID(lensRef) {
		return nil
	}
	entry, err := conn.KVGet(ctx, CoreBucket, "vtx.meta."+lensRef+".spec")
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s: cannot verify lens binding for target %q (refusing, fail-closed): %w", proposalKey, targetID, err)
	}
	var env struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			TargetConfig struct {
				Protected     bool              `json:"protected"`
				SecureColumns []json.RawMessage `json:"secureColumns"`
			} `json:"targetConfig"`
		} `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s: lens %q spec is unparseable (refusing, fail-closed): %w", proposalKey, lensRef, err)
	}
	if env.IsDeleted {
		return nil
	}
	if env.Data.TargetConfig.Protected || len(env.Data.TargetConfig.SecureColumns) > 0 {
		return fmt.Errorf("pkgmgr: capability apply: proposal %s: weaver target %q binds to lens %q, which is protected or declares secure columns, so no AI-authored target may read its rows",
			proposalKey, targetID, lensRef)
	}
	return nil
}
