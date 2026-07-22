package loom_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/loom"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedValueInterfaceSubject writes the fixture the starlark Value-interface
// tests below share: a root identity vertex with two data fields (so dict
// rendering/iteration has a deterministic multi-key shape — GoMapToStarlarkDict
// inserts keys sorted), a `profile` aspect with data, and an `empty` aspect
// whose data envelope is an empty object (so a data dict with Truth() == False
// is reachable through a real guard).
func seedValueInterfaceSubject(t *testing.T, ctx context.Context, conn *substrate.Conn) string {
	t.Helper()
	subjectKey := "vtx.identity." + mustNanoID(t)
	rootBody, err := json.Marshal(map[string]any{
		"class": "identity",
		"data":  map[string]any{"age": 21, "name": "Ada"},
	})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, coreKVBucket, subjectKey, rootBody)
	require.NoError(t, err)
	seedAspect(t, ctx, conn, subjectKey, "profile", map[string]any{"name": "Ada"})
	seedAspect(t, ctx, conn, subjectKey, "empty", map[string]any{})
	return subjectKey
}

// TestGuardEval_Starlark_ValueInterfaceTable drives the starlark Value
// interface surface of the guard's `subject` / `subject.data` wrappers
// (guard_starlark.go's starlarkSubject / starlarkDataDict) through real guard
// predicates — str() (String), bool() (Truth), type() (Type), dir()
// (AttrNames), comprehension iteration (Iterate), and the class/isDeleted
// attribute arms — all shapes a guard author can legally write (design doc
// §2.2 resolves the guard grammar as ordinary Starlark, so every builtin that
// dispatches on these methods is reachable from a predicate body).
func TestGuardEval_Starlark_ValueInterfaceTable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	subjectKey := seedValueInterfaceSubject(t, ctx, conn)

	eval := func(t *testing.T, raw string) bool {
		t.Helper()
		g, perr := loom.ParseGuardForTest(raw)
		require.NoError(t, perr)
		got, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
		require.NoError(t, eerr)
		return got
	}

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{
			"str(subject) renders the class-tagged subject form",
			`{"starlark":"def guard(subject): return str(subject) == 'subject(class=\"identity\")'"}`,
			true,
		},
		{
			"str on a declared aspect renders that aspect's own class",
			`{"reads":["profile"],"starlark":"def guard(subject): return str(subject.profile) == 'subject(class=\"aspect\")'"}`,
			true,
		},
		{
			"str(subject.data) renders the dict with keys in sorted insertion order",
			`{"starlark":"def guard(subject): return str(subject.data) == '{\"age\": 21, \"name\": \"Ada\"}'"}`,
			true,
		},
		{
			"bool(subject) is always True",
			`{"starlark":"def guard(subject): return bool(subject)"}`,
			true,
		},
		{
			"a declared aspect is truthy as a value even when its data is empty",
			`{"reads":["empty"],"starlark":"def guard(subject): return bool(subject.empty) and not bool(subject.empty.data)"}`,
			true,
		},
		{
			"bool of populated data is True",
			`{"starlark":"def guard(subject): return bool(subject.data)"}`,
			true,
		},
		{
			// The want-false sibling of the two rows above: an empty data
			// dict alone must read falsy through the full eval path, so the
			// truthiness rows cannot pass vacuously.
			"bool of empty data is False",
			`{"reads":["empty"],"starlark":"def guard(subject): return bool(subject.empty.data)"}`,
			false,
		},
		{
			"type() names both wrapper types",
			`{"reads":["profile"],"starlark":"def guard(subject): return type(subject) == 'subject' and type(subject.data) == 'data' and type(subject.profile) == 'subject'"}`,
			true,
		},
		{
			// dir() sorts AttrNames, so the declared-aspect name lands in
			// deterministic order regardless of Go map iteration.
			"dir(subject) lists the fixed fields plus each declared aspect",
			`{"reads":["profile"],"starlark":"def guard(subject): return dir(subject) == ['class', 'data', 'isDeleted', 'profile']"}`,
			true,
		},
		{
			// starlarkDataDict.AttrNames reports only the dict's builtin
			// method names — data keys resolve through Attr, not AttrNames.
			"dir(subject.data) exposes dict methods, never data keys",
			`{"starlark":"def guard(subject): return 'get' in dir(subject.data) and 'keys' in dir(subject.data) and 'age' not in dir(subject.data)"}`,
			true,
		},
		{
			"iterating subject.data yields keys in sorted insertion order",
			`{"starlark":"def guard(subject): return [k for k in subject.data] == ['age', 'name']"}`,
			true,
		},
		{
			// `class` is a reserved keyword in Starlark, so dot-notation
			// (subject.class) is a syntax error — getattr is the one route a
			// script has to the class attribute arm.
			"class (via getattr) and isDeleted attributes read through",
			`{"starlark":"def guard(subject): return getattr(subject, 'class') == 'identity' and not subject.isDeleted"}`,
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, eval(t, tc.raw))
		})
	}
}

// TestGuardEval_Starlark_UnhashableAsDictKey pins the Hash() posture of both
// wrapper types: neither `subject` nor `subject.data` may be used as a dict
// key — Hash() refuses with a typed not-hashable error, surfaced to the guard
// author as an evaluation error (never a silent false). The builtin hash()
// only accepts string/bytes and never dispatches to a Value's Hash method, so
// dict-literal key insertion is the script-reachable trigger. The first case
// is the positive sibling: the identical dict-literal construction with a
// hashable key (the subject's class string — via getattr, `class` being a
// Starlark reserved keyword) evaluates clean, proving the two rejection cases
// fail on the key's hashability and nothing else.
func TestGuardEval_Starlark_UnhashableAsDictKey(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	subjectKey := seedValueInterfaceSubject(t, ctx, conn)

	cases := []struct {
		name            string
		raw             string
		want            bool
		wantErrContains string // "" = must evaluate clean to want
	}{
		{
			"hashable key sibling builds the dict and reads back",
			`{"starlark":"def guard(subject): return {getattr(subject, 'class'): subject.data.age}[getattr(subject, 'class')] == 21"}`,
			true,
			"",
		},
		{
			"subject as a dict key is rejected as unhashable",
			`{"starlark":"def guard(subject): return {subject: 1} != None"}`,
			false,
			"subject is not hashable",
		},
		{
			"subject.data as a dict key is rejected as unhashable",
			`{"starlark":"def guard(subject): return {subject.data: 1} != None"}`,
			false,
			"data is not hashable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, perr := loom.ParseGuardForTest(tc.raw)
			require.NoError(t, perr)
			got, eerr := loom.EvalGuardForTest(ctx, conn, coreKVBucket, subjectKey, g)
			if tc.wantErrContains == "" {
				require.NoError(t, eerr)
				require.Equal(t, tc.want, got)
				return
			}
			require.Error(t, eerr)
			require.Contains(t, eerr.Error(), tc.wantErrContains)
		})
	}
}
