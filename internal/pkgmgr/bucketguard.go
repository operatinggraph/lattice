package pkgmgr

import (
	"fmt"
	"strings"
)

// reservedBucketAliases maps a short, provision-time alias to the canonical
// NATS KV bucket a package lens must target. A provisioned bucket is keyed
// by its canonical name (e.g. the auth plane's "capability-kv"); the short
// alias ("capability") names the same plane in operator-facing copy but is
// NOT a real bucket. Bootstrap translates the alias to the canonical name for
// the primordial lenses, but a package lens's declared Bucket is consumed
// verbatim by the Refractor nats-kv adapter, which auto-creates whatever name
// it is given. A lens declaring the alias would therefore project into a
// phantom bucket that no reader (the capability authorizer, the auth-plane
// resurrection guard) consults — silent mis-targeting of the authorization
// surface. Install rejects the alias so the footgun fails closed.
var reservedBucketAliases = map[string]string{
	"capability": "capability-kv",
}

// validateLensBuckets rejects any lens whose declared Bucket is a reserved
// short alias of a provisioned bucket, directing the author to the canonical
// name. It is a pure function (no I/O) so it runs before any KV operation and
// is unit-testable without a live substrate.
func (def Definition) validateLensBuckets() error {
	for idx, l := range def.Lenses {
		if canonical, reserved := reservedBucketAliases[l.Bucket]; reserved {
			return fmt.Errorf(
				"pkgmgr: Lens[%d] %q declares Bucket %q, which is a reserved alias of the provisioned bucket %q — use %q so the lens targets the real auth-plane bucket (the alias auto-creates a phantom bucket no reader consults)",
				idx, l.CanonicalName, l.Bucket, canonical, canonical)
		}
	}
	return nil
}

// validateLensAdapters checks that each lens carries the fields required by
// its declared adapter. It is a pure function and runs before any KV
// operation.
func (def Definition) validateLensAdapters() error {
	for idx, l := range def.Lenses {
		switch l.Adapter {
		case "", "nats-kv":
			if l.Bucket == "" {
				return fmt.Errorf("pkgmgr: Lens[%d] %q (nats-kv): Bucket is required", idx, l.CanonicalName)
			}
		case "postgres":
			var missing []string
			if l.DSN == "" {
				missing = append(missing, "DSN")
			}
			if l.Table == "" {
				missing = append(missing, "Table")
			}
			if len(missing) > 0 {
				return fmt.Errorf("pkgmgr: Lens[%d] %q (postgres): %s required", idx, l.CanonicalName, strings.Join(missing, ", "))
			}
		default:
			return fmt.Errorf("pkgmgr: Lens[%d] %q: unknown Adapter %q (must be \"nats-kv\" or \"postgres\")", idx, l.CanonicalName, l.Adapter)
		}
	}
	return nil
}
