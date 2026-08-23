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
| `this increment` | 35 | — | **admit** — the same grammar under another word (§2.1) |
| `pre-fix` | 11 | — | **admit** — a compression of the already-banned `Before the fix` (§2.1) |
| `this run` | 25 | 12 | **reject** — dominated by runtime prose (a CLI invocation, `cmd/lattice-pkg`) |
| `this pass` | 37 | 21 | **reject** — dominated by runtime prose (a reconcile/verify pass) |
| `this commit` | 3 | 3 | **reject** — `commit path` / `commit step` is core Processor vocabulary, and RE2 has no lookahead to separate it; a 1-in-3 false-positive rate on a blocking gate is not worth 2 sites, which this fire repairs by hand instead |

First admitted union: **89 hits across 71 files**, 8 of them non-test files under `packages/`.

### 2.1 What the first sweep surfaced — the gate was line-anchored

Two of the sweep builders reported the same shape independently: narration the gate had *not* flagged
sitting three lines from narration it had. The cause is that the check matched one physical line at a time,
and a wrapped doc comment breaks its sentences wherever the margin falls —

```go
// Mode selects the planner-extension posture (§10.8 Planner extension,
// Fire 4): "" (absent, the default — every target installed before this
// fire) is frozen table-only behavior, byte-identical; …
```

— so the gate reads two clean lines and lets it through (`internal/weaver/registry.go:301`). A gate that a
line break defeats does not enforce the rule; it enforces a formatting accident. **The line-anchored pass
is joined by a block pass**: maximal runs of comment-ONLY lines are joined and matched as one string, the
finding reported on the line the phrase starts on. Only comment-only lines join — a run of trailing comments
on consecutive code lines is not one sentence, and joining those would manufacture a phrase from two
unrelated remarks.

Measured alongside it, and admitted for the same reason: **`this increment`** (35 hits) is the same
self-referential grammar with another word in it, and **`pre-fix`** (11) is a compression of the
already-banned `Before the fix`. Both are dominated by narration in every sampled hit.

Strengthening the gate surfaced a second sweep of **71 sites across 51 files**.

### 2.2 Two accepted false positives

Both cost a rewording, never a wrong green, and both are recorded at the phrase list so the next author who
trips one is not left guessing:

- **`fire` as a verb** — `// should this fire next` (`packages/clinic-reminders/visitseries.go`). Reworded to
  "trigger".
- **`change` as a verb inside a quoted question** — `// what makes "did this change" answerable at all`
  (`cmd/refractor/taxonomy_reload.go`). Surfaced only by the block pass, which joins the quote's two halves.

RE2 has no lookahead, and a special case carved into a lint gate is a worse liability than two rewordings.

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
4. **Close the line-anchoring** (§2.1) — the block pass, `this increment` + `pre-fix`, and the second sweep of
   71 sites, plus one `t.Fatalf` message carrying the same narration into test output. Same green bar.
5. **Gates** — `go build ./...`, `make vet`, `golangci-lint run ./...`, `go run ./scripts/lint-board.go`,
   `go test ./...` with `POSTGRES_TEST_DSN` set (REMOTE.md §3).

Every swept file is additionally proven **comment-only** by comparing its comment-stripped AST against `main`,
the same technique `commentOnlyGoChange` uses — a sweep this wide cannot be eyeballed for a stray code edit.

## 5. Non-goals

`the fix` (52 hits), `the old` (103), `the new` (182), `legacy` (135), `pre-existing` (91) and the rest of the
measured tail: each is a distinct admission question with its own false-positive profile, and none is the
self-referential shape this row names. Not screened here, not swept here. Narration inside **string literals**
is out of scope too — the gate reads comments, and the one `t.Fatalf` message found carrying it is repaired by
hand rather than by widening the gate to Go string content.

## 6. Build note

Commit trail on `claude/great-lamport-2hvzj6`. Two sweeps, 160 comment sites across 115 files, plus the
gate work. What the close adversarial pass found, and what it means for the next sweep of this shape.

### 6.1 The review's findings, classified

**Implementation-bug — the dominant class, and the one the gate cannot see (3 BLOCKING + 1 MAJOR).** Four
rewrites removed the banned phrase and left a sentence that is *false about current behaviour*:

- `internal/natsperm/conf_test.go` claimed core-events "sits beyond `protectedStreamDenies`' reach"; the
  matrix denies over it explicitly. The truth is that it is outside the `PlatformBuckets()` *loop*.
- `packages/identity-domain/derive_reads_test.go` claimed ClaimIdentity's submitter declares the
  credentialindex probe; the same file's function doc says no submitter ever does, which is the entire point.
- `internal/refractor/pipeline/plain_derivation_act_e2e_test.go` claimed the far position "never learns"
  the new value; the seeded multi-position test in the same file asserts that it does.
- Eight `byte-identical to a target declaring no Mode` rewrites made the comparand the thing being defined,
  converting a real backward-compatibility invariant into a tautology.

**The lesson, which no gate in this fire mechanizes:** a green gate proves the *phrase* is gone, not that
the *replacement is true*. A prose sweep is a correctness change to documentation, and it needs an
independent truth check — a cold reader who re-derives each claim from the code — exactly as a code change
does. Nothing about "comment-only" makes it safe; the AST proof below establishes only that behaviour did
not move. If a second sweep of this shape produces the same class, it earns a standing-checklist slot.

**Design-gap — the mirrored helper carried debt (1 MAJOR).** `commentOnlyGoChange` compared directive lines
as text, which the review defeated four ways: a `//go:build` relocated below the package clause (constraint
silently deleted), the legacy `// +build` form (unmatched), a `//go:embed` re-associated to a different var,
and a cgo preamble's C source (a comment that compiles). Each is now refused — directives are compared
paired with the declaration they bind to, `// +build` is matched, and a file importing `"C"` is refused
outright. Zero of these shapes exist under `packages/` today, so this was latent, not live.

**Implementation-bug found by the fire's own proof harness.** The AST comparison keeps node positions, and
the printer spaces statements by the line gap between them — so inserting one comment line *between* two
statements printed a blank line that was not there before, and the exemption answered "content changed". A
comment edit that adds a line would have extorted exactly the version bump this exemption exists to stop.
Blank lines are now stripped before comparing.

**Design-gap — phrase admission (1 MINOR).** `\b` matches a hyphen, so `this fire-and-forget path` tripped
the gate; `fire-and-forget` is house vocabulary in 20+ comments. A hyphen after the word is now excluded.
Three false-positive shapes remain, recorded at the phrase list (§2.2 plus the two domain nouns
`this change request` and `by this increment`).

**Test-gap (1 MINOR).** A self-test case named "…is reported on its own line" asserted only the message; the
harness never reads `fd.line`, so the offset-to-line arithmetic was unpinned. `historyLineSelfTest` now pins
it across five shapes, including a three-line span and a match preceded by multi-byte em-dashes.

**Pre-existing hole the new phrases inherited (1 MINOR).** `/* … */` comments evaded the gate entirely.
Closed; zero live instances, so this was prophylactic.

### 6.2 What the sweep is proven to be

Every changed `.go` file's comment-stripped AST is compared against `main`: **113 proven comment-only, zero
suspect**, and exactly five declared exceptions — this doc, the `t.Fatalf` message, the `invalid_reason`
literal, and capability-author's paired version constants. Each of the three new gate mechanisms is
revert-proven: stub the block pass, restore the `\b`, or drop the `/*` handling, and precisely that
mechanism's self-test cases fail.

### 6.3 Scope honesty

The sweep is regex-scoped, not rule-scoped: `the old` (103 hits), `no longer` (255), `legacy` (135) and the
rest of §5's tail are still standing next to comments this fire repaired. That is the declared non-goal, not
an oversight — but a reader of this doc should not mistake "the gate is green" for "the tree carries no
change-narration."
