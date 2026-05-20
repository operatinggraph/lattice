// Command lattice-pkg is the Capability Package install / uninstall /
// list CLI. See docs/components/_packages.md.
//
// Usage:
//
//	lattice-pkg install <path-to-package-dir>
//	lattice-pkg uninstall <package-canonical-name>
//	lattice-pkg list
//
// Phase 1: substrate-direct. The operator credential is the admin
// actor NanoID read from lattice.bootstrap.json. Phase 2 / Story 5.3
// will route installs through the Processor as CreateMetaVertex
// operations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/substrate"
	identityhygiene "github.com/asolgan/lattice/packages/identity-hygiene"
)

// bootstrapJSON is the on-disk shape of lattice.bootstrap.json. We need
// the bootstrap identity key as the install-time admin actor.
type bootstrapJSON struct {
	PrimordialIDs map[string]string `json:"primordialIDs"`
}

// packageRegistry maps a directory name to its Go Definition. Phase 1
// is a static import map; future package discovery is out of scope.
var packageRegistry = map[string]pkgmgr.Definition{
	"identity-hygiene": identityhygiene.Package,
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
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "install requires <path-to-package-dir>")
			os.Exit(2)
		}
		if err := runInstall(args[0], natsURL, bootstrapPath, logger); err != nil {
			fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
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
  lattice-pkg install <path-to-package-dir>
  lattice-pkg uninstall <package-canonical-name>
  lattice-pkg list

Environment:
  NATS_URL              default nats://localhost:4222
  BOOTSTRAP_JSON_PATH   default ./lattice.bootstrap.json`)
}

func runInstall(pkgPath, natsURL, bootstrapPath string, logger *slog.Logger) error {
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
	// Resolve `grantsTo` role canonical names → role NanoIDs from
	// lattice.bootstrap.json.
	bs, adminActor, err := loadBootstrap(bootstrapPath)
	if err != nil {
		return err
	}
	def = resolveGrantsToRoleIDs(def, bs)

	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{URL: natsURL, Name: "lattice-pkg"})
	if err != nil {
		return fmt.Errorf("substrate open: %w", err)
	}
	defer conn.Close()

	inst := pkgmgr.NewInstaller(conn, adminActor)
	res, err := inst.Install(context.Background(), def)
	if err != nil {
		return err
	}
	for _, w := range res.DependencyWarnings {
		logger.Warn(w)
	}
	if res.Skipped {
		logger.Info("install skipped", "reason", res.Reason, "package", res.PackageName)
		return nil
	}
	logger.Info("install committed",
		"package", res.PackageName,
		"version", res.PackageVersion,
		"packageKey", res.PackageKey,
		"writeCount", len(res.DeclaredKeys),
	)
	return nil
}

func runUninstall(packageName, natsURL, bootstrapPath string, logger *slog.Logger) error {
	_, adminActor, err := loadBootstrap(bootstrapPath)
	if err != nil {
		return err
	}
	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{URL: natsURL, Name: "lattice-pkg"})
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

func runList(natsURL, bootstrapPath string, logger *slog.Logger) error {
	_ = bootstrapPath // not strictly required for list, kept for parity
	conn, err := substrate.Connect(context.Background(), substrate.ConnectOpts{URL: natsURL, Name: "lattice-pkg"})
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

// resolveGrantsToRoleIDs walks each Permission and translates each
// `grantsTo` entry from a role canonical name (e.g. "operator") to the
// real role NanoID from lattice.bootstrap.json. Unrecognized canonical
// names are passed through unchanged so the installer can flag them.
func resolveGrantsToRoleIDs(def pkgmgr.Definition, bs *bootstrapJSON) pkgmgr.Definition {
	roleMap := map[string]string{
		"consumer":     bs.PrimordialIDs["roleConsumer"],
		"frontOfHouse": bs.PrimordialIDs["roleFrontOfHouse"],
		"backOfHouse":  bs.PrimordialIDs["roleBackOfHouse"],
		"operator":     bs.PrimordialIDs["roleOperator"],
		"platformIntl": bs.PrimordialIDs["rolePlatformIntl"],
	}
	out := def
	out.Permissions = make([]pkgmgr.PermissionSpec, len(def.Permissions))
	for i, p := range def.Permissions {
		grants := make([]string, 0, len(p.GrantsTo))
		for _, g := range p.GrantsTo {
			if id, ok := roleMap[g]; ok && id != "" {
				grants = append(grants, id)
			} else {
				grants = append(grants, g)
			}
		}
		p.GrantsTo = grants
		out.Permissions[i] = p
	}
	return out
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
