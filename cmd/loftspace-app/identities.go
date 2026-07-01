package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// identityRow is one projected `applicantRoster` row — a selectable identity with
// its human-readable name. computeIdentities is the shared reshaper the
// trusted-tool console (unit_applications.go, lease_document.go) uses to resolve
// a name for display from the unprotected NATS-KV bucket server-side; the
// HTTP-facing picker itself reads the PROTECTED applicantRosterRead model
// instead (staff_identities.go, D1.5) — see that file for why.
type identityRow struct {
	IdentityKey string `json:"identityKey"`
	Name        string `json:"name"`
	State       string `json:"state"`
}

// identityView is the picker's projection of one identity: the key it scopes to
// plus the human name to show.
type identityView struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// computeIdentities assembles the applicant picker from the `applicantRoster` lens
// read model: every named identity (the lens already filters out unnamed service
// actors), reshaped to {key, name, state} and sorted by name for a stable picker.
// A row that fails to decode or carries no key/name is skipped.
func computeIdentities(keys []string, get kvGetter) []identityView {
	out := make([]identityView, 0)
	for _, k := range keys {
		raw, ok := get(k)
		if !ok {
			continue
		}
		var row identityRow
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		if row.IdentityKey == "" || strings.TrimSpace(row.Name) == "" {
			continue
		}
		out = append(out, identityView{Key: row.IdentityKey, Name: row.Name, State: row.State})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Key < out[j].Key
	})
	return out
}
