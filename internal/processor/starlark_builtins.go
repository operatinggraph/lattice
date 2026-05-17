package processor

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"

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

// seedFromRequestID derives two uint64s from sha256(requestId). The
// PCG32 PRNG requires two seeds; we use the first 16 bytes of the hash.
func seedFromRequestID(requestID string) [2]uint64 {
	sum := sha256.Sum256([]byte(requestID))
	return [2]uint64{
		binary.BigEndian.Uint64(sum[0:8]),
		binary.BigEndian.Uint64(sum[8:16]),
	}
}

// deterministicNanoID emits an n-character NanoID from substrate.Alphabet
// using rejection sampling against a 6-bit mask, seeded from pcg.
func deterministicNanoID(src *rand.PCG, n int) string {
	const mask = 63
	alpha := substrate.Alphabet
	out := make([]byte, n)
	written := 0
	for written < n {
		// PCG.Uint64 yields 64 bits; chew through them 6 at a time.
		v := src.Uint64()
		for i := 0; i < 10 && written < n; i++ {
			b := byte(v & mask)
			v >>= 6
			if int(b) < len(alpha) {
				out[written] = alpha[b]
				written++
			}
		}
	}
	return string(out)
}

// cryptoModule returns a Starlark struct exposing:
//   - crypto.sha256(s)        → lowercase hex-encoded SHA-256 digest (64 chars)
//   - crypto.sha256NanoID(s)  → 20-char NanoID-alphabet ID derived from SHA-256(s)
//
// Both functions are pure (side-effect-free, deterministic) and consistent
// with the sandbox principles established in Story 1.6:
//   - no I/O, no os access, no time, no randomness
//   - output is fully determined by input
//
// sha256(s): stores hashes of sensitive tokens in Core KV without leaking
// plaintext. Story 4.3 will use the same builtin to hash a submitted
// plaintext for comparison.
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
		sum := sha256.Sum256([]byte(string(s)))
		// Use the SHA-256 bytes as a PCG seed to generate a valid NanoID.
		// This is deterministic: same input → same output, always.
		seed := [2]uint64{
			binary.BigEndian.Uint64(sum[0:8]),
			binary.BigEndian.Uint64(sum[8:16]),
		}
		pcg := rand.NewPCG(seed[0], seed[1])
		return starlarklib.String(deterministicNanoID(pcg, substrate.NanoIDLength)), nil
	})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"sha256":       sha256Fn,
		"sha256NanoID": sha256NanoIDFn,
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
