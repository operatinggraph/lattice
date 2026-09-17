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

// TestComputeBookings_PriceCentsIsTheLensEffectiveColumn proves the row's
// PriceCents is exactly the wellnessBookings projection's own priceCents — the
// lens's one effective column (the seat's .status.priceCents snapshot, else
// the session's current price by the booking's rate,
// packages/wellness-domain/lenses.go) — and that computeBookings applies no
// resident override of its own on top of it. A row still carrying a stray
// residentPriceCents field (an older projection shape) must not change the
// answer: the resolution lives in the lens, so My Classes and the ledger's
// charge read the same number.
func TestComputeBookings_PriceCentsIsTheLensEffectiveColumn(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b1": map[string]any{
			"bookingKey":         "vtx.booking.b1",
			"status":             "booked",
			"rate":               "resident",
			"sessionKey":         "vtx.session.s1",
			"sessionName":        "Vinyasa Flow",
			"priceCents":         1000.0,
			"residentPriceCents": 900.0,
			"bookerKey":          "vtx.identity.alice",
		},
		"vtx.booking.b2": map[string]any{
			"bookingKey":  "vtx.booking.b2",
			"status":      "booked",
			"rate":        "standard",
			"sessionKey":  "vtx.session.s1",
			"sessionName": "Vinyasa Flow",
			"priceCents":  1500.0,
			"bookerKey":   "vtx.identity.bob",
		},
		"vtx.booking.b3": map[string]any{
			"bookingKey":  "vtx.booking.b3",
			"status":      "booked",
			"rate":        "standard",
			"sessionKey":  "vtx.session.s1",
			"sessionName": "Vinyasa Flow",
			"priceCents":  0.0,
			"bookerKey":   "vtx.identity.carol",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b1", "vtx.booking.b2", "vtx.booking.b3"}, get, "", "")
	require.Len(t, rows, 3)
	byKey := map[string]bookingRow{}
	for _, r := range rows {
		byKey[r.BookingKey] = r
	}
	require.Equal(t, int64(1000), byKey["vtx.booking.b1"].PriceCents, "a resident-rate row shows the lens's priceCents as-is; no app-side override")
	require.Equal(t, int64(1500), byKey["vtx.booking.b2"].PriceCents)
	require.Equal(t, int64(0), byKey["vtx.booking.b3"].PriceCents, "a seat booked free is free")
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

// TestComputeBookings_PromotedAtThreadsThroughUnchanged proves the row's
// PromotedAt is exactly the wellnessBookings projection's own promotedAt
// column, and nil (omitted from JSON) on a seat booked directly — the FE's
// seating badge and its late-cancel exemption both key on it.
func TestComputeBookings_PromotedAtThreadsThroughUnchanged(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b6": map[string]any{
			"bookingKey":  "vtx.booking.b6",
			"status":      "booked",
			"rate":        "standard",
			"sessionKey":  "vtx.session.s1",
			"sessionName": "Vinyasa Flow",
			"priceCents":  1500.0,
			"bookerKey":   "vtx.identity.fay",
			"promotedAt":  "2026-09-05T07:57:00Z",
		},
		"vtx.booking.b7": map[string]any{
			"bookingKey":  "vtx.booking.b7",
			"status":      "booked",
			"rate":        "standard",
			"sessionKey":  "vtx.session.s1",
			"sessionName": "Vinyasa Flow",
			"priceCents":  1500.0,
			"bookerKey":   "vtx.identity.gus",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b6", "vtx.booking.b7"}, get, "", "")
	require.Len(t, rows, 2)
	byKey := map[string]bookingRow{}
	for _, r := range rows {
		byKey[r.BookingKey] = r
	}
	require.NotNil(t, byKey["vtx.booking.b6"].PromotedAt)
	require.Equal(t, "2026-09-05T07:57:00Z", *byKey["vtx.booking.b6"].PromotedAt)
	require.Nil(t, byKey["vtx.booking.b7"].PromotedAt)
}

// TestComputeBookings_ChangeNoticeThreadsThroughUnchanged proves the row's
// ChangeNoticeSentAt and MovedFor are exactly the wellnessBookings
// projection's own changeNoticeSentAt / movedFor columns, and nil (omitted
// from JSON) on a booking never told of a move — the FE's movedBadge keys on
// both.
func TestComputeBookings_ChangeNoticeThreadsThroughUnchanged(t *testing.T) {
	get := mapGetter(map[string]any{
		"vtx.booking.b8": map[string]any{
			"bookingKey":         "vtx.booking.b8",
			"status":             "booked",
			"rate":               "standard",
			"sessionKey":         "vtx.session.s1",
			"sessionName":        "Vinyasa Flow",
			"startsAt":           "2026-09-10T09:00:00Z",
			"priceCents":         1500.0,
			"bookerKey":          "vtx.identity.hana",
			"changeNoticeSentAt": "2026-09-05T07:57:00Z",
			"movedFor":           "2026-09-10T09:00:00Z",
		},
		"vtx.booking.b9": map[string]any{
			"bookingKey":  "vtx.booking.b9",
			"status":      "booked",
			"rate":        "standard",
			"sessionKey":  "vtx.session.s1",
			"sessionName": "Vinyasa Flow",
			"startsAt":    "2026-09-10T09:00:00Z",
			"priceCents":  1500.0,
			"bookerKey":   "vtx.identity.ivo",
		},
	})
	rows := computeBookings([]string{"vtx.booking.b8", "vtx.booking.b9"}, get, "", "")
	require.Len(t, rows, 2)
	byKey := map[string]bookingRow{}
	for _, r := range rows {
		byKey[r.BookingKey] = r
	}
	require.NotNil(t, byKey["vtx.booking.b8"].ChangeNoticeSentAt)
	require.Equal(t, "2026-09-05T07:57:00Z", *byKey["vtx.booking.b8"].ChangeNoticeSentAt)
	require.NotNil(t, byKey["vtx.booking.b8"].MovedFor)
	require.Equal(t, "2026-09-10T09:00:00Z", *byKey["vtx.booking.b8"].MovedFor)
	require.Nil(t, byKey["vtx.booking.b9"].ChangeNoticeSentAt)
	require.Nil(t, byKey["vtx.booking.b9"].MovedFor)
}
