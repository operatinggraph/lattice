package processor

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/rand/v2"

	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// nanoidModule returns a Starlark struct exposing `nanoid.new()` and
// `nanoid.short()`. The PRNG is seeded deterministically from the
// operation's requestId so retries (or replays from the same envelope)
// produce identical NanoID sequences. Two calls within the same
// invocation produce different IDs (the PRNG advances), but the
// sequence is reproducible.
//
// Determinism rationale (Contract #3 §3.6 + brief decision #12): the
// commit path is at-least-once. Idempotent step 8 commits depend on
// the script's outputs being byte-equal between invocations of the
// same operation.
//
// Not part of internal/starlarksandbox's pure module set: it depends on
// substrate.NanoIDFromPCG (an internal package the zero-internal-dep leaf
// does not import) and is per-invocation-seeded (bound to a requestID),
// unlike the leaf's stateless crypto/time builtins.
func nanoidModule(requestID string) *starlarkstruct.Struct {
	seed := seedFromRequestID(requestID)
	// Independent counter per builtin so call order between new() and
	// short() doesn't entangle their streams. The PRNG state advances
	// as a single shared stream — that's fine, only determinism for
	// the same `requestId` matters.
	pcg := rand.NewPCG(seed[0], seed[1])

	newFn := starlarklib.NewBuiltin("new", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 0 || len(kwargs) != 0 {
			return nil, errBuiltin("nanoid.new() takes no arguments")
		}
		return starlarklib.String(deterministicNanoID(pcg, substrate.NanoIDLength)), nil
	})

	shortFn := starlarklib.NewBuiltin("short", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 0 || len(kwargs) != 0 {
			return nil, errBuiltin("nanoid.short() takes no arguments")
		}
		return starlarklib.String(deterministicNanoID(pcg, substrate.ShortCodeLength)), nil
	})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"new":   newFn,
		"short": shortFn,
	})
}

func errBuiltin(msg string) error {
	return &starlarklib.EvalError{Msg: msg}
}

// SeedFromRequestID derives two uint64s from sha256(requestId). The
// PCG32 PRNG requires two seeds; we use the first 16 bytes of the hash.
// Exported so external test packages can reproduce NanoIDs generated
// by the Starlark `nanoid.new()` builtin for a given requestId — the
// same algorithm the Processor uses at commit time.
func SeedFromRequestID(requestID string) [2]uint64 {
	sum := sha256.Sum256([]byte(requestID))
	return [2]uint64{
		binary.BigEndian.Uint64(sum[0:8]),
		binary.BigEndian.Uint64(sum[8:16]),
	}
}

// seedFromRequestID is the unexported alias retained for internal callers.
func seedFromRequestID(requestID string) [2]uint64 {
	return SeedFromRequestID(requestID)
}

// DeterministicNanoID emits an n-character NanoID from substrate.Alphabet
// using rejection sampling against a 6-bit mask, seeded from pcg.
// Exported so external test packages can reproduce the NanoIDs generated
// by `nanoid.new()` / `nanoid.short()` at Starlark runtime.
func DeterministicNanoID(src *rand.PCG, n int) string {
	return deterministicNanoID(src, n)
}

// deterministicNanoID emits an n-character NanoID from substrate.Alphabet using
// rejection sampling against a 6-bit mask, seeded from pcg. The generation is
// owned by substrate (NanoIDFromPCG) so a Go-side caller and a Starlark script
// derive byte-identical ids from the same seed.
func deterministicNanoID(src *rand.PCG, n int) string {
	return substrate.NanoIDFromPCG(src, n)
}

// cryptoModule returns a Starlark struct exposing:
//   - crypto.sha256(s)                  → lowercase hex-encoded SHA-256 digest (64 chars)
//   - crypto.sha256NanoID(s)            → 20-char NanoID-alphabet ID derived from SHA-256(s)
//   - crypto.constant_time_equal(a, b)  → bool (constant-time byte comparison)
//
// sha256 and constant_time_equal are internal/starlarksandbox.CryptoBuiltins
// — pure, zero-internal-dep. sha256NanoID is Processor-specific: it derives a
// valid Contract #1 NanoID from SHA-256(s), used to build deterministic
// index-vertex keys (vtx.identityIndex.<id>) that satisfy substrate.ClassifyKey
// (which requires NanoID-alphabet chars in the 3rd segment); it depends on
// substrate (an internal package), which is why it is composed in here rather
// than living in the leaf. The contact-type prefix in the hash input (e.g.
// "email:..." vs "phone:...") prevents cross-type collisions.
func cryptoModule() *starlarkstruct.Struct {
	builtins := starlarksandbox.CryptoBuiltins()

	sha256NanoIDFn := starlarklib.NewBuiltin("sha256NanoID", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, errBuiltin("crypto.sha256NanoID(s) takes exactly 1 positional argument")
		}
		s, ok := args[0].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("crypto.sha256NanoID: argument must be a string, got " + args[0].Type())
		}
		// Deterministic content-addressed id, owned by substrate so a Go-side
		// client (Loupe / the object GC) derives the identical id off-script.
		return starlarklib.String(substrate.SHA256NanoID(string(s))), nil
	})
	builtins["sha256NanoID"] = sha256NanoIDFn

	return starlarkstruct.FromStringDict(starlarkstruct.Default, builtins)
}

// timeModule returns a Starlark struct exposing:
//   - time.rfc3339_utc(s) → s parsed as an RFC3339 timestamp and re-emitted
//     in canonical UTC form (whole seconds, "Z" suffix), e.g.
//     "2026-06-04T23:00:00+09:00" → "2026-06-04T14:00:00Z".
//   - time.rfc3339_add(s, duration) → s parsed as RFC3339, advanced by a Go
//     duration string (time.ParseDuration, e.g. "720h", "90s"), re-emitted in
//     canonical UTC form. A negative duration ("-1h") subtracts.
//   - time.weekday(s) → int 0..6, the UTC weekday of s (Sunday=0 … Saturday=6,
//     matching Go's time.Weekday).
//   - time.seconds_of_day(s) → int 0..86399, the UTC seconds-since-midnight of
//     s (h*3600 + m*60 + sec).
//
// All four are internal/starlarksandbox.TimeBuiltins — pure (deterministic,
// no I/O, no wall-clock read): the output is a function of the input
// string(s) only, the host clock is never consulted. The canonical form
// matches the format the Refractor populates `$now` with
// (`time.Now().UTC().Format(time.RFC3339)`), so a lens cypher comparing
// `task.data.expiresAt > $now` lexically is sound. A malformed input raises
// a Starlark error; CreateTask surfaces it as a structured ScriptError
// ("InvalidArgument: expiresAt: ...").
func timeModule() *starlarkstruct.Struct {
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarksandbox.TimeBuiltins())
}

// jsonToGenericMap parses raw JSON into map[string]interface{}.
// Returns (nil, false) for non-object payloads.
func jsonToGenericMap(raw []byte) (map[string]interface{}, bool) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}
