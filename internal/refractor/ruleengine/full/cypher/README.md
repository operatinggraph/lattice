# openCypher Parser

This directory holds the generated Go parser for the openCypher grammar.
`Cypher.g4` started as a copy from:

https://github.com/jtejido/go-opencypher

(Story 3.1, so Refractor could implement its own listener/visitor without
depending on the upstream repo as a Go module) but it is no longer a verbatim
copy: `oC_NodePattern` carries a Refractor extension, a trailing `'*'?` sigil
(`(l:location*)`, the reflexive-transitive label-expansion marker —
dynamic-type-taxonomy-design.md §14 Fire A). The sigil is confined to
`oC_NodePattern` and deliberately not added to `oC_NodeLabel`, which is also
reachable from `oC_PropertyOrLabelsExpression`, where a trailing `*` is the
multiplication operator.

The generated files are regenerated locally, not hand-edited. Use **`make
regen-cypher`**, which runs

`antlr -Dlanguage=Go -package cypher -o . Cypher.g4`

from this directory and then deletes the four side artifacts ANTLR also emits
(`Cypher.interp`, `Cypher.tokens`, `CypherLexer.interp`, `CypherLexer.tokens`),
none of which are committed. `go generate ./internal/refractor/ruleengine/full/cypher/...`
runs the `antlr` line only and leaves those four behind; they are gitignored,
so the tree stays clean either way, but the Makefile target is the
full recipe.

`TestGrammarMatchesGeneratedParser` pins `Cypher.g4`'s digest so that editing
the grammar without regenerating fails loudly. Nothing else would catch it:
the committed parser is internally self-consistent, so a stale one builds,
vets and tests green while the grammar file quietly becomes a statement of
intent rather than a description of the shipped parser.

The generator on record is ANTLR **4.13.2**; the `go.mod` runtime pin is
`github.com/antlr4-go/antlr/v4` **v4.13.1**. The two do not match and cannot:
**there is no v4.13.2 release of the Go runtime module** (`go list -m -versions`
offers v4.12.0, v4.13.0, v4.13.1), so the pin is not lagging and must not be
"caught up". What makes the mismatch safe is that the Go runtime performs no
tool-version handshake — `BaseRecognizer.checkVersion` is unexported and never
called from generated Go, and `ATNDeserializer.checkVersion` validates the
serialized-ATN format number, not the tool version — combined with the measured
fact that 4.13.2 regenerates *this* grammar byte-identically to the committed
4.13.1 output apart from the header comment. That byte-identity is a
per-grammar result, not a general guarantee about the two generator versions:
re-verify it if the generator moves again.

Generated files:

- `cypher_lexer.go`
- `cypher_parser.go`
- `cypher_listener.go`
- `cypher_base_listener.go`
