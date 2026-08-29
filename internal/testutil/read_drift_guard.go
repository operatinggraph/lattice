package testutil

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// ReadDriftGuard fails a test whose script read or walked Core KV outside what
// its operation declared — the enforcement half of the read record
// (processor.ScriptReadRecord). It is armed on every CapabilityPipeline, with no
// env var to switch it on, because the property worth having is "new drift
// cannot land", and an opt-in guard protects nothing.
//
// A RATCHET, not a clean-room rule. Contract #2 §2.5 wants every script read
// declared, and the `packages/` corpus is a long way from that today (CLAUDE.md
// names the class-(b) debt; read_drift_baseline.txt measures it). So each
// recorded read is admitted on one of two grounds:
//
//   - class (e), the sanctioned live read: the key's 3-segment vertex root is a
//     vertex THIS execution's own kv.Links walk DISCOVERED, so the read is a
//     follow-up on an enumeration rather than a key pulled from nowhere.
//     Attribution to a particular walk is deliberately not required — a walk
//     that was itself undeclared is flagged in its own right below, so a script
//     cannot launder an undeclared read by walking first.
//
//     "Discovered" means the far endpoint, never the hub: a walk's subject
//     filter pins the hub to one end of every link it returns, so a walked set
//     that included hubs would make this arm a blanket pass on every aspect of
//     every vertex the operation walks from — and `vtx.identity.<actor>
//     holdsRole out` is the standard confinement preamble, so that is most of
//     the corpus. processor.ScriptReadRecord.EnumeratedVertices excludes the
//     hub for exactly this reason;
//
//   - baselined: the key's normalized SHAPE is recorded in the checked-in
//     baseline as debt that already existed.
//
// Enumerations are admitted when declared in `contextHint.enumerations` or
// baselined, on the same footing.
//
// The guard reports SHAPES, never raw keys, so one finding is one fix rather
// than one per fixture id.
type ReadDriftGuard struct {
	// errorf reports one finding. Always t.Errorf in real wiring; the guard's
	// own tests substitute a collector, since a test proving the guard fires
	// cannot itself fail.
	errorf func(format string, args ...any)
	// reads/walks are the baselined shapes per operationType, taken from the
	// embedded table. Held per guard rather than read from package state on
	// every execution so the guard's own tests can drive a known table.
	reads map[string]map[string]struct{}
	walks map[string]map[string]struct{}

	mu sync.Mutex
	// closed goes true when the test that owns t finishes. A pipeline can
	// outlive its test — a background consumer draining a last delivery — and
	// t.Errorf after the test has completed panics the run, which would turn a
	// drift finding into an unreadable crash in an unrelated place.
	closed bool
	// seen dedupes by (operationType, shape): a fixture that submits the same
	// operation 300 times must produce one finding, not 300.
	seen map[string]struct{}
}

// NewReadDriftGuard returns a guard bound to t. Registers a cleanup that stops
// it reporting once t completes.
func NewReadDriftGuard(t *testing.T) *ReadDriftGuard {
	t.Helper()
	reads, walks, err := baselineTables()
	if err != nil {
		// Fatal rather than "carry on with an empty baseline": an unreadable
		// baseline is not a licence to guard nothing, and it is not a licence to
		// redden the corpus with a misleading finding either.
		t.Fatalf("read-drift guard: cannot read its baseline: %v", err)
	}
	g := &ReadDriftGuard{errorf: t.Errorf, reads: reads, walks: walks, seen: map[string]struct{}{}}
	t.Cleanup(func() {
		g.mu.Lock()
		g.closed = true
		g.mu.Unlock()
	})
	return g
}

// ObserveScriptReads implements processor.ScriptReadObserver.
func (g *ReadDriftGuard) ObserveScriptReads(_ context.Context, env *processor.OperationEnvelope, record processor.ScriptReadRecord) {
	if g == nil || env == nil {
		return
	}
	op := env.OperationType
	walked := make(map[string]struct{}, len(record.EnumeratedVertices))
	for _, v := range record.EnumeratedVertices {
		walked[v] = struct{}{}
	}
	reads := g.reads[op]
	for _, key := range record.LiveReads {
		// An empty root is "this key has no vertex root" (a link key), never a
		// match: testing it against the walked set would admit every link-key
		// read the moment one malformed endpoint put "" in that set.
		if root := VertexRoot(key); root != "" {
			if _, ok := walked[root]; ok {
				continue
			}
		}
		shape := NormalizeReadKey(key)
		if _, ok := reads[shape]; ok {
			continue
		}
		g.fail("read:"+op+"|"+shape,
			"read-drift: operation %q read %s live, and nothing declared it.\n"+
				"    fix: add the key to that dispatcher's contextHint — `reads` if the operation depends on it, `optionalReads` if absence is expected.\n"+
				"    if the read is live BY DESIGN (Contract #2 §2.5 class (c) config read, or an (e) follow-up on an enumeration), annotate the kv.Read call `# read-posture:` and add the row `read\t%s\t%s` to internal/testutil/read_drift_baseline.txt, under a `#` comment stating why.\n"+
				"    add that row BY HAND. Regenerating the baseline would re-record every read the run made, including this one, and silence the finding without anyone reading it.",
			op, shape, op, shape)
	}
	declared := make(map[string]struct{})
	if h := env.ContextHint; h != nil {
		for _, e := range h.Enumerations {
			declared[NormalizeEnumeration(e.Hub, e.Relation, e.Direction)] = struct{}{}
		}
	}
	walks := g.walks[op]
	for _, e := range record.Enumerations {
		shape := NormalizeEnumeration(e.Hub, e.Relation, e.Direction)
		if _, ok := declared[shape]; ok {
			continue
		}
		if _, ok := walks[shape]; ok {
			continue
		}
		g.fail("walk:"+op+"|"+shape,
			"read-drift: operation %q walked the enumeration %s, and nothing declared it.\n"+
				"    fix: add {hub, relation, direction} to that dispatcher's contextHint.enumerations.\n"+
				"    if the walk cannot be declared (a link-discovered hub — one only a prior read resolves, so no dispatcher can name it up front), add the row `walk\t%s\t%s` to internal/testutil/read_drift_baseline.txt, under a `#` comment stating why.\n"+
				"    add that row BY HAND. Regenerating the baseline would re-record every walk the run made, including this one, and silence the finding without anyone reading it.",
			op, shape, op, shape)
	}
}

// fail reports one finding once, and never after the owning test has finished.
//
// g.mu is held ACROSS the errorf call, not just across the checks. Releasing it
// first reopens the window the closed flag exists to shut: a background consumer
// could pass the check, unlock, lose the race with the t.Cleanup that sets
// closed, and then panic the whole binary from t.Errorf on a finished test.
// Holding the lock makes "not closed" true for the duration of the report.
// t.Errorf is internally synchronized and takes no lock this guard holds, so
// there is no ordering hazard.
func (g *ReadDriftGuard) fail(dedupe, format string, args ...any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	if _, dup := g.seen[dedupe]; dup {
		return
	}
	g.seen[dedupe] = struct{}{}
	g.errorf(format, args...)
}

// VertexRoot is the 3-segment vertex key an aspect or vertex key belongs to —
// what an enumeration surfaces as a far endpoint. A link key has no vertex
// root, so it never matches a walked vertex.
//
// Exported so the baseline generator applies the guard's own rule rather than a
// copy: the generator is `//go:build ignore` and never compiles under
// `go build ./...`, so a duplicated definition could drift out of agreement
// with the guard for as long as nobody regenerated.
func VertexRoot(key string) string {
	if !strings.HasPrefix(key, "vtx.") {
		return ""
	}
	p := strings.Split(key, ".")
	if len(p) < 3 {
		return ""
	}
	return strings.Join(p[:3], ".")
}

// aspectTimestamp matches the compact ISO-8601 basic-format instant that
// schedule-slot aspects embed in their localName (`slot20260708t090000z`).
// Contract #1 §1.1 confines a localName to `[a-z][a-zA-Z0-9]*`, so the extended
// form with dashes and colons cannot occur in a key and is deliberately not
// matched. The 8+6 digit run is tight on purpose: a looser pattern would eat
// `seat1` and turn a baseline entry into a wildcard.
var aspectTimestamp = regexp.MustCompile(`[0-9]{8}[tT][0-9]{6}[zZ]?$`)

// NormalizeReadKey reduces a Core KV key to the SHAPE a baseline entry records:
// the runtime-varying parts become placeholders, everything an author chose
// stays literal.
//
// What is normalized:
//   - the id segment of a vertex key and both id segments of a link key, when
//     the segment is a real NanoID (substrate/keys' alphabet and length, not a
//     loose regex — a 20-character segment over some other alphabet is a fixture
//     name and stays literal);
//   - inside an aspect localName only: a trailing instant (`slot<t>`), and a
//     trailing NanoID for the explicitly listed composite prefixes
//     (`activeVisitSeriesWith<id>` — see compositeLocalNamePrefixes for why the
//     list is enumerated rather than inferred).
//
// What is NEVER normalized: type segments, link relations, and the static
// prefix of a localName. Collapsing any of those would widen one baseline entry
// into a wildcard over a whole keyspace, which is how a ratchet quietly becomes
// a rubber stamp. A key whose prefix is neither `vtx` nor `lnk`, or whose
// segment count does not fit the contract shape, is returned untouched rather
// than guessed at.
func NormalizeReadKey(key string) string {
	p := strings.Split(key, ".")
	switch {
	case p[0] == "vtx" && len(p) >= 3:
		p[2] = normalizeID(p[2])
		for i := 3; i < len(p); i++ {
			p[i] = normalizeLocalName(p[i])
		}
	case p[0] == "lnk" && len(p) == 6:
		p[2] = normalizeID(p[2])
		p[5] = normalizeID(p[5])
	default:
		return key
	}
	return strings.Join(p, ".")
}

// NormalizeEnumeration is NormalizeReadKey for a kv.Links walk: the hub is a
// vertex key, the relation and direction are the author's own words and stay
// verbatim.
func NormalizeEnumeration(hub, relation, direction string) string {
	return NormalizeReadKey(hub) + " " + relation + " " + direction
}

// normalizeID replaces a whole segment that is a genuine NanoID.
func normalizeID(seg string) string {
	if keys.IsValidNanoID(seg) {
		return "<id>"
	}
	return seg
}

// compositeLocalNamePrefixes are the aspect localNames built as a fixed word
// plus a NanoID — the whole list, named one by one on purpose.
//
// The tempting rule is "if the last 20 characters parse as a NanoID, collapse
// them", and it is wrong: it makes collapsing an alphabet lottery over ordinary
// names. `emergencyContactPhone` and `erasureRequestedAtNow` are 21 characters
// whose last 20 happen to avoid l/I/O/0, so both would reduce to `e<id>` — one
// baseline row then admitting every 21-character aspect on that vertex type
// beginning with `e`. An explicit list cannot widen: a shape absent from it is
// simply never collapsed, which fails toward a guard finding rather than
// toward silence. Add a prefix here only with the composite key in front of you.
var compositeLocalNamePrefixes = []string{
	"activeVisitSeriesWith", // wellness-domain: one aspect per subject provider
}

// normalizeLocalName replaces runtime data embedded in an aspect localName,
// keeping the static prefix that names what the aspect IS.
func normalizeLocalName(seg string) string {
	if loc := aspectTimestamp.FindStringIndex(seg); loc != nil && loc[0] > 0 {
		return seg[:loc[0]] + "<t>"
	}
	for _, prefix := range compositeLocalNamePrefixes {
		if rest, ok := strings.CutPrefix(seg, prefix); ok && keys.IsValidNanoID(rest) {
			return prefix + "<id>"
		}
	}
	return seg
}

// multiScriptReadObserver fans one execution's record out to several observers —
// the drift guard is always armed, the census only when its env var names a file.
type multiScriptReadObserver []processor.ScriptReadObserver

// ObserveScriptReads implements processor.ScriptReadObserver.
func (m multiScriptReadObserver) ObserveScriptReads(ctx context.Context, env *processor.OperationEnvelope, record processor.ScriptReadRecord) {
	for _, o := range m {
		o.ObserveScriptReads(ctx, env, record)
	}
}
