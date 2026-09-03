//go:build ignore

// lint-cap-read-producers — closes the D1 read-grant producer set at authoring
// time (personal-lens-derivation-licence-design.md §4.3b).
//
// Refractor's D1 read gate answers by a WILDCARD listing over `cap-read.*`
// (internal/refractor/capabilityread: "package names are not enumerable
// statically"), so the producer set is OPEN BY CONSTRUCTION. Any lens that
// writes a key in that namespace is read as a live grant, whether or not the
// platform has ever heard of it — which makes "every cap-read producer
// announces on the change edge" a standing claim rather than a census result.
// A consumer that NARROWS on the strength of that claim needs it to be a
// property, not a hope.
//
// # The rule
//
// ONE predicate decides, and it is the runtime resolver itself:
// projection.CapReadWriterRefusal, called on a lens.Rule this gate builds from
// the AST-resolved declaration. Importing the package rather than restating its
// conjuncts is the whole design: a restatement is a second implementation of a
// rule that has to stay identical, and the ways it drifts are invisible — a
// conjunct the copy never reads (realnessFilter, which ParseOutputDescriptor
// REQUIRES whenever entryKeyColumn is set) passes a lens here that the
// installer refuses at boot. The shipped precedent for a script importing repo
// packages is lint-package-standard.go, lint-manifest-entity-type.go and
// lint-gap-column-declaration.go.
//
// A second check has no runtime analogue at declaration level and lives only
// here: a lens with NO §6.13 output descriptor renders its key by joining
// RETURN column values, so it can mint `cap-read.<domain>.<actor>.<anchor>`
// while declaring nothing at all. A descriptor-less lens on the auth-plane
// bucket whose cypher mentions `cap-read` is refused. Its runtime counterpart
// is the adapter's own namespace guard (NatsKVAdapter.refuseUnsanctionedGrantKey),
// which refuses on the RENDERED key — the only place that key exists.
//
// # What each arm actually covers
//
// Be honest about this, because a gate's value is exactly its reach:
//
//   - the `internal/bootstrap` arm inspects the kernel's base `capabilityRead`
//     producer, which is a real shipped lens;
//   - the `packages/**` arm is PREVENTIVE ONLY. The three shipped generated
//     producers (`edgeManifest{,Staff,Provider}ReadGrants`) are composed at
//     install time by internal/pkgmgr/anchorwalk.go from declared
//     ReadGrantDomains and are not literals in any package's source, so this
//     gate never sees them. They are closed elsewhere, by three mechanisms
//     that do see them: validateGrantDomainName (anchorwalk.go) bounds the
//     domain segment their key pattern is built from, the corpus test
//     TestCapReadWriterRefusal_ShippedProducersStillInstall runs the shared
//     predicate over every generated producer the registry composes, and the
//     adapter's namespace guard refuses any unlicensed write at runtime.
//
// So a clean run over `packages/**` proves that no author has hand-declared a
// bad producer, and nothing more. That is worth having — it is the shape the
// design's §4.3b exploit takes — but it is not a census of the shipped
// producers, and the self-vectors below are what keep the rule honest.
//
// Runs over every package under packages/ plus the kernel's own lens
// definitions in internal/bootstrap. STRICT=1 exits non-zero on any issue —
// which is the default posture, since the tree ships no violating lens and a
// warn over a clean corpus is exactly the fingers-crossed state the gate ends.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/capabilityread"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// capReadPrefix is the literal internal/refractor/capabilityread builds its
// reader filter from, taken from that package rather than respelled — the two
// enforcement points must be talking about the same namespace or the closure
// argument is vacuous.
const capReadPrefix = capabilityread.KeyPrefix

// authPlaneBuckets are the target-bucket spellings that classify a lens as
// auth-plane. projection.AuthPlaneBucket ("capability-kv") is the provisioned
// bucket; "capability" and "" are the kernel's own LensDefinition spellings,
// which internal/bootstrap's makeLensSpecBody maps onto it. BOTH arms matter:
// an ABSENT TargetBucket defaults to the capability bucket there, so reading
// only the explicit spelling would classify a kernel producer as a business
// lens and skip the auth-plane conjunct entirely.
var authPlaneBuckets = map[string]bool{
	projection.AuthPlaneBucket: true,
	"capability":               true,
	"":                         true,
}

// provisionedBucket maps a kernel LensDefinition's TargetBucket spelling onto
// the bucket Refractor actually activates the lens against, exactly as
// internal/bootstrap's makeLensSpecBody does — including its default for an
// ABSENT bucket. A gate carrying the short name into the shared predicate would
// have every kernel lens answer non-auth-plane, which is the opposite of what
// they are.
//
// Only meaningful for a NATS-KV target: a postgres lens's bucket field is
// empty because it HAS no bucket, and reading that emptiness as the kernel's
// default would classify every Path-A grant-table lens as a capability-bucket
// writer. targetsNatsKV is what keeps the two absences apart.
func (l capReadLens) provisionedBucket() string {
	if l.bucket == "capability" || l.bucket == "" {
		return bootstrap.CapabilityKVBucket
	}
	return l.bucket
}

// targetsNatsKV reports whether this lens writes NATS KV keys at all.
//
// The whole gate is about one thing: a KEY in the D1 cap-read namespace, in the
// capability bucket, that capabilityread's wildcard listing will read as a live
// grant. Only a nats-kv lens can write one. A postgres lens projects table rows
// (the Path-A actor_read_grants producers are exactly this shape, and their
// cypher names cap-read all over its comments), and a nats-subject Personal
// lens publishes to a subject; neither can mint a key, so neither is this
// gate's business. An absent adapter is nats-kv, which is what pkgmgr's own
// switch defaults it to.
func (l capReadLens) targetsNatsKV() bool {
	return l.adapter == "" || l.adapter == "nats-kv"
}

// capReadLens is one declared lens, reduced to the fields the shared predicate
// and the descriptor-less check decide on.
//
// Every "resolved" flag exists because ABSENT and UNREADABLE are different
// answers and only one of them is safe: an absent EntryKeyColumn is a decidable
// fact, while one this gate could not parse is a lens it cannot vouch for.
type capReadLens struct {
	name string
	// bucket is the declared target bucket; bucketResolved is false when it was
	// present but not statically readable. An unresolvable bucket is passed to
	// the predicate as "" — which the predicate reads as not-auth-plane and
	// therefore REFUSES for a namespace-claiming lens, the default-deny
	// direction.
	bucket         string
	bucketResolved bool
	adapter        string
	projectionKind string

	hasOutput bool
	// output carries every descriptor field the runtime resolver reads, so the
	// gate's verdict is that resolver's verdict rather than a summary of it.
	output         lens.OutputDescriptorSpec
	outputResolved bool

	// spec is the lens's cypher text; specResolved is false when it is not a
	// literal or a resolvable local const. Read only by the descriptor-less
	// check, which has no declared key space to reason about and must fall back
	// to what the cypher says.
	spec         string
	specResolved bool

	pos token.Position
}

// refuse names why a lens must not ship, or "" when it may.
//
// The declared-key-space arm delegates to projection.CapReadWriterRefusal — the
// SAME function the runtime resolver calls at registration — over a lens.Rule
// built from the resolved declaration. One predicate, two callers: a conjunct
// added there is enforced here for free, and neither side can answer a question
// the other would answer differently.
func refuse(l capReadLens) string {
	if !l.targetsNatsKV() {
		return ""
	}
	if l.hasOutput {
		if !l.outputResolved {
			// Fail closed. A descriptor this gate cannot read may well claim the
			// namespace, and a computed key pattern must not walk past a
			// default-deny check by being unreadable.
			return "declares an Output descriptor whose fields are not statically resolvable (a string literal, a local string const, or a `+` concatenation of those), so this gate cannot tell whether it claims the " + capReadPrefix + " namespace — spell them so the producer-closure rule stays checkable"
		}
		if refusal := projection.CapReadWriterRefusal(l.rule()); refusal != "" {
			return refusal + ". Declare a pkgmgr.ReadGrantDomain and let its walks compile a real producer"
		}
		return ""
	}

	// No §6.13 descriptor: the lens declares no key space at all, and renders
	// its key by joining RETURN column values instead. That is the shape both
	// declaration-level checks are structurally blind to — a cypher returning
	// the literal 'cap-read.billing' into its first key column writes a live
	// five-token grant while declaring nothing — so the only evidence available
	// here is the cypher text.
	if !authPlaneBuckets[l.bucket] || !l.bucketResolved {
		// Off the auth plane a cap-read-shaped key reaches no reader: the D1
		// gate lists the capability bucket and nothing else. An unresolvable
		// bucket is handled by the same clause on purpose — see below.
		if !l.bucketResolved && l.specMentionsNamespace() {
			return "has no Output descriptor, a target bucket this gate cannot resolve, and a cypher naming " + capReadPrefix + " — so it cannot be shown NOT to mint D1 grants out of its RETURN columns. Spell the bucket as a literal or a local const"
		}
		return ""
	}
	if !l.specResolved {
		return "has no Output descriptor and targets the auth-plane bucket, but its cypher is not statically resolvable, so this gate cannot tell whether it renders a " + capReadPrefix + " key out of its RETURN columns — spell the spec as a literal or a local const"
	}
	if l.specMentionsNamespace() {
		return "has no Output descriptor and renders its key from RETURN columns, and its cypher names " + capReadPrefix + " while targeting the auth-plane bucket — a plain lens can mint a live D1 grant this way while declaring no key space at all, and no plane can ever announce it withdrawn. Declare a pkgmgr.ReadGrantDomain and let its walks compile a real producer"
	}
	return ""
}

// specMentionsNamespace reports whether the lens's cypher text names the D1
// namespace at all. Deliberately a substring test rather than a parse: the key
// is assembled at runtime from column VALUES, so any occurrence of the literal
// is the author reaching for the namespace, and a gate that tried to prove the
// assembly would be answering a harder question than the refusal needs.
func (l capReadLens) specMentionsNamespace() bool {
	return strings.Contains(l.spec, strings.TrimSuffix(capReadPrefix, "."))
}

// rule rebuilds the lens.Rule the runtime resolver would hold at registration,
// so the shared predicate answers about the same object on both sides.
//
// Target is nats_kv because that is the only target whose keys reach the D1
// gate's bucket listing; a postgres or nats_subject lens has no such namespace
// and is filtered by the bucket conjunct inside the predicate anyway.
func (l capReadLens) rule() *lens.Rule {
	out := l.output
	return &lens.Rule{
		ID:             "lint-cap-read-producers",
		CanonicalName:  l.name,
		ProjectionKind: l.projectionKind,
		Into: lens.IntoConfig{
			Target: "nats_kv",
			Bucket: l.provisionedBucket(),
			Key:    lens.KeyField{"key"},
		},
		Output: &out,
	}
}

func main() {
	strict := os.Getenv("STRICT") == "1"
	for _, a := range os.Args[1:] {
		if a == "--strict" {
			strict = true
		}
		if a == "--selftest" {
			runSelfTest(true)
			return
		}
	}

	// The rule is PREVENTIVE: the shipped corpus contains no violating lens, so
	// a clean run over the tree proves nothing on its own and a rule that
	// silently stopped matching would read as green forever. The vectors
	// therefore run on EVERY invocation, so CI executes the positive one too —
	// the shape lint-lens-anchors.go and lint-conventions.go both take.
	runSelfTest(false)

	var dirs []string
	globbed, _ := filepath.Glob("packages/*")
	dirs = append(dirs, globbed...)
	// The kernel declares the base cap-read producer itself, in its own type
	// rather than a pkgmgr.LensSpec. Leaving it out would exempt the one
	// producer every deployment runs.
	dirs = append(dirs, "internal/bootstrap")

	issues, producers := 0, 0
	for _, dir := range dirs {
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		i, p := checkDir(dir)
		issues += i
		producers += p
	}

	// A clean run is only meaningful if the enumeration actually REACHED the
	// producers. The corpus ships at least the kernel's base cap-read lens, so
	// seeing none means the declaration shape moved and this gate has been
	// silently inspecting nothing — which reads exactly like a clean corpus.
	if producers == 0 {
		fmt.Fprintln(os.Stderr, "lint-cap-read-producers: FAIL — the enumeration found NO cap-read producer anywhere in packages/ or internal/bootstrap; the kernel ships one, so the declaration shape this gate reads has moved and it is inspecting nothing")
		os.Exit(2)
	}

	if issues == 0 {
		fmt.Println("lint-cap-read-producers: 0 issues — every lens declaring a cap-read.* key space is a sink-capable read-grant producer")
		return
	}
	fmt.Printf("lint-cap-read-producers: %d issue(s)\n", issues)
	if strict {
		os.Exit(1)
	}
}

// checkDir parses one directory's non-test Go files and enforces the rule over
// every lens declaration it finds.
func checkDir(dir string) (issues, capReadProducers int) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		// A package that does not parse is another gate's problem, not ours.
		return 0, 0
	}

	consts := map[string]string{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			collectConsts(f, consts)
		}
	}
	var lenses []capReadLens
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			lenses = append(lenses, collectCapReadLenses(fset, f, consts)...)
		}
	}

	issues, producers := 0, 0
	for _, l := range lenses {
		if l.hasOutput && l.outputResolved && strings.HasPrefix(l.output.OutputKeyPattern, capReadPrefix) {
			producers++
		}
		if refusal := refuse(l); refusal != "" {
			fmt.Printf("%s:%d: lens %s %s\n", l.pos.Filename, l.pos.Line, describe(l), refusal)
			issues++
		}
	}
	return issues, producers
}

func describe(l capReadLens) string {
	if l.name != "" {
		return l.name
	}
	return "(name not statically resolvable)"
}

// collectConsts records every string const's value (backtick or quoted), so a
// pattern declared as a const still resolves.
func collectConsts(f *ast.File, out map[string]string) {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if v, err := strconv.Unquote(bl.Value); err == nil {
						out[name.Name] = v
					}
				}
			}
		}
	}
}

// collectCapReadLenses finds every lens declaration in one file and reads the
// fields this gate decides on.
//
// It walks EVERY composite literal whose type is named LensSpec or
// LensDefinition, wherever it appears — a slice element, a bare value returned
// from a helper, a map value, an argument to append. Position is not evidence:
// six shipped declarations are helper returns (packages/clinic-reminders and
// packages/wellness-reminders build their lenses in functions), so a gate that
// recognized only slice literals would be blind to a violating lens that is
// semantically identical to one it flags.
//
// Recognizing a declaration by TYPE rather than by file path or syntactic
// position is what keeps a package covered when it moves or refactors its
// declarations. What is EXCLUDED, stated exactly rather than as a closure
// claim: a composite literal whose own type, or whose enclosing container's
// element type, does not resolve to a lens after stripping pointers,
// parentheses, file-local aliases and any depth of slice/array/map nesting.
// Two real gaps remain and are named rather than implied — a lens type aliased
// in ANOTHER file of the same package (this walk is per-file), and a lens value
// built field-by-field through assignment rather than as a literal. Neither
// ships today; both would need a type-checked pass rather than an AST walk,
// which is a different tool.
//
// Nested literals are visited once: ast.Inspect reaches an element of a slice
// literal on its own, so the slice itself is not descended into separately, and
// positions are deduped to make that guarantee explicit rather than incidental.
func collectCapReadLenses(fset *token.FileSet, f *ast.File, consts map[string]string) []capReadLens {
	var out []capReadLens
	seen := map[token.Pos]bool{}
	lensTypes := lensTypeNames(f)

	take := func(cl *ast.CompositeLit) {
		if seen[cl.Pos()] {
			return
		}
		seen[cl.Pos()] = true
		out = append(out, readLensLiteral(fset, cl, consts))
	}

	// descend walks a container literal whose elements have type elemType,
	// taking the ones that ARE lenses and recursing through the ones that are
	// themselves containers. Recursion is what covers `map[string][]LensSpec`
	// and `[][]LensSpec`, where the lens literal sits two elisions deep.
	var descend func(cl *ast.CompositeLit, elemType ast.Expr)
	descend = func(cl *ast.CompositeLit, elemType ast.Expr) {
		elemType = unwrapType(elemType)
		inner, isContainer := containerElementType(elemType)
		for _, e := range cl.Elts {
			// An array literal may carry indexed elements and a map literal
			// always carries keyed ones; the lens is the VALUE in both.
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				e = kv.Value
			}
			el, ok := e.(*ast.CompositeLit)
			if !ok {
				continue
			}
			switch {
			case typeIsLens(elemType, lensTypes):
				take(el)
			case isContainer:
				descend(el, inner)
			}
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		// A literal that names its own type: a bare value returned from a
		// helper, an argument to append, an explicitly-typed element, the
		// kernel's own LensDefinition. A pointer literal (&LensSpec{…}) is the
		// same thing behind a UnaryExpr, and its inner composite is visited by
		// Inspect in its own right.
		if typeIsLens(cl.Type, lensTypes) {
			take(cl)
			return true
		}
		// A CONTAINER whose element type is (or eventually contains) a lens.
		// Inside `[]pkgmgr.LensSpec{{…}}`, `[]*pkgmgr.LensSpec{{…}}`,
		// `map[string]pkgmgr.LensSpec{"k": {…}}` and their nested forms, Go
		// elides the element type — those elements have a nil Type and name
		// nothing at all, so the only way to know what they are is to ask the
		// container. This is the corpus's most common shape by far.
		if inner, isContainer := containerElementType(cl.Type); isContainer {
			descend(cl, inner)
		}
		return true
	})
	return out
}

// lensTypeNames returns every type name that denotes a lens in this file: the
// two the declaration surfaces define, plus any file-local alias or definition
// that resolves to one.
//
// `type LS = pkgmgr.LensSpec` is a plain Go alias and `[]LS{{…}}` is the same
// declaration written differently; a gate that matched only the spelled-out
// names would be recognizing a NAME rather than a type, which is the thing
// position-based matching already got wrong once. Iterated to a fixpoint so an
// alias of an alias resolves too.
func lensTypeNames(f *ast.File) map[string]bool {
	names := map[string]bool{"LensSpec": true, "LensDefinition": true}
	for changed := true; changed; {
		changed = false
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name == nil || names[ts.Name.Name] {
				return true
			}
			if typeIsLens(ts.Type, names) {
				names[ts.Name.Name] = true
				changed = true
			}
			return true
		})
	}
	return names
}

// typeIsLens reports whether e names one of the lens types, through any number
// of pointer and paren wrappers.
func typeIsLens(e ast.Expr, names map[string]bool) bool {
	switch t := unwrapType(e).(type) {
	case *ast.SelectorExpr:
		return t.Sel != nil && names[t.Sel.Name]
	case *ast.Ident:
		return names[t.Name]
	}
	return false
}

// unwrapType strips the wrappers that change how a type is written but not
// which type it is: pointers (`[]*LensSpec` elides its elements exactly as
// `[]LensSpec` does) and parentheses.
func unwrapType(e ast.Expr) ast.Expr {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		default:
			return e
		}
	}
}

// containerElementType returns the type a container literal's elements have,
// and whether e is a container at all. A map's elements are its VALUES — the
// key never carries a lens.
func containerElementType(e ast.Expr) (ast.Expr, bool) {
	switch t := unwrapType(e).(type) {
	case *ast.ArrayType:
		return t.Elt, true
	case *ast.MapType:
		return t.Value, true
	}
	return nil, false
}

// readLensLiteral reads one lens composite literal's decidable fields.
//
// Field names are read across BOTH declaration surfaces: a package's
// pkgmgr.LensSpec spells the target bucket `Bucket` and the cypher `Spec`,
// while the kernel's LensDefinition spells them `TargetBucket` and
// `CypherRule`. A gate that knew only one spelling would silently skip a whole
// conjunct for the other corpus rather than fail visibly.
func readLensLiteral(fset *token.FileSet, el *ast.CompositeLit, consts map[string]string) capReadLens {
	l := capReadLens{pos: fset.Position(el.Pos()), bucketResolved: true, specResolved: true, outputResolved: true}
	// An omitted field is a decidable fact (absent), not an unreadable one; the
	// resolved flags start true and are cleared only by a field that IS present
	// and could not be read.
	for _, fe := range el.Elts {
		kv, ok := fe.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "CanonicalName":
			l.name, _ = resolveString(kv.Value, consts)
		case "Bucket", "TargetBucket":
			l.bucket, l.bucketResolved = resolveString(kv.Value, consts)
		case "Adapter":
			l.adapter, _ = resolveString(kv.Value, consts)
		case "ProjectionKind":
			l.projectionKind, _ = resolveString(kv.Value, consts)
		case "Spec", "CypherRule":
			l.spec, l.specResolved = resolveString(kv.Value, consts)
		case "Output":
			l.hasOutput = true
			l.output, l.outputResolved = readOutputDescriptor(kv.Value, consts)
		}
	}
	return l
}

// readOutputDescriptor reads an `&OutputDescriptorSpec{...}` literal into the
// runtime struct the shared predicate reads, field for field.
//
// Every field ParseOutputDescriptor validates is carried, including the ones a
// summary would drop — realnessFilter is the one that caught this: it is
// REQUIRED whenever entryKeyColumn is set, and a gate that did not read it
// passed a lens the installer refuses at boot.
//
// resolved is false when the literal is not a composite at all, or when any
// field is present but unreadable; the caller treats that as UNVERIFIABLE.
func readOutputDescriptor(e ast.Expr, consts map[string]string) (lens.OutputDescriptorSpec, bool) {
	var out lens.OutputDescriptorSpec
	if u, ok := e.(*ast.UnaryExpr); ok {
		e = u.X
	}
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return out, false
	}
	resolved := true
	str := func(v ast.Expr) string {
		s, ok := resolveString(v, consts)
		if !ok {
			resolved = false
		}
		return s
	}
	for _, fe := range cl.Elts {
		kv, ok := fe.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch id.Name {
		case "AnchorType":
			out.AnchorType = str(kv.Value)
		case "OutputKeyPattern":
			out.OutputKeyPattern = str(kv.Value)
		case "EmptyBehavior":
			out.EmptyBehavior = str(kv.Value)
		case "RealnessFilter":
			out.RealnessFilter = str(kv.Value)
		case "Freshness":
			out.Freshness = str(kv.Value)
		case "KeyColumn":
			out.KeyColumn = str(kv.Value)
		case "ActorField":
			out.ActorField = str(kv.Value)
		case "EntryKeyColumn":
			out.EntryKeyColumn = str(kv.Value)
		case "BodyColumns":
			cols, ok := resolveStringSlice(kv.Value, consts)
			if !ok {
				resolved = false
			}
			out.BodyColumns = cols
		case "Lanes":
			out.Lanes, _ = resolveStringSlice(kv.Value, consts)
		case "StaticEmptyColumns":
			out.StaticEmptyColumns, _ = resolveStringSlice(kv.Value, consts)
		}
	}
	return out, resolved
}

// resolveStringSlice reads a []string{...} literal. An absent or nil slice is
// resolvable-and-empty; an element this gate cannot read is not.
func resolveStringSlice(e ast.Expr, consts map[string]string) ([]string, bool) {
	if id, ok := e.(*ast.Ident); ok && id.Name == "nil" {
		return nil, true
	}
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(cl.Elts))
	for _, el := range cl.Elts {
		v, ok := resolveString(el, consts)
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// resolveString evaluates a statically-known string expression: a literal, a
// local string const, or any `+` concatenation of those.
//
// Concatenation is resolved rather than refused because it is how the shipped
// corpus actually writes a key pattern — `NoShowSettlementTarget +
// ".{actorSuffix}"` names the target once and derives the pattern from it, and
// eleven lenses do exactly that. Refusing the form would make the gate's
// fail-closed arm fire on a value that IS static, which is debt the gate would
// have to ship warn-first to carry.
//
// resolved is false for anything genuinely unreadable — a call, a cross-package
// reference, a const this directory does not declare — and every caller treats
// that as UNVERIFIABLE rather than as "not a cap-read producer".
func resolveString(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		left, okL := resolveString(v.X, consts)
		right, okR := resolveString(v.Y, consts)
		if !okL || !okR {
			return "", false
		}
		return left + right, true
	case *ast.ParenExpr:
		return resolveString(v.X, consts)
	}
	return "", false
}

// runSelfTest proves the rule end-to-end against synthetic lenses written to a
// scratch package directory and run through the same checkDir entry point the
// real corpus uses. This file carries `//go:build ignore` (like every script
// here), which keeps it out of `go test`'s package builds, so this doubles as
// the colocated test: `go run ./scripts/lint-cap-read-producers.go --selftest`.
//
// It runs unconditionally from main, where verbose is false: only failures
// print, and any mismatch exits 2 — the gate does not behave as documented,
// which is a different failure from a corpus violation. The tree ships no
// violating lens, so without these vectors a broken gate and a clean corpus
// are indistinguishable.
func runSelfTest(verbose bool) {
	dir, err := os.MkdirTemp("", "lint-cap-read-producers-selftest")
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-cap-read-producers selftest: FAIL — mkdtemp:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, "lenses.go"), []byte(selfTestFixture), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "lint-cap-read-producers selftest: FAIL — write fixture:", err)
		os.Exit(2)
	}

	var issues, producers int
	out, err := captureStdout(func() { issues, producers = checkDir(dir) })
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-cap-read-producers selftest: FAIL — capture stdout:", err)
		os.Exit(2)
	}
	if verbose {
		fmt.Print(out)
	}

	pass := true
	check := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-cap-read-producers selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}
	// The name and the reason must appear on the SAME finding line. Testing
	// them independently over the whole output is the classic wrong-reason
	// green: with a dozen fixtures in one run, some other lens's message
	// supplies the reason while this lens was flagged for something else.
	flagged := func(name, reason string) {
		hit := false
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, name) && strings.Contains(line, reason) {
				hit = true
				break
			}
		}
		check(hit, name+" is flagged, naming "+strconv.Quote(reason)+" on its own finding")
	}
	clean := func(name string) {
		check(!strings.Contains(out, name), name+" is not flagged")
	}

	// Each conjunct of the shared predicate, named in its own finding.
	flagged("plainLensClaimingTheNamespace_flagged", "without projectionKind actorAggregate")
	flagged("docModeProducer_flagged", "no entryKeyColumn")
	flagged("businessBucketProducer_flagged", "does not project an authorization surface")
	flagged("repeatedPlaceholder_flagged", "does not round-trip through AnchorFromKey")
	flagged("trailingLiteralAfterPlaceholder_flagged", "does not parse")
	// realnessFilter is the divergence that motivated calling the runtime
	// resolver instead of restating it: ParseOutputDescriptor REQUIRES it
	// whenever entryKeyColumn is set, and the gate's own first implementation
	// never read the field, so a lens that fails at boot shipped clean.
	flagged("missingRealnessFilter_flagged", "requires realnessFilter")
	flagged("unresolvablePattern_flagged", "not statically resolvable")
	flagged("kernelDocModeProducer_flagged", "no entryKeyColumn")

	// B1: a declaration is recognized by its TYPE, wherever it is written.
	// Six shipped lenses are helper returns; a violating one written that way
	// was invisible while being semantically identical to one that is flagged.
	flagged("helperReturnedProducer_flagged", "without projectionKind actorAggregate")
	flagged("appendedProducer_flagged", "no entryKeyColumn")
	flagged("mapValueProducer_flagged", "does not project an authorization surface")

	// The descriptor-less arm: a plain lens has NO declared key space and
	// renders its key from RETURN columns, which is the shape both
	// declaration-level checks are structurally blind to.
	flagged("pointerSliceProducer_flagged", "without projectionKind actorAggregate")
	flagged("pointerMapProducer_flagged", "without projectionKind actorAggregate")
	flagged("mapOfSliceProducer_flagged", "without projectionKind actorAggregate")
	flagged("sliceOfSliceProducer_flagged", "without projectionKind actorAggregate")
	flagged("aliasedTypeProducer_flagged", "without projectionKind actorAggregate")

	flagged("plainCapReadRenderer_flagged", "renders its key from RETURN columns")
	flagged("kernelPlainCapReadRenderer_flagged", "renders its key from RETURN columns")
	flagged("plainAuthPlaneUnresolvableSpec_flagged", "cypher is not statically resolvable")

	clean("sanctionedBaseProducer_clean")
	clean("sanctionedDomainProducer_clean")
	clean("writePlaneProducer_clean")
	clean("plainLensWithNoDescriptor_clean")
	clean("kernelShortBucketSpelling_clean")
	clean("kernelDefaultBucketSpelling_clean")
	clean("plainBusinessBucketMentionsNamespace_clean")
	clean("postgresGrantTableProducer_clean")
	clean("personalSubjectLens_clean")

	check(issues == 19, fmt.Sprintf("exactly nineteen issues total (got %d)", issues))
	check(producers == 19, fmt.Sprintf("the enumeration reached all nineteen descriptor-declaring cap-read fixtures, so the corpus-coverage floor in main counts what it claims to (got %d)", producers))

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-cap-read-producers: self-test failure(s) — the gate does not behave as documented")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}

// selfTestFixture is the synthetic package every vector above runs against. It
// is written to a scratch directory and read back through the same checkDir
// entry point the real corpus uses, so the vectors exercise the parse, the
// resolution and the predicate rather than a hand-built struct.
const selfTestFixture = `package selftestpkg

const computedPattern = "cap-read.computed.{actorSuffix}"
const capReadRenderingSpec = "MATCH (i:identity {key: $actorKey})-[:mayRead]->(x) RETURN 'cap-read.billing' AS d, 'identity' AS atype, nanoIdFromKey(i.key) AS aid, nanoIdFromKey(x.key) AS anchor"

var Lenses = []pkgmgr.LensSpec{
	{
		CanonicalName:  "sanctionedBaseProducer_clean",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "sanctionedDomainProducer_clean",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: computedPattern,
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName: "plainLensClaimingTheNamespace_flagged",
		Bucket:        "capability-kv",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.billing.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "docModeProducer_flagged",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.billing.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "businessBucketProducer_flagged",
		Bucket:         "weaver-targets",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.billing.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "repeatedPlaceholder_flagged",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.{actorSuffix}.x.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "trailingLiteralAfterPlaceholder_flagged",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.{actorSuffix}.grants",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "missingRealnessFilter_flagged",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.billing.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "unresolvablePattern_flagged",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read." + domain + ".{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName:  "writePlaneProducer_clean",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap.roles.{actorSuffix}",
			BodyColumns:      []string{"roles"},
			EmptyBehavior:    "delete",
			Freshness:        "auto",
		},
	},
	{
		CanonicalName: "plainLensWithNoDescriptor_clean",
		Bucket:        "weaver-targets",
		Spec:          "MATCH (t:task) RETURN t.key AS key",
	},
	{
		CanonicalName: "plainCapReadRenderer_flagged",
		Bucket:        "capability-kv",
		Spec:          capReadRenderingSpec,
	},
	{
		CanonicalName: "plainAuthPlaneUnresolvableSpec_flagged",
		Bucket:        "capability-kv",
		Spec:          buildSpec("mayRead"),
	},
	{
		CanonicalName: "plainBusinessBucketMentionsNamespace_clean",
		Bucket:        "weaver-targets",
		Spec:          capReadRenderingSpec,
	},
	{
		// A Path-A grant-table producer: postgres, no bucket at all, and a
		// cypher that names the namespace all over. It writes actor_read_grants
		// ROWS, never a KV key, so it cannot mint the thing this gate guards.
		// Its empty Bucket means "no bucket", NOT the kernel's capability
		// default — conflating the two absences flags eleven shipped lenses.
		CanonicalName: "postgresGrantTableProducer_clean",
		Adapter:       "postgres",
		GrantTable:    true,
		Spec:          capReadRenderingSpec,
	},
	{
		// Same for the Personal transport: a nats-subject lens publishes to a
		// subject, so no key it renders ever reaches the D1 bucket listing.
		CanonicalName: "personalSubjectLens_clean",
		Adapter:       "nats-subject",
		Spec:          capReadRenderingSpec,
	},
}

// A helper RETURN is one of the six shipped declaration shapes — position is
// not evidence, so it must be recognized by type like any other.
func HelperReturned() pkgmgr.LensSpec {
	return pkgmgr.LensSpec{
		CanonicalName: "helperReturnedProducer_flagged",
		Bucket:        "capability-kv",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.helper.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	}
}

func Appended(in []pkgmgr.LensSpec) []pkgmgr.LensSpec {
	return append(in, pkgmgr.LensSpec{
		CanonicalName:  "appendedProducer_flagged",
		Bucket:         "capability-kv",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.appended.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			Freshness:        "auto",
		},
	})
}

var ByName = map[string]pkgmgr.LensSpec{
	"mapValue": {
		CanonicalName:  "mapValueProducer_flagged",
		Bucket:         "weaver-targets",
		ProjectionKind: "actorAggregate",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.mapped.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
}

// The five shapes a walk that matched only the plain slice/map forms lets
// through. None ships today, which is exactly why they are vectors: the rule is
// about the TYPE, and a gate that recognized four spellings of it would be
// recognizing spellings.
type AliasedLens = pkgmgr.LensSpec

var PointerSlice = []*pkgmgr.LensSpec{
	{
		CanonicalName: "pointerSliceProducer_flagged",
		Bucket:        "capability-kv",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.ptrslice.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
}

var PointerMap = map[string]*pkgmgr.LensSpec{
	"k": {
		CanonicalName: "pointerMapProducer_flagged",
		Bucket:        "capability-kv",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.ptrmap.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
}

var MapOfSlice = map[string][]pkgmgr.LensSpec{
	"k": {
		{
			CanonicalName: "mapOfSliceProducer_flagged",
			Bucket:        "capability-kv",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "cap-read.mapslice.{actorSuffix}",
				BodyColumns:      []string{"readableAnchors"},
				EmptyBehavior:    "delete",
				RealnessFilter:   "anchorId",
				EntryKeyColumn:   "anchorId",
				Freshness:        "auto",
			},
		},
	},
}

var SliceOfSlice = [][]pkgmgr.LensSpec{
	{
		{
			CanonicalName: "sliceOfSliceProducer_flagged",
			Bucket:        "capability-kv",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "cap-read.sliceslice.{actorSuffix}",
				BodyColumns:      []string{"readableAnchors"},
				EmptyBehavior:    "delete",
				RealnessFilter:   "anchorId",
				EntryKeyColumn:   "anchorId",
				Freshness:        "auto",
			},
		},
	},
}

var Aliased = []AliasedLens{
	{
		CanonicalName: "aliasedTypeProducer_flagged",
		Bucket:        "capability-kv",
		Output: &pkgmgr.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.aliased.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	},
}

func KernelBase() LensDefinition {
	return LensDefinition{
		CanonicalName:  "kernelShortBucketSpelling_clean",
		TargetBucket:   "capability",
		ProjectionKind: "actorAggregate",
		Output: &OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	}
}

// An ABSENT TargetBucket defaults to the capability bucket in the seeder, so a
// gate that read only the explicit "capability" spelling would classify this
// one as a business lens and skip the auth-plane conjunct entirely.
func KernelDefaultBucket() LensDefinition {
	return LensDefinition{
		CanonicalName:  "kernelDefaultBucketSpelling_clean",
		ProjectionKind: "actorAggregate",
		Output: &OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.kerneldefault.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			RealnessFilter:   "anchorId",
			EntryKeyColumn:   "anchorId",
			Freshness:        "auto",
		},
	}
}

// The kernel declares its cypher as CypherRule, not Spec, so a gate reading
// only the package spelling would never see a descriptor-less kernel lens
// reaching for the namespace.
func KernelPlainRenderer() LensDefinition {
	return LensDefinition{
		CanonicalName: "kernelPlainCapReadRenderer_flagged",
		TargetBucket:  "capability",
		CypherRule:    capReadRenderingSpec,
	}
}

func KernelViolator() LensDefinition {
	return LensDefinition{
		CanonicalName:  "kernelDocModeProducer_flagged",
		TargetBucket:   "capability",
		ProjectionKind: "actorAggregate",
		Output: &OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap-read.kernel.{actorSuffix}",
			BodyColumns:      []string{"readableAnchors"},
			EmptyBehavior:    "delete",
			Freshness:        "auto",
		},
	}
}
`

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything fn printed to it.
func captureStdout(fn func()) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	saved := os.Stdout
	os.Stdout = w
	outCh := make(chan string, 1)
	go func() {
		var buf strings.Builder
		io.Copy(&buf, r)
		outCh <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = saved
	return <-outCh, nil
}
