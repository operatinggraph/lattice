package failure

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Heuristic classification — raw error types ────────────────────────────────

func TestClassify_Infrastructure(t *testing.T) {
	infraErrors := []error{
		nats.ErrConnectionClosed,
		nats.ErrConnectionDraining,
		nats.ErrDisconnected,
		nats.ErrNoServers,
		fmt.Errorf("wrapped: %w", nats.ErrConnectionClosed),
	}
	for _, err := range infraErrors {
		assert.Equal(t, CatInfra, Classify(err), "expected CatInfra for: %v", err)
	}
}

func TestClassify_Structural(t *testing.T) {
	structuralErrors := []error{
		jetstream.ErrBucketNotFound,
		jetstream.ErrStreamNotFound,
		fmt.Errorf("wrapped: %w", jetstream.ErrBucketNotFound),
	}
	for _, err := range structuralErrors {
		assert.Equal(t, CatStructural, Classify(err), "expected CatStructural for: %v", err)
	}
}

func TestClassify_Transient(t *testing.T) {
	transientErrors := []error{
		errors.New("some generic error"),
		fmt.Errorf("timeout writing to db"),
		errors.New("lock contention"),
	}
	for _, err := range transientErrors {
		assert.Equal(t, CatTransient, Classify(err), "expected CatTransient for: %v", err)
	}
}

func TestClassify_NilPanics(t *testing.T) {
	require.Panics(t, func() { Classify(nil) })
}

// ── Postgres structural error tests (no real Postgres needed) ─────────────────

func TestClassify_Structural_PgError_UndefinedTable(t *testing.T) {
	err := &pgconn.PgError{Code: "42P01", Message: `relation "foo" does not exist`}
	assert.Equal(t, CatStructural, Classify(err), "42P01 undefined_table must be CatStructural (FR37)")
}

func TestClassify_Structural_PgError_NotNullViolation(t *testing.T) {
	err := &pgconn.PgError{Code: "23502", Message: `null value in column "name" violates not-null constraint`}
	assert.Equal(t, CatStructural, Classify(err), "23502 not_null_violation must be CatStructural (FR38)")
}

func TestClassify_Structural_PgError_DatatypeMismatch(t *testing.T) {
	err := &pgconn.PgError{Code: "42804", Message: `column "qty" is of type integer but expression is of type text`}
	assert.Equal(t, CatStructural, Classify(err), "42804 datatype_mismatch must be CatStructural (FR38)")
}

func TestClassify_Structural_PgError_InvalidTextRepresentation(t *testing.T) {
	err := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "abc"`}
	assert.Equal(t, CatStructural, Classify(err), "22P02 invalid_text_representation must be CatStructural (FR38)")
}

func TestClassify_Structural_PgError_Wrapped(t *testing.T) {
	inner := &pgconn.PgError{Code: "42P01", Message: "relation does not exist"}
	err := fmt.Errorf("adapter: %w", inner)
	assert.Equal(t, CatStructural, Classify(err), "wrapped *pgconn.PgError must be unwrapped via errors.As")
}

func TestClassify_Transient_PgError_OtherCode(t *testing.T) {
	// Serialization failure is retryable — must NOT be CatStructural.
	err := &pgconn.PgError{Code: "40001", Message: "serialization failure"}
	assert.Equal(t, CatTransient, Classify(err), "non-structural PgError must fall through to CatTransient")
}

// ── Terminal classification tests ─────────────────────────────────────────────

func TestClassify_Terminal(t *testing.T) {
	wrapped := Terminal(errors.New("value 'xyz' is not a valid integer"))
	assert.Equal(t, CatTerminal, Classify(wrapped), "Terminal error must classify as CatTerminal")
}

func TestClassify_Terminal_Wrapped(t *testing.T) {
	inner := Terminal(errors.New("bad data"))
	outer := fmt.Errorf("adapter upsert: %w", inner)
	assert.Equal(t, CatTerminal, Classify(outer), "double-wrapped terminal must still classify as CatTerminal")
}

func TestClassify_Terminal_UnwrapsOriginal(t *testing.T) {
	cause := errors.New("root cause")
	wrapped := Terminal(cause)
	assert.True(t, errors.Is(wrapped, cause), "Terminal must preserve errors.Is chain via Unwrap")
}

// ── Constructor nil-panic tests ───────────────────────────────────────────────

func TestTerminal_NilPanics(t *testing.T) {
	require.Panics(t, func() { _ = Terminal(nil) })
}

func TestInfrastructure_NilPanics(t *testing.T) {
	require.Panics(t, func() { _ = Infrastructure(nil) })
}

func TestStructural_NilPanics(t *testing.T) {
	require.Panics(t, func() { _ = Structural(nil) })
}

func TestTransient_NilPanics(t *testing.T) {
	require.Panics(t, func() { _ = Transient(nil) })
}

// ── Constructor classification tests ─────────────────────────────────────────

func TestInfrastructure_Constructor(t *testing.T) {
	cause := errors.New("connection refused")
	wrapped := Infrastructure(cause)
	assert.Equal(t, CatInfra, Classify(wrapped), "Infrastructure constructor must classify as CatInfra")
}

func TestStructural_Constructor(t *testing.T) {
	cause := errors.New("table does not exist")
	wrapped := Structural(cause)
	assert.Equal(t, CatStructural, Classify(wrapped), "Structural constructor must classify as CatStructural")
}

func TestTransient_Constructor(t *testing.T) {
	cause := errors.New("lock contention")
	wrapped := Transient(cause)
	assert.Equal(t, CatTransient, Classify(wrapped), "Transient constructor must classify as CatTransient")
}

// TestConstructor_PreservesErrorChain verifies all constructors preserve errors.Is chain.
func TestConstructor_PreservesErrorChain(t *testing.T) {
	cause := errors.New("root cause")
	assert.True(t, errors.Is(Infrastructure(cause), cause), "Infrastructure must preserve errors.Is chain")
	assert.True(t, errors.Is(Structural(cause), cause), "Structural must preserve errors.Is chain")
	assert.True(t, errors.Is(Terminal(cause), cause), "Terminal must preserve errors.Is chain")
	assert.True(t, errors.Is(Transient(cause), cause), "Transient must preserve errors.Is chain")
}

// TestExplicitWrapper_OverridesHeuristic verifies explicit wrappers beat raw-type detection.
// Example: wrapping a NATS error (normally CatInfra) with Structural → CatStructural wins.
func TestExplicitWrapper_OverridesHeuristic(t *testing.T) {
	// NATS error normally classified as CatInfra — explicit Structural wrapper wins.
	natsErr := nats.ErrConnectionClosed
	assert.Equal(t, CatStructural, Classify(Structural(natsErr)),
		"Structural wrapper must override NATS heuristic")

	// JetStream bucket-not-found normally CatStructural — explicit Infrastructure wrapper wins.
	jsErr := jetstream.ErrBucketNotFound
	assert.Equal(t, CatInfra, Classify(Infrastructure(jsErr)),
		"Infrastructure wrapper must override JetStream heuristic")

	// Any error forced Transient via explicit wrapper.
	pgErr := &pgconn.PgError{Code: "42P01", Message: "table missing"}
	assert.Equal(t, CatTransient, Classify(Transient(pgErr)),
		"Transient wrapper must override Postgres structural heuristic")

	// Terminal wrapper overrides a heuristic that would otherwise classify differently.
	genericErr := errors.New("some transient-looking error")
	assert.Equal(t, CatTerminal, Classify(Terminal(genericErr)),
		"Terminal wrapper must override default transient classification")
}
