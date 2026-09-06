package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// mapGetter is a kvGetter backed by a plain map — computeBookings/computeSessions
// take one via a function value, so no NATS fixture is needed to unit-test the
// decode/resolve logic in isolation.
func mapGetter(rows map[string]any) kvGetter {
	encoded := make(map[string][]byte, len(rows))
	for k, v := range rows {
		b, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		encoded[k] = b
	}
	return func(key string) ([]byte, bool) {
		v, ok := encoded[key]
		return v, ok
	}
}

// TestComputeBookings_ResidentRateChargesResidentPrice proves a resident-rate
// booking's row carries the session's residentPriceCents, not priceCents — the
// "never seen" half of verticals.md's "a wellness class's resident price can
// be charged but never set or seen": before this, My Classes always showed
// the standard price even for a booking the settlement lens actually charges
// at the resident rate.
func TestComputeBookings_ResidentRateChargesResidentPrice(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b1": map[string]any{
			"bookingKey":         "vtx.booking.b1",
			"status":             "booked",
			"rate":               "resident",
			"sessionKey":         "vtx.session.s1",
			"sessionName":        "Vinyasa Flow",
			"priceCents":         1500.0,
			"residentPriceCents": 1000.0,
			"bookerKey":          "vtx.identity.alice",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b1"}, get, "", "")
	require.Len(t, rows, 1)
	require.Equal(t, int64(1000), rows[0].PriceCents, "a resident-rate booking must show the resident price, not the standard price")
}

// TestComputeBookings_StandardRateChargesStandardPrice proves a standard-rate
// booking is unaffected by a session's residentPriceCents — the sibling case
// to the resident-rate proof above.
func TestComputeBookings_StandardRateChargesStandardPrice(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b2": map[string]any{
			"bookingKey":         "vtx.booking.b2",
			"status":             "booked",
			"rate":               "standard",
			"sessionKey":         "vtx.session.s1",
			"sessionName":        "Vinyasa Flow",
			"priceCents":         1500.0,
			"residentPriceCents": 1000.0,
			"bookerKey":          "vtx.identity.bob",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b2"}, get, "", "")
	require.Len(t, rows, 1)
	require.Equal(t, int64(1500), rows[0].PriceCents, "a standard-rate booking must not accidentally receive the resident discount")
}

// TestComputeBookings_ResidentRateNoOverrideFallsBackToStandard proves a
// resident-rate booking on a session with no residentPriceCents falls back to
// the standard price — mirrors wellnessClassPriceSettlement's own CASE WHEN
// fallback (packages/wellness-ledger/lenses.go), so My Classes never diverges
// from what the member is actually charged.
func TestComputeBookings_ResidentRateNoOverrideFallsBackToStandard(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b3": map[string]any{
			"bookingKey":  "vtx.booking.b3",
			"status":      "booked",
			"rate":        "resident",
			"sessionKey":  "vtx.session.s2",
			"sessionName": "Power Vinyasa",
			"priceCents":  1800.0,
			"bookerKey":   "vtx.identity.carol",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b3"}, get, "", "")
	require.Len(t, rows, 1)
	require.Equal(t, int64(1800), rows[0].PriceCents)
}

// TestComputeBookings_ReminderSentAtThreadsThroughUnchanged proves the row's
// ReminderSentAt is exactly the wellnessBookings projection's own
// reminderSentAt column — the FE cannot show a reminder marker computeBookings
// silently drops going from projection to row.
func TestComputeBookings_ReminderSentAtThreadsThroughUnchanged(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b4": map[string]any{
			"bookingKey":     "vtx.booking.b4",
			"status":         "booked",
			"rate":           "standard",
			"sessionKey":     "vtx.session.s1",
			"sessionName":    "Vinyasa Flow",
			"priceCents":     1500.0,
			"bookerKey":      "vtx.identity.dana",
			"reminderSentAt": "2026-09-05T09:00:00Z",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b4"}, get, "", "")
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].ReminderSentAt)
	require.Equal(t, "2026-09-05T09:00:00Z", *rows[0].ReminderSentAt)
}

// TestComputeBookings_NoReminderSentAtIsNil proves an unreminded booking's row
// carries a nil ReminderSentAt (omitted from JSON), not an empty string that
// would render a badge with no date.
func TestComputeBookings_NoReminderSentAtIsNil(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b5": map[string]any{
			"bookingKey":  "vtx.booking.b5",
			"status":      "booked",
			"rate":        "standard",
			"sessionKey":  "vtx.session.s1",
			"sessionName": "Vinyasa Flow",
			"priceCents":  1500.0,
			"bookerKey":   "vtx.identity.erin",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b5"}, get, "", "")
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].ReminderSentAt)
}
