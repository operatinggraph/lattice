"use strict";

// Clinic app — book · my appointments · provider schedule (Increment A). Vanilla
// JS, no build step. The Go server does all NATS I/O for reads: this view reads
// /api/providers + /api/staff/patients + /api/appointments. Writes
// (CreatePatient / CreateProvider / CreateAppointment / RescheduleAppointment /
// SetAppointmentStatus / ...) go browser-direct to the Gateway's
// POST /v1/operations via submitOp() (real-actor-write-auth-e2e-design.md §3.1).

const PATIENT_KEY = "clinic.patient";
const state = {
  patients: [], // append-only lookup cache — every patient the FE has ever seen, never shrinks (so an
  // already-selected patient's contact/self-token lookups survive a later ?q= filter)
  patientOptions: [], // the roster currently rendered in the #patient select — the full cache, or a
  // narrower ?q= match while a front-desk search is active
  providers: [],
  providerSearch: "", // #provider-search term, filters the booking picker's roster client-side (name/specialty substring)
  appts: [],
  schedule: [],
  followups: [], // every appointment whose documented visit requested a follow-up (clinic-wide worklist)
  series: [], // clinic-wide recurring visit series worklist (PROTECTED, staff wildcard, D1.5)
  mySeries: [], // the selected patient's own recurring visit series (PROTECTED, patient-self RLS, D1.5)
  ledger: null, // the selected patient's last-loaded /api/ledger response (billing history + balance)
  patient: null, // the selected patient key (the trusted-tool context)
  view: "book",
  highlight: null,
  rescheduling: null, // the appointment row being rescheduled (modal context)
  documenting: null, // { a, onDone } for the Document-visit (RecordEncounter) modal
  schedView: "week", // Schedule tab calendar mode: "week" | "day"
  schedAnchor: null, // a Date within the visible period (null → current week/day)
  schedSelected: null, // appointmentKey shown in the Schedule detail panel
  hoursDraft: [], // SetProviderHours windows being composed for the selected provider
  hoursProvider: null, // the provider key the draft is scoped to (reset on change)
  timeOffDraft: [], // SetProviderTimeOff ranges being edited (seeded from the provider's current ranges)
  timeOffProvider: null, // the provider key the time-off draft is scoped to (re-seeded on change)
  slotApptCache: {}, // providerKey -> existing appointments, for the booking slot picker (invalidated on book)
  slotPatientApptCache: {}, // patientKey -> the patient's appointments across all providers (cross-provider double-book exclusion; invalidated on book)
  slotCalAnchor: null, // UTC-midnight Date for the 1st of the month shown in the booking calendar (null → current UTC month)
};

const $ = (sel) => document.querySelector(sel);

// api issues a JSON request and throws Error(body.error) on an error response so
// callers can surface a single message.
async function api(path, opts) {
  const res = await fetch(path, opts);
  let body = null;
  try {
    body = await res.json();
  } catch (_) {
    /* empty/non-JSON body */
  }
  // A structured op reply carries `status` (accepted | rejected) and is returned
  // even on rejection — a rejected op is a domain outcome the caller branches on
  // via rejectionMessage()/friendlyBookingRejection(), not a transport error. Its
  // .error is an object {code, message}, which must NOT be thrown as-is (that
  // surfaces "[object Object]"). Only a real transport failure (!res.ok) or a
  // non-op error body throws — with a string message.
  if (body && typeof body.status === "string") {
    return body;
  }
  if (!res.ok || (body && body.error)) {
    const e = body && body.error;
    throw new Error((typeof e === "string" ? e : e && e.message) || `HTTP ${res.status}`);
  }
  return body;
}

// ---- Read-boundary token (D1.5) ----
// My Appointments is served from the PROTECTED clinicAppointmentsRead Postgres
// model as an authenticated actor: RLS returns only the signed-in patient's
// rows, so the request must carry a verified JWT (there is no client-side
// patient filter to forge — mirrors loftspace-app's D1.3 pattern). In the
// trusted-tool DEMO posture the app mints a short-lived token for the selected
// patient via POST /api/dev-token; a production deployment wires a real IdP and
// the FE would present that token instead. The token is cached per subject
// until shortly before expiry.
let readTokenCache = { subject: null, token: null, exp: 0 };

// bareId extracts the bare identity NanoID (the RLS principal / JWT subject)
// from a full vtx.patient.<id> (or vtx.identity.<id>) key.
function bareId(fullKey) {
  const i = (fullKey || "").lastIndexOf(".");
  return i >= 0 ? fullKey.slice(i + 1) : fullKey || "";
}

async function readToken() {
  if (!state.patient) return null;
  const subject = bareId(state.patient);
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
  if (!token) throw new Error("select a patient to sign in");
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

// providerReadToken is readToken's provider-anchored sibling (D1.5 Increment
// 2), minting a demo token for a given provider key instead of the selected
// patient. The Schedule tab's provider dropdown IS the "acting as" context here
// (the same trusted-tool minting semantics as the patient switcher), not a
// separate provider login flow — whichever provider is selected is who the
// desk is viewing the schedule as, mirroring readToken's per-subject cache.
let providerTokenCache = { subject: null, token: null, exp: 0 };

async function providerReadToken(providerKey) {
  const subject = bareId(providerKey);
  if (!subject) return null;
  const now = Date.now();
  if (providerTokenCache.subject === subject && providerTokenCache.token && now < providerTokenCache.exp - 60000) {
    return providerTokenCache.token;
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
  providerTokenCache = { subject, token: body.token, exp: Date.parse(body.expiresAt) || now + 5 * 60000 };
  return body.token;
}

// authedGetAsProvider is authedGet's provider-anchored sibling: fetches a
// protected endpoint with a Bearer token minted for providerKey, retrying once
// on a 401 with a freshly minted token.
async function authedGetAsProvider(path, providerKey) {
  let token = await providerReadToken(providerKey);
  if (!token) throw new Error("select a provider to view their schedule");
  try {
    return await api(path, { headers: { Authorization: "Bearer " + token } });
  } catch (e) {
    if (!/HTTP 401|authentication required/i.test(e.message)) throw e;
    providerTokenCache = { subject: null, token: null, exp: 0 };
    token = await providerReadToken(providerKey);
    if (!token) throw e;
    return api(path, { headers: { Authorization: "Bearer " + token } });
  }
}

// selfWriteToken (patient self-service booking) mints/caches a demo token for
// the selected patient's own LINKED IDENTITY — the capability-plane actor a
// consumer-scope write authenticates as (clinic-patient-self-service-booking-
// design.md Checkpoint item (a)). This is distinct from readToken(), which
// mints for the patient key itself and only ever serves the Postgres-RLS read
// boundary; the write path's actor is necessarily an identity (the capability
// plane resolves cap.identity.<id>, never cap.patient.<id> — see
// CreateAppointment's identifiedBy guard). Returns null when the selected
// patient has no linked identity (no self-service possible yet for them).
//
// The linked identity is CreateUnclaimedIdentity's result — nothing may ever
// authenticate AS it directly (it starts with no role); every actual request
// must run the claim ceremony (ensureClaimedDevice, below) first and
// authenticate as the resulting DEVICE identity instead. Minting a token for
// the identity's own key here (the old shortcut) left it permanently
// role-less: CreateAppointment's consumer scope=self grant never applied, and
// every self-service booking 403'd (mirrors gateway-claim-flow-identity-
// provisioning-design.md §11.1, same fix as LoftSpace's Apply claim ceremony).
let selfTokenCache = { subject: null, token: null, exp: 0 };

function patientIdentityKey() {
  const m = state.patients.find((p) => p.patientKey === state.patient);
  return (m && m.identityKey) || null;
}

async function selfWriteToken() {
  const identityKey = patientIdentityKey();
  if (!identityKey) return null;
  const subject = await ensureClaimedDevice(identityKey);
  const now = Date.now();
  if (selfTokenCache.subject === subject && selfTokenCache.token && now < selfTokenCache.exp - 60000) {
    return selfTokenCache.token;
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
  selfTokenCache = { subject, token: body.token, exp: Date.parse(body.expiresAt) || now + 5 * 60000 };
  return body.token;
}

// ---- Claim ceremony (gateway-claim-flow-identity-provisioning-design.md
// §11.1 Scenario A / §11.1a — mirrors loftspace-app's Apply fix) ----
//
// A patient's linked identity U (CreateUnclaimedIdentity's result) can never
// authenticate directly: U starts with no role, and ProvisionConsumerIdentity
// refuses to touch a vertex some other op already created. Every self-service
// write must instead authenticate as a separate DEVICE identity A that (1)
// auto-provisions with the consumer role on its first Gateway touch, then (2)
// calls ClaimIdentity to bind A -> U, which is what grants U the consumer role
// and lets the Gateway's credential-bindings resolution turn a request
// authenticated as A into env.Actor = U.
const PATIENT_AUTH_KEY = "clinic.patientAuth"; // localStorage: {U-bareId: A-bareId}

function loadPatientAuthMap() {
  try {
    return JSON.parse(localStorage.getItem(PATIENT_AUTH_KEY) || "{}");
  } catch (_) {
    return {};
  }
}

function savePatientAuthEntry(uBareId, aBareId) {
  const m = loadPatientAuthMap();
  m[uBareId] = aBareId;
  localStorage.setItem(PATIENT_AUTH_KEY, JSON.stringify(m));
}

// pendingClaimSecrets holds a freshly client-minted (not yet claimed) secret
// for a linked identity created THIS session (submitNewPatient) — in-memory
// only, mirroring §11.1a: "the client retains the plaintext; it is the single
// copy." A patient whose identity was linked in a prior session (or before
// this ceremony was wired up) has no pending secret and falls back to
// staff-gated RotateClaimKey (R4) to re-issue one.
const pendingClaimSecrets = {};

// mintDeviceToken mints a fresh, uncached dev-token for an arbitrary bare
// subject — the one-off device-identity (A) calls the claim ceremony makes,
// distinct from selfWriteToken()'s per-identity cache.
async function mintDeviceToken(subject) {
  const res = await fetch("/api/dev-token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ subject }),
  });
  if (!res.ok) throw new Error("mint device token: HTTP " + res.status);
  const body = await res.json();
  return body.token;
}

// postOpAsSubject submits an operation to the Gateway authenticated as a raw
// bare-id subject — used only by the claim ceremony, which must authenticate
// as the fresh device identity A itself (the Gateway's raw-credential
// carve-out for ClaimIdentity), not through the self/staff token caches.
async function postOpAsSubject(subject, body) {
  const [base, token] = await Promise.all([gatewayURL(), mintDeviceToken(subject)]);
  return api(base + "/v1/operations", {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
    body: JSON.stringify(body),
  });
}

// claimCeremonyInFlight de-dupes concurrent ensureClaimedDevice callers for
// the same linked identity onto the SAME promise — without this, two
// concurrent first-touches for one never-claimed identity would each mint
// their own device identity and race ClaimIdentity.
const claimCeremonyInFlight = {};

// ensureClaimedDevice runs the claim ceremony for identity uKey the first
// time it's needed and returns its device identity's bare id (A) — the
// subject every subsequent token for uKey mints against. Idempotent per uKey
// via the persisted patientAuth map (both across calls and across reloads).
async function ensureClaimedDevice(uKey) {
  const uBareId = bareId(uKey);
  const map = loadPatientAuthMap();
  if (map[uBareId]) return map[uBareId];
  if (claimCeremonyInFlight[uBareId]) return claimCeremonyInFlight[uBareId];
  const promise = runClaimCeremony(uKey, uBareId).finally(() => {
    delete claimCeremonyInFlight[uBareId];
  });
  claimCeremonyInFlight[uBareId] = promise;
  return promise;
}

async function runClaimCeremony(uKey, uBareId) {
  let secret = pendingClaimSecrets[uKey];
  if (!secret) {
    secret = mintClaimSecret();
    const newHash = await sha256Hex(secret);
    const rotateReply = await submitOp("RotateClaimKey", "identity", { identityKey: uKey, claimKeyHash: newHash }, [
      uKey,
      uKey + ".state",
      uKey + ".claimKey",
    ]);
    const rotateMsg = rejectionMessage(rotateReply);
    if (rotateMsg) throw new Error("could not prepare sign-in for this patient — " + rotateMsg);
  }

  const aBareId = await sha256NanoID(uBareId + ":device:" + mintClaimSecret());
  const aKey = "vtx.identity." + aBareId;
  const claimOp = {
    operationType: "ClaimIdentity",
    class: "identity",
    reads: [uKey, uKey + ".state", uKey + ".claimKey"],
    payload: { targetIdentityKey: uKey, claimKey: secret },
    authContext: { target: aKey },
  };

  // A's ProvisionConsumerIdentity pre-flight (the Gateway's provisionActorIfNeeded,
  // run inline just above by the fetch this postOpAsSubject makes) commits A's
  // consumer-role grant to Core KV synchronously, but the CapabilityAuthorizer
  // reads it via an asynchronously-projected Capability Lens — so THIS very next
  // call, submitted milliseconds later under the same brand-new actor, can race
  // ahead of that projection (isTransientAuthLag, below).
  let claimReply;
  for (let attempt = 0; ; attempt++) {
    claimReply = await postOpAsSubject(aBareId, claimOp);
    if (!isTransientAuthLag(claimReply) || attempt >= retryBackoffsMs.length) break;
    await new Promise((resolve) => setTimeout(resolve, retryBackoffsMs[attempt]));
  }
  const claimMsg = rejectionMessage(claimReply);
  if (claimMsg) throw new Error("could not sign in this patient — " + claimMsg);
  delete pendingClaimSecrets[uKey];
  savePatientAuthEntry(uBareId, aBareId);
  return aBareId;
}

// isTransientAuthLag reports whether a rejected reply is the known,
// architecturally-expected async-projection race — the Capability Lens or the
// credential-bindings materializer (both eventually-consistent CDC
// projections, lattice-architecture.md's documented <500ms p99 lag) catching
// up after a first-touch provision or claim, not yet visible to THIS
// immediately-following request. Distinguishes it from a genuine, persistent
// authorization denial, which should surface immediately rather than retry.
function isTransientAuthLag(reply) {
  if (!reply || reply.status !== "rejected" || !reply.error) return false;
  if (reply.error.code !== "AuthDenied") return false;
  const reason = reply.error.details && reply.error.details.reason;
  return reason === "NoCapabilityEntry" || reason === "OperationNotPermitted";
}

// retryBackoffsMs is the bounded backoff schedule the isTransientAuthLag
// retry loops in this file share — ~3s total, comfortably under the 5s
// deadline the codebase's own Go E2E poll helper
// (scripts/verify-real-actor-write-auth.go) uses for the same class of race.
const retryBackoffsMs = [200, 400, 800, 1600];

// staffReadToken (D1.5, the staff wildcard increment) mints/caches a demo
// token for the clinic-wide STAFF view via POST /api/staff/dev-token — unlike
// readToken/providerReadToken it carries no subject: the server mints for its
// own fixed admin actor, so the FE never needs to know (or be trusted to
// name) which identity holds the wildcard grant.
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
// retrying once on a 401 with a freshly minted token — the clinic-wide sibling
// of authedGet/authedGetAsProvider.
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

function shortKey(key) {
  const i = (key || "").lastIndexOf(".");
  return i >= 0 ? key.slice(i + 1) : key || "—";
}

// ---- op submit helper ----
//
// submitOp posts an op browser-direct to the Gateway's POST /v1/operations
// (real-actor-write-auth-e2e-design.md §3.1) instead of proxying through this
// app's own /api/op. Almost every clinic op is operator-only, so every write
// carries the staff Bearer token by default — the same actor (this app's
// admin identity) /api/op used to stamp server-side, now verified by the
// Gateway instead of assumed. CreateAppointment ALSO carries a real
// consumer-scope=self grant (clinic-patient-self-service-booking-design.md):
// passing opts.asSelf submits as the selected patient's own linked identity
// instead, with authContext.target naming that identity — the Gateway/Processor
// authorize the write as the real patient, not staff-on-their-behalf. Returns
// the reply (with .status) so callers can branch on rejected, same as before.
// selfWriteToken() resolves the patient's linked identity through the claim
// ceremony (ensureClaimedDevice), so an asSelf submit racing right behind a
// fresh claim gets the same bounded isTransientAuthLag retry that ceremony
// already uses — staff never races this (a long-lived, already-resolved
// actor), so it submits once.
let gatewayURLCache = null;
async function gatewayURL() {
  if (gatewayURLCache) return gatewayURLCache;
  const body = await api("/api/config");
  gatewayURLCache = body.gatewayUrl;
  return gatewayURLCache;
}

async function submitOp(operationType, klass, payload, reads, opts) {
  const asSelf = !!(opts && opts.asSelf);
  const [base, token] = await Promise.all([gatewayURL(), asSelf ? selfWriteToken() : staffReadToken()]);
  if (asSelf && !token) throw new Error("this patient has no linked identity — self-service booking is unavailable");
  const body = { operationType, class: klass, payload, reads };
  if (opts && opts.optionalReads) body.optionalReads = opts.optionalReads;
  if (asSelf) body.authContext = { target: patientIdentityKey() };
  const post = () =>
    api(base + "/v1/operations", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
      body: JSON.stringify(body),
    });
  if (!asSelf) return post();

  let reply;
  for (let attempt = 0; ; attempt++) {
    reply = await post();
    if (!isTransientAuthLag(reply) || attempt >= retryBackoffsMs.length) break;
    await new Promise((resolve) => setTimeout(resolve, retryBackoffsMs[attempt]));
  }
  return reply;
}

function rejectionMessage(reply) {
  if (reply && reply.status === "rejected") {
    return reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
  }
  return null;
}

// friendlyBookingRejection maps an op rejection message to operator-readable text
// for the booking / reschedule paths: a provider double-book (SlotConflict), a
// patient double-book across providers (PatientDoubleBook), an
// out-of-availability-window booking (OutsideHours), a date-specific time-off
// overlap (ProviderUnavailable), a past-dated booking (ScheduleInPast), a
// misaligned 15-minute-grid time (SlotGridViolation), and an over-long appointment
// (AppointmentTooLong) are the domain rejections CreateAppointment /
// RescheduleAppointment raise. Anything else passes through.
function friendlyBookingRejection(msg) {
  if (msg.indexOf("SlotConflict") !== -1) {
    return "That time overlaps another appointment for this provider. Pick another slot.";
  }
  if (msg.indexOf("PatientDoubleBook") !== -1) {
    return "This patient already has another appointment at that time. Pick a slot that does not overlap.";
  }
  if (msg.indexOf("ProviderUnavailable") !== -1) {
    return "The provider is on time-off (vacation / holiday / out) during that time. Pick a date outside their time-off.";
  }
  if (msg.indexOf("OutsideHours") !== -1) {
    return "That time is outside the provider's availability (UTC). Set hours under “Manage availability” or pick a time inside them.";
  }
  if (msg.indexOf("ScheduleInPast") !== -1) {
    return "That time is in the past. Pick a future date and time.";
  }
  if (msg.indexOf("SlotGridViolation") !== -1) {
    return "Appointments must start and end on the clinic's 15-minute grid (:00/:15/:30/:45). Adjust the time.";
  }
  if (msg.indexOf("AppointmentTooLong") !== -1) {
    return "That appointment is too long (over 24 hours). Shorten the duration.";
  }
  return msg;
}

// ---- Patient context (the trusted-tool switcher) ----
//
// No per-user auth yet (P5/trust model): the user picks which patient they are
// from a human-readable roster (the clinicPatients lens read model, never Core
// KV). The selected key is persisted in localStorage so a refresh keeps context.

function nameForPatient(key) {
  const m = state.patients.find((p) => p.patientKey === key);
  return m && m.name ? m.name : shortKey(key);
}

// renderPatientContact shows the selected patient's email/phone (Vault Fire 5
// Secure-Lens columns on /api/staff/patients — decrypted at projection from
// the patient's identifiedBy identity) next to the switcher, so staff can see
// contact info without a separate admin view. Null for a patient with no
// linked identity yet, a linked identity missing that field, or a shredded one.
function renderPatientContact() {
  const el = $("#patient-contact");
  if (!el) return;
  const m = state.patients.find((p) => p.patientKey === state.patient);
  if (!m) {
    el.textContent = "";
    return;
  }
  const parts = [m.email, m.phone].filter(Boolean);
  el.textContent = parts.length ? parts.join(" · ") : "No contact on file";
}

function restorePatient() {
  const saved = (localStorage.getItem(PATIENT_KEY) || "").trim();
  state.patient = saved || null;
}

// loadPatients reads the clinic-wide patient roster from the PROTECTED,
// RLS-scoped /api/staff/patients (D1.5, the staff wildcard increment) — the
// switcher used to read the unprotected /api/patients, letting ANY caller dump
// every patient's full name with no authentication at all (a membership-
// disclosure PHI leak). authedGetAsStaff mints its own fixed-subject token, so
// this still works before a patient has been selected. q, if given, narrows
// the server-side query to a name match (the front-desk typeahead) — the
// result only replaces state.patientOptions (what the select renders); it is
// merged INTO state.patients (never removed from it), so a patient the search
// scrolls past never loses its resolvable contact/self-token lookup.
async function loadPatients(q) {
  const query = (q || "").trim();
  try {
    const path = query ? "/api/staff/patients?q=" + encodeURIComponent(query) : "/api/staff/patients";
    const data = await authedGetAsStaff(path);
    const results = data.patients || [];
    state.patientOptions = results;
    if (query) {
      const known = new Map(state.patients.map((p) => [p.patientKey, p]));
      for (const p of results) known.set(p.patientKey, p);
      state.patients = Array.from(known.values());
    } else {
      state.patients = results;
    }
  } catch (_) {
    state.patientOptions = [];
    if (!query) state.patients = [];
  }
  populatePatientSelect();
  renderPatientContact();
  syncBookPatient();
}

// wirePatientSearch debounces #patient-search into loadPatients(q) — mirrors
// loftspace-app's unified-search debounce (app.js wireUnifiedSearch, 250ms).
let patientSearchTimer = null;
function wirePatientSearch() {
  const input = $("#patient-search");
  if (!input) return;
  input.addEventListener("input", () => {
    clearTimeout(patientSearchTimer);
    const q = input.value.trim();
    patientSearchTimer = setTimeout(() => loadPatients(q), 250);
  });
}

function populatePatientSelect() {
  const sel = $("#patient");
  sel.innerHTML = "";
  const options = state.patientOptions;
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = options.length
    ? "Select patient…"
    : state.patients.length
      ? "No matches"
      : "No patients — add one →";
  sel.append(placeholder);
  for (const p of options) {
    const o = document.createElement("option");
    o.value = p.patientKey;
    o.textContent = p.name;
    sel.append(o);
  }
  const values = options.map((p) => p.patientKey);
  sel.value = state.patient && values.includes(state.patient) ? state.patient : "";
}

function setPatient(value) {
  const v = (value || "").trim();
  state.patient = v || null;
  state.highlight = null;
  if (v) localStorage.setItem(PATIENT_KEY, v);
  else localStorage.removeItem(PATIENT_KEY);
  renderPatientContact();
  syncBookPatient();
  // Re-render the slot picker so it excludes the newly-selected patient's existing
  // appointments (cross-provider double-book exclusion). Idempotent; bails if the
  // Book form has no provider/date yet.
  refreshSlots();
  renderSoonest();
  if (state.view === "appts") {
    loadAppts();
    loadMySeries();
    loadLedger();
  }
}

// syncBookPatient reflects the selected patient into the Book tab's read-only echo,
// enables/disables the Book button, and toggles the self-service checkbox — offered
// only when the selected patient has a linked identity (patientIdentityKey()), the
// precondition CreateAppointment's consumer scope=self grant enforces server-side.
// Unchecked + disabled whenever the patient changes, so a stale self-service choice
// never survives a switch to an unlinked (or different) patient.
function syncBookPatient() {
  const echo = $("#book-patient");
  echo.textContent = state.patient ? nameForPatient(state.patient) : "Select a patient above first.";
  const selfBox = $("#book-self");
  const selfHint = $("#book-self-hint");
  const linked = !!patientIdentityKey();
  selfBox.checked = false;
  selfBox.disabled = !linked;
  if (selfHint) {
    selfHint.textContent = state.patient && !linked
      ? "This patient has no linked identity yet — self-service booking needs one (add email/phone when creating a patient)."
      : "";
  }
  refreshBookEnabled();
}

// ---- New patient modal ----
//
// Patient contact (email/phone) is Vault-plane PII — it never lands on the
// bare .demographics aspect (D5, non-sensitive-only). When the operator
// supplies contact info, the FE does the loftspace-app two-step: mint an
// unclaimed identity carrying the sensitive contact via identity-domain's
// CreateUnclaimedIdentity (name + email/phone + a client-minted claimKeyHash
// — Lattice never holds the plaintext), then CreatePatient with that
// identityKey so it wires the identifiedBy link. No contact → the patient
// is created with no linked identity (fullName only).

function openNewPatient() {
  $("#patient-form").reset();
  $("#patient-overlay").hidden = false;
  $("#np-name").focus();
}

function closeNewPatient() {
  $("#patient-overlay").hidden = true;
}

// sha256Hex returns the lowercase hex sha256 of a string — the shape
// CreateUnclaimedIdentity stores for claimKeyHash.
async function sha256Hex(s) {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
  return Array.from(new Uint8Array(buf)).map((b) => b.toString(16).padStart(2, "0")).join("");
}

// mintClaimSecret returns a random claim-secret plaintext. It is hashed and only
// the hash is sent; the plaintext never enters Lattice. Used both for a real
// CreateUnclaimedIdentity claimKeyHash and, elsewhere, as raw entropy for
// deriving a device identity's NanoID (ensureClaimedDevice) — a fresh
// unpredictable value, not a secret that has to survive that second use.
function mintClaimSecret() {
  const a = new Uint8Array(32);
  crypto.getRandomValues(a);
  return Array.from(a).map((b) => b.toString(16).padStart(2, "0")).join("");
}

// sha256NanoID derives a valid 20-char Contract #1 NanoID from SHA-256(s),
// byte-identical to internal/substrate.SHA256NanoID / the Starlark
// crypto.sha256NanoID(s) builtin (both seed a 128-bit PCG from the digest and
// rejection-sample the alphabet). Needed client-side so this dispatcher can
// declare the identityindex probe keys CreateUnclaimedIdentity's script
// derives from the same normalized email/phone/name.
async function sha256NanoID(s) {
  const ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789";
  const MASK64 = (1n << 64n) - 1n;
  const MASK128 = (1n << 128n) - 1n;
  const MUL = (2549297995355413924n << 64n) | 4865540595714422341n;
  const INC = (6364136223846793005n << 64n) | 1442695040888963407n;
  const CHEAP_MUL = 0xda942042e4dd58b5n;

  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
  const beUint64 = (off) => {
    let v = 0n;
    for (let i = 0; i < 8; i++) v = (v << 8n) | BigInt(digest[off + i]);
    return v;
  };
  let state = (beUint64(0) << 64n) | beUint64(8);
  const nextUint64 = () => {
    state = (state * MUL + INC) & MASK128;
    let hi = state >> 64n;
    const lo = state & MASK64;
    hi ^= hi >> 32n;
    hi = (hi * CHEAP_MUL) & MASK64;
    hi ^= hi >> 48n;
    hi = (hi * (lo | 1n)) & MASK64;
    return hi;
  };
  let out = "";
  while (out.length < 20) {
    let v = nextUint64();
    for (let i = 0; i < 10 && out.length < 20; i++) {
      const b = Number(v & 63n);
      v >>= 6n;
      if (b < ALPHABET.length) out += ALPHABET[b];
    }
  }
  return out;
}

// identityIndexProbeKeys computes the dedup identityindex probe keys
// (email/phone/name) for a CreateUnclaimedIdentity payload, mirroring the
// normalization identity-domain's script applies byte-for-byte. Declaring
// them as optionalReads activates the dormant duplicate-flag probe and
// avoids the RevisionConflict a duplicate contact would otherwise hit.
async function identityIndexProbeKeys({ email, phone, name }) {
  const keys = [];
  if (email) {
    const e = email.trim().toLowerCase();
    if (e) keys.push("vtx.identityindex." + await sha256NanoID("email:" + e));
  }
  if (phone) {
    const p = Array.from(phone).filter((ch) => (ch >= "0" && ch <= "9") || ch === "+").join("");
    if (p) keys.push("vtx.identityindex." + await sha256NanoID("phone:" + p));
  }
  if (name) {
    const n = name.toLowerCase().split(/\s+/).filter(Boolean).join(" ");
    if (n) keys.push("vtx.identityindex." + await sha256NanoID("name:" + n));
  }
  return keys;
}

async function submitNewPatient(ev) {
  ev.preventDefault();
  const name = $("#np-name").value.trim();
  if (!name) {
    toast("A patient name is required.", "err");
    return;
  }
  const email = $("#np-email").value.trim();
  const phone = $("#np-phone").value.trim();

  const submit = $("#patient-submit");
  submit.disabled = true;
  try {
    let identityKey = "";
    if (email || phone) {
      const claimSecret = mintClaimSecret();
      const claimKeyHash = await sha256Hex(claimSecret);
      const idPayload = { name, claimKeyHash };
      if (email) idPayload.email = email;
      if (phone) idPayload.phone = phone;
      const optionalReads = await identityIndexProbeKeys(idPayload);
      const idReply = await submitOp("CreateUnclaimedIdentity", "identity", idPayload, undefined, { optionalReads });
      const idMsg = rejectionMessage(idReply);
      if (idMsg) {
        toast("Could not create patient — " + idMsg, "err");
        return;
      }
      identityKey = idReply && idReply.primaryKey ? idReply.primaryKey : "";
      if (identityKey) {
        // Run the real claim ceremony now, while the plaintext secret still
        // exists (§11.1a — Lattice never stores it): without this the linked
        // identity is created but role-less, and every self-service booking
        // 403s. Best-effort — a failure here still leaves the patient created
        // (with a linked identity); the RotateClaimKey fallback in
        // ensureClaimedDevice can retry it the first time self-service is used.
        pendingClaimSecrets[identityKey] = claimSecret;
        try {
          await ensureClaimedDevice(identityKey);
        } catch (e) {
          toast("Patient created, but self-service sign-in setup failed — " + e.message, "err");
        }
      }
    }

    const payload = { fullName: name };
    if (identityKey) payload.identityKey = identityKey;
    // read-posture (d): identityKey + ".patientClaim" is a read-before-create dedup
    // guard (claim_identity, ddls.go) — its absence is the common, legitimate case,
    // so it is declared optionalReads, not reads (script-read-posture-design.md §13).
    const reply = await submitOp(
      "CreatePatient",
      "patient",
      payload,
      identityKey ? [identityKey] : undefined,
      identityKey ? { optionalReads: [identityKey + ".patientClaim"] } : undefined,
    );
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not create patient — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    closeNewPatient();
    toast("Patient created.", "ok", key);
    // Make the new patient the active context (the lens may take a moment to
    // project; select it now and reload so the switcher shows it once projected).
    if (key) {
      state.patient = key;
      localStorage.setItem(PATIENT_KEY, key);
    }
    const search = $("#patient-search");
    if (search) search.value = "";
    setTimeout(loadPatients, 700);
  } catch (e) {
    toast("Could not create patient: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Providers (booking picker + inline add) ----

async function loadProviders() {
  try {
    const data = await api("/api/providers");
    state.providers = data.providers || [];
  } catch (_) {
    state.providers = [];
  }
  populateSpecialtySelect();
  populateProviderSelect("#provider", { specialty: $("#book-specialty") ? $("#book-specialty").value : "" });
  populateProviderSelect("#sched-provider", { includeAll: true });
  populateProviderSelect("#avail-provider");
  populateProviderSelect("#series-provider");
  refreshBookEnabled();
  renderSlotCalendar();
  renderAvailEditors();
  renderSoonest();
}

// renderAvailEditors re-seeds (only if the selected provider changed) and renders
// both Availability-tab editors against #avail-provider. Safe to call any time —
// after a roster refresh the selection is preserved, so a just-saved draft is kept.
function renderAvailEditors() {
  seedProviderEdit();
  hoursDraftForSelectedProvider();
  renderHoursDraft();
  timeOffDraftForSelectedProvider();
  renderTimeOffDraft();
}

// seedProviderEdit fills the "Provider details" editor from the selected provider's
// projected profile (name / specialty / credentials / bio — all carried by the
// clinicProviders lens). Like the hours / time-off editors it re-seeds ONLY when
// the selected provider changes, so an in-progress edit survives a roster refresh;
// with no provider selected the fields are cleared + disabled.
function seedProviderEdit() {
  const prov = $("#avail-provider").value;
  if (prov === state.editProvider) return;
  state.editProvider = prov;
  const p = providerByKey(prov);
  $("#edit-prov-name").value = p ? p.name || "" : "";
  $("#edit-prov-specialty").value = p ? p.specialty || "" : "";
  $("#edit-prov-credentials").value = p ? p.credentials || "" : "";
  $("#edit-prov-bio").value = p ? p.bio || "" : "";
  const disabled = !prov;
  for (const id of ["#edit-prov-name", "#edit-prov-specialty", "#edit-prov-credentials", "#edit-prov-bio", "#edit-prov-save"]) {
    $(id).disabled = disabled;
  }
}

// saveProviderEdit submits SetProviderProfile for the selected provider. The op
// REPLACES the whole .profile, so the form (seeded from the projected profile)
// carries every field; name + specialty are required so the roster lens never
// loses the provider. Mirrors saveProviderHours / saveProviderTimeOff (no re-seed
// after save — the form already shows what was saved, and the projection may lag).
async function saveProviderEdit() {
  const prov = $("#avail-provider").value;
  if (!prov) {
    toast("Select a provider first.", "err");
    return;
  }
  const name = $("#edit-prov-name").value.trim();
  const specialty = $("#edit-prov-specialty").value.trim();
  const credentials = $("#edit-prov-credentials").value.trim();
  const bio = $("#edit-prov-bio").value.trim();
  if (!name || !specialty) {
    toast("Provider name and specialty are required.", "err");
    return;
  }
  const payload = { providerKey: prov, fullName: name, specialty };
  if (credentials) payload.credentials = credentials;
  if (bio) payload.bio = bio;

  const btn = $("#edit-prov-save");
  btn.disabled = true;
  try {
    const reply = await submitOp("SetProviderProfile", "provider", payload, [prov]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not save provider details — " + msg, "err");
      return;
    }
    toast("Provider details saved.", "ok");
    // Refresh the roster so the picker label (name · specialty) reflects the edit;
    // the selection is preserved, so seedProviderEdit keeps the just-saved form.
    loadProviders();
  } catch (e) {
    toast("Could not save provider details: " + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}

function providerLabel(p) {
  return p.specialty ? `${p.name} · ${p.specialty}` : p.name;
}

// wireProviderSearch filters #provider (the booking picker only) as the front
// desk types — client-side, since the roster is already fully loaded (unlike
// #patient's server ?q=, providers carry no PII and the roster is small
// enough that a round-trip buys nothing).
function wireProviderSearch() {
  const input = $("#provider-search");
  if (!input) return;
  input.addEventListener("input", () => {
    state.providerSearch = input.value.trim();
    populateProviderSelect("#provider", { specialty: $("#book-specialty") ? $("#book-specialty").value : "" });
  });
}

// SCHED_ALL is the sentinel select value for the clinic-wide "All providers"
// schedule view (every provider's appointments on one grid). It is offered only on
// the Schedule picker (includeAll), never the booking picker — you book one provider.
const SCHED_ALL = "__all__";

// populateProviderSelect fills a provider picker from the roster, optionally
// narrowed to opts.specialty — the booking picker's specialty filter uses this so
// the dropdown only lists providers who can actually help, instead of a flat list
// the patient has to already know a name to navigate.
function populateProviderSelect(sel, opts) {
  const el = $(sel);
  if (!el) return;
  const prev = el.value;
  el.innerHTML = "";
  let roster = opts && opts.specialty ? state.providers.filter((p) => p.specialty === opts.specialty) : state.providers;
  if (sel === "#provider" && state.providerSearch) {
    const q = state.providerSearch.toLowerCase();
    roster = roster.filter((p) => (p.name || "").toLowerCase().includes(q) || (p.specialty || "").toLowerCase().includes(q));
  }
  const placeholder = document.createElement("option");
  placeholder.value = "";
  placeholder.textContent = roster.length
    ? "Select provider…"
    : sel === "#provider" && state.providerSearch
      ? "No matches"
      : opts && opts.specialty
        ? `No ${opts.specialty} providers — try "Any specialty".`
        : "No providers — add one in the Availability tab";
  el.append(placeholder);
  if (opts && opts.includeAll && roster.length) {
    const all = document.createElement("option");
    all.value = SCHED_ALL;
    all.textContent = "All providers (clinic-wide)";
    el.append(all);
  }
  for (const p of roster) {
    const o = document.createElement("option");
    o.value = p.providerKey;
    o.textContent = providerLabel(p);
    el.append(o);
  }
  const values = roster.map((p) => p.providerKey);
  if (prev === SCHED_ALL && opts && opts.includeAll) {
    el.value = SCHED_ALL;
  } else {
    el.value = values.includes(prev) ? prev : "";
  }
}

// populateSpecialtySelect fills the booking specialty filter from the roster's
// distinct specialties, defaulting to "Any specialty" — the entry point for a
// patient who knows what kind of care they need but not which provider offers it.
function populateSpecialtySelect() {
  const el = $("#book-specialty");
  if (!el) return;
  const prev = el.value;
  el.innerHTML = "";
  const any = document.createElement("option");
  any.value = "";
  any.textContent = "Any specialty";
  el.append(any);
  const specialties = [...new Set(state.providers.map((p) => p.specialty).filter(Boolean))].sort();
  for (const s of specialties) {
    const o = document.createElement("option");
    o.value = s;
    o.textContent = s;
    el.append(o);
  }
  el.value = specialties.includes(prev) ? prev : "";
}

async function submitAddProvider() {
  const name = $("#np-prov-name").value.trim();
  const specialty = $("#np-prov-specialty").value.trim();
  const credentials = $("#np-prov-credentials").value.trim();
  if (!name || !specialty) {
    toast("Provider name and specialty are required.", "err");
    return;
  }
  const payload = { fullName: name, specialty };
  if (credentials) payload.credentials = credentials;

  const btn = $("#add-provider-submit");
  btn.disabled = true;
  try {
    const reply = await submitOp("CreateProvider", "provider", payload);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not add provider — " + msg, "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    $("#np-prov-name").value = "";
    $("#np-prov-specialty").value = "";
    $("#np-prov-credentials").value = "";
    $("#add-provider").open = false;
    toast("Provider added.", "ok", key);
    setTimeout(async () => {
      await loadProviders();
      // The add affordance lives in the Availability tab — select the new provider
      // there so the user can set its hours / time-off next.
      if (key) {
        $("#avail-provider").value = key;
        renderAvailEditors();
      }
    }, 700);
  } catch (e) {
    toast("Could not add provider: " + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}

// ---- Provider availability (SetProviderHours) ----
//
// The editor composes a UTC weekly-availability window list for the provider
// selected in the Book form and submits SetProviderHours (which REPLACES the
// provider's .hours aspect). Like the time-off manager this is READ-MODIFY-WRITE:
// the draft is SEEDED from the provider's currently-projected .hours windows (the
// clinicProviders lens carries them), so Add / Remove edits the live set and Save
// replaces the whole list — adding one window no longer silently wipes a provider's
// existing hours. Times are UTC to match the op's UTC weekday / seconds-of-day
// enforcement.

const DAY_NAMES = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const MONTH_NAMES = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];

function hmsToSeconds(hhmm) {
  const m = /^(\d{2}):(\d{2})$/.exec(hhmm || "");
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (h > 23 || min > 59) return null;
  return h * 3600 + min * 60;
}

function secondsToHMS(sec) {
  const pad = (n) => String(n).padStart(2, "0");
  return `${pad(Math.floor(sec / 3600))}:${pad(Math.floor((sec % 3600) / 60))}`;
}

// hoursDraftForSelectedProvider returns the Availability tab's selected provider
// key, re-seeding the draft from that provider's currently-projected .hours windows
// whenever the selection changed (so one provider's edits can't be saved onto
// another, and so an Add/Remove edits the live set rather than starting blank).
function hoursDraftForSelectedProvider() {
  const prov = $("#avail-provider").value;
  if (prov !== state.hoursProvider) {
    state.hoursProvider = prov;
    const p = providerByKey(prov);
    // Clone so editing the draft doesn't mutate the loaded provider row.
    state.hoursDraft = p && Array.isArray(p.hours)
      ? p.hours.map((w) => ({ day: w.day, openSec: w.openSec, closeSec: w.closeSec }))
      : [];
  }
  return prov;
}

function renderHoursDraft() {
  const list = $("#hours-list");
  if (!list) return;
  list.innerHTML = "";
  if (!state.hoursDraft.length) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No windows added — saving clears this provider's hours (always available).";
    list.appendChild(p);
    return;
  }
  state.hoursDraft.forEach((w, i) => {
    const row = document.createElement("div");
    row.className = "hours-row";
    const label = document.createElement("span");
    label.textContent = `${DAY_NAMES[w.day]} ${secondsToHMS(w.openSec)}–${secondsToHMS(w.closeSec)} UTC`;
    const rm = document.createElement("button");
    rm.type = "button";
    rm.className = "ghost danger";
    rm.textContent = "Remove";
    rm.addEventListener("click", () => {
      state.hoursDraft.splice(i, 1);
      renderHoursDraft();
    });
    row.appendChild(label);
    row.appendChild(rm);
    list.appendChild(row);
  });
}

function addHoursWindow() {
  if (!hoursDraftForSelectedProvider()) {
    toast("Select a provider first.", "err");
    return;
  }
  const day = Number($("#hours-day").value);
  const openSec = hmsToSeconds($("#hours-open").value);
  const closeSec = hmsToSeconds($("#hours-close").value);
  if (openSec === null || closeSec === null) {
    toast("Enter valid open and close times.", "err");
    return;
  }
  if (openSec >= closeSec) {
    toast("Open time must be before close time.", "err");
    return;
  }
  state.hoursDraft.push({ day, openSec, closeSec });
  renderHoursDraft();
}

async function saveProviderHours() {
  const provider = hoursDraftForSelectedProvider();
  if (!provider) {
    toast("Select a provider first.", "err");
    return;
  }
  const btn = $("#hours-save");
  btn.disabled = true;
  try {
    const reply = await submitOp("SetProviderHours", "provider",
      { providerKey: provider, windows: state.hoursDraft }, [provider]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not set hours — " + msg, "err");
      return;
    }
    const n = state.hoursDraft.length;
    toast(n ? `Availability saved (${n} window${n === 1 ? "" : "s"}).` : "Availability cleared (always available).", "ok");
    // Refresh the roster so the persisted windows back the booking slot picker + the
    // editor (the lens may take a moment to project; selection is preserved). Mirrors
    // saveProviderTimeOff.
    loadProviders();
  } catch (e) {
    toast("Could not set hours: " + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}

// ---- Provider time-off (SetProviderTimeOff) ----
//
// Date-specific blackout ranges on top of the recurring .hours. Unlike the hours
// editor, this is READ-MODIFY-WRITE: the draft is SEEDED from the provider's
// currently-projected .timeOff ranges (the clinicProviders lens now carries them),
// so Add / Remove edits the live set and Save replaces the whole list via
// SetProviderTimeOff. Ranges are whole-day, UTC, half-open [from, to): a single
// blocked day D is stored {from: D 00:00Z, to: (D+1) 00:00Z}.

function providerByKey(key) {
  return state.providers.find((p) => p.providerKey === key) || null;
}

// dayStartUTC turns a "YYYY-MM-DD" date input into the canonical UTC RFC3339
// instant at that day's start (00:00:00Z).
function dayStartUTC(dateStr) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(dateStr || "")) return "";
  return dateStr + "T00:00:00Z";
}

// nextDayStartUTC turns a "YYYY-MM-DD" into the start of the FOLLOWING day in UTC —
// the exclusive end of a whole-day block (so a To of D blocks all of day D).
function nextDayStartUTC(dateStr) {
  const start = dayStartUTC(dateStr);
  if (!start) return "";
  const d = new Date(start);
  if (isNaN(d)) return "";
  d.setUTCDate(d.getUTCDate() + 1);
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

// timeOffRangeLabel renders a stored {from, to} (to is the exclusive next-day start)
// as an inclusive human range: "Jul 1" for a single day, "Jul 1 – Jul 5" for a span.
function timeOffRangeLabel(r) {
  const from = new Date(r.from);
  const toExcl = new Date(r.to);
  if (isNaN(from) || isNaN(toExcl)) return `${r.from} → ${r.to}`;
  const incl = new Date(toExcl.getTime() - 86400000); // exclusive end − 1 day = inclusive last day
  const opts = { timeZone: "UTC", month: "short", day: "numeric", year: "numeric" };
  const f = from.toLocaleDateString(undefined, opts);
  const t = incl.toLocaleDateString(undefined, opts);
  return f === t ? f : `${f} – ${t}`;
}

// timeOffDraftForSelectedProvider returns the Availability tab's selected provider
// key, re-seeding the draft from that provider's currently-projected ranges whenever
// the selection changed (so one provider's edits can't be saved onto another).
function timeOffDraftForSelectedProvider() {
  const prov = $("#avail-provider").value;
  if (prov !== state.timeOffProvider) {
    state.timeOffProvider = prov;
    const p = providerByKey(prov);
    // Clone so editing the draft doesn't mutate the loaded provider row.
    state.timeOffDraft = p && Array.isArray(p.timeOff)
      ? p.timeOff.map((r) => ({ from: r.from, to: r.to, reason: r.reason }))
      : [];
  }
  return prov;
}

function renderTimeOffDraft() {
  const list = $("#timeoff-list");
  if (!list) return;
  list.innerHTML = "";
  if (!state.timeOffDraft.length) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No time-off — this provider has no blocked dates.";
    list.appendChild(p);
    return;
  }
  state.timeOffDraft.forEach((r, i) => {
    const row = document.createElement("div");
    row.className = "hours-row";
    const label = document.createElement("span");
    label.textContent = timeOffRangeLabel(r) + (r.reason ? ` · ${r.reason}` : "");
    const rm = document.createElement("button");
    rm.type = "button";
    rm.className = "ghost danger";
    rm.textContent = "Remove";
    rm.addEventListener("click", () => {
      state.timeOffDraft.splice(i, 1);
      renderTimeOffDraft();
    });
    row.appendChild(label);
    row.appendChild(rm);
    list.appendChild(row);
  });
}

function addTimeOffRange() {
  if (!timeOffDraftForSelectedProvider()) {
    toast("Select a provider first.", "err");
    return;
  }
  const fromStr = $("#timeoff-from").value;
  const toStr = $("#timeoff-to").value;
  if (!fromStr || !toStr) {
    toast("Pick a From and To date.", "err");
    return;
  }
  if (toStr < fromStr) {
    toast("The To date must be on or after the From date.", "err");
    return;
  }
  const from = dayStartUTC(fromStr);
  const to = nextDayStartUTC(toStr);
  if (!from || !to) {
    toast("Those dates are not valid.", "err");
    return;
  }
  const range = { from, to };
  const reason = $("#timeoff-reason").value.trim();
  if (reason) range.reason = reason;
  state.timeOffDraft.push(range);
  $("#timeoff-reason").value = "";
  renderTimeOffDraft();
}

async function saveProviderTimeOff() {
  const provider = timeOffDraftForSelectedProvider();
  if (!provider) {
    toast("Select a provider first.", "err");
    return;
  }
  const btn = $("#timeoff-save");
  btn.disabled = true;
  try {
    // SetProviderTimeOff replaces the whole .timeOff aspect; ranges=[] clears it.
    // The op re-normalizes from/to to canonical UTC and validates from < to.
    const reply = await submitOp("SetProviderTimeOff", "provider",
      { providerKey: provider, ranges: state.timeOffDraft }, [provider]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not set time-off — " + msg, "err");
      return;
    }
    const n = state.timeOffDraft.length;
    toast(n ? `Time-off saved (${n} range${n === 1 ? "" : "s"}).` : "Time-off cleared (no blocked dates).", "ok");
    // Refresh the roster so the persisted ranges back the booking warning + the
    // editor (the lens may take a moment to project; selection is preserved).
    loadProviders();
  } catch (e) {
    toast("Could not set time-off: " + e.message, "err");
  } finally {
    btn.disabled = false;
  }
}

// refreshTimeOffWarning shows a soft heads-up under the Date & time field when the
// chosen start falls inside the selected provider's projected time-off. The op
// (ProviderUnavailable) is the authority; this just warns before submit.
function refreshTimeOffWarning() {
  const el = $("#timeoff-warning");
  if (!el) return;
  el.hidden = true;
  el.textContent = "";
  const p = providerByKey($("#provider").value);
  const when = $("#startsAt").value;
  if (!p || !Array.isArray(p.timeOff) || !p.timeOff.length || !when) return;
  const start = new Date(toRFC3339(when));
  if (isNaN(start)) return;
  const hit = p.timeOff.find((r) => {
    const from = new Date(r.from);
    const to = new Date(r.to);
    return !isNaN(from) && !isNaN(to) && start >= from && start < to;
  });
  if (hit) {
    el.textContent = `Heads up: ${p.name} is on time-off then (${timeOffRangeLabel(hit)}). The booking will be rejected.`;
    el.hidden = false;
  }
}

// ---- Available-slot picker ----
// Suggests the provider's open appointment starts for a chosen date, computed from
// the same inputs the op enforces — the .hours availability windows (enforce_hours),
// the .timeOff blackouts (enforce_time_off), the provider's existing appointments
// (the double-book check), and the past-time guard (ScheduleInPast). A suggested
// slot is built so it passes those checks; the op stays the authority.

// providerAppointments fetches (and caches per provider) the provider's existing
// appointments. The cache is invalidated on a successful booking so a just-booked
// slot stops being offered.
async function providerAppointments(provider) {
  if (state.slotApptCache[provider]) return state.slotApptCache[provider];
  try {
    const data = await api("/api/appointments?provider=" + encodeURIComponent(provider));
    state.slotApptCache[provider] = data.appointments || [];
  } catch (e) {
    state.slotApptCache[provider] = [];
  }
  return state.slotApptCache[provider];
}

// patientAppointments fetches (and caches per patient) the selected patient's
// appointments across ALL providers — so the slot picker can exclude a time the
// patient is already booked elsewhere, which the op rejects as a PatientDoubleBook.
// Invalidated on a successful booking alongside the provider cache.
//
// D1.5: reads the PROTECTED /api/my-appointments endpoint (RLS, patient-self
// anchor) instead of the old unauthenticated `?patient=` filter — patient is
// always the signed-in state.patient (its only caller), so the authenticated
// actor's own rows are exactly what this needs.
async function patientAppointments(patient) {
  if (state.slotPatientApptCache[patient]) return state.slotPatientApptCache[patient];
  try {
    const data = await authedGet("/api/my-appointments");
    state.slotPatientApptCache[patient] = data.appointments || [];
  } catch (e) {
    state.slotPatientApptCache[patient] = [];
  }
  return state.slotPatientApptCache[patient];
}

// apptBlocks reports whether an appointment still occupies its slot. A cancelled /
// no-show appointment has its slot-claim aspects released on the terminal
// transition, so the op would allow rebooking that time — exclude it from the
// picker's block set.
function apptBlocks(a) {
  return a.status !== "cancelled" && a.status !== "noShow";
}

// computeOpenSlots derives the provider's open appointment starts (UTC ms) for a
// calendar date (interpreted as a UTC day, matching how .hours windows are keyed by
// UTC weekday + seconds-of-day). durationMin is both the slot length and the step,
// so suggested slots are back-to-back at the appointment length. A slot is dropped
// when it is in the past, overlaps any time-off range, or overlaps a live
// appointment — the same conditions the op rejects. `appts` carries both the
// provider's appointments (the provider double-book / SlotConflict check) and the
// selected patient's appointments across all providers (the cross-provider
// PatientDoubleBook check), so the picker never offers a slot the op would reject.
function computeOpenSlots(p, dateStr, durationMin, appts, nowMs) {
  if (!p || !Array.isArray(p.hours) || !p.hours.length || !dateStr) return [];
  const dayStart = Date.parse(dateStr + "T00:00:00Z");
  if (isNaN(dayStart)) return [];
  const weekday = new Date(dayStart).getUTCDay();
  const durMs = durationMin * 60000;
  const stepSec = Math.max(durationMin, 15) * 60;
  const timeOff = Array.isArray(p.timeOff) ? p.timeOff : [];
  const blocking = (appts || [])
    .filter(apptBlocks)
    .map((a) => ({ s: Date.parse(a.startsAt), e: Date.parse(a.endsAt) }))
    .filter((x) => !isNaN(x.s) && !isNaN(x.e));
  const slots = [];
  const seen = new Set();
  for (const w of p.hours) {
    if (w.day !== weekday) continue;
    for (let sec = w.openSec; sec + durationMin * 60 <= w.closeSec; sec += stepSec) {
      const s = dayStart + sec * 1000;
      const e = s + durMs;
      if (s <= nowMs) continue; // past — matches ScheduleInPast (start <= submittedAt)
      const offHit = timeOff.some((r) => {
        const rf = Date.parse(r.from), rt = Date.parse(r.to);
        return !isNaN(rf) && !isNaN(rt) && s < rt && e > rf;
      });
      if (offHit) continue;
      if (blocking.some((b) => s < b.e && e > b.s)) continue;
      if (seen.has(s)) continue;
      seen.add(s);
      slots.push(s);
    }
  }
  slots.sort((a, b) => a - b);
  return slots;
}

// slotTimeLabel renders a slot's UTC instant as the local clock time the button
// shows; the click fills #startsAt (a local datetime-local value) with the same
// instant, which round-trips back to this UTC start on submit.
function slotTimeLabel(ms) {
  const d = new Date(ms);
  const pad = (n) => String(n).padStart(2, "0");
  let h = d.getHours();
  const m = pad(d.getMinutes());
  const ap = h < 12 ? "AM" : "PM";
  h = h % 12;
  if (h === 0) h = 12;
  return `${h}:${m} ${ap}`;
}

// noSlotsReason names why a date has no open slots, distinguishing the two
// "blocked date" cases the picker should call out — the provider doesn't work
// that weekday, or is on time-off that whole day — from the generic
// fully-booked / duration fallback (returns "" for that case so the caller
// keeps its default line). Mirrors computeOpenSlots' UTC-day interpretation so
// the reason matches the slots that would be shown. Time-off ranges are authored
// whole-day (.timeOff stores [from, (to+1day)) UTC), so a date is "on time-off"
// when the full UTC day falls inside one range.
function noSlotsReason(p, dateStr) {
  const dayStart = Date.parse(dateStr + "T00:00:00Z");
  if (isNaN(dayStart)) return "";
  const dayEnd = dayStart + 86400000;
  const weekday = new Date(dayStart).getUTCDay();
  const timeOff = Array.isArray(p.timeOff) ? p.timeOff : [];
  const cover = timeOff.find((r) => {
    const rf = Date.parse(r.from), rt = Date.parse(r.to);
    return !isNaN(rf) && !isNaN(rt) && rf <= dayStart && dayEnd <= rt;
  });
  if (cover) {
    return `${p.name} is on time-off that day` + (cover.reason ? ` (${cover.reason})` : "") + " — pick another date.";
  }
  const days = [...new Set(p.hours.map((w) => w.day))].sort((a, b) => a - b);
  if (!days.includes(weekday)) {
    const list = days.map((d) => DAY_NAMES[d].slice(0, 3)).join(", ");
    return `${p.name} doesn't see patients on ${DAY_NAMES[weekday]}s — available ${list}.`;
  }
  return "";
}

// ---- Booking date calendar ----
// A custom month grid for choosing the booking date, so days the provider can't be
// booked on are greyed out — the native <input type=date> can't exclude arbitrary
// dates. A date is interpreted as a UTC day (matching how .hours, .timeOff, and
// computeOpenSlots are keyed), so the grid is built in UTC: columns are UTC weekdays
// and each cell is a UTC calendar day. Blocking mirrors the op's rejections at the
// whole-day grain — a working day that happens to be fully booked stays enabled (the
// slots area then explains it). The op stays the booking authority.

// ymdUTC formats a UTC year/month/day as the "YYYY-MM-DD" string the slot picker and
// computeOpenSlots consume (parsed back as <date>T00:00:00Z).
function ymdUTC(y, m, d) {
  const pad = (n) => String(n).padStart(2, "0");
  return `${y}-${pad(m + 1)}-${pad(d)}`;
}

// dayBlockedReason reports why a whole UTC calendar day is unbookable for a provider,
// or "" when at least part of the day could be booked. Past days, a weekday the
// provider doesn't work, and a whole-day time-off range are blocked.
function dayBlockedReason(p, y, m, d, nowMs) {
  const dayStart = Date.UTC(y, m, d);
  const dayEnd = dayStart + 86400000;
  if (dayEnd <= nowMs) return "Past date.";
  const weekday = new Date(dayStart).getUTCDay();
  const days = [...new Set((p.hours || []).map((w) => w.day))];
  if (!days.includes(weekday)) return `${p.name} doesn't see patients on ${DAY_NAMES[weekday]}s.`;
  const timeOff = Array.isArray(p.timeOff) ? p.timeOff : [];
  const cover = timeOff.find((r) => {
    const rf = Date.parse(r.from), rt = Date.parse(r.to);
    return !isNaN(rf) && !isNaN(rt) && rf <= dayStart && dayEnd <= rt;
  });
  if (cover) return `Time off${cover.reason ? ` (${cover.reason})` : ""}.`;
  return "";
}

// renderSlotCalendar draws the month grid for the selected provider. Picking an
// enabled day sets #slot-date and refreshes the open-slot list below.
function renderSlotCalendar() {
  const box = $("#slot-calendar");
  if (!box) return;
  box.innerHTML = "";
  const p = providerByKey($("#provider").value);
  if (!p) {
    const m = document.createElement("p");
    m.className = "cal-empty";
    m.textContent = "Select a provider to see available dates.";
    box.appendChild(m);
    return;
  }
  if (!Array.isArray(p.hours) || !p.hours.length) {
    const m = document.createElement("p");
    m.className = "cal-empty";
    m.textContent = "This provider has set no availability hours — enter a date & time above directly.";
    box.appendChild(m);
    return;
  }

  const now = new Date();
  const nowMs = Date.now();
  const curMonthStart = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1);
  if (!state.slotCalAnchor || state.slotCalAnchor.getTime() < curMonthStart) {
    state.slotCalAnchor = new Date(curMonthStart);
  }
  const anchor = state.slotCalAnchor;
  const y = anchor.getUTCFullYear();
  const m = anchor.getUTCMonth();
  const selected = $("#slot-date").value;
  const todayStr = ymdUTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());

  const head = document.createElement("div");
  head.className = "cal-head";
  const prev = document.createElement("button");
  prev.type = "button";
  prev.className = "cal-step";
  prev.textContent = "‹";
  prev.setAttribute("aria-label", "Previous month");
  prev.disabled = Date.UTC(y, m, 1) <= curMonthStart; // no fully-past months
  prev.addEventListener("click", () => {
    state.slotCalAnchor = new Date(Date.UTC(y, m - 1, 1));
    renderSlotCalendar();
  });
  const title = document.createElement("span");
  title.className = "cal-title";
  title.textContent = `${MONTH_NAMES[m]} ${y}`;
  const next = document.createElement("button");
  next.type = "button";
  next.className = "cal-step";
  next.textContent = "›";
  next.setAttribute("aria-label", "Next month");
  next.addEventListener("click", () => {
    state.slotCalAnchor = new Date(Date.UTC(y, m + 1, 1));
    renderSlotCalendar();
  });
  head.append(prev, title, next);
  box.appendChild(head);

  const grid = document.createElement("div");
  grid.className = "cal-grid";
  for (const dow of ["S", "M", "T", "W", "T", "F", "S"]) {
    const c = document.createElement("div");
    c.className = "cal-dow";
    c.textContent = dow;
    grid.appendChild(c);
  }
  const firstDow = new Date(Date.UTC(y, m, 1)).getUTCDay();
  const daysInMonth = new Date(Date.UTC(y, m + 1, 0)).getUTCDate();
  for (let i = 0; i < firstDow; i++) {
    const c = document.createElement("div");
    c.className = "cal-day empty";
    grid.appendChild(c);
  }
  for (let d = 1; d <= daysInMonth; d++) {
    const dateStr = ymdUTC(y, m, d);
    const reason = dayBlockedReason(p, y, m, d, nowMs);
    const cell = document.createElement("button");
    cell.type = "button";
    cell.className = "cal-day";
    cell.textContent = String(d);
    if (dateStr === todayStr) cell.classList.add("today");
    if (reason) {
      cell.classList.add("disabled");
      cell.disabled = true;
      cell.title = reason;
    } else {
      if (dateStr === selected) cell.classList.add("selected");
      cell.addEventListener("click", () => {
        $("#slot-date").value = dateStr;
        renderSlotCalendar();
        refreshSlots();
      });
    }
    grid.appendChild(cell);
  }
  box.appendChild(grid);
}

// refreshSlots re-renders the open-slot buttons for the selected provider + date +
// duration. Idempotent and safe to call on any of those changing.
async function refreshSlots() {
  const box = $("#slots");
  if (!box) return;
  box.innerHTML = "";
  const provider = $("#provider").value;
  const dateStr = $("#slot-date").value;
  if (!provider || !dateStr) return;
  const p = providerByKey(provider);
  if (!p) return;
  if (!Array.isArray(p.hours) || !p.hours.length) {
    const m = document.createElement("p");
    m.className = "muted";
    m.textContent = "This provider has set no availability hours — enter a date & time above directly.";
    box.appendChild(m);
    return;
  }
  const durationMin = Number($("#duration").value || 30);
  const provAppts = await providerAppointments(provider);
  // The provider's appointments AND the selected patient's appointments (across all
  // providers) both block a slot — the latter so the picker doesn't offer a time the
  // op rejects as a cross-provider PatientDoubleBook.
  const patAppts = state.patient ? await patientAppointments(state.patient) : [];
  // The provider/date/patient may have changed while awaiting the fetches — bail if so.
  if ($("#provider").value !== provider || $("#slot-date").value !== dateStr) return;
  const slots = computeOpenSlots(p, dateStr, durationMin, provAppts.concat(patAppts), Date.now());
  if (!slots.length) {
    const m = document.createElement("p");
    m.className = "muted";
    m.textContent = noSlotsReason(p, dateStr) || "No open slots that day — try another date or a shorter duration.";
    box.appendChild(m);
    return;
  }
  const chosen = $("#startsAt").value ? toRFC3339($("#startsAt").value) : "";
  for (const ms of slots) {
    const iso = new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "slot-btn";
    btn.textContent = slotTimeLabel(ms);
    if (chosen && chosen === iso) btn.classList.add("selected");
    btn.addEventListener("click", () => {
      $("#startsAt").value = toLocalInputValue(iso);
      refreshTimeOffWarning();
      refreshSlots();
    });
    box.appendChild(btn);
  }
}

// findSoonestSlots computes, for each provider matching the given specialty (all
// providers if ""), their single earliest open slot within a bounded look-ahead
// window — so a patient who only knows the specialty they need, not a specific
// provider's name, can see who is soonest available. Stops scanning a provider's
// days once its first open slot is found (only that provider's soonest matters
// here); the full remaining-day picker is still computeOpenSlots via refreshSlots
// once a specific provider is chosen. Mirrors computeOpenSlots' UTC-day grid.
async function findSoonestSlots(specialty, durationMin, nowMs, daysAhead, limit) {
  const candidates = state.providers.filter((p) => !specialty || p.specialty === specialty);
  const patAppts = state.patient ? await patientAppointments(state.patient) : [];
  const results = [];
  for (const p of candidates) {
    if (!Array.isArray(p.hours) || !p.hours.length) continue;
    const provAppts = await providerAppointments(p.providerKey);
    const blocking = provAppts.concat(patAppts);
    for (let d = 0; d < daysAhead; d++) {
      const day = new Date(nowMs + d * 86400000);
      const dateStr = ymdUTC(day.getUTCFullYear(), day.getUTCMonth(), day.getUTCDate());
      const slots = computeOpenSlots(p, dateStr, durationMin, blocking, nowMs);
      if (slots.length) {
        results.push({ ms: slots[0], providerKey: p.providerKey, dateStr });
        break;
      }
    }
  }
  results.sort((a, b) => a.ms - b.ms);
  return results.slice(0, limit || 5);
}

// renderSoonest shows the soonest open slot per matching provider (grouped by the
// selected specialty) so a patient can book without already knowing a provider's
// name. Hidden once a specific provider is chosen below — that provider's own
// calendar (renderSlotCalendar/refreshSlots) takes over from there.
async function renderSoonest() {
  const box = $("#soonest");
  if (!box) return;
  box.innerHTML = "";
  if ($("#provider").value) return;
  const specialty = $("#book-specialty").value;
  const durationMin = Number($("#duration").value || 30);
  const results = await findSoonestSlots(specialty, durationMin, Date.now(), 14, 5);
  // The specialty/duration/provider may have changed while awaiting the fetches.
  if ($("#book-specialty").value !== specialty || Number($("#duration").value || 30) !== durationMin || $("#provider").value) return;
  if (!results.length) {
    const m = document.createElement("p");
    m.className = "muted";
    m.textContent = specialty ? `No open slots for ${specialty} in the next two weeks.` : "No open slots in the next two weeks.";
    box.appendChild(m);
    return;
  }
  const label = document.createElement("p");
  label.className = "hint";
  label.textContent = "Soonest available — pick one, or choose a specific provider below instead.";
  box.appendChild(label);
  for (const r of results) {
    const p = providerByKey(r.providerKey);
    const iso = new Date(r.ms).toISOString().replace(/\.\d{3}Z$/, "Z");
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "slot-btn";
    btn.textContent = `${providerLabel(p)} — ${slotTimeLabel(r.ms)} ${r.dateStr}`;
    btn.addEventListener("click", () => {
      $("#provider").value = r.providerKey;
      $("#slot-date").value = r.dateStr;
      $("#startsAt").value = toLocalInputValue(iso);
      refreshBookEnabled();
      refreshTimeOffWarning();
      renderSlotCalendar();
      refreshSlots();
      renderSoonest();
    });
    box.appendChild(btn);
  }
}

// ---- Book ----

function refreshBookEnabled() {
  const btn = $("#book-submit");
  if (!btn) return;
  const ready = !!state.patient && !!$("#provider").value;
  btn.disabled = !ready;
  btn.title = !state.patient ? "Select a patient first" : !$("#provider").value ? "Select a provider" : "";
}

// toRFC3339 converts a datetime-local value (local wall time, no zone) to a
// canonical UTC RFC3339 instant, as the .schedule aspect expects.
function toRFC3339(localValue) {
  const d = new Date(localValue);
  if (isNaN(d)) return "";
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

function addMinutesRFC3339(localValue, minutes) {
  const d = new Date(localValue);
  if (isNaN(d)) return "";
  d.setMinutes(d.getMinutes() + minutes);
  return d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

// slotCells / slotCellCode mirror the clinic-domain Starlark's grid
// discretization (ddls.go slot_cells/slot_cellcode) so the dispatcher can
// declare each covered cell's slot-claim key as an optionalReads
// (script-read-posture-design.md §13, claim_cell class-d) — an absent slot is
// the common case (no existing booking), never a required read.
const SLOT_GRID_STEP_MINUTES = 15;
const SLOT_MAX_CELLS = 96; // 24h backstop, mirrors MAX_SLOT_CELLS

function slotCells(startsAt, endsAt) {
  const cells = [];
  let cur = new Date(startsAt);
  const end = new Date(endsAt);
  for (let i = 0; i < SLOT_MAX_CELLS + 1 && cur < end; i++) {
    cells.push(cur.toISOString().replace(/\.\d{3}Z$/, "Z"));
    cur = new Date(cur.getTime() + SLOT_GRID_STEP_MINUTES * 60000);
  }
  return cells;
}

function slotCellCode(cellStart) {
  return cellStart.replace(/-/g, "").replace(/:/g, "").toLowerCase();
}

function slotClaimKeys(hub, startsAt, endsAt) {
  return slotCells(startsAt, endsAt).map((c) => hub + ".slot" + slotCellCode(c));
}

// toLocalInputValue formats a stored RFC3339 (UTC) instant back into the local
// "YYYY-MM-DDTHH:MM" a <input type=datetime-local> expects, for prefilling the
// reschedule modal with the appointment's current time.
function toLocalInputValue(rfc3339) {
  const d = new Date(rfc3339);
  if (isNaN(d)) return "";
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// nowLocalInputValue returns the current local wall time as the
// "YYYY-MM-DDTHH:MM" a <input type=datetime-local min=...> expects. Used as a
// first line of defence so the browser's own picker discourages a past time; the
// CreateAppointment / RescheduleAppointment op is the authority (ScheduleInPast).
function nowLocalInputValue() {
  return toLocalInputValue(new Date().toISOString());
}

// durationMinutes derives the appointment length (minutes) from its start/end so
// the reschedule modal can prefill the duration select.
function durationMinutes(startsAt, endsAt) {
  const s = new Date(startsAt);
  const e = new Date(endsAt);
  if (isNaN(s) || isNaN(e) || e <= s) return 30;
  return Math.round((e - s) / 60000);
}

async function submitBook(ev) {
  ev.preventDefault();
  if (!state.patient) {
    toast("Select a patient first.", "err");
    return;
  }
  const provider = $("#provider").value;
  if (!provider) {
    toast("Select a provider.", "err");
    return;
  }
  const when = $("#startsAt").value;
  if (!when) {
    toast("Pick a date and time.", "err");
    return;
  }
  const startsAt = toRFC3339(when);
  const endsAt = addMinutesRFC3339(when, Number($("#duration").value || 30));
  if (!startsAt || !endsAt) {
    toast("That date/time is not valid.", "err");
    return;
  }

  const payload = { patient: state.patient, provider, startsAt, endsAt };
  const reason = $("#reason").value.trim();
  if (reason) payload.reason = reason;

  const asSelf = $("#book-self").checked && !$("#book-self").disabled;

  const submit = $("#book-submit");
  submit.disabled = true;
  try {
    // The op claims a deterministic slot-claim aspect per covered 15-minute grid
    // cell on both the provider and patient hubs — the write-path key collision at
    // commit IS the double-book lock (SlotConflict / PatientDoubleBook), so no
    // per-hub OCC epoch needs to be declared here. Each covered cell's slot-claim
    // key is (d)-declared optionalReads (claim_cell, ddls.go — absence is the
    // common no-existing-booking case; script-read-posture-design.md §13).
    const optionalReads = slotClaimKeys(provider, startsAt, endsAt).concat(
      slotClaimKeys(state.patient, startsAt, endsAt),
    );
    // Resident-visit confinement (Inc 5, mixed-use composition design):
    // if the selected patient's own linked identity matches a lease
    // applicant's identity, attach that lease so CreateAppointment can write
    // a residentVisit link — best-effort, no leaseAppKey attached (and no
    // hard failure) when the patient has no linked identity or no matching
    // lease. Mirrors wellness-app's CreateBooking leaseAppKey wiring.
    const identityKey = patientIdentityKey();
    if (identityKey) {
      try {
        const rs = await api("/api/residents");
        const match = (rs.residents || []).find((r) => r.bookerKey === identityKey);
        if (match) {
          payload.leaseAppKey = match.leaseAppKey;
          optionalReads.push(
            match.leaseAppKey,
            match.leaseAppKey + ".tenancy",
          );
        }
      } catch (_) { /* residents lookup unreachable — book without lease confinement */ }
    }
    const reply = await submitOp("CreateAppointment", "appointment", payload,
      [state.patient, provider], { asSelf, optionalReads });
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Booking rejected — " + friendlyBookingRejection(msg), "err");
      return;
    }
    const key = reply && reply.primaryKey ? reply.primaryKey : "";
    // The new appointment invalidates this provider's AND this patient's cached
    // slot sets (the patient now has one more appointment to exclude elsewhere).
    delete state.slotApptCache[provider];
    delete state.slotPatientApptCache[state.patient];
    $("#book-form").reset();
    refreshSlots();
    toast("Appointment booked.", "ok", key);
    // Route to My Appointments with the new appointment highlighted (the lens may
    // take a moment to project; a Refresh shows it once projected).
    state.highlight = key || null;
    showView("appts");
  } catch (e) {
    toast("Could not book: " + e.message, "err");
  } finally {
    refreshBookEnabled();
  }
}

// ---- My Appointments (scoped to the selected patient) ----

async function loadAppts() {
  const grid = $("#appts");
  const empty = $("#appts-empty");
  if (!state.patient) {
    grid.innerHTML = "";
    state.appts = [];
    empty.hidden = false;
    empty.textContent = "Select a patient above to see their appointments.";
    $("#appts-summary").textContent = "";
    return;
  }
  $("#appts-summary").textContent = "loading…";
  try {
    // D1.5: the PROTECTED, RLS-scoped, authenticated read (see
    // patientAppointments' doc above the sibling slot-picker call) — replaces
    // the old unauthenticated `?patient=` vector that let anyone impersonate any
    // patient.
    const data = await authedGet("/api/my-appointments");
    state.appts = data.appointments || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load appointments: " + e.message;
    $("#appts-summary").textContent = "";
    return;
  }
  renderAppts();
}

// apptMatchesFilter applies the My Appointments status filter ("all" | "active" |
// a single status). "active" keeps the non-terminal lifecycle states.
function apptMatchesFilter(a, filter) {
  if (filter === "all") return true;
  if (filter === "active") return ACTIVE_STATUSES.includes(a.status);
  return a.status === filter;
}

function renderAppts() {
  const grid = $("#appts");
  const empty = $("#appts-empty");
  grid.innerHTML = "";
  if (state.appts.length === 0) {
    empty.hidden = false;
    empty.textContent = "No appointments yet. Book one on the Book tab.";
    $("#appts-summary").textContent = "";
    return;
  }

  const filter = ($("#appts-filter") && $("#appts-filter").value) || "all";
  const matched = state.appts.filter((a) => apptMatchesFilter(a, filter));

  // Split upcoming vs past so the patient's next appointment leads (the API sorts
  // ascending, which otherwise buries it under accumulated history). Upcoming reads
  // soonest-first; Past reads most-recent-first.
  const upcoming = matched.filter((a) => !isPast(a.startsAt));
  const past = matched.filter((a) => isPast(a.startsAt)).reverse();

  if (matched.length === 0) {
    empty.hidden = false;
    empty.textContent = "No appointments match this filter.";
    $("#appts-summary").textContent = `0 of ${state.appts.length}`;
    return;
  }
  empty.hidden = true;

  const section = (label, rows) => {
    if (rows.length === 0) return;
    const head = document.createElement("div");
    head.className = "appts-section-head";
    head.textContent = `${label} · ${rows.length}`;
    grid.append(head);
    for (const a of rows) grid.append(renderApptCard(a, { showProvider: true, cancelable: true }));
  };
  section("Upcoming", upcoming);
  section("Past", past);

  const n = matched.length;
  const suffix = filter === "all" ? "" : ` of ${state.appts.length}`;
  $("#appts-summary").textContent = `${n} appointment${n === 1 ? "" : "s"}${suffix}`;
}

// ---- Follow-ups worklist (clinic-wide staff queue) ----
//
// A documented visit (RecordEncounter) can flag followUpRequested + an optional
// followUpDate — operational, non-PHI signals the clinicAppointments lens projects
// (P5: read the lens read model, never Core KV). This tab is the clinic-wide queue of
// those requests so one does not silently fall through: it reads EVERY appointment
// (not the patient-scoped My Appointments view) and keeps the flagged ones. A
// follow-up reads as "addressed" once a later non-cancelled appointment sits on the
// same patient's record (the natural close-the-loop signal); the default filter hides
// those so the list behaves as a worklist that empties.
//
// Reads the PROTECTED, RLS-scoped /api/staff/appointments (D1.5, the staff
// wildcard increment) — a staff actor's WildcardAnchor grant returns every
// appointment, closing the unauthenticated clinic-wide dump the unprotected
// /api/appointments used to serve.

async function loadFollowups() {
  const grid = $("#followups");
  const empty = $("#followups-empty");
  $("#followups-summary").textContent = "loading…";
  let all;
  try {
    const data = await authedGetAsStaff("/api/staff/appointments");
    all = data.appointments || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load follow-ups: " + e.message;
    $("#followups-summary").textContent = "";
    return;
  }
  const requested = all.filter((a) => a.followUpRequested);
  for (const f of requested) f._addressed = hasLaterVisit(f, all);
  state.followups = requested;
  renderFollowups();
}

// hasLaterVisit reports whether the patient has another non-cancelled appointment
// after this one — the heuristic that a requested follow-up has since been booked.
function hasLaterVisit(f, all) {
  return all.some(
    (g) =>
      g.appointmentKey !== f.appointmentKey &&
      g.patientKey === f.patientKey &&
      (g.status || "").toLowerCase() !== "cancelled" &&
      g.startsAt > f.startsAt,
  );
}

// followupUrgency buckets a follow-up by its target date relative to today (local):
// overdue (date passed), soon (within 14 days), later, or nodate.
function followupUrgency(f) {
  const date = (f.followUpDate || "").slice(0, 10);
  if (!date) return "nodate";
  if (date < localDateStr(0)) return "overdue";
  if (date <= localDateStr(14)) return "soon";
  return "later";
}

// localDateStr returns today + offsetDays as a YYYY-MM-DD string in local time, for
// lexical comparison against a follow-up's YYYY-MM-DD target date.
function localDateStr(offsetDays) {
  const d = new Date();
  d.setDate(d.getDate() + offsetDays);
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${d.getFullYear()}-${m}-${day}`;
}

const FOLLOWUP_GROUPS = [
  { key: "overdue", label: "Overdue" },
  { key: "soon", label: "Due soon (next 14 days)" },
  { key: "later", label: "Upcoming" },
  { key: "nodate", label: "No target date" },
];

const FOLLOWUP_BADGE = { overdue: "Overdue", soon: "Due soon", later: "Upcoming", nodate: "No date" };

function renderFollowups() {
  const grid = $("#followups");
  const empty = $("#followups-empty");
  grid.innerHTML = "";

  if (state.followups.length === 0) {
    empty.hidden = false;
    empty.textContent = "No follow-ups requested yet. Document a completed visit and tick “Follow-up needed”.";
    $("#followups-summary").textContent = "";
    return;
  }

  const filter = ($("#followups-filter") && $("#followups-filter").value) || "outstanding";
  const rows = state.followups.filter((f) => filter === "all" || !f._addressed);
  if (rows.length === 0) {
    empty.hidden = false;
    empty.textContent = "No outstanding follow-ups — every requested follow-up has a later visit booked.";
    $("#followups-summary").textContent = `0 of ${state.followups.length}`;
    return;
  }
  empty.hidden = true;

  // Sort by target date (no-date last), then patient — overdue floats to the top.
  const sorted = rows.slice().sort((a, b) => {
    const da = (a.followUpDate || "9999").slice(0, 10);
    const db = (b.followUpDate || "9999").slice(0, 10);
    if (da !== db) return da < db ? -1 : 1;
    return (a.patientName || a.patientKey) < (b.patientName || b.patientKey) ? -1 : 1;
  });

  for (const g of FOLLOWUP_GROUPS) {
    const inGroup = sorted.filter((f) => followupUrgency(f) === g.key);
    if (inGroup.length === 0) continue;
    const head = document.createElement("div");
    head.className = "appts-section-head";
    head.textContent = `${g.label} · ${inGroup.length}`;
    grid.append(head);
    for (const f of inGroup) grid.append(renderFollowupCard(f));
  }

  const n = rows.length;
  const suffix = filter === "all" ? "" : ` of ${state.followups.length}`;
  $("#followups-summary").textContent = `${n} follow-up${n === 1 ? "" : "s"}${suffix}`;
}

function renderFollowupCard(f) {
  const card = document.createElement("div");
  card.className = "card";

  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = f.patientName || shortKey(f.patientKey);

  const sub = document.createElement("div");
  sub.className = "addr-sub";
  if (f.providerName) {
    sub.textContent = "with " + f.providerName + (f.providerSpecialty ? " · " + f.providerSpecialty : "");
  }

  const visit = document.createElement("div");
  visit.className = "meta";
  const vd = new Date(f.documentedAt || f.startsAt);
  visit.textContent = "Visit " + (isNaN(vd) ? "" : vd.toLocaleDateString()) + (f.reason ? " · " + f.reason : "");

  const target = document.createElement("div");
  target.className = "when";
  target.textContent = f.followUpDate ? "Follow up by " + f.followUpDate.slice(0, 10) : "Follow-up requested (no date)";

  const actions = document.createElement("div");
  actions.className = "card-actions";

  const badges = document.createElement("span");
  badges.className = "card-btns";
  const urg = followupUrgency(f);
  const badge = document.createElement("span");
  badge.className = "badge followup-" + urg;
  badge.textContent = FOLLOWUP_BADGE[urg];
  badges.append(badge);
  if (f._addressed) {
    const ad = document.createElement("span");
    ad.className = "badge followup-addressed";
    ad.textContent = "Later visit booked";
    badges.append(ad);
  }
  // The at-the-date follow-up reminder, once the clinic-reminders followUpReminders
  // orchestration has fired it (surfaced via the clinicAppointments lens's
  // followUpReminderSentAt column). Absent until the @at fires / when clinic-reminders
  // is not installed.
  if (f.followUpReminderSentAt) {
    const r = new Date(f.followUpReminderSentAt);
    const rem = document.createElement("span");
    rem.className = "badge reminder-sent";
    rem.textContent = "🔔 Reminder sent" + (isNaN(r) ? "" : " · " + r.toLocaleDateString());
    badges.append(rem);
  }
  actions.append(badges);

  const btns = document.createElement("span");
  btns.className = "card-btns";
  const book = document.createElement("button");
  book.className = "ghost";
  book.textContent = "Book follow-up";
  book.addEventListener("click", () => bookFollowup(f));
  btns.append(book);
  actions.append(btns);

  card.append(title);
  if (sub.textContent) card.append(sub);
  card.append(visit);
  card.append(target);
  card.append(actions);
  return card;
}

// bookFollowup drops the user into the Book tab pre-filled with the follow-up's
// patient (the global patient context) and provider, so a requested follow-up is one
// click from being scheduled.
function bookFollowup(f) {
  const sel = $("#patient");
  if (sel && [...sel.options].some((o) => o.value === f.patientKey)) sel.value = f.patientKey;
  setPatient(f.patientKey);
  const prov = $("#provider");
  if (prov && [...prov.options].some((o) => o.value === f.providerKey)) {
    prov.value = f.providerKey;
    prov.dispatchEvent(new Event("change"));
  }
  showView("book");
  toast("Booking a follow-up for " + (f.patientName || shortKey(f.patientKey)) + ". Pick a date & time.", "ok");
}

// ---- Recurring visit series (clinic-wide worklist + the patient's own list) ----
//
// A patient on a standing cadence (chronic-care monthly check-ins, weekly PT) can
// have a recurring visit series started against a provider (StartVisitSeries).
// The clinic-reminders visitSeriesRead PROTECTED Postgres read model (D1.5) rolls
// the series' own "next visit due" deadline forward every time it converges — this
// section renders BOTH the clinic-wide "Series" tab worklist (the AUTHENTICATED
// staff wildcard read, /api/staff/visit-series → state.series) and the selected
// patient's own series list + start/pause/resume controls on the My Appointments
// tab (the AUTHENTICATED patient-self RLS read, /api/my-visit-series →
// state.mySeries).

async function loadSeries() {
  const grid = $("#series");
  const empty = $("#series-empty");
  if ($("#series-summary")) $("#series-summary").textContent = "loading…";
  try {
    const data = await authedGetAsStaff("/api/staff/visit-series");
    state.series = data.series || [];
  } catch (e) {
    grid.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load recurring visit series: " + e.message;
    $("#series-summary").textContent = "";
    renderSeries();
    loadMySeries();
    return;
  }
  renderSeries();
  loadMySeries();
}

// loadMySeries fetches the selected patient's own recurring visit series from
// the PROTECTED, RLS-scoped /api/my-visit-series (D1.5) — the sibling of
// loadSeries' clinic-wide staff fetch.
async function loadMySeries() {
  if (!state.patient) {
    state.mySeries = [];
    renderMySeries();
    return;
  }
  try {
    const data = await authedGet("/api/my-visit-series");
    state.mySeries = data.series || [];
  } catch (e) {
    state.mySeries = [];
    renderMySeries();
    return;
  }
  renderMySeries();
}

// ---- Billing ledger (view + record charges/payments) ----
//
// One row of the clinic-ledger `clinicLedgerHistory` lens per posted
// transaction, read via GET /api/ledger?patientKey= (P5 — a lens read model,
// never Core KV). The account key is independently minted (never derived
// from the patient's own NanoID), so GET /api/ledger resolves it server-side
// via the `clinicPatientAccounts` lens and returns "" when the patient hasn't
// opened one yet — the FE never guesses it. Mirrors loftspace-app's payment
// ledger, keyed by patient instead of lease. /api/ledger is staff-authenticated
// (authedGetAsStaff) — it is a front-desk-only view today, gated the same as
// /api/staff/patients.

// moneyAmount formats a cents amount as a USD-style dollar figure.
function moneyAmount(cents) {
  return typeof cents === "number" ? "$" + (cents / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 }) : "—";
}

// loadLedger (re)loads and renders the selected patient's billing panel: the
// running balance, the transaction list (oldest first). Bails to an empty
// state with no patient selected.
async function loadLedger() {
  const balanceEl = $("#ledger-balance");
  const list = $("#ledger-list");
  const empty = $("#ledger-empty");
  if (!state.patient) {
    balanceEl.textContent = "";
    list.innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Select a patient above to see their billing history.";
    state.ledger = null;
    return;
  }
  balanceEl.textContent = "Loading…";
  list.innerHTML = "";
  empty.hidden = true;
  let data;
  try {
    data = await authedGetAsStaff("/api/ledger?patientKey=" + encodeURIComponent(state.patient));
  } catch (e) {
    balanceEl.textContent = "";
    empty.hidden = false;
    empty.textContent = "Could not load billing history: " + e.message;
    state.ledger = null;
    return;
  }
  state.ledger = data;
  renderLedger(data);
}

// renderLedger paints the balance + transaction list from the last loaded
// /api/ledger response.
function renderLedger(data) {
  const balanceEl = $("#ledger-balance");
  const list = $("#ledger-list");
  const empty = $("#ledger-empty");

  const owed = data.balanceCents || 0;
  if (owed > 0) balanceEl.textContent = "Balance owed: " + moneyAmount(owed);
  else if (owed < 0) balanceEl.textContent = "Credit balance: " + moneyAmount(-owed);
  else balanceEl.textContent = "Balance: $0.00 (paid in full)";

  const txs = data.transactions || [];
  list.innerHTML = "";
  if (txs.length === 0) {
    empty.hidden = false;
    empty.textContent = "No charges or payments recorded yet.";
    return;
  }
  empty.hidden = true;
  for (const t of txs) {
    const li = document.createElement("li");
    li.className = "ledger-entry " + t.type;
    const sign = t.type === "debit" ? "+" : "−";
    const d = new Date(t.postedAt);
    const when = isNaN(d) ? t.postedAt : d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
    li.textContent = when + " · " + sign + moneyAmount(t.amountCents) + (t.memo ? " — " + t.memo : "");
    list.append(li);
  }
}

// openLedgerAccount opens the patient's ledger account (CreateAccount) and
// returns its freshly-minted key. The account carries its OWN independent
// NanoID (never derived from the patient's — Core KV NanoIDs are unique
// platform-wide, not reused across vertex types), so the ONLY reliable
// source for it is the ACCEPTED reply's primaryKey. reads declares
// patientKey only — the guard aspect that enforces one-account-per-patient
// doesn't exist yet on this (first-ever) call, and the Processor hard-rejects
// a contextHint.reads key that doesn't exist (HydrationMiss), so declaring it
// here would make account-opening impossible rather than idempotent.
async function openLedgerAccount(patientKey) {
  const reply = await submitOp("CreateAccount", "clinicaccount", { patientKey }, [patientKey]);
  if (reply && reply.status === "accepted" && reply.primaryKey) {
    return reply.primaryKey;
  }
  // A genuine race (two concurrent first-opens for the same patient) fails
  // the loser's guard-aspect create-only write — re-fetch the ledger, which
  // resolves the account key via the clinicPatientAccounts lens regardless of
  // which side won.
  const data = await authedGetAsStaff("/api/ledger?patientKey=" + encodeURIComponent(patientKey));
  if (data.accountKey) return data.accountKey;
  const msg = rejectionMessage(reply);
  throw new Error(msg || "could not open the ledger account");
}

// submitLedgerEntry posts a DebitAccount/CreditAccount against the selected
// patient's ledger account, opening the account first if this is its
// first-ever charge or payment (state.ledger.accountKey empty).
async function submitLedgerEntry(opType, what) {
  if (!state.patient) {
    toast("Select a patient first.", "err");
    return;
  }
  const amountInput = $("#ledger-amount");
  const memoInput = $("#ledger-memo");
  const dollars = Number(amountInput.value);
  if (!(dollars > 0)) {
    toast("Enter an amount greater than zero.", "err");
    return;
  }
  const cents = Math.round(dollars * 100);
  const chargeBtn = $("#ledger-charge");
  const paymentBtn = $("#ledger-payment");
  chargeBtn.disabled = paymentBtn.disabled = true;
  try {
    let accountKey = state.ledger && state.ledger.accountKey;
    if (!accountKey) accountKey = await openLedgerAccount(state.patient);
    const reply = await submitOp(
      opType,
      "clinictransaction",
      { accountKey, amountCents: cents, memo: memoInput.value.trim() || undefined },
      [accountKey]
    );
    const msg = rejectionMessage(reply);
    if (msg) throw new Error(msg);
    toast(what.charAt(0).toUpperCase() + what.slice(1) + " recorded.", "ok");
    amountInput.value = "";
    memoInput.value = "";
    setTimeout(loadLedger, 700);
  } catch (e) {
    toast("Could not " + what + " — " + e.message, "err");
  } finally {
    chargeBtn.disabled = paymentBtn.disabled = false;
  }
}

// seriesUrgency buckets a series by how soon its next occurrence is due, mirroring
// followupUrgency: overdue (today or past), soon (within 14 days), later, or
// inactive (paused, or past its activeUntil — the lens's "active" column already
// folds both cases together, so the FE does not distinguish them).
function seriesUrgency(s) {
  if (!s.active) return "inactive";
  const date = (s.nextDueAt || "").slice(0, 10);
  if (!date) return "later";
  if (date < localDateStr(0)) return "overdue";
  if (date <= localDateStr(14)) return "soon";
  return "later";
}

const SERIES_GROUPS = [
  { key: "overdue", label: "Due now" },
  { key: "soon", label: "Due soon (next 14 days)" },
  { key: "later", label: "Upcoming" },
  { key: "inactive", label: "Paused / ended" },
];

const SERIES_BADGE = { overdue: "Due now", soon: "Due soon", later: "Upcoming", inactive: "Inactive" };

function renderSeries() {
  const grid = $("#series");
  const empty = $("#series-empty");
  grid.innerHTML = "";

  if (state.series.length === 0) {
    empty.hidden = false;
    empty.textContent = "No recurring visit series yet. Start one from a patient's My Appointments tab.";
    $("#series-summary").textContent = "";
    return;
  }

  const filter = ($("#series-filter") && $("#series-filter").value) || "active";
  const rows = state.series.filter((s) => filter === "all" || s.active);
  if (rows.length === 0) {
    empty.hidden = false;
    empty.textContent = "No active recurring visit series.";
    $("#series-summary").textContent = `0 of ${state.series.length}`;
    return;
  }
  empty.hidden = true;

  const sorted = rows.slice().sort((a, b) => {
    const da = a.nextDueAt || "9999";
    const db = b.nextDueAt || "9999";
    if (da !== db) return da < db ? -1 : 1;
    return (a.patientName || a.patientKey) < (b.patientName || b.patientKey) ? -1 : 1;
  });

  for (const g of SERIES_GROUPS) {
    const inGroup = sorted.filter((s) => seriesUrgency(s) === g.key);
    if (inGroup.length === 0) continue;
    const head = document.createElement("div");
    head.className = "appts-section-head";
    head.textContent = `${g.label} · ${inGroup.length}`;
    grid.append(head);
    for (const s of inGroup) grid.append(renderSeriesCard(s));
  }

  const n = rows.length;
  const suffix = filter === "all" ? "" : ` of ${state.series.length}`;
  $("#series-summary").textContent = `${n} series${suffix}`;
}

function renderSeriesCard(s) {
  const card = document.createElement("div");
  card.className = "card";

  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = s.patientName || shortKey(s.patientKey);

  const sub = document.createElement("div");
  sub.className = "addr-sub";
  if (s.providerName) sub.textContent = "with " + s.providerName + (s.providerSpecialty ? " · " + s.providerSpecialty : "");

  const cadence = document.createElement("div");
  cadence.className = "meta";
  cadence.textContent = "Every " + s.intervalDays + " day" + (s.intervalDays === 1 ? "" : "s") + " · occurrence " + (s.occurrenceCount + 1);

  const due = document.createElement("div");
  due.className = "when";
  due.textContent = s.active ? (s.nextDueAt ? "Next due " + s.nextDueAt.slice(0, 10) : "No upcoming occurrence") : "Paused or ended";

  const actions = document.createElement("div");
  actions.className = "card-actions";

  const badges = document.createElement("span");
  badges.className = "card-btns";
  const urg = seriesUrgency(s);
  const badge = document.createElement("span");
  // Reuses the follow-ups badge palette (overdue/soon/later red-amber-neutral,
  // "addressed" grey for the inactive bucket) rather than introducing new colors
  // for the same urgency semantics.
  badge.className = "badge followup-" + (urg === "inactive" ? "addressed" : urg);
  badge.textContent = SERIES_BADGE[urg];
  badges.append(badge);
  actions.append(badges);

  const btns = document.createElement("span");
  btns.className = "card-btns";
  const book = document.createElement("button");
  book.className = "ghost";
  book.textContent = "Book";
  book.addEventListener("click", () => bookSeriesOccurrence(s));
  btns.append(book);
  actions.append(btns);

  card.append(title);
  if (sub.textContent) card.append(sub);
  card.append(cadence);
  card.append(due);
  card.append(actions);
  return card;
}

// bookSeriesOccurrence drops the user into the Book tab pre-filled with the
// series' patient and provider (the bookFollowup precedent) — booking the actual
// appointment is a separate, ordinary CreateAppointment; the series itself only
// tracks when the next one is due.
function bookSeriesOccurrence(s) {
  const sel = $("#patient");
  if (sel && [...sel.options].some((o) => o.value === s.patientKey)) sel.value = s.patientKey;
  setPatient(s.patientKey);
  const prov = $("#provider");
  if (prov && [...prov.options].some((o) => o.value === s.providerKey)) {
    prov.value = s.providerKey;
    prov.dispatchEvent(new Event("change"));
  }
  showView("book");
  toast("Booking the recurring visit for " + (s.patientName || shortKey(s.patientKey)) + ". Pick a date & time.", "ok");
}

// renderMySeries fills the My Appointments tab's "Recurring visit series" panel
// with the selected patient's own series — state.mySeries, the PROTECTED,
// RLS-scoped fetch (/api/my-visit-series), already scoped server-side to this
// patient (no client-side filter needed).
function renderMySeries() {
  const list = $("#my-series-list");
  const empty = $("#my-series-empty");
  if (!list || !empty) return;
  list.innerHTML = "";
  if (!state.patient) {
    empty.hidden = false;
    empty.textContent = "Select a patient above to see or start a recurring visit series.";
    return;
  }
  const mine = state.mySeries;
  if (mine.length === 0) {
    empty.hidden = false;
    empty.textContent = "No recurring visit series for this patient yet.";
    return;
  }
  empty.hidden = true;
  const sorted = mine.slice().sort((a, b) => ((a.nextDueAt || "9999") < (b.nextDueAt || "9999") ? -1 : 1));
  for (const s of sorted) list.append(renderMySeriesCard(s));
}

function renderMySeriesCard(s) {
  const card = document.createElement("div");
  card.className = "card";

  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = s.providerName ? "with " + s.providerName : "Recurring series";

  const cadence = document.createElement("div");
  cadence.className = "meta";
  cadence.textContent = "Every " + s.intervalDays + " day" + (s.intervalDays === 1 ? "" : "s") + " · occurrence " + (s.occurrenceCount + 1);

  const due = document.createElement("div");
  due.className = "when";
  due.textContent = s.active ? (s.nextDueAt ? "Next due " + s.nextDueAt.slice(0, 10) : "No upcoming occurrence") : "Paused";

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const btns = document.createElement("span");
  btns.className = "card-btns";
  const toggle = document.createElement("button");
  toggle.className = "ghost";
  toggle.textContent = s.active ? "Pause" : "Resume";
  toggle.addEventListener("click", () => toggleSeries(s));
  btns.append(toggle);
  actions.append(btns);

  card.append(title);
  card.append(cadence);
  card.append(due);
  card.append(actions);
  return card;
}

// toggleSeries submits Pause/ResumeVisitSeries for one series and reloads. Resuming
// a series whose activeUntil has already passed is a harmless no-op — the lens's
// "active" column stays false because the term is (not paused) AND (within
// activeUntil), and there is no FE affordance yet to distinguish that from a plain
// pause (the design's noted, deferred skip-to-latest / re-arm case).
async function toggleSeries(s) {
  const op = s.active ? "PauseVisitSeries" : "ResumeVisitSeries";
  try {
    const reply = await submitOp(op, "", { seriesKey: s.entityKey }, [s.entityKey]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast(msg, "err");
      return;
    }
    toast(s.active ? "Series paused." : "Series resumed.", "ok");
    loadSeries();
  } catch (e) {
    toast("Could not update series: " + e.message, "err");
  }
}

// submitStartSeries submits StartVisitSeries for the selected patient against the
// chosen provider, then closes the inline form and reloads.
async function submitStartSeries() {
  if (!state.patient) {
    toast("Select a patient first.", "err");
    return;
  }
  const providerKey = $("#series-provider").value;
  if (!providerKey) {
    toast("Choose a provider.", "err");
    return;
  }
  const intervalDays = parseInt($("#series-interval").value, 10);
  if (!intervalDays || intervalDays <= 0) {
    toast("Enter a positive interval in days.", "err");
    return;
  }
  const startDate = $("#series-start").value;
  if (!startDate) {
    toast("Pick a first-occurrence date.", "err");
    return;
  }
  const payload = { patientKey: state.patient, providerKey, intervalDays, startAt: startDate + "T09:00:00Z" };
  const endDate = $("#series-end").value;
  if (endDate) payload.activeUntil = endDate + "T09:00:00Z";

  const submit = $("#series-start-submit");
  submit.disabled = true;
  try {
    const reply = await submitOp("StartVisitSeries", "", payload, [state.patient, providerKey]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast(msg, "err");
      return;
    }
    toast("Recurring visit series started.", "ok");
    $("#series-interval").value = "30";
    $("#series-start").value = "";
    $("#series-end").value = "";
    $("#start-series").open = false;
    loadSeries();
  } catch (e) {
    toast("Could not start series: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Provider Schedule (read-only day/week calendar desk view) ----
//
// The Schedule tab is a positioned calendar grid: a time axis down the left, one
// column per day (7 in Week view, 1 in Day view), and each appointment rendered as
// a block sized to its duration and coloured by status. A single provider reads
// the PROTECTED /api/my-schedule (RLS, provider-self anchor, D1.5 Increment 2) —
// the dropdown selection IS the "acting as" context, minting a token for that
// provider, mirroring the patient switcher's minting semantics. "All providers"
// mode reads the PROTECTED, RLS-scoped /api/staff/appointments (D1.5, the staff
// wildcard increment) as the staff actor. Either way the grid filters
// client-side to the visible period (no date-range query needed). Clicking a
// block opens a read-only detail panel — the desk view doesn't mutate
// (Cancel / Reschedule live on My Appointments).

const PX_PER_HOUR = 44;

// schedIsAll reports whether the Schedule tab is in clinic-wide "All providers"
// mode (every provider on one grid), vs scoped to a single provider.
function schedIsAll() {
  return $("#sched-provider").value === SCHED_ALL;
}

async function loadSchedule() {
  const provider = $("#sched-provider").value;
  const empty = $("#schedule-empty");
  hideSchedDetail();
  if (!provider) {
    $("#schedule").innerHTML = "";
    state.schedule = [];
    empty.hidden = false;
    empty.textContent = "Choose a provider — or All providers — to see the schedule.";
    $("#schedule-summary").textContent = "";
    $("#sched-range").textContent = "";
    return;
  }
  $("#schedule-summary").textContent = "loading…";
  try {
    // All-providers mode reads the PROTECTED, RLS-scoped staff model (the
    // WildcardAnchor grant); a single provider reads the PROTECTED,
    // RLS-scoped model as that provider.
    const data =
      provider === SCHED_ALL
        ? await authedGetAsStaff("/api/staff/appointments")
        : await authedGetAsProvider("/api/my-schedule", provider);
    state.schedule = data.appointments || [];
  } catch (e) {
    $("#schedule").innerHTML = "";
    empty.hidden = false;
    empty.textContent = "Could not load schedule: " + e.message;
    $("#schedule-summary").textContent = "";
    $("#sched-range").textContent = "";
    return;
  }
  renderSchedule();
}

// ---- date helpers (local wall-clock; the grid is laid out in the operator's zone) ----

function startOfDay(d) {
  const x = new Date(d);
  x.setHours(0, 0, 0, 0);
  return x;
}

// startOfWeek returns local midnight on the Monday of d's week (Mon-first columns).
function startOfWeek(d) {
  const x = startOfDay(d);
  const dow = (x.getDay() + 6) % 7; // 0=Mon … 6=Sun
  x.setDate(x.getDate() - dow);
  return x;
}

function addDays(d, n) {
  const x = new Date(d);
  x.setDate(x.getDate() + n);
  return x;
}

// schedPeriodStart is the local midnight that begins the visible period — the
// Monday of the anchored week, or the anchored day. Defaults to the current week/day
// when no period has been navigated to yet.
function schedPeriodStart() {
  const base = state.schedAnchor || new Date();
  return state.schedView === "week" ? startOfWeek(base) : startOfDay(base);
}

function fmtHour(h) {
  if (h % 24 === 0) return "12 AM";
  if (h === 12) return "12 PM";
  return h < 12 ? `${h} AM` : `${h - 12} PM`;
}

function rangeLabel(periodStart, days) {
  if (days === 1) {
    return periodStart.toLocaleDateString(undefined, {
      weekday: "long", month: "long", day: "numeric", year: "numeric",
    });
  }
  const end = addDays(periodStart, days - 1);
  const sMonth = periodStart.toLocaleDateString(undefined, { month: "short" });
  const eMonth = end.toLocaleDateString(undefined, { month: "short" });
  const y = end.getFullYear();
  return periodStart.getMonth() === end.getMonth()
    ? `${sMonth} ${periodStart.getDate()} – ${end.getDate()}, ${y}`
    : `${sMonth} ${periodStart.getDate()} – ${eMonth} ${end.getDate()}, ${y}`;
}

function renderSchedule() {
  const cal = $("#schedule");
  const empty = $("#schedule-empty");
  cal.innerHTML = "";
  if (!$("#sched-provider").value) {
    empty.hidden = false;
    empty.textContent = "Choose a provider — or All providers — to see the schedule.";
    $("#schedule-summary").textContent = "";
    $("#sched-range").textContent = "";
    return;
  }

  const days = state.schedView === "week" ? 7 : 1;
  const periodStart = schedPeriodStart();
  const periodEnd = addDays(periodStart, days);
  const visible = state.schedule.filter((a) => {
    const s = new Date(a.startsAt);
    return !isNaN(s) && s >= periodStart && s < periodEnd;
  });

  $("#sched-range").textContent = rangeLabel(periodStart, days);
  const n = visible.length;
  const total = state.schedule.length;
  $("#schedule-summary").textContent =
    `${n} this ${state.schedView}` + (total > n ? ` · ${total} total` : "");

  // The hour window fits the visible appointments, never narrower than 8 AM–6 PM.
  let startH = 8;
  let endH = 18;
  for (const a of visible) {
    const s = new Date(a.startsAt);
    const e = new Date(a.endsAt);
    if (!isNaN(s)) startH = Math.min(startH, s.getHours());
    if (!isNaN(e)) endH = Math.max(endH, e.getMinutes() > 0 ? e.getHours() + 1 : e.getHours());
  }
  startH = Math.max(0, startH);
  endH = Math.min(24, Math.max(endH, startH + 1));

  // The (possibly empty) grid is the source of truth — an empty period still
  // renders the week/day structure, with the summary reading "0 this week". The
  // dashed empty placeholder is reserved for "no provider chosen".
  empty.hidden = true;
  cal.append(buildCalendar(periodStart, days, startH, endH, visible));
}

function buildCalendar(periodStart, days, startH, endH, appts) {
  const wrap = document.createElement("div");
  wrap.className = "cal-wrap";
  wrap.style.setProperty("--cal-days", String(days));
  const bodyH = (endH - startH) * PX_PER_HOUR;
  const todayMid = startOfDay(new Date()).getTime();

  const head = document.createElement("div");
  head.className = "cal-head";
  head.append(document.createElement("div")); // empty corner over the time gutter
  for (let i = 0; i < days; i++) {
    const day = addDays(periodStart, i);
    const h = document.createElement("div");
    h.className = "cal-day-head";
    if (startOfDay(day).getTime() === todayMid) h.classList.add("today");
    const wd = document.createElement("span");
    wd.className = "cal-wd";
    wd.textContent = day.toLocaleDateString(undefined, { weekday: "short" });
    const dn = document.createElement("span");
    dn.className = "cal-dn";
    dn.textContent = day.toLocaleDateString(undefined, { month: "short", day: "numeric" });
    h.append(wd, dn);
    head.append(h);
  }
  wrap.append(head);

  const body = document.createElement("div");
  body.className = "cal-body";
  body.style.height = bodyH + "px";

  const gutter = document.createElement("div");
  gutter.className = "cal-gutter";
  for (let h = startH; h <= endH; h++) {
    const lab = document.createElement("div");
    lab.className = "cal-hour";
    lab.style.top = (h - startH) * PX_PER_HOUR + "px";
    lab.textContent = fmtHour(h);
    gutter.append(lab);
  }
  body.append(gutter);

  for (let i = 0; i < days; i++) {
    const dayStart = startOfDay(addDays(periodStart, i));
    const col = document.createElement("div");
    col.className = "cal-col";
    col.style.backgroundImage =
      `repeating-linear-gradient(to bottom, transparent 0, transparent ${PX_PER_HOUR - 1}px, ` +
      `var(--border) ${PX_PER_HOUR - 1}px, var(--border) ${PX_PER_HOUR}px)`;
    const dayAppts = appts.filter((a) => {
      const s = new Date(a.startsAt);
      return !isNaN(s) && startOfDay(s).getTime() === dayStart.getTime();
    });
    for (const placed of layoutDay(dayAppts)) {
      col.append(apptBlock(placed.a, dayStart, startH, endH, placed.lane, placed.lanes));
    }
    body.append(col);
  }
  wrap.append(body);
  return wrap;
}

// layoutDay greedily assigns overlapping appointments to side-by-side lanes so
// double-booked slots (booking enforces no conflict yet) render next to each other
// instead of stacking. Returns each appointment with its lane index and the day's
// total lane count (used for block width).
function layoutDay(appts) {
  const items = appts
    .map((a) => ({ a, s: new Date(a.startsAt).getTime(), e: new Date(a.endsAt).getTime() }))
    .filter((x) => !isNaN(x.s))
    .sort((x, y) => x.s - y.s || x.e - y.e);
  const laneEnds = [];
  for (const it of items) {
    const end = isNaN(it.e) || it.e <= it.s ? it.s + 30 * 60000 : it.e;
    let lane = laneEnds.findIndex((le) => le <= it.s);
    if (lane === -1) {
      lane = laneEnds.length;
      laneEnds.push(end);
    } else {
      laneEnds[lane] = end;
    }
    it.lane = lane;
  }
  const lanes = Math.max(1, laneEnds.length);
  return items.map((it) => ({ a: it.a, lane: it.lane, lanes }));
}

function apptBlock(a, dayStart, startH, endH, lane, lanes) {
  const s = new Date(a.startsAt);
  let e = new Date(a.endsAt);
  if (isNaN(e) || e <= s) e = new Date(s.getTime() + 30 * 60000);
  const winTop = startH * 60;
  const winBot = endH * 60;
  const startMin = (s - dayStart) / 60000;
  const endMin = (e - dayStart) / 60000;
  const top = ((Math.max(startMin, winTop) - winTop) / 60) * PX_PER_HOUR;
  const bottom = ((Math.min(endMin, winBot) - winTop) / 60) * PX_PER_HOUR;
  const height = Math.max(bottom - top, 20);
  const widthPct = 100 / lanes;

  const block = document.createElement("button");
  block.type = "button";
  block.className = "cal-appt " + statusClass(a.status);
  if (state.schedSelected === a.appointmentKey) block.classList.add("sel");
  if (isPast(a.startsAt) && ACTIVE_STATUSES.includes(a.status)) block.classList.add("past");
  block.style.top = top + "px";
  block.style.height = height + "px";
  block.style.left = `calc(${lane * widthPct}% + 2px)`;
  block.style.width = `calc(${widthPct}% - 4px)`;

  const t = document.createElement("span");
  t.className = "cal-appt-t";
  t.textContent = s.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  const who = document.createElement("span");
  who.className = "cal-appt-who";
  who.textContent = a.patientName || shortKey(a.patientKey);
  block.append(t, who);
  // Clinic-wide view: the block's title is the patient, so name the provider too
  // (which provider's slot this is) — the one thing the single-provider grid implies
  // but the all-providers grid can't.
  if (schedIsAll()) {
    const prov = document.createElement("span");
    prov.className = "cal-appt-prov";
    prov.textContent = a.providerName || shortKey(a.providerKey);
    block.append(prov);
  }
  block.title =
    `${a.patientName || shortKey(a.patientKey)} · ${fmtWhen(a.startsAt, a.endsAt)}` +
    (schedIsAll() ? " · " + (a.providerName || shortKey(a.providerKey)) : "") +
    (a.reason ? " · " + a.reason : "") + ` · ${a.status}`;
  block.addEventListener("click", () => selectSchedAppt(a));
  return block;
}

// selectSchedAppt opens the read-only detail panel for a clicked block and marks it
// selected (re-render reflects the .sel highlight; the detail panel is a separate
// node the calendar rebuild leaves intact).
function selectSchedAppt(a) {
  state.schedSelected = a.appointmentKey;
  const d = $("#sched-detail");
  d.innerHTML = "";

  const close = document.createElement("button");
  close.className = "sched-detail-x";
  close.setAttribute("aria-label", "Close details");
  close.textContent = "×";
  close.addEventListener("click", hideSchedDetail);

  const who = document.createElement("div");
  who.className = "sd-who";
  who.textContent = a.patientName || shortKey(a.patientKey);

  const when = document.createElement("div");
  when.className = "sd-when";
  when.textContent = fmtWhen(a.startsAt, a.endsAt);

  const badge = document.createElement("span");
  badge.className = "badge " + statusClass(a.status);
  badge.textContent = a.status || "—";

  d.append(close, who, when, badge);
  if (schedIsAll()) {
    const prov = document.createElement("div");
    prov.className = "sd-meta";
    prov.textContent = "with " + (a.providerName || shortKey(a.providerKey)) +
      (a.providerSpecialty ? " · " + a.providerSpecialty : "");
    d.append(prov);
  }
  if (a.reason) {
    const meta = document.createElement("div");
    meta.className = "sd-meta";
    meta.textContent = a.reason;
    d.append(meta);
  }
  if (a.statusNote) {
    const note = document.createElement("div");
    note.className = "sd-meta status-note";
    note.textContent = "📝 " + a.statusNote;
    d.append(note);
  }
  if (a.reminderSentAt) {
    const r = new Date(a.reminderSentAt);
    const rem = document.createElement("div");
    rem.className = "sd-meta reminder-sent";
    rem.textContent = "🔔 Reminder sent" + (isNaN(r) ? "" : " · " + r.toLocaleString());
    d.append(rem);
  }
  const encSummary = encounterSummary(a);
  if (encSummary) {
    const enc = document.createElement("div");
    enc.className = "sd-meta documented";
    enc.textContent = encSummary;
    d.append(enc);
  }
  // Day-of-visit transitions on the desk view (the front-desk / provider operational
  // surface the PO flagged). loadSchedule re-fetches + closes this panel (it calls
  // hideSchedDetail first), and the block re-colours to the new status.
  if (ACTIVE_STATUSES.includes(a.status)) {
    const acts = document.createElement("div");
    acts.className = "sd-actions";
    acts.append(lifecycleButtons(a, loadSchedule));
    const cancel = document.createElement("button");
    cancel.className = "ghost danger";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => setStatus(a, "cancelled", loadSchedule));
    acts.append(cancel);
    d.append(acts);
  } else if ((a.status || "").toLowerCase() === "completed") {
    const acts = document.createElement("div");
    acts.className = "sd-actions";
    const doc = document.createElement("button");
    doc.className = "ghost";
    doc.textContent = a.documentedAt ? "Edit documentation" : "Document visit";
    doc.addEventListener("click", () => openEncounter(a, loadSchedule));
    acts.append(doc);
    d.append(acts);
  }
  d.hidden = false;
  renderSchedule();
}

function hideSchedDetail() {
  state.schedSelected = null;
  const d = $("#sched-detail");
  d.hidden = true;
  d.innerHTML = "";
  document.querySelectorAll("#schedule .cal-appt.sel").forEach((el) => el.classList.remove("sel"));
}

// ---- Schedule navigation (period + view) ----

function schedNav(direction) {
  const step = state.schedView === "week" ? 7 : 1;
  state.schedAnchor = addDays(schedPeriodStart(), direction * step);
  hideSchedDetail();
  renderSchedule();
}

function schedToday() {
  state.schedAnchor = new Date();
  hideSchedDetail();
  renderSchedule();
}

function setSchedView(view) {
  if (state.schedView === view) return;
  state.schedView = view;
  for (const v of ["week", "day"]) {
    const btn = $("#sched-" + v);
    btn.classList.toggle("active", v === view);
    btn.setAttribute("aria-pressed", String(v === view));
  }
  hideSchedDetail();
  renderSchedule();
}

// ---- Appointment card (shared by both lists) ----

function fmtWhen(startsAt, endsAt) {
  const s = new Date(startsAt);
  if (isNaN(s)) return startsAt || "";
  const day = s.toLocaleDateString(undefined, { weekday: "short", year: "numeric", month: "short", day: "numeric" });
  const t1 = s.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  const e = new Date(endsAt);
  const t2 = isNaN(e) ? "" : e.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  return t2 ? `${day} · ${t1}–${t2}` : `${day} · ${t1}`;
}

function isPast(startsAt) {
  const s = new Date(startsAt);
  return !isNaN(s) && s.getTime() < Date.now();
}

const ACTIVE_STATUSES = ["scheduled", "confirmed", "checkedIn"];

function renderApptCard(a, opts) {
  const card = document.createElement("div");
  card.className = "card";
  if (state.highlight && a.appointmentKey === state.highlight) card.classList.add("highlight");
  if (isPast(a.startsAt) && ACTIVE_STATUSES.includes(a.status)) card.classList.add("past");

  const title = document.createElement("div");
  title.className = "addr";
  title.textContent = opts.showProvider
    ? a.providerName || shortKey(a.providerKey)
    : a.patientName || shortKey(a.patientKey);

  const sub = document.createElement("div");
  sub.className = "addr-sub";
  // My Appointments shows the provider's specialty under their name; the provider
  // Schedule's title is already the patient name, so no sub-label is needed.
  sub.textContent = opts.showProvider ? a.providerSpecialty || "" : "";

  const when = document.createElement("div");
  when.className = "when";
  when.textContent = fmtWhen(a.startsAt, a.endsAt);

  const reason = document.createElement("div");
  reason.className = "meta";
  reason.textContent = a.reason || "";

  // An audit note recorded with a cancel / no-show transition (clinicAppointments
  // lens statusNote column). Absent unless a note was supplied.
  const statusNote = document.createElement("div");
  statusNote.className = "meta status-note";
  statusNote.textContent = a.statusNote ? "📝 " + a.statusNote : "";

  // The ~24h reminder, once the clinic-reminders orchestration has fired it
  // (surfaced via the clinicAppointments lens's reminderSentAt column). Absent
  // until sent.
  const reminder = document.createElement("div");
  reminder.className = "meta reminder-sent";
  if (a.reminderSentAt) {
    const r = new Date(a.reminderSentAt);
    reminder.textContent = "🔔 Reminder sent" + (isNaN(r) ? "" : " · " + r.toLocaleString());
  }

  // The "visit documented" presence signal + any requested follow-up (the
  // clinicAppointments lens's operational encounter columns — the clinical content
  // itself is PHI and never projected). Absent until the visit is documented.
  const documented = document.createElement("div");
  documented.className = "meta documented";
  documented.textContent = encounterSummary(a);

  const actions = document.createElement("div");
  actions.className = "card-actions";
  const badge = document.createElement("span");
  badge.className = "badge " + statusClass(a.status);
  badge.textContent = a.status || "—";
  actions.append(badge);

  if (opts.cancelable && ACTIVE_STATUSES.includes(a.status)) {
    const btns = document.createElement("span");
    btns.className = "card-btns";

    btns.append(lifecycleButtons(a, loadAppts));

    const reschedule = document.createElement("button");
    reschedule.className = "ghost";
    reschedule.textContent = "Reschedule";
    reschedule.addEventListener("click", () => openReschedule(a));
    btns.append(reschedule);

    const cancel = document.createElement("button");
    cancel.className = "ghost danger";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => setStatus(a, "cancelled", loadAppts));
    btns.append(cancel);

    actions.append(btns);
  }

  // A completed visit can be documented (or its documentation corrected — the op is
  // a re-runnable upsert). The clinical note lives behind the modal; only the
  // "documented" + follow-up signals show on the card.
  if (opts.cancelable && (a.status || "").toLowerCase() === "completed") {
    const btns = document.createElement("span");
    btns.className = "card-btns";
    const doc = document.createElement("button");
    doc.className = "ghost";
    doc.textContent = a.documentedAt ? "Edit documentation" : "Document visit";
    doc.addEventListener("click", () => openEncounter(a, loadAppts));
    btns.append(doc);
    actions.append(btns);
  }

  card.append(title);
  if (sub.textContent) card.append(sub);
  card.append(when);
  if (reason.textContent) card.append(reason);
  if (statusNote.textContent) card.append(statusNote);
  if (reminder.textContent) card.append(reminder);
  if (documented.textContent) card.append(documented);
  card.append(actions);
  return card;
}

// encounterSummary renders the operational encounter signals (the lens's
// documentedAt / followUpRequested / followUpDate columns) for an appointment, or
// "" when the visit has not been documented. The clinical content is PHI and is
// never projected, so it is never shown here.
function encounterSummary(a) {
  if (!a.documentedAt) return "";
  const d = new Date(a.documentedAt);
  let t = "✓ Visit documented" + (isNaN(d) ? "" : " · " + d.toLocaleDateString());
  if (a.followUpRequested) {
    t += " · follow-up" + (a.followUpDate ? " " + a.followUpDate.slice(0, 10) : " requested");
  }
  return t;
}

function statusClass(status) {
  return (status || "").toLowerCase() === "noshow" ? "noshow" : (status || "").toLowerCase();
}

// ---- Appointment lifecycle (the day-of-visit transitions) ----
//
// SetAppointmentStatus is unconditioned (re-runnable), so the transitions below are
// UI affordances, not server gates: a scheduled appointment can be confirmed /
// completed / no-showed; a confirmed one completed / no-showed; completed · cancelled
// · noShow are terminal. Cancel and the no-show transition prompt for confirmation;
// forward progress (confirm / complete) proceeds directly.

const TERMINAL_STATUSES = ["completed", "cancelled", "noshow"];
const STATUS_LABEL = { confirmed: "Confirm", checkedIn: "Check in", completed: "Complete", noShow: "No-show", cancelled: "Cancel" };
const STATUS_PAST = { confirmed: "confirmed", checkedIn: "checked in", completed: "completed", noShow: "marked no-show", cancelled: "cancelled" };

// lifecycleTransitions returns the SetAppointmentStatus targets reachable from the
// current status (excluding Cancel, which renders as its own button alongside).
// The day-of-visit flow is scheduled → confirmed → checkedIn → completed; check-in
// and complete/no-show stay reachable from the earlier active states too.
function lifecycleTransitions(status) {
  const s = (status || "").toLowerCase();
  if (s === "scheduled") return ["confirmed", "checkedIn", "completed", "noShow"];
  if (s === "confirmed") return ["checkedIn", "completed", "noShow"];
  if (s === "checkedin") return ["completed", "noShow"];
  return []; // completed / cancelled / noShow are terminal
}

// setStatus drives SetAppointmentStatus to the given status and reloads via onDone.
// noShow / cancelled prompt for an optional audit note (a reason recorded on the
// .status aspect for records / billing); cancelling the prompt aborts. The FIRST
// transition into a terminal status (completed/cancelled/noShow) also needs
// provider + patient so the op can release the appointment's held slot-claim cells
// (a same-value re-set, e.g. correcting a note, has already released them).
const TERMINAL_STATUS_VALUES = ["completed", "cancelled", "noShow"];

async function setStatus(a, status, onDone) {
  const payload = { appointmentKey: a.appointmentKey, status };
  // read-posture: appt.status is (d) — absence is the legit first-set case
  // (optionalReads); the terminal-transition branch additionally reads (a)
  // appt.schedule + the withProvider/forPatient endpoint-validation links,
  // required only when this call can hit that branch (script-read-posture-
  // design.md §13).
  const reads = [a.appointmentKey];
  const optionalReads = [a.appointmentKey + ".status"];
  if (TERMINAL_STATUS_VALUES.indexOf(status) !== -1 && status !== a.status) {
    payload.provider = a.providerKey;
    payload.patient = a.patientKey;
    reads.push(
      a.appointmentKey + ".schedule",
      "lnk.appointment." + bareId(a.appointmentKey) + ".withProvider.provider." + bareId(a.providerKey),
      "lnk.appointment." + bareId(a.appointmentKey) + ".forPatient.patient." + bareId(a.patientKey),
    );
  }
  if (status === "noShow" || status === "cancelled") {
    const verb = status === "noShow" ? "Mark as no-show" : "Cancel this appointment";
    const note = prompt(verb + ". Optional note (reason):", "");
    if (note === null) return; // prompt dismissed → abort
    const trimmed = note.trim();
    if (trimmed) payload.note = trimmed;
  }
  try {
    const reply = await submitOp(
      "SetAppointmentStatus",
      "appointment",
      payload,
      reads,
      { optionalReads },
    );
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not update status — " + msg, "err");
      return;
    }
    toast("Appointment " + (STATUS_PAST[status] || status) + ".", "ok");
    if (onDone) onDone();
  } catch (e) {
    toast("Could not update status: " + e.message, "err");
  }
}

// lifecycleButtons builds the status-transition buttons for an appointment, wired to
// reload via onDone. Returns a fragment (empty for a terminal appointment).
function lifecycleButtons(a, onDone) {
  const frag = document.createDocumentFragment();
  for (const st of lifecycleTransitions(a.status)) {
    const b = document.createElement("button");
    b.className = st === "noShow" ? "ghost danger" : "ghost";
    b.textContent = STATUS_LABEL[st];
    b.addEventListener("click", () => setStatus(a, st, onDone));
    frag.append(b);
  }
  return frag;
}

// ---- Reschedule (move an appointment to a new time) ----
//
// RescheduleAppointment rewrites the .schedule aspect with new times; the op
// re-derives remindAt = startsAt − 24h so the ~24h reminder re-arms for a
// not-yet-sent reminder, and rejects a move into a slot already booked for the
// provider (SlotConflict) or the patient (PatientDoubleBook) by releasing the
// vacated 15-minute grid cells and claiming the newly-covered ones in the same
// atomic batch — a collision leaves the original booking's claims intact. The
// existing reason is round-tripped (the op clears it if omitted), and the
// provider / patient links + status are untouched server-side.

function openReschedule(a) {
  state.rescheduling = a;
  const who = a.providerName || shortKey(a.providerKey);
  $("#reschedule-context").textContent = `${who} · currently ${fmtWhen(a.startsAt, a.endsAt)}`;
  $("#rs-startsAt").value = toLocalInputValue(a.startsAt);
  $("#rs-startsAt").min = nowLocalInputValue();
  const dur = durationMinutes(a.startsAt, a.endsAt);
  const sel = $("#rs-duration");
  sel.value = String(dur);
  if (!sel.value) sel.value = "30"; // a non-standard length falls back to 30 min
  $("#reschedule-overlay").hidden = false;
  $("#rs-startsAt").focus();
}

function closeReschedule() {
  $("#reschedule-overlay").hidden = true;
  state.rescheduling = null;
}

async function submitReschedule(ev) {
  ev.preventDefault();
  const a = state.rescheduling;
  if (!a) {
    closeReschedule();
    return;
  }
  const when = $("#rs-startsAt").value;
  if (!when) {
    toast("Pick a new date and time.", "err");
    return;
  }
  const startsAt = toRFC3339(when);
  const endsAt = addMinutesRFC3339(when, Number($("#rs-duration").value || 30));
  if (!startsAt || !endsAt) {
    toast("That date/time is not valid.", "err");
    return;
  }

  const payload = { appointmentKey: a.appointmentKey, provider: a.providerKey, patient: a.patientKey, startsAt, endsAt };
  if (a.reason) payload.reason = a.reason; // round-trip the existing reason (omitted → cleared)

  const submit = $("#reschedule-submit");
  submit.disabled = true;
  try {
    // read-posture (a): the appointment's current .schedule (required to compute
    // released/claimed cells) + the withProvider/forPatient endpoint-validation
    // links (require_matching_provider/patient, ddls.go) — script-read-posture-
    // design.md §13. The new interval's slot-claim keys are (d) optionalReads
    // (claim_cell; an over-declare of cells already held across the move is
    // harmless — the script only reads what claim_cell actually calls kv.Read on).
    const optionalReads = slotClaimKeys(a.providerKey, startsAt, endsAt).concat(
      slotClaimKeys(a.patientKey, startsAt, endsAt),
    );
    const reply = await submitOp(
      "RescheduleAppointment",
      "appointment",
      payload,
      [
        a.appointmentKey,
        a.appointmentKey + ".schedule",
        "lnk.appointment." + bareId(a.appointmentKey) + ".withProvider.provider." + bareId(a.providerKey),
        "lnk.appointment." + bareId(a.appointmentKey) + ".forPatient.patient." + bareId(a.patientKey),
      ],
      { optionalReads },
    );
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not reschedule — " + friendlyBookingRejection(msg), "err");
      return;
    }
    state.highlight = a.appointmentKey;
    // The moved appointment invalidates both providers' and the patient's cached
    // slot sets so the picker reflects the new time.
    delete state.slotApptCache[a.providerKey];
    delete state.slotPatientApptCache[a.patientKey];
    closeReschedule();
    toast("Appointment rescheduled.", "ok");
    loadAppts();
  } catch (e) {
    toast("Could not reschedule: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Document visit (RecordEncounter — the post-visit clinical record) ----
//
// RecordEncounter upserts the appointment's .encounter aspect. The RAW clinical
// content (summary / assessment / plan) is PHI: it is captured but NEVER projected
// (the deferred Vault plane owns its display), so the form cannot pre-fill it even
// when correcting an existing note — only the operational follow-up signals are
// projected and round-tripped. The op is a re-runnable upsert (re-saving replaces
// the whole aspect).

function openEncounter(a, onDone) {
  state.documenting = { a, onDone };
  const who = a.patientName || shortKey(a.patientKey);
  $("#encounter-context").textContent = a.documentedAt
    ? `${who} · ${fmtWhen(a.startsAt, a.endsAt)} — re-documenting replaces the prior note`
    : `${who} · ${fmtWhen(a.startsAt, a.endsAt)}`;
  // Clinical content is never projected, so even an already-documented visit starts
  // blank (an honest consequence of the PHI-not-projected discipline). The follow-up
  // signals ARE projected, so they pre-fill.
  $("#enc-summary").value = "";
  $("#enc-assessment").value = "";
  $("#enc-plan").value = "";
  $("#enc-followup").checked = !!a.followUpRequested;
  $("#enc-followup-date").value = a.followUpDate ? a.followUpDate.slice(0, 10) : "";
  toggleFollowupDate();
  $("#encounter-overlay").hidden = false;
  $("#enc-summary").focus();
}

function closeEncounter() {
  $("#encounter-overlay").hidden = true;
  state.documenting = null;
}

function toggleFollowupDate() {
  $("#enc-followup-date-field").hidden = !$("#enc-followup").checked;
}

async function submitEncounter(ev) {
  ev.preventDefault();
  const ctx = state.documenting;
  if (!ctx) {
    closeEncounter();
    return;
  }
  const a = ctx.a;
  const summary = $("#enc-summary").value.trim();
  if (!summary) {
    toast("A visit summary is required.", "err");
    return;
  }
  const payload = { appointmentKey: a.appointmentKey, summary };
  const assessment = $("#enc-assessment").value.trim();
  if (assessment) payload.assessment = assessment;
  const plan = $("#enc-plan").value.trim();
  if (plan) payload.plan = plan;
  const followUp = $("#enc-followup").checked;
  payload.followUpRequested = followUp;
  if (followUp) {
    const fd = $("#enc-followup-date").value;
    if (fd) payload.followUpDate = fd;
  }

  const submit = $("#encounter-submit");
  submit.disabled = true;
  try {
    const reply = await submitOp("RecordEncounter", "appointment", payload, [a.appointmentKey]);
    const msg = rejectionMessage(reply);
    if (msg) {
      toast("Could not save documentation — " + msg, "err");
      return;
    }
    state.highlight = a.appointmentKey;
    closeEncounter();
    toast("Visit documented.", "ok");
    if (ctx.onDone) ctx.onDone();
  } catch (e) {
    toast("Could not save documentation: " + e.message, "err");
  } finally {
    submit.disabled = false;
  }
}

// ---- Tabs ----

const VIEWS = ["book", "appts", "schedule", "followups", "series", "availability"];

function showView(view) {
  state.view = view;
  for (const v of VIEWS) {
    const isV = v === view;
    $("#view-" + v).hidden = !isV;
    const tab = $("#tab-" + v);
    tab.classList.toggle("active", isV);
    tab.setAttribute("aria-selected", String(isV));
  }
  // "appts" (My Appointments) also hosts the selected patient's own recurring
  // series panel (state.mySeries, the PROTECTED patient-self RLS fetch) — a
  // sibling of the clinic-wide "series" tab's staff fetch, not the same data.
  if (view === "appts") loadAppts();
  if (view === "schedule") loadSchedule();
  if (view === "followups") loadFollowups();
  if (view === "appts" || view === "series") loadSeries();
  if (view === "appts") loadLedger();
  if (view === "availability") renderAvailEditors();
}

// ---- wire up ----

function init() {
  restorePatient();
  loadPatients();
  loadProviders();

  // Discourage a past booking from the picker itself; refresh on focus so a
  // long-open session never carries a stale floor. The op stays the authority.
  $("#startsAt").min = nowLocalInputValue();
  $("#startsAt").addEventListener("focus", () => {
    $("#startsAt").min = nowLocalInputValue();
  });
  $("#startsAt").addEventListener("change", () => {
    refreshTimeOffWarning();
    refreshSlots(); // keep the slot highlight in sync with a typed time
  });
  // #slot-date is a hidden value driven by the custom booking calendar
  // (renderSlotCalendar), which greys out unbookable days; the per-slot past check
  // remains the floor and the op stays the authority.

  $("#patient").addEventListener("change", (e) => setPatient(e.target.value));
  wirePatientSearch();
  wireProviderSearch();
  $("#new-patient").addEventListener("click", openNewPatient);
  $("#patient-cancel").addEventListener("click", closeNewPatient);
  $("#patient-overlay").addEventListener("click", (e) => {
    if (e.target === $("#patient-overlay")) closeNewPatient();
  });
  $("#patient-form").addEventListener("submit", submitNewPatient);

  $("#reschedule-cancel").addEventListener("click", closeReschedule);
  $("#reschedule-overlay").addEventListener("click", (e) => {
    if (e.target === $("#reschedule-overlay")) closeReschedule();
  });
  $("#reschedule-form").addEventListener("submit", submitReschedule);

  $("#encounter-cancel").addEventListener("click", closeEncounter);
  $("#encounter-overlay").addEventListener("click", (e) => {
    if (e.target === $("#encounter-overlay")) closeEncounter();
  });
  $("#encounter-form").addEventListener("submit", submitEncounter);
  $("#enc-followup").addEventListener("change", toggleFollowupDate);

  $("#provider").addEventListener("change", () => {
    refreshBookEnabled();
    refreshTimeOffWarning();
    // A new provider has different availability — clear the chosen date and rebuild
    // the calendar so the user re-picks against the new provider's open days.
    $("#slot-date").value = "";
    renderSlotCalendar();
    refreshSlots();
    renderSoonest();
  });
  $("#book-specialty").addEventListener("change", () => {
    populateProviderSelect("#provider", { specialty: $("#book-specialty").value });
    refreshBookEnabled();
    $("#slot-date").value = "";
    renderSlotCalendar();
    refreshSlots();
    renderSoonest();
  });
  $("#duration").addEventListener("change", () => {
    refreshSlots();
    renderSoonest();
  });
  $("#add-provider-submit").addEventListener("click", submitAddProvider);
  // Availability tab — its own provider picker drives both editors; a change
  // re-seeds each draft from the newly-selected provider's projected values.
  $("#avail-provider").addEventListener("change", renderAvailEditors);
  $("#edit-prov-save").addEventListener("click", saveProviderEdit);
  $("#hours-add").addEventListener("click", addHoursWindow);
  $("#hours-save").addEventListener("click", saveProviderHours);
  $("#timeoff-add").addEventListener("click", addTimeOffRange);
  $("#timeoff-save").addEventListener("click", saveProviderTimeOff);
  $("#book-form").addEventListener("submit", submitBook);

  $("#tab-book").addEventListener("click", () => showView("book"));
  $("#tab-appts").addEventListener("click", () => showView("appts"));
  $("#tab-schedule").addEventListener("click", () => showView("schedule"));
  $("#tab-followups").addEventListener("click", () => showView("followups"));
  $("#tab-series").addEventListener("click", () => showView("series"));
  $("#tab-availability").addEventListener("click", () => showView("availability"));
  // The Book form's pointer link jumps to the Availability tab, carrying the
  // provider the user was about to book so the editor opens on that provider.
  $("#go-availability").addEventListener("click", (e) => {
    e.preventDefault();
    const prov = $("#provider").value;
    if (prov) $("#avail-provider").value = prov;
    showView("availability");
  });
  $("#reload-appts").addEventListener("click", loadAppts);
  $("#appts-filter").addEventListener("change", renderAppts);
  $("#ledger-charge").addEventListener("click", () => submitLedgerEntry("DebitAccount", "record the charge"));
  $("#ledger-payment").addEventListener("click", () => submitLedgerEntry("CreditAccount", "record the payment"));
  $("#reload-followups").addEventListener("click", loadFollowups);
  $("#followups-filter").addEventListener("change", renderFollowups);
  $("#reload-series").addEventListener("click", loadSeries);
  $("#series-filter").addEventListener("change", renderSeries);
  $("#series-start-submit").addEventListener("click", submitStartSeries);
  $("#series-start").min = localDateStr(0);
  $("#reload-schedule").addEventListener("click", loadSchedule);
  $("#sched-provider").addEventListener("change", loadSchedule);
  $("#sched-week").addEventListener("click", () => setSchedView("week"));
  $("#sched-day").addEventListener("click", () => setSchedView("day"));
  $("#sched-prev").addEventListener("click", () => schedNav(-1));
  $("#sched-next").addEventListener("click", () => schedNav(1));
  $("#sched-today").addEventListener("click", schedToday);
}

document.addEventListener("DOMContentLoaded", init);
