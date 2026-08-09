package cypher

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// grammarDigest is the SHA-256 of Cypher.g4 as of the committed generated
// parser. Regenerating with `make regen-cypher` is what makes the two agree
// again; this constant records which grammar the committed parser was built
// from.
const grammarDigest = "6701f5e3170d19eed565d46e3d84e4cbdf1b5b717195d4dae97a31f3ea34d8cb"

// TestGrammarMatchesGeneratedParser fails when Cypher.g4 is edited without
// regenerating the parser beside it.
//
// Nothing else catches that drift. The committed parser is internally
// self-consistent, so a stale one builds, vets and passes the whole suite
// while the grammar file describes a parser that is not the one shipping —
// and the regeneration step needs Java and the `antlr` CLI, which CI does not
// have, so it cannot be a CI gate. Comparing the grammar's digest needs
// neither, and turns a silent divergence into a named failure.
func TestGrammarMatchesGeneratedParser(t *testing.T) {
	src, err := os.ReadFile("Cypher.g4")
	if err != nil {
		t.Fatalf("read Cypher.g4: %v", err)
	}

	sum := sha256.Sum256(src)
	got := hex.EncodeToString(sum[:])
	if got != grammarDigest {
		t.Fatalf("Cypher.g4 does not match the committed generated parser.\n"+
			"  grammar digest: %s\n"+
			"  parser built from: %s\n"+
			"Run `make regen-cypher`, then set grammarDigest in this file to the new digest.",
			got, grammarDigest)
	}
}
