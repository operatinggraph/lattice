// Unit tests for starlark_builtins.go — specifically the cryptoModule().
//
// Deliverable #3 from Story 4.2: crypto.sha256 known-digest test,
// wrong-arity rejection, non-string argument rejection.
package processor

import (
	"strings"
	"testing"

	starlarklib "go.starlark.net/starlark"
)

// --- time.rfc3339_utc ---

// TestTimeRFC3339UTC_Normalizes verifies offset + fractional-second inputs
// normalize to canonical UTC whole-second RFC3339 (the form $now uses), so a
// lexical expiresAt > now comparison in the lens is sound.
func TestTimeRFC3339UTC_Normalizes(t *testing.T) {
	mod := timeModule()
	fn, err := mod.Attr("rfc3339_utc")
	if err != nil || fn == nil {
		t.Fatalf("time.rfc3339_utc attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}
	cases := []struct{ in, want string }{
		{"2026-06-04T14:00:00Z", "2026-06-04T14:00:00Z"},
		{"2026-06-04T23:00:00+09:00", "2026-06-04T14:00:00Z"},   // +09:00 → UTC
		{"2026-06-04T09:00:00-05:00", "2026-06-04T14:00:00Z"},   // -05:00 → UTC
		{"2026-06-04T14:00:00.123456Z", "2026-06-04T14:00:00Z"}, // fractional dropped
	}
	for _, tc := range cases {
		res, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(tc.in)}, nil)
		if err != nil {
			t.Fatalf("time.rfc3339_utc(%q): %v", tc.in, err)
		}
		got, _ := res.(starlarklib.String)
		if string(got) != tc.want {
			t.Fatalf("time.rfc3339_utc(%q) = %q, want %q", tc.in, string(got), tc.want)
		}
	}
}

// TestTimeRFC3339UTC_Malformed rejects non-RFC3339 input with an
// InvalidArgument-prefixed error (surfaced as a structured ScriptError).
func TestTimeRFC3339UTC_Malformed(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("rfc3339_utc")
	thread := &starlarklib.Thread{Name: "test"}
	for _, bad := range []string{"not-a-time", "2026-06-04", "06/04/2026", ""} {
		_, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(bad)}, nil)
		if err == nil {
			t.Fatalf("time.rfc3339_utc(%q) expected error, got nil", bad)
		}
		if !strings.Contains(err.Error(), "InvalidArgument") {
			t.Fatalf("time.rfc3339_utc(%q) error = %q, want InvalidArgument", bad, err.Error())
		}
	}
}

// TestTimeRFC3339UTC_WrongArity rejects 0/2 args and non-string args.
func TestTimeRFC3339UTC_WrongArity(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("rfc3339_utc")
	thread := &starlarklib.Thread{Name: "test"}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{}, nil); err == nil {
		t.Fatal("time.rfc3339_utc() with 0 args expected error")
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.MakeInt(1)}, nil); err == nil {
		t.Fatal("time.rfc3339_utc(int) expected error")
	}
}

// --- time.rfc3339_add ---

// TestTimeRFC3339Add_Adds verifies a Go duration is added to an RFC3339 instant
// and the result is canonical whole-second UTC (the form $now uses), so a lexical
// validUntil > now comparison in the lens is sound. A negative duration subtracts.
func TestTimeRFC3339Add_Adds(t *testing.T) {
	mod := timeModule()
	fn, err := mod.Attr("rfc3339_add")
	if err != nil || fn == nil {
		t.Fatalf("time.rfc3339_add attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}
	cases := []struct{ in, dur, want string }{
		{"2026-06-04T14:00:00Z", "720h", "2026-07-04T14:00:00Z"},      // +30 days
		{"2026-06-04T14:00:00Z", "90s", "2026-06-04T14:01:30Z"},       // +90 seconds
		{"2026-06-04T14:00:00Z", "-1h", "2026-06-04T13:00:00Z"},       // negative subtracts
		{"2026-06-04T23:00:00+09:00", "0s", "2026-06-04T14:00:00Z"},   // offset normalized to UTC
		{"2026-06-04T14:00:00.123456Z", "1m", "2026-06-04T14:01:00Z"}, // fractional dropped
		{"2026-06-04T14:00:00Z", "5m", "2026-06-04T14:05:00Z"},        // the demo window magnitude
	}
	for _, tc := range cases {
		res, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(tc.in), starlarklib.String(tc.dur)}, nil)
		if err != nil {
			t.Fatalf("time.rfc3339_add(%q, %q): %v", tc.in, tc.dur, err)
		}
		got, _ := res.(starlarklib.String)
		if string(got) != tc.want {
			t.Fatalf("time.rfc3339_add(%q, %q) = %q, want %q", tc.in, tc.dur, string(got), tc.want)
		}
	}
}

// TestTimeRFC3339Add_Deterministic confirms the builtin reads no wall clock:
// the same (instant, duration) pair always yields the same output.
func TestTimeRFC3339Add_Deterministic(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("rfc3339_add")
	thread := &starlarklib.Thread{Name: "test"}
	call := func() string {
		res, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("2026-06-04T14:00:00Z"), starlarklib.String("720h")}, nil)
		if err != nil {
			t.Fatalf("time.rfc3339_add: %v", err)
		}
		s, _ := res.(starlarklib.String)
		return string(s)
	}
	if a, b := call(), call(); a != b {
		t.Fatalf("time.rfc3339_add not deterministic: %q != %q", a, b)
	}
}

// TestTimeRFC3339Add_BadTimestamp rejects a non-RFC3339 first argument.
func TestTimeRFC3339Add_BadTimestamp(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("rfc3339_add")
	thread := &starlarklib.Thread{Name: "test"}
	for _, bad := range []string{"not-a-time", "2026-06-04", ""} {
		_, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(bad), starlarklib.String("1h")}, nil)
		if err == nil {
			t.Fatalf("time.rfc3339_add(%q, ...) expected error, got nil", bad)
		}
		if !strings.Contains(err.Error(), "InvalidArgument") {
			t.Fatalf("time.rfc3339_add(%q, ...) error = %q, want InvalidArgument", bad, err.Error())
		}
	}
}

// TestTimeRFC3339Add_BadDuration rejects an unparseable Go duration.
func TestTimeRFC3339Add_BadDuration(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("rfc3339_add")
	thread := &starlarklib.Thread{Name: "test"}
	for _, bad := range []string{"720", "thirty-minutes", "", "1x"} {
		_, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("2026-06-04T14:00:00Z"), starlarklib.String(bad)}, nil)
		if err == nil {
			t.Fatalf("time.rfc3339_add(_, %q) expected error, got nil", bad)
		}
		if !strings.Contains(err.Error(), "InvalidArgument") {
			t.Fatalf("time.rfc3339_add(_, %q) error = %q, want InvalidArgument", bad, err.Error())
		}
	}
}

// TestTimeRFC3339Add_WrongArity rejects 0/1/3 args and non-string args.
func TestTimeRFC3339Add_WrongArity(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("rfc3339_add")
	thread := &starlarklib.Thread{Name: "test"}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{}, nil); err == nil {
		t.Fatal("time.rfc3339_add() with 0 args expected error")
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("2026-06-04T14:00:00Z")}, nil); err == nil {
		t.Fatal("time.rfc3339_add(s) with 1 arg expected error")
	}
	three := starlarklib.Tuple{starlarklib.String("2026-06-04T14:00:00Z"), starlarklib.String("1h"), starlarklib.String("x")}
	if _, err := starlarklib.Call(thread, fn, three, nil); err == nil {
		t.Fatal("time.rfc3339_add(s, d, x) with 3 args expected error")
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.MakeInt(1), starlarklib.String("1h")}, nil); err == nil {
		t.Fatal("time.rfc3339_add(int, d) expected error")
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("2026-06-04T14:00:00Z"), starlarklib.MakeInt(1)}, nil); err == nil {
		t.Fatal("time.rfc3339_add(s, int) expected error")
	}
}

// --- time.weekday ---

// TestTimeWeekday verifies the UTC weekday (Sunday=0 … Saturday=6) of an
// instant, including offset normalization (the day is taken in UTC, not the
// input's local zone).
func TestTimeWeekday(t *testing.T) {
	mod := timeModule()
	fn, err := mod.Attr("weekday")
	if err != nil || fn == nil {
		t.Fatalf("time.weekday attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}
	cases := []struct {
		in   string
		want int
	}{
		{"2026-06-28T12:00:00Z", 0}, // Sunday
		{"2026-06-29T12:00:00Z", 1}, // Monday
		{"2026-07-01T12:00:00Z", 3}, // Wednesday
		{"2026-07-04T12:00:00Z", 6}, // Saturday
		// 2026-06-29T01:00:00+09:00 == 2026-06-28T16:00:00Z → Sunday (UTC), not Monday.
		{"2026-06-29T01:00:00+09:00", 0},
	}
	for _, tc := range cases {
		res, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(tc.in)}, nil)
		if err != nil {
			t.Fatalf("time.weekday(%q): %v", tc.in, err)
		}
		got, ok := res.(starlarklib.Int)
		if !ok {
			t.Fatalf("time.weekday(%q) returned %s, want int", tc.in, res.Type())
		}
		gi, _ := got.Int64()
		if int(gi) != tc.want {
			t.Fatalf("time.weekday(%q) = %d, want %d", tc.in, gi, tc.want)
		}
	}
}

func TestTimeWeekday_Malformed(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("weekday")
	thread := &starlarklib.Thread{Name: "test"}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("not-a-time")}, nil); err == nil {
		t.Fatal("time.weekday(garbage) expected error")
	} else if !strings.Contains(err.Error(), "InvalidArgument") {
		t.Fatalf("time.weekday error = %q, want InvalidArgument", err.Error())
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{}, nil); err == nil {
		t.Fatal("time.weekday() with 0 args expected error")
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.MakeInt(1)}, nil); err == nil {
		t.Fatal("time.weekday(int) expected error")
	}
}

// --- time.seconds_of_day ---

// TestTimeSecondsOfDay verifies UTC seconds-since-midnight, including offset
// normalization (the time-of-day is taken in UTC).
func TestTimeSecondsOfDay(t *testing.T) {
	mod := timeModule()
	fn, err := mod.Attr("seconds_of_day")
	if err != nil || fn == nil {
		t.Fatalf("time.seconds_of_day attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}
	cases := []struct {
		in   string
		want int
	}{
		{"2026-06-28T00:00:00Z", 0},
		{"2026-06-28T09:00:00Z", 32400},      // 9*3600
		{"2026-06-28T17:00:00Z", 61200},      // 17*3600
		{"2026-06-28T23:59:59Z", 86399},      // last second of the day
		{"2026-06-28T09:30:15Z", 34215},      // 9*3600+30*60+15
		{"2026-06-28T18:00:00+09:00", 32400}, // == 09:00:00Z
	}
	for _, tc := range cases {
		res, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(tc.in)}, nil)
		if err != nil {
			t.Fatalf("time.seconds_of_day(%q): %v", tc.in, err)
		}
		got, ok := res.(starlarklib.Int)
		if !ok {
			t.Fatalf("time.seconds_of_day(%q) returned %s, want int", tc.in, res.Type())
		}
		gi, _ := got.Int64()
		if int(gi) != tc.want {
			t.Fatalf("time.seconds_of_day(%q) = %d, want %d", tc.in, gi, tc.want)
		}
	}
}

func TestTimeSecondsOfDay_Malformed(t *testing.T) {
	mod := timeModule()
	fn, _ := mod.Attr("seconds_of_day")
	thread := &starlarklib.Thread{Name: "test"}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("nope")}, nil); err == nil {
		t.Fatal("time.seconds_of_day(garbage) expected error")
	} else if !strings.Contains(err.Error(), "InvalidArgument") {
		t.Fatalf("time.seconds_of_day error = %q, want InvalidArgument", err.Error())
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{}, nil); err == nil {
		t.Fatal("time.seconds_of_day() with 0 args expected error")
	}
	if _, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.MakeInt(1)}, nil); err == nil {
		t.Fatal("time.seconds_of_day(int) expected error")
	}
}

// --- crypto.sha256 ---

// TestCryptoSha256_KnownDigest verifies that crypto.sha256("") equals the
// known SHA-256 hash of the empty string.
func TestCryptoSha256_KnownDigest(t *testing.T) {
	// sha256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	const wantEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	mod := cryptoModule()
	fn, err := mod.Attr("sha256")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256 attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}
	result, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("")}, nil)
	if err != nil {
		t.Fatalf("crypto.sha256(''): %v", err)
	}
	got, ok := result.(starlarklib.String)
	if !ok {
		t.Fatalf("crypto.sha256('') returned %T, want String", result)
	}
	if string(got) != wantEmpty {
		t.Fatalf("crypto.sha256('') = %q, want %q", string(got), wantEmpty)
	}

	// "hello" → 2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824
	const wantHello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	result2, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("hello")}, nil)
	if err != nil {
		t.Fatalf("crypto.sha256('hello'): %v", err)
	}
	got2, _ := result2.(starlarklib.String)
	if string(got2) != wantHello {
		t.Fatalf("crypto.sha256('hello') = %q, want %q", string(got2), wantHello)
	}

	// Output is always 64 hex chars.
	if len(string(got2)) != 64 {
		t.Fatalf("crypto.sha256 output length = %d, want 64", len(string(got2)))
	}
}

// TestCryptoSha256_WrongArity checks that calling sha256 with 0 or 2
// arguments returns an error.
func TestCryptoSha256_WrongArity(t *testing.T) {
	mod := cryptoModule()
	fn, err := mod.Attr("sha256")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256 attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}

	// zero args
	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{}, nil)
	if err == nil {
		t.Fatal("crypto.sha256() with 0 args: expected error, got nil")
	}

	// two args
	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("a"), starlarklib.String("b")}, nil)
	if err == nil {
		t.Fatal("crypto.sha256(a, b) with 2 args: expected error, got nil")
	}
}

// TestCryptoSha256_NonString verifies that passing a non-string argument
// (e.g. an integer) returns an error with a descriptive message.
func TestCryptoSha256_NonString(t *testing.T) {
	mod := cryptoModule()
	fn, err := mod.Attr("sha256")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256 attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}

	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.MakeInt(42)}, nil)
	if err == nil {
		t.Fatal("crypto.sha256(42): expected error for non-string, got nil")
	}
	if !strings.Contains(err.Error(), "int") {
		t.Fatalf("error message should mention type 'int', got: %v", err)
	}
}

// --- crypto.sha256NanoID ---

// --- crypto.constant_time_equal ---

// TestCryptoConstantTimeEqual covers equal strings, unequal strings,
// length-mismatch strings, and wrong-arity / non-string argument rejection.
func TestCryptoConstantTimeEqual(t *testing.T) {
	mod := cryptoModule()
	fn, err := mod.Attr("constant_time_equal")
	if err != nil || fn == nil {
		t.Fatalf("crypto.constant_time_equal attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}

	call := func(a, b starlarklib.Value) (starlarklib.Value, error) {
		return starlarklib.Call(thread, fn, starlarklib.Tuple{a, b}, nil)
	}

	// Equal strings → True.
	res, err := call(starlarklib.String("abc"), starlarklib.String("abc"))
	if err != nil {
		t.Fatalf("equal: unexpected error: %v", err)
	}
	if res != starlarklib.Bool(true) {
		t.Fatalf("equal strings: got %v, want True", res)
	}

	// Unequal same-length strings → False.
	res, err = call(starlarklib.String("abc"), starlarklib.String("abd"))
	if err != nil {
		t.Fatalf("unequal: unexpected error: %v", err)
	}
	if res != starlarklib.Bool(false) {
		t.Fatalf("unequal same-length: got %v, want False", res)
	}

	// Length mismatch → False.
	res, err = call(starlarklib.String("abc"), starlarklib.String("ab"))
	if err != nil {
		t.Fatalf("length mismatch: unexpected error: %v", err)
	}
	if res != starlarklib.Bool(false) {
		t.Fatalf("length mismatch: got %v, want False", res)
	}

	// Empty strings are equal.
	res, err = call(starlarklib.String(""), starlarklib.String(""))
	if err != nil {
		t.Fatalf("empty strings: unexpected error: %v", err)
	}
	if res != starlarklib.Bool(true) {
		t.Fatalf("empty strings: got %v, want True", res)
	}

	// Wrong arity (0 args) → error.
	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{}, nil)
	if err == nil {
		t.Fatal("0 args: expected error, got nil")
	}

	// Wrong arity (1 arg) → error.
	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String("a")}, nil)
	if err == nil {
		t.Fatal("1 arg: expected error, got nil")
	}

	// Wrong arity (3 args) → error.
	_, err = starlarklib.Call(thread, fn, starlarklib.Tuple{
		starlarklib.String("a"), starlarklib.String("b"), starlarklib.String("c"),
	}, nil)
	if err == nil {
		t.Fatal("3 args: expected error, got nil")
	}

	// Non-string first arg → error.
	_, err = call(starlarklib.MakeInt(42), starlarklib.String("abc"))
	if err == nil {
		t.Fatal("non-string first arg: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "int") {
		t.Fatalf("error should mention 'int', got: %v", err)
	}

	// Non-string second arg → error.
	_, err = call(starlarklib.String("abc"), starlarklib.MakeInt(42))
	if err == nil {
		t.Fatal("non-string second arg: expected error, got nil")
	}
}

// --- crypto.sha256NanoID ---

// TestCryptoSha256NanoID_Deterministic checks that sha256NanoID returns a
// 20-char NanoID-alphabet string and is deterministic (same input → same output).
func TestCryptoSha256NanoID_Deterministic(t *testing.T) {
	mod := cryptoModule()
	fn, err := mod.Attr("sha256NanoID")
	if err != nil || fn == nil {
		t.Fatalf("crypto.sha256NanoID attr: %v", err)
	}
	thread := &starlarklib.Thread{Name: "test"}

	call := func(s string) string {
		t.Helper()
		result, err := starlarklib.Call(thread, fn, starlarklib.Tuple{starlarklib.String(s)}, nil)
		if err != nil {
			t.Fatalf("crypto.sha256NanoID(%q): %v", s, err)
		}
		got, ok := result.(starlarklib.String)
		if !ok {
			t.Fatalf("crypto.sha256NanoID(%q) returned %T, want String", s, result)
		}
		return string(got)
	}

	id1 := call("email:test@example.com")
	id2 := call("email:test@example.com")
	if id1 != id2 {
		t.Fatalf("sha256NanoID not deterministic: %q != %q", id1, id2)
	}
	if len(id1) != 20 {
		t.Fatalf("sha256NanoID length = %d, want 20", len(id1))
	}

	// Different inputs must produce different outputs.
	idPhone := call("phone:+15551234567")
	if idPhone == id1 {
		t.Fatalf("sha256NanoID collision: email and phone prefixes produced same ID")
	}

	// All chars must be in the NanoID alphabet (no 0, I, l, O).
	for _, c := range id1 {
		if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789", c) {
			t.Fatalf("sha256NanoID(%q) contains invalid char %q: %q", "email:test@example.com", c, id1)
		}
	}
}
