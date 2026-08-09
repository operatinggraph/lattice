// Package cypher holds the generated openCypher parser. See README.md for
// provenance and the grammar's Refractor-specific node-pattern `*` sigil.
package cypher

//go:generate antlr -Dlanguage=Go -package cypher -o . Cypher.g4
