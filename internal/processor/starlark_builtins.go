package processor

import (
	"crypto/sha256"
	"crypto/subtle"
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
//   - crypto.sha256(s)                  → lowercase hex-encoded SHA-256 digest (64 chars)
//   - crypto.sha256NanoID(s)            → 20-char NanoID-alphabet ID derived from SHA-256(s)
//   - crypto.constant_time_equal(a, b)  → bool (constant-time byte comparison)
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

	// constant_time_equal(a, b) → bool
	//
	// Compares two strings in constant time with respect to content. Length
	// mismatch returns False immediately (not constant-time across length, but
	// timing reveals only length, not content — acceptable Phase 1; document
	// as a known limitation in the closing summary).
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
		"sha256":               sha256Fn,
		"sha256NanoID":         sha256NanoIDFn,
		"constant_time_equal":  constantTimeEqualFn,
	})
}

// stringsModule returns a Starlark struct exposing pure string-math helpers:
//   - strings.levenshtein(a, b)       → int   — classical DP edit distance
//   - strings.levenshtein_ratio(a, b) → float — 1 − dist / max(len(a), len(b))
//
// Both functions are pure, deterministic, side-effect-free (no I/O, no time,
// no randomness) — consistent with Story 1.6 sandbox principles. The DP
// implementation uses a rolling-row approach: O(len(a)*len(b)) time,
// O(min(len(a),len(b))) space.
//
// levenshtein_ratio edge case: both empty strings → returns 1.0 (they are
// identical). One empty + one non-empty → ratio = 0.0.
func stringsModule() *starlarkstruct.Struct {
	levenshteinFn := starlarklib.NewBuiltin("levenshtein", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 2 || len(kwargs) != 0 {
			return nil, errBuiltin("strings.levenshtein(a, b) takes exactly 2 positional arguments")
		}
		a, aOK := args[0].(starlarklib.String)
		b, bOK := args[1].(starlarklib.String)
		if !aOK {
			return nil, errBuiltin("strings.levenshtein: first argument must be a string, got " + args[0].Type())
		}
		if !bOK {
			return nil, errBuiltin("strings.levenshtein: second argument must be a string, got " + args[1].Type())
		}
		return starlarklib.MakeInt(levenshteinDistance(string(a), string(b))), nil
	})

	levenshteinRatioFn := starlarklib.NewBuiltin("levenshtein_ratio", func(_ *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 2 || len(kwargs) != 0 {
			return nil, errBuiltin("strings.levenshtein_ratio(a, b) takes exactly 2 positional arguments")
		}
		a, aOK := args[0].(starlarklib.String)
		b, bOK := args[1].(starlarklib.String)
		if !aOK {
			return nil, errBuiltin("strings.levenshtein_ratio: first argument must be a string, got " + args[0].Type())
		}
		if !bOK {
			return nil, errBuiltin("strings.levenshtein_ratio: second argument must be a string, got " + args[1].Type())
		}
		la, lb := len(string(a)), len(string(b))
		maxLen := la
		if lb > maxLen {
			maxLen = lb
		}
		if maxLen == 0 {
			return starlarklib.Float(1.0), nil
		}
		dist := levenshteinDistance(string(a), string(b))
		ratio := 1.0 - float64(dist)/float64(maxLen)
		return starlarklib.Float(ratio), nil
	})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"levenshtein":       levenshteinFn,
		"levenshtein_ratio": levenshteinRatioFn,
	})
}

// levenshteinDistance computes the classical Wagner-Fischer edit distance
// between strings a and b. Uses a rolling-row approach for O(min(|a|,|b|))
// space. Cost: insert=1, delete=1, substitute=1.
func levenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	// Ensure a is the shorter string for the rolling-row optimisation.
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	n, m := len(ra), len(rb)
	if n == 0 {
		return m
	}
	// prev[j] = distance between ra[0..i-1] and rb[0..j-1] after last row.
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if rb[i-1] == ra[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = del
			if ins < curr[j] {
				curr[j] = ins
			}
			if sub < curr[j] {
				curr[j] = sub
			}
		}
		prev, curr = curr, prev
	}
	return prev[n]
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
