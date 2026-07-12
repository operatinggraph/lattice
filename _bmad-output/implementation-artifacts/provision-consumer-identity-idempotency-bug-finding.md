# `ProvisionConsumerIdentity` idempotency check is wrong — self-service apply is broken for every vertical (finding, 2026-07-11)

**Repro (live, this dev stack, PO discovery fire):** LoftSpace `+ New applicant` (submits
`CreateUnclaimedIdentity`) → select the new applicant → `Apply` on any listing → **`Application rejected —
AuthDenied: no matching platformPermission`**. Reproduced on 3 identities: two pre-existing picker entries
(`PO Discovery Test`, `Selfservice Sam`) and a brand-new one created fresh during this fire
(`vtx.identity.JggGZ5k9zYMmhcvQaLxE`, first-ever Gateway touch). `processor.log` confirms the Gateway's
`provisionActorIfNeeded` pre-flight fires `ProvisionConsumerIdentity` immediately before every one of these
(`authorized`, `step 4: hydrated`) but **executes with `mutations=0 events=0`** — a silent no-op — so the
subsequent `CreateLeaseApplication` still 403s. Every applicant identity currently in this stack's picker
holds **zero** `holdsRole` links (`lattice graph keys lnk.identity.<id>.holdsRole` — empty for all 6).

**This supersedes [self-service-identity-env-gap-finding.md](self-service-identity-env-gap-finding.md).**
That finding's two env gaps (stale Gateway binary, missing `identityProvisioner` grant) were real and were
fixed + closed 2026-07-10 (`verticals.md` Done log). But the flow is broken again — live-verified today,
2026-07-11 — for a **different, code-level reason**, not an env regression.

**Root cause — `ProvisionConsumerIdentity`'s idempotency check reads the wrong key.**
`packages/identity-domain/ddls.go:724-730`:
```
existing = kv.Read(target_actor_key)      # reads the IDENTITY VERTEX
if existing != None:
    return {"mutations": [], "events": []}   # "already provisioned" no-op
```
This treats "an identity vertex exists at this key" as "this actor already holds the consumer role." But
every vertical app's own "New applicant"/"New patient" flow (`loftspace-app` `app.js:680`, `clinic-app`
`app.js:603`) calls `CreateUnclaimedIdentity` **first** — which creates exactly that bare identity vertex,
unclaimed, holding no role, by design (§the "no claim ceremony in this demo" comment at `app.js:565`). So
the very first time that identity ever reaches the Gateway (its `CreateLeaseApplication`/`BookAppointment`
self-submit), `ProvisionConsumerIdentity`'s pre-flight sees the vertex already exists (from the earlier
`CreateUnclaimedIdentity`) and **silently skips granting the role** — permanently. There is no retry path:
the vertex will always exist from here on, so this op no-ops on every future attempt too.

The correct idempotency key is the **`holdsRole` link**, not the vertex — exactly the pattern
`AssignRole` already uses correctly (`packages/rbac-domain/ddls.go:333-339`, checks
`state[lnk_key]`, not the actor vertex). `ProvisionConsumerIdentity` should mirror that: read
`lnk.identity.<id>.holdsRole.role.<consumerRoleId>`, no-op only if *that* exists, and otherwise proceed to
grant the role regardless of whether the identity vertex itself pre-exists (mutations for the vertex/state
aspect become conditional-create-if-absent instead of assumed-absent).

**Blast radius — every vertical, not just LoftSpace.** `clinic-app`'s self-book path
(`app.js:272-290`, `asSelf`/`authContext.target`) follows the identical shape (`CreateUnclaimedIdentity`
then a self-submit relying on the same Gateway pre-flight) and will hit the same no-op. This is the
platform's one and only self-service consumer-provisioning mechanism — the bug blocks the "real applicant
applies for themselves" path (the design's own proof case, `real-actor-write-auth-e2e-design.md` §3.4) for
every vertical that follows the documented create-then-self-submit UX, which is all of them.

**Fix is small and precedented** — swap the idempotency check to the `holdsRole` link (mirror
`AssignRole`), keep the vertex/state-aspect mutations conditional. No FE change needed.
