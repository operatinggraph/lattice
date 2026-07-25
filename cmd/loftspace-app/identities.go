package main

import (
	"context"
	"strings"
)

// identityView is the picker's projection of one identity: the key it scopes to
// plus the human name to show.
type identityView struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// rosterIdentities reads the PROTECTED applicantRosterRead Postgres model
// (read_loftspace_identities) as the given actor and reshapes it to the
// {key, name, state} display shape the trusted-tool console consumes
// (unit_applications.go, lease_document.go). That model is the ONLY roster
// surface: the identity name is a sensitive aspect that the Refractor Secure
// Lens decrypts into this RLS-protected table alone (Contract #3 §3.10), so
// server-side name resolution reads it as the SIGNED-IN caller rather than from
// any unprotected bucket — RLS decides which names that session may resolve. A
// row with an empty key or name is skipped; queryIdentities already returns
// rows name-sorted for a stable order.
func rosterIdentities(ctx context.Context, pool pgxBeginner, actorID string) ([]identityView, error) {
	rows, err := queryIdentities(ctx, pool, actorID)
	if err != nil {
		return nil, err
	}
	return reshapeRoster(rows), nil
}

// reshapeRoster converts protected-model rows to the picker/display shape,
// dropping any row without a key or a non-blank name (defense in depth on top
// of the query's own name IS NOT NULL filter). Order is preserved —
// queryIdentities sorts by name.
func reshapeRoster(rows []protectedIdentityRow) []identityView {
	out := make([]identityView, 0, len(rows))
	for _, row := range rows {
		if row.IdentityKey == "" || strings.TrimSpace(row.Name) == "" {
			continue
		}
		out = append(out, identityView{Key: row.IdentityKey, Name: row.Name, State: row.State})
	}
	return out
}
