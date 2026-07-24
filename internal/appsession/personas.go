package appsession

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Persona is one entry of the demo sign-in fence — the hosted-demo login
// posture (deploy/demo): a curated, seed-derived identity the login page
// offers as a one-tap sign-in card. While the list is non-empty these are
// also the ONLY subjects the login endpoint will mint for, so an app's
// open-ended ceremonies (claim, credential linking) stay unreachable from a
// proxied public listener.
type Persona struct {
	// ID is the persona's bare identity NanoID (a "vtx.identity." prefix is
	// tolerated and stripped at parse).
	ID string `json:"id"`
	// Label is the card's headline (e.g. the seeded resident's name).
	Label string `json:"label"`
	// Sub is an optional second line (e.g. "Resident · Unit 1").
	Sub string `json:"sub,omitempty"`
}

// ParsePersonas parses an app's <PREFIX>_DEMO_PERSONAS env value, naming it
// envVar in errors. Empty input is the non-demo posture (nil list). Every
// entry must carry a valid bare NanoID and a label — a malformed list fails
// startup rather than silently widening the fence to nothing.
func ParsePersonas(envVar, raw string) ([]Persona, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var personas []Persona
	if err := json.Unmarshal([]byte(raw), &personas); err != nil {
		return nil, fmt.Errorf("%s: %w", envVar, err)
	}
	if len(personas) == 0 {
		return nil, fmt.Errorf("%s: set but names no personas", envVar)
	}
	for i := range personas {
		personas[i].ID = strings.TrimPrefix(strings.TrimSpace(personas[i].ID), "vtx.identity.")
		if !substrate.IsValidNanoID(personas[i].ID) {
			return nil, fmt.Errorf("%s[%d]: id must be a 20-character NanoID", envVar, i)
		}
		if strings.TrimSpace(personas[i].Label) == "" {
			return nil, fmt.Errorf("%s[%d]: label is required", envVar, i)
		}
	}
	return personas, nil
}
