package control

// TargetIDFromSubject exposes the unexported targetIDFromSubject for the
// control_test package. The disable/enable/revoke endpoints register on the
// wildcard subject lattice.ctrl.weaver.*.<op>, so NATS subject routing can
// only ever deliver a conforming 5-token subject to dispatchEndpoint — the
// parser's deviation branches are an unreachable-via-NATS defensive boundary.
// Exposing the helper lets those branches be table-tested directly, guarding
// against a future direct caller or a refactor that loosens the wildcard.
var TargetIDFromSubject = targetIDFromSubject

// RegisteredExactOps and RegisteredTargetOps expose the op tokens
// StartNATSListener registers endpoints for, split by SUBJECT SHAPE and read
// off the SAME exactOps/targetOps vars the registration loops range over, so a
// test walks each with the subject builder that shape uses. Both halves are
// derived rather than listed: an op registered outside those vars is served
// without appearing here, and an op invisible to a lockstep test is an op that
// lockstep does not cover.
func RegisteredExactOps() []string { return append([]string(nil), exactOps...) }

func RegisteredTargetOps() []string { return append([]string(nil), targetOps...) }

// ExactSubject builds the exact-subject shape StartNATSListener registers an
// exactOps endpoint on — the same subjectPrefix+"."+op the registration uses,
// so a test walking RegisteredExactOps addresses each one the way NATS routes
// it rather than restating one op's subject for all of them.
func ExactSubject(op string) string { return subjectPrefix + "." + op }
