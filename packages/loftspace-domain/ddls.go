package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Canonical names. The vertexType DDL owns the op scripts; the two aspectType
// DDLs are step-6 write gates (the Processor keys permittedCommands on the
// MUTATION document's class, so the listing/address aspect each names its writer
// op — mirroring orchestration-base's freshnessMarker/freshnessExpiry split).
const (
	loftspaceListingDDL = "loftspaceListing"
	listingAspectDDL    = "listing"
	addressAspectDDL    = "address"
)

// DDLs returns the package's four DDL meta-vertex declarations:
//
//   - loftspaceListing (vertexType) — owns SetListing + SetUnitAddress.
//   - listing (aspectType) — declares the .listing aspect shape, admits SetListing.
//   - address (aspectType) — declares the .address aspect shape, admits SetUnitAddress.
//   - loftspaceOwnership (vertexType) — owns AssignUnitOwner + RemoveUnitOwner,
//     which write / tombstone the landlord→unit management link (D1.3).
//
// Architectural rules (binding — the same known-key discipline as
// location-domain / service-domain):
//
//   - The script reads ONLY by known key. SetListing / SetUnitAddress validate
//     their target unit by the key the caller lists in ContextHint.Reads. No
//     prefix scans, no adjacency lookups.
//   - The target MUST be an alive vtx.unit.<NanoID> of class=location (the place
//     graph's unit, owned by location-domain). A non-unit key, a dead vertex, or
//     a non-location class is rejected (structured ScriptError) — listing
//     economics attach only to a leasable unit.
//   - This package owns NO vertex type. The unit is minted by location-domain's
//     CreateLocation(locationType=unit); loftspace-domain only contributes the
//     .listing + .address aspects on top of it (the cross-package
//     aspect-contribution pattern — packages add aspects to vertices they do not
//     own, gated by the aspect-type DDL being installed).
//
// Both aspects are NON-sensitive: rent / address are not PII in the NFR-S3
// sense, and they attach to a vtx.unit (class=location), not an identity — so
// step-6's sensitiveAspectScope (which anchors sensitive aspects to identity
// vertices) must NOT fire. Applicant income / employment is the sensitive data;
// it lives on the identity (identity-domain), not here.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{loftspaceListingVertexDDL(), listingAspectTypeDDL(), addressAspectTypeDDL(), loftspaceOwnershipVertexDDL()}
}

func loftspaceListingVertexDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     loftspaceListingDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"SetListing", "SetUnitAddress", "SetListingStatus"},
		Description: "LoftSpace listing-economics DDL. Owns SetListing + SetUnitAddress + SetListingStatus, which " +
			"attach the leasable facets onto an EXISTING location unit (vtx.unit.<NanoID>, class=location, owned by " +
			"location-domain) — this package introduces NO vertex type. SetListing writes the .listing aspect " +
			"{rentAmount, rentCurrency, bedrooms, bathrooms?, sqft?, availableFrom (RFC3339 date), " +
			"leaseTermMonths, status ∈ available|pending|leased|withdrawn}. SetUnitAddress writes the .address aspect " +
			"{line1, line2?, city, region, postal}. SetListingStatus is a status-only transition: it reads the " +
			"existing .listing (kv.Read) and rewrites ONLY status, preserving the economics verbatim (rejects a " +
			"unit with no listing) — the op a lease-application's convergence directOp dispatches to mark a unit " +
			"leased on approval, and the op a landlord calls to take a unit off-market (withdrawn) or relist it " +
			"(available). All three are unconditioned upserts (create-if-absent / overwrite-if-present) so " +
			"an operator can correct a listing or flip status by hand. The target unit MUST be alive + " +
			"class=location; the caller lists the unit key in ContextHint.Reads. Neither aspect is sensitive (they " +
			"attach to a unit, not an identity).",
		Script: loftspaceListingDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"unit":{"type":"string","description":"vtx.unit.<NanoID> of an existing location unit (required; validated alive + class=location)."},` +
			`"rentAmount":{"type":"number","description":"Monthly rent (SetListing; required, > 0)."},` +
			`"rentCurrency":{"type":"string","description":"ISO currency code for rentAmount, e.g. USD (SetListing; required)."},` +
			`"bedrooms":{"type":"integer","description":"Bedroom count (SetListing; required, >= 0)."},` +
			`"bathrooms":{"type":"number","description":"Bathroom count, may be fractional e.g. 1.5 (SetListing; optional, >= 0)."},` +
			`"sqft":{"type":"integer","description":"Floor area in square feet (SetListing; optional, > 0)."},` +
			`"availableFrom":{"type":"string","description":"Earliest move-in date, RFC3339 (SetListing; required)."},` +
			`"leaseTermMonths":{"type":"integer","description":"Lease term in months (SetListing; required, > 0)."},` +
			`"status":{"type":"string","enum":["available","pending","leased","withdrawn"],"description":"Listing availability state (SetListing / SetListingStatus; required). 'withdrawn' = off-market (hidden from applicant Browse; relist by flipping back to 'available')."},` +
			`"line1":{"type":"string","description":"Street address line 1 (SetUnitAddress; required)."},` +
			`"line2":{"type":"string","description":"Street address line 2 (SetUnitAddress; optional)."},` +
			`"city":{"type":"string","description":"City (SetUnitAddress; required)."},` +
			`"region":{"type":"string","description":"State / province / region (SetUnitAddress; required)."},` +
			`"postal":{"type":"string","description":"Postal / ZIP code (SetUnitAddress; required)."}},` +
			`"required":["unit"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"The aspect key the operation wrote: vtx.unit.<NanoID>.listing (SetListing) or vtx.unit.<NanoID>.address (SetUnitAddress)."}}}`,
		FieldDescription: map[string]string{
			"unit":            "Full vtx.unit.<NanoID> key of an existing location unit. Both ops validate it is alive + class=location and write their aspect on it. The caller MUST list this key in ContextHint.Reads.",
			"rentAmount":      "Monthly rent as a number (> 0). Stored on the .listing aspect (SetListing).",
			"rentCurrency":    "ISO currency code (e.g. USD) for rentAmount. Stored on the .listing aspect (SetListing).",
			"bedrooms":        "Bedroom count, integer >= 0. Stored on the .listing aspect (SetListing).",
			"bathrooms":       "Optional bathroom count (number, may be fractional e.g. 1.5), >= 0. Stored on the .listing aspect when present (SetListing).",
			"sqft":            "Optional floor area in square feet (integer > 0). Stored on the .listing aspect when present (SetListing).",
			"availableFrom":   "Earliest move-in date, RFC3339. Stored verbatim on the .listing aspect (SetListing).",
			"leaseTermMonths": "Lease term in months (integer > 0). Stored on the .listing aspect (SetListing).",
			"status":          "Listing availability, one of {available, pending, leased, withdrawn}. 'withdrawn' takes the unit off-market (hidden from applicant Browse; relist via SetListingStatus status=available). Stored on the .listing aspect (SetListing sets it alongside the economics; SetListingStatus rewrites only this field, preserving the rest).",
			"line1":           "Street address line 1. Stored on the .address aspect (SetUnitAddress).",
			"line2":           "Optional street address line 2. Stored on the .address aspect when present (SetUnitAddress).",
			"city":            "City. Stored on the .address aspect (SetUnitAddress).",
			"region":          "State / province / region. Stored on the .address aspect (SetUnitAddress).",
			"postal":          "Postal / ZIP code. Stored on the .address aspect (SetUnitAddress).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "SetListing — publish a unit's listing economics",
				Payload: map[string]any{
					"unit":            "vtx.unit.<unitNanoID>",
					"rentAmount":      2400,
					"rentCurrency":    "USD",
					"bedrooms":        2,
					"bathrooms":       1.5,
					"sqft":            950,
					"availableFrom":   "2026-08-01T00:00:00Z",
					"leaseTermMonths": 12,
					"status":          "available",
				},
				ExpectedOutcome: "Validates the unit is alive + class=location, then writes vtx.unit.<unitNanoID>.listing " +
					"(class=listing) as an unconditioned upsert. Returns primaryKey (the listing aspect key). Re-running " +
					"with new values overwrites in place (e.g. flip status to leased). Rejects a non-unit key, a dead unit, " +
					"or a non-location vertex with a ScriptError.",
			},
			{
				Name: "SetUnitAddress — record a unit's street address",
				Payload: map[string]any{
					"unit":   "vtx.unit.<unitNanoID>",
					"line1":  "123 Market St",
					"city":   "San Francisco",
					"region": "CA",
					"postal": "94103",
				},
				ExpectedOutcome: "Validates the unit is alive + class=location, then writes vtx.unit.<unitNanoID>.address " +
					"(class=address) as an unconditioned upsert. Returns primaryKey (the address aspect key).",
			},
			{
				Name: "SetListingStatus — flip a unit's listing status (e.g. mark leased on approval)",
				Payload: map[string]any{
					"unit":   "vtx.unit.<unitNanoID>",
					"status": "leased",
				},
				ExpectedOutcome: "Validates the unit is alive + class=location, kv.Reads the existing .listing aspect " +
					"(rejects NoListing if absent), and rewrites ONLY status — preserving rentAmount / bedrooms / " +
					"availableFrom / … verbatim. Idempotent: a re-dispatch when status already equals the target is a " +
					"clean no-op (no mutation). This is the op the leaseApplicationComplete convergence target " +
					"dispatches as a directOp to mark a unit leased once its application is approved.",
			},
		},
	}
}

// listingAspectTypeDDL declares the .listing aspect-type DDL. It exists so
// step-6 — which keys permittedCommands on the mutation document's class
// (listing) — admits the SetListing-written aspect. Declaration-only: the
// SetListing script lives on loftspaceListing (the vertexType DDL); this DDL
// carries no op handler and fails closed if dispatched. NON-sensitive (it
// attaches to a unit, not an identity).
func listingAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     listingAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetListing", "SetListingStatus"},
		Description: "Listing-economics aspect (LoftSpace). Stored as vtx.unit.<NanoID>.listing = {rentAmount, " +
			"rentCurrency, bedrooms, bathrooms?, sqft?, availableFrom, leaseTermMonths, status}. Non-sensitive; " +
			"attaches to a location unit, not an identity. Written by SetListing (full upsert) and SetListingStatus " +
			"(status-only rewrite, preserving the rest) — both owned by the loftspaceListing vertexType DDL's " +
			"script; this aspect-type DDL exists so step-6's permittedCommands check, keyed on the mutation's " +
			"class, admits the write. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"rentAmount":{"type":"number"},"rentCurrency":{"type":"string"},"bedrooms":{"type":"integer"},` +
			`"bathrooms":{"type":"number"},"sqft":{"type":"integer"},"availableFrom":{"type":"string"},` +
			`"leaseTermMonths":{"type":"integer"},"status":{"type":"string","enum":["available","pending","leased"]}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"rentAmount":      "Monthly rent (number).",
			"rentCurrency":    "ISO currency code for rentAmount.",
			"bedrooms":        "Bedroom count.",
			"bathrooms":       "Bathroom count (may be fractional).",
			"sqft":            "Floor area in square feet.",
			"availableFrom":   "Earliest move-in date (RFC3339).",
			"leaseTermMonths": "Lease term in months.",
			"status":          "Availability: available | pending | leased.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "listing aspect",
				Payload:         map[string]any{"rentAmount": 2400, "rentCurrency": "USD", "bedrooms": 2, "availableFrom": "2026-08-01T00:00:00Z", "leaseTermMonths": 12, "status": "available"},
				ExpectedOutcome: "Stored as vtx.unit.<NanoID>.listing; written by SetListing as an unconditioned upsert.",
			},
		},
	}
}

// addressAspectTypeDDL declares the .address aspect-type DDL — the step-6 write
// gate for SetUnitAddress. Declaration-only; NON-sensitive.
func addressAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     addressAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetUnitAddress"},
		Description: "Unit street-address aspect (LoftSpace). Stored as vtx.unit.<NanoID>.address = {line1, line2?, " +
			"city, region, postal}. Non-sensitive; attaches to a location unit, not an identity. Written ONLY by " +
			"SetUnitAddress (whose loftspaceListing vertexType DDL owns the script); this aspect-type DDL is the " +
			"step-6 write gate. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"line1":{"type":"string"},"line2":{"type":"string"},"city":{"type":"string"},` +
			`"region":{"type":"string"},"postal":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"line1":  "Street address line 1.",
			"line2":  "Street address line 2 (optional).",
			"city":   "City.",
			"region": "State / province / region.",
			"postal": "Postal / ZIP code.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "address aspect",
				Payload:         map[string]any{"line1": "123 Market St", "city": "San Francisco", "region": "CA", "postal": "94103"},
				ExpectedOutcome: "Stored as vtx.unit.<NanoID>.address; written by SetUnitAddress as an unconditioned upsert.",
			},
		},
	}
}

// loftspaceListingDDLScript handles SetListing + SetUnitAddress. Known-key reads
// only: the target unit is validated by the key the caller lists in
// ContextHint.Reads. The target MUST be an alive vtx.unit.<NanoID> of
// class=location. Aspect writes are unconditioned upserts (create-if-absent /
// overwrite-if-present) so an operator can correct a listing or flip status.
const loftspaceListingDDLScript = `
def make_aspect_upsert(vtx_key, local_name, cls, data):
    # Unconditioned update: create-if-absent / overwrite-if-present. No
    # expectedRevision, so re-publishing a listing (e.g. status available->leased)
    # overwrites in place rather than conflicting.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty string when present")
    return v.strip()

def is_number(v):
    return type(v) == type(0) or type(v) == type(0.0)

def required_number(p, name, allow_zero):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or not is_number(v):
        fail("InvalidArgument: " + name + ": required number")
    if allow_zero:
        if v < 0:
            fail("InvalidArgument: " + name + ": must be >= 0")
    else:
        if v <= 0:
            fail("InvalidArgument: " + name + ": must be > 0")
    return v

def optional_number(p, name, allow_zero):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if not is_number(v):
        fail("InvalidArgument: " + name + ": must be a number when present")
    if allow_zero:
        if v < 0:
            fail("InvalidArgument: " + name + ": must be >= 0")
    else:
        if v <= 0:
            fail("InvalidArgument: " + name + ": must be > 0")
    return v

LISTING_STATUSES = ["available", "pending", "leased", "withdrawn"]

def required_status(p):
    s = required_string(p, "status")
    if s not in LISTING_STATUSES:
        fail("InvalidArgument: status: must be one of available, pending, leased, withdrawn; got " + s)
    return s

def copy_data(d):
    # Shallow-copy a kv.Read .data dict (a real Starlark dict, iterable by key) so
    # a status-only rewrite preserves every other listing field verbatim.
    out = {}
    for k in d:
        out[k] = d[k]
    return out

def unit_parts(key):
    # Parse the target as a UNIT vertex key: exactly 3 segments vtx.unit.<NanoID>.
    # A non-3-segment key (aspect/link), or a type segment other than "unit", is
    # rejected — listing economics attach only to a leasable unit.
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "unit":
        fail("InvalidArgument: unit: required vtx.unit.<NanoID> (exactly 3 segments); got " + key)
    if parts[2] == "":
        fail("InvalidArgument: unit: empty id segment; required vtx.unit.<NanoID>; got " + key)
    return parts[2]

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_unit(state, key):
    # The target MUST be alive AND class=location (location-domain's unit). A
    # dead or non-location vertex never receives listing economics.
    if key not in state or state[key] == None:
        fail("UnknownUnit: unit: " + key + " is absent")
    doc = state[key]
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        fail("UnknownUnit: unit: " + key + " is tombstoned")
    cls = class_of(state, key)
    if cls != "location":
        fail("NotAUnit: unit: " + key + " has class " + str(cls) + ", required location")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "SetListing":
        unit = required_string(p, "unit")
        unit_parts(unit)
        require_live_unit(state, unit)

        data = {
            "rentAmount": required_number(p, "rentAmount", False),
            "rentCurrency": required_string(p, "rentCurrency"),
            "bedrooms": required_number(p, "bedrooms", True),
            "availableFrom": required_string(p, "availableFrom"),
            "leaseTermMonths": required_number(p, "leaseTermMonths", False),
            "status": required_status(p),
        }
        bathrooms = optional_number(p, "bathrooms", True)
        if bathrooms != None:
            data["bathrooms"] = bathrooms
        sqft = optional_number(p, "sqft", False)
        if sqft != None:
            data["sqft"] = sqft

        listing_key = unit + ".listing"
        mutations = [make_aspect_upsert(unit, "listing", "listing", data)]
        events = [{"class": "loftspace.listingSet",
                   "data": {"unit": unit, "status": data["status"]}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": listing_key}}

    if ot == "SetUnitAddress":
        unit = required_string(p, "unit")
        unit_parts(unit)
        require_live_unit(state, unit)

        data = {
            "line1": required_string(p, "line1"),
            "city": required_string(p, "city"),
            "region": required_string(p, "region"),
            "postal": required_string(p, "postal"),
        }
        line2 = optional_string(p, "line2")
        if line2 != None:
            data["line2"] = line2

        address_key = unit + ".address"
        mutations = [make_aspect_upsert(unit, "address", "address", data)]
        events = [{"class": "loftspace.unitAddressSet",
                   "data": {"unit": unit}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": address_key}}

    if ot == "SetListingStatus":
        # Status-only transition: rewrite ONLY .listing.status, preserving the
        # economics. The directOp the leaseApplicationComplete convergence target
        # dispatches to mark a unit leased once its application is approved; also
        # operator-callable by hand (a landlord-driven revert leased->available is
        # allowed — convergence only ever drives it to leased). The unit root is
        # hydrated via ContextHint.Reads=[unit] (the playbook routes row.unitKey).
        unit = required_string(p, "unit")
        unit_parts(unit)
        require_live_unit(state, unit)
        status = required_status(p)

        # Read the existing .listing on demand (kv.Read, §2.5) — the directOp
        # declares reads=[unit] only, so the aspect is NOT in state. A status
        # transition needs a listing to transition: a unit with none is rejected
        # (NoListing) rather than minting a bare {status}-only listing.
        # read-posture: (a) declared reads at SetListingStatus dispatch (both
        # the FE + the leaseApplicationComplete directOp declare unit.listing;
        # script-read-posture-design.md §13 hard case 4).
        listing_key = unit + ".listing"
        existing = kv.Read(listing_key)
        if existing == None or existing.isDeleted:
            fail("NoListing: unit " + unit + " has no listing to transition")

        # Idempotent no-op: an at-least-once re-dispatch when the listing already
        # holds the target status emits NOTHING — no mutation, no event, no CDC
        # churn (the convergence gap is already closed). primaryKey is omitted: the
        # reply-constraint requires a non-empty primaryKey to be a committed
        # mutation key, and a no-op commits none.
        if existing.data.get("status") == status:
            return {"mutations": [], "events": [], "response": {}}

        # Rewrite preserving every other economics field verbatim (unconditioned
        # upsert, the SetListing idiom — last-write-wins with a racing SetListing,
        # which self-heals: if a SetListing loses leased the gap re-opens and
        # Weaver re-dispatches while the application stays approved).
        data = copy_data(existing.data)
        data["status"] = status
        mutations = [make_aspect_upsert(unit, "listing", "listing", data)]
        events = [{"class": "loftspace.listingStatusSet",
                   "data": {"unit": unit, "status": status}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": listing_key}}

    fail("loftspaceListing DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the declaration-only Starlark for the listing /
// address aspect-type DDLs. The aspects are written by the loftspaceListing
// vertexType DDL's ops; these aspect-type DDLs are step-6 write gates only,
// never op handlers — they fail closed if dispatched.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
