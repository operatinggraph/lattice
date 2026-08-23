# History-comment gate — the "this fire"-shaped narration class

**Status:** ✅ Winston-ratified — build-ready (2026-08-23). Steward-owned tooling item; no frozen contract,
no architectural fork. Board row: `backlog/lattice.md` → *[Tooling] The history-comment gate misses
"this fire"-shaped narration*.

## 1. The gap

CLAUDE.md's no-history-comment rule is the repo's most-violated convention, and `scripts/lint-conventions.go`
mechanizes it (`historyComment`, `scripts/lint-conventions.go:239`). Its phrase list screens narration written
in the *past* tense about a *named prior state* — `Previously`, `Was:`, `Replaces`, `renamed from`,
`moved from`, `formerly`, `Before the fix`, `// Story N`.

It does not screen the shape this fleet's builders actually write: narration anchored on **the change being
made right now** — "this fire", "this fix", "this change". Those comments fail the rule for exactly the reason
the rule exists: a reader with no idea a change ever happened cannot resolve the referent. The gate reports the
tree clean while 89 such comments stand.

## 2. Phrase admission — the evidence

`historyComment`'s own doc comment binds phrase admission to measurement: a candidate is counted tree-wide
first, and one whose hits are dominated by legitimate prose is rejected rather than gated (`used to`, 96 hits,
and `no longer`, 255, were rejected on that test). Re-run over every tracked `*.go` outside `scripts/lint-*`,
comment text only, at `1abea2d`:

| candidate | hits | files | verdict |
|---|---|---|---|
| `this fire` | 72 | 59 | **admit** — every hit narrates the authoring change |
| `this fix` | 14 | 12 | **admit** — every hit is `before this fix` / `the bug this fix closes` shaped |
| `this change` | 3 | 3 | **admit** — all three narrate |
| `this run` | 25 | 12 | **reject** — dominated by runtime prose (a CLI invocation, `cmd/lattice-pkg`) |
| `this pass` | 37 | 21 | **reject** — dominated by runtime prose (a reconcile/verify pass) |
| `this commit` | 3 | 3 | **reject** — `commit path` / `commit step` is core Processor vocabulary, and RE2 has no lookahead to separate it; a 1-in-3 false-positive rate on a blocking gate is not worth 2 sites, which this fire repairs by hand instead |

Admitted union: **89 hits across 71 files**, 8 of them non-test files under `packages/`.

## 3. Why the sweep needs a `lint-package-version` fix first

`scripts/lint-package-version.go` demands a manifest version bump for any changed file under
`packages/<name>/` that is not `_test.go`, not `.md`, and not import-only (`main`, lines 76-85). A comment-only
edit therefore extorts a version bump — publishing a package revision that asserts a content change which did
not happen, so every stack diff-applies the package for nothing. The repo already pays this: `bc22234`
("bump the manifest version for the package-doc edit").

The exemption already exists — `commentOnlyGoChange` (`:314`), an AST-without-comments comparison — but it is
wired only into `walkGeneratorConsumers`' `internal/pkgmgr/` branch. Extending it to the in-package branch is
the whole fix, and it is the same reasoning the walk-generator branch already states verbatim.

**Precedent debt in the mirrored helper (standing-checklist #6).** `commentOnlyGoChange` parses without
`parser.ParseComments`, so it drops **directive** comments too — `//go:build`, `//go:embed`, `//go:generate`.
A changed build tag is content, not a comment, and would ride through the exemption invisibly. This is not
theoretical here: `packages/lease-signing/` carries four constrained files
(`freshness_window{,_short}.go`, `renewal_window{,_short}.go`) whose `//go:build` tag decides which window
compiles into the installed Definition. Harden the helper itself — comparing the `//go:` directive lines at
both revisions — which closes the same latent hole on the existing walk-generator branch.

## 4. Increment order

1. **`lint-package-version`** — harden `commentOnlyGoChange` with a `//go:` directive comparison; wire it into
   `main`'s in-package `.go` branch. Green: `go run ./scripts/lint-package-version.go` clean; a comment-only
   edit under `packages/clinic-domain/` stays clean; a `//go:build` tag edit under `packages/lease-signing/`
   still demands a bump.
2. **`lint-conventions`** — admit the three phrases into `historyPhrases`; record the rejections in the
   evidence comment; add self-test cases for the admitted and the rejected shapes (the message prefix filter
   in `selfTest` must learn `history/changelog`). Green: self-test passes; the gate now reports the 89 sites.
3. **The sweep** — repair all 89 comments so each describes current behaviour, plus the 2 genuine `this commit`
   sites §2 rejected from the gate. Green: `STRICT=1 go run ./scripts/lint-conventions.go` → 0 issues, and
   `go run ./scripts/lint-package-version.go` clean with **no** manifest bumped.
4. **Gates** — `go build ./...`, `make vet`, `golangci-lint run ./...`, `go run ./scripts/lint-board.go`,
   `go test ./...` with `POSTGRES_TEST_DSN` set (REMOTE.md §3).

## 5. Non-goals

`the fix` (52 hits), `the old` (103), `the new` (182), `legacy` (135), `pre-existing` (91) and the rest of the
measured tail: each is a distinct admission question with its own false-positive profile, and none is the
self-referential shape this row names. Not screened here, not swept here.

## 6. Build note

See the commit trail on `claude/great-lamport-2hvzj6`.
