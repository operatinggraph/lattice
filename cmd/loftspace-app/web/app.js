"use strict";

// LoftSpace applicant app — Browse & Apply (Increment A). Vanilla JS, no build
// step. The Go server does all NATS I/O; this view reads /api/listings +
// /api/staff/identities and submits CreateLeaseApplication via /api/op.

const APPLICANT_KEY = "loftspace.applicant";
const MODE_KEY = "loftspace.mode";
const state = {
  listings: [], applications: [], tasks: [], docs: [], identities: [], units: [],
  applicant: null, current: null, currentTask: null, view: "browse", highlight: null,
  mode: "applicant",
  docScope: null,
  // sessionUploads maps an oid uploaded THIS session to the link it was created
  // with, so the doc can be detached. A listed doc from a prior session has no
  // linkName in the read model (the lens cannot project type(r)), so detach of
  // those is a documented follow-up.
  sessionUploads: {},
  // unitPhotos maps a unitKey → its listing photos ([{oid, contentType}]), cached
  // so the Browse filter/sort re-render reads photos without refetching. Loaded
  // lazily after the listings land (see loadListingPhotos).
  unitPhotos: {},
  // lightbox holds the open gallery's photo set + current index.
  lightbox: null,
  // photoUnitKey is the unit whose manage-photos modal (landlord) is open.
  photoUnitKey: null,
  // editUnitKey is set when the listing modal is in EDIT mode (vs post). editStatus
  // holds the unit's current listing status so an edit preserves it (SetListing
  // requires status, and an edit must not silently relist a withdrawn/leased unit).
  editUnitKey: null,
  editStatus: null,
};

// DOC_SLOTS labels the upload "slot" (the link name) for display.
const DOC_SLOTS = {
  idDocument: "ID document",
  proofOfIncome: "Proof of income",
  signedLeasePdf: "Signed lease (PDF)",
};

// PHOTO_LINK is the link name every listing photo is attached under (owner =
// vtx.unit.<id>). It's deterministic (unlike the per-document slot), so a unit
// photo from any session can be detached — the manage-photos modal relies on it.
const PHOTO_LINK = "listingPhoto";

// isImage keeps a gallery to renderable raster images; the object GET streams only
// the image/* allow-list inline (everything else is forced to octet-stream), so a
// non-image attached to a unit would not render as an <img>.
function isImage(contentType) {
  return typeof contentType === "string" && contentType.indexOf("image/") === 0;
}

// photosFor returns the cached image photos for a unit (empty until loaded).
function photosFor(unitKey) {
  return (state.unitPhotos[unitKey] || []).filter((p) => isImage(p.contentType));
}

// photoSrc is the streaming URL for an object id (served inline for image types).
function photoSrc(oid) {
  return "/api/objects/" + encodeURIComponent(oid);
}

// COMPLETIONS maps a userTask op to how the applicant completes it in-app. target
// is the op's primary key field, filled from the task's scopedTo — for a userTask
// the §10.5 invariant holds (assignee == scopedTo == the subject), so scopedTo is
// the entity the op acts on. class is the op's DDL-inference class; reads carry the
// scopedTo key. An op not listed here can't be completed in-app yet (the generic
// DDL-self-describing form needs an op-catalog read model — a Core-KV op-meta scan
// would violate P5 in a vertical app); its card links to Loupe instead.
const COMPLETIONS = {
  SignLease: {
    title: "Sign your lease",
    klass: "leaseapp",
    targetField: "leaseAppKey",
    fields: [],
    submitLabel: "Sign lease",
  },
  RecordIdentityPII: {
    title: "Provide your identity details",
    klass: "identity",
    targetField: "identityKey",
    sensitive: true,
    fields: [
      { name: "ssn", label: "Social Security Number", placeholder: "123-45-6789", required: true },
      { name: "dob", label: "Date of birth", type: "date", required: true },
    ],
    submitLabel: "Submit details",
  },
};

const $ = (sel) => document.querySelector(sel);

// api issues a JSON request and returns the parsed body. A structured op reply
// carries a string `status` (accepted | rejected) and is returned even on
// rejection — a rejected op is a domain outcome the caller branches on via its
// reply.status==="rejected" handler, not a transport error. Its .error is an
// object {code, message}, which must NOT be thrown as-is (that surfaces
// "[object Object]"). Only a real transport failure (!res.ok) or a non-op error
// body throws — always with a string message.
async function api(path, opts) {
  const res = await fetch(path, opts);
  let body = null;
  try {
    body = await res.json();
  } catch (_) {
    /* empty/non-JSON body */
  }
  if (body && typeof body.status === "string") {
    return body;
  }
  if (!res.ok || (body && body.error)) {
    const e = body && body.error;
    throw new Error((typeof e === "string" ? e : e && e.message) || `HTTP ${res.status}`);
  }
  return body;
}

// ---- Read-boundary token (D1.3 Fire 3) ----
// The My Applications list is served from the PROTECTED Postgres read model as an
// authenticated actor: RLS returns only the signed-in applicant's rows, so the
// request must carry a verified JWT (there is no client-side applicant filter to
// forge). In the trusted-tool DEMO posture the app mints a short-lived token for
// the selected applicant via POST /api/dev-token (the explicit stand-in for the
// deferred Gateway/IdP login); a production deployment wires a real IdP and the FE
// would present that token instead. The token is cached per subject until shortly
// before expiry.
let readTokenCache = { subject: null, token: null, exp: 0 };

// bareId extracts the bare identity NanoID (the RLS principal / JWT subject) from
// a full vtx.identity.<id> key.
function bareId(fullKey) {
  const i = (fullKey || "").lastIndexOf(".");
  return i >= 0 ? fullKey.slice(i + 1) : fullKey || "";
}

async function readToken() {
  if (!state.applicant) return null;
  const subject = bareId(state.applicant);
  const now = Date.now();
  if (readTokenCache.subject === subject && readTokenCache.token && now < readTokenCache.exp - 60000) {
    return readTokenCache.token;
  }
  const res = await fetch("/api/dev-token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ subject }),
  });
  if (!res.ok) {
    throw new Error("sign-in required — the read boundary has no demo token minter (deferred Gateway login)");
  }
  const body = await res.json();
  readTokenCache = { subject, token: body.token, exp: Date.parse(body.expiresAt) || now + 5 * 60000 };
  return body.token;
}

// authedGet fetches a protected endpoint with the read-boundary Bearer token. On
// a 401 (e.g. the app restarted with a fresh ephemeral dev-auth key, invalidating
// a cached token) it clears the cache and retries once with a freshly minted token.
async function authedGet(path) {
  let token = await readToken();
  if (!token) throw new Error("select an applicant identity to sign in");
  try {
    return await api(path, { headers: { Authorization: "Bearer " + token } });
  } catch (e) {
    if (!/HTTP 401|authentication required/i.test(e.message)) throw e;
    readTokenCache = { subject: null, token: null, exp: 0 };
    token = await readToken();
    if (!token) throw e;
    return api(path, { headers: { Authorization: "Bearer " + token } });
  }
}

// staffReadToken (D1.5, the staff wildcard increment) mints/caches a demo
// token for the system-wide STAFF view via POST /api/staff/dev-token — unlike
// readToken it carries no subject: the server mints for its own fixed admin
// actor, so the FE never needs to know (or be trusted to name) which identity
// holds the wildcard grant. Used to bootstrap the applicant-identity picker
// itself, which must work before any applicant has been selected.
let staffTokenCache = { token: null, exp: 0 };

async function staffReadToken() {
  const now = Date.now();
  if (staffTokenCache.token && now < staffTokenCache.exp - 60000) {
    return staffTokenCache.token;
  }
  const res = await fetch("/api/staff/dev-token", { method: "POST" });
  if (!res.ok) {
    throw new Error("sign-in required — the read boundary has no demo token minter (deferred Gateway login)");
  }
  const body = await res.json();
  staffTokenCache = { token: body.token, exp: Date.parse(body.expiresAt) || now + 5 * 60000 };
  return body.token;
}

// authedGetAsStaff fetches a protected endpoint with the staff Bearer token,
// retrying once on a 401 with a freshly minted token — the system-wide
// sibling of authedGet.
async function authedGetAsStaff(path) {
  let token = await staffReadToken();
  try {
    return await api(path, { headers: { Authorization: "Bearer " + token } });
  } catch (e) {
    if (!/HTTP 401|authentication required/i.test(e.message)) throw e;
    staffTokenCache = { token: null, exp: 0 };
    token = await staffReadToken();
    return api(path, { headers: { Authorization: "Bearer " + token } });
  }
}

// openDocument fetches a D1.5-protected object's bytes with the Bearer token
// (a plain <a href> navigation can't attach one) and opens them as a blob URL
// in a new tab. The blob URL is revoked after a delay long enough for the new
// tab to load the content, so it doesn't leak for the life of the page.
async function openDocument(oid) {
  let token;
  try {
    token = await readToken();
  } catch (e) {
    toast("Could not open document: " + e.message, "err");
    return;
  }
  if (!token) {
    toast("select an applicant identity to sign in", "err");
    return;
  }
  const res = await fetch("/api/objects/" + encodeURIComponent(oid), {
    headers: { Authorization: "Bearer " + token },
  });
  if (!res.ok) {
    toast("Could not open document: HTTP " + res.status, "err");
    return;
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  window.open(url, "_blank", "noopener");
  setTimeout(() => URL.revokeObjectURL(url), 60000);
}

function toast(msg, kind, extra) {
  const t = $("#toast");
  t.className = "toast " + (kind || "");
  t.innerHTML = "";
  t.append(document.createTextNode(msg));
  if (extra) {
    const span = document.createElement("span");
    span.className = "mono";
    span.textContent = " " + extra;
    t.append(span);
  }
  t.hidden = false;
  clearTimeout(toast._timer);
  toast._timer = setTimeout(() => (t.hidden = true), 6000);
}

// ---- Applicant identity (the trusted-tool switcher) ----
//
// The applicant picks who they are from a human-readable roster, read from the
// PROTECTED applicantRosterRead Postgres model (D1.5) via authedGetAsStaff — the
// picker used to read the unprotected /api/identities, letting ANY caller dump
// every identity's full name with no authentication at all (a system-wide
// membership-disclosure leak). The selected key is persisted in localStorage so
// a refresh keeps context.

// nameFor resolves an identity key to its human name from the loaded roster,
// falling back to the short key when the roster has not loaded (or the key is an
// application/unit, not a person).
function nameFor(key) {
  const m = state.identities.find((i) => i.identityKey === key);
  return m && m.name ? m.name : shortKey(key);
}

function restoreApplicant() {
  const saved = (localStorage.getItem(APPLICANT_KEY) || "").trim();
  state.applicant = saved || null;
}

// loadIdentities reads the protected, RLS-scoped applicant roster (D1.5, the
// staff wildcard increment) and rebuilds the top-right picker. authedGetAsStaff
// mints its own fixed-subject token, so this still works before an applicant
// has been selected. Non-fatal on error — the picker just shows the empty hint.
async function loadIdentities() {
  try {
    const data = await authedGetAsStaff("/api/staff/identities");
    state.identities = data.identities || [];
  } catch (_) {
    state.identities = [];
  }
  populateApplicantSelect();
}

// populateApplicantSelect rebuilds the #applicant <select>: a placeholder + one
// option per named identity (label = name, value = identityKey), selecting the
// persisted applicant when it is in the roster.
function populateApplicantSelect() {
  const sel = $("#applicant");
  sel.innerHTML = "";
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = state.identities.length
    ? "Select your identity…"
    : "No identities — create one via Loupe/CLI";
  sel.append(placeholder);
  for (const id of state.identities) {
    const o = document.createElement("option");
    o.value = id.identityKey;
    o.textContent = id.name;
    sel.append(o);
  }
  const values = state.identities.map((i) => i.identityKey);
  sel.value = state.applicant && values.includes(state.applicant) ? state.applicant : "";
}

function setApplicant(value) {
  const v = (value || "").trim();
  state.applicant = v || null;
  state.highlight = null; // a highlight belongs to the applicant who just applied
  if (v) localStorage.setItem(APPLICANT_KEY, v);
  else localStorage.removeItem(APPLICANT_KEY);
  renderListings(); // re-enable/disable Apply for the new applicant
  if (state.view === "apps") loadApplications(); // re-scope the tracker to the new applicant
  if (state.view === "tasks") loadTasks(); // re-scope the inbox to the new applicant
  if (state.view === "docs") loadDocsView(); // re-scope the documents to the new applicant
  if (state.mode === "landlord") loadLandlord(); // re-scope the operator console to the new sign-in
}

// ---- New applicant modal ----
//
// CreateUnclaimedIdentity (identity-domain) requires a name + at least one contact
// (email/phone) + a claimKeyHash = sha256-hex of a client-minted secret (Lattice
// never holds the plaintext). This trusted-tool app mints a random secret, hashes
// it in-browser (crypto.subtle — 127.0.0.1 is a secure context), and submits only
// the hash; the applicant is created directly (no claim ceremony in this demo) and
// becomes the active applicant. Mirrors clinic-app's in-app "New patient".

function openNewApplicant() {
  $("#applicant-form").reset();
  $("#applicant-overlay").hidden = false;
  $("#na-name").focus();
}

function closeNewApplicant() {
  $("#applicant-overlay").hidden = true;
}

// sha256Hex returns the lowercase hex sha256 of a string — the shape
// CreateUnclaimedIdentity stores for claimKeyHash.
async function sha256Hex(s) {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, "0")).join("");
}

// mintClaimSecret returns a random claim-secret plaintext. It is hashed and only the
// hash is sent; the plaintext never enters Lattice (and, in this demo, is discarded).
function mintClaimSecret() {
  const a = new Uint8Array(32);
  crypto.getRandomValues(a);
  return Array.from(a).map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function submitNewApplicant(ev) {
  ev.preventDefault();
  const name = $("#na-name").value.trim();
  const email = $("#na-email").value.trim();
  const phone = $("#na-phone").value.trim();
  if (!name) {
    toast("A name is required.", "err");
    return;
  }
  if (!email && !phone) {
    toast("Enter an email or a phone number.", "err");
    return;
  }

  const submit = $("#applicant-submit");
  submit.disabled = true;
  try {
    const claimKeyHash = await sha256Hex(mintClaimSecret());
    const payload = { name, claimKeyHash };
    if (email) payload.email = email;
    if (phone) payload.phone = phone;
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ operationType: "CreateUnclaimedIdentity", class: "identity", payload }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not create applicant — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    closeNewApplicant();
    toast("Applicant created.", "ok", key);
    // Make the new applicant active (the roster lens may take a moment to project;
    // select now + reload so the switcher shows it once projected).
    if (key) {
      state.applicant = key;
      localStorage.setItem(APPLICANT_KEY, key);
    }
    await loadIdentities();
    renderListings(); // re-enable Apply for the now-selected applicant
  } catch (e) {
    toast("Could not create applicant: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Tabs (Browse & Apply / My Applications / Tasks / Documents) ----

const VIEWS = ["browse", "apps", "tasks", "docs"];

function showView(view) {
  state.view = view;
  for (const v of VIEWS) {
    const isV = v === view;
    $("#view-" + v).hidden = !isV;
    const tab = $("#tab-" + v);
    tab.classList.toggle("active", isV);
    tab.setAttribute("aria-selected", String(isV));
  }
  // Re-render Browse on show so cards pick up any photo covers cached since they
  // were first rendered (e.g. while the user was in another view / landlord mode).
  if (view === "browse") renderListings();
  if (view === "apps") loadApplications();
  if (view === "tasks") loadTasks();
  if (view === "docs") loadDocsView();
}

// ---- Mode (Applicant / Landlord) ----
//
// The two sides of the marketplace share one trusted-tool app. Applicant mode is
// the default (Browse / My Applications / Tasks / Documents over the per-applicant
// identity); Landlord mode swaps to a my-units view over the by-unit aggregate. The
// chosen mode persists across reloads.

const MODES = ["applicant", "landlord"];

function restoreMode() {
  const saved = (localStorage.getItem(MODE_KEY) || "").trim();
  state.mode = MODES.includes(saved) ? saved : "applicant";
}

function setMode(mode) {
  state.mode = MODES.includes(mode) ? mode : "applicant";
  localStorage.setItem(MODE_KEY, state.mode);
  applyMode();
}

function applyMode() {
  const landlord = state.mode === "landlord";
  $("#mode-applicant").classList.toggle("active", !landlord);
  $("#mode-applicant").setAttribute("aria-selected", String(!landlord));
  $("#mode-landlord").classList.toggle("active", landlord);
  $("#mode-landlord").setAttribute("aria-selected", String(landlord));
  $("#applicant-tabs").hidden = landlord;
  // The identity picker stays visible in landlord mode too (D1.5): the operator
  // console now reads as this identity, RLS-scoped to the units it manages, so
  // there is no "trusted, no sign-in" posture left to hide it for.
  $("#applicant-who").hidden = false;
  $("label[for='applicant']").textContent = landlord ? "Signed in as" : "Applicant";
  $("#brand-sub").textContent = landlord ? "manage your units" : "apply to lease";
  $("#view-landlord").hidden = !landlord;
  if (landlord) {
    for (const v of VIEWS) $("#view-" + v).hidden = true;
    loadLandlord();
  } else {
    showView(state.view);
  }
}

// ---- Listings (Browse & Apply) ----

async function loadListings() {
  const status = $("#status").value;
  const grid = $("#listings");
  const empty = $("#empty");
  $("#summary").textContent = "loading…";
  try {
    const data = await api("/api/listings?status=" + encodeURIComponent(status));
    state.listings = data.listings || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load listings: " + e.message;
    $("#summary").textContent = "";
    return;
  }
  renderListings();
  loadListingPhotos();
}

// loadListingPhotos lazily fetches each listed unit's photos from the
// objectAttachments read model (P5: /api/objects?owner=<unitKey>, the lens
// projection — never Core KV), caching by unitKey so the filter/sort re-render
// never refetches. It re-renders once the (parallel) fetches settle so covers
// fill in. Best-effort: a unit whose photos fail to load just shows the
// no-photo placeholder.
async function loadListingPhotos() {
  const want = state.listings.map((l) => l.unitKey).filter((k) => k && !(k in state.unitPhotos));
  if (want.length === 0) return;
  await Promise.allSettled(
    want.map(async (unitKey) => {
      try {
        const data = await api("/api/objects?owner=" + encodeURIComponent(unitKey));
        state.unitPhotos[unitKey] = (data.documents || []).map((d) => ({ oid: d.oid, contentType: d.contentType }));
      } catch (e) {
        state.unitPhotos[unitKey] = []; // negative-cache so we don't loop on a dead unit
      }
    }),
  );
  // Only re-render if Browse is the active view (avoid clobbering another panel).
  if (state.view === "browse" && state.mode === "applicant") renderListings();
}

// visibleListings applies the Browse filter/sort bar over the already-loaded
// state.listings (no server round-trip — the availableListings projection carries
// rent/beds/city/availableFrom). Returns a filtered, sorted copy.
function visibleListings() {
  const q = ($("#q-search").value || "").trim().toLowerCase();
  const minBeds = parseInt($("#q-beds").value, 10) || 0;
  const maxRentRaw = ($("#q-maxrent").value || "").trim();
  const maxRent = maxRentRaw === "" ? null : Number(maxRentRaw);
  const sort = $("#q-sort").value;

  const rows = state.listings.filter((row) => {
    const L = row.listing || {};
    const A = row.address || {};
    if (q) {
      const hay = [A.line1, A.line2, A.city, A.region, A.postal].filter(Boolean).join(" ").toLowerCase();
      if (!hay.includes(q)) return false;
    }
    if (minBeds && !(typeof L.bedrooms === "number" && L.bedrooms >= minBeds)) return false;
    // A max-rent filter excludes only listings with a known rent above it; a listing
    // missing a rent figure is left in rather than silently dropped.
    if (maxRent !== null && !Number.isNaN(maxRent) && typeof L.rentAmount === "number" && L.rentAmount > maxRent) return false;
    return true;
  });

  const rent = (r) => (r.listing && typeof r.listing.rentAmount === "number" ? r.listing.rentAmount : Infinity);
  const beds = (r) => (r.listing && typeof r.listing.bedrooms === "number" ? r.listing.bedrooms : -1);
  const avail = (r) => {
    const v = r.listing && r.listing.availableFrom ? Date.parse(r.listing.availableFrom) : NaN;
    return Number.isNaN(v) ? Infinity : v;
  };
  const cmp = {
    "rent-asc": (a, b) => rent(a) - rent(b),
    "rent-desc": (a, b) => rent(b) - rent(a),
    "beds-desc": (a, b) => beds(b) - beds(a),
    "avail-asc": (a, b) => avail(a) - avail(b),
  }[sort];
  return cmp ? rows.sort(cmp) : rows;
}

function renderListings() {
  const grid = $("#listings");
  const empty = $("#empty");
  grid.innerHTML = "";
  const total = state.listings.length;
  if (total === 0) {
    empty.hidden = false;
    empty.textContent = "No units are listed for lease right now.";
    $("#summary").textContent = "";
    return;
  }
  const rows = visibleListings();
  if (rows.length === 0) {
    empty.hidden = false;
    empty.textContent = "No listings match your filters.";
    $("#summary").textContent = `0 of ${total} listing${total === 1 ? "" : "s"}`;
    return;
  }
  empty.hidden = true;
  for (const row of rows) grid.append(renderCard(row));
  $("#summary").textContent =
    rows.length === total
      ? `${total} listing${total === 1 ? "" : "s"}`
      : `${rows.length} of ${total} listings`;
}

function money(listing) {
  const amt = listing && typeof listing.rentAmount === "number" ? listing.rentAmount : null;
  if (amt === null) return "—";
  const cur = listing.rentCurrency || "";
  const n = amt.toLocaleString();
  return cur === "USD" ? `$${n}` : `${n} ${cur}`.trim();
}

function fmtDate(s) {
  if (!s) return "";
  const d = new Date(s);
  return isNaN(d) ? s : d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// renderCardPhoto builds the Browse card's cover image. With photos it shows the
// first as a cover with an "n photos" count and opens the lightbox on click;
// with none it shows a neutral placeholder so a listing nobody photographed still
// reads as a card (and a real leasing product's "no photos yet" state is honest).
function renderCardPhoto(row) {
  const photos = photosFor(row.unitKey);
  const wrap = document.createElement("div");
  wrap.className = "card-photo";
  if (photos.length === 0) {
    wrap.classList.add("placeholder");
    wrap.innerHTML = '<span class="card-photo-icon">🏠</span><span class="card-photo-none">No photos</span>';
    return wrap;
  }
  const img = document.createElement("img");
  img.src = photoSrc(photos[0].oid);
  img.alt = "Listing photo";
  img.loading = "lazy";
  wrap.append(img);
  if (photos.length > 1) {
    const count = document.createElement("span");
    count.className = "card-photo-count";
    count.textContent = "📷 " + photos.length;
    wrap.append(count);
  }
  wrap.title = "View photos";
  wrap.addEventListener("click", () => openLightbox(row.unitKey, 0));
  return wrap;
}

function renderCard(row) {
  const L = row.listing || {};
  const A = row.address || {};
  const card = document.createElement("div");
  card.className = "card";

  card.append(renderCardPhoto(row));

  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = A.line1 || row.unitKey;
  const addrSub = document.createElement("div");
  addrSub.className = "addr-sub";
  addrSub.textContent = [A.line2, [A.city, A.region].filter(Boolean).join(", "), A.postal].filter(Boolean).join(" · ");

  const rent = document.createElement("div");
  rent.className = "rent";
  rent.innerHTML = `${money(L)} <span>/ month</span>`;

  const facts = document.createElement("div");
  facts.className = "facts";
  const f = [];
  if (typeof L.bedrooms === "number") f.push(`${L.bedrooms} bd`);
  if (typeof L.bathrooms === "number") f.push(`${L.bathrooms} ba`);
  if (typeof L.sqft === "number") f.push(`${L.sqft.toLocaleString()} sqft`);
  facts.textContent = f.join("  ·  ");

  const meta = document.createElement("div");
  meta.className = "meta";
  const m = [];
  if (L.availableFrom) m.push("available " + fmtDate(L.availableFrom));
  if (typeof L.leaseTermMonths === "number") m.push(`${L.leaseTermMonths}-mo term`);
  meta.textContent = m.join("  ·  ");

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const badge = document.createElement("span");
  badge.className = "badge " + (row.status || "");
  badge.textContent = row.status || "—";
  const apply = document.createElement("button");
  apply.textContent = "Apply";
  const leasable = row.status === "available";
  apply.disabled = !leasable || !state.applicant;
  apply.title = !state.applicant ? "Select an applicant first" : !leasable ? "Not available" : "";
  apply.addEventListener("click", () => openApply(row));
  actions.append(badge, apply);

  card.append(addr, addrSub, rent);
  if (facts.textContent) card.append(facts);
  if (meta.textContent) card.append(meta);
  card.append(actions);
  return card;
}

// ---- Apply modal ----

function openApply(row) {
  if (!state.applicant) {
    toast("Select an applicant first.", "err");
    return;
  }
  state.current = row;
  const A = row.address || {};
  $("#apply-unit").textContent = (A.line1 ? A.line1 + " · " : "") + row.unitKey;
  $("#apply-applicant").textContent = nameFor(state.applicant);
  $("#apply-form").reset();
  syncTermRequirement();
  $("#apply-overlay").hidden = false;
  $("#moveInDate").focus();
}

function closeApply() {
  $("#apply-overlay").hidden = true;
  state.current = null;
}

// A move-in date makes the lease term required (the DDL rejects a half-specified
// terms block).
function syncTermRequirement() {
  const hasDate = !!$("#moveInDate").value;
  $("#term-hint").hidden = !hasDate;
  $("#term-opt").textContent = hasDate ? "(required)" : "(optional)";
}

async function submitApply(ev) {
  ev.preventDefault();
  const row = state.current;
  if (!row || !state.applicant) return;

  const moveIn = $("#moveInDate").value;
  const term = $("#leaseTermMonths").value;
  const rent = $("#requestedRent").value;

  if (moveIn && !term) {
    toast("Enter a lease term to go with the move-in date.", "err");
    return;
  }

  const payload = { applicant: state.applicant, unit: row.unitKey };
  if (moveIn) {
    // The .terms aspect stores moveInDate verbatim; normalize the date input to
    // an RFC3339 instant.
    payload.moveInDate = moveIn.length === 10 ? moveIn + "T00:00:00Z" : moveIn;
    payload.leaseTermMonths = Number(term);
  }
  if (rent) payload.requestedRent = Number(rent);

  const submit = $("#apply-submit");
  submit.disabled = true;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "CreateLeaseApplication",
        class: "leaseapp",
        reads: [state.applicant, row.unitKey],
        payload,
      }),
    });
    if (reply && reply.status === "rejected") {
      const errMsg = (reply.error && reply.error.message) || "";
      // The guard rejects a repeat application by the same applicant for the same
      // unit (script fail "DuplicateApplication: ..."); surface it plainly.
      if (errMsg.includes("DuplicateApplication")) {
        toast("You already have an active application for this unit.", "err");
        return;
      }
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Application rejected — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    closeApply();
    toast("Application submitted.", "ok", key);
    loadListings();
    // Route to My Applications with the new application highlighted (the lens
    // may take a moment to project, so an empty/late row is normal on first load;
    // a Refresh shows it once projected). showView triggers the scoped load.
    state.highlight = key || null;
    showView("apps");
  } catch (e) {
    toast("Could not submit: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- My Applications (status tracker) ----

async function loadApplications() {
  const grid = $("#apps");
  const empty = $("#apps-empty");
  if (!state.applicant) {
    grid.innerHTML = "";
    state.applications = [];
    empty.hidden = false;
    empty.textContent = "Select an applicant identity above to see their applications.";
    $("#apps-summary").textContent = "";
    return;
  }
  $("#apps-summary").textContent = "loading…";
  try {
    const data = await authedGet("/api/applications");
    state.applications = data.applications || [];
    state.appsScope = data.scope || "";
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load applications: " + e.message;
    $("#apps-summary").textContent = "";
    return;
  }
  renderApplications();
}

function renderApplications() {
  const highlight = state.highlight;
  const grid = $("#apps");
  const empty = $("#apps-empty");
  grid.innerHTML = "";
  if (state.applications.length === 0) {
    empty.hidden = false;
    empty.textContent = "No applications yet. Browse a listing and apply to get started.";
    $("#apps-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const row of state.applications) grid.append(renderApplicationCard(row, highlight));
  const n = state.applications.length;
  $("#apps-summary").textContent = `${n} application${n === 1 ? "" : "s"}`;
}

// Each step is one gap dimension, derived from the lens columns the row actually
// carries: a closed gap is "done"; an open gap with a standing rejection (a failed
// check no retry has superseded) is "declined"; an open gap with a call in flight
// is "active" (In progress); an open gap with nothing in flight is "todo". A retry
// in flight (inflight) takes precedence over a standing rejection — the check is
// being re-run. The lens does not project a per-row "retries exhausted" signal
// (maxretries_<g> is a constant cap), so a stalled non-declined automated step
// reads as "todo".
function stepState(done, inflight, declined) {
  if (done) return "done";
  if (inflight) return "active";
  if (declined) return "declined";
  return "todo";
}

const STEP_LABEL = { done: "Done", active: "In progress", declined: "Declined", todo: "To do" };

function renderStep(num, title, st, note) {
  const step = document.createElement("li");
  step.className = "step " + st;
  const dot = document.createElement("span");
  dot.className = "step-dot";
  dot.textContent = st === "done" ? "✓" : st === "declined" ? "✕" : String(num);
  const body = document.createElement("div");
  body.className = "step-body";
  const t = document.createElement("div");
  t.className = "step-title";
  t.textContent = title;
  const s = document.createElement("div");
  s.className = "step-status";
  s.textContent = note ? `${STEP_LABEL[st]} · ${note}` : STEP_LABEL[st];
  body.append(t, s);
  step.append(dot, body);
  return step;
}

function shortKey(key) {
  const i = (key || "").lastIndexOf(".");
  return i >= 0 ? key.slice(i + 1) : key || "—";
}

// renderProtectedApplicationCard renders one application from the protected,
// RLS-scoped read model: the unit header, a coarse decision/status banner, the
// lease-terms panel (the scalars the protected model carries), the signed-lease
// download, and Withdraw. The detailed per-step journey is omitted — it depends on
// the Weaver convergence aggregate the protected model does not (yet) carry — and
// the card says so once.
function renderProtectedApplicationCard(row, highlight) {
  const card = document.createElement("div");
  card.className = "card app-card";
  if (highlight && row.entityKey === highlight) card.classList.add("highlight");

  const head = document.createElement("div");
  head.className = "app-head";
  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = row.unitAddress || (row.unitKey ? shortKey(row.unitKey) : "Application");
  head.append(addr);
  if (typeof row.unitRent === "number") {
    const rent = document.createElement("div");
    rent.className = "rent";
    rent.innerHTML = `$${row.unitRent.toLocaleString()} <span>/ month</span>`;
    head.append(rent);
  }
  const ref = document.createElement("div");
  ref.className = "addr-sub mono";
  ref.textContent = shortKey(row.entityKey);
  head.append(ref);

  const banner = document.createElement("div");
  if (row.declined) {
    banner.className = "decision declined";
    banner.textContent =
      row.landlordDeclined && row.declineReason
        ? "Application declined: " + row.declineReason
        : "Application declined.";
  } else if (row.landlordApproved && row.unitStatus === "leased") {
    banner.className = "decision ok";
    banner.textContent = "Application complete — lease executed.";
  } else if (row.landlordApproved) {
    banner.className = "decision ok";
    banner.textContent = "Approved — finalizing lease.";
  } else if (row.signedAt) {
    banner.className = "decision pending";
    banner.textContent = "Signed — awaiting landlord decision.";
  } else {
    banner.className = "decision pending";
    banner.textContent = "In review.";
  }
  card.append(head, banner);

  const terms = renderLeaseTermsPanel(row);
  if (terms) card.append(terms);

  const note = document.createElement("div");
  note.className = "addr-sub";
  note.textContent = "Step-by-step tracking returns when the protected read model carries gap state.";
  card.append(note);

  const actions = document.createElement("div");
  actions.className = "card-actions";
  if (row.signedAt) {
    const lease = document.createElement("a");
    lease.className = "ghost btn-link";
    lease.textContent = "📄 Signed lease";
    lease.href = "/api/lease-document?leaseAppKey=" + encodeURIComponent(row.entityKey);
    lease.target = "_blank";
    lease.rel = "noopener";
    lease.title = "Signed on " + fmtDate(row.signedAt);
    actions.append(lease);
  }
  if (!row.landlordApproved && row.unitKey) {
    const wd = document.createElement("button");
    wd.className = "ghost danger";
    wd.textContent = "Withdraw application";
    wd.addEventListener("click", () => withdrawApplication(row));
    actions.append(wd);
  }
  if (actions.childElementCount > 0) card.append(actions);
  return card;
}

function renderApplicationCard(row, highlight) {
  // The PROTECTED Postgres read model (D1.3 Fire 3) carries the application's
  // display scalars but not the Weaver-internal convergence aggregate (the per-gap
  // stepper booleans, §10.2 — D1.5 rolls a protected gap model onto this pattern).
  // In that scope render a compact, honest card instead of a stepper whose every
  // step would falsely read "to do".
  if (state.appsScope === "rls") {
    return renderProtectedApplicationCard(row, highlight);
  }
  const card = document.createElement("div");
  card.className = "card app-card";
  if (highlight && row.entityKey === highlight) card.classList.add("highlight");

  // Header: what am I leasing
  const head = document.createElement("div");
  head.className = "app-head";
  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = row.unitAddress || (row.unitKey ? shortKey(row.unitKey) : "Application");
  head.append(addr);
  if (typeof row.unitRent === "number") {
    const rent = document.createElement("div");
    rent.className = "rent";
    rent.innerHTML = `$${row.unitRent.toLocaleString()} <span>/ month</span>`;
    head.append(rent);
  }
  const ref = document.createElement("div");
  ref.className = "addr-sub mono";
  ref.textContent = shortKey(row.entityKey);
  head.append(ref);

  // Decision banner. Declined takes precedence: a standing rejection (a failed
  // verification OR an explicit landlord decline — both fold into row.declined) is a
  // terminal disposition, not a step still to complete. Finishing the four applicant
  // steps no longer means the application is done — the landlord still has to decide.
  // So "complete" requires BOTH the landlord approval AND the unit actually leased
  // (the genuine done state — an early approval on a not-yet-qualified application
  // does not read "complete"). Between the approval and the listing flip the lease
  // is being finalized (row.landlordApproved, unit not yet leased) — a short window
  // the directOp closes. A qualified-but-undecided application (row.missing_decision)
  // reads "awaiting landlord review."
  const banner = document.createElement("div");
  if (row.declined) {
    banner.className = "decision declined";
    // A landlord decline may carry a reason (declineReason); a verification decline
    // (failed bgcheck/payment) never does. Surface the reason so a decline gives the
    // applicant feedback rather than a bare rejection.
    banner.textContent =
      row.landlordDeclined && row.declineReason
        ? "Application declined: " + row.declineReason
        : "Application declined.";
  } else if (row.landlordApproved && row.unitStatus === "leased") {
    banner.className = "decision ok";
    banner.textContent = "Application complete — all steps done.";
  } else if (row.landlordApproved) {
    banner.className = "decision ok";
    banner.textContent = "Approved — finalizing lease.";
  } else if (row.missing_decision) {
    banner.className = "decision pending";
    banner.textContent = "Qualified — awaiting landlord review.";
  } else {
    banner.className = "decision pending";
    banner.textContent = "In review — complete the open steps below.";
  }

  // Stepper (journey order)
  const steps = document.createElement("ol");
  steps.className = "stepper";
  steps.append(
    renderStep(1, "Onboarding (identity details)", stepState(!row.missing_onboarding, false, false)),
    renderStep(2, "Background check", stepState(!row.missing_bgcheck, row.inflight_bgcheck, row.declined_bgcheck)),
    renderStep(3, "Payment", stepState(!row.missing_payment, row.inflight_payment, row.declined_payment)),
    renderStep(4, "Sign lease", stepState(!row.missing_signature, false, false)),
  );

  card.append(head, banner, steps);

  // Lease terms — what the applicant is actually agreeing to (rent, term, move-in,
  // property). Projected by the convergence lens from the unit's .listing/.address
  // and the application's own .terms. Renders only the fields that are present, so
  // an application with no .terms (moveInDate omitted at apply) or an older lens
  // projection degrades to whatever the row carries instead of showing blanks.
  const terms = renderLeaseTermsPanel(row);
  if (terms) card.append(terms);

  // Qualification profile — the applicant records income / employment / references /
  // co-applicant / guarantor so the landlord has something to decide on. Available
  // while the application is live (hidden once the landlord has approved + the lease
  // is being finalized). The raw figures go to the package; only the derived signals
  // the landlord reads are projected back.
  if (!row.landlordApproved && row.unitKey) {
    card.append(renderProfilePanel(row));
  }

  const actions = document.createElement("div");
  actions.className = "card-actions";

  // Signed-lease download: once the lease is signed, the executed lease is a
  // produced artifact (a deterministic summary of the agreed terms, stamped with
  // the signature date). The GET streams it on demand so the link never waits on
  // object-store projection lag; the durable attach is produced on signing.
  if (!row.missing_signature) {
    const lease = document.createElement("a");
    lease.className = "ghost btn-link";
    lease.textContent = "📄 Signed lease";
    lease.href = "/api/lease-document?leaseAppKey=" + encodeURIComponent(row.entityKey);
    lease.target = "_blank";
    lease.rel = "noopener";
    if (row.signedAt) lease.title = "Signed on " + fmtDate(row.signedAt);
    actions.append(lease);
  }

  // Withdraw: back out of an application before the landlord approves (frees the
  // applicant to re-apply to the same unit). Stays available while the application is
  // qualified-but-undecided (awaiting landlord review) — the applicant may still
  // change their mind. Hidden once the landlord approves — the unit is being leased.
  if (!row.landlordApproved && row.unitKey) {
    const wd = document.createElement("button");
    wd.className = "ghost danger";
    wd.textContent = "Withdraw application";
    wd.addEventListener("click", () => withdrawApplication(row));
    actions.append(wd);
  }

  if (actions.childElementCount > 0) card.append(actions);
  return card;
}

// renderLeaseTermsPanel builds the "Lease terms" review panel — the terms the
// applicant is agreeing to, so signing is no longer blind. It reads the unit's
// listing economics + address and the application's own requested .terms, both
// projected onto the convergence row. A term row renders only when its value is
// present; if nothing beyond the address is known the panel is omitted entirely
// (returns null) so it never shows an empty shell. When the applicant requested a
// different rent than the listing asks, both are shown ("you offered …").
function renderLeaseTermsPanel(row) {
  const rows = [];
  const addTerm = (label, value) => {
    if (value === null || value === undefined || value === "") return;
    rows.push([label, value]);
  };

  const fullAddr = [row.unitAddress, row.unitCity, row.unitRegion].filter(Boolean).join(", ");
  addTerm("Property", fullAddr);

  const beds = typeof row.unitBedrooms === "number" ? `${row.unitBedrooms} bd` : "";
  const baths = typeof row.unitBathrooms === "number" ? `${row.unitBathrooms} ba` : "";
  addTerm("Size", [beds, baths].filter(Boolean).join(" · "));

  if (typeof row.unitRent === "number") {
    const cur = row.unitCurrency && row.unitCurrency !== "USD" ? ` ${row.unitCurrency}` : "";
    const base = row.unitCurrency && row.unitCurrency !== "USD"
      ? `${row.unitRent.toLocaleString()}${cur} / month`
      : `$${row.unitRent.toLocaleString()} / month`;
    let rent = base;
    if (typeof row.termsRequestedRent === "number" && row.termsRequestedRent !== row.unitRent) {
      rent += ` (you offered $${row.termsRequestedRent.toLocaleString()})`;
    }
    addTerm("Rent", rent);
  }

  const term = typeof row.termsLeaseTermMonths === "number"
    ? row.termsLeaseTermMonths
    : (typeof row.unitLeaseTermMonths === "number" ? row.unitLeaseTermMonths : null);
  if (term !== null) addTerm("Lease term", `${term} months`);

  const moveIn = row.termsMoveInDate || row.unitAvailableFrom;
  if (moveIn) addTerm(row.termsMoveInDate ? "Requested move-in" : "Available from", fmtDate(moveIn));

  if (rows.length === 0) return null;

  const panel = document.createElement("div");
  panel.className = "lease-terms";
  const h = document.createElement("div");
  h.className = "lease-terms-head";
  h.textContent = row.missing_signature
    ? "Lease terms — review before signing"
    : "Lease terms";
  panel.append(h);
  const dl = document.createElement("dl");
  for (const [label, value] of rows) {
    const dt = document.createElement("dt");
    dt.textContent = label;
    const dd = document.createElement("dd");
    dd.textContent = value;
    dl.append(dt, dd);
  }
  panel.append(dl);
  return panel;
}

// EMPLOYMENT_OPTIONS mirrors the SetApplicantProfile employmentStatus enum.
const EMPLOYMENT_OPTIONS = ["employed", "self-employed", "unemployed", "student", "retired"];

// renderProfilePanel builds the applicant's qualification-profile section: a short
// status line of the derived signals already recorded (so the applicant sees what
// the landlord sees) plus a collapsible form to add / update the profile. The raw
// income / employer / references go to SetApplicantProfile; only the derived signals
// come back projected.
function renderProfilePanel(row) {
  const panel = document.createElement("div");
  panel.className = "profile-panel";
  const h = document.createElement("div");
  h.className = "profile-head";
  h.textContent = "Qualification profile";
  panel.append(h);

  const status = document.createElement("div");
  status.className = "profile-status";
  if (row.profileSubmitted) {
    // Reuse the same derived chips the landlord reads.
    status.append(renderQualification(row));
  } else {
    const muted = document.createElement("div");
    muted.className = "qualification none";
    muted.textContent = "Not submitted yet — add your income, employment and references so the landlord can review.";
    status.append(muted);
  }
  panel.append(status);

  const toggle = document.createElement("button");
  toggle.className = "ghost";
  toggle.textContent = row.profileSubmitted ? "Update qualification profile" : "Add qualification profile";
  panel.append(toggle);

  const form = document.createElement("form");
  form.className = "profile-form";
  form.hidden = true;
  form.innerHTML = `
    <label>Gross annual income ($)
      <input type="number" name="annualIncome" min="1" step="1" required />
    </label>
    <label>Employment status
      <select name="employmentStatus" required>
        ${EMPLOYMENT_OPTIONS.map((o) => `<option value="${o}">${o}</option>`).join("")}
      </select>
    </label>
    <label>Employer (optional)
      <input type="text" name="employerName" />
    </label>
    <label>References (optional, one per line)
      <textarea name="references" rows="2" placeholder="Prior landlord — Jane Doe&#10;Manager — John Roe"></textarea>
    </label>
    <label class="check"><input type="checkbox" name="hasCoApplicant" /> Has a co-applicant</label>
    <div class="profile-sub" data-for="hasCoApplicant" hidden>
      <label>Co-applicant name
        <input type="text" name="coApplicantName" />
      </label>
      <label>Co-applicant contact (email / phone)
        <input type="text" name="coApplicantContact" />
      </label>
    </div>
    <label class="check"><input type="checkbox" name="hasGuarantor" /> Has a guarantor</label>
    <div class="profile-sub" data-for="hasGuarantor" hidden>
      <label>Guarantor name
        <input type="text" name="guarantorName" />
      </label>
      <label>Guarantor relationship
        <input type="text" name="guarantorRelationship" placeholder="e.g. parent" />
      </label>
      <label>Guarantor gross annual income ($)
        <input type="number" name="guarantorAnnualIncome" min="1" step="1" />
      </label>
    </div>
    <div class="profile-form-actions">
      <button type="submit">Save profile</button>
    </div>
  `;
  // A guarantor / co-applicant's detail sub-fields are revealed only when its flag
  // is checked (and the op captures them only then), so the form stays compact.
  form.querySelectorAll('input[name="hasCoApplicant"], input[name="hasGuarantor"]').forEach((cb) => {
    const sub = form.querySelector(`.profile-sub[data-for="${cb.name}"]`);
    cb.addEventListener("change", () => {
      if (sub) sub.hidden = !cb.checked;
    });
  });
  toggle.addEventListener("click", () => {
    form.hidden = !form.hidden;
  });
  form.addEventListener("submit", (ev) => submitProfile(ev, row));
  panel.append(form);
  return panel;
}

// submitProfile sends SetApplicantProfile with the raw profile fields (the package
// derives + projects the landlord-facing signals). reads=[leaseAppKey] (the op
// validates the application + reads the unit's listing rent on demand).
async function submitProfile(ev, row) {
  ev.preventDefault();
  const f = ev.target;
  const income = Number(f.annualIncome.value);
  if (!Number.isFinite(income) || income <= 0) {
    toast("Enter a positive annual income.", "err");
    return;
  }
  const references = f.references.value
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
  const payload = {
    leaseAppKey: row.entityKey,
    unit: row.unitKey,
    annualIncome: income,
    employmentStatus: f.employmentStatus.value,
    hasCoApplicant: f.hasCoApplicant.checked,
    hasGuarantor: f.hasGuarantor.checked,
  };
  const employer = f.employerName.value.trim();
  if (employer) payload.employerName = employer;
  if (references.length) payload.references = references;
  // Guarantor / co-applicant detail is sent (raw — the op stores it, never projects
  // it) only when its flag is set; the one derived signal the landlord sees is
  // guarantorIncomeToRentMet (guarantor income ≥ 3× rent), computed by the op.
  if (f.hasGuarantor.checked) {
    const gName = f.guarantorName.value.trim();
    const gRel = f.guarantorRelationship.value.trim();
    const gIncome = Number(f.guarantorAnnualIncome.value);
    if (gName) payload.guarantorName = gName;
    if (gRel) payload.guarantorRelationship = gRel;
    if (Number.isFinite(gIncome) && gIncome > 0) payload.guarantorAnnualIncome = gIncome;
  }
  if (f.hasCoApplicant.checked) {
    const cName = f.coApplicantName.value.trim();
    const cContact = f.coApplicantContact.value.trim();
    if (cName) payload.coApplicantName = cName;
    if (cContact) payload.coApplicantContact = cContact;
  }
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "SetApplicantProfile",
        class: "leaseapp",
        reads: [row.entityKey],
        payload,
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Profile rejected — " + msg, "err");
      return;
    }
    toast("Qualification profile saved.", "ok");
    setTimeout(loadApplications, 600);
  } catch (e) {
    toast("Profile failed — " + e.message, "err");
  }
}

// withdrawApplication submits WithdrawLeaseApplication (tombstones the leaseapp +
// frees the per-(applicant, unit) guard link) after a confirm, then reloads — the
// withdrawn application drops from the tracker and the unit frees for re-apply.
// The op verifies applicant against the application's applicationFor link, so the
// current applicant (whose My Applications view this is) is passed through.
async function withdrawApplication(row) {
  if (!confirm("Withdraw this application? You'll be able to apply to this unit again.")) return;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "WithdrawLeaseApplication",
        class: "leaseapp",
        reads: [row.entityKey],
        payload: { leaseAppKey: row.entityKey, unit: row.unitKey, applicant: state.applicant },
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not withdraw — " + msg, "err");
      return;
    }
    toast("Application withdrawn.", "ok");
    loadApplications();
  } catch (e) {
    toast("Could not withdraw: " + e.message, "err");
  }
}

// ---- Tasks (inbox) ----
//
// The applicant's OPEN tasks, read from the `my-tasks` lens projection (P5: a
// vertical app reads a read-model, never Core KV — Loupe scans Core KV only as the
// inspector). Each task is self-describing (the lens aspect-hops the op name +
// description off the forOperation meta), and completion submits the bound op.

async function loadTasks() {
  const grid = $("#tasks");
  const empty = $("#tasks-empty");
  if (!state.applicant) {
    grid.innerHTML = "";
    state.tasks = [];
    empty.hidden = false;
    empty.textContent = "Select an applicant identity above to see their tasks.";
    $("#tasks-summary").textContent = "";
    return;
  }
  $("#tasks-summary").textContent = "loading…";
  try {
    const data = await authedGet("/api/tasks");
    state.tasks = data.tasks || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load tasks: " + e.message;
    $("#tasks-summary").textContent = "";
    return;
  }
  renderTasks();
}

function renderTasks() {
  const grid = $("#tasks");
  const empty = $("#tasks-empty");
  grid.innerHTML = "";
  if (state.tasks.length === 0) {
    empty.hidden = false;
    empty.textContent = "No open tasks. When your application needs you to act, it will show up here.";
    $("#tasks-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const t of state.tasks) grid.append(renderTaskCard(t));
  const n = state.tasks.length;
  $("#tasks-summary").textContent = `${n} open task${n === 1 ? "" : "s"}`;
}

function renderTaskCard(t) {
  const card = document.createElement("div");
  card.className = "card task-card";

  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = t.operationName || shortKey(t.operation) || "Task";

  const desc = document.createElement("div");
  desc.className = "addr-sub";
  desc.textContent = t.operationDescription || "";

  const scope = document.createElement("div");
  scope.className = "task-scope mono";
  scope.textContent = t.scopedTo ? shortKey(t.scopedTo) : shortKey(t.taskKey);

  const meta = document.createElement("div");
  meta.className = "meta";
  if (t.expiresAt) meta.textContent = "due " + fmtDate(t.expiresAt);

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const badge = document.createElement("span");
  badge.className = "badge pending";
  badge.textContent = "open";
  const btn = document.createElement("button");
  const canComplete = !!COMPLETIONS[t.operationName];
  btn.textContent = canComplete ? "Complete" : "Complete in Loupe";
  btn.disabled = !canComplete;
  btn.title = canComplete ? "" : "This task type isn't completable in this app yet — use Loupe's Submit Op.";
  if (canComplete) btn.addEventListener("click", () => openComplete(t));
  actions.append(badge, btn);

  card.append(title);
  if (desc.textContent) card.append(desc);
  card.append(scope);
  if (meta.textContent) card.append(meta);
  card.append(actions);
  return card;
}

// ---- Complete task modal ----

function openComplete(task) {
  const desc = COMPLETIONS[task.operationName];
  if (!desc) return;
  state.currentTask = task;
  $("#complete-title").textContent = desc.title;
  $("#complete-desc").textContent = task.operationDescription || "";
  $("#tc-target").textContent = task.scopedTo || task.taskKey;
  $("#tc-sensitive").hidden = !desc.sensitive;
  $("#complete-submit").textContent = desc.submitLabel || "Complete";

  const host = $("#tc-fields");
  host.innerHTML = "";
  for (const f of desc.fields) {
    const wrap = document.createElement("div");
    wrap.className = "field";
    const label = document.createElement("label");
    label.setAttribute("for", "tc-" + f.name);
    label.textContent = f.label + (f.required ? "" : " (optional)");
    const input = document.createElement("input");
    input.id = "tc-" + f.name;
    input.type = f.type || "text";
    if (f.placeholder) input.placeholder = f.placeholder;
    wrap.append(label, input);
    host.append(wrap);
  }
  $("#complete-overlay").hidden = false;
  const first = host.querySelector("input");
  if (first) first.focus();
}

function closeComplete() {
  $("#complete-overlay").hidden = true;
  state.currentTask = null;
}

async function submitComplete(ev) {
  ev.preventDefault();
  const task = state.currentTask;
  if (!task) return;
  const desc = COMPLETIONS[task.operationName];
  if (!desc) return;

  const target = task.scopedTo || "";
  if (!target) {
    toast("This task has no target to act on.", "err");
    return;
  }
  const payload = {};
  payload[desc.targetField] = target;
  for (const f of desc.fields) {
    const v = ($("#tc-" + f.name).value || "").trim();
    if (!v) {
      if (f.required) {
        toast(f.label + " is required.", "err");
        return;
      }
      continue;
    }
    payload[f.name] = v;
  }

  const submit = $("#complete-submit");
  submit.disabled = true;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: task.operationName,
        class: desc.klass,
        reads: [target],
        payload,
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not complete — " + msg, "err");
      return;
    }
    // The bound op closed the gap; now retire the task. This app submits as the
    // trusted admin actor (a standing permission), NOT via the task's ephemeral
    // grant, so the Processor's task-path auto-complete (Contract #10 §10.7) does
    // not fire — we close the task ourselves through the contract's retained
    // out-of-band CompleteTask path. A benign rejection (the task already closed,
    // e.g. a double-submit) is non-fatal: the bound op already committed.
    await completeTask(task.taskKey);
    // Signing the lease produces the executed-lease artifact: durably attach it to
    // the application so both parties have the document on file (best-effort — the
    // download path regenerates the same bytes on demand if this misses).
    if (task.operationName === "SignLease" && target) ensureLeaseDocument(target);
    closeComplete();
    toast(desc.title + " — done.", "ok");
    loadTasks();
    loadApplications();
  } catch (e) {
    toast("Could not complete: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// completeTask submits an explicit CompleteTask(taskKey) — the Contract #10 §10.7
// out-of-band completion path — to retire the task whose bound op just committed.
// Best-effort: a rejection (the task already closed) or a transport error is logged,
// never surfaced, because the gap-closing op has already succeeded.
async function completeTask(taskKey) {
  if (!taskKey) return;
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "CompleteTask",
        class: "task",
        reads: [taskKey],
        payload: { taskKey },
      }),
    });
    if (reply && reply.status === "rejected" && reply.error) {
      console.warn("CompleteTask not applied:", reply.error.code, reply.error.message);
    }
  } catch (e) {
    console.warn("CompleteTask request failed:", e.message);
  }
}

// ensureLeaseDocument produces + durably attaches the executed-lease artifact for
// a just-signed application. Best-effort: a failure (the convergence row not yet
// projecting the signature) is logged, not surfaced — the download path
// regenerates the same deterministic bytes on demand regardless.
async function ensureLeaseDocument(leaseAppKey) {
  if (!leaseAppKey) return;
  try {
    await api("/api/lease-document", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ leaseAppKey }),
    });
  } catch (e) {
    console.warn("lease document not attached (will regenerate on download):", e.message);
  }
}

// ---- Documents (upload / view / list) ----
//
// The applicant's documents, read from the `objectAttachments` lens projection
// (P5: a vertical app reads a read-model, never Core KV). A document is attached
// to a "scope" — the applicant's identity (ID docs) or one of their applications
// (proof-of-income, signed lease) — chosen in the scope selector; uploads attach
// to that scope and the list shows that scope's documents. Bytes flow through the
// Go server's object endpoints, never the Refractor.

// loadDocsView refreshes the scope selector (identity + the applicant's
// applications) then loads the selected scope's documents.
async function loadDocsView() {
  const empty = $("#docs-empty");
  const grid = $("#docs");
  if (!state.applicant) {
    grid.innerHTML = "";
    state.docs = [];
    $("#doc-scope").innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Select an applicant identity above to manage their documents.";
    $("#docs-summary").textContent = "";
    return;
  }
  // Refresh applications so the scope selector lists the applicant's current
  // applications; a failure is non-fatal (the identity scope still works).
  try {
    const data = await authedGet("/api/applications");
    state.applications = data.applications || [];
  } catch (_) {
    /* keep whatever applications we already had */
  }
  populateDocScope();
  loadDocuments();
}

// DOC_SCOPE_ALL is the sentinel scope value for the aggregated "All my documents"
// view — a union of the applicant's identity + every one of their applications.
const DOC_SCOPE_ALL = "__all__";

// docScopeOwners returns the owner keys a scope reads: the union (identity + every
// application) for DOC_SCOPE_ALL, or just the one key for a single-record scope.
function docScopeOwners(scope) {
  if (scope !== DOC_SCOPE_ALL) return [scope];
  return [state.applicant, ...state.applications.map((a) => a.entityKey)];
}

// docScopeLabel names an owner key for a card in the aggregated view: the
// applicant's identity, or the application's unit address.
function docScopeLabel(ownerKey) {
  if (ownerKey === state.applicant) return "Your identity";
  const a = state.applications.find((x) => x.entityKey === ownerKey);
  if (a) return "Application · " + (a.unitAddress || (a.unitKey ? shortKey(a.unitKey) : shortKey(a.entityKey)));
  return shortKey(ownerKey);
}

// populateDocScope rebuilds the scope <select>: an aggregated "All my documents"
// view first (only when the applicant has applications, so it adds something
// beyond identity-only), then the applicant's identity, then one option per
// application (value = the owner key the documents link to).
function populateDocScope() {
  const sel = $("#doc-scope");
  const prev = state.docScope;
  sel.innerHTML = "";
  const opt = (value, label) => {
    const o = document.createElement("option");
    o.value = value;
    o.textContent = label;
    sel.append(o);
  };
  if (state.applications.length > 0) opt(DOC_SCOPE_ALL, "All my documents");
  opt(state.applicant, "Your identity (" + nameFor(state.applicant) + ")");
  for (const a of state.applications) {
    const label = a.unitAddress || (a.unitKey ? shortKey(a.unitKey) : shortKey(a.entityKey));
    opt(a.entityKey, "Application · " + label);
  }
  // Keep the previous selection if it still exists, else default to identity.
  const values = Array.from(sel.options).map((o) => o.value);
  state.docScope = prev && values.includes(prev) ? prev : state.applicant;
  sel.value = state.docScope;
  syncUploadAvailability();
}

// syncUploadAvailability disables the upload form for the aggregated view — a
// document attaches to one specific record, not to the union — and explains why.
function syncUploadAvailability() {
  const all = state.docScope === DOC_SCOPE_ALL;
  const submit = $("#upload-submit");
  if (submit) {
    submit.disabled = all;
    submit.title = all ? "Pick a specific record (identity or an application) to attach a document to" : "";
  }
}

async function loadDocuments() {
  const grid = $("#docs");
  const empty = $("#docs-empty");
  const scope = state.docScope;
  if (!scope) {
    grid.innerHTML = "";
    state.docs = [];
    return;
  }
  $("#docs-summary").textContent = "loading…";
  const query = docScopeOwners(scope)
    .filter(Boolean)
    .map((o) => "owner=" + encodeURIComponent(o))
    .join("&");
  try {
    // Documents-tab owners are always identity/leaseapp keys (never a unit), so
    // this list is D1.5-protected server-side — authedGet, mirroring
    // loadLandlordRLS/handleApplications.
    const data = await authedGet("/api/objects?" + query);
    state.docs = data.documents || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load documents: " + e.message;
    $("#docs-summary").textContent = "";
    return;
  }
  renderDocuments();
}

function renderDocuments() {
  const grid = $("#docs");
  const empty = $("#docs-empty");
  grid.innerHTML = "";
  if (state.docs.length === 0) {
    empty.hidden = false;
    empty.textContent = "No documents yet. Upload an ID, proof of income, or signed lease above.";
    $("#docs-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const d of state.docs) grid.append(renderDocCard(d));
  const n = state.docs.length;
  $("#docs-summary").textContent = `${n} document${n === 1 ? "" : "s"}`;
}

function fmtSize(n) {
  if (typeof n !== "number" || n < 0) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / (1024 * 1024)).toFixed(1) + " MB";
}

function renderDocCard(d) {
  const card = document.createElement("div");
  card.className = "card doc-card";

  const sess = state.sessionUploads[d.oid];
  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = sess && DOC_SLOTS[sess.linkName] ? DOC_SLOTS[sess.linkName] : "Document";

  const meta = document.createElement("div");
  meta.className = "addr-sub";
  meta.textContent = [d.contentType || "file", fmtSize(d.size)].filter(Boolean).join("  ·  ");

  // In the aggregated "All my documents" view, name the record each doc belongs to.
  let scopeLine = null;
  if (state.docScope === DOC_SCOPE_ALL) {
    scopeLine = document.createElement("div");
    scopeLine.className = "addr-sub";
    scopeLine.textContent = "📁 " + docScopeLabel(d.ownerKey);
  }

  const ref = document.createElement("div");
  ref.className = "addr-sub mono";
  ref.textContent = d.oid;

  const actions = document.createElement("div");
  actions.className = "card-actions";
  // A document's bytes are D1.5-protected (identity/leaseapp owner), so a plain
  // <a href> navigation (no Authorization header) would 404. Fetch it with the
  // Bearer token and open the result as a blob URL instead.
  const view = document.createElement("button");
  view.className = "ghost btn-link";
  view.textContent = "View";
  view.addEventListener("click", () => openDocument(d.oid));
  actions.append(view);

  // Detach is available for documents uploaded this session (the FE knows the
  // link name); a doc listed from a prior session has no link name in the read
  // model, so detach of those is a documented follow-up.
  if (sess) {
    const detach = document.createElement("button");
    detach.className = "ghost danger";
    detach.textContent = "Detach";
    detach.addEventListener("click", () => detachDoc(d.oid, sess));
    actions.append(detach);
  }

  card.append(title, meta);
  if (scopeLine) card.append(scopeLine);
  card.append(ref, actions);
  return card;
}

async function submitUpload(ev) {
  ev.preventDefault();
  if (!state.applicant) {
    toast("Select an applicant first.", "err");
    return;
  }
  const scope = state.docScope;
  if (!scope) {
    toast("Choose what to attach the document to.", "err");
    return;
  }
  if (scope === DOC_SCOPE_ALL) {
    toast("Pick a specific record (identity or an application) to attach a document to.", "err");
    return;
  }
  const slot = $("#doc-slot").value;
  const fileInput = $("#doc-file");
  const file = fileInput.files && fileInput.files[0];
  if (!file) {
    toast("Choose a file to upload.", "err");
    return;
  }

  const fd = new FormData();
  fd.append("file", file);
  fd.append("targetKey", scope);
  fd.append("linkName", slot);

  const submit = $("#upload-submit");
  submit.disabled = true;
  try {
    const reply = await api("/api/objects", { method: "POST", body: fd });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Upload rejected — " + msg, "err");
      return;
    }
    if (reply && reply.oid) {
      state.sessionUploads[reply.oid] = { linkName: slot, ownerKey: scope };
    }
    fileInput.value = "";
    toast("Document uploaded.", "ok", reply && reply.oid ? reply.oid : "");
    // The lens may take a moment to project; a Refresh shows it once projected.
    loadDocuments();
  } catch (e) {
    toast("Could not upload: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

async function detachDoc(oid, sess) {
  if (!confirm("Detach this document? The file is removed from this record.")) return;
  try {
    const q = "?targetKey=" + encodeURIComponent(sess.ownerKey) + "&linkName=" + encodeURIComponent(sess.linkName);
    const reply = await api("/api/objects/" + encodeURIComponent(oid) + q, { method: "DELETE" });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not detach — " + msg, "err");
      return;
    }
    delete state.sessionUploads[oid];
    toast("Document detached.", "ok");
    loadDocuments();
  } catch (e) {
    toast("Could not detach: " + e.message, "err");
  }
}

// ---- Landlord — my units ----
//
// The by-unit aggregate from /api/unit-applications (P5: three lens read models,
// never Core KV): every listed unit and the live applications against it. The
// landlord posts a listing (a CreateLocation → SetUnitAddress → SetListing chain)
// and decides a qualified application (DecideLeaseApplication approve/decline) — the
// human-in-the-loop the convergence lens now gates the lease behind.

// DISPOSITION maps an applicantSummary.status to its badge label + class.
const DISPOSITION = {
  leased: { label: "Leased", cls: "leased" },
  approved: { label: "Approved — leasing", cls: "approved" },
  qualified: { label: "Qualified — awaiting decision", cls: "qualified" },
  declined: { label: "Declined", cls: "declined" },
  in_review: { label: "In review", cls: "review" },
};

// Ranking for the landlord by-unit view: a unit's competing applicants are ordered
// best-first so the landlord can compare at a glance rather than reading an arbitrary
// NanoID order. Pure FE over the already-projected disposition + qualification signals
// (no new lens/data). Tier by status (the resolved winner up top, declined to the
// bottom), then by a qualification score, then leaseAppKey for a stable order.
const STATUS_RANK = { leased: 0, approved: 1, qualified: 2, in_review: 3, declined: 4 };

function qualScore(a) {
  let s = 0;
  if (a.qualified) s += 100;
  if (a.signed) s += 40;
  if (a.incomeToRentMet === true) s += 30;
  if (a.employmentVerified === true) s += 15;
  if (typeof a.referenceCount === "number") s += Math.min(a.referenceCount, 5) * 3;
  if (a.hasGuarantor === true) s += 5;
  if (a.hasCoApplicant === true) s += 3;
  if (a.profileSubmitted) s += 2;
  return s;
}

function rankApplications(apps) {
  return apps.slice().sort((x, y) => {
    const tx = STATUS_RANK[x.status] ?? 9;
    const ty = STATUS_RANK[y.status] ?? 9;
    if (tx !== ty) return tx - ty;
    const sx = qualScore(x);
    const sy = qualScore(y);
    if (sx !== sy) return sy - sx;
    return (x.leaseAppKey || "").localeCompare(y.leaseAppKey || "");
  });
}

// moneyAmount formats a bare rent number (the by-unit row carries no currency) as a
// USD-style figure; the listings in this demo are USD.
function moneyAmount(n) {
  return typeof n === "number" ? "$" + n.toLocaleString() : "—";
}

// loadLandlord reads the operator console from /api/unit-applications as the
// SIGNED-IN identity (D1.5: this used to be an unauthenticated, system-wide read
// — every landlord's units and every applicant's PII/qualification signals, to
// any caller). The server now scopes the response to the units the RLS-enforced
// read_landlord_lease_applications model says this actor manages, so the picker
// selection IS the landlord sign-in, same as the applicant flows.
async function loadLandlord() {
  const grid = $("#units");
  const empty = $("#units-empty");
  if (!state.applicant) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Select your identity above to sign in and see the units you manage.";
    $("#units-summary").textContent = "";
    return;
  }
  $("#units-summary").textContent = "loading…";
  try {
    const data = await authedGet("/api/unit-applications");
    state.units = data.units || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load units: " + e.message;
    $("#units-summary").textContent = "";
    return;
  }
  renderUnits();
  loadLandlordRLS();
}

// loadLandlordRLS exercises the D1.3 Increment-3 PROTECTED read boundary: the
// signed-in identity reads /api/landlord/applications as an AUTHENTICATED actor, and
// Postgres RLS returns ONLY the applications to units that identity manages —
// the SAME scope the operator console above now enforces (D1.5). This adds the
// RICH read-only card view (qualification-profile signals via renderQualification)
// as a second rendering of that same RLS-scoped set; it stays INFORMATIONAL — no
// Approve/Decline (those live on the operator console above). Best-effort: a
// missing read boundary (no Postgres / protected lens) degrades to an
// informational note, never blocking the operator console.
async function loadLandlordRLS() {
  const el = $("#landlord-rls");
  const listEl = $("#landlord-rls-units");
  if (!el) return;
  if (!state.applicant) {
    el.hidden = true;
    if (listEl) listEl.hidden = true;
    return;
  }
  try {
    const data = await authedGet("/api/landlord/applications");
    const units = data.units || [];
    const apps = data.applicationCount || 0;
    el.hidden = false;
    if (units.length === 0) {
      // 0 can mean "manages nothing" OR "grant revoked" — both correctly return
      // empty (no oracle); state the scope, not a cause we cannot distinguish here.
      el.textContent = "🔒 RLS read boundary: Postgres scopes you to 0 units.";
      if (listEl) { listEl.hidden = true; listEl.innerHTML = ""; }
      return;
    }
    el.textContent = `🔒 RLS read boundary: Postgres scopes you to ${units.length} unit${units.length === 1 ? "" : "s"} you manage (${apps} application${apps === 1 ? "" : "s"}) — the operator console above is sourced from this same RLS-scoped read and is where you Approve/Decline.`;
    renderLandlordRLSUnits(units);
  } catch (e) {
    // The boundary is not provisioned in every dev posture; do not break the view.
    // Show a fixed string (server wording is for the console/log, not the banner).
    console.warn("landlord RLS boundary unavailable:", e);
    el.hidden = false;
    el.textContent = "🔒 RLS read boundary unavailable in this dev posture.";
    if (listEl) { listEl.hidden = true; listEl.innerHTML = ""; }
  }
}

// renderLandlordRLSUnits renders the RLS-scoped units + applications as a
// read-only card list — the enforced-scope counterpart of renderUnitCard, minus
// Approve/Decline (informational only, per D1.5 Rec C).
function renderLandlordRLSUnits(units) {
  const listEl = $("#landlord-rls-units");
  if (!listEl) return;
  listEl.innerHTML = "";
  listEl.hidden = false;
  for (const u of units) listEl.append(renderRLSUnitCard(u));
}

function renderRLSUnitCard(u) {
  const card = document.createElement("div");
  card.className = "card unit-card rls-card";

  const head = document.createElement("div");
  head.className = "unit-head";
  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = u.unitAddress || "Unit " + shortKey(u.unitKey);
  const sub = document.createElement("div");
  sub.className = "unit-sub";
  const rent = document.createElement("span");
  rent.textContent = u.unitRent != null ? moneyAmount(u.unitRent) + " / month" : "—";
  const status = u.unitStatus || "—";
  const badge = document.createElement("span");
  badge.className = "badge " + status;
  badge.textContent = status;
  sub.append(rent, badge);
  head.append(addr, sub);
  card.append(head);

  const list = document.createElement("div");
  list.className = "applicants";
  const apps = u.applications || [];
  if (apps.length === 0) {
    const none = document.createElement("div");
    none.className = "applicant-none";
    none.textContent = "No applications yet.";
    list.append(none);
  } else {
    for (const a of apps) list.append(renderRLSApplicantRow(a));
  }
  card.append(list);
  return card;
}

// renderRLSApplicantRow renders one RLS-scoped application: name falls back to a
// short key (the applicant's name is a Vault-gated sensitive field this protected
// lens does not carry, §3 of the design doc), the landlord's disposition, and the
// SAME qualification-profile chips the trusted console shows (renderQualification —
// D1.5 Rec C, informational, no aggregation/readiness gate). No Approve/Decline
// here; that stays on the trusted console (unitLeased / a.qualified gating out of
// scope for this informational read).
function renderRLSApplicantRow(a) {
  const row = document.createElement("div");
  row.className = "applicant";

  const info = document.createElement("div");
  info.className = "applicant-info";
  const name = document.createElement("span");
  name.className = "applicant-name";
  name.textContent = shortKey(a.applicant);
  info.append(name);
  if (a.landlordApproved) info.append(dispChip("Approved — leasing", "approved"));
  else if (a.landlordDeclined) info.append(dispChip("Declined", "declined"));
  else info.append(dispChip("Awaiting your decision", "review"));
  if (a.signedAt) {
    const signed = document.createElement("span");
    signed.className = "signed";
    signed.textContent = "✓ signed";
    info.append(signed);
  }
  row.append(info);

  row.append(renderQualification(a));

  if (a.landlordDeclined && a.declineReason) {
    const reason = document.createElement("div");
    reason.className = "applicant-note";
    reason.textContent = "Reason: " + a.declineReason;
    row.append(reason);
  }
  return row;
}

// dispChip builds the small disposition badge renderRLSApplicantRow uses (the DISPOSITION-styled
// class names so the existing CSS applies with no new rules).
function dispChip(text, cls) {
  const badge = document.createElement("span");
  badge.className = "disp " + cls;
  badge.textContent = text;
  return badge;
}

function renderUnits() {
  const grid = $("#units");
  const empty = $("#units-empty");
  grid.innerHTML = "";
  if (state.units.length === 0) {
    empty.hidden = false;
    empty.textContent = "No units listed yet. Post a listing to get started.";
    $("#units-summary").textContent = "";
    return;
  }
  empty.hidden = true;
  for (const u of state.units) grid.append(renderUnitCard(u));
  const n = state.units.length;
  $("#units-summary").textContent = `${n} unit${n === 1 ? "" : "s"}`;
}

function renderUnitCard(u) {
  const card = document.createElement("div");
  card.className = "card unit-card";

  const head = document.createElement("div");
  head.className = "unit-head";
  const addr = document.createElement("div");
  addr.className = "addr";
  addr.textContent = u.unitAddress || "Unit " + shortKey(u.unitKey);
  const sub = document.createElement("div");
  sub.className = "unit-sub";
  const rent = document.createElement("span");
  rent.textContent = u.unitRent != null ? moneyAmount(u.unitRent) + " / month" : "—";
  const status = u.unitStatus || "—";
  const badge = document.createElement("span");
  badge.className = "badge " + status;
  badge.textContent = status;
  sub.append(rent, badge);
  head.append(addr, sub);

  const count = document.createElement("div");
  count.className = "unit-count";
  count.textContent = u.applicationCount === 1 ? "1 application" : `${u.applicationCount} applications`;

  const photoBtn = document.createElement("button");
  photoBtn.className = "ghost";
  photoBtn.textContent = "📷 Photos";
  photoBtn.title = "Add or remove listing photos";
  photoBtn.addEventListener("click", () => openManagePhotos(u.unitKey, u.unitAddress || shortKey(u.unitKey)));
  const meta = document.createElement("div");
  meta.className = "unit-meta-row";
  meta.append(count, photoBtn);

  // Edit is offered whenever the unit has a listing to edit (full economics come
  // down on u.listing/u.address for the pre-fill).
  if (u.listing) {
    const editBtn = document.createElement("button");
    editBtn.className = "ghost";
    editBtn.textContent = "✎ Edit listing";
    editBtn.title = "Fix the rent, address, or other details";
    editBtn.addEventListener("click", () => openEditListing(u));
    meta.append(editBtn);
  }
  // Off-market: a landlord pulls a vacancy (renovating, sold, listed elsewhere)
  // without faking a lease. Available/pending → Unpublish (withdrawn, hidden from
  // applicant Browse); withdrawn → Relist (back to available). A leased unit shows
  // neither (it's occupied; the convergence flow owns its status).
  if (status === "available" || status === "pending") {
    const offBtn = document.createElement("button");
    offBtn.className = "ghost danger";
    offBtn.textContent = "Unpublish";
    offBtn.title = "Take this unit off-market (hidden from applicants); relist anytime";
    offBtn.addEventListener("click", () => setListingStatus(u, "withdrawn"));
    meta.append(offBtn);
  } else if (status === "withdrawn") {
    const relistBtn = document.createElement("button");
    relistBtn.className = "ghost";
    relistBtn.textContent = "Relist";
    relistBtn.title = "Put this unit back on the market";
    relistBtn.addEventListener("click", () => setListingStatus(u, "available"));
    meta.append(relistBtn);
  }

  card.append(head, meta);

  const list = document.createElement("div");
  list.className = "applicants";
  if (!u.applications || u.applications.length === 0) {
    const none = document.createElement("div");
    none.className = "applicant-none";
    none.textContent = "No applications yet.";
    list.append(none);
  } else {
    const ranked = rankApplications(u.applications);
    // When more than one applicant competes for a unit, flag the top-ranked one that
    // is still awaiting the landlord's decision so the strongest live candidate stands
    // out — the decision-support the ranking exists for.
    const topMatchKey =
      ranked.length > 1 ? (ranked.find((a) => a.status === "qualified") || {}).leaseAppKey : undefined;
    for (const a of ranked) {
      list.append(renderApplicantRow(a, u, !!a.leaseAppKey && a.leaseAppKey === topMatchKey));
    }
  }
  card.append(list);
  return card;
}

function renderApplicantRow(a, unit, isTopMatch) {
  const row = document.createElement("div");
  row.className = "applicant";

  const info = document.createElement("div");
  info.className = "applicant-info";
  const name = document.createElement("span");
  name.className = "applicant-name";
  name.textContent = a.applicantName || shortKey(a.applicant);
  const disp = DISPOSITION[a.status] || { label: a.status || "—", cls: "review" };
  const badge = document.createElement("span");
  badge.className = "disp " + disp.cls;
  badge.textContent = disp.label;
  info.append(name, badge);
  if (a.signed) {
    const signed = document.createElement("span");
    signed.className = "signed";
    signed.textContent = "✓ signed";
    info.append(signed);
  }
  if (isTopMatch) {
    const top = document.createElement("span");
    top.className = "top-match";
    top.textContent = "★ Best match";
    top.title = "Highest-ranked applicant awaiting your decision";
    info.append(top);
  }
  row.append(info);

  // The qualification profile the landlord decides on — derived signals only
  // (never the raw financials). Absent until the applicant submits a profile.
  row.append(renderQualification(a));

  const unitLeased = unit.unitStatus === "leased";
  if (a.qualified && !unitLeased) {
    const actions = document.createElement("div");
    actions.className = "applicant-actions";
    const approve = document.createElement("button");
    approve.textContent = "Approve";
    approve.addEventListener("click", () => decideApplication(a, "approved"));
    const decline = document.createElement("button");
    decline.className = "ghost danger";
    decline.textContent = "Decline";
    decline.addEventListener("click", () => decideApplication(a, "declined"));
    actions.append(approve, decline);
    row.append(actions);
  } else if (unitLeased && a.status !== "leased" && a.status !== "declined") {
    const note = document.createElement("div");
    note.className = "applicant-note";
    note.textContent = "Unit leased to another applicant.";
    row.append(note);
  }
  // Echo a landlord's decline reason back on the by-unit row so the landlord sees
  // the rationale they recorded (declineReason is set only on a landlord decline).
  if (a.landlordDeclined && a.declineReason) {
    const reason = document.createElement("div");
    reason.className = "applicant-note";
    reason.textContent = "Reason: " + a.declineReason;
    row.append(reason);
  }
  return row;
}

// renderQualification builds the compact qualification line the landlord reads to
// decide. It renders only the DERIVED signals the lens projects (income meets 3×
// rent, employment verified, reference count, co-applicant, guarantor) — never the
// raw financials. Until the applicant submits a profile it shows a muted "no
// qualification profile yet" so the landlord knows the decision is blind.
function renderQualification(a) {
  const wrap = document.createElement("div");
  wrap.className = "qualification";
  if (!a.profileSubmitted) {
    wrap.classList.add("none");
    wrap.textContent = "No qualification profile submitted yet";
    return wrap;
  }
  const chip = (text, cls) => {
    const c = document.createElement("span");
    c.className = "qual-chip " + cls;
    c.textContent = text;
    return c;
  };
  // Income vs 3× rent. null = unknown (no listing rent at submit time).
  if (a.incomeToRentMet === true) wrap.append(chip("✓ Income ≥ 3× rent", "ok"));
  else if (a.incomeToRentMet === false) wrap.append(chip("✗ Income < 3× rent", "bad"));
  else wrap.append(chip("Income/rent unknown", "muted"));
  // Employment.
  if (a.employmentVerified === true) wrap.append(chip("✓ Employed", "ok"));
  else if (a.employmentVerified === false) wrap.append(chip("Unverified income", "muted"));
  // References.
  if (typeof a.referenceCount === "number") {
    wrap.append(chip(a.referenceCount === 1 ? "1 reference" : `${a.referenceCount} references`, a.referenceCount > 0 ? "ok" : "muted"));
  }
  if (a.hasCoApplicant === true) wrap.append(chip("+ Co-applicant", "ok"));
  // A guarantor's whole point is covering a thin-income applicant, so when the
  // guarantor's own income is known, say whether it meets 3× rent rather than a
  // bare "+ Guarantor". null = no guarantor income provided.
  if (a.hasGuarantor === true) {
    if (a.guarantorIncomeToRentMet === true) wrap.append(chip("✓ Guarantor covers 3× rent", "ok"));
    else if (a.guarantorIncomeToRentMet === false) wrap.append(chip("Guarantor income < 3× rent", "muted"));
    else wrap.append(chip("+ Guarantor", "ok"));
  }
  return wrap;
}

// decideApplication records the landlord's approve/decline (DecideLeaseApplication)
// for a qualified application, then reloads after a beat so the new disposition (and
// any unit-leased flip the convergence lens drives) shows once reprojected.
async function decideApplication(a, decision) {
  const who = a.applicantName || shortKey(a.applicant);
  // A decline prompts for an optional reason (applicant feedback + a fair-housing
  // record). Cancelling the prompt aborts the decline; an empty reason still declines.
  const payload = { leaseAppKey: a.leaseAppKey, decision };
  if (decision === "declined") {
    const reason = prompt(`Decline ${who}'s application?\n\nOptional reason (shown to the applicant):`, "");
    if (reason === null) return;
    const trimmed = reason.trim();
    if (trimmed) payload.reason = trimmed;
  }
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "DecideLeaseApplication",
        class: "leaseapp",
        reads: [a.leaseAppKey],
        payload,
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Decision rejected — " + msg, "err");
      return;
    }
    toast(decision === "approved" ? "Application approved." : "Application declined.", "ok");
    setTimeout(loadLandlord, 800);
  } catch (e) {
    toast("Could not record decision: " + e.message, "err");
  }
}

// ---- Post / edit a listing (landlord) ----

function openPostListing() {
  state.editUnitKey = null;
  state.editStatus = null;
  $("#listing-form").reset();
  $("#li-currency").value = "USD";
  $("#listing-title").textContent = "Post a listing";
  $("#listing-sub").textContent = "Create a unit and list it for lease.";
  $("#listing-submit").textContent = "Post listing";
  $("#li-photos-field").hidden = false; // photos only on post (a new unit)
  $("#listing-overlay").hidden = false;
  $("#li-line1").focus();
}

// openEditListing reuses the post-listing modal in EDIT mode: it pre-fills every
// field from the unit's projected listing/address (u.listing / u.address from
// /api/unit-applications) and, on submit, skips CreateLocation and re-runs
// SetUnitAddress + SetListing against the existing unit. The current status is
// preserved (editStatus) so a fix to a withdrawn or leased unit never silently
// relists it. Photos are managed via the 📷 button, so the photo field is hidden.
function openEditListing(u) {
  const li = u.listing || {};
  const ad = u.address || {};
  state.editUnitKey = u.unitKey;
  state.editStatus = li.status || u.unitStatus || "available";
  $("#listing-form").reset();
  $("#li-line1").value = ad.line1 || "";
  $("#li-line2").value = ad.line2 || "";
  $("#li-city").value = ad.city || "";
  $("#li-region").value = ad.region || "";
  $("#li-postal").value = ad.postal || "";
  $("#li-rent").value = li.rentAmount != null ? li.rentAmount : "";
  $("#li-currency").value = li.rentCurrency || "USD";
  $("#li-bedrooms").value = li.bedrooms != null ? li.bedrooms : "";
  // availableFrom is RFC3339 (e.g. 2026-08-01T00:00:00Z); a <input type=date> wants YYYY-MM-DD.
  $("#li-availfrom").value = (li.availableFrom || "").slice(0, 10);
  $("#li-leaseterm").value = li.leaseTermMonths != null ? li.leaseTermMonths : "";
  $("#li-bathrooms").value = li.bathrooms != null ? li.bathrooms : "";
  $("#li-sqft").value = li.sqft != null ? li.sqft : "";
  $("#listing-title").textContent = "Edit listing";
  $("#listing-sub").textContent = u.unitAddress || shortKey(u.unitKey);
  $("#listing-submit").textContent = "Save changes";
  $("#li-photos-field").hidden = true;
  $("#listing-overlay").hidden = false;
  $("#li-line1").focus();
}

function closePostListing() {
  $("#listing-overlay").hidden = true;
  state.editUnitKey = null;
  state.editStatus = null;
}

// setListingStatus flips a unit's listing status (SetListingStatus, status-only —
// the economics are preserved) for the landlord Unpublish / Relist actions, then
// reloads after a beat so the new disposition shows once reprojected.
async function setListingStatus(u, status) {
  try {
    const reply = await api("/api/op", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        operationType: "SetListingStatus",
        class: "loftspaceListing",
        reads: [u.unitKey],
        payload: { unit: u.unitKey, status },
      }),
    });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not change status — " + msg, "err");
      return;
    }
    toast(status === "withdrawn" ? "Unit taken off-market." : "Unit relisted.", "ok");
    setTimeout(loadLandlord, 800);
  } catch (e) {
    toast("Could not change status: " + e.message, "err");
  }
}

// opOrThrow submits an op and throws on a rejection or transport error, so the
// post-a-listing chain stops at the first failure with a message naming the step.
async function opOrThrow(body, what) {
  const reply = await api("/api/op", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (reply && reply.status === "rejected") {
    const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
    throw new Error(`Could not ${what} — ${msg}`);
  }
  return reply || {};
}

// submitPostListing runs the three-op chain that mints + lists a unit: CreateLocation
// (the reply's primaryKey is the new vtx.unit key) → SetUnitAddress → SetListing. Each
// step awaits the prior since the address/listing target the unit the first op mints.
async function submitPostListing(ev) {
  ev.preventDefault();
  const line1 = $("#li-line1").value.trim();
  const line2 = $("#li-line2").value.trim();
  const city = $("#li-city").value.trim();
  const region = $("#li-region").value.trim();
  const postal = $("#li-postal").value.trim();
  const rent = Number($("#li-rent").value);
  const currency = $("#li-currency").value.trim() || "USD";
  const bedrooms = $("#li-bedrooms").value;
  const availFrom = $("#li-availfrom").value; // "YYYY-MM-DD" from a date input
  const leaseTerm = $("#li-leaseterm").value;
  const bathrooms = $("#li-bathrooms").value;
  const sqft = $("#li-sqft").value;

  if (!line1 || !city || !region || !postal) {
    toast("Fill in the full address (line 1, city, region, postal).", "err");
    return;
  }
  if (!(rent > 0)) {
    toast("Enter a monthly rent greater than zero.", "err");
    return;
  }
  if (bedrooms === "") {
    toast("Enter the number of bedrooms.", "err");
    return;
  }
  if (!availFrom) {
    toast("Pick the date the unit is available from.", "err");
    return;
  }
  if (!(Number(leaseTerm) > 0)) {
    toast("Enter a lease term in months.", "err");
    return;
  }

  const editing = !!state.editUnitKey;
  const submit = $("#listing-submit");
  submit.disabled = true;
  try {
    // EDIT reuses the existing unit; POST mints one (CreateLocation → its
    // primaryKey is the new vtx.unit key).
    let unitKey = state.editUnitKey;
    if (!editing) {
      const created = await opOrThrow(
        { operationType: "CreateLocation", class: "location", payload: { locationType: "unit" } },
        "create the unit",
      );
      unitKey = created.primaryKey;
      if (!unitKey) {
        toast("The unit was created but returned no key; try Refresh.", "err");
        return;
      }
    }

    const addr = { unit: unitKey, line1, city, region, postal };
    if (line2) addr.line2 = line2;
    await opOrThrow(
      { operationType: "SetUnitAddress", class: "loftspaceListing", reads: [unitKey], payload: addr },
      "set the address",
    );

    const listing = {
      unit: unitKey,
      rentAmount: rent,
      rentCurrency: currency,
      bedrooms: Number(bedrooms),
      availableFrom: availFrom + "T00:00:00Z", // SetListing wants RFC3339
      leaseTermMonths: Number(leaseTerm),
      // POST defaults to available; EDIT preserves the unit's current status so a
      // fix never silently relists a withdrawn unit or un-leases a leased one.
      status: editing ? state.editStatus || "available" : "available",
    };
    if (bathrooms !== "") listing.bathrooms = Number(bathrooms);
    if (sqft !== "") listing.sqft = Number(sqft);
    await opOrThrow(
      { operationType: "SetListing", class: "loftspaceListing", reads: [unitKey], payload: listing },
      editing ? "save the listing" : "create the listing",
    );

    if (editing) {
      closePostListing();
      toast("Listing updated.", "ok", unitKey);
      setTimeout(loadLandlord, 800);
      return;
    }

    // Photos are best-effort (POST only): the listing is already posted, so a
    // failed photo upload warns but never unwinds the unit. The new unit
    // invalidates its (empty) photo cache so Browse refetches.
    const files = Array.from(($("#li-photos").files) || []);
    let uploaded = 0;
    for (const f of files) {
      try {
        await uploadUnitPhoto(unitKey, f);
        uploaded++;
      } catch (e) {
        toast("A photo did not upload: " + e.message, "err");
      }
    }
    delete state.unitPhotos[unitKey];

    closePostListing();
    toast(files.length ? `Listing posted with ${uploaded} photo${uploaded === 1 ? "" : "s"}.` : "Listing posted.", "ok", unitKey);
    setTimeout(loadLandlord, 800);
  } catch (e) {
    toast(e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Listing photos (landlord upload + applicant gallery) ----

// uploadUnitPhoto attaches one image to a unit as a listing photo: POST the bytes
// + AttachObject under linkName=listingPhoto, ownerKey=vtx.unit.<id> — the same
// generic objects-base plumbing the Documents tab uses, just a different owner.
// Rejects throw so the caller can count/report.
async function uploadUnitPhoto(unitKey, file) {
  const fd = new FormData();
  fd.append("file", file);
  fd.append("targetKey", unitKey);
  fd.append("linkName", PHOTO_LINK);
  const reply = await api("/api/objects", { method: "POST", body: fd });
  if (reply && reply.status === "rejected") {
    const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
    throw new Error(msg);
  }
  return reply;
}

// openLightbox shows a unit's photos full-size with prev/next + a thumbnail strip.
function openLightbox(unitKey, index) {
  const photos = photosFor(unitKey);
  if (photos.length === 0) return;
  state.lightbox = { photos, index: Math.max(0, Math.min(index, photos.length - 1)) };
  renderLightbox();
  $("#photo-overlay").hidden = false;
}

function closeLightbox() {
  $("#photo-overlay").hidden = true;
  state.lightbox = null;
}

function stepLightbox(delta) {
  if (!state.lightbox) return;
  const n = state.lightbox.photos.length;
  state.lightbox.index = (state.lightbox.index + delta + n) % n;
  renderLightbox();
}

function renderLightbox() {
  const lb = state.lightbox;
  if (!lb) return;
  const photos = lb.photos;
  $("#lb-img").src = photoSrc(photos[lb.index].oid);
  $("#lb-caption").textContent = `${lb.index + 1} of ${photos.length}`;
  const multi = photos.length > 1;
  $("#lb-prev").hidden = !multi;
  $("#lb-next").hidden = !multi;
  const strip = $("#lb-strip");
  strip.innerHTML = "";
  if (multi) {
    photos.forEach((p, i) => {
      const t = document.createElement("img");
      t.src = photoSrc(p.oid);
      t.className = "lb-thumb" + (i === lb.index ? " active" : "");
      t.loading = "lazy";
      t.addEventListener("click", () => {
        lb.index = i;
        renderLightbox();
      });
      strip.append(t);
    });
  }
}

// ---- Manage photos (landlord) ----

// openManagePhotos loads a unit's current photos into the manage modal and lets
// the landlord add or remove. Photos are read fresh (not the Browse cache) so the
// modal reflects the latest projection.
async function openManagePhotos(unitKey, label) {
  state.photoUnitKey = unitKey;
  $("#mp-unit").textContent = label || shortKey(unitKey);
  $("#mp-files").value = "";
  $("#photos-overlay").hidden = false;
  await reloadManagePhotos();
}

function closeManagePhotos() {
  $("#photos-overlay").hidden = true;
  state.photoUnitKey = null;
}

async function reloadManagePhotos() {
  const unitKey = state.photoUnitKey;
  if (!unitKey) return;
  const grid = $("#mp-grid");
  grid.innerHTML = "loading…";
  let photos = [];
  try {
    const data = await api("/api/objects?owner=" + encodeURIComponent(unitKey));
    photos = (data.documents || []).filter((d) => isImage(d.contentType));
  } catch (e) {
    grid.innerHTML = "";
    toast("Could not load photos: " + e.message, "err");
    return;
  }
  grid.innerHTML = "";
  if (photos.length === 0) {
    const none = document.createElement("div");
    none.className = "applicant-none";
    none.textContent = "No photos yet. Add some below.";
    grid.append(none);
    return;
  }
  for (const p of photos) {
    const cell = document.createElement("div");
    cell.className = "mp-cell";
    const img = document.createElement("img");
    img.src = photoSrc(p.oid);
    img.loading = "lazy";
    const rm = document.createElement("button");
    rm.className = "ghost danger";
    rm.textContent = "Remove";
    rm.addEventListener("click", () => removeUnitPhoto(p.oid));
    cell.append(img, rm);
    grid.append(cell);
  }
}

// removeUnitPhoto detaches a listing photo. The link name is deterministic
// (PHOTO_LINK) so even a photo uploaded in a prior session can be detached —
// unlike the Documents tab whose slot is ambiguous across sessions.
async function removeUnitPhoto(oid) {
  const unitKey = state.photoUnitKey;
  if (!unitKey) return;
  if (!confirm("Remove this photo from the listing?")) return;
  try {
    const q = "?targetKey=" + encodeURIComponent(unitKey) + "&linkName=" + encodeURIComponent(PHOTO_LINK);
    const reply = await api("/api/objects/" + encodeURIComponent(oid) + q, { method: "DELETE" });
    if (reply && reply.status === "rejected") {
      const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
      toast("Could not remove — " + msg, "err");
      return;
    }
    delete state.unitPhotos[unitKey];
    toast("Photo removed.", "ok");
    reloadManagePhotos();
  } catch (e) {
    toast("Could not remove: " + e.message, "err");
  }
}

async function submitAddPhotos() {
  const unitKey = state.photoUnitKey;
  if (!unitKey) return;
  const input = $("#mp-files");
  const files = Array.from(input.files || []);
  if (files.length === 0) {
    toast("Choose one or more photos to add.", "err");
    return;
  }
  const btn = $("#mp-add");
  btn.disabled = true;
  let uploaded = 0;
  for (const f of files) {
    try {
      await uploadUnitPhoto(unitKey, f);
      uploaded++;
    } catch (e) {
      toast("A photo did not upload: " + e.message, "err");
    }
  }
  input.value = "";
  delete state.unitPhotos[unitKey];
  btn.disabled = false;
  if (uploaded) toast(`Added ${uploaded} photo${uploaded === 1 ? "" : "s"}.`, "ok");
  // The lens takes a beat to project; reload after a short delay so the new
  // photos appear.
  setTimeout(reloadManagePhotos, 800);
}

// ---- wire up ----

function init() {
  restoreApplicant();
  restoreMode();
  loadIdentities();
  $("#applicant").addEventListener("change", (e) => setApplicant(e.target.value));
  $("#new-applicant").addEventListener("click", openNewApplicant);
  $("#applicant-cancel").addEventListener("click", closeNewApplicant);
  $("#applicant-overlay").addEventListener("click", (e) => {
    if (e.target === $("#applicant-overlay")) closeNewApplicant();
  });
  $("#applicant-form").addEventListener("submit", submitNewApplicant);
  $("#status").addEventListener("change", loadListings);
  $("#reload-listings").addEventListener("click", loadListings);
  // The filter/sort bar re-renders the already-loaded listings client-side — no fetch.
  $("#q-search").addEventListener("input", renderListings);
  $("#q-beds").addEventListener("change", renderListings);
  $("#q-maxrent").addEventListener("input", renderListings);
  $("#q-sort").addEventListener("change", renderListings);
  $("#clear-filters").addEventListener("click", () => {
    $("#q-search").value = "";
    $("#q-beds").value = "0";
    $("#q-maxrent").value = "";
    $("#q-sort").value = "rent-asc";
    renderListings();
  });
  $("#apply-cancel").addEventListener("click", closeApply);
  $("#apply-overlay").addEventListener("click", (e) => {
    if (e.target === $("#apply-overlay")) closeApply();
  });
  $("#moveInDate").addEventListener("input", syncTermRequirement);
  $("#apply-form").addEventListener("submit", submitApply);
  $("#tab-browse").addEventListener("click", () => showView("browse"));
  $("#tab-apps").addEventListener("click", () => showView("apps"));
  $("#tab-tasks").addEventListener("click", () => showView("tasks"));
  $("#tab-docs").addEventListener("click", () => showView("docs"));
  $("#reload-apps").addEventListener("click", loadApplications);
  $("#reload-tasks").addEventListener("click", loadTasks);
  $("#reload-docs").addEventListener("click", loadDocsView);
  $("#doc-scope").addEventListener("change", (e) => {
    state.docScope = e.target.value;
    syncUploadAvailability();
    loadDocuments();
  });
  $("#upload-form").addEventListener("submit", submitUpload);
  $("#complete-cancel").addEventListener("click", closeComplete);
  $("#complete-overlay").addEventListener("click", (e) => {
    if (e.target === $("#complete-overlay")) closeComplete();
  });
  $("#complete-form").addEventListener("submit", submitComplete);

  $("#mode-applicant").addEventListener("click", () => setMode("applicant"));
  $("#mode-landlord").addEventListener("click", () => setMode("landlord"));
  $("#post-listing").addEventListener("click", openPostListing);
  $("#reload-units").addEventListener("click", loadLandlord);
  $("#listing-cancel").addEventListener("click", closePostListing);
  $("#listing-overlay").addEventListener("click", (e) => {
    if (e.target === $("#listing-overlay")) closePostListing();
  });
  $("#listing-form").addEventListener("submit", submitPostListing);

  // Listing-photo lightbox (applicant) + manage-photos modal (landlord).
  $("#lb-close").addEventListener("click", closeLightbox);
  $("#lb-prev").addEventListener("click", () => stepLightbox(-1));
  $("#lb-next").addEventListener("click", () => stepLightbox(1));
  $("#photo-overlay").addEventListener("click", (e) => {
    if (e.target === $("#photo-overlay")) closeLightbox();
  });
  document.addEventListener("keydown", (e) => {
    if ($("#photo-overlay").hidden) return;
    if (e.key === "Escape") closeLightbox();
    else if (e.key === "ArrowLeft") stepLightbox(-1);
    else if (e.key === "ArrowRight") stepLightbox(1);
  });
  $("#mp-close").addEventListener("click", closeManagePhotos);
  $("#mp-add").addEventListener("click", submitAddPhotos);
  $("#photos-overlay").addEventListener("click", (e) => {
    if (e.target === $("#photos-overlay")) closeManagePhotos();
  });

  loadListings();
  applyMode();
}

document.addEventListener("DOMContentLoaded", init);
