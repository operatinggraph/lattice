package processor

import (
	"context"
	"errors"
	"strings"
	"testing"

	starlarklib "go.starlark.net/starlark"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// The proof obligations of design sensitive-read-tracker-consumption §5:
// step 6's external-egress guard must key on what the SCRIPT consumed, not on
// what hydration pre-fetched. Under the hydration-keyed flag a *surplus*
// declared read of a sensitive-classed aspect split an external-egress op's
// outcome on whether that key exists — an existence oracle the script never
// participated in.

const (
	consumptionExternalEvent = `{"class": "external.notify.requested", "data": {}}`
	// An identity that was never seeded — the absent arm of the oracle probe.
	consumptionAbsentIdentity = "Wm5xTc7bKq2nRd9pYv4h"
)

// consumptionFixture is a hydrator over a real Vault with one encrypted,
// sensitive `ssn` aspect present and a second sensitive-classed `ssn` key that
// is ABSENT — the two arms of the oracle probe.
type consumptionFixture struct {
	ctx       context.Context
	conn      *substrate.Conn
	vault     vault.Vault
	hydrator  *HydratorImpl
	cache     *DDLCache
	presentK  string
	absentK   string
	plainK    string
	identityK string
}

func newConsumptionFixture(t *testing.T) consumptionFixture {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	v, err := vault.NewLocalBackend([]byte("lattice-testutil-vault-master-ke"), "test-v1")
	if err != nil {
		t.Fatalf("NewLocalBackend: %v", err)
	}
	h := newEgressTestHydrator(t, ctx, conn, v)

	identityKey := "vtx.identity." + testNanoID2
	envelope, err := v.CreateIdentityKey(ctx, identityKey)
	if err != nil {
		t.Fatalf("CreateIdentityKey: %v", err)
	}
	seedPiiKeyAspect(t, ctx, conn, identityKey, envelope)
	ct, err := v.Encrypt(ctx, identityKey, envelope, []byte(`{"value":"123-45-6789"}`))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	presentK := identityKey + ".ssn"
	seedRealCiphertextAspect(t, ctx, conn, presentK, "ssn", ct)

	plainK := identityKey + ".nickname"
	if _, err := conn.KVPut(ctx, testCoreBucket, plainK,
		[]byte(`{"class":"nickname","isDeleted":false,"data":{"value":"Andy"}}`)); err != nil {
		t.Fatalf("seed nickname aspect: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	return consumptionFixture{
		ctx: ctx, conn: conn, vault: v, hydrator: h, cache: cache,
		presentK:  presentK,
		absentK:   "vtx.identity." + consumptionAbsentIdentity + ".ssn",
		plainK:    plainK,
		identityK: identityKey,
	}
}

// hydrate runs step 4 with the sensitive key declared under the given list.
func (f consumptionFixture) hydrate(t *testing.T, list string, key string) HydratedState {
	t.Helper()
	env := newTestEnvelope(testNanoID1)
	switch list {
	case "reads":
		env.ContextHint = &ContextHint{Reads: []string{key}}
	case "optionalReads":
		env.ContextHint = &ContextHint{OptionalReads: []string{key}}
	default:
		t.Fatalf("unknown declaration list %q", list)
	}
	state, err := f.hydrator.Hydrate(f.ctx, env)
	if err != nil {
		t.Fatalf("Hydrate (%s %s): %v", list, key, err)
	}
	return state
}

// TestSensitiveTracker_SurplusDeclaredRead_IsNotAnExistenceOracle is the point
// of the change: an op that emits an `external.*` event and declares a
// sensitive-classed read its script never names must behave IDENTICALLY whether
// that key exists or not — both accepted. Under the hydration-keyed flag the
// present arm rejected with externalEgressSensitivePlaintext and the absent arm
// committed, which answered "does this key exist, and is it sensitive?" to any
// actor holding a grant on the op.
func TestSensitiveTracker_SurplusDeclaredRead_IsNotAnExistenceOracle(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)

	// The script emits an external event and never names the declared key.
	script := `
def execute(state, op):
    return {"mutations": [], "events": [` + consumptionExternalEvent + `]}
`
	for _, list := range []string{"reads", "optionalReads"} {
		for _, arm := range []struct {
			name string
			key  string
		}{
			{"present", f.presentK},
			{"absent", f.absentK},
		} {
			t.Run(list+"/"+arm.name, func(t *testing.T) {
				state := f.hydrate(t, list, arm.key)
				result, err := runKVScript(t, state.Context, script)
				if err != nil {
					t.Fatalf("script must run: %v", err)
				}
				if err := validateExternalEgressGuard(result, state, testNanoID1); err != nil {
					t.Fatalf("surplus declared read of a %s key must not reject: %v", arm.name, err)
				}
			})
		}
	}
}

// TestSensitiveTracker_ConsumedPlaintextBlocksExternalEgress pins containment:
// every seam through which the script actually takes the decrypted document
// still rejects an `external.*` emitter, byte-identically to the
// hydration-keyed behavior. These are the vectors that would go silent if the
// consume hooks were dropped.
func TestSensitiveTracker_ConsumedPlaintextBlocksExternalEgress(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)

	for _, tc := range []struct{ name, body string }{
		{"kv.Read", `    v = kv.Read("` + f.presentK + `")`},
		{"subscript", `    v = state["` + f.presentK + `"]`},
		{"get", `    v = state.get("` + f.presentK + `")`},
		{"membership", `    v = "` + f.presentK + `" in state`},
		{"items", `    n = 0
    for k, v in state.items():
        n = n + 1`},
		{"values", `    n = 0
    for v in state.values():
        n = n + 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := f.hydrate(t, "reads", f.presentK)
			result, err := runKVScript(t, state.Context, `
def execute(state, op):
`+tc.body+`
    return {"mutations": [], "events": [`+consumptionExternalEvent+`]}
`)
			if err != nil {
				t.Fatalf("script must run: %v", err)
			}
			err = validateExternalEgressGuard(result, state, testNanoID1)
			var viol *DDLViolation
			if !errors.As(err, &viol) || viol.ViolatedConstraint != "externalEgressSensitivePlaintext" {
				t.Fatalf("consuming sensitive plaintext via %s must reject; got %v", tc.name, err)
			}
		})
	}
}

// TestSensitiveTracker_WholeSetRenderingBlocksExternalEgress pins the
// containment hole a consumption-keyed flag opens if only the keyed seams are
// hooked: a dict renders its VALUES, so `str(state)` and every expression that
// reaches String() hand the script every hydrated document's data — decrypted
// plaintext included — as a string it can drop straight into an `external.*`
// event, while never calling Get, items() or values().
//
// The counterpart assertion (key-only paths that render nothing) lives in
// TestSensitiveTracker_KeyOnlyEnumerationDoesNotBlockExternalEgress; together
// they pin the line between the two.
func TestSensitiveTracker_WholeSetRenderingBlocksExternalEgress(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)

	for _, tc := range []struct{ name, expr string }{
		{"str", `str(state)`},
		{"repr", `repr(state)`},
		{"format", `"{}".format(state)`},
		{"percent", `"%s" % state`},
		{"concat", `"x" + str(state)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := f.hydrate(t, "reads", f.presentK)
			result, err := runKVScript(t, state.Context, `
def execute(state, op):
    s = `+tc.expr+`
    return {"mutations": [], "events": [{"class": "external.notify.requested", "data": {"rendered": s}}]}
`)
			if err != nil {
				t.Fatalf("script must run: %v", err)
			}
			// The rendering really does carry the plaintext — otherwise this
			// vector would pass for the wrong reason.
			if rendered, _ := result.Events[0].Data["rendered"].(string); !strings.Contains(rendered, "123-45-6789") {
				t.Fatalf("%s did not render the decrypted value; vector is vacuous: %q", tc.name, rendered)
			}
			err = validateExternalEgressGuard(result, state, testNanoID1)
			var viol *DDLViolation
			if !errors.As(err, &viol) || viol.ViolatedConstraint != "externalEgressSensitivePlaintext" {
				t.Fatalf("rendering the whole set via %s must reject; got %v", tc.name, err)
			}
		})
	}
}

// TestSensitiveTracker_KeyOnlyEnumerationDoesNotBlockExternalEgress is the
// other side of §2.1: `keys()` and `for k in state` yield key NAMES, never a
// document, so they are not consumption and must not trip the guard. Without
// this the items/values wrap could have been written as a blanket
// "any Attr flips", which would reject an op that only ever looked at key
// names — and would reject it exactly when the probed key exists.
func TestSensitiveTracker_KeyOnlyEnumerationDoesNotBlockExternalEgress(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)

	for _, tc := range []struct{ name, body string }{
		{"iterate", `    n = 0
    for k in state:
        n = n + 1`},
		{"keys", `    n = 0
    for k in state.keys():
        n = n + 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := f.hydrate(t, "reads", f.presentK)
			result, err := runKVScript(t, state.Context, `
def execute(state, op):
`+tc.body+`
    return {"mutations": [], "events": [`+consumptionExternalEvent+`]}
`)
			if err != nil {
				t.Fatalf("script must run: %v", err)
			}
			if err := validateExternalEgressGuard(result, state, testNanoID1); err != nil {
				t.Fatalf("enumerating key names is not consumption; must not reject: %v", err)
			}
		})
	}
}

// TestState_AttrSurfaceIsDefaultDeny pins the allowlist: a dict's `pop`,
// `setdefault` and `popitem` all RETURN a stored value, so delegating them would
// bypass both of Get's hooks — the required-absent `HydrationMiss` and the
// sensitive-plaintext consumption record. Freezing the dict does not close them
// (`setdefault` on an existing key never inserts, so it never trips the frozen
// check), which is why the surface is an allowlist rather than a blocklist: a
// future go.starlark.net method fails closed here.
func TestState_AttrSurfaceIsDefaultDeny(t *testing.T) {
	t.Parallel()
	key := "vtx.identity." + testNanoID2 + ".ssn"

	for _, tc := range []struct{ name, expr string }{
		{"pop", `state.pop("` + key + `", None)`},
		{"setdefault", `state.setdefault("` + key + `", None)`},
		{"popitem", `state.popitem()`},
		{"clear", `state.clear()`},
		{"update", `state.update({})`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := ScriptContext{
				Hydrated: map[string]VertexDoc{key: {
					Key: key, Class: "ssn",
					Data: map[string]interface{}{"value": "123-45-6789"},
				}},
				SensitiveReads: &sensitiveReadTracker{},
			}
			sc.SensitiveReads.markPlaintext(key)
			if _, err := runKVScript(t, sc, `
def execute(state, op):
    x = `+tc.expr+`
    return {"mutations": [], "events": []}
`); err == nil {
				t.Fatalf("state.%s must not be reachable; the snapshot exposes only %v", tc.name, stateAttrs)
			}
		})
	}

	// stateAttrs and the Attr switch are two independent lists; keep them in
	// lockstep. A name in the slice that Attr refuses makes hasattr() lie one way,
	// a case Attr answers that is missing from the slice lies the other.
	probe := &stateMapValue{d: new(starlarklib.Dict)}
	for _, name := range probe.AttrNames() {
		v, err := probe.Attr(name)
		if err != nil || v == nil {
			t.Fatalf("AttrNames advertises %q but Attr does not answer it (v=%v err=%v)", name, v, err)
		}
	}
	for _, name := range []string{"pop", "popitem", "setdefault", "clear", "update", "nonesuch"} {
		v, err := probe.Attr(name)
		if err == nil && v != nil {
			t.Fatalf("Attr answers %q but AttrNames does not advertise it", name)
		}
	}

	// The four allowlisted accessors stay reachable — the deny must not have
	// swallowed the real surface.
	sc := ScriptContext{
		Hydrated:       map[string]VertexDoc{key: {Key: key, Class: "ssn"}},
		SensitiveReads: &sensitiveReadTracker{},
	}
	if _, err := runKVScript(t, sc, `
def execute(state, op):
    a = state.get("`+key+`")
    b = [k for k in state.keys()]
    c = [v for v in state.values()]
    d = [k for k, v in state.items()]
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("the allowlisted accessors must all work: %v", err)
	}
}

// TestState_JSONEncodeExposesNoDocumentData pins a trap. `json.encode` tries
// starlark's IterableMapping FIRST and only falls through to Iterable — key names
// alone — because stateMapValue has no `Items()` method. Adding one (the natural
// way to make `dict(state)` work) would turn this into a full dump of every
// hydrated document with no consume call at all. Either keep Items() off the
// type, or make it record a whole-set consumption; this test is what tells you.
func TestState_JSONEncodeExposesNoDocumentData(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)
	state := f.hydrate(t, "reads", f.presentK)

	result, err := runKVScript(t, state.Context, `
def execute(state, op):
    s = json.encode(state)
    return {"mutations": [], "events": [{"class": "external.notify.requested", "data": {"encoded": s}}]}
`)
	if err != nil {
		// An error is also an acceptable outcome — it exposes nothing.
		t.Logf("json.encode(state) errored (closed): %v", err)
		return
	}
	encoded, _ := result.Events[0].Data["encoded"].(string)
	if strings.Contains(encoded, "123-45-6789") {
		t.Fatalf("json.encode(state) leaked plaintext and recorded no consumption: %q", encoded)
	}
	if err := validateExternalEgressGuard(result, state, testNanoID1); err != nil {
		t.Fatalf("encoding key names alone is not consumption: %v", err)
	}
}

// TestHydrator_AlwaysWiresSensitiveTracker: the whole egress guard is vacuous
// when ScriptContext.SensitiveReads is nil, and nothing downstream can recover
// it — hydration is where the recording happens, so a fresh tracker installed
// later would record nothing. The invariant is therefore that the production
// hydrator always wires one, whether or not the operation declares any read.
func TestHydrator_AlwaysWiresSensitiveTracker(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)

	bare := newTestEnvelope(testNanoID1)
	state, err := f.hydrator.Hydrate(f.ctx, bare)
	if err != nil {
		t.Fatalf("Hydrate (no contextHint): %v", err)
	}
	if state.Context.SensitiveReads == nil {
		t.Fatalf("hydration must always wire a sensitive-read tracker; a nil one makes step 6's egress guard silently vacuous")
	}
}

// TestSensitiveTracker_NonSensitiveWorkingSetNeverBlocks: consumption only
// matters for a document that carries decrypted plaintext. A script that reads
// its whole non-sensitive working set through every seam — including
// values() — commits.
func TestSensitiveTracker_NonSensitiveWorkingSetNeverBlocks(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)
	state := f.hydrate(t, "reads", f.plainK)

	result, err := runKVScript(t, state.Context, `
def execute(state, op):
    a = kv.Read("`+f.plainK+`")
    b = state["`+f.plainK+`"]
    c = state.get("`+f.plainK+`")
    d = "`+f.plainK+`" in state
    n = 0
    for k, v in state.items():
        n = n + 1
    for v in state.values():
        n = n + 1
    return {"mutations": [], "events": [`+consumptionExternalEvent+`]}
`)
	if err != nil {
		t.Fatalf("script must run: %v", err)
	}
	if err := validateExternalEgressGuard(result, state, testNanoID1); err != nil {
		t.Fatalf("a non-sensitive working set must never trip the guard: %v", err)
	}
}

// TestSensitiveTracker_PlaintextAtRestStillBlocksExternalEgress: with no Vault
// wired, step 6.5 never encrypts on the way in, so a sensitive aspect sits in
// Core KV as PLAINTEXT and no decrypt happens on the way out. Recording only on
// a successful decrypt left the guard vacuous for exactly the deployment that
// has no crypto boundary at all — the widest case, and the dev default. The
// predicate is "sensitive-classed and readable", not "I decrypted it".
func TestSensitiveTracker_PlaintextAtRestStillBlocksExternalEgress(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := newEgressTestHydrator(t, ctx, conn, nil) // no Vault — nothing encrypts, nothing decrypts.

	aspectKey := "vtx.identity." + testNanoID2 + ".ssn"
	if _, err := conn.KVPut(ctx, testCoreBucket, aspectKey,
		[]byte(`{"class":"ssn","isDeleted":false,"data":{"value":"123-45-6789"}}`)); err != nil {
		t.Fatalf("seed plaintext sensitive aspect: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{Reads: []string{aspectKey}}
	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	// Non-vacuity: the body really is readable plaintext, no decrypt involved.
	if got, _ := state.Context.Hydrated[aspectKey].Data["value"].(string); got != "123-45-6789" {
		t.Fatalf("aspect data = %+v, want readable plaintext at rest", state.Context.Hydrated[aspectKey].Data)
	}

	result, err := runKVScript(t, state.Context, `
def execute(state, op):
    d = state["`+aspectKey+`"]
    return {"mutations": [], "events": [{"class": "external.notify.requested", "data": {"ssn": d.data["value"]}}]}
`)
	if err != nil {
		t.Fatalf("script must run: %v", err)
	}
	err = validateExternalEgressGuard(result, state, testNanoID1)
	var viol *DDLViolation
	if !errors.As(err, &viol) || viol.ViolatedConstraint != "externalEgressSensitivePlaintext" {
		t.Fatalf("a sensitive aspect readable at rest must still block external egress; got %v", err)
	}
}

// TestSensitiveTracker_LazyUndeclaredReadConsumesPlaintext: the lazy kv.Read
// seam was already consumption-keyed (a live read the script explicitly named)
// and must stay so now that the flip runs through consume() rather than as a
// side effect of decryption.
func TestSensitiveTracker_LazyUndeclaredReadConsumesPlaintext(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)

	tracker := &sensitiveReadTracker{}
	sc := ScriptContext{
		SensitiveReads: tracker,
		KVReader: connKVReader{
			conn: f.conn, bucket: testCoreBucket, ddls: f.cache, vault: f.vault,
			tracker: tracker, requestID: testNanoID1,
		},
	}
	result, err := runKVScript(t, sc, `
def execute(state, op):
    v = kv.Read("`+f.presentK+`")
    return {"mutations": [], "events": [`+consumptionExternalEvent+`]}
`)
	if err != nil {
		t.Fatalf("script must run: %v", err)
	}
	err = validateExternalEgressGuard(result, HydratedState{Context: sc}, testNanoID1)
	var viol *DDLViolation
	if !errors.As(err, &viol) || viol.ViolatedConstraint != "externalEgressSensitivePlaintext" {
		t.Fatalf("a lazy read of a sensitive aspect must reject an external emitter; got %v", err)
	}
}

// TestSensitiveTracker_NoExternalEvent_NoGuard: the guard's scope is the
// external-egress plane only. An op that consumes sensitive plaintext and emits
// ordinary domain events commits without triggering the guard.
func TestSensitiveTracker_NoExternalEvent_NoGuard(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)
	state := f.hydrate(t, "reads", f.presentK)

	result, err := runKVScript(t, state.Context, `
def execute(state, op):
    v = kv.Read("`+f.presentK+`")
    return {"mutations": [], "events": [{"class": "identity.updated", "data": {}}]}
`)
	if err != nil {
		t.Fatalf("script must run: %v", err)
	}
	if !state.Context.SensitiveReads.plaintextRead {
		t.Fatalf("the read must still be recorded as consumed")
	}
	if err := validateExternalEgressGuard(result, state, testNanoID1); err != nil {
		t.Fatalf("no external.* event means no guard: %v", err)
	}
}

// TestSensitiveTracker_FreshTrackerPerHydration: consumption state must not
// leak across hydration attempts (the OCC-retry obligation) — each Hydrate
// yields its own tracker.
func TestSensitiveTracker_FreshTrackerPerHydration(t *testing.T) {
	t.Parallel()
	f := newConsumptionFixture(t)

	first := f.hydrate(t, "reads", f.presentK)
	if _, err := runKVScript(t, first.Context, `
def execute(state, op):
    v = kv.Read("`+f.presentK+`")
    return {"mutations": [], "events": []}
`); err != nil {
		t.Fatalf("first script: %v", err)
	}
	if !first.Context.SensitiveReads.plaintextRead {
		t.Fatalf("first attempt must record the consumption")
	}

	second := f.hydrate(t, "reads", f.presentK)
	if second.Context.SensitiveReads == first.Context.SensitiveReads {
		t.Fatalf("each hydration attempt must carry its own tracker")
	}
	if second.Context.SensitiveReads.plaintextRead {
		t.Fatalf("a re-hydration must start with no consumption recorded")
	}
}
