//go:build ignore

// lint-web — the web-asset half of the G2 derived-key ban
// (client-ceremony-op-descriptors-design.md §6).
//
// WHY THIS IS ITS OWN SCRIPT. lint-conventions discovers files via
// `git ls-files "*.go"` and hard-filters anything that is not .go, and its
// annotation parser is Go-comment shaped. The browser is where the derived-key
// problem was *worst* — two independent hand-ports of a 128-bit PCG expansion,
// in a language with no shared code path to the Go original whatsoever — so the
// gate has to reach .js/.mjs, and reaching it as a filter change to
// lint-conventions would have meant reworking that script's whole file model.
//
// WHAT IT BANS. A hash-to-NanoID derivation in a web asset: the canonical
// Contract #1 NanoID alphabet appearing in a file that also computes a SHA-256
// digest. That conjunction IS the re-port — it is what both deleted
// `sha256NanoID` ports looked like, and it is what any future one must look
// like, because a Contract #1 key needs the alphabet and a content-addressed
// derivation needs the digest.
//
// WHY THE CONJUNCTION AND NOT THE FUNCTION NAME. A name scan (`sha256NanoID`)
// is defeated by a rename, which is not a hypothetical for code someone is
// re-adding after it was deleted. And an alphabet-only scan is worse in the
// other direction: cmd/facet/web/boot.mjs uses the same alphabet to mint a
// RANDOM device id, which is legitimate and has nothing to do with deriving a
// key — flagging it would train authors to annotate noise, which is how a
// default-deny gate stops being read.
//
// THE ESCAPE. `// derived-key: <reason>` on the flagged line or the line above
// it, exactly as the Go half spells it. It is not an amnesty: it says the
// derivation produces something that is NOT a declared read (an object id, a
// Contract #4 requestId), which is a claim a reviewer can check.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// nanoidAlphabet is the canonical Contract #1 alphabet (internal/substrate/
// keys/nanoid.go). Matched as a literal rather than a pattern: a partial or
// reordered alphabet does not produce Contract #1 ids, so it is not the thing
// being banned.
const nanoidAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

var (
	// sha256Digest — the WebCrypto call every browser-side derivation must
	// make. `crypto.subtle.digest("SHA-256", ...)` is the only way to compute
	// one in a browser without shipping a hash implementation, and a shipped
	// hash implementation would carry the alphabet too.
	sha256Digest = regexp.MustCompile(`subtle\.digest\(\s*["']SHA-256["']`)
	// derivedKeyShape mirrors the Go half's annotation, and additionally
	// accepts the /* */ and JSDoc forms, which are idiomatic in these files and
	// which a `//`-only parser would reject as undeclared — fail-closed, but
	// fail-closed on a correct declaration just teaches authors the gate is
	// broken.
	derivedKeyShape = regexp.MustCompile(`(?://|/\*+|^\s*\*)\s*derived-key:(.*?)(?:\*/)?$`)
	// derivedKeyConstruction — a Contract #1 key literal concatenated with a
	// call whose name says it hashes. This is the second detector, and it is
	// what makes the gate survive the alphabet const being annotated once: the
	// deleted ports all read `"vtx.identityindex." + await sha256NanoID(...)`,
	// and a re-port that reuses an existing NANOID_ALPHABET still has to write
	// that line somewhere.
	derivedKeyConstruction = regexp.MustCompile(`"(?:vtx|lnk)\.[a-zA-Z]*\.?"\s*\+\s*(?:await\s+)?[A-Za-z_$][A-Za-z0-9_$]*(?:NanoID|Nanoid|nanoid|Hash|hash|Digest|digest|Sha|sha|SHA)[A-Za-z0-9_$]*\(`)
	// alphabetBinding captures the identifier a file binds the alphabet to, so
	// every later USE of that name is flagged as well as the declaration.
	alphabetBinding = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*["']` + nanoidAlphabet + `["']`)
)

// usesAny reports whether line references any of the given identifiers as a
// whole word.
func usesAny(line string, names []string) bool {
	for _, n := range names {
		for _, idx := range indexAll(line, n) {
			beforeOK := idx == 0 || !isIdentByte(line[idx-1])
			end := idx + len(n)
			afterOK := end >= len(line) || !isIdentByte(line[end])
			if beforeOK && afterOK {
				return true
			}
		}
	}
	return false
}

func indexAll(hay, needle string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(hay[i:], needle)
		if j < 0 {
			return out
		}
		out = append(out, i+j)
		i += j + len(needle)
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '$' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// webExtensions are the shipped web assets. Test assets (.test.mjs) are IN
// scope: a test is exactly where a deleted re-port gets reintroduced, and the
// gate's clean-tree claim would not survive exempting them.
//
// .html is here because every app ships a login.html with a real inline
// <script> (the six app servers go:embed their web/ directory), and the auth
// flow is exactly where an identity-key derivation would be written. A gate
// that stopped at .js would be defeated by moving the code one file sideways.
var webExtensions = map[string]bool{".js": true, ".mjs": true, ".html": true}

type finding struct {
	file string
	line int
	msg  string
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
	}

	if failures := selfTest(); len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, "lint-web self-test: "+f)
		}
		fmt.Fprintf(os.Stderr, "lint-web: %d self-test failure(s) — the gate does not behave as documented\n", len(failures))
		os.Exit(2)
	}

	var findings []finding
	for _, f := range trackedWebFiles() {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		findings = append(findings, scanSource(f, string(data))...)
	}

	for _, fd := range findings {
		fmt.Printf("%s:%d: %s\n", fd.file, fd.line, fd.msg)
	}
	if len(findings) == 0 {
		fmt.Println("lint-web: 0 issues")
		return
	}
	fmt.Printf("lint-web: %d issue(s)\n", len(findings))
	if strict {
		os.Exit(1)
	}
}

func trackedWebFiles() []string {
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" && webExtensions[filepath.Ext(l)] {
			files = append(files, l)
		}
	}
	return files
}

// scanSource flags each line carrying the NanoID alphabet in a file that also
// digests. The check is per-LINE rather than per-file so that one annotated
// derivation does not silently amnesty a second one added later to the same
// file — file granularity would make the first annotation a permanent hole in
// exactly the file most likely to grow another derivation.
func scanSource(path, src string) []finding {
	lines := strings.Split(src, "\n")
	// Detector 1 — the alphabet and a digest in one file: a hash-to-NanoID
	// port. Flagged on the alphabet line, which is where the annotation goes.
	alphabetPort := strings.Contains(src, nanoidAlphabet) && sha256Digest.MatchString(src)

	// The names bound to the alphabet literal. A re-port does not have to
	// re-declare the alphabet — the whole point of the const being there is
	// that it can be REUSED — so following the identifier is what keeps the
	// annotated const from turning the rest of the file into a free pass.
	var alphabetNames []string
	if alphabetPort {
		for _, line := range lines {
			if m := alphabetBinding.FindStringSubmatch(line); m != nil {
				alphabetNames = append(alphabetNames, m[1])
			}
		}
	}

	var out []finding
	for i, line := range lines {
		if isCommentLine(line) {
			continue
		}
		var msg string
		switch {
		case alphabetPort && (strings.Contains(line, nanoidAlphabet) || usesAny(line, alphabetNames)):
			msg = "derived-key: hash-to-NanoID derivation in a web asset — this file both carries the Contract #1 NanoID alphabet and computes a SHA-256 digest, which is a client-side port of substrate.SHA256NanoID."
		case derivedKeyConstruction.MatchString(line):
			// Independent of detector 1 on purpose: once a file's alphabet
			// constant carries an annotation, detector 1 goes quiet for that
			// file forever, and a second derivation added later would ride in
			// free. This one fires per key-construction site.
			msg = "derived-key: a Contract #1 key built from a hash call in a web asset — deriving the key client-side is what class (g) replaced."
		default:
			continue
		}
		if declaredAt(lines, i) {
			continue
		}
		out = append(out, finding{file: path, line: i + 1, msg: msg + " A key derived this way from an operation payload is a class-(g) declared read the owning DDL's `derive_reads` computes (Contract #2 §2.5); a browser port is a second implementation of the package's normalization that nothing keeps in agreement. If this derives something that is NOT a declared read (an object id, a requestId), declare `// derived-key: <what it derives and why the package cannot>`"})
	}
	return out
}

// isCommentLine reports whether a line is wholly a comment. Without it the
// alphabet quoted inside a doc comment is flagged, which is the annotate-noise
// failure this gate's own design argues against.
func isCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// declaredAt reports whether line i carries a `// derived-key:` annotation with
// a stated reason, either inline or on the immediately preceding comment lines.
// A bare `// derived-key:` with no reason does not count — the reason is the
// whole point, and an empty one would make the escape a rubber stamp.
func declaredAt(lines []string, i int) bool {
	if reasonGiven(lines[i]) {
		return true
	}
	for j := i - 1; j >= 0; j-- {
		// A blank line or any code line ends the run, so an annotation never
		// reaches past the statement it was written for. Every comment form is
		// walked (`//`, `/* */`, a JSDoc `*` continuation) so that a correct
		// declaration written in the idiom these files already use is honored
		// rather than read as undeclared.
		if !isCommentLine(lines[j]) {
			return false
		}
		if reasonGiven(lines[j]) {
			return true
		}
	}
	return false
}

func reasonGiven(line string) bool {
	m := derivedKeyShape.FindStringSubmatch(line)
	return m != nil && strings.TrimSpace(m[1]) != ""
}

// selfTest pins the gate's documented behaviour, in the lint-conventions idiom:
// every case names the finding it expects, so no case can pass by tripping a
// different rule than the one it exists to pin.
func selfTest() []string {
	const alphabet = nanoidAlphabet
	const digest = "\tconst d = await crypto.subtle.digest(\"SHA-256\", enc.encode(s));\n"

	cases := []struct {
		name string
		src  string
		want bool // true = expect a finding
	}{
		{"alphabet plus digest is denied",
			digest + "\tconst A = \"" + alphabet + "\";\n", true},
		{"the alphabet alone passes (random id minting)",
			"\tconst A = \"" + alphabet + "\";\n\tcrypto.getRandomValues(buf);\n", false},
		{"a digest alone passes (hashing a secret)",
			digest, false},
		{"an inline declaration passes",
			digest + "\tconst A = \"" + alphabet + "\"; // derived-key: requestId, not a read\n", false},
		{"a preceding-comment declaration passes",
			digest + "\t// derived-key: requestId derivation, mirrors substrate.DeriveNanoID\n" +
				"\tconst A = \"" + alphabet + "\";\n", false},
		{"a reasonless declaration is denied",
			digest + "\t// derived-key:\n\tconst A = \"" + alphabet + "\";\n", true},
		{"a declaration on an earlier statement does not carry over",
			digest + "\t// derived-key: requestId, not a read\n" +
				"\tconst A = \"" + alphabet + "\";\n" +
				"\tconst B = \"" + alphabet + "\";\n", true},
		{"a block-comment declaration passes",
			digest + "\t/* derived-key: requestId, not a read */\n" +
				"\tconst A = \"" + alphabet + "\";\n", false},
		{"a JSDoc declaration passes",
			digest + "\t/**\n\t * derived-key: requestId, not a read\n\t */\n" +
				"\tconst A = \"" + alphabet + "\";\n", false},
		{"the alphabet quoted inside a comment is not flagged",
			digest + "\t// the canonical alphabet is \"" + alphabet + "\"\n", false},

		// Detector 2 — key construction. These are the shape the deleted ports
		// had, and the reason the gate survives an annotated alphabet const.
		{"a key built from a hash call is denied, with no alphabet in the file",
			"\tkeys.push(\"vtx.identityindex.\" + await sha256NanoID(\"email:\" + e));\n", true},
		{"a renamed hash function does not evade detector 2",
			"\tkeys.push(\"vtx.identityindex.\" + await contactDigest(\"email:\" + e));\n", true},
		{"reusing an ANNOTATED alphabet const does not buy a free derivation",
			digest + "\t// derived-key: requestId minting, not a read\n" +
				"\tconst A = \"" + alphabet + "\";\n" +
				"\tconst k = \"vtx.identityindex.\" + await sha256NanoID(\"email:\" + e);\n", true},
		{"an annotated key construction passes",
			"\t// derived-key: object id, content-addressed\n" +
				"\tconst k = \"vtx.object.\" + await objectHash(bytes);\n", false},
		{"an ordinary key concatenation is not flagged",
			"\tconst k = \"lnk.identity.\" + applicantId + \".appliedToUnit.unit.\" + unitId;\n", false},
	}

	var failures []string
	for _, tc := range cases {
		got := len(scanSource("cmd/self-test/web/app.js", tc.src)) > 0
		if got != tc.want {
			failures = append(failures, fmt.Sprintf("%s: got finding=%v, want %v", tc.name, got, tc.want))
		}
	}
	return failures
}
