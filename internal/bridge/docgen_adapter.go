package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// leaseDocStoreNamespace seeds the deterministic object store name for an
// application's executed-lease artifact: derived from the leaseapp key alone,
// so a re-render for the same application overwrites the SAME store object
// rather than leaking an orphan blob per call.
const leaseDocStoreNamespace = "loftspace:lease-doc:store:"

// leaseDocContentType is the produced artifact's MIME type, echoed on the
// document-pointer set the replyOp records.
const leaseDocContentType = "text/plain; charset=utf-8"

// docGenParams is the external.docGen event's params payload: the family
// discriminator, the application the document is about, and the resolved
// document fields the instanceOp assembled Processor-side. Read from
// Request.RawParams (the doc object and its numeric fields do not survive the
// flat string-map coercion).
type docGenParams struct {
	Family      string       `json:"family"`
	LeaseAppKey string       `json:"leaseAppKey"`
	Doc         docGenFields `json:"doc"`
}

// docGenFields are the resolved document inputs. Every field is optional except
// SignedAt (an executed lease exists only for a signed application — the
// instanceOp gates on it, and the renderer refuses without it): the renderer
// emits only present fields, so an application missing optional terms degrades
// to whatever it carries. Numeric fields are pointers so an absent value stays
// absent rather than rendering a misleading 0.
type docGenFields struct {
	TenantName           string   `json:"tenantName"`
	Applicant            string   `json:"applicant"`
	UnitKey              string   `json:"unitKey"`
	UnitAddress          string   `json:"unitAddress"`
	UnitCity             string   `json:"unitCity"`
	UnitRegion           string   `json:"unitRegion"`
	UnitRent             *float64 `json:"unitRent"`
	UnitCurrency         string   `json:"unitCurrency"`
	UnitBedrooms         *float64 `json:"unitBedrooms"`
	UnitBathrooms        *float64 `json:"unitBathrooms"`
	UnitLeaseTermMonths  *float64 `json:"unitLeaseTermMonths"`
	UnitAvailableFrom    string   `json:"unitAvailableFrom"`
	TermsMoveInDate      string   `json:"termsMoveInDate"`
	TermsLeaseTermMonths *float64 `json:"termsLeaseTermMonths"`
	TermsRequestedRent   *float64 `json:"termsRequestedRent"`
	SignedAt             string   `json:"signedAt"`
}

// docPointer is the completed Result's Detail payload — the produced artifact's
// reference metadata, JSON-encoded into the free-form Detail string. The
// RecordLeaseDocOutcome replyOp parses it onto the claim's .outcome aspect,
// from which the convergence lens projects the columns Weaver's AttachObject
// dispatch templates.
type docPointer struct {
	Digest      string `json:"digest"`
	Size        uint64 `json:"size"`
	ContentType string `json:"contentType"`
	StoreName   string `json:"storeName"`
	Filename    string `json:"filename"`
}

// FakeDocGen is the reference external legal-document vendor (the docGen
// adapter) — the mock of a jurisdiction-aware document-generation service, the
// same posture as FakeStripe / FakeBackgroundCheck (docs/components/bridge.md:
// real vendor integrations are Phase 3; the fake stays as the CI/e2e
// reference). Unlike the pure in-memory fakes it performs a REAL byte-plane
// side-effect: it renders the executed-lease text from the resolved document
// fields the instanceOp assembled and ObjectPuts the bytes into the
// core-objects store under a deterministic, application-derived store name (a
// re-render overwrites, never orphans). The bytes are inert until an
// AttachObject op anchors them (Weaver's missing_leaseDocAttach dispatch).
//
// Idempotency: the ObjectPut is the external side-effect, memo-deduped per
// idempotencyKey — a repeat Execute for the same key returns the first call's
// Result without a second put. A render with missing REQUIRED inputs (no
// leaseAppKey / no signedAt) is a terminal OutcomeFailed with the reason in
// Detail (a definitive business rejection, not an error); a store write
// failure is a returned error (transient — the bridge re-drives on the same
// key and the retry re-puts under the same store name).
type FakeDocGen struct {
	conn      *substrate.Conn
	bucket    string
	uploadCap int64

	mu sync.Mutex
	// results memoizes the Result per idempotencyKey so a repeat key returns
	// the first call's result verbatim.
	results map[string]Result
	// calls counts the byte-plane side-effects actually performed per
	// idempotencyKey — the idempotency assertion: a repeat key leaves it at 1.
	calls map[string]int
}

// NewFakeDocGen returns the reference docGen adapter over conn, writing into
// the named object-store bucket with putCap bounding a single artifact's size.
func NewFakeDocGen(conn *substrate.Conn, bucket string, putCap int64) *FakeDocGen {
	return &FakeDocGen{
		conn:      conn,
		bucket:    bucket,
		uploadCap: putCap,
		results:   make(map[string]Result),
		calls:     make(map[string]int),
	}
}

// Execute renders + stores the executed-lease artifact exactly once per
// idempotencyKey. It is synchronous: always a Resolved Dispatch (a terminal
// Result inline, never Pending).
func (f *FakeDocGen) Execute(ctx context.Context, req Request) (Dispatch, error) {
	f.mu.Lock()
	if res, seen := f.results[req.IdempotencyKey]; seen {
		f.mu.Unlock()
		return Dispatch{Disposition: Resolved, Result: res}, nil
	}
	f.mu.Unlock()

	var params docGenParams
	if len(req.RawParams) > 0 {
		if err := json.Unmarshal(req.RawParams, &params); err != nil {
			return f.memoize(req.IdempotencyKey,
				Result{Status: OutcomeFailed, Detail: "lease-doc render failed: unparseable params: " + err.Error()}), nil
		}
	}
	// Terminal render rejections: a document cannot exist without the
	// application it is about, or before that application is signed. Business
	// verdicts (err == nil), never retried.
	if strings.TrimSpace(params.LeaseAppKey) == "" {
		return f.memoize(req.IdempotencyKey,
			Result{Status: OutcomeFailed, Detail: "lease-doc render failed: params.leaseAppKey is required"}), nil
	}
	if strings.TrimSpace(params.Doc.SignedAt) == "" {
		return f.memoize(req.IdempotencyKey,
			Result{Status: OutcomeFailed, Detail: "lease-doc render failed: doc.signedAt is required (unsigned application)"}), nil
	}

	content := renderLeaseDocument(params.LeaseAppKey, params.Doc)
	storeName := substrate.DeriveNanoID(leaseDocStoreNamespace, params.LeaseAppKey)
	info, err := f.conn.ObjectPut(ctx, f.bucket, storeName, strings.NewReader(content), f.uploadCap)
	if err != nil {
		// Transient store failure: surface the error so the bridge NakWithDelays
		// and re-drives on the same idempotencyKey (the retry overwrites the same
		// deterministic store name — no orphan). Nothing is memoized.
		return Dispatch{}, fmt.Errorf("bridge: docGen store lease document bytes: %w", err)
	}

	ptr := docPointer{
		Digest:      info.Digest,
		Size:        info.Size,
		ContentType: leaseDocContentType,
		StoreName:   storeName,
		Filename:    "signed-lease-" + shortKey(params.LeaseAppKey) + ".txt",
	}
	detail, err := json.Marshal(ptr)
	if err != nil {
		return Dispatch{}, fmt.Errorf("bridge: docGen marshal document pointer: %w", err)
	}

	f.mu.Lock()
	f.calls[req.IdempotencyKey]++
	f.mu.Unlock()
	return f.memoize(req.IdempotencyKey, Result{Status: OutcomeCompleted, Detail: string(detail)}), nil
}

// memoize records the terminal Result for key and returns its Resolved
// Dispatch. A concurrent first-writer wins: if another Execute memoized the key
// in the window since the entry check, that earlier Result is returned so a
// repeat key never observes two different results.
func (f *FakeDocGen) memoize(key string, res Result) Dispatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	if prior, seen := f.results[key]; seen {
		return Dispatch{Disposition: Resolved, Result: prior}
	}
	f.results[key] = res
	return Dispatch{Disposition: Resolved, Result: res}
}

// Poll is unreachable for this synchronous adapter (Execute never returns
// Pending, so the bridge never holds a Ref to poll). It returns a clear error
// so a wiring mistake surfaces rather than silently resolving.
func (f *FakeDocGen) Poll(_ context.Context, ref string) (Dispatch, error) {
	return Dispatch{}, fmt.Errorf("bridge: FakeDocGen is synchronous: Poll unsupported (ref %q)", ref)
}

// SideEffects reports how many times the byte-plane write was performed for
// idempotencyKey — 0 before the first Execute (and for a failed render, which
// writes nothing), exactly 1 no matter how many repeat Executes follow.
func (f *FakeDocGen) SideEffects(idempotencyKey string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[idempotencyKey]
}

// renderLeaseDocument renders the executed-lease text from the resolved
// document fields — the reference vendor's renderer. It is deterministic:
// every line is drawn from the supplied fields, with no clock read, so the
// same inputs always render byte-identical bytes (the basis for the
// idempotent, orphan-free attach — identical bytes map to one digest/oid).
// Only present fields are emitted, so an application missing optional terms
// degrades to whatever it carries rather than printing blanks; an unnamed
// applicant renders by their bare identity key.
func renderLeaseDocument(leaseAppKey string, doc docGenFields) string {
	var b strings.Builder
	line := func(label, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		fmt.Fprintf(&b, "%-16s%s\n", label+":", value)
	}

	b.WriteString("RESIDENTIAL LEASE AGREEMENT\n")
	b.WriteString("===========================\n\n")
	b.WriteString("Executed electronically through LoftSpace.\n\n")

	b.WriteString("PARTIES & PREMISES\n")
	b.WriteString("------------------\n")
	tenant := doc.TenantName
	if tenant == "" {
		tenant = doc.Applicant
	}
	line("Tenant", tenant)
	if doc.TenantName != "" && doc.Applicant != "" {
		line("Tenant ID", doc.Applicant)
	}
	line("Landlord", "LoftSpace property management")
	fullAddr := joinNonEmpty(", ", doc.UnitAddress, doc.UnitCity, doc.UnitRegion)
	line("Premises", fullAddr)
	line("Unit", doc.UnitKey)

	b.WriteString("\nLEASE TERMS\n")
	b.WriteString("-----------\n")
	line("Monthly rent", renderRent(doc))
	if term := leaseTermMonths(doc); term != "" {
		line("Lease term", term+" months")
	}
	if moveIn := firstNonEmpty(doc.TermsMoveInDate, doc.UnitAvailableFrom); moveIn != "" {
		line("Move-in date", moveIn)
	}
	if doc.UnitBedrooms != nil {
		line("Bedrooms", trimFloat(*doc.UnitBedrooms))
	}
	if doc.UnitBathrooms != nil {
		line("Bathrooms", trimFloat(*doc.UnitBathrooms))
	}

	b.WriteString("\nEXECUTION\n")
	b.WriteString("---------\n")
	line("Signed by", tenant)
	line("Signed on", doc.SignedAt)
	line("Application", leaseAppKey)

	b.WriteString("\nThis document is a system-generated summary of the executed lease\n")
	b.WriteString("application and is provided for the records of both parties.\n")
	return b.String()
}

// renderRent formats the monthly rent with its currency: the bare-dollar form
// for USD / unspecified, "<amount> <CUR>" otherwise. The applicant's requested
// rent is noted when it differs from the listing ask.
func renderRent(doc docGenFields) string {
	if doc.UnitRent == nil {
		if doc.TermsRequestedRent != nil {
			return trimFloat(*doc.TermsRequestedRent) + " (applicant offer)"
		}
		return ""
	}
	cur := strings.TrimSpace(doc.UnitCurrency)
	base := "$" + trimFloat(*doc.UnitRent)
	if cur != "" && cur != "USD" {
		base = trimFloat(*doc.UnitRent) + " " + cur
	}
	if doc.TermsRequestedRent != nil && *doc.TermsRequestedRent != *doc.UnitRent {
		base += " (applicant offered " + trimFloat(*doc.TermsRequestedRent) + ")"
	}
	return base
}

// leaseTermMonths picks the applicant's requested term, falling back to the
// listing's term.
func leaseTermMonths(doc docGenFields) string {
	if doc.TermsLeaseTermMonths != nil {
		return trimFloat(*doc.TermsLeaseTermMonths)
	}
	if doc.UnitLeaseTermMonths != nil {
		return trimFloat(*doc.UnitLeaseTermMonths)
	}
	return ""
}

// trimFloat renders a numeric field without a trailing ".0" for whole numbers
// (12 not 12.0) while preserving any genuine fraction.
func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// firstNonEmpty returns the first non-blank string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// joinNonEmpty joins the non-blank parts with sep.
func joinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// shortKey abbreviates a vtx.<type>.<id> key to <type>.<id-prefix> for
// human-facing filenames, mirroring the vertical FE's shortKey.
func shortKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) >= 3 {
		id := parts[2]
		if len(id) > 8 {
			id = id[:8]
		}
		return parts[1] + "." + id
	}
	return key
}
