//go:build ignore

// lint-capability-kv-readers — Capability-KV single-read-path gate
// (capability-kv-single-read-path-design.md §3.5; docs/contracts/06-capability-kv.md
// §6.1).
//
// internal/capabilitykv exists to be the one place that knows Contract #6 §6.1's
// Capability-KV key routing (the class-aware `cap.<rest>` vs `cap.roles.<rest>`
// split) and doc merge — "so a second reader can read the same projection through
// the same key-routing without duplicating it" (internal/capabilitykv/doc.go).
// This gate mechanizes TWO independent ways a non-test Go file outside
// internal/capabilitykv can re-implement that path instead of calling into it,
// unless the file is on the named allowlist below:
//
//  1. BUCKET-ARGUMENT READ. A KVGet / KVGetMulti / KVGetMultiNoSnapshot /
//     KVListKeys / KVListKeysPrefix / KVListKeysFilter call whose own bucket
//     argument names the capability bucket — the qualified constant
//     bootstrap.CapabilityKVBucket or projection.AuthPlaneBucket (bare or
//     package-qualified), a "capability-kv" string literal, or a field/parameter
//     carrying one of the short names this tree's callers use for it (capBucket,
//     capabilityBucket).
//
//  2. KEY-PREFIX DERIVATION. A string literal "cap.", "cap.roles.",
//     "cap.identity.", or "cap.roles.identity." concatenated (or interpolated
//     into a format string) with an actor id/suffix — restating the routing
//     capabilitykv.CapabilityKeyFromActor / RolesKeyFromActor already do,
//     regardless of which bucket the derived key is later read from. This is
//     the more load-bearing of the two checks: a hand-rolled key handed to the
//     SANCTIONED helper (capabilitykv.ReadAndMerge) still restates the routing
//     and still reproduces the routing bug this gate exists to prevent, and
//     check 1 alone cannot see that — the bucket argument there is the
//     legitimate helper call.
//
// Both checks are deliberately narrow about which "cap." families they touch.
// cap.svc., cap.ephemeral., cap-read.*, and cap.role-by-operation. are disjoint
// key families capabilitykv does not own (Contract #6 §6.1 scopes it to the
// per-actor platform/roles doc alone) — a literal-prefix scan that did not
// enumerate the exact four owned prefixes would flag every one of them.
//
// WHY THE ARGUMENT, NOT THE WHOLE FILE. Checking "does this file mention the
// bucket name anywhere" over-flags: a file can legitimately pass
// bootstrap.CapabilityKVBucket to a constructor as plain wiring in one function
// while reading an unrelated bucket (Core KV, say) elsewhere in the same file —
// mentioning the capability bucket is not reading it. So check 1 resolves, per
// KVGet-family call, which bucket THAT call reads: its own argument. Matching by
// name/shape rather than by tracing an assignment also means the argument does
// not have to sit near the bucket's declaration — a struct field read by a
// method far from the constructor that set it is still caught, because the
// field's own name is the signal, not its proximity to anything else in the
// file.
//
// RESIDUAL (documented, not fixed — matches this file's neighbours' own
// pragmatic-scanner tradeoffs, e.g. lint-conventions.go's substrateConnectOptsAssign
// comment): neither check sees a capability read performed through a KV HANDLE
// opened once and reused — `capabilityKV, err := js.KeyValue(ctx,
// CapabilityKVBucket)` (internal/bootstrap/primordial.go) or `conn.OpenKV(ctx,
// bootstrap.CapabilityKVBucket)` (cmd/refractor/main.go) — because the actual
// Get/Put calls against that handle carry no bucket argument at all; nor does
// either check know about a THIRD alias for the bucket that has not yet been
// named here. Widen the const/field-name lists on evidence of a new caller's
// naming, not preemptively — an unenumerated shape is a gap to close when found,
// not a reason to fall back to file-wide co-occurrence (which reintroduces the
// false-positive class the argument-level design exists to avoid).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	// kvBucketMethodCall anchors the start of a bucket-taking read call. Every
	// method here takes the bucket as its second positional argument:
	// Func(ctx, bucket, ...) — KVListKeys/KVListKeysPrefix/KVListKeysFilter
	// included (internal/substrate/kv.go, kv_multi.go).
	kvBucketMethodCall = regexp.MustCompile(`\.(KVGet|KVGetMulti|KVGetMultiNoSnapshot|KVListKeys|KVListKeysPrefix|KVListKeysFilter)\(`)
	// capBucketArg matches a captured bucket argument that names the capability
	// bucket: the quoted literal, or an identifier/selector ending in one of the
	// shapes this tree's callers actually use for it. Deliberately an
	// exact-shape match, not a loose "contains cap ... bucket" scan — this
	// tree's OTHER NATS-KV buckets include ones that share that loose shape
	// without being the capability-kv bucket (capabilityauthor.
	// CapabilityProposalsBucket, the capability-AUTHORING review queue, is a
	// different bucket entirely). Widen this on evidence of a new caller's
	// naming, per the file doc comment's RESIDUAL note.
	capBucketArg = regexp.MustCompile(`(?i)^"capability-kv"$|\bcap(?:ability)?(?:kv)?bucket\b|\bauthplanebucket\b`)
	// capKeyPrefixLiteral matches one of the four capability-actor key prefixes
	// Contract #6 §6.1 assigns to internal/capabilitykv, used as the left operand
	// of a string concatenation with an actor suffix, or interpolated into a
	// format string via a %s/%v verb — the shape that restates the routing
	// rather than merely mentioning the bucket. Every other "cap." family
	// (cap.svc., cap.ephemeral., cap-read.*, cap.role-by-operation.) is
	// deliberately excluded: the alternation only admits an exact prefix,
	// captured in group 1 (without its quotes, for the finding message) and
	// immediately followed by either the closing quote plus a concatenation
	// operator, or a bare format verb inside the same literal. (Deliberately not
	// spelled out here as a literal code example: doing so would reproduce the
	// exact shape this check matches, self-flagging this file the moment it is
	// not on its own allowlist entry below.)
	capKeyPrefixLiteral = regexp.MustCompile(`"(cap\.(?:roles\.identity\.|identity\.|roles\.)?)(?:"\s*\+|%[sv])`)
)

// allowEntry is one deliberate exemption from the gate. Adding an entry is a
// deliberate decision — it must carry its own reason, stated here, not merely a
// path.
type allowEntry struct {
	prefix string
	reason string
}

// allowlist — the declared exemptions (capability-kv-single-read-path-design.md
// §3.5). Each entry is a path PREFIX (repo-relative) a violating file/call may
// sit under, scoped as narrowly as the exemption it actually covers rather than
// blanket over a whole subtree, and carrying its own reason.
var allowlist = []allowEntry{
	{
		prefix: "cmd/lattice/query/",
		reason: "operator inspection of a caller-supplied key — the Loupe-class inspector exception (query.go:65, capability-kv-single-read-path-design.md G12).",
	},
	{
		// Scoped to the one file that needs it, not internal/refractor/ at
		// large: no other file under internal/refractor performs a
		// capability-bucket read or a cap./cap.roles. key derivation.
		prefix: "internal/refractor/pipeline/evaluate.go",
		reason: "the PRODUCER side's key derivation: capabilityKeyForActor mirrors capabilitykv.CapabilityKeyFromActor's \"cap.\" + rest routing but lives here to avoid a circular import (capabilityenv imports pipeline for EnvelopeFn) — a documented deliberate duplicate. It derives a key; it performs no capability-bucket KVGet itself.",
	},
	{
		prefix: "scripts/",
		reason: "build-ignore operator tooling (seed + verify harnesses) that inspects raw projection state and never authorizes against it — the same standing as the _test.go exemption. A script that needs to know what an actor HOLDS belongs on pkgmgr.ReadHeldPermissions, not a hand-rolled key/bucket read.",
	},
}

// capabilitykvPkg is the one package this gate exists to funnel every other
// reader through — it IS the read path, so it is exempt from its own rule.
const capabilitykvPkg = "internal/capabilitykv/"

func main() {
	strict := os.Getenv("STRICT") == "1"

	files, err := trackedGoFiles()
	if err != nil {
		// A broken scan must never read as a clean pass — see trackedGoFiles's
		// doc comment. Exit 2 (distinct from the "issues found" exit 1) and
		// always non-zero, STRICT or not: a positively-worded all-clear over
		// zero scanned files is worse than no verdict at all.
		fmt.Fprintf(os.Stderr, "lint-capability-kv-readers: %v — refusing to report a false all-clear\n", err)
		os.Exit(2)
	}
	reportUntrackedGoFiles()

	var issues int
	for _, f := range files {
		issues += checkFile(f)
	}

	if issues == 0 {
		fmt.Println("lint-capability-kv-readers: 0 issues — no capability-bucket read call and no cap./cap.roles.-prefixed key derivation found outside internal/capabilitykv (or its declared exceptions)")
		return
	}
	fmt.Printf("lint-capability-kv-readers: %d issue(s)\n", issues)
	if strict {
		os.Exit(1)
	}
}

func checkFile(path string) int {
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return 0
	}
	if strings.HasPrefix(path, capabilitykvPkg) {
		return 0
	}
	if allowlisted(path) {
		return 0
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	issues := checkBucketArgReads(path, data)
	issues += checkKeyPrefixDerivation(path, data)
	return issues
}

// checkBucketArgReads implements check 1: a KVGet-family call whose own bucket
// argument names the capability bucket. The argument is extracted with a
// paren-depth-aware split (splitTopLevelArgs) rather than a single-line regex
// capture, so a call whose first argument is itself a call expression — an
// inline per-request context accessor, say — still resolves its bucket
// argument correctly instead of being silently missed. (Deliberately not shown
// as a literal call here: spelling one out would reproduce the exact call
// shape this check matches, self-flagging this file.)
func checkBucketArgReads(path string, data []byte) int {
	issues := 0
	for _, m := range kvBucketMethodCall.FindAllSubmatchIndex(data, -1) {
		openParen := m[1] // index right after "("
		method := string(data[m[2]:m[3]])
		args := splitTopLevelArgs(data[openParen:])
		if len(args) < 2 {
			continue
		}
		bucketArg := strings.TrimSpace(args[1])
		if !capBucketArg.MatchString(bucketArg) {
			continue
		}
		line := lineAt(data, openParen)
		fmt.Printf("%s:%d: %s reads the capability bucket (%s) directly — the Capability-KV authorization surface has one read path, internal/capabilitykv (Contract #6 §6.1's key routing + doc merge). Route this through internal/capabilitykv (e.g. capabilitykv.ReadAndMerge / ReadPlatformDoc) instead of reading %q here.\n",
			path, line, method, bucketArg, bucketArg)
		issues++
	}
	return issues
}

// checkKeyPrefixDerivation implements check 2: a "cap."-family literal Contract
// #6 §6.1 assigns to internal/capabilitykv, concatenated or interpolated with an
// actor suffix. Line-scoped: every call site in the corpus keeps the literal and
// its "+"/format verb on one line.
func checkKeyPrefixDerivation(path string, data []byte) int {
	issues := 0
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		m := capKeyPrefixLiteral.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		fmt.Printf("%s:%d: builds a capability actor key from the literal prefix %q — this restates Contract #6 §6.1's key routing (internal/capabilitykv.CapabilityKeyFromActor / RolesKeyFromActor own it), independent of which bucket the derived key is later read from. Route through internal/capabilitykv instead of re-deriving the prefix here.\n",
			path, i+1, m[1])
		issues++
	}
	return issues
}

// splitTopLevelArgs splits a call's argument list — the text starting right
// after its opening "(" — into top-level (depth-0-relative) comma-separated
// arguments, stopping at the "(" that closes it. It tracks (), [], {} nesting
// and skips the contents of "..." / `...` literals so a nested call
// (r.Context()) or a key literal containing a comma never miscounts a
// boundary. Returns nil if the call is never closed (a malformed or
// unrecognizably-formatted file — nothing to report from it, not a crash).
func splitTopLevelArgs(rest []byte) []string {
	depth := 1
	var args []string
	start := 0
	var inString byte // 0, '"', or '`'
	escaped := false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if inString != 0 {
			switch {
			case inString == '"' && escaped:
				escaped = false
			case inString == '"' && c == '\\':
				escaped = true
			case c == inString:
				inString = 0
			}
			continue
		}
		switch c {
		case '"', '`':
			inString = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				args = append(args, string(rest[start:i]))
				return args
			}
		case ',':
			if depth == 1 {
				args = append(args, string(rest[start:i]))
				start = i + 1
			}
		}
	}
	return nil
}

// lineAt returns the 1-based line number of byte offset off in data.
func lineAt(data []byte, off int) int {
	if off > len(data) {
		off = len(data)
	}
	return 1 + strings.Count(string(data[:off]), "\n")
}

// allowlisted reports whether path is a declared exception.
func allowlisted(path string) bool {
	for _, a := range allowlist {
		if strings.HasPrefix(path, a.prefix) {
			return true
		}
	}
	return false
}

// trackedGoFiles returns every git-tracked .go file, or an error if the scan
// itself cannot be trusted: `git ls-files` failing outright, or succeeding but
// naming zero files, both look identical to "every file passed" once folded
// into an issue count — this is the fail-open lint-conventions.go's own
// trackedGoFiles has (it returns nil on error, which then scans nothing and
// reports 0 issues). Concretely: an author who runs this gate from the wrong
// directory, or in a tree that is not a git repo, gets a positively-worded
// all-clear naming the exact invariant a real inline reader would violate. The
// caller treats a non-nil error as a hard failure, not an empty result.
func trackedGoFiles() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("git ls-files returned zero .go files — this looks like a broken invocation (wrong working directory, not a git repository, or a corrupt index), not a clean repo")
	}
	return files, nil
}

// reportUntrackedGoFiles names the .go files this run did NOT scan because git
// does not track them yet — mirrors lint-conventions.go's helper of the same
// name. The scan set comes from `git ls-files`, so a file not yet `git add`ed is
// invisible to this gate locally while CI (which lints a committed tree) would
// see it; this makes that gap visible instead of leaving it silent.
func reportUntrackedGoFiles() {
	out, err := exec.Command("git", "ls-files", "--others", "--exclude-standard", "*.go").Output()
	if err != nil {
		return
	}
	var skipped []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			skipped = append(skipped, l)
		}
	}
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "lint-capability-kv-readers: NOT SCANNED — %d untracked .go file(s); `git add` them for this gate to see what CI will:\n", len(skipped))
	for _, f := range skipped {
		fmt.Fprintf(os.Stderr, "  %s\n", f)
	}
}
