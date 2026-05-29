package identitydomain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/substrate"
)

// PreInstall seeds the 3 user-facing roles (consumer, frontOfHouse,
// backOfHouse) directly to core-kv as a substrate-direct atomic batch
// before identity-domain's main install batch runs.
//
// Idempotency: if the canonical-name index entries
// `vtx.roleindex.<sha256NanoID>` already point to live role vertices,
// the existing NanoIDs are returned and no new writes happen. This
// makes the hook safe to re-run after partial install failure.
//
// The returned map (canonicalName → NanoID) is merged into the
// Installer's RoleIDs map so identity-domain's atomic batch can resolve
// `grantsTo: [consumer]` / `[frontOfHouse, ...]` to real link targets.
func PreInstall(ctx context.Context, conn *substrate.Conn, adminActor string) (map[string]string, error) {
	roles := []string{"consumer", "frontOfHouse", "backOfHouse"}
	out := map[string]string{}

	now := time.Now().UTC()
	createdByOp := "pkg-install:identity-domain"

	var ops []substrate.BatchOp
	for _, name := range roles {
		indexKey := "vtx.roleindex." + sha256NanoID("rolecanonical:"+name)

		// Idempotency: read existing index — if present, reuse the NanoID.
		if existing, err := conn.KVGet(ctx, pkgmgr.CoreBucket, indexKey); err == nil && existing != nil {
			var env struct {
				IsDeleted bool           `json:"isDeleted"`
				Data      map[string]any `json:"data"`
			}
			if jsonErr := json.Unmarshal(existing.Value, &env); jsonErr == nil && !env.IsDeleted {
				if id, _ := env.Data["roleId"].(string); id != "" {
					out[name] = id
					continue
				}
			}
		} else if !errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, fmt.Errorf("identity-domain seed: read %s: %w", indexKey, err)
		}

		// Fresh: mint role NanoID + write vertex + 2 aspects + index entry.
		roleID, err := substrate.NewNanoID()
		if err != nil {
			return nil, fmt.Errorf("identity-domain seed: nanoid: %w", err)
		}
		roleKey := "vtx.role." + roleID
		out[name] = roleID

		vtxEnv, err := makeEnvelope(roleKey, "role", nil, adminActor, createdByOp, now)
		if err != nil {
			return nil, err
		}
		ops = append(ops, substrate.BatchOp{
			Bucket: pkgmgr.CoreBucket, Key: roleKey, Value: vtxEnv, CreateOnly: true,
		})
		cnEnv, err := makeAspect(roleKey+".canonicalName", roleKey, "canonicalName",
			map[string]any{"value": name}, adminActor, createdByOp, now)
		if err != nil {
			return nil, err
		}
		ops = append(ops, substrate.BatchOp{
			Bucket: pkgmgr.CoreBucket, Key: roleKey + ".canonicalName", Value: cnEnv, CreateOnly: true,
		})
		descEnv, err := makeAspect(roleKey+".description", roleKey, "description",
			map[string]any{"text": userFacingRoleDescription(name)}, adminActor, createdByOp, now)
		if err != nil {
			return nil, err
		}
		ops = append(ops, substrate.BatchOp{
			Bucket: pkgmgr.CoreBucket, Key: roleKey + ".description", Value: descEnv, CreateOnly: true,
		})

		// Index entry — canonical-name -> roleId. Used for idempotent re-runs
		// and for cross-package canonical lookups.
		idxEnv, err := makeEnvelope(indexKey, "roleindex",
			map[string]any{"canonicalName": name, "roleId": roleID}, adminActor, createdByOp, now)
		if err != nil {
			return nil, err
		}
		ops = append(ops, substrate.BatchOp{
			Bucket: pkgmgr.CoreBucket, Key: indexKey, Value: idxEnv, CreateOnly: true,
		})
	}

	if len(ops) == 0 {
		return out, nil
	}
	if _, err := conn.AtomicBatch(ops, 15*time.Second); err != nil {
		return nil, fmt.Errorf("identity-domain seed atomic batch: %w", err)
	}
	return out, nil
}

func userFacingRoleDescription(name string) string {
	switch name {
	case "consumer":
		return "A resident, tenant, or other end-consumer of platform services."
	case "frontOfHouse":
		return "Front-of-house staff with visibility into resident-facing operations."
	case "backOfHouse":
		return "Back-of-house staff responsible for internal operational tasks."
	}
	return name
}

// makeEnvelope builds a Contract #1-compliant document envelope.
func makeEnvelope(key, class string, data map[string]any, actor, createdByOp string, now time.Time) ([]byte, error) {
	env := substrate.NewDocumentEnvelopeAt(class, actor, createdByOp, now)
	env.Key = key
	if data != nil {
		env.Data = data
	}
	return json.Marshal(env)
}

func makeAspect(key, vertexKey, localName string, data map[string]any, actor, createdByOp string, now time.Time) ([]byte, error) {
	base := substrate.NewDocumentEnvelopeAt(localName, actor, createdByOp, now)
	base.Key = key
	if data != nil {
		base.Data = data
	}
	asp := substrate.AspectEnvelope{
		DocumentEnvelope: base,
		VertexKey:        vertexKey,
		LocalName:        localName,
	}
	return json.Marshal(asp)
}

// sha256NanoID derives a deterministic 20-char NanoID-alphabet key from
// an arbitrary string. Used as a stable index-vertex suffix so the seed
// step is idempotent (same canonical name → same index key on every
// run). Does NOT need exact parity with the Starlark
// `crypto.sha256NanoID` builtin (the seed-side index is read only by
// Go installer code, never by scripts); a simpler deterministic mapping
// from SHA-256 bytes to the Contract #1 alphabet suffices.
func sha256NanoID(s string) string {
	sum := sha256.Sum256([]byte(s))
	alphabet := substrate.Alphabet
	out := make([]byte, substrate.NanoIDLength)
	// 32 bytes of SHA-256 over 20 chars: take 2 bytes per char, mod alphabet length.
	for i := 0; i < substrate.NanoIDLength; i++ {
		// Indices 0..19 → sum bytes 0..1, 2..3, ... 38..39 wrap at 32.
		hi := sum[(i*2)%len(sum)]
		lo := sum[((i*2)+1)%len(sum)]
		idx := (int(hi)<<8 | int(lo)) % len(alphabet)
		out[i] = alphabet[idx]
	}
	return string(out)
}
