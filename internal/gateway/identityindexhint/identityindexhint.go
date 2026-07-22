// Package identityindexhint is the P5-clean read seam the Gateway's
// provision-time probe uses (whoami `?probe=1`,
// multi-credential-identity-linking-design.md §3.4): a direct read against
// the identity-domain package's identityIndexHint lens bucket, keyed the
// same way the write-path dedup scripts derive an identityindex key
// (`vtx.identityindex.` + sha256NanoID(contactType+":"+value)). No operation
// is submitted for this read — Contract #2 §2.7's closed `response` schema
// permits only `primaryKey` and forbids any other script-returned data, so
// the hint is served from the lens projection instead (the same P5 seam
// credentialbinding/revocation already use), never from an operation reply.
package identityindexhint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// BucketName is the canonical identity-index-hint bucket
// (packages/identity-domain.IdentityIndexHintBucket).
const BucketName = "identity-index-hint"

// kvGetter is the minimal read surface Lookup needs — *substrate.KV
// satisfies it; the interface keeps the package test-fakeable without a
// live NATS connection (mirrors credentialbinding.kvGetter).
type kvGetter interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
}

// hint is the lens-projected document shape (packages/identity-domain's
// identityIndexHint lens: `RETURN n.key AS key, n.data.identityKey AS
// identityKey, n.data.contactType AS contactType`).
type hint struct {
	IdentityKey string `json:"identityKey"`
}

// Resolver reads the identity-index-hint bucket. Safe for concurrent use
// (it holds only a read handle).
type Resolver struct {
	kv kvGetter
}

// New builds a Resolver over an already-opened identity-index-hint bucket
// handle (obtain via substrate.Conn.OpenKV(ctx, identityindexhint.BucketName)).
func New(kv kvGetter) *Resolver {
	return &Resolver{kv: kv}
}

// Lookup answers "does a live identityindex vertex exist at this derived
// key, and if so which identity does it point at?" found=false (no error)
// means no such index vertex has ever been projected — the honest-limits
// case (never-indexed Scenario-B identities, a genuinely new contact) or the
// CDC-lag window between a fresh staff-created identity and this bucket
// observing it; callers must treat it as "no hint", never as a denial.
func (r *Resolver) Lookup(ctx context.Context, indexKey string) (identityKey string, found bool, err error) {
	entry, err := r.kv.Get(ctx, indexKey)
	switch {
	case err == nil:
		var h hint
		if uerr := json.Unmarshal(entry.Value, &h); uerr != nil {
			return "", false, fmt.Errorf("identityindexhint: malformed hint for %q: %w", indexKey, uerr)
		}
		if h.IdentityKey == "" {
			return "", false, fmt.Errorf("identityindexhint: hint for %q missing identityKey", indexKey)
		}
		return h.IdentityKey, true, nil
	case errors.Is(err, substrate.ErrKeyNotFound):
		return "", false, nil
	default:
		return "", false, fmt.Errorf("identityindexhint: lookup %q: %w", indexKey, err)
	}
}
