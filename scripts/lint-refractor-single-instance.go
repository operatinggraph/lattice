//go:build ignore

// lint-refractor-single-instance — refuses the deployment affordance that would
// silently revoke the personal derivation licence's premise
// (personal-lens-derivation-licence-design.md §4.4c conjunct 5, §9 R4).
//
// # The rule
//
// The D1 read-grant change edge is an in-process function call: a producer's
// guarded write calls notifyGrantChange, which reaches Reprojector.GrantChanged,
// which drops the actor on a dirty set THIS process's drain owns. Nothing about
// it crosses a process boundary — and grantchange.GrantChangeEdgeSpansDeployment
// says so in code rather than in prose.
//
// A personal lens's narrowing rests on that edge. On a second Refractor
// instance, a producer on A announces to no personal lens on B, while every
// wiring conjunct a consumer could test — "a reprojector is wired", "the read
// gate is threaded" — stays true on both. That is a fail-open at exactly the
// transition, and the runtime backstop (the licence counts live instances off
// Health KV once per sweep) has a real hole in one direction: a newly started
// instance that has not yet written its first heartbeat UNDER-counts.
//
// So the primary defence is here, at build time, aimed at the author who makes
// Refractor multi-instance and who will not be reading that design. This gate
// fails the moment the repository gains an affordance for a second Refractor
// while the edge is still process-local.
//
// # What it REFUSES, exactly
//
//  1. a `replicas:` or `scale:` above one on a Refractor service in any compose
//     file — including override files — or one whose value it cannot resolve
//     (fail closed). A service counts as Refractor's by its NAME, its `image:`
//     or its `command:`, because a service called `projector` running the
//     refractor image is the same deployment fact under a different label;
//  2. two or more background launches of the refractor binary inside ONE
//     Makefile recipe, or a shell loop (`for`, `seq`, `xargs`) around one;
//  3. a compose `--scale <svc>=N` (N > 1) or `docker compose scale` command
//     anywhere in the Makefile or a compose file — the affordance that needs no
//     file edit at all, and so the one a replica-count check alone never sees;
//  4. a JetStream queue group (`DeliverGroup`) declared under cmd/refractor or
//     internal/refractor beyond the single pinned baseline occurrence — a new
//     one is a deliberate cross-instance fan-out declaration;
//  5. an instance-identity or cardinality knob read from the environment by
//     Refractor: REPLICA(S), INSTANCE_ID/INDEX, INSTANCES, SHARD, ORDINAL,
//     PARTITION, NODE_ID.
//
// It also fails when the DeliverGroup baseline VANISHES, on the shrink-only
// ledger rule the package-standard gate uses: a baseline that silently stopped
// matching is a gate inspecting nothing while reporting green.
//
// # What it CANNOT see — stated, because a gate is worth exactly its reach
//
// This list is the honest half of a clean run, and it is longer than the refusal
// list. Every entry is a way a second Refractor can arrive with nothing in this
// repository changing.
//
//   - Any orchestration outside this repository: Kubernetes Deployments or
//     StatefulSets, Helm charts, Nomad jobs, systemd units, ECS/Cloud Run
//     service definitions, a Procfile or supervisor config held elsewhere. This
//     repo carries none today; if one arrives, this gate must learn it or its
//     clean run stops meaning anything.
//   - An operator who simply starts `bin/refractor` twice, or runs the
//     `cycle-refractor` recipe without stopping the running process. No
//     build-time gate can refuse either, which is why the runtime instance count
//     exists as well — the two are a pair, not alternatives.
//   - A compose scale applied interactively (`docker compose up --scale
//     refractor=3` typed at a shell). The gate refuses that STRING where it is
//     committed; it cannot refuse it where it is typed.
//   - A refractor service composed under a name, image and command this gate
//     does not recognize, or built from a Dockerfile whose entrypoint it never
//     reads.
//   - The affordance that is ALREADY TRUE and is not this gate's to refuse:
//     Refractor's lens consumers are PULL consumers on a durable named per lens
//     (cmd/refractor's lensConsumerSpec), so two Refractor processes binding the
//     same durable already split that stream between them with no new
//     declaration anywhere. The pinned `DeliverGroup` on that same spec is set
//     alongside it and is baselined rather than flagged. This gate refuses the
//     repository's affordances; it does not — and cannot — refuse the
//     substrate's ability.
//
// # Vectors
//
// The self-vectors run on EVERY invocation, including the one CI performs,
// because the tree ships no violating configuration: a clean corpus proves
// nothing on its own, and a rule that silently stopped matching would read green
// forever. The `lint-lens-anchors` / `lint-cap-read-producers` precedent.
//
// STRICT=1 exits non-zero on any issue, which is the default posture.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
)

// deliverGroupBaseline is the one queue-group declaration the tree ships, pinned
// by file and by its own source text.
//
// It is a BASELINE rather than a refusal because it is not a transition anyone
// is about to make: it has been on the lens consumer spec since that spec
// existed, it sits on a pull consumer where a deliver group has no delivery
// semantics of its own, and the real cross-instance property of that spec is the
// shared durable NAME, which no gate can refuse without refusing the lens
// pipeline. Pinning it is what makes a SECOND one legible.
//
// Shrink-only, like the package-standard gate's debt ledger: a baseline entry
// that stops matching fails too, so the ledger cannot rot into a gate that
// inspects nothing.
var deliverGroupBaseline = []baselineSite{
	{file: "cmd/refractor/main.go", text: "DeliverGroup:    subjects.LensDurable(r.ID),"},
}

type baselineSite struct {
	file string
	text string
}

// instanceKnobPattern names the environment variables that would hand a
// Refractor process an identity or a cardinality within a set of peers. Every
// one of them is a shape that only makes sense when more than one instance runs.
var instanceKnobPattern = regexp.MustCompile(`(?i)(REPLICAS?|INSTANCE_ID|INSTANCE_INDEX|INSTANCES|SHARD|ORDINAL|PARTITION|NODE_ID)`)

// envReadPattern matches the two ways this codebase reads an environment
// variable. Matched on the LITERAL name, because a name assembled at runtime is
// not something an AST-free scan can resolve and is not a shape anything here
// writes.
var envReadPattern = regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\(\s*"([A-Za-z0-9_]+)"\s*\)`)

// refractorLaunchPattern matches a line that NAMES the refractor binary.
// Naming it is not starting it — a build, a kill, a liveness probe and an echo
// all name it — so backgroundPattern is what decides.
var refractorLaunchPattern = regexp.MustCompile(`(\./)?bin/refractor\b`)

// backgroundPattern matches a recipe line that ends by backgrounding what it
// just ran: a bare `&`, optionally followed by the Make line-continuation
// backslash. That trailing `&` is the difference between a line that STARTS a
// Refractor and a line that merely mentions one, and reading it is what keeps
// the shipped `cycle-refractor` recipe — which builds the binary, echoes about
// it, starts it once and then pgreps for it — from counting as four instances.
var backgroundPattern = regexp.MustCompile(`&\s*\\?\s*$`)

// scaleCommandPattern matches a compose scale APPLIED AS A COMMAND rather than
// declared as a field: `--scale refractor=3`, `docker compose scale rfx=2`. It
// is the affordance that needs no file edit at all, so a replica-count check
// over service blocks alone never sees it.
//
// It captures the service name and the count so a `=1` — which is a deployment,
// not a fan-out — reads the same as a declared `replicas: 1`.
var scaleCommandPattern = regexp.MustCompile(`(?i)(?:--scale|\bscale)\s+([A-Za-z0-9_.-]+)=(\d+)`)

// loopPattern matches the shell constructs that would turn one launch into N.
var loopPattern = regexp.MustCompile(`\b(for\s|seq\s|xargs\b|while\s)`)

// finding is one refusal, carrying where it was found and why.
type finding struct {
	where  string
	reason string
}

func (f finding) String() string { return f.where + ": " + f.reason }

// tree is the repository content the rule reads, gathered once so the same
// checker runs over the real corpus and over the self-test's synthetic one.
type tree struct {
	// composeFiles maps a path onto its content.
	composeFiles map[string]string
	// makefile is the Makefile's content, "" when there is none.
	makefile string
	// goFiles maps a path onto its content, non-test only.
	goFiles map[string]string
}

// check applies the whole rule and returns every refusal, plus how many baseline
// sites it actually reached — the coverage floor that keeps a clean run
// meaningful.
func check(t tree, edgeSpansDeployment bool) (findings []finding, baselineHits int) {
	if edgeSpansDeployment {
		// The premise is gone: the change edge reaches every process that hosts
		// a personal lens, so a second Refractor no longer silently strands a
		// grant withdrawal. The gate retires itself rather than being deleted,
		// so the reason it existed stays readable beside the constant that
		// retired it.
		return nil, 0
	}

	for path, body := range t.composeFiles {
		findings = append(findings, checkCompose(path, body)...)
	}
	findings = append(findings, checkMakefile(t.makefile)...)

	gf, hits := checkGoSources(t.goFiles)
	findings = append(findings, gf...)
	return findings, hits
}

// checkCompose refuses a replica or scale declaration above one on a Refractor
// service.
//
// The scan is indentation-aware rather than a YAML parse: a compose file's
// service block is two-space indented by convention throughout this repo, and a
// parse would drag a YAML dependency into a script whose whole job is to read
// four line shapes. What it CANNOT read it refuses — a value that is not a plain
// integer is a finding, not a skip.
func checkCompose(path, body string) []finding {
	var out []finding
	var pending []finding
	service := ""
	inServices := false
	isRefractor := false
	// emit routes a finding to the output when the current service is already
	// known to be Refractor's, and holds it otherwise: `image:` and `command:`
	// can sit below the replica line, so the classification is not final until
	// the block ends.
	emit := func(f finding) {
		if isRefractor {
			out = append(out, f)
			return
		}
		pending = append(pending, f)
	}
	for _, raw := range strings.Split(body, "\n") {
		// A scale COMMAND can appear anywhere in a compose file (an x- extension,
		// a comment-free command block) and needs no service block at all.
		out = append(out, scaleCommandFindings(path, raw)...)
		line := strings.TrimRight(raw, " \t")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		switch {
		case !strings.HasPrefix(line, " ") && strings.HasPrefix(line, "services:"):
			inServices = true
			service = ""
			continue
		case !strings.HasPrefix(line, " "):
			inServices = false
			service = ""
			isRefractor = false
			pending = pending[:0]
			continue
		}
		if !inServices {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)
		if indent == 2 && strings.HasSuffix(trimmed, ":") {
			service = strings.TrimSuffix(trimmed, ":")
			isRefractor = strings.Contains(strings.ToLower(service), "refractor")
			pending = pending[:0]
			continue
		}
		// A service is Refractor's by its NAME, its image or its command. A
		// service called `projector` running the refractor image is the same
		// deployment fact wearing a different label, and a gate that read only
		// the name would be reading a spelling. Because image and command can
		// appear AFTER the replica line, a non-matching service's findings are
		// held and either emitted or dropped once the block ends.
		if !isRefractor {
			lower := strings.ToLower(trimmed)
			if k, v, ok := strings.Cut(lower, ":"); ok {
				switch strings.TrimSpace(k) {
				case "image", "command", "entrypoint", "container_name":
					if strings.Contains(v, "refractor") {
						isRefractor = true
						out = append(out, pending...)
						pending = pending[:0]
					}
				}
			}
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "replicas" && key != "scale" {
			continue
		}
		value = strings.TrimSpace(value)
		n, err := strconv.Atoi(value)
		if err != nil {
			emit(finding{
				where:  path + " (service " + service + ")",
				reason: "declares `" + key + ": " + value + "`, which this gate cannot resolve to a number — while the grant-change edge is process-local a Refractor's cardinality must be legible, so spell it as a plain integer",
			})
			continue
		}
		if n > 1 {
			emit(finding{
				where:  path + " (service " + service + ")",
				reason: "declares `" + key + ": " + value + "`, a second Refractor instance, while grantchange.GrantChangeEdgeSpansDeployment is false — a read-grant producer on one instance announces to no personal lens on another, and every personal lens's derivation licence would keep narrowing on an edge that reaches one process in " + value + ". The precondition is the durable grant-change signal (personal-lens-derivation-licence-design.md §8 alternative #6), not this line",
			})
		}
	}
	return out
}

// scaleCommandFindings refuses a compose scale applied as a COMMAND. It runs
// over every line of every compose file and of the Makefile, because that is a
// string, not a structure, and it can live anywhere either of them allows one.
func scaleCommandFindings(where, raw string) []finding {
	var out []finding
	for _, m := range scaleCommandPattern.FindAllStringSubmatch(raw, -1) {
		svc, count := m[1], m[2]
		if !strings.Contains(strings.ToLower(svc), "refractor") {
			continue
		}
		n, err := strconv.Atoi(count)
		if err != nil || n <= 1 {
			// `=1` is a deployment, not a fan-out — the same reading a declared
			// `replicas: 1` gets.
			continue
		}
		out = append(out, finding{
			where:  where,
			reason: "scales `" + svc + "` to " + count + " with a compose scale COMMAND while grantchange.GrantChangeEdgeSpansDeployment is false — this needs no service-block edit at all, so a replica-count check alone never sees it, and the grant-change edge still reaches exactly one of the " + count + " processes",
		})
	}
	return out
}

// checkMakefile refuses two background Refractor launches inside one recipe, and
// any loop around one.
//
// Per RECIPE rather than per file, because the file legitimately carries several
// launch sites today (`up` starts one, `cycle-refractor` restarts one) and they
// are alternatives, never concurrent. What would be a real transition is one
// recipe starting two, or wrapping a launch in a loop — that is a deployment
// growing a second instance, not a second way to start the first.
func checkMakefile(body string) []finding {
	if body == "" {
		return nil
	}
	var out []finding
	for _, raw := range strings.Split(body, "\n") {
		out = append(out, scaleCommandFindings("Makefile", raw)...)
	}
	target := ""
	launches := 0
	flush := func() {
		if launches > 1 {
			out = append(out, finding{
				where:  "Makefile (recipe " + target + ")",
				reason: fmt.Sprintf("starts the refractor binary %d times in one recipe while grantchange.GrantChangeEdgeSpansDeployment is false — the grant-change edge is an in-process call, so a second instance strands every read-grant withdrawal the first one publishes, and every personal lens's derivation licence is narrowing on it", launches),
			})
		}
		launches = 0
	}
	for _, raw := range strings.Split(body, "\n") {
		if !strings.HasPrefix(raw, "\t") && !strings.HasPrefix(raw, " ") {
			flush()
			if name, _, ok := strings.Cut(raw, ":"); ok && name != "" && !strings.HasPrefix(raw, "#") && !strings.Contains(name, "=") {
				target = strings.TrimSpace(name)
			} else {
				target = ""
			}
			continue
		}
		if target == "" {
			continue
		}
		if !refractorLaunchPattern.MatchString(raw) {
			continue
		}
		// Four line shapes name the binary and start nothing: a build
		// (`go build -o bin/refractor`), a kill (`pkill -f bin/refractor`), a
		// liveness probe (`pgrep -x refractor`), and an echo about any of them.
		// The shipped `cycle-refractor` recipe carries all four around ONE real
		// launch, so a rule that counted mentions would report it as a
		// multi-instance deployment — a false refusal on the recipe an operator
		// runs after every merge.
		if strings.Contains(raw, "go build") || strings.Contains(raw, "pkill") || strings.Contains(raw, "pgrep") {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(raw, " \t@-"), "echo ") {
			continue
		}
		if loopPattern.MatchString(raw) {
			out = append(out, finding{
				where:  "Makefile (recipe " + target + ")",
				reason: "wraps a refractor launch in a shell loop while grantchange.GrantChangeEdgeSpansDeployment is false — one launch per loop iteration is N instances, and the grant-change edge reaches exactly one of them",
			})
			continue
		}
		if !backgroundPattern.MatchString(strings.TrimRight(raw, " \t")) {
			continue
		}
		launches++
	}
	flush()
	return out
}

// checkGoSources refuses a new queue group and any instance-identity knob, and
// enforces the DeliverGroup baseline in both directions.
func checkGoSources(files map[string]string) (out []finding, baselineHits int) {
	seen := map[string]bool{}
	for path, body := range files {
		for i, raw := range strings.Split(body, "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "//") {
				continue
			}
			if strings.Contains(line, "DeliverGroup") {
				if baselined(path, line) {
					seen[path+"\x00"+line] = true
					baselineHits++
					continue
				}
				out = append(out, finding{
					where:  fmt.Sprintf("%s:%d", path, i+1),
					reason: "declares a JetStream queue group (`" + line + "`) while grantchange.GrantChangeEdgeSpansDeployment is false — a queue group exists to spread one stream across instances, and every personal lens's derivation licence assumes there is only one. Land the durable grant-change signal first (personal-lens-derivation-licence-design.md §8 alternative #6), flip that constant, and this gate retires itself",
				})
				continue
			}
			for _, m := range envReadPattern.FindAllStringSubmatch(line, -1) {
				if !instanceKnobPattern.MatchString(m[1]) {
					continue
				}
				out = append(out, finding{
					where:  fmt.Sprintf("%s:%d", path, i+1),
					reason: "reads `" + m[1] + "`, an instance-identity or cardinality knob, while grantchange.GrantChangeEdgeSpansDeployment is false — a Refractor that knows which of N it is, is a Refractor whose grant-change edge reaches one process in N while every personal lens narrows as though it reached all of them",
				})
			}
		}
	}
	for _, b := range deliverGroupBaseline {
		if !seen[b.file+"\x00"+b.text] {
			out = append(out, finding{
				where:  b.file,
				reason: "the pinned queue-group baseline `" + b.text + "` is no longer present. If it was removed, strike its row from deliverGroupBaseline in this script; if it merely moved or was respelled, re-pin it. A baseline that silently stops matching leaves this gate inspecting nothing while reporting green",
			})
		}
	}
	return out, baselineHits
}

func baselined(path, line string) bool {
	for _, b := range deliverGroupBaseline {
		if b.file == path && b.text == line {
			return true
		}
	}
	return false
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

	runSelfTest(false)

	if grantchange.GrantChangeEdgeSpansDeployment {
		fmt.Println("lint-refractor-single-instance: RETIRED — grantchange.GrantChangeEdgeSpansDeployment is true, so the grant-change edge reaches every process that hosts a personal lens and a second Refractor no longer strands a grant withdrawal. Delete this gate and its Makefile/CI wiring.")
		return
	}

	t, err := readTree()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lint-refractor-single-instance: FAIL —", err)
		os.Exit(2)
	}
	findings, baselineHits := check(t, grantchange.GrantChangeEdgeSpansDeployment)

	// A clean run is only meaningful if the enumeration reached the corpus. The
	// baseline is the floor: the tree ships exactly the pinned queue-group
	// site(s), so reaching none means the file walk moved and this gate has been
	// reading an empty corpus — which looks exactly like a clean one.
	if baselineHits < len(deliverGroupBaseline) {
		fmt.Fprintf(os.Stderr, "lint-refractor-single-instance: FAIL — the source walk reached %d of %d pinned baseline sites; the enumeration has moved and this gate is inspecting nothing\n",
			baselineHits, len(deliverGroupBaseline))
		os.Exit(2)
	}

	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) == 0 {
		fmt.Println("lint-refractor-single-instance: 0 issues — the repository declares no second-instance affordance for Refractor while the grant-change edge is process-local.")
		fmt.Println("  Out of reach, by construction — the honest half of this result:")
		fmt.Println("    - orchestration outside this repo (k8s/Helm/Nomad/systemd/ECS/Procfile); none ships here today, and one arriving makes this clean run mean less than it does now")
		fmt.Println("    - an operator starting bin/refractor twice, or running cycle-refractor without stopping the process it replaces")
		fmt.Println("    - a `docker compose up --scale refractor=N` typed at a shell rather than committed")
		fmt.Println("    - a refractor service composed under a name, image and command this gate does not recognize")
		fmt.Println("    - the shared pull-consumer durable, which already lets two processes split a lens's stream with no declaration anywhere")
		fmt.Println("  The runtime instance count in the personal derivation licence is the other half of the pair; neither is sufficient alone.")
		return
	}
	fmt.Printf("lint-refractor-single-instance: %d issue(s)\n", len(findings))
	if strict {
		os.Exit(1)
	}
}

// readTree gathers the repository content the rule reads.
func readTree() (tree, error) {
	t := tree{composeFiles: map[string]string{}, goFiles: map[string]string{}}

	// Every spelling a compose file takes in the wild, override files included:
	// `docker-compose.override.yml` is precisely where a replica count lands
	// without touching the file under review.
	var candidates []string
	for _, pattern := range []string{
		"docker-compose*.y*ml", "compose*.y*ml",
		"deploy/*.y*ml", "deploy/*/*.y*ml",
	} {
		matched, _ := filepath.Glob(pattern)
		candidates = append(candidates, matched...)
	}
	seen := map[string]bool{}
	for _, p := range candidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		b, err := os.ReadFile(p)
		if err != nil {
			return t, fmt.Errorf("read %s: %w", p, err)
		}
		t.composeFiles[p] = string(b)
	}

	if b, err := os.ReadFile("Makefile"); err == nil {
		t.makefile = string(b)
	} else {
		return t, fmt.Errorf("read Makefile: %w", err)
	}

	for _, root := range []string{"cmd/refractor", "internal/refractor"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			t.goFiles[path] = string(b)
			return nil
		})
		if err != nil {
			return t, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	return t, nil
}

// reportReaderFindsOverrideFiles drives readTree over a scratch directory that
// carries a compose OVERRIDE file, so the glob itself is a vector rather than an
// assumption. It chdirs, which is safe here: this is a single-goroutine script,
// and the directory is restored before it returns.
func reportReaderFindsOverrideFiles(report func(bool, string)) {
	dir, err := os.MkdirTemp("", "lint-refractor-single-instance-reader")
	if err != nil {
		report(false, "reader vector: mkdtemp: "+err.Error())
		return
	}
	defer os.RemoveAll(dir)

	cwd, err := os.Getwd()
	if err != nil {
		report(false, "reader vector: getwd: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  nats:\n    image: nats\n"), 0o644); err != nil {
		report(false, "reader vector: write compose: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.override.yml"),
		[]byte("services:\n  refractor:\n    deploy:\n      replicas: 2\n"), 0o644); err != nil {
		report(false, "reader vector: write override: "+err.Error())
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("up:\n\t@echo hi\n"), 0o644); err != nil {
		report(false, "reader vector: write Makefile: "+err.Error())
		return
	}
	if err := os.Chdir(dir); err != nil {
		report(false, "reader vector: chdir: "+err.Error())
		return
	}
	defer func() { _ = os.Chdir(cwd) }()

	// readTree also walks cmd/refractor and internal/refractor, which do not
	// exist here; that walk's error is the reason this checks the compose map
	// rather than readTree's error.
	t, _ := readTree()
	_, override := t.composeFiles["docker-compose.override.yml"]
	report(override, "the reader globs compose OVERRIDE files — the file a replica count lands in without touching the one under review")
	if !override {
		return
	}
	report(len(checkCompose("docker-compose.override.yml", t.composeFiles["docker-compose.override.yml"])) > 0,
		"and the override file's replica count is refused once the reader has found it")
}

// runSelfTest exercises every refusal and every clean vector against synthetic
// content, through the same check entry point the real corpus goes through.
func runSelfTest(verbose bool) {
	pass := true
	report := func(cond bool, desc string) {
		switch {
		case !cond:
			fmt.Fprintln(os.Stderr, "lint-refractor-single-instance selftest: FAIL —", desc)
			pass = false
		case verbose:
			fmt.Println("selftest: PASS —", desc)
		}
	}

	// The baseline the real tree carries, reused by every vector so a synthetic
	// corpus never fails on ledger rot it was not written to exercise.
	baselineGo := map[string]string{
		"cmd/refractor/main.go": "package main\n\nfunc f() {\n\t\tDeliverGroup:    subjects.LensDurable(r.ID),\n}\n",
	}
	cleanMake := "up:\n\t./bin/refractor >log 2>&1 &\n\ncycle-refractor:\n\tgo build -o bin/refractor ./cmd/refractor\n\t./bin/refractor >>log 2>&1 &\n\ndown:\n\t-pkill -f \"bin/refractor\"\n"

	run := func(t tree, spans bool) []finding {
		f, _ := check(t, spans)
		return f
	}
	names := func(fs []finding) string {
		var b strings.Builder
		for _, f := range fs {
			b.WriteString(f.String())
			b.WriteString("\n")
		}
		return b.String()
	}
	flagged := func(fs []finding, where, reason, desc string) {
		hit := false
		for _, f := range fs {
			if strings.Contains(f.where, where) && strings.Contains(f.reason, reason) {
				hit = true
				break
			}
		}
		report(hit, desc+" (got: "+strings.TrimSpace(names(fs))+")")
	}

	// The positive vector FIRST: without it a green refusal could equally come
	// from a checker that refuses nothing.
	clean := tree{
		composeFiles: map[string]string{"docker-compose.yml": "services:\n  nats:\n    image: nats\n  postgres:\n    image: pg\n"},
		makefile:     cleanMake,
		goFiles:      baselineGo,
	}
	report(len(run(clean, false)) == 0, "the shipped shape — no refractor service, one launch per recipe, one baselined queue group — is clean (got: "+strings.TrimSpace(names(run(clean, false)))+")")

	// 1. compose replicas / scale.
	replicated := clean
	replicated.composeFiles = map[string]string{"docker-compose.yml": "services:\n  refractor:\n    image: rfx\n    deploy:\n      replicas: 3\n"}
	flagged(run(replicated, false), "refractor", "a second Refractor instance", "a refractor service with replicas: 3 is refused")

	scaled := clean
	scaled.composeFiles = map[string]string{"docker-compose.yml": "services:\n  refractor-worker:\n    image: rfx\n    scale: 2\n"}
	flagged(run(scaled, false), "refractor-worker", "a second Refractor instance", "a refractor service with scale: 2 is refused")

	unresolvable := clean
	unresolvable.composeFiles = map[string]string{"docker-compose.yml": "services:\n  refractor:\n    replicas: ${RFX_N}\n"}
	flagged(run(unresolvable, false), "refractor", "cannot resolve to a number", "a replica count this gate cannot read fails CLOSED")

	single := clean
	single.composeFiles = map[string]string{"docker-compose.yml": "services:\n  refractor:\n    image: rfx\n    deploy:\n      replicas: 1\n"}
	report(len(run(single, false)) == 0, "a refractor service at replicas: 1 is a deployment, not a fan-out, and is clean")

	otherService := clean
	otherService.composeFiles = map[string]string{"docker-compose.yml": "services:\n  weaver:\n    deploy:\n      replicas: 4\n"}
	report(len(run(otherService, false)) == 0, "another component's replica count is not this gate's business")

	// 1b. A service is Refractor's by IMAGE or COMMAND, not only by name — a
	// service called `projector` running the refractor image is the same
	// deployment fact under a different label. The replica line sits ABOVE the
	// image line here on purpose: the classification is not final until the
	// block ends, and a gate that decided at the replica line would miss it.
	byImage := clean
	byImage.composeFiles = map[string]string{"docker-compose.yml": "services:\n  projector:\n    deploy:\n      replicas: 4\n    image: ghcr.io/lattice/refractor:1.2\n"}
	flagged(run(byImage, false), "projector", "a second Refractor instance", "a refractor service recognized by its IMAGE is refused")

	byCommand := clean
	byCommand.composeFiles = map[string]string{"docker-compose.yml": "services:\n  worker:\n    scale: 2\n    command: /bin/refractor --serve\n"}
	flagged(run(byCommand, false), "worker", "a second Refractor instance", "a refractor service recognized by its COMMAND is refused")

	byOther := clean
	byOther.composeFiles = map[string]string{"docker-compose.yml": "services:\n  weaver:\n    replicas: 4\n    image: ghcr.io/lattice/weaver:1.2\n"}
	report(len(run(byOther, false)) == 0, "a service that is neither named nor built as refractor stays clean, so the image match is not a substring free-for-all")

	// 1c. An OVERRIDE file is where a replica count lands without touching the
	// file under review; the reader globs for it, and this vector pins that the
	// checker treats it like any other compose file.
	override := clean
	override.composeFiles = map[string]string{
		"docker-compose.yml":          "services:\n  nats:\n    image: nats\n",
		"docker-compose.override.yml": "services:\n  refractor:\n    deploy:\n      replicas: 2\n",
	}
	flagged(run(override, false), "docker-compose.override.yml", "a second Refractor instance", "a replica count in an OVERRIDE file is refused")

	// 3. The scale COMMAND — the affordance that needs no service-block edit at
	// all, so a replica-count check over service blocks alone never sees it.
	scaleCmd := clean
	scaleCmd.makefile = "up:\n\tdocker compose up -d --scale refractor=3\n"
	flagged(run(scaleCmd, false), "Makefile", "compose scale COMMAND", "a --scale refractor=3 in the Makefile is refused")

	scaleInCompose := clean
	scaleInCompose.composeFiles = map[string]string{"docker-compose.yml": "x-notes: run with docker compose scale refractor=2\n"}
	flagged(run(scaleInCompose, false), "docker-compose.yml", "compose scale COMMAND", "a scale command inside a compose file is refused")

	scaleOne := clean
	scaleOne.makefile = "up:\n\tdocker compose up -d --scale refractor=1\n"
	report(len(run(scaleOne, false)) == 0, "--scale refractor=1 is a deployment, not a fan-out, and stays clean")

	scaleOther := clean
	scaleOther.makefile = "up:\n\tdocker compose up -d --scale weaver=3\n"
	report(len(run(scaleOther, false)) == 0, "scaling another component is not this gate's business")

	// 2. Makefile: two launches in one recipe, and a loop around one.
	twoInOne := clean
	twoInOne.makefile = "up:\n\t./bin/refractor >a.log 2>&1 &\n\t./bin/refractor >b.log 2>&1 &\n"
	flagged(run(twoInOne, false), "recipe up", "2 times in one recipe", "two refractor launches in ONE recipe are refused")

	looped := clean
	looped.makefile = "up:\n\tfor i in $(seq 2); do ./bin/refractor & done\n"
	flagged(run(looped, false), "recipe up", "shell loop", "a loop around a refractor launch is refused")

	report(len(checkMakefile(cleanMake)) == 0,
		"two SEPARATE recipes each starting one refractor are alternatives, not instances, and stay clean")

	// The shipped cycle-refractor shape, verbatim in structure: a kill, an echo,
	// a build, another echo, ONE background launch, and a pgrep probe — five
	// lines naming the binary around one instance. A rule that counted mentions
	// rather than backgrounded launches would refuse the recipe an operator runs
	// after every merge, which is the false-positive this vector exists to hold.
	shippedShape := "cycle-refractor: assert-main-checkout\n" +
		"\t@echo \"==> Killing the running refractor...\"\n" +
		"\t-pkill -x refractor 2>/dev/null || true\n" +
		"\t@echo \"==> Rebuilding bin/refractor...\"\n" +
		"\tgo build -o bin/refractor ./cmd/refractor\n" +
		"\t@echo \"==> Starting refractor in background...\"\n" +
		"\t@NATS_URL=$(NATS_URL) ./bin/refractor >>refractor.log 2>&1 </dev/null & \\\n" +
		"\t  sleep 2; pgrep -x refractor >/dev/null && echo \"==> refractor running\" || { echo \"!! see refractor.log\"; exit 1; }\n"
	report(len(checkMakefile(shippedShape)) == 0,
		"the shipped cycle-refractor recipe — build, echoes and a pgrep around ONE launch — is one instance, not five")

	// 3. a new queue group, and the baseline in both directions.
	newGroup := clean
	newGroup.goFiles = map[string]string{
		"cmd/refractor/main.go":       baselineGo["cmd/refractor/main.go"],
		"internal/refractor/other.go": "package other\n\nvar s = Spec{DeliverGroup: \"rfx-shared\"}\n",
	}
	flagged(run(newGroup, false), "internal/refractor/other.go", "declares a JetStream queue group", "a SECOND queue group under Refractor is refused")

	movedBaseline := clean
	movedBaseline.goFiles = map[string]string{"cmd/refractor/main.go": "package main\n"}
	flagged(run(movedBaseline, false), "cmd/refractor/main.go", "no longer present", "a vanished baseline fails too, so the ledger cannot rot")

	commented := clean
	commented.goFiles = map[string]string{
		"cmd/refractor/main.go":       baselineGo["cmd/refractor/main.go"],
		"internal/refractor/other.go": "package other\n\n// DeliverGroup is what a queue group is called.\n",
	}
	report(len(run(commented, false)) == 0, "a comment naming DeliverGroup is prose, not a declaration")

	// 4. instance-identity knobs.
	for _, knob := range []string{"REFRACTOR_REPLICAS", "REFRACTOR_INSTANCE_ID", "RFX_SHARD", "POD_ORDINAL", "REFRACTOR_PARTITION", "NODE_ID"} {
		v := clean
		v.goFiles = map[string]string{
			"cmd/refractor/main.go":      baselineGo["cmd/refractor/main.go"],
			"internal/refractor/knob.go": "package knob\n\nvar n = os.Getenv(\"" + knob + "\")\n",
		}
		flagged(run(v, false), "internal/refractor/knob.go", "instance-identity or cardinality knob", knob+" is refused")
	}

	innocuous := clean
	innocuous.goFiles = map[string]string{
		"cmd/refractor/main.go":      baselineGo["cmd/refractor/main.go"],
		"internal/refractor/knob.go": "package knob\n\nvar a = os.Getenv(\"REFRACTOR_WALK_SCOPE\")\nvar b = os.LookupEnv(\"REFRACTOR_MAX_BINDINGS\")\n",
	}
	report(len(run(innocuous, false)) == 0, "the shipped Refractor env knobs are not instance knobs and stay clean")

	// 4. THE READER, not the checker. Every vector above hands check() a
	// pre-built map, so none of them exercises readTree's globbing — and the
	// glob is where an override file is either seen or missed. A replica count
	// lands in docker-compose.override.yml precisely because that file is not
	// the one under review, so a gate that read only docker-compose.yml would
	// report clean over the exact shape it exists to refuse.
	reportReaderFindsOverrideFiles(report)

	// 5. the retirement vector: every refusal above goes away once the durable
	// grant-change signal lands and the constant flips.
	allBad := tree{
		composeFiles: map[string]string{"docker-compose.yml": "services:\n  refractor:\n    deploy:\n      replicas: 5\n"},
		makefile:     "up:\n\t./bin/refractor &\n\t./bin/refractor &\n",
		goFiles: map[string]string{
			"internal/refractor/other.go": "package other\n\nvar s = Spec{DeliverGroup: \"rfx\"}\n",
			"internal/refractor/knob.go":  "package knob\n\nvar n = os.Getenv(\"REFRACTOR_REPLICAS\")\n",
		},
	}
	report(len(run(allBad, false)) > 0, "the multi-instance corpus is refused while the edge is process-local")
	report(len(run(allBad, true)) == 0,
		"the SAME corpus passes once grantchange.GrantChangeEdgeSpansDeployment is true — the gate guards a premise, not a shape")

	if !pass {
		fmt.Fprintln(os.Stderr, "lint-refractor-single-instance: self-test failure(s) — the gate does not behave as documented")
		os.Exit(2)
	}
	if verbose {
		fmt.Println("selftest: all vectors passed")
	}
}
