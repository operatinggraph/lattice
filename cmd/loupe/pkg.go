package main

// Package detail + lifecycle endpoints (design §9):
//
//	GET  /api/package?key=          → manifest + graph-resolved contents
//	POST /api/packages/install     → multipart manifest.yaml; upgrade-aware Apply
//	POST /api/packages/upgrade     → same, but a missing base install is an error
//	POST /api/packages/uninstall   → JSON {name}; tombstones the declared keys
//
// A package is a compiled-in Go Definition (internal/pkgmgr); the uploaded
// manifest.yaml only names + cross-checks it. Install/upgrade/uninstall are
// P2-clean — the Installer submits InstallPackage / UpgradePackage /
// UninstallPackage ops through the Processor; Loupe never writes Core KV.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/substrate"
	augur "github.com/asolgan/lattice/packages/augur"
	bespokecontracts "github.com/asolgan/lattice/packages/bespoke-contracts"
	cafedomain "github.com/asolgan/lattice/packages/cafe-domain"
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
	onebill "github.com/asolgan/lattice/packages/one-bill"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	privacybase "github.com/asolgan/lattice/packages/privacy-base"
	privacyoperatorgrant "github.com/asolgan/lattice/packages/privacy-operator-grant"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
	servicelocation "github.com/asolgan/lattice/packages/service-location"
	wellnessdomain "github.com/asolgan/lattice/packages/wellness-domain"
)

// packageRegistry maps a manifest name to its compiled Go Definition. Keep in
// sync with cmd/lattice-pkg/main.go's packageRegistry — both binaries carry
// the same static import map until package discovery exists.
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
	"cafe-domain":            cafedomain.Package,
	"one-bill":               onebill.Package,
	"objects-base":           objectsbase.Package,
	"augur":                  augur.Package,
	"capability-author":      capabilityauthor.Package,
	"privacy-base":           privacybase.Package,
	"privacy-operator-grant": privacyoperatorgrant.Package,
	"bespoke-contracts":      bespokecontracts.Package,
	"control-authz":          controlauthz.Package,
	"console-operator":       consoleoperator.Package,
	"wellness-domain":        wellnessdomain.Package,
}

// pkgApplyTimeout bounds an install/upgrade/uninstall round-trip. The
// Installer reads the whole core-kv key list plus submits a batch op with a
// 30s internal budget (pkgmgr.DefaultBatchTimeout), so the default 8s
// per-request limit is too tight for this one endpoint family.
const pkgApplyTimeout = 45 * time.Second

// pkgManifestCap bounds an uploaded manifest.yaml read.
const pkgManifestCap = 1 << 20

// pkgSectionOrder fixes the section rendering order for the package detail's
// contents panel. Only sections with items are emitted.
var pkgSectionOrder = []struct{ kind, label string }{
	{"entities", "Entities (vertex types)"},
	{"aspects", "Aspect types"},
	{"operations", "Operations"},
	{"lenses", "Lenses"},
	{"orchestration", "Orchestration (weaver targets / loom patterns)"},
	{"roles", "Roles"},
	{"permissions", "Permissions"},
	{"grants", "Grants (permission → role links)"},
	{"other", "Other"},
}

// pkgEnvelope is the slice of a Core KV document envelope the package detail
// needs.
type pkgEnvelope struct {
	Class     string         `json:"class"`
	IsDeleted bool           `json:"isDeleted"`
	CreatedAt string         `json:"createdAt"`
	Data      map[string]any `json:"data"`
}

func readPkgEnvelope(get kvGetter, key string) (pkgEnvelope, bool) {
	raw, ok := get(key)
	if !ok {
		return pkgEnvelope{}, false
	}
	var env pkgEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return pkgEnvelope{}, false
	}
	return env, true
}

// pkgItem is one resolved declared entity in a contents section.
type pkgItem struct {
	Key       string `json:"key"`
	Name      string `json:"name,omitempty"`
	Class     string `json:"class,omitempty"`
	Found     bool   `json:"found"`
	IsDeleted bool   `json:"isDeleted,omitempty"`
	Aspects   int    `json:"aspects,omitempty"`
	// LensID carries the bare NanoID for a meta.lens item so the UI can link
	// the #/lens/<id> page in addition to the graph chip.
	LensID string `json:"lensId,omitempty"`
}

// computePackage assembles the GET /api/package response for one package
// vertex key from Core KV reads alone: the vertex envelope (installedAt), the
// .manifest aspect (name / version / declaredKeys), and one classification
// pass over the declared keys — vertex roots become section items (named via
// their .canonicalName aspect when one was declared), aspects fold into their
// parent item's count, links land in the grants section. A declared key that
// no longer resolves stays visible with found=false — never silently dropped.
func computePackage(key string, get kvGetter) map[string]any {
	root, ok := readPkgEnvelope(get, key)
	if !ok {
		return map[string]any{"error": "package vertex " + key + " not found"}
	}
	manifest, ok := readPkgEnvelope(get, key+".manifest")
	if !ok {
		return map[string]any{"error": "package " + key + " has no manifest aspect"}
	}

	declared := make([]string, 0)
	if arr, ok := manifest.Data["declaredKeys"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok && s != "" {
				declared = append(declared, s)
			}
		}
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, dk := range declared {
		declaredSet[dk] = struct{}{}
	}

	// One item per declared vertex root or link; aspect keys fold into their
	// parent root's count (an orphan aspect whose parent was not declared
	// stays its own item so nothing disappears).
	var roots []string
	aspectCount := map[string]int{}
	var orphans []string
	var links []string
	var unknowns []string
	for _, dk := range declared {
		if dk == key {
			continue // the install batch declares the package vertex itself — the page IS that vertex
		}
		switch classifyKey(dk) {
		case classVertex, classMeta:
			roots = append(roots, dk)
		case classAspect:
			parent := strings.Join(strings.Split(dk, ".")[:3], ".")
			if _, ok := declaredSet[parent]; ok {
				aspectCount[parent]++
			} else {
				orphans = append(orphans, dk)
			}
		case classLink:
			links = append(links, dk)
		default:
			unknowns = append(unknowns, dk)
		}
	}

	sections := map[string][]pkgItem{}
	unresolved := 0
	addItem := func(kind string, it pkgItem) {
		if !it.Found {
			unresolved++
		}
		sections[kind] = append(sections[kind], it)
	}

	for _, rk := range roots {
		env, found := readPkgEnvelope(get, rk)
		it := pkgItem{Key: rk, Found: found, Aspects: aspectCount[rk]}
		if !found {
			addItem("other", it)
			continue
		}
		it.Class = env.Class
		it.IsDeleted = env.IsDeleted
		it.Name = dataString(metaData(get, rk+".canonicalName"), "value", "name", "canonicalName")
		if it.Name == "" {
			it.Name = dataString(env.Data, "operationType", "name", "canonicalName", "value")
		}
		kind := pkgSectionFor(env.Class, env.Data)
		if kind == "lenses" {
			it.LensID = strings.TrimPrefix(rk, "vtx.meta.")
		}
		addItem(kind, it)
	}
	for _, lk := range links {
		env, found := readPkgEnvelope(get, lk)
		it := pkgItem{Key: lk, Found: found}
		if found {
			it.Class = env.Class
			it.IsDeleted = env.IsDeleted
		}
		addItem("grants", it)
	}
	for _, ok2 := range orphans {
		env, found := readPkgEnvelope(get, ok2)
		it := pkgItem{Key: ok2, Found: found}
		if found {
			it.Class = env.Class
			it.IsDeleted = env.IsDeleted
		}
		addItem("other", it)
	}
	for _, uk := range unknowns {
		addItem("other", pkgItem{Key: uk, Found: false})
	}

	out := make([]map[string]any, 0, len(pkgSectionOrder))
	for _, sec := range pkgSectionOrder {
		items := sections[sec.kind]
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool {
			ni, nj := items[i].Name, items[j].Name
			if ni != nj {
				return ni < nj
			}
			return items[i].Key < items[j].Key
		})
		out = append(out, map[string]any{
			"kind":  sec.kind,
			"label": sec.label,
			"items": items,
			"count": len(items),
		})
	}

	return map[string]any{
		"key":           key,
		"name":          dataString(manifest.Data, "name"),
		"version":       dataString(manifest.Data, "version"),
		"description":   dataString(manifest.Data, "description"),
		"installedAt":   root.CreatedAt,
		"isDeleted":     root.IsDeleted,
		"manifest":      manifest.Data,
		"sections":      out,
		"declaredCount": len(declared),
		"unresolved":    unresolved,
	}
}

// pkgSectionFor buckets a declared vertex by its envelope class (the shapes
// internal/pkgmgr's install batch writes). An op-meta shares the
// meta.ddl.vertexType class with entity DDLs and is told apart by the
// operationType it carries on the vertex data.
func pkgSectionFor(class string, data map[string]any) string {
	switch {
	case strings.HasPrefix(class, "meta.lens"):
		return "lenses"
	case class == "meta.weaverTarget" || class == "meta.loomPattern":
		return "orchestration"
	case strings.HasPrefix(class, "meta.ddl.aspect"):
		return "aspects"
	case strings.HasPrefix(class, "meta.ddl."):
		if s, ok := data["operationType"].(string); ok && s != "" {
			return "operations"
		}
		return "entities"
	case class == "role" || class == "roleindex":
		return "roles"
	case class == "permission":
		return "permissions"
	default:
		return "other"
	}
}

// handlePackage implements GET /api/package?key=vtx.package.<id>. It runs
// under pkgApplyTimeout, not the default request limit — a large package
// resolves one KVGet per declared key (~170 for the biggest shipped package),
// sequentially. A transport-level read failure fails the whole request (502)
// rather than letting half-resolved contents render as confident "not found
// in graph" rows — that state feeds the uninstall confirm, so it must never
// be a disguised timeout.
func (s *server) handlePackage(w http.ResponseWriter, r *http.Request) {
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	key := r.URL.Query().Get("key")
	if key == "" || !strings.HasPrefix(key, pkgmgr.PackageVertexPrefix) {
		s.writeError(w, http.StatusBadRequest, "key must be a vtx.package.<id> vertex key")
		return
	}
	ctx, cancel := s.pkgContext(r)
	defer cancel()
	var readErr error
	get := func(k string) ([]byte, bool) {
		if readErr != nil {
			return nil, false // already failing — don't pile on more reads
		}
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, k)
		if err != nil {
			if !errors.Is(err, substrate.ErrKeyNotFound) {
				readErr = err
			}
			return nil, false
		}
		return entry.Value, true
	}
	body := computePackage(key, get)
	if readErr != nil {
		s.writeError(w, http.StatusBadGateway, "read core-kv: "+readErr.Error())
		return
	}
	if errMsg, isErr := body["error"].(string); isErr {
		s.writeError(w, http.StatusNotFound, errMsg)
		return
	}
	s.writeJSON(w, http.StatusOK, body)
}

// manifestFromUpload picks the manifest file out of a multipart upload: an
// exact manifest.yaml / manifest.yml wins; a single uploaded file of any name
// is accepted (the operator picked just the manifest). Anything else is
// ambiguous.
func manifestFromUpload(files []*multipart.FileHeader) (*multipart.FileHeader, error) {
	if len(files) == 0 {
		return nil, errors.New("no files uploaded; select the package's manifest.yaml")
	}
	for _, fh := range files {
		base := strings.ToLower(fh.Filename)
		if base == "manifest.yaml" || base == "manifest.yml" {
			return fh, nil
		}
	}
	if len(files) == 1 {
		return files[0], nil
	}
	return nil, errors.New("multiple files uploaded but none named manifest.yaml")
}

// applyReply flattens an ApplyResult for the UI (lower-case JSON keys; key
// lists present only on a dry-run preview).
func applyReply(res *pkgmgr.ApplyResult) map[string]any {
	return map[string]any{
		"packageName":    res.PackageName,
		"packageKey":     res.PackageKey,
		"action":         res.Action,
		"fromVersion":    res.FromVersion,
		"toVersion":      res.ToVersion,
		"created":        res.Created,
		"updated":        res.Updated,
		"tombstoned":     res.Tombstoned,
		"skipped":        res.Skipped,
		"dryRun":         res.DryRun,
		"reason":         res.Reason,
		"createdKeys":    res.CreatedKeys,
		"updatedKeys":    res.UpdatedKeys,
		"tombstonedKeys": res.TombstonedKeys,
		"warnings":       res.DependencyWarnings,
	}
}

// requireRootAdmin gates the meta-lane pkg-lifecycle handlers (loupe-operator-
// auth-lift-design.md §4 mechanism B: "pkg-lifecycle tab is gated to a
// distinct root-admin path... or hidden for consoleOperators"). Because
// verifyOperatorToken only ever authenticates ONE identity per process
// (readauth.go:364 denies any subject other than s.operatorActorKey), any
// request that reached this handler already IS that configured operator —
// this just asks whether that operator is root-equivalent.
//
// Without this, packagesApply/handlePackagesUninstall would submit
// InstallPackage/UninstallPackage stamped as s.adminActor for ANY
// successfully-logged-in operator regardless of their own grants — a confused-
// deputy gap: Loupe performing a root-privileged action on behalf of a caller
// whose own identity wouldn't authorize it. It's latent today only because
// operatorActorKey defaults to adminActor (the same identity); re-scoping
// LOUPE_OPERATOR_ACTOR_KEY to a scoped consoleOperator (mechanism B) would
// otherwise silently reopen it.
//
// Root-equivalence is checked the same way the wildcard-read-grant lens and
// the write-side capability anchor both do: bootstrap.SystemActorKeys's
// bounded holdsRole→operator existence scan (Contract #7 §7.7) — never a
// package-vocabulary check, so this needs no consoleOperator-specific code
// and stays correct if a future mechanism grants root-equivalence some other
// way. Writes the error response itself and returns false on any failure
// (fail-closed: an errored root-check must never fall through to "allowed").
func (s *server) requireRootAdmin(w http.ResponseWriter, r *http.Request, conn *substrate.Conn) bool {
	if s.operatorActorKey == "" {
		s.writeError(w, http.StatusBadGateway, "no operator actor configured (LOUPE_OPERATOR_ACTOR_KEY unset and no bootstrap admin actor loaded)")
		return false
	}
	ctx, cancel := s.reqContext(r)
	defer cancel()
	roots, err := bootstrap.SystemActorKeys(ctx, conn)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "root-admin check: "+err.Error())
		return false
	}
	for _, k := range roots {
		if k == s.operatorActorKey {
			return true
		}
	}
	s.writeError(w, http.StatusForbidden,
		"pkg-lifecycle is a root-admin-only action; the configured operator is not root-equivalent (holds no holdsRole->operator link)")
	return false
}

// handlePackagesInstall implements POST /api/packages/install (multipart:
// files=manifest.yaml, force=, dryRun=). Upgrade-aware per pkgmgr.Apply:
// fresh install / same-version skip (force = dev refresh) / cross-version
// in-place upgrade.
func (s *server) handlePackagesInstall(w http.ResponseWriter, r *http.Request) {
	s.packagesApply(w, r, false)
}

// handlePackagesUpgrade implements POST /api/packages/upgrade — the same
// multipart Apply but a missing base install is an error rather than a fresh
// create (the detail page's re-submit semantics).
func (s *server) handlePackagesUpgrade(w http.ResponseWriter, r *http.Request) {
	s.packagesApply(w, r, true)
}

func (s *server) packagesApply(w http.ResponseWriter, r *http.Request, requireInstalled bool) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	if s.crossOriginBlocked(w, r) {
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if !s.requireRootAdmin(w, r, conn) {
		return
	}
	if s.adminActor == "" {
		s.writeError(w, http.StatusBadGateway,
			"admin actor not loaded; a valid bootstrap file (BOOTSTRAP_JSON_PATH) is required to install packages")
		return
	}
	// MaxBytesReader bounds the whole request body — ParseMultipartForm's
	// argument is only the in-memory share; overflow would otherwise spool
	// to disk without limit.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.writeError(w, http.StatusBadRequest, "parse multipart form: "+err.Error())
		return
	}
	var files []*multipart.FileHeader
	for _, fhs := range r.MultipartForm.File {
		files = append(files, fhs...)
	}
	fh, err := manifestFromUpload(files)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := fh.Open()
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "open upload: "+err.Error())
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, pkgManifestCap+1))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read upload: "+err.Error())
		return
	}
	if len(raw) > pkgManifestCap {
		// Reject rather than truncate — a cut-at-a-boundary YAML can parse
		// as a valid but different manifest.
		s.writeError(w, http.StatusBadRequest, "manifest exceeds 1MiB")
		return
	}
	manifest, err := pkgmgr.ParseManifestBytes(raw)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "parse manifest: "+err.Error())
		return
	}
	def, ok := packageRegistry[manifest.Name]
	if !ok {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"package %q is not in Loupe's compiled registry — a new package needs a registry row (cmd/loupe/pkg.go + cmd/lattice-pkg) and a rebuild", manifest.Name))
		return
	}
	if err := manifest.VerifyAgainstDefinition(def); err != nil {
		s.writeError(w, http.StatusBadRequest, "manifest does not match the compiled definition: "+err.Error())
		return
	}

	opts := pkgmgr.ApplyOptions{
		Force:            r.FormValue("force") == "true",
		DryRun:           r.FormValue("dryRun") == "true",
		RequireInstalled: requireInstalled,
	}
	inst := pkgmgr.NewInstaller(conn, s.adminActor)
	inst.RoleIDs = kernelRoleIDs()
	inst.Submit = s.pkgmgrSubmit

	ctx, cancel := s.pkgContext(r)
	defer cancel()
	res, err := inst.Apply(ctx, def, opts)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, pkgmgr.ErrNotInstalled) || errors.Is(err, pkgmgr.ErrCanonicalNameCollision) {
			status = http.StatusConflict
		}
		s.writeError(w, status, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, applyReply(res))
}

// handlePackagesUninstall implements POST /api/packages/uninstall with a JSON
// {"name": "<canonical package name>"} body. The typed confirm lives in the
// UI; the server just requires an exact name. Soft-delete only — the
// UninstallPackage op tombstones every declared key.
func (s *server) handlePackagesUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	if s.crossOriginBlocked(w, r) {
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if !s.requireRootAdmin(w, r, conn) {
		return
	}
	if s.adminActor == "" {
		s.writeError(w, http.StatusBadGateway,
			"admin actor not loaded; a valid bootstrap file (BOOTSTRAP_JSON_PATH) is required to uninstall packages")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}
	if req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	inst := pkgmgr.NewInstaller(conn, s.adminActor)
	inst.RoleIDs = kernelRoleIDs()
	inst.Submit = s.pkgmgrSubmit

	ctx, cancel := s.pkgContext(r)
	defer cancel()
	res, err := inst.Uninstall(ctx, req.Name)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"packageName": res.PackageName,
		"tombstoned":  res.Tombstoned,
		"note":        res.Note,
	})
}

// pkgContext bounds the package endpoint family by pkgApplyTimeout (it
// outlives the default per-request NATS limit: the installer's batch op has a
// 30s internal budget, and the detail read fans out one KVGet per declared
// key).
func (s *server) pkgContext(r *http.Request) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(r.Context(), pkgApplyTimeout)
}

// kernelRoleIDs maps kernel-seeded role canonical names to their NanoIDs for
// the installer's grant resolution (same sourcing as cmd/lattice-pkg's
// roleIDsFromBootstrap: the kernel seeds only the operator role; every other
// role is package-declared and minted deterministically at install time).
func kernelRoleIDs() map[string]string {
	if bootstrap.RoleOperatorID == "" {
		return nil
	}
	return map[string]string{"operator": bootstrap.RoleOperatorID}
}
