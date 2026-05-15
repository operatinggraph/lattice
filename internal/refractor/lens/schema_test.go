package lens_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/lens"
)

// validRuleYAML returns a minimal valid rule YAML for use in tests.
func validRuleYAML() []byte {
	return []byte(`
id: occupancy-view
team: facilities
match: |
  MATCH (r:room)-[:HAS_OCCUPANT]->(p:person)
  RETURN r.id AS room_id, p.name AS occupant_name
into:
  target: nats_kv
  bucket: occupancy-view
  key: room_id
`)
}

func TestParse_ValidRule(t *testing.T) {
	r, err := lens.Parse(validRuleYAML())
	require.NoError(t, err)
	assert.Equal(t, "occupancy-view", r.ID)
	assert.Equal(t, "facilities", r.Team)
	assert.Contains(t, strings.ToUpper(r.Match), "RETURN")
	assert.Equal(t, "nats_kv", r.Into.Target)
	assert.Equal(t, "occupancy-view", r.Into.Bucket)
	assert.Equal(t, lens.KeyField{"room_id"}, r.Into.Key)
}

func TestParse_MissingID(t *testing.T) {
	y := []byte(`
team: facilities
match: MATCH (r:room) RETURN r.id AS room_id
into:
  target: nats_kv
  bucket: test
  key: room_id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id")
}

func TestParse_MissingTeam(t *testing.T) {
	y := []byte(`
id: test-rule
match: MATCH (r:room) RETURN r.id AS room_id
into:
  target: nats_kv
  bucket: test
  key: room_id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team")
}

func TestParse_MatchWithoutReturn(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (r:room)-[:HAS_OCCUPANT]->(p:person)
into:
  target: nats_kv
  bucket: test
  key: room_id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, strings.ToUpper(err.Error()), "RETURN")
}

func TestParse_InvalidMatchSyntax(t *testing.T) {
	// A query that contains "RETURN" but is syntactically malformed — exercises the
	// engine.Parse integration path and the "rule validation: invalid match query" wrapping.
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement-[:HAS_PARTY]->(i:identity) RETURN a.id AS id
into:
  target: nats_kv
  bucket: test
  key: id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid match query")
}

func TestParse_RetryConfig(t *testing.T) {
	y := []byte(`
id: agreement-summary
team: agreements
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: agreement-summary
  key: agreement_id
retry:
  max_attempts: 5
  backoff: PT5S
`)
	r, err := lens.Parse(y)
	require.NoError(t, err)
	assert.Equal(t, 5, r.Retry.MaxAttempts)
	assert.Equal(t, "PT5S", r.Retry.Backoff)
}

func TestParse_CompositeKeyArray(t *testing.T) {
	y := []byte(`
id: agreement-summary
team: agreements
match: MATCH (a:agreement) RETURN a.team AS team_id, a.id AS agreement_id
into:
  target: nats_kv
  bucket: agreement-summary
  key:
    - team_id
    - agreement_id
`)
	r, err := lens.Parse(y)
	require.NoError(t, err)
	assert.Equal(t, lens.KeyField{"team_id", "agreement_id"}, r.Into.Key)
}

func TestParse_CompositeKeyString(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test
  key: agreement_id
`)
	r, err := lens.Parse(y)
	require.NoError(t, err)
	assert.Equal(t, lens.KeyField{"agreement_id"}, r.Into.Key)
}

func TestParse_UnknownExtraFields(t *testing.T) {
	// Extra/unknown fields must not cause errors — forward/backward compat (NFR22)
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test
  key: agreement_id
future_field: some_value
another_unknown:
  nested: true
`)
	_, err := lens.Parse(y)
	require.NoError(t, err)
}

func TestParse_MissingKey(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key")
}

func TestParse_InvalidTarget(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: redis
  bucket: test
  key: agreement_id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target")
}

func TestParse_RetryOptional(t *testing.T) {
	// retry section absent — zero value is fine (no retry)
	r, err := lens.Parse(validRuleYAML())
	require.NoError(t, err)
	assert.Equal(t, 0, r.Retry.MaxAttempts)
	assert.Equal(t, "", r.Retry.Backoff)
}

func TestParse_PostgresTarget(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: agreements
  key: agreement_id
`)
	r, err := lens.Parse(y)
	require.NoError(t, err)
	assert.Equal(t, "postgres", r.Into.Target)
}

func TestParse_NatsKV_MissingBucket(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  key: agreement_id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket")
}

func TestParse_Postgres_MissingDSN(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  table: agreements
  key: agreement_id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dsn")
}

func TestParse_Postgres_MissingTable(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  key: agreement_id
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table")
}

func TestParse_QueryTimeout_Default(t *testing.T) {
	// A postgres rule with no query_timeout should default to 30s.
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: agreements
  key: agreement_id
`)
	r, err := lens.Parse(y)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, r.Into.QueryTimeout, "absent query_timeout must default to 30s")
}

func TestParse_QueryTimeout_Custom(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: agreements
  key: agreement_id
  query_timeout: 5s
`)
	r, err := lens.Parse(y)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, r.Into.QueryTimeout)
}

func TestParse_QueryTimeout_Invalid(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: agreements
  key: agreement_id
  query_timeout: not-a-duration
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query_timeout")
}

func TestParse_QueryTimeout_NatsKV_DefaultApplied(t *testing.T) {
	// nats_kv rules also get the 30s default — harmless but consistent.
	r, err := lens.Parse(validRuleYAML())
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, r.Into.QueryTimeout)
}

func TestParse_KeyArrayEmptyElement(t *testing.T) {
	y := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test
  key:
    - agreement_id
    - ""
`)
	_, err := lens.Parse(y)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}
