// Package controlauth is the shared home for control-plane request
// authentication + authorization (control-plane-capability-authz-design.md).
// Fire 1a added the actor-on-the-wire header carried by the three control
// services (Weaver/Loom/Refractor) and their CLI/Loupe clients (header.go).
// Fire 1b adds CapabilityKVChecker — the capability checker that authorizes
// the extracted actor against Contract #6 §6.4 platform permissions.
package controlauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/asolgan/lattice/internal/capabilitykv"
)

// AuthMode selects a control service's CapabilityChecker. Mirrors
// internal/processor's AuthMode naming (same LATTICE_AUTH_MODE knob, one
// value for both the write path and the control plane — design §3.3: "no
// second CTRL knob, the asymmetry is unjustified").
type AuthMode string

const (
	// AuthModeStub is the always-allow checker for dev/test use. Every call
	// logs a warning and periodically emits the `stub-control-active` Health
	// KV alert. Production deploys must use AuthModeCapability (the default).
	AuthModeStub AuthMode = "stub"
	// AuthModeCapability is the real Capability KV checker. Empty AuthMode
	// also resolves to this mode.
	AuthModeCapability AuthMode = "capability"
)

// Fail-closed denial reasons. Every non-allow path returns one of these —
// there is no fail-open branch (design §3.3).
var (
	ErrNoActor           = errors.New("controlauth: no actor asserted")
	ErrUnknownControlOp  = errors.New("controlauth: unknown control op")
	ErrNoCapabilityEntry = errors.New("controlauth: no capability kv entry for actor")
	ErrControlDenied     = errors.New("controlauth: actor lacks the control grant")
)

// stubAlertEveryNCalls mirrors processor's StubAuthorizer: every Nth
// Authorize call additionally emits a Health KV alert (avoid flooding while
// keeping the signal alive between heartbeats).
const stubAlertEveryNCalls uint64 = 1000

// CapabilityKVChecker is a control.CapabilityChecker (the identical
// 3-string-arg Authorize signature is structurally satisfied for Weaver,
// Loom, and Refractor — one concrete type serves all three, design R5). It
// shares the Contract #6 §6.2 read + class-aware key routing with the
// Processor via internal/capabilitykv, but owns its own simple
// operationType/scope-any matcher (the Processor's matcher has no `ctrl.*`
// semantics and its `specific` scope denies — design §2(a)/§3.3).
type CapabilityKVChecker struct {
	component string
	ops       map[string]OpMeta
	reader    capabilitykv.KVGetter
	bucket    string
	keysFor   func(actor string) ([]string, error)
	mode      AuthMode
	alerts    AuthAlertEmitter
	logger    *slog.Logger
	counter   atomic.Uint64
}

// NewCapabilityKVChecker constructs a checker bound to one component
// ("weaver" | "loom" | "refractor") and its op→verb table (WeaverOps /
// LoomOps / RefractorOps). rbacRolesActive + systemActorKeys mirror the
// Processor's step-3 platform routing inputs (processor.AuthWiring) so the
// checker reads the same key the Processor would for any given actor:
// rbacRolesActive true routes the kernel-seeded system actors to a union of
// their cap.<actor> anchor and cap.roles.<actor>, and every other actor to
// cap.roles.<actor> alone; false routes every actor to cap.<actor>.
//
// Production ALWAYS passes true. Class-aware routing is correct whether or not
// rbac-domain is installed — an absent cap.roles.<actor> is an empty skip in
// the union read (capabilitykv.ReadAndMerge), so a fresh kernel degrades to the
// anchor floor for system actors and deny-by-absence for ordinary actors. Do
// NOT re-gate it on a boot-time rbac-install probe: that probe latched the
// pre-install state for a component booted before packages install and denied
// every package-granted actor for the process lifetime. The false path is a
// test-only posture. Nil alerts uses a no-op emitter; nil logger uses
// slog.Default().
func NewCapabilityKVChecker(
	component string,
	ops map[string]OpMeta,
	reader capabilitykv.KVGetter,
	bucket string,
	systemActorKeys []string,
	rbacRolesActive bool,
	mode AuthMode,
	alerts AuthAlertEmitter,
	logger *slog.Logger,
) *CapabilityKVChecker {
	if logger == nil {
		logger = slog.Default()
	}
	if alerts == nil {
		alerts = noopAlertEmitter{}
	}
	var keysFor func(string) ([]string, error)
	if rbacRolesActive {
		keysFor = capabilitykv.ClassAwarePlatformKey(systemActorKeys)
	} else {
		keysFor = func(actor string) ([]string, error) {
			key, err := capabilitykv.CapabilityKeyFromActor(actor)
			if err != nil {
				return nil, err
			}
			return []string{key}, nil
		}
	}
	return &CapabilityKVChecker{
		component: component,
		ops:       ops,
		reader:    reader,
		bucket:    bucket,
		keysFor:   keysFor,
		mode:      mode,
		alerts:    alerts,
		logger:    logger,
	}
}

// Authorize implements control.CapabilityChecker for all three components.
// Every non-allow path denies — actor=="" , unknown op, infra read error,
// nil doc, and operationType/scope miss all deny; stub mode is the only
// allow-without-grant path, and it is loud (per-call warn + periodic
// stub-control-active alert), never the default.
func (c *CapabilityKVChecker) Authorize(ctx context.Context, actor, op, id string) error {
	if c.mode == AuthModeStub {
		c.logAndAlertStub(ctx, actor, op, id)
		return nil
	}
	if actor == "" {
		return ErrNoActor
	}
	meta, ok := c.ops[op]
	if !ok {
		return ErrUnknownControlOp
	}
	keys, err := c.keysFor(actor)
	if err != nil {
		return fmt.Errorf("controlauth: derive keys for actor %q: %w", actor, err)
	}
	doc, _, err := capabilitykv.ReadAndMerge(ctx, c.reader, c.bucket, keys)
	if err != nil {
		return fmt.Errorf("controlauth: capability kv read: %w", err)
	}
	if doc == nil {
		return ErrNoCapabilityEntry
	}
	want := "ctrl." + c.component + "." + meta.Verb
	for _, p := range doc.PlatformPermissions {
		if p.OperationType == want && p.Scope == "any" {
			return nil
		}
	}
	return ErrControlDenied
}

func (c *CapabilityKVChecker) logAndAlertStub(ctx context.Context, actor, op, id string) {
	c.logger.Warn("STUB CONTROL AUTH: allow-all; set LATTICE_AUTH_MODE=capability to enable Capability KV auth",
		"component", c.component, "actor", actor, "op", op, "targetId", id)
	n := c.counter.Add(1)
	if n == 1 || n%stubAlertEveryNCalls == 0 {
		c.alerts.EmitAlert(ctx, "stub-control-active", map[string]any{
			"component": c.component,
			"callCount": n,
			"actor":     actor,
			"op":        op,
			"targetId":  id,
		})
	}
}
