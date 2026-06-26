package processor

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"time"

	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/asolgan/lattice/internal/substrate"
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
// Both functions are pure (side-effect-free, deterministic) per sandbox rules:
//   - no I/O, no os access, no time, no randomness
//   - output is fully determined by input
//
// sha256(s): stores hashes of sensitive tokens in Core KV without leaking
// plaintext. Used to hash submitted plaintext for comparison in ClaimIdentity.
//
// sha256NanoID(s): derives a valid Contract #1 NanoID from SHA-256(s),
// used to build deterministic index-vertex keys (vtx.identityIndex.<id>)
// that satisfy substrate.ClassifyKey (which requires NanoID-alphabet chars
// in the 3rd segment). The contact-type prefix in the hash input (e.g.
// "email:..." vs "phone:...") prevents cross-type collisions.
func cryptoModule() *starlarkstruct.Struct {
	sha256Fn := starlarklib.NewBuiltin("sha256", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, errBuiltin("crypto.sha256(s) takes exactly 1 positional argument")
		}
		s, ok := args[0].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("crypto.sha256: argument must be a string, got " + args[0].Type())
		}
		sum := sha256.Sum256([]byte(string(s)))
		return starlarklib.String(hex.EncodeToString(sum[:])), nil
	})

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

	// constant_time_equal(a, b) → bool
	//
	// Compares two strings in constant time with respect to content. Both
	// operands MUST be fixed-length (e.g., NanoIDs or other fixed-size tokens).
	// Length mismatch returns False immediately — timing leaks the length of
	// the shorter operand. Do NOT use with variable-length secrets (passwords,
	// arbitrary user input) where length must also be kept confidential.
	//
	// Implementation: crypto/subtle.ConstantTimeCompare (stdlib). Returns True
	// iff both strings are identical, False otherwise. Sandboxed: pure function,
	// no I/O, no OS access, no randomness.
	constantTimeEqualFn := starlarklib.NewBuiltin("constant_time_equal", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 2 || len(kwargs) != 0 {
			return nil, errBuiltin("crypto.constant_time_equal(a, b) takes exactly 2 positional arguments")
		}
		a, aOK := args[0].(starlarklib.String)
		b, bOK := args[1].(starlarklib.String)
		if !aOK {
			return nil, errBuiltin("crypto.constant_time_equal: first argument must be a string, got " + args[0].Type())
		}
		if !bOK {
			return nil, errBuiltin("crypto.constant_time_equal: second argument must be a string, got " + args[1].Type())
		}
		eq := subtle.ConstantTimeCompare([]byte(string(a)), []byte(string(b))) == 1
		return starlarklib.Bool(eq), nil
	})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"sha256":              sha256Fn,
		"sha256NanoID":        sha256NanoIDFn,
		"constant_time_equal": constantTimeEqualFn,
	})
}

// timeModule returns a Starlark struct exposing:
//   - time.rfc3339_utc(s) → s parsed as an RFC3339 timestamp and re-emitted
//     in canonical UTC form (whole seconds, "Z" suffix), e.g.
//     "2026-06-04T23:00:00+09:00" → "2026-06-04T14:00:00Z".
//   - time.rfc3339_add(s, duration) → s parsed as RFC3339, advanced by a Go
//     duration string (time.ParseDuration, e.g. "720h", "90s"), re-emitted in
//     canonical UTC form. A negative duration ("-1h") subtracts.
//
// Both are pure (deterministic, no I/O, no wall-clock read): the output is a
// function of the input string(s) only — the host clock is never consulted.
// This is the same sandbox-safe builtin pattern as crypto.sha256 / nanoid.
//
// The canonical form matches the format the Refractor populates `$now` with
// (`time.Now().UTC().Format(time.RFC3339)`), so a lens cypher comparing
// `task.data.expiresAt > $now` lexically is sound: both sides are UTC,
// whole-second, "Z"-suffixed RFC3339. Callers normalize timestamps before
// they are stored as task scalars.
//
// rfc3339_add lets a read-free op precompute a deadline from its own
// completedAt without consulting the clock (e.g. a freshness window
// `validUntil = completedAt + window`); the result is whole-second UTC RFC3339,
// directly comparable against `$now` by the same lexical rule.
//
//   - time.weekday(s) → int 0..6, the UTC weekday of s (Sunday=0 … Saturday=6,
//     matching Go's time.Weekday). Lets an op test "what day of the week is this
//     instant" without a clock — e.g. provider-availability windows.
//   - time.seconds_of_day(s) → int 0..86399, the UTC seconds-since-midnight of s
//     (h*3600 + m*60 + sec). The time-of-day companion to weekday; integers
//     compare exactly (no mixed-width "HH:MM" lexical-prefix hazard).
//
// Both are pure too: weekday / seconds_of_day are a function of the instant only,
// taken in UTC (the host zone is never consulted), so a replayed operation
// reproduces byte-identical output.
//
// A malformed input raises a Starlark error; CreateTask surfaces it as a
// structured ScriptError ("InvalidArgument: expiresAt: ...").
func timeModule() *starlarkstruct.Struct {
	rfc3339UTCFn := starlarklib.NewBuiltin("rfc3339_utc", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, errBuiltin("time.rfc3339_utc(s) takes exactly 1 positional argument")
		}
		s, ok := args[0].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("time.rfc3339_utc: argument must be a string, got " + args[0].Type())
		}
		// Accept both whole-second and fractional-second RFC3339; both
		// normalize to whole-second UTC. RFC3339Nano parses both forms.
		t, err := time.Parse(time.RFC3339Nano, string(s))
		if err != nil {
			return nil, errBuiltin("InvalidArgument: not a valid RFC3339 timestamp: " + string(s))
		}
		return starlarklib.String(t.UTC().Format(time.RFC3339)), nil
	})

	rfc3339AddFn := starlarklib.NewBuiltin("rfc3339_add", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 2 || len(kwargs) != 0 {
			return nil, errBuiltin("time.rfc3339_add(s, duration) takes exactly 2 positional arguments")
		}
		s, ok := args[0].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("time.rfc3339_add: first argument must be a string, got " + args[0].Type())
		}
		durStr, ok := args[1].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("time.rfc3339_add: second argument must be a string, got " + args[1].Type())
		}
		// Same parse surface as rfc3339_utc: accept whole- and fractional-second
		// RFC3339, normalize the result to whole-second UTC.
		t, err := time.Parse(time.RFC3339Nano, string(s))
		if err != nil {
			return nil, errBuiltin("InvalidArgument: not a valid RFC3339 timestamp: " + string(s))
		}
		d, err := time.ParseDuration(string(durStr))
		if err != nil {
			return nil, errBuiltin("InvalidArgument: not a valid Go duration: " + string(durStr))
		}
		return starlarklib.String(t.Add(d).UTC().Format(time.RFC3339)), nil
	})

	weekdayFn := starlarklib.NewBuiltin("weekday", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, errBuiltin("time.weekday(s) takes exactly 1 positional argument")
		}
		s, ok := args[0].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("time.weekday: argument must be a string, got " + args[0].Type())
		}
		t, err := time.Parse(time.RFC3339Nano, string(s))
		if err != nil {
			return nil, errBuiltin("InvalidArgument: not a valid RFC3339 timestamp: " + string(s))
		}
		return starlarklib.MakeInt(int(t.UTC().Weekday())), nil
	})

	secondsOfDayFn := starlarklib.NewBuiltin("seconds_of_day", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, errBuiltin("time.seconds_of_day(s) takes exactly 1 positional argument")
		}
		s, ok := args[0].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("time.seconds_of_day: argument must be a string, got " + args[0].Type())
		}
		t, err := time.Parse(time.RFC3339Nano, string(s))
		if err != nil {
			return nil, errBuiltin("InvalidArgument: not a valid RFC3339 timestamp: " + string(s))
		}
		u := t.UTC()
		return starlarklib.MakeInt(u.Hour()*3600 + u.Minute()*60 + u.Second()), nil
	})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"rfc3339_utc":    rfc3339UTCFn,
		"rfc3339_add":    rfc3339AddFn,
		"weekday":        weekdayFn,
		"seconds_of_day": secondsOfDayFn,
	})
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
