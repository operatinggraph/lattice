package bridge

// DeriveReplyRequestID exposes the internal deterministic result-op requestId
// derivation to the external bridge_test package, so the FR58 harness can assert
// the bridge posts a replyOp whose requestId is exactly the deterministic
// function of the opaque instanceKey (the pinned Contract #10 §10.3 invariant).
// Test-only seam (export_test.go is compiled only under `go test`).
func DeriveReplyRequestID(instanceKey string) string { return deriveReplyRequestID(instanceKey) }

// DeriveDispatchRequestID exposes the internal deterministic dispatch-op requestId
// derivation to the external bridge_test package, so the async harness can assert
// the bridge posts the pending-marker op under the deterministic id (a redelivered
// Pending collapses on the Contract #4 tracker). Test-only seam.
func DeriveDispatchRequestID(instanceKey string) string { return deriveDispatchRequestID(instanceKey) }
