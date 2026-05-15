# Vendored openCypher Parser

This directory contains the generated Go parser files for the openCypher
grammar copied from:

https://github.com/jtejido/go-opencypher

The grammar was copied for Story 3.1 so Refractor can implement its own
listener/visitor without depending on the upstream repo as a Go module.

Generation note:

`Code generated from Cypher.g4 by ANTLR 4.13.1. DO NOT EDIT.`

Runtime dependency:

`github.com/antlr4-go/antlr/v4 v4.13.1`

Copied files:

- `Cypher.g4`
- `cypher_lexer.go`
- `cypher_parser.go`
- `cypher_listener.go`
- `cypher_base_listener.go`
