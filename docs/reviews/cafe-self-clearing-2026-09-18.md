# Café — "A staffer clears their own café debt" (2026-09-18)

**Filed (PO, 2026-09-17):** the staff leg of `CreditCafeAccount{waiver}`, `RefundCafeCharge`, `PayoutCafeCredit`
and `Settle{paidCents}` checks `worksAt` only ([scripts.go:1202](../../packages/cafe-ledger/scripts.go)); the
arrears grid draws *Write off* on the staffer's own row. Refuse a clearing verb on the actor's own lease's account
(the wellness/clinic ledgers share the shape). Live: Sam Okafor (front of house, 32 d overdue) wrote off 1¢ of
their own balance. Winston-adjudicated (implementation-level; no contract surface, no fork).

## Grounding

- **The staff leg proves standing, never non-ownership.** `post_entry` ([scripts.go:1578-1700](../../packages/cafe-ledger/scripts.go))
  runs `require_workplace([account_unit(acct_key)])` on the staff leg (no `authContextTarget`) and the ownership
  proof — `heldFor` → lease → `applicationFor.identity.<target>` — only on the self leg. A front-of-house staffer
  who also holds the lease passes the walk on their own account.
- **Three verbs give the house's money up.** `waiver` (a credit that forgives), `refund` (a credit against a
  charge), `payout` (a debit that hands credit back in cash) — the `reason` `post_entry` writes
  ([scripts.go:1606-1625](../../packages/cafe-ledger/scripts.go)). A `payment` credit records money coming IN and
  is the honor-system fact every resident may record on the self leg
  ([permissions.go:84-87](../../packages/cafe-ledger/permissions.go)); `Settle{paidCents}` posts the same
  `payment` credit through `cafeTabSettlement`'s `missing_payment`. Refusing a staffer's own counter payment would
  send them to the resident self-pay form for the same fact — no protection gained, so it is **not** a clearing
  verb here; the guarded quantity is what the house gives up.
- **The owner is one bounded walk away.** The self leg's own idiom: `kv.Links(acct_key, "heldFor", …, 1)` then
  a follow-up `kv.Read("lnk.leaseapp.<lease>.applicationFor.identity.<id>")` (read-posture (e)), the id being
  `op.actor` on the staff leg.
- **FE sites.** The Front Desk arrears grid draws *Write off* / *Pay out* per row keyed by
  `residentsByLease[row.leaseAppKey]` ([app.js:1830-1850](../../cmd/cafe-app/web/app.js)); the desk's resident
  ledger draws `refund-charge-btn` per charge when `!selfMode` ([app.js:2635](../../cmd/cafe-app/web/app.js)).
  The viewer is `state.identityId`. Facet describes all three ops ([opmetas.go:80,140,213](../../packages/cafe-ledger/opmetas.go)).
- **Sibling ledgers.** clinic-ledger and wellness-ledger carry the `waiver` reason on their credit's staff leg
  ([clinic scripts.go:1130](../../packages/clinic-ledger/scripts.go), [wellness scripts.go:1168](../../packages/wellness-ledger/scripts.go))
  with the same `heldFor` → owner topology (patient `identifiedBy`, member). The same hole; the same shape of fix.

## Verdict — *nobody clears their own debt from the desk: a clearing verb on the actor's own account is refused `SelfClearing`*

1. **cafe-ledger `post_entry`**, staff leg only (`authContextTarget == ""`), after `require_workplace` and before
   the `tabRef` / balance reads: when the entry's `reason` is `waiver`, `refund` or `payout`, resolve the account's
   lease via `heldFor` and read `applicationFor.identity.<op.actor>`; live ⇒ `fail("SelfClearing: a staffer may
   not <forgive|refund|pay out> their own account — another staffer must")`. The operator is not exempt: the
   rule is about whose money it is, not whose standing. A `payment` credit and a charge are untouched.
2. **clinic-ledger / wellness-ledger**: the same refusal on their credit's staff leg for `reason == "waiver"` (and
   a `reversesRef` reversal where the leg admits one), the owner resolved through each ledger's own `heldFor` walk.
3. **FE courtesy (cafe-app).** `isOwnAccount(bookerKey)` (pure): the arrears grid hides *Write off* and *Pay out*
   on the viewer's own row (the *Take payment* stays — a payment is not a clearing verb) and says *your own
   account — another staffer clears it*; the desk's resident ledger hides `refund-charge-btn` when the picked
   lease's resident is the viewer. clinic-app / wellness-app: the same courtesy at their waiver sites. Facet:
   `none` on the standing-dispatched refund / payout descriptors (the generic form cannot see the actor's own
   lease); `unreachable` on every `self`-dispatched credit descriptor, whose self leg refuses a waiver `AuthDenied`
   before the staff-leg check runs.
4. cafe-ledger `0.8.1 → 0.9.0`; clinic-ledger and wellness-ledger minor bumps; DDL prose + READMEs.

### Fire brief (build note, 2026-09-18 — S, compressed)

Scope (row, verbatim): *Refuse a clearing verb on the actor's own lease's account (the wellness/clinic ledgers
share the shape).* `Settle{paidCents}` descoped with the reason above. Green bar: a front-of-house staffer who
holds the lease is refused `SelfClearing` on `CreditCafeAccount{reason: waiver}`, `RefundCafeCharge` and
`PayoutCafeCredit` against their own account and accepted on a `payment` credit; the same staffer clears another
resident's account; an operator holding a lease is refused on their own; a staffer at another building is refused
on confinement, never `SelfClearing`; clinic + wellness waiver vectors mirror the first; `isOwnAccount` goja-pinned
and the arrears grid's markup pinned (own row: no Write off / Pay out, the note; other row: both). Touch-list:
`packages/cafe-ledger/scripts.go:1578-1700` (`post_entry`), its vectors (`ledger_test.go`,
`workplace_confinement_test.go:183-366` → mirror), `opmetas.go` (three `(facet)` lines), `package.go:130`,
`manifest.yaml:2`, README; `packages/{clinic,wellness}-ledger/scripts.go` credit staff leg + vectors + versions;
`cmd/cafe-app/web/app.js:1795-1870` (arrears grid), `:2635-2660` (refund gate), `:2940-2960` (refund form) and
each site's `refusal-courtesy` line; clinic-app / wellness-app waiver sites (find via `STRICT=1
lint-refusal-courtesy`). Precedents: the self leg's `heldFor` + `applicationFor` read; `require_workplace`'s
placement; `TestCreditWorkplace_*`. Gotchas: version bumps (three packages); `lint-refusal-courtesy` at every
site + Facet; the `_packages.md` dossier — *the "leg" of a guard is every op that WRITES the guarded value* (three
café verbs, one clinic, one wellness — grep each ledger's `reason` writers) · *a mirror that drops one of the
precedent's checks drops the invariant* (the refusal reads the account's OWN topology, never the payload) ·
checklist #3 (revert-prove each package's vector) · #6 (the sibling ledgers are the precedent being fixed, not
copied). Non-goals: a payment on one's own account; the loftspace ledger (its `PayOutBalance` is the landlord's
verb on a tenant's deposit — a different actor/owner pair); a resident's self leg (already refused `waiver`).

### Build note (2026-09-18)

Shipped `5e86a342` (merge `b3a354ca`); brief `745105f6`. Live on the shared stack (cafe-ledger 0.9.0, clinic-ledger
0.8.0, wellness-ledger 0.4.0 diff-applied; the three apps cycled): Sam Okafor (front of house, holder of lease
`KYeRsfCAYA51y5mrFYSK`) submitted a staff-leg `CreditCafeAccount{reason: waiver, 1¢}` against their own account
through the Gateway and was refused `SelfClearing: a staffer may not forgive their own account — another staffer
must`; a 1¢ `payment` on the same account was accepted, and a 1¢ waiver on another resident's account was accepted.
(Two 1¢ probe waivers landed on other residents' demo accounts while locating Sam's own — `U2zSpGUFWK1So6BRyjF6`,
`5jYLVXXwmgEshTitVQqR` — $0.02 of demo write-offs, left as posted.)

Deviations from the brief: none in scope; the clinic staff leg admits a `reversesRef` reversal and the wellness leg
a `refund`, both covered as the design's §2 "where the leg admits one". Review classification (one cold pass, three
lenses at capability-plane depth, over the whole diff): no BLOCKING — the forged-target, reason-order, Weaver-dispatch
and read-drift attacks were each grounded closed; **test-gap** — the clinic `reversesRef` leg had no vector (added,
revert-proven); **convention** — a Facet courtesy on a `self`-dispatched descriptor was `none` where `unreachable`
is the fact; a predicate's doc comment asserted the staffer is never the patient. Adjacent finds: none.
