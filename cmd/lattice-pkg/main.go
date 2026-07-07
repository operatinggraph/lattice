// Command lattice-pkg is the Capability Package install / uninstall /
// list CLI. See docs/components/_packages.md.
//
// Usage:
//
//	lattice-pkg install <path-to-package-dir>
//	lattice-pkg uninstall <package-canonical-name>
//	lattice-pkg list
//
// The operator credential is the admin actor NanoID read from
// lattice.bootstrap.json. Install submits an InstallPackage op to the
// Processor; the Processor is the sole writer of core-kv.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	augur "github.com/asolgan/lattice/packages/augur"
	bespokecontracts "github.com/asolgan/lattice/packages/bespoke-contracts"
	cafeledger "github.com/asolgan/lattice/packages/cafe-ledger"
	capabilityauthor "github.com/asolgan/lattice/packages/capability-author"
	clinicdomain "github.com/asolgan/lattice/packages/clinic-domain"
	clinicledger "github.com/asolgan/lattice/packages/clinic-ledger"
	clinicreminders "github.com/asolgan/lattice/packages/clinic-reminders"
	consoleoperator "github.com/asolgan/lattice/packages/console-operator"
	controlauthz "github.com/asolgan/lattice/packages/control-authz"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	identityhygiene "github.com/asolgan/lattice/packages/identity-hygiene"
	leasesigning "github.com/asolgan/lattice/packages/lease-signing"
	locationdomain "github.com/asolgan/lattice/packages/location-domain"
	loftspacedomain "github.com/asolgan/lattice/packages/loftspace-domain"
	loftspaceledger "github.com/asolgan/lattice/packages/loftspace-ledger"
	objectsbase "github.com/asolgan/lattice/packages/objects-base"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	privacybase "github.com/asolgan/lattice/packages/privacy-base"
	privacyoperatorgrant "github.com/asolgan/lattice/packages/privacy-operator-grant"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
	servicelocation "github.com/asolgan/lattice/packages/service-location"
)

// bootstrapJSON is the on-disk shape of lattice.bootstrap.json. We need
// the bootstrap identity key as the install-time admin actor.
type bootstrapJSON struct {
	PrimordialIDs map[string]string `json:"primordialIDs"`
}

// packageRegistry maps a directory name to its Go Definition. Phase 1
// is a static import map; future package discovery is out of scope.
var packageRegistry = map[string]pkgmgr.Definition{
	"rbac-domain":            rbacdomain.Package,
	"identity-domain":        identitydomain.Package,
	"identity-hygiene":       identityhygiene.Package,
	"orchestration-base":     orchestrationbase.Package,
	"service-domain":         servicedomain.Package,
	"location-domain":        locationdomain.Package,
	"loftspace-domain":       loftspacedomain.Package,
	"clinic-domain":          clinicdomain.Package,
	"clinic-ledger":          clinicledger.Package,
	"clinic-reminders":       clinicreminders.Package,
	"service-location":       servicelocation.Package,
	"lease-signing":          leasesigning.Package,
	"loftspace-ledger":       loftspaceledger.Package,
	"cafe-ledger":            cafeledger.Package,
	"objects-base":           objectsbase.Package,
	"augur":                  augur.Package,
	"capability-author":      capabilityauthor.Package,
	"privacy-base":           privacybase.Package,
	"privacy-operator-grant": privacyoperatorgrant.Package,
	"bespoke-contracts":      bespokecontracts.Package,
	"control-authz":          controlauthz.Package,
	"console-operator":       consoleoperator.Package,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	natsURL := envOrDefault("NATS_URL", "nats://localhost:4222")
	bootstrapPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	switch cmd {
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		force := fs.Bool("force", false, "same-version: re-apply changed entity bodies in place (dev refresh)")
		dryRun := fs.Bool("dry-run", false, "preview the create/update/tombstone delta without submitting")
		_ = fs.Parse(args)
		rest := fs.Args()
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "install requires [--force] [--dry-run] <path-to-package-dir>")
			os.Exit(2)
		}
		opts := pkgmgr.ApplyOptions{Force: *force, DryRun: *dryRun}
		if err := runApply("install", rest[0], natsURL, bootstrapPath, opts, logger); err != nil {
			if errors.Is(err, pkgmgr.ErrBootstrapRequired) {
				fmt.Fprintln(os.Stderr, "install failed: core-kv bucket not found — run `lattice-pkg bootstrap` (or `make up`) before installing packages.")
			} else {
				fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
			}
			os.Exit(1)
		}
	case "upgrade":
		fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
		dryRun := fs.Bool("dry-run", false, "preview the create/update/tombstone delta without submitting")
		_ = fs.Parse(args)
		rest := fs.Args()
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "upgrade requires [--dry-run] <path-to-package-dir>")
			os.Exit(2)
		}
		opts := pkgmgr.ApplyOptions{DryRun: *dryRun, RequireInstalled: true}
		if err := runApply("upgrade", rest[0], natsURL, bootstrapPath, opts, logger); err != nil {
			if errors.Is(err, pkgmgr.ErrBootstrapRequired) {
				fmt.Fprintln(os.Stderr, "upgrade failed: core-kv bucket not found — run `lattice-pkg bootstrap` (or `make up`) before upgrading packages.")
			} else if errors.Is(err, pkgmgr.ErrNotInstalled) {
				fmt.Fprintf(os.Stderr, "upgrade failed: %v — run `lattice-pkg install` first.\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "upgrade failed: %v\n", err)
			}
			os.Exit(1)
		}
	case "uninstall":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "uninstall requires <package-canonical-name>")
			os.Exit(2)
		}
		if err := runUninstall(args[0], natsURL, bootstrapPath, logger); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall failed: %v\n", err)
			os.Exit(1)
		}
	case "apply-proposal":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "apply-proposal requires <capability-proposal-id>")
			os.Exit(2)
		}
		if err := runApplyProposal(args[0], natsURL, bootstrapPath, logger); err != nil {
			fmt.Fprintf(os.Stderr, "apply-proposal failed: %v\n", err)
			os.Exit(1)
		}
	case "list":
		if err := runList(natsURL, bootstrapPath, logger); err != nil {
			fmt.Fprintf(os.Stderr, "list failed: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `lattice-pkg — Capability Package CLI

Usage:
  lattice-pkg install [--force] [--dry-run] <path-to-package-dir>
  lattice-pkg upgrade [--dry-run] <path-to-package-dir>
  lattice-pkg uninstall <package-canonical-name>
  lattice-pkg list
  lattice-pkg apply-proposal <capability-proposal-id>

install dispatches on installed state:
  not installed        → fresh install
  same version         → skip (use --force to re-apply changed bodies)
  different version    → in-place upgrade (diff-apply)
upgrade is the explicit in-place diff-apply (errors if not installed).
--dry-run previews the create/update/tombstone delta without submitting.

apply-proposal materializes an APPROVED AI-authored capability proposal
(see "lattice capability review") into a Definition and installs/upgrades
it through this same Apply path, then records MarkCapabilityProposalApplied.

Environment:
  NATS_URL              default nats://localhost:4222
  BOOTSTRAP_JSON_PATH   default ./lattice.bootstrap.json`)
}

// runApply drives both the `install` and `upgrade` commands through the
// upgrade-aware pkgmgr.Apply dispatcher. cmd is "install" or "upgrade" (it only
// shapes the log/error wording; the actual dispatch is governed by opts).
func runApply(cmd, pkgPath, natsURL, bootstrapPath string, opts pkgmgr.ApplyOptions, logger *slog.Logger) error {
	manifestPath := filepath.Join(pkgPath, "manifest.yaml")
	manifest, err := pkgmgr.ParseManifest(manifestPath)
	if err != nil {
		return err
	}
	def, ok := packageRegistry[manifest.Name]
	if !ok {
		return fmt.Errorf("package %q not in compiled registry; rebuild lattice-pkg with the package's Go code imported", manifest.Name)
	}
	if err := manifest.VerifyAgainstDefinition(def); err != nil {
		return err
	}
	// Read admin actor + kernel role NanoIDs from lattice.bootstrap.json.
	// The Installer resolves Permission.GrantsTo canonical names through
	// the RoleIDs map; roles a package declares itself (Definition.Roles)
	// are minted with deterministic NanoIDs and merged in at install time
	// (see pkgmgr.Installer.resolveGrants).
	bs, adminActor, err := loadBootstrap(bootstrapPath)
	if err != nil {
		return err
	}

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:          natsURL,
		Name:         "lattice-pkg",
		NKeySeedFile: envOrDefault("NATS_NKEY", ""),
		CredsFile:    envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate open: %w", err)
	}
	defer conn.Close()

	inst := pkgmgr.NewInstaller(conn, adminActor)
	inst.RoleIDs = roleIDsFromBootstrap(bs)
	res, err := inst.Apply(context.Background(), def, opts)
	if err != nil {
		return err
	}
	logApplyResult(cmd, res, logger)
	return nil
}

// logApplyResult renders an ApplyResult: a dry-run prints the previewed delta +
// affected keys; a real run logs the action that landed (install / upgrade /
// skip).
func logApplyResult(cmd string, res *pkgmgr.ApplyResult, logger *slog.Logger) {
	for _, w := range res.DependencyWarnings {
		logger.Warn(w)
	}
	if res.DryRun {
		logger.Info("dry-run — no changes submitted",
			"package", res.PackageName,
			"action", res.Action,
			"from", res.FromVersion,
			"to", res.ToVersion,
			"created", res.Created,
			"updated", res.Updated,
			"tombstoned", res.Tombstoned,
		)
		logKeys(logger, "create", res.CreatedKeys)
		logKeys(logger, "update", res.UpdatedKeys)
		logKeys(logger, "tombstone", res.TombstonedKeys)
		return
	}
	if res.Skipped {
		logger.Info(cmd+" skipped", "reason", res.Reason, "package", res.PackageName)
		return
	}
	switch res.Action {
	case "upgrade":
		logger.Info("upgrade committed",
			"package", res.PackageName,
			"fromVersion", res.FromVersion,
			"toVersion", res.ToVersion,
			"created", res.Created,
			"updated", res.Updated,
			"tombstoned", res.Tombstoned,
		)
	default:
		logger.Info("install committed",
			"package", res.PackageName,
			"version", res.ToVersion,
			"packageKey", res.PackageKey,
			"writeCount", res.Created,
		)
	}
}

func logKeys(logger *slog.Logger, op string, keys []string) {
	for _, k := range keys {
		logger.Info("dry-run delta", "op", op, "key", k)
	}
}

func runUninstall(packageName, natsURL, bootstrapPath string, logger *slog.Logger) error {
	_, adminActor, err := loadBootstrap(bootstrapPath)
	if err != nil {
		return err
	}
	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:          natsURL,
		Name:         "lattice-pkg",
		NKeySeedFile: envOrDefault("NATS_NKEY", ""),
		CredsFile:    envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate open: %w", err)
	}
	defer conn.Close()

	inst := pkgmgr.NewInstaller(conn, adminActor)
	res, err := inst.Uninstall(context.Background(), packageName)
	if err != nil {
		return err
	}
	logger.Info("uninstall committed",
		"package", res.PackageName,
		"tombstoneCount", len(res.Tombstoned),
		"note", res.Note,
	)
	return nil
}

// runApplyProposal materializes an APPROVED capability-author proposal
// (ai-authored-capabilities-design.md §3.5) into a Definition
// (pkgmgr.CapabilityApplyPlanForProposal — the SAME Definition §5 already
// validated at record/approve time) and installs/upgrades it through the
// existing, unmodified Apply dispatcher, then submits
// MarkCapabilityProposalApplied to close the loop. Two separate Processor
// commits, exactly as the DDL's own doc comment requires — this function
// never itself flips review.state.
func runApplyProposal(proposalID, natsURL, bootstrapPath string, logger *slog.Logger) error {
	if err := validateBareProposalID(proposalID); err != nil {
		return fmt.Errorf("proposal id: %w", err)
	}

	bs, adminActor, err := loadBootstrap(bootstrapPath)
	if err != nil {
		return err
	}

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:          natsURL,
		Name:         "lattice-pkg",
		NKeySeedFile: envOrDefault("NATS_NKEY", ""),
		CredsFile:    envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate open: %w", err)
	}
	defer conn.Close()

	ctx := context.Background()
	proposalKey := "vtx.capabilityproposal." + proposalID
	plan, err := pkgmgr.CapabilityApplyPlanForProposal(ctx, conn, proposalKey)
	if err != nil {
		return fmt.Errorf("build apply plan: %w", err)
	}

	inst := pkgmgr.NewInstaller(conn, adminActor)
	inst.RoleIDs = roleIDsFromBootstrap(bs)
	res, err := inst.Apply(ctx, plan.Definition, pkgmgr.ApplyOptions{})
	if err != nil {
		return fmt.Errorf("apply %s: %w", plan.PackageName, err)
	}
	logApplyResult("apply-proposal", res, logger)

	installRequestID := res.Action + ":" + res.PackageName + "@" + res.ToVersion
	markCtx, markCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer markCancel()
	reply, err := submitMarkApplied(markCtx, conn, adminActor, proposalID, res.PackageKey, installRequestID)
	if err != nil {
		return fmt.Errorf("MarkCapabilityProposalApplied: %w (the package IS already applied — packageKey=%s, installRequestId=%s; retry MarkCapabilityProposalApplied alone rather than re-running apply-proposal)", err, res.PackageKey, installRequestID)
	}
	if reply.Status == processor.ReplyStatusRejected {
		return fmt.Errorf("MarkCapabilityProposalApplied rejected: %s — %s", reply.Error.Code, reply.Error.Message)
	}
	logger.Info("capability proposal applied",
		"proposalId", proposalID,
		"packageKey", res.PackageKey,
		"installRequestId", installRequestID,
	)
	return nil
}

// validateBareProposalID rejects a proposal id carrying key-shape
// metacharacters — the same bare-id discipline the capabilityproposal DDL
// script itself enforces (required_bare_id in
// packages/capability-author/ddls.go). Without this, an id containing "."
// would silently address a different (or malformed) vtx.capabilityproposal
// key instead of failing with a clear message.
func validateBareProposalID(id string) error {
	if id == "" {
		return fmt.Errorf("must not be empty")
	}
	for _, bad := range []string{".", "*", ">", " ", "\t", "\n"} {
		if strings.Contains(id, bad) {
			return fmt.Errorf("must carry no dots / key segments, wildcards, or whitespace; got %q", id)
		}
	}
	return nil
}

// submitMarkApplied publishes MarkCapabilityProposalApplied via JetStream,
// carrying the reply inbox in a header (mirrors cmd/lattice/output.SubmitOp
// — a plain NATS Request() would receive only the JetStream publish-ack,
// since ops.<lane> is consumed by JetStream pull consumers).
func submitMarkApplied(ctx context.Context, conn *substrate.Conn, actor, proposalID, packageKey, installRequestID string) (*processor.OperationReply, error) {
	requestID, err := substrate.NewNanoID()
	if err != nil {
		return nil, fmt.Errorf("generate requestId: %w", err)
	}
	payload, err := json.Marshal(map[string]any{
		"proposalId":       proposalID,
		"packageKey":       packageKey,
		"installRequestId": installRequestID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneDefault,
		OperationType: "MarkCapabilityProposalApplied",
		Actor:         actor,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       json.RawMessage(payload),
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	const replyInboxHeader = "Lattice-Reply-Inbox"
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribe inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	subject := "ops." + string(env.Lane)
	msg := &nats.Msg{
		Subject: subject,
		Data:    envBytes,
		Header:  nats.Header{replyInboxHeader: []string{inbox}},
	}
	if _, err := conn.JetStream().PublishMsg(ctx, msg); err != nil {
		return nil, fmt.Errorf("publish to %s: %w", subject, err)
	}

	replyMsg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for reply: %w", err)
	}
	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	return &reply, nil
}

func runList(natsURL, bootstrapPath string, logger *slog.Logger) error {
	_ = bootstrapPath // not strictly required for list, kept for parity
	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{
		URL:          natsURL,
		Name:         "lattice-pkg",
		NKeySeedFile: envOrDefault("NATS_NKEY", ""),
		CredsFile:    envOrDefault("NATS_CREDS", ""),
	})
	if err != nil {
		return fmt.Errorf("substrate open: %w", err)
	}
	defer conn.Close()

	inst := pkgmgr.NewInstaller(conn, "")
	pkgs, err := inst.List(context.Background())
	if err != nil {
		return err
	}
	if len(pkgs) == 0 {
		fmt.Println("(no packages installed)")
		return nil
	}
	for _, p := range pkgs {
		fmt.Printf("%s\t%s\t%s\n", p.PackageName(), p.PackageVersion(), p.PackageKey())
	}
	return nil
}

func loadBootstrap(path string) (*bootstrapJSON, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	var b bootstrapJSON
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	bootstrapID := b.PrimordialIDs["bootstrapIdentity"]
	if bootstrapID == "" {
		return nil, "", errors.New("lattice.bootstrap.json missing primordialIDs.bootstrapIdentity")
	}
	return &b, "vtx.identity." + bootstrapID, nil
}

// roleIDsFromBootstrap returns the canonical-name → NanoID map for
// kernel-seeded roles. The kernel seeds the `operator` role; the other
// roles (consumer, frontOfHouse, backOfHouse, identityProvisioner) are
// declared by identity-domain (Definition.Roles) and minted with
// deterministic NanoIDs (pkgmgr.RoleID("identity-domain", canonicalName) —
// the same computation cmd/gateway/main.go's ConfigureProvisioning call
// already relies on to resolve the consumer role without a KV round-trip).
// `cmd/lattice-pkg install` runs identity-domain and its downstream
// consumers (e.g. lease-signing's CreateLeaseApplication scope=self grant,
// real-actor-write-auth-e2e design §3.4) as SEPARATE process invocations, so
// "merged into Installer.RoleIDs at install time" only holds within
// identity-domain's own install — a downstream package's install needs these
// resolved here too. Legacy bootstrap.json fields, when present, override
// the deterministic default (warm-tree compatibility).
func roleIDsFromBootstrap(bs *bootstrapJSON) map[string]string {
	out := map[string]string{}
	if id := bs.PrimordialIDs["roleOperator"]; id != "" {
		out["operator"] = id
	}
	for _, name := range []string{"consumer", "frontOfHouse", "backOfHouse", "identityProvisioner"} {
		out[name] = pkgmgr.RoleID("identity-domain", name)
	}
	// Legacy fields tolerated for warm-tree compatibility — override the
	// deterministic default above when present (roleConsumer/roleFrontOfHouse/
	// roleBackOfHouse only; rolePlatformIntl maps to a role no package
	// currently declares, so it has no deterministic default to override —
	// it stays a pure legacy pass-through, dropping out as a no-op in
	// resolveGrants unless some future package declares that role).
	for _, name := range []string{"roleConsumer", "roleFrontOfHouse", "roleBackOfHouse", "rolePlatformIntl"} {
		short := canonicalFromBootstrapField(name)
		if short == "" {
			continue
		}
		if id := bs.PrimordialIDs[name]; id != "" {
			out[short] = id
		}
	}
	return out
}

func canonicalFromBootstrapField(field string) string {
	switch field {
	case "roleConsumer":
		return "consumer"
	case "roleFrontOfHouse":
		return "frontOfHouse"
	case "roleBackOfHouse":
		return "backOfHouse"
	case "rolePlatformIntl":
		return "platformIntl"
	}
	return ""
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
