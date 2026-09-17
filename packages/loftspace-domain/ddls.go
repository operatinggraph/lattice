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
//   - loftspaceListing (vertexType) — owns SetListing + SetUnitAddress +
//     SetListingStatus + FloorListingAvailability.
//   - listing (aspectType) — declares the .listing aspect shape, admits the
//     three .listing writers (SetListing, SetListingStatus,
//     FloorListingAvailability).
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
//   - The target MUST be an alive vtx.unit.<NanoID> (the place
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
// sense, and they attach to a vtx.unit, not an identity — so
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
		PermittedCommands: []string{"SetListing", "SetUnitAddress", "SetListingStatus", "FloorListingAvailability"},
		Description: "LoftSpace listing-economics DDL. Owns SetListing + SetUnitAddress + SetListingStatus + FloorListingAvailability, which " +
			"attach the leasable facets onto an EXISTING location unit (vtx.unit.<NanoID>, owned by " +
			"location-domain) — this package introduces NO vertex type. SetListing writes the .listing aspect " +
			"{rentAmount, rentCurrency, bedrooms, bathrooms?, sqft?, availableFrom (an RFC3339 instant or a bare " +
			"YYYY-MM-DD read as midnight UTC, STORED NORMALIZED in canonical RFC3339 UTC form; anything else is refused " +
			"InvalidArgument), " +
			"leaseTermMonths, depositAmount?, status ∈ available|pending|leased|withdrawn}. SetUnitAddress writes the .address aspect " +
			"{line1, line2?, city, region, postal}. SetListingStatus is a status-only transition: it reads the " +
			"existing .listing (kv.Read) and rewrites ONLY status, preserving the economics verbatim (rejects a " +
			"unit with no listing) — the op a lease-application's convergence directOp dispatches to mark a unit " +
			"leased on approval, and the op a landlord calls to take a unit off-market (withdrawn) or relist it " +
			"(available). FloorListingAvailability{unit, availableFrom} is an availability floor: it reads the existing " +
			".listing (rejects NoListing), normalizes both the stored availableFrom and the payload's to a canonical " +
			"RFC3339 UTC instant (a bare YYYY-MM-DD anchors to midnight UTC), and rewrites availableFrom to the LATER of " +
			"the two, preserving every other field verbatim — a no-op when the result equals the stored string, so a " +
			"landlord's later date is never lowered and a re-dispatch writes nothing. It is the op lease-signing's " +
			"tenancyEnd convergence target dispatches so a unit is never marketed from a day its tenancy's recorded " +
			"end (endedAt, or a notice's move-out) still covers. All four are unconditioned upserts (create-if-absent / " +
			"overwrite-if-present) so an operator can correct a listing, flip status or floor a date by hand. The first " +
			"three also carry a consumer scope=self grant — the landlord path — and on that validated self path every " +
			"one of them requires the acting " +
			"identity's own manages link to the payload unit (lnk.identity.<actor>.manages.unit.<unit>, declared as an " +
			"optionalRead) BEFORE the unit's liveness is checked, so the script's own answer never reveals whether a unit exists. " +
			"The target unit MUST be alive + " +
			"a vtx.unit.<NanoID> key; the caller lists the unit key in ContextHint.Reads. Neither aspect is sensitive (they " +
			"attach to a unit, not an identity).",
		Script: loftspaceListingDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"unit":{"type":"string","description":"vtx.unit.<NanoID> of an existing location unit (required; validated alive + a vtx.unit.<NanoID> key)."},` +
			`"rentAmount":{"type":"number","description":"Monthly rent (SetListing; required, > 0, at most two decimals — the ledger keeps whole cents)."},` +
			`"rentCurrency":{"type":"string","description":"ISO currency code for rentAmount, e.g. USD (SetListing; required)."},` +
			`"bedrooms":{"type":"integer","description":"Bedroom count (SetListing; required, >= 0)."},` +
			`"bathrooms":{"type":"number","description":"Bathroom count, may be fractional e.g. 1.5 (SetListing; optional, >= 0)."},` +
			`"sqft":{"type":"integer","description":"Floor area in square feet (SetListing; optional, > 0)."},` +
			`"availableFrom":{"type":"string","description":"Earliest move-in date — an RFC3339 instant or a bare YYYY-MM-DD read as midnight UTC (SetListing; required; stored normalized in canonical RFC3339 UTC form, anything else refused InvalidArgument). For FloorListingAvailability (required): the instant the listing's date is raised to when the stored date is earlier, the same two shapes admitted."},` +
			`"leaseTermMonths":{"type":"integer","description":"Lease term in months (SetListing; required, > 0)."},` +
			`"depositAmount":{"type":"number","description":"Security deposit, a number > 0 with at most two decimals in the listing's currency (SetListing; optional; absent = the unit takes no deposit)."},` +
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
			"unit":            "Full vtx.unit.<NanoID> key of an existing location unit. Both ops validate it is alive + a vtx.unit.<NanoID> key and write their aspect on it. The caller MUST list this key in ContextHint.Reads.",
			"rentAmount":      "Monthly rent as a number (> 0, at most two decimals — InvalidArgument otherwise; the ledger keeps whole cents). Stored on the .listing aspect (SetListing).",
			"rentCurrency":    "ISO currency code (e.g. USD) for rentAmount. Stored on the .listing aspect (SetListing).",
			"bedrooms":        "Bedroom count, integer >= 0. Stored on the .listing aspect (SetListing).",
			"bathrooms":       "Optional bathroom count (number, may be fractional e.g. 1.5), >= 0. Stored on the .listing aspect when present (SetListing).",
			"sqft":            "Optional floor area in square feet (integer > 0). Stored on the .listing aspect when present (SetListing).",
			"availableFrom":   "Earliest move-in date — an RFC3339 instant or a bare YYYY-MM-DD (read as midnight UTC). SetListing stores it NORMALIZED to canonical RFC3339 UTC form (whole seconds, Z) and refuses any other shape (InvalidArgument). FloorListingAvailability raises the stored date to this instant when the stored one is earlier (the same two shapes admitted; the stored value is rewritten in canonical form), and never lowers it.",
			"leaseTermMonths": "Lease term in months (integer > 0). Stored on the .listing aspect (SetListing).",
			"depositAmount":   "Security deposit, a number > 0 with at most two decimals in the listing's currency (InvalidArgument otherwise). Absent = the unit takes no deposit. Stored on the .listing aspect when present (SetListing REPLACES the stored value on every write — a re-submit without it clears it).",
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
					"depositAmount":   2400,
					"status":          "available",
				},
				ExpectedOutcome: "Validates the unit is alive + a vtx.unit.<NanoID> key, then writes vtx.unit.<unitNanoID>.listing " +
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
				ExpectedOutcome: "Validates the unit is alive + a vtx.unit.<NanoID> key, then writes vtx.unit.<unitNanoID>.address " +
					"(class=address) as an unconditioned upsert. Returns primaryKey (the address aspect key).",
			},
			{
				Name: "SetListingStatus — flip a unit's listing status (e.g. mark leased on approval)",
				Payload: map[string]any{
					"unit":   "vtx.unit.<unitNanoID>",
					"status": "leased",
				},
				ExpectedOutcome: "Validates the unit is alive + a vtx.unit.<NanoID> key, kv.Reads the existing .listing aspect " +
					"(rejects NoListing if absent), and rewrites ONLY status — preserving rentAmount / bedrooms / " +
					"availableFrom / … verbatim. Idempotent: a re-dispatch when status already equals the target is a " +
					"clean no-op (no mutation). This is the op the leaseApplicationComplete convergence target " +
					"dispatches as a directOp to mark a unit leased once its application is approved.",
			},
			{
				Name: "FloorListingAvailability — raise a unit's availability to a tenancy's recorded end",
				Payload: map[string]any{
					"unit":          "vtx.unit.<unitNanoID>",
					"availableFrom": "2027-01-31T00:00:00Z",
				},
				ExpectedOutcome: "Validates the unit is alive + a vtx.unit.<NanoID> key, kv.Reads the existing .listing aspect " +
					"(rejects NoListing if absent), and rewrites availableFrom to the LATER of the stored date and the " +
					"payload's — both read as RFC3339 UTC instants, a bare YYYY-MM-DD as midnight UTC — preserving " +
					"rentAmount / bedrooms / status / … verbatim. A no-op (no mutation, no event) when the result equals " +
					"the stored string: a landlord's later date is never lowered. This is the op the tenancyEnd " +
					"convergence target dispatches as a directOp once a term's recorded end sits after the listing's date.",
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
		PermittedCommands: []string{"SetListing", "SetListingStatus", "FloorListingAvailability"},
		Description: "Listing-economics aspect (LoftSpace). Stored as vtx.unit.<NanoID>.listing = {rentAmount, " +
			"rentCurrency, bedrooms, bathrooms?, sqft?, availableFrom, leaseTermMonths, depositAmount?, status}. Non-sensitive; " +
			"attaches to a location unit, not an identity. depositAmount is the security deposit, a number > 0 in the " +
			"listing's currency; absent = no deposit. Written by SetListing (full upsert), SetListingStatus " +
			"(status-only rewrite, preserving the rest) and FloorListingAvailability (availableFrom raised to a " +
			"tenancy's recorded end, never lowered, preserving the rest) — all owned by the loftspaceListing vertexType DDL's " +
			"script; this aspect-type DDL exists so step-6's permittedCommands check, keyed on the mutation's " +
			"class, admits the write. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"rentAmount":{"type":"number"},"rentCurrency":{"type":"string"},"bedrooms":{"type":"integer"},` +
			`"bathrooms":{"type":"number"},"sqft":{"type":"integer"},"availableFrom":{"type":"string"},` +
			`"leaseTermMonths":{"type":"integer"},"depositAmount":{"type":"number"},"status":{"type":"string","enum":["available","pending","leased"]}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"rentAmount":      "Monthly rent (number).",
			"rentCurrency":    "ISO currency code for rentAmount.",
			"bedrooms":        "Bedroom count.",
			"bathrooms":       "Bathroom count (may be fractional).",
			"sqft":            "Floor area in square feet.",
			"availableFrom":   "Earliest move-in date, canonical RFC3339 UTC (SetListing normalizes a bare YYYY-MM-DD to midnight UTC and an offset instant to UTC). Floored to a tenancy's recorded end by FloorListingAvailability.",
			"leaseTermMonths": "Lease term in months.",
			"depositAmount":   "Security deposit, a number > 0 in the listing's currency; absent = no deposit.",
			"status":          "Availability: available | pending | leased.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "listing aspect",
				Payload:         map[string]any{"rentAmount": 2400, "rentCurrency": "USD", "bedrooms": 2, "availableFrom": "2026-08-01T00:00:00Z", "leaseTermMonths": 12, "depositAmount": 2400, "status": "available"},
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

// loftspaceListingDDLScript handles SetListing + SetUnitAddress +
// SetListingStatus + FloorListingAvailability. Known-key reads
// only: the target unit is validated by the key the caller lists in
// ContextHint.Reads. The target MUST be an alive vtx.unit.<NanoID> of
// a vtx.unit.<NanoID> key. Aspect writes are unconditioned upserts (create-if-absent /
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

def two_decimals(v, name):
    # A dollar amount carries at most two decimals: v × 100 must sit within a
    # millionth of an integer. The ledger keeps integer cents, and a figure
    # with a fractional cent here would become a clause the reader refuses on
    # every convergence pass — so it is refused once, at the source.
    scaled = v * 100
    nearest = int(scaled + 0.5)
    d = scaled - nearest
    if d < 0:
        d = -d
    if d > 0.000001:
        fail("InvalidArgument: " + name + ": at most two decimal places")
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

def as_rfc3339_instant(s):
    # A bare "YYYY-MM-DD" (the FE's <input type=date> shape, and what the seed
    # scripts write for availableFrom) anchors to midnight UTC.
    # time.rfc3339_utc itself rejects a bare date, so every caller normalizes
    # through this first; an already-RFC3339 value passes through unchanged.
    if len(s) == 10:
        return s + "T00:00:00Z"
    return s

def required_instant(p, name):
    # A required instant field: a bare "YYYY-MM-DD" (read as midnight UTC) or
    # an RFC3339 instant, returned in time.rfc3339_utc's canonical UTC form
    # (whole seconds, "Z") so the stored value orders lexically against every
    # other canonical stamp — the tenancyEnd lens compares it as a string
    # against a term's recorded end, and FloorListingAvailability parses it on
    # every dispatch, so a free-text value here would be a gap that never
    # closes. The shape is checked first so the refusal names the FIELD (the
    # lease-signing required_date_instant idiom); a well-shaped date the
    # calendar rejects is still the parser's own InvalidArgument.
    v = required_string(p, name)
    s = as_rfc3339_instant(v)
    digits = s[0:4] + s[5:7] + s[8:10]
    if len(s) < 20 or s[4] != "-" or s[7] != "-" or s[10] != "T" or not digits.isdigit():
        fail("InvalidArgument: " + name + ": must be an RFC3339 instant or YYYY-MM-DD, got " + v)
    return time.rfc3339_utc(s)

def parts_of(key, name, want_type):
    # Parse a VERTEX key: exactly 3 segments vtx.<type>.<NanoID>. A non-3-segment
    # key (aspect/link) is rejected, not silently truncated. The unit callers pass
    # want_type "unit" — listing economics attach only to a leasable unit.
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def require_manages(unit_key, what):
    # The landlord ownership probe: a signed-in landlord authorizes a listing
    # write (economics, address, or a status transition) via a scope=self
    # grant, and what confines them is their management link to the unit
    # under the write.
    #
    # It binds the platform-VALIDATED self path and only that path. The
    # convergence directOp that drives a unit to leased runs as Weaver's service
    # actor and carries no authContext at all; an operator on the standing
    # scope=any path is authorized without the target being inspected, so
    # neither reaches the probe even if a client sends a target.
    #
    # authcontext-target: (ownership) the target is used only as op.actor's own
    # key, on a path the platform already proved equal to the actor, and the
    # authority it buys is then proven by the manages LINK read below.
    if not op.authTargetValidated or op.authContextTarget != op.actor:
        return
    actor_parts = op.actor.split(".")
    if len(actor_parts) != 3 or actor_parts[0] != "vtx" or actor_parts[1] != "identity":
        fail("AuthDenied: " + op.actor + " is not an identity, so it holds no management link; " + what)
    _, unit_id = parts_of(unit_key, "unit", "unit")
    # read-posture: (d) declared optionalReads at every landlord-path dispatch
    # of the three listing ops. optionalReads, not reads: absence IS the denial this
    # probe exists to produce, so hydrating it as required would turn every
    # unauthorized call into a HydrationMiss the instant the guard's own
    # kv.Read named it — still fatal, just not pre-empting the guard from
    # hydration itself.
    lnk = kv.Read("lnk.identity." + actor_parts[2] + ".manages.unit." + unit_id)
    if lnk == None or lnk.isDeleted:
        # Nothing beyond what the caller itself supplied is named: what
        # carries the payload unit the caller already holds, and the probe
        # adds no fact about it (not whether it exists, not who manages it),
        # so a denial is never a lookup.
        fail("AuthDenied: " + op.actor + " does not manage the unit this write is for; " + what)

def require_live_unit(state, key):
    # The target MUST be alive, keyed vtx.unit.<NanoID> (location-domain's unit
    # level), AND carrying a unit class. A dead, wrong-typed or wrong-classed
    # vertex never receives listing economics.
    # BOTH the key and the class are checked, and each catches what the other
    # cannot. The KEY's type segment is what distinguishes a unit from a
    # building at all. The CLASS is what proves location-domain minted the
    # vertex: a foreign package writing vtx.unit.<id> with a class of its own
    # passes the key check and must still be refused.
    if key not in state or state[key] == None:
        fail("UnknownUnit: unit: " + key + " is absent")
    doc = state[key]
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        fail("UnknownUnit: unit: " + key + " is tombstoned")
    if key_type_of(key) != "unit":
        fail("NotAUnit: unit: " + key + " is not a vtx.unit.<NanoID> key, required unit")
    cls = class_of(state, key)
    if cls not in UNIT_CLASSES:
        fail("NotAUnit: unit: " + key + " has class " + str(cls) + ", required unit")

def class_of(state, key):
    # The vertex's root class, or None if absent. "class" is a Starlark
    # reserved word, so getattr with the string key is required.
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

# The classes a live location-domain UNIT may carry: its own key type.
UNIT_CLASSES = ["unit"]

def key_type_of(key):
    # The type segment of a 3-segment vtx.<type>.<NanoID> key, or None for any
    # other shape (an aspect key, a link key, a malformed string).
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        return None
    return parts[1]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "SetListing":
        unit = required_string(p, "unit")
        parts_of(unit, "unit", "unit")
        # workplace-exempt: (ownership-bound) the ownership probe answers before
        # require_live_unit, so a caller who manages nothing cannot use this op
        # to learn whether a unit exists.
        require_manages(unit, "cannot set the listing on " + unit)
        require_live_unit(state, unit)

        data = {
            "rentAmount": two_decimals(required_number(p, "rentAmount", False), "rentAmount"),
            "rentCurrency": required_string(p, "rentCurrency"),
            "bedrooms": required_number(p, "bedrooms", True),
            "availableFrom": required_instant(p, "availableFrom"),
            "leaseTermMonths": required_number(p, "leaseTermMonths", False),
            "status": required_status(p),
        }
        bathrooms = optional_number(p, "bathrooms", True)
        if bathrooms != None:
            data["bathrooms"] = bathrooms
        sqft = optional_number(p, "sqft", False)
        if sqft != None:
            data["sqft"] = sqft
        deposit_amount = optional_number(p, "depositAmount", False)
        if deposit_amount != None:
            data["depositAmount"] = two_decimals(deposit_amount, "depositAmount")

        listing_key = unit + ".listing"
        mutations = [make_aspect_upsert(unit, "listing", "listing", data)]
        events = [{"class": "loftspace.listingSet",
                   "data": {"unit": unit, "status": data["status"]}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": listing_key}}

    if ot == "SetUnitAddress":
        unit = required_string(p, "unit")
        parts_of(unit, "unit", "unit")
        # workplace-exempt: (ownership-bound) same discharge as SetListing --
        # the probe answers before the liveness check.
        require_manages(unit, "cannot set the address on " + unit)
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
        # economics. The directOp lease-signing's convergence targets dispatch:
        # leaseApplicationComplete marks a unit leased once its application is
        # approved, and tenancyEnd marks it available again once the recorded
        # term has ended and no other live tenancy holds it. Also callable by a
        # managing landlord by hand (any transition is admitted; a manual relist
        # under a live tenancy is flipped straight back by missing_listingLeased).
        # The unit root is hydrated via ContextHint.Reads=[unit] (the playbook
        # routes row.unitKey).
        unit = required_string(p, "unit")
        parts_of(unit, "unit", "unit")
        # workplace-exempt: (ownership-bound) the ownership probe answers before
        # require_live_unit, so a caller who manages nothing cannot use this op
        # to learn whether a unit exists.
        require_manages(unit, "cannot transition the listing on " + unit)
        require_live_unit(state, unit)
        status = required_status(p)

        # Read the existing .listing on demand (kv.Read, §2.5) — the directOp
        # declares reads=[unit] only, so the aspect is NOT in state. A status
        # transition needs a listing to transition: a unit with none is rejected
        # (NoListing) rather than minting a bare {status}-only listing.
        listing_key = unit + ".listing"
        # read-posture: (a) declared reads at SetListingStatus dispatch (both
        # the FE + the leaseApplicationComplete directOp declare unit.listing;
        # script-read-posture-design.md §13 hard case 4).
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

    if ot == "FloorListingAvailability":
        # Availability floor: raise .listing.availableFrom to the payload's
        # instant when the stored date is earlier, preserving every other
        # economics field verbatim. The directOp lease-signing's tenancyEnd
        # target dispatches once a term has a recorded end (endedAt, or the
        # move-out of a notice) that the listing's date sits before — a unit
        # is never marketed from a day its sitting tenant still holds. The
        # floor is a monotone max: a landlord's LATER date (a renovation gap
        # after the end) is never lowered, and every ended term on the unit
        # floors to its own end, so the unit converges to the latest end in
        # any dispatch order. Also callable by an operator by hand.
        unit = required_string(p, "unit")
        parts_of(unit, "unit", "unit")
        # workplace-exempt: (ownership-bound) the ownership probe answers before
        # require_live_unit, so a caller who manages nothing cannot use this op
        # to learn whether a unit exists.
        require_manages(unit, "cannot floor the availability on " + unit)
        require_live_unit(state, unit)
        floor_at = time.rfc3339_utc(as_rfc3339_instant(required_string(p, "availableFrom")))

        listing_key = unit + ".listing"
        # read-posture: (a) declared reads at FloorListingAvailability dispatch
        # (the tenancyEnd directOp declares unit.listing; the descriptor's
        # Reads carries the same key for a by-hand submission).
        existing = kv.Read(listing_key)
        if existing == None or existing.isDeleted:
            fail("NoListing: unit " + unit + " has no listing to floor")

        # The stored date may be a bare YYYY-MM-DD (seed data) or an RFC3339
        # instant (the FE's shape); both normalize to the canonical UTC form
        # before the max, which is then a plain string compare. A listing with
        # no date at all floors straight to the payload's.
        stored = existing.data.get("availableFrom")
        floored = floor_at
        if type(stored) == type("") and len(stored) > 0:
            stored_at = time.rfc3339_utc(as_rfc3339_instant(stored))
            if stored_at > floored:
                floored = stored_at

        # Idempotent no-op on STRING equality with the stored value, not
        # instant equality: the tenancyEnd lens compares the stored string
        # against the recorded end lexically, and a bare "2027-01-31" reads
        # below "2027-01-31T00:00:00Z", so an instant-equal no-op would leave
        # that gap open forever. Rewriting the bare date in canonical form
        # closes it in one pass; a re-dispatch after that emits NOTHING (no
        # mutation, no event, no CDC churn). primaryKey is omitted: the
        # reply-constraint requires a non-empty primaryKey to be a committed
        # mutation key, and a no-op commits none.
        if floored == stored:
            return {"mutations": [], "events": [], "response": {}}

        data = copy_data(existing.data)
        data["availableFrom"] = floored
        mutations = [make_aspect_upsert(unit, "listing", "listing", data)]
        events = [{"class": "loftspace.listingAvailabilityFloored",
                   "data": {"unit": unit, "availableFrom": floored}}]
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
