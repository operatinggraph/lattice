# LoftSpace — "Rent owed" never says how late (2026-09-15)

**Filed (PO, 2026-09-15):** the portfolio list ranks balances by amount ([app.js:4203](../../cmd/loftspace-app/web/app.js))
and neither it nor the statement reads the recorded `dueAt` ([lenses.go:84](../../packages/loftspace-ledger/lenses.go));
a bill posted today and one 40 days overdue read alike, and no debtor is ever reminded. Live: Priya's $2,400 due 09-08
shows under Riley's $3,300 with no age. "Due in N days" / "N days overdue" per lease; one reminder per arrears episode
(the café/wellness `.arrears` idiom). Winston-adjudicated (implementation-level; no contract surface, no fork — the
wellness ledger's arrears mechanism is the ratified pattern, applied to one more ledger that stores no balance).

## Grounding

- **Loftspace-ledger stores no balance and no arrears state.** `vtx.account` root data is `{}` by design and the balance
  is derived by the `ledgerHistory` lens ([scripts.go:29](../../packages/loftspace-ledger/scripts.go)); no `.arrears`,
  no `targets.go`, no `notifications.go`, no `derive_reads`. `post_entry` ([scripts.go:539-862](../../packages/loftspace-ledger/scripts.go))
  already replays the account's own `postedTo` history paged for the resident's self-credit cap
  (`SELF_CREDIT_PAGE_LIMIT` 50 × 10, [scripts.go:625-649](../../packages/loftspace-ledger/scripts.go)) — the
  `arrears_entries` walk is the same enumeration with the same `(e)` posture.
- **The due date is already RECORDED on the entry.** `DebitAccount` stamps `periodStart` / `periodEnd` / `dueAt` on the
  `.entry` from the clause's anniversary grid ([scripts.go:772-790](../../packages/loftspace-ledger/scripts.go));
  `ledgerHistory` projects it ([lenses.go:84](../../packages/loftspace-ledger/lenses.go)) and the one-bill lens
  carries it too. A payment or a landlord one-off (`LoftspaceRecordCharge`) records none. This is where LoftSpace
  differs from café/wellness, whose head is aged by a 15-day TERM from its posting: here the head's own recorded
  `dueAt` is the fact, and a clock-free rule ("due on receipt" = its `postedAt`) covers an entry that records none.
- **The wellness ledger is the closer precedent (no stored balance).** `EvaluateWellnessArrears`
  ([wellness-ledger/scripts.go:419-640](../../packages/wellness-ledger/scripts.go)): Weaver-actor guard, paged replay
  with `historyTooLong` degrade, FIFO head = episode start, `remindedFor` on every due evaluation, `sentAt` once per
  episode with the send at-or-before the head's `postedAt` dropped as a finished episode's, `external.notification`
  keyed `accountKey:dueAt`, `{evaluatedAt}` alone when nothing is owed; `post_entry` carries + `stale`s an existing
  `.arrears` and mints nothing ([scripts.go:1082-1107](../../packages/wellness-ledger/scripts.go)); `derive_reads`
  on both scripts returns `optionalReads [root, .arrears]` so the upsert stays OCC for an undeclared submitter
  ([scripts.go:386, 1114](../../packages/wellness-ledger/scripts.go)); the `wellnessArrearsReminders` lens
  ([lenses.go:251-278](../../packages/wellness-ledger/lenses.go)) arms `freshUntil = dueAt`, opens
  `missing_evaluation` on never-evaluated / stale / recorded lapse ≥ dueAt with `remindedFor <> dueAt`; the playbook's
  `Params` name only `row.entityKey` ([targets.go](../../packages/wellness-ledger/targets.go));
  `RecordWellnessArrearsReminderNotification` → `.arrearsNotification` as an idempotent overwrite
  ([notifications.go](../../packages/wellness-ledger/notifications.go)); the member-accounts lens projects
  `arrearsDueAt / RemindedFor / ReminderSentAt` (café: [cafe-ledger/lenses.go:143-151](../../packages/cafe-ledger/lenses.go)).
- **No refund netting exists to mirror.** Wellness/café pre-net a refund credit against the charge it `reverses`.
  No loftspace entry carries a `reverses` / `settlesRefund` link (census: `grep -n 'reverses\|settlesRefund'
  packages/loftspace-ledger/*.go` → 0); the invariant that pre-pass enforces (a credit naming a charge retires THAT
  charge, not the oldest) has no writer in this ledger, so the FIFO runs plain. The day a refund verb lands here, it
  lands with the pre-pass.
- **The tenant is two hops from the account.** `account -[heldFor]-> leaseapp -[applicationFor]-> identity`. `heldFor`
  is written once and never repointed (a page of one is exact — [scripts.go:581-586](../../packages/loftspace-ledger/scripts.go));
  `applicationFor` is paged `LEASEAPP_UNIT_PAGE_LIMIT`-style by every lease-signing reader
  ([leasedoc_scripts.go:112-131](../../packages/lease-signing/leasedoc_scripts.go)). The lease vertex itself must be
  live-checked (a withdrawn lease dangles live links, [scripts.go:589-598](../../packages/loftspace-ledger/scripts.go)).
- **Three app surfaces render the balance, none reads a due date.** Landlord ledger `refreshLedgerBody`
  ([app.js:3431](../../cmd/loftspace-app/web/app.js), `/api/ledger` [ledger.go:189-274](../../cmd/loftspace-app/ledger.go),
  `ledgerHistory` + `leaseAccounts` buckets); tenant statement `refreshStatementBody` ([app.js:3557](../../cmd/loftspace-app/web/app.js),
  `/api/one-bill` [one_bill.go:103-165](../../cmd/loftspace-app/one_bill.go), the composed rent+café history);
  portfolio `renderPortfolioArrears` ([app.js:4203](../../cmd/loftspace-app/web/app.js), `/api/portfolio-pulse`
  `computeLandlordLeaseBalances` [portfolio.go:129-150](../../cmd/loftspace-app/portfolio.go), sorted by amount).
  `fmtUTCDate` ([app.js:1489](../../cmd/loftspace-app/web/app.js)) is the UTC-date renderer the entry labels already
  use. The wellness app's `deriveStatement` / `recordedOrDerivedDueDate` / `statementLine`
  ([ledger.go:294-375, 625](../../cmd/wellness-app/ledger.go), [app.js:505-521](../../cmd/wellness-app/web/app.js)) is the
  shape for the Go derivation and the goja-pinned line.
- **`vtx.account` is anchored only by loftspace-ledger, one-bill and semantic-contracts** (`chargesTo`, `postedTo`,
  `heldFor` walks; census: `grep -rn '(\w*:account[ )]' packages/*/lenses.go` → 4 sites, none reads `.arrears`).
  `lint-refusal-courtesy` governs the `InvalidState` the class check adds to `post_entry` at every FE dispatch site of
  `CreditAccount` / `LoftspaceRecordCharge` ([app.js:3626-3629](../../cmd/loftspace-app/web/app.js)) and any Facet descriptor.
- **No `scripts/verify-package-loftspace-ledger.go`** (no `ddlCheck` pin to extend); no seed submits a ledger op.

## Verdict — *the rent ledger records its episodes the way the wellness ledger does, and every surface says how late*

1. **`.arrears` on `vtx.account`** (class `loftspaceAccountArrears`) = `{evaluatedAt, dueAt?, remindAt?, remindedFor?,
   sentAt?, stale?, historyTooLong?}`. **`dueAt` is the FIFO head's own recorded `.entry.dueAt`** — its `postedAt`
   when it records none (a landlord one-off is due on receipt) — never a term added to the posting. **`remindAt =
   dueAt + ARREARS_GRACE_DURATION` (`"120h"`, `ArrearsGraceDays = 5`)**: rent is due on its date and "N days overdue"
   counts from it; the reminder waits out the customary five-day grace, so a rent charge that posts on its due date
   does not nag the same morning. Lifetime = wellness's, verbatim: minted only by the evaluation; `post_entry` (every
   debit/credit) carries + marks `stale` when it exists and mints nothing when absent; the never-evaluated gap first
   evaluates every standing account; the episode ends at the evaluation — `{evaluatedAt}` alone with nothing owed, and
   a `sentAt` at or before the head's `postedAt` dropped as a finished episode's; `stale` / `historyTooLong` never
   carried past an evaluation that read the history. Never tombstoned.
2. **`EvaluateLoftspaceArrears{accountKey}`** on the account DDL script: Weaver-actor guard first; paged replay
   (`ARREARS_PAGE_LIMIT` 50 × `ARREARS_MAX_PAGES` 10, `historyTooLong` recorded, never a refusal); plain FIFO
   (postedAt, key) with surplus carry — the head returns `{postedAt, dueAt}`; `remindedFor = dueAt` on every
   evaluation where `remindAt <= evaluatedAt`; `sentAt` once per episode with an `external.notification` keyed
   `accountKey:dueAt`, `replyOp RecordLoftspaceArrearsReminderNotification`, params `{accountKey, leaseAppKey?,
   identityKey?, reminderType: "loftspaceRentArrears", dueAt, balanceCents}` — the lease and identity resolved by the op
   from the account's own `heldFor` → live lease → `applicationFor` walk, never the payload, and optional (an account
   whose lease is withdrawn still ages). Operator-granted (Weaver's service actor). `derive_reads` on BOTH scripts.
3. **`loftspaceArrearsReminders`** weaver-target lens + playbook: wellness's cypher on `account`, with `freshUntil =
   remindAt`, the lapse compare `byTarget.<target> >= remindAt`, the reminded conjunct `remindedFor <> dueAt`;
   `missing_evaluation → directOp(EvaluateLoftspaceArrears)` with `Params {accountKey: row.entityKey}`,
   `OptionalReads [row.entityKey.arrears]`, enumerations `postedTo in` / `heldFor out` on `row.entityKey`; a
   `maxretries_evaluation` column per the wellness idiom. **`RecordLoftspaceArrearsReminderNotification`** →
   `.arrearsNotification` (class `loftspaceAccountArrearsNotification`), idempotent overwrite (episodes recur).
4. **`leaseAccounts` projects `arrearsDueAt`, `arrearsRemindedFor`, `arrearsReminderSentAt`** (café's three columns).
5. **The app says how late, on all three surfaces.** `/api/ledger`, `/api/one-bill` and every `/api/portfolio-pulse`
   lease-balance row carry `dueDate / isOverdue / daysOverdue / daysUntilDue / reminderSentAt` for the RENT account: the
   recorded `arrearsDueAt` wins when the `leaseAccounts` row has one, else the app derives it by the op's own rule
   (FIFO over the `ledgerHistory` rows; head's recorded `dueAt` else `postedAt`); overdue at the instant
   (`dueDate <= now`), days floored on whole UTC days. The portfolio ranks **most overdue first** (daysOverdue desc,
   then balance desc; not-yet-due rows last) under "💸 Rent owed (most overdue first)"; one goja-pinned
   `rentBalanceLine(data)` renders `Balance owed: $X · rent due <UTC date> · N days overdue` / `· due in N days` /
   `· a reminder was sent <UTC date>` for the landlord ledger and the tenant statement alike (the one-bill balance is
   rent + café, so the suffix names RENT), and each portfolio row carries the same age text.
6. **The DDL text states the rule**; loftspace-ledger 0.7.4 → 0.7.5; README updated.

**Not a hold.** Nothing refuses on `.arrears` here — the filed demand is age + one reminder; a hold on a tenant's op has
no ratified target in LoftSpace (a tenant cannot be refused rent-paying, and no discretionary consumer verb exists on
the lease the way a café tab or a class seat does). A future hold is its own row.

### Fire brief (build note, 2026-09-15)

1. **Scope** (board row, verbatim): *The portfolio list ranks balances by amount and neither it nor the statement reads
   the recorded `dueAt`; a bill posted today and one 40 days overdue read alike, and no debtor is ever reminded. Live:
   Priya's $2,400 due 09-08 shows under Riley's $3,300 with no age. "Due in N days" / "N days overdue" per lease; one
   reminder per arrears episode (the café/wellness `.arrears` idiom).* Green bar: every standing account is evaluated
   once by Weaver; an account whose head's recorded `dueAt` is 5+ days past is stamped `sentAt` and notified once per
   episode; a payment to zero ends the episode; the landlord ledger, the tenant statement and the portfolio list say
   due / due in N / N days overdue / reminded, the portfolio most-overdue first; Priya (due 09-08) ranks above Riley.
2. **Touch-list (verified live):** loftspace-ledger `scripts.go:59-70` (account script helpers; the evaluate block joins
   `execute` at the account script's dispatcher) + `:385-400` (transaction script header) + `:539-545` (`post_entry`
   entry: the `.arrears` carry+stale rides the mutations list at `:822-829`) + `:864-892` (`execute`; `derive_reads`
   added above it) · `ddls.go:19` (account DDL: `PermittedCommands += EvaluateLoftspaceArrears`, description), `:66`
   (guard aspect, the shape for the two new aspect-type DDLs), `:94` (transaction DDL description) · `lenses.go:40-58`
   (`Lenses()` + new target lens) + `:91-99` (`leaseAccountsSpec`) · new `targets.go`, new `notifications.go` (both
   wellness's files renamed) · `permissions.go:109` (the shape for the operator grant) · `opmetas.go` (op-meta for the
   evaluate + the reply op, per wellness) · `package.go:59` + `manifest.yaml:2` · tests `ledger_test.go`,
   `lens_cypher_test.go`, new `arrears_test.go`; `internal/refractor/{actor_onekey,anchor_hopindex,actor_walk_scope,
   branch_decomposition_pins,label_derivation,grouping_reduction}_corpus_census_test.go` (one pin each for the new
   lens; `leaseAccounts` RETURN edit re-pinned if a census keys on it). loftspace-app `ledger.go:26-50` (projections
   gain the three arrears columns on the lease-accounts row), `:56-103` (`computeLedgerHistory` — the rows the FIFO
   derives from), `:189-274` (`handleLedger`) · `one_bill.go:103-165` · `portfolio.go:116-150`, `:396-454` ·
   `web/app.js:1489` (`fmtUTCDate`), `:3431-3460`, `:3557-3580`, `:3626-3629` (courtesy lines), `:4203-4232` ·
   tests `ledger_test.go`, `one_bill_test.go`, `portfolio_test.go`, new `rent_arrears_ui_test.go` (goja, harness per
   `renewal_ready_test.go:20`).
3. **Precedents:** wellness-ledger `scripts.go:140-330` (`arrears_entries` / `arrears_head` / `carry_arrears` /
   `identity_for_account` / `derive_reads`), `:419-640` (evaluate), `:718-790` + `:1082-1152` (post_entry carry +
   transaction `derive_reads`), `lenses.go:240-278`, `targets.go` (ArrearsReminders block), `notifications.go` whole;
   cafe-ledger `lenses.go:137-151`; wellness-ledger tests `TestArrears_*` / `TestWellnessArrears_*`; wellness-app
   `ledger.go:209-216, 294-375, 559-632`, `app.js:505-521`, `web_hold_test.go:44-114` (the line pins); loftspace-app
   `lease_term_ui_test.go` (UTC pin under a pinned LA zone).
4. **Increments:** (1) loftspace-ledger — aspect + op + `post_entry` carry/stale + `derive_reads` × 2 + notification
   replyOp + target lens + playbook + `leaseAccounts` columns + permissions + opmetas + version + README; tests mirror
   wellness's set minus netting, plus the loftspace-specific vectors: head's recorded `dueAt` wins over `postedAt`;
   a head with no `dueAt` is due at its `postedAt`; `remindAt = dueAt + 120h` and nothing sends inside the grace; the
   lens arms `freshUntil = remindAt`; identity resolved through a live lease, none through a withdrawn one; bare-envelope
   vectors for the three transaction ops + the evaluate. Green: `go test ./packages/loftspace-ledger/ -count=1`,
   `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`. (2) loftspace-app — the arrears columns on the
   lease-accounts projection, `deriveRentArrears` (FIFO, head's `dueAt` else `postedAt`), recorded-wins in all three
   handlers, `daysOverdue` / `daysUntilDue`, the portfolio sort, `rentBalanceLine` + the portfolio row's age text,
   courtesy lines; goja pins per suffix + the sort; Go tests for the derivation (recorded wins; no dueAt → postedAt;
   overdue boundary at the instant; the sort). Green: `go test ./cmd/loftspace-app/ -count=1`, every
   `scripts/lint-*.go` (STRICT=1 where honoured), `golangci-lint run ./...`, `make vet`. (3) live: `make
   refresh-loftspace`, cycle `bin/loftspace-app`; every standing account evaluates once; Priya's account (due 09-08,
   7 days) → `sentAt` + notification; portfolio ranks her above Riley; the tenant statement says "7 days overdue".
5. **Gotchas:** one package edit ⇒ one version bump + `Version` constant; a lens RETURN edit / new lens is a corpus edit
   (six `internal/refractor` census pins); `lint-links-page-limit` on every new walk; `lint-live-read-pinned-mutation`
   — the `.arrears` upsert is OCC on the declared read's revision (derive_reads is the guarantee, the playbook's
   OptionalReads the documentation); `lint-derive-reads-bare-vector` — a bare-envelope vector per op that gains
   `derive_reads` (four); `lint-refusal-courtesy` — the class-check `InvalidState` at every `CreditAccount` /
   `LoftspaceRecordCharge` site (`app.js:3626`) and any Facet descriptor; `lint-app-op-descriptors` — no op name in a
   `cmd/loftspace-app` Go comment; `lint-seed-declared-reads` — `grep -rn "DebitAccount\|CreditAccount\|LoftspaceRecordCharge" scripts/`
   (none submit); the Weaver playbook's `Params` names only `row.entityKey`. `_packages.md` dossier: *a mirror that
   drops one of the precedent's checks drops the INVARIANT* (the netting pre-pass is dropped — no writer of `reverses`
   here, stated above; every other wellness branch is kept); *a recorded value is read as the FACT it records*
   (`dueAt` = the entry's own recorded due, never re-gridded; `sentAt` is the SEND INTENT — the FE says "a reminder was
   sent" off `sentAt` as the wellness app does, and `.arrearsNotification` is the delivery fact); *a dispatch
   declaration must name what the runtime binds* (no optional-hop Params); *the "leg" is every writer of the guarded
   value* (three writers post entries — all three carry + stale). `vertical-apps.md` dossier: *two courtesy surfaces
   name the same instant* (every date the line renders is a UTC slice via `fmtUTCDate`, pinned under a pinned LA zone);
   *a count the FE promises must apply the op's own predicate* (days overdue counts from the head's `dueAt`, the op's
   rule); *a value reaches markup unescaped* (every new string is `.textContent`). Standing checklist #1 (the lifetime
   above) · #3 (each lens conjunct and each refusal revert-proven; the threaded `reminderSentAt` asserted equal to its
   source in a handler test) · #5 (`.arrears`: evaluation mints/rewrites, `post_entry` carries+stales;
   `.arrearsNotification`: one writer) · #6 (wellness's `.balance`-free branch is the only branch here).
6. **Adjacent finds:** none surfaced by the scouts.
7. **Non-goals:** a `CreditHold` on any LoftSpace op; a stored balance; stamping `dueAt` on `LoftspaceRecordCharge`
   (due-on-receipt is the read rule); café-side age on the one-bill (the café ledger has its own); the notice /
   early-end row; the deposit row; the wellness/café mechanisms themselves.
