package main

import (
	"encoding/json"
	"fmt"

	"github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// deviceIDLocalName is the sovereign, device-only store slot (the "local:"
// namespace, edge-lattice-full-design.md §3.1) holding this host's device id
// for one identity. It carries the same name as the browser engine's
// localStorage slot (web/boot.mjs's deviceIdKey) because it is the same
// thing: the host's own persistent device identity, never synced, never
// uploaded.
//
// A device id must persist for exactly as long as the mirror it belongs to.
// It names both the durable consumer (edgesync.DurableName) and, through the
// store it is read from, the resume cursor that positions that consumer — so
// an id that changes per engine build strands the previous durable forever and
// abandons the cursor beside it. That is why this lives in the store with the
// cursor, not in memory.
const deviceIDLocalName = "facet.deviceId"

// resolveDeviceID returns st's persisted device id, minting and storing one
// on first use. A stored value that is absent, unreadable as a string, or
// not a valid NanoID (corrupt, or written by an older scheme) is replaced —
// the id is spliced into a NATS subject token by both the Gateway's
// auth-callout grant and the durable's own name, so a malformed one would
// fail the connection's authorization rather than degrade quietly.
//
// Unlike the browser's resolveDeviceId, a persist failure is fatal here: the
// browser degrades to an ephemeral id because a page still has to boot,
// whereas an engine that cannot persist its id is exactly the orphan-per-
// build behaviour this exists to prevent.
func resolveDeviceID(st store.Store) (string, error) {
	raw, ok, err := st.GetLocal(deviceIDLocalName)
	if err != nil {
		return "", fmt.Errorf("read device id: %w", err)
	}
	if ok {
		var id string
		if json.Unmarshal(raw, &id) == nil && substrate.IsValidNanoID(id) {
			return id, nil
		}
	}
	id, err := substrate.NewNanoID()
	if err != nil {
		return "", fmt.Errorf("generate device id: %w", err)
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("encode device id: %w", err)
	}
	if err := st.PutLocal(deviceIDLocalName, encoded); err != nil {
		return "", fmt.Errorf("persist device id: %w", err)
	}
	return id, nil
}

// readDeviceID returns st's persisted device id without minting one. ok is
// false when the store holds none (or holds an unusable value) — the caller
// is tearing the mirror down, and there is nothing to reap for an identity
// that never opened an engine here.
func readDeviceID(st store.Store) (id string, ok bool, err error) {
	raw, found, err := st.GetLocal(deviceIDLocalName)
	if err != nil || !found {
		return "", false, err
	}
	if json.Unmarshal(raw, &id) != nil || !substrate.IsValidNanoID(id) {
		return "", false, nil
	}
	return id, true, nil
}
