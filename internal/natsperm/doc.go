// Package natsperm holds the Lattice NATS transport-authorization permission
// matrix (the NATS account-level write restriction, Path A) and its
// conformance proof.
//
// Matrix + platform-bucket-registry-derived owner-allows/denies live here
// (matrix.go); deploy/gen-dev-nkeys is a thin renderer that mints per-
// component NKey seeds and writes deploy/nats-server.conf from Matrix. The
// package's tests load that exact production config + seeds into an embedded
// JetStream server and assert the matrix's intended invariant — only the
// processor may write Core KV and only refractor may write capability-kv /
// the lens targets — end-to-end, before enforcement is ever wired into the
// live stack (natsperm-matrix-hygiene-design.md).
//
// That invariant binds a component's ordinary client paths, and nothing more:
// a message the SERVER publishes on a component's behalf — a request's reply,
// a PubAck, a stream's RePublish — carries no permissions, so every row here
// can land bytes on a subject the matrix denies it. Deny's doc comment has
// the routes and replysubject_test.go pins them. Read a write-isolation
// assertion in this package accordingly: it proves the door is shut, not that
// there is no way around the building.
package natsperm
