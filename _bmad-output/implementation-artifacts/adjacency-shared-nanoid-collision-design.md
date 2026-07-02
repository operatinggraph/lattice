# Shared-NanoID accounts corrupt adjacency — fixed in clinic-ledger, loftspace-ledger still carries it

**Status:** ✅ resolved in `clinic-ledger` (this fire, commit follows) and **live-verified** on a
fresh stack. `loftspace-ledger` still carries the identical anti-pattern — filed as a follow-up row
on `verticals.md`. The platform-layer hardening options below are NOT being pursued: Andrew's
ruling was that the anti-pattern itself — not the adjacency layer — is the defect (2026-07-01):
"vertex Ids must be unique across core-kv... do not design this on purpose."

## What happened

`clinic-ledger`'s `CreateAccount` minted the ledger account under the **same bare NanoID as the
patient it belongs to** ("one account per patient," the shared key doubling as the uniqueness
guard — an idiom copied from `loftspace-ledger`, which does the same against the lease). This is a
genuine Contract #1 violation: NanoIDs are unique identifiers across all of Core KV, not scoped per
vertex type, and reusing one across two different vertex types is not something the platform's
absence-of-enforcement makes acceptable.

The concrete failure it caused: `internal/refractor/adjacency/store.go` keys the Adjacency KV by
**bare NanoID only** (`adj.<NodeID>`, no vertex-type qualifier — confirmed via `builder.go`'s
`key := subjects.AdjKey(evt.NodeID)`). With the account and patient sharing a NanoID, their edges
collided under one `adj.<sharedNanoID>` entry — confirmed live via a direct dump of
`refractor-adjacency`, which showed the patient's `heldFor` edge and the account's `postedTo` edges
merged under a single key with `heldFor`'s direction recorded from the patient's (not the account's)
perspective. `clinicLedgerHistory`'s second-hop `MATCH (a)-[:heldFor]->(pt:patient)` — a proven
chained-REQUIRED-MATCH shape (mirrors the shipped `lease-signing` `landlordLeaseApplicationsRead`
lens) — walks `heldFor` outbound from the account, which the corrupted, patient-perspective
`inbound`-only entry never satisfies. Every row dropped; the lens silently converged on zero output.
`GET /api/ledger` returned a clean `200` with `"transactions": []` (a soft empty state, not an
error), so the defect was invisible short of directly inspecting the projected KV bucket — and
`loftspace-ledger`'s identical, already-shipped lens (`12736df`/`9947f75`) carried the same defect,
unnoticed because nothing had ever posted a real charge through its UI end-to-end.

## The fix (clinic-ledger, this fire)

- `CreateAccount` now mints the account under its **own independently-generated NanoID**
  (`nanoid.new()`, matching how every other vertex-minting DDL in this codebase already works).
- "At most one account per patient" is enforced by a **deterministic create-only guard aspect on
  the patient** instead of a shared/derived key: `vtx.patient.<NanoID>.ledgerAccount`
  (`clinicLedgerAccountGuard` DDL) = `{accountKey}`, written once by `CreateAccount` alongside the
  account it names. A second `CreateAccount` that declares the guard in `contextHint.reads`
  conflicts on it cleanly (`AccountAlreadyExists`); a first-ever call (the guard doesn't exist yet
  to declare — the Processor hard-rejects a `contextHint.reads` key that's absent) instead relies on
  the guard aspect's own create-only write to fail a genuine concurrent race.
- Added the `clinicPatientAccounts` lens (one row per patient, `accountKey` empty until one is
  opened) — the account key can no longer be derived by string manipulation, so this is now the
  FE's only way to resolve it. `cmd/clinic-app/ledger.go`'s `handleLedger` reads it; the FE's
  `submitLedgerEntry` tries the charge/payment post first and opens the account only on a
  `HydrationMiss` (no account yet), reading the new account's key straight off the `CreateAccount`
  reply's `primaryKey` rather than deriving or guessing it.

Live-verified on a freshly cycled stack (`make down && make up-full` + reinstall both verticals,
Andrew-authorized 2026-07-01): `CreateAccount` mints an account key genuinely independent of the
patient's; `DebitAccount`/`CreditAccount` post correctly; the billing panel in `cmd/clinic-app`
renders the real balance and transaction history through the actual browser UI (screenshotted:
"Balance owed: $15.00" after a $25 charge + $10 payment, both rows present).

## Follow-up: loftspace-ledger carries the same anti-pattern

`packages/loftspace-ledger`'s `CreateAccount` still mints the account under the lease's own bare
NanoID (unfixed — out of scope for this fire). It ships the identical latent defect: its
`ledgerHistory` lens will silently project zero rows the first time a real charge is posted through
`loftspace-app`'s UI, for the exact mechanism described above. The fix is mechanical once mirrored
from `clinic-ledger`'s pattern (independent NanoID + a `.ledgerAccount`-shaped guard aspect on the
`leaseapp` + a `loftspacePatientAccounts`-shaped lookup lens + the FE's post-first/create-on-miss
flow) — filed as a row on `verticals.md`.

## Platform-layer hardening — considered, not pursued

The adjacency layer's bare-NodeID keying (no vertex-type qualifier) is a real structural gap: it
silently assumes NanoIDs are unique platform-wide, an assumption the platform itself does not
enforce anywhere. Three hardening options were sketched (type-qualify the adjacency key; keep a
per-type edge partition under one node's key; or leave the layer as-is and rely on convention).
Andrew's ruling was to fix the actual defect (stop generating colliding IDs) rather than harden the
adjacency layer to tolerate it — the anti-pattern is the bug, not the layer that faithfully
reflected it. Revisit only if a *different* legitimate reason to share a NanoID across vertex types
ever emerges (none is known today).
