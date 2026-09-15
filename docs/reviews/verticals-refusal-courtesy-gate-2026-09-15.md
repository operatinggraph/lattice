# Vertical FEs dossier — the sibling-form class goes to a gate (2026-09-15)

The third promotion pass over the `docs/components/vertical-apps.md` "Review keeps catching" dossier. The
**sibling-form class** — *a server-side refusal added to one form leaves its sibling form a dead end* — has been
seen three times (café house-tab payment cap 2026-09-05; wellness guest picker vs member picker 2026-09-13; café
`ItemUnavailable` 2026-09-15, where the third form was in ANOTHER client: Facet's descriptor-driven self-order,
fed by edge-manifest's `edgeEntityMenuItems`), each caught cold, and `agents/steward/SKILL.md` §4 owes a gate at
two. Its prescribed check — "when an op gains a server refusal, walk every form that dispatches it and give each
the courtesy its sibling already has; the census spans every client that dispatches the op" — is a census a lint
can force at exactly the moment it is skipped: when a script gains a `fail(` code, or a client gains a dispatch
site.

## Fire brief

**Scope sentence.** Promote the sibling-form class to a CI gate, `scripts/lint-refusal-courtesy.go`: for every
op that a package script can refuse with a *state* code and that is dispatched from two or more sites across the
clients (the four vertical FEs' `app.js` + Facet's descriptor form), every dispatch site carries a declaration
of the courtesy it gives that code — or an honest `none`/`unreachable` with its reason — and the declaration
stays true (a code the script can no longer raise, or an op the site no longer names, fails). Author the
declarations across the four FEs and the described ops' `opmetas.go` entries, **fix every sibling the census
finds without the courtesy its sibling already has**, wire the gate into `lint-static`, the Makefile and
`docs/components/lint-gates.md`, and retire the dossier entry.

**Why this shape (grounded live at `5912d907`).**

- *An op's refusal codes are derivable.* Every package script dispatches on `if ot == "X":` / `op.operationType
  == "X"` literal chains (all 12 `ddls.go` corpora; no dict dispatch, no `in [...]` beyond what
  `lint-opmeta-required-fields.go:250-316`'s `walkDispatch`/`opNames`/`isOpTypeExpr` already recognise — copy
  those three with provenance, a `//go:build ignore` main cannot be imported). Every `fail(` in the corpus takes a
  string literal first argument, or a `+` concatenation whose leftmost operand is one, of the shape `"<Code>: …"`
  (489 calls, 0 non-literal). The op's code set = every `fail(` code reachable from its dispatch block through the
  transitive closure of top-level `def` calls (refusals live in helpers: café `require_no_credit_hold` →
  `CreditHold`, `require_open_status` → `TabNotOpen`, `require_menu_item_price` → `ItemUnavailable`).
- *The exempt family is the codes whose courtesy is not per-form.* `InvalidArgument` (payload shape — the field's
  own validation, bound by `lint-opmeta-required-fields`), `WrongClass` (a site binds one class), `AuthDenied` /
  `PermissionDenied` (the hat the whole surface renders under), `NotFound` / `Unknown[A-Z]*` / `NotA[A-Z]*` (a
  dead or mistyped key — the read-model rows a site offers filter `isDeleted`). Everything else is a **state**
  code and is governed. The boundary is stated in the gate's header; widening it is a gate edit, not a
  declaration.
- *A dispatch site is derivable.* In a vertical `app.js` (read through goja's parser, the `lint-ceremony-throw-
  path.go:56-100` shape, with its `parseable` rewrite), a site is a **top-level function (or top-level
  statement)** that names a registered op: a string literal equal to the op name on a line matching
  `lint-app-op-descriptors.go:309`'s `submissionContext` (`operationType:` / `submitOp(` / `opOrThrow(`) or passed
  as a call argument, or a member access whose property is the op name (`opCatalogCache.CreditCafeAccount`,
  `catalog["X"]`); the `KNOWN_CATALOG_OPS` array literal and comments are not sites. **Facet is a site for every
  op whose `OpMetaSpec.Dispatch` is non-nil**: `edgeOpCatalog` projects every op meta (`packages/edge-manifest/
  lenses.go:727`, `WHERE op.data.operationType <> null`), the renderer resolves the row at dispatch (`cmd/facet/
  web/app.js:1762`), and the only per-state courtesy Facet's generic form can give is a column the entity lens
  projects and `entityRefCandidates` drops on (`cmd/facet/web/app.js:3021`, `available !== false`).
- *The declaration binds where the courtesy lives.* JS: `// refusal-courtesy: <Op>/<Code>: <verb> — <clause>`
  inside the site function's span (or on the comment block directly above its header). Go: `// refusal-
  courtesy(facet): <Code>: <verb> — <clause>` inside the op's `OpMetaSpec` composite literal in the owning
  package's `opmetas.go` (go/ast: a comment whose position lies between the literal's braces). Verbs are a closed
  vocabulary: `hide` (not rendered in the state) · `disable` · `drop` (the picker omits the row) · `cap` (an input
  bound) · `prefill` · `confirm` (asks first; the refusal is then the answer) · `none — <why no courtesy>` ·
  `unreachable — <why this site never reaches the branch>` · `see <fn>` (JS only: this function delegates to
  `<fn>`'s declaration; `<fn>` must be a site declaring the code). The clause is mandatory for every verb — it
  names the gate/column/filter, so a reviewer can open it.
- *The gate is default-deny and stays true.* For each governed (op, code): a site with no declaration FAILS at
  the function header; a declaration naming a code the op cannot raise FAILS (stale); a declaration in a function
  that names no such op FAILS; a `see <fn>` to a non-site or non-declaring function FAILS; an unknown verb or an
  empty clause FAILS. An op whose site count (JS sites + Facet) is 1 has no sibling and is ungoverned — the
  second site added later is what arms it. `STRICT=1` exits non-zero; unset warns; `--list` prints the governed
  census (op · code · script line · sites); `--selftest` runs the mutation battery (the
  `lint-derive-reads-bare-vector.go:787` shape).

**Touch-list (verified live).**

- `scripts/lint-refusal-courtesy.go` — NEW.
- `cmd/{cafe,clinic,loftspace,wellness}-app/web/app.js` — declarations at every governed site; the fixes the
  census finds.
- `packages/{cafe-domain,cafe-ledger,clinic-domain,clinic-ledger,loftspace-domain,loftspace-ledger,
  wellness-domain,wellness-ledger,identity-domain,lease-signing,service-location}/opmetas.go` — `(facet)`
  declarations on described governed ops (only those packages whose ops the FEs dispatch; the census decides).
  A `packages/` **comment-only** edit is not a semantic change — no version bump (`lint-package-version` reads
  the manifest/DDL bodies; confirm with `DIFF_BASE=5912d907 go run ./scripts/lint-package-version.go`).
- `.github/workflows/ci.yml` (`lint-static`, after `lint-seed-declared-reads`, ~line 585) · `Makefile`
  (`.PHONY` line 176 + a target beside `lint-seed-declared-reads:` 2535) · `docs/components/lint-gates.md` (one
  table row) · `docs/components/vertical-apps.md` (retire the entry into the walk-half statement).
- `scripts/lint-app-op-descriptors.go` `appOpCeilings` (215-291) — only if a fix adds a hand-wired op literal.

**Precedents to mirror.** `lint-opmeta-required-fields.go` (dispatch walk, self-test) ·
`lint-live-read-pinned-mutation.go` (`# occ:` declared escape, Starlark corpus over `pkgregistry.All()`) ·
`lint-ceremony-throw-path.go` (goja parse of `app.js`, function spans) · `lint-seed-declared-reads.go` (go/ast
over descriptors) · `lint-markup-escaping.go:140` (JS `// <family>:` declaration regex).

**Increment order.** (1) the gate + self-test, run `--list` on `main` for the census; (2) declarations per FE +
`(facet)` per described op, each site's courtesy verified by opening it, siblings without their sibling's
courtesy FIXED (goja-pinned where the precedent pins); (3) wiring + registry + dossier retire; (4) cold review
over the whole diff; gates.

**Gotchas.** A `fail(` inside a nested `def` belongs to the enclosing top-level def's closure. A helper shared by
several ops over-attributes a code to an op that never reaches the branch — `unreachable — <why>` is the honest
declaration, not a silent exemption. `KNOWN_CATALOG_OPS` names ops the file never dispatches by literal
elsewhere — those are dispatched through `opCatalogCache.X` member reads, which ARE sites. goja does not parse
the dynamic `import()` — reuse `parseable`. Comments are not in goja's AST — scan raw lines for the declaration
and map line → enclosing function span from the AST.

**Standing checklist, part 5.** (2) every census is a premise — the `--list` output is the population, re-run
after every fix; (3) a negative test needs its positive vector — every FAIL shape in the battery has a passing
twin; (6) precedent may carry debt — `submissionContext` counts comments, this gate must not.

**Non-goals.** No new courtesy mechanism in Facet beyond the `available` column pattern; no OpMetaSpec schema
field; no descriptor-driven migration of hand-built forms; no change to `lint-app-op-descriptors`' R1/R2.

## Census

*(filled at build — the `--list` population on `main` and what the declarations found.)*
