/* "One graph, five lenses" — simulated walkthrough of package composition.
   Canned data; the key grammar, lens names and flows mirror the real stack. */
(function () {
  const root = document.getElementById("demo");
  if (!root) return;

  // ---------------------------------------------------------------- data
  const PKGS = {
    leasing:  { label: "Leasing" },
    clinic:   { label: "Clinic" },
    cafe:     { label: "Café" },
    wellness: { label: "Wellness" },
  };

  // pkg:"base" nodes are always present; anyOf nodes appear once any listed
  // package is installed. x/y in a 660x600 viewBox.
  const NODES = [
    { id: "bldg",    label: "The Foundry",    key: "vtx.location.FNDRY01",   pkg: "base",     x: 330, y: 66,  person: false },
    { id: "u204",    label: "Unit 204",       key: "vtx.unit.U204",          pkg: "base",     x: 205, y: 158, person: false },
    { id: "u207",    label: "Unit 207",       key: "vtx.unit.U207",          pkg: "base",     x: 452, y: 148, person: false },
    { id: "kestrel", label: "Kestrel Mgmt",   key: "vtx.identity.KESTREL9",  pkg: "base",     x: 583, y: 70,  person: true  },
    { id: "maya",    label: "Maya",           key: "vtx.identity.MAYA4kP",   pkg: "base",     x: 124, y: 438, person: true  },
    // The human behind the provider records. Her login is minted with the first
    // binding, so she enters the graph with the package that binds her.
    { id: "okafor",  label: "Dr. Okafor",     key: "vtx.identity.OKAF2rMx",  pkg: "base",     x: 590, y: 508, person: true,
      anyOf: ["clinic", "wellness"] },

    { id: "app19",   label: "Application",    key: "vtx.leaseapp.APP19",     pkg: "leasing",  x: 318, y: 252, person: false },
    { id: "lease",   label: "Lease",          key: "vtx.lease.L204",         pkg: "leasing",  x: 212, y: 318, person: false },
    { id: "renewal", label: "Renewal R-88",   key: "vtx.renewal.R88",        pkg: "leasing",  x: 322, y: 388, person: false },

    { id: "clinic",  label: "Clinic",         key: "vtx.location.CLIN1",     pkg: "clinic",   x: 540, y: 232, person: false },
    { id: "drok",    label: "Provider record", key: "vtx.provider.OKAFOR2",  pkg: "clinic",   x: 600, y: 330, person: false },
    { id: "pat88",   label: "Patient record", key: "vtx.patient.P88",        pkg: "clinic",   x: 452, y: 372, person: false },
    { id: "appt",    label: "Appointment",    key: "vtx.appointment.A7761",  pkg: "clinic",   x: 545, y: 445, person: false },

    { id: "cafe",    label: "Café",           key: "vtx.location.CAFE1",     pkg: "cafe",     x: 330, y: 505, person: false },
    { id: "tab",     label: "House tab",      key: "vtx.account.ACC88",      pkg: "cafe",     x: 218, y: 545, person: false },

    { id: "studio",  label: "Studio",         key: "vtx.location.WELL1",     pkg: "wellness", x: 86,  y: 258, person: false },
    { id: "booking", label: "Class booking",  key: "vtx.booking.BK7",        pkg: "wellness", x: 122, y: 338, person: false },
    { id: "instr",   label: "Instructor record", key: "vtx.instructor.YOGA6", pkg: "wellness", x: 76, y: 516, person: false },
  ];

  // Link naming reads "source relation target"; later-arriving vertex is the source.
  // bow bends the link into an arc, perpendicular to its own direction, to keep
  // long spans clear of the clusters they pass.
  const LINKS = [
    { from: "u204",    to: "bldg",    rel: "partOf",        pkg: "base" },
    { from: "u207",    to: "bldg",    rel: "partOf",        pkg: "base" },
    { from: "kestrel", to: "u204",    rel: "manages",       pkg: "base" },
    { from: "kestrel", to: "u207",    rel: "manages",       pkg: "base" },

    { from: "app19",   to: "maya",    rel: "applicationFor", pkg: "leasing" },
    { from: "app19",   to: "u204",    rel: "appliesToUnit",  pkg: "leasing" },
    { from: "lease",   to: "u204",    rel: "forUnit",        pkg: "leasing" },
    { from: "lease",   to: "maya",    rel: "heldBy",         pkg: "leasing" },
    { from: "renewal", to: "lease",   rel: "forLease",       pkg: "leasing" },

    { from: "clinic",  to: "bldg",    rel: "partOf",         pkg: "clinic" },
    { from: "pat88",   to: "maya",    rel: "identifiedBy",   pkg: "clinic" },
    { from: "appt",    to: "drok",    rel: "withProvider",   pkg: "clinic" },
    { from: "appt",    to: "pat88",   rel: "forPatient",     pkg: "clinic" },
    { from: "drok",    to: "clinic",  rel: "practicesAt",    pkg: "clinic" },

    { from: "cafe",    to: "bldg",    rel: "partOf",         pkg: "cafe" },
    { from: "tab",     to: "maya",    rel: "heldBy",         pkg: "cafe" },

    { from: "studio",  to: "bldg",    rel: "partOf",         pkg: "wellness" },
    { from: "booking", to: "studio",  rel: "bookedAt",       pkg: "wellness" },
    { from: "booking", to: "maya",    rel: "bookedBy",       pkg: "wellness" },
    { from: "instr",   to: "studio",  rel: "teachesAt",      pkg: "wellness", bow: -60 },

    // binding links: what makes someone a provider here is one of these, not a
    // user-type column. Each names the login a professional record answers to.
    { from: "drok",    to: "okafor",  rel: "identifiedBy",   pkg: "clinic",   binding: true },
    { from: "instr",   to: "okafor",  rel: "identifiedBy",   pkg: "wellness", binding: true, bow: -80 },

    // cross-package links: only exist when BOTH packages are installed
    { from: "tab",     to: "lease",   rel: "billedWith",     pkg: "cafe",     needs: "leasing", emergent: true },
    { from: "booking", to: "lease",   rel: "residentRate",   pkg: "wellness", needs: "leasing", emergent: true },
  ];

  // The ladder the platform actually models: service flows to you, you are the
  // business, service flows through you, the business answers to you.
  const PERSONAS = {
    resident: { label: "Maya — resident", color: "var(--c-consumer)" },
    front:    { label: "Front desk",      color: "var(--c-front)" },
    back:     { label: "Operations",      color: "var(--c-back)" },
    provider: { label: "Provider",        color: "var(--c-provider)" },
    operator: { label: "Operator",        color: "var(--c-operator)" },
  };

  // visibility per persona: hi = their lens, mid = context, dim = outside the projection
  const VIS = {
    resident: { hi: ["maya", "app19", "lease", "renewal", "pat88", "appt", "tab", "booking"], mid: ["u204", "bldg", "clinic", "cafe", "studio", "drok", "okafor", "instr"], },
    front:    { hi: ["app19", "appt", "drok", "tab", "booking", "maya", "okafor"], mid: ["clinic", "cafe", "studio", "bldg", "u204", "pat88", "lease", "instr"], },
    back:     { hi: ["kestrel", "u204", "u207", "renewal", "bldg", "clinic", "cafe", "studio"], mid: ["lease", "app19", "drok", "appt", "booking", "tab", "okafor", "instr"], },
    provider: { hi: ["okafor", "drok", "appt", "pat88", "clinic", "instr", "studio", "booking"], mid: ["bldg", "maya"], },
    operator: { hi: NODES.map(n => n.id), mid: [] },
  };

  const CAPTIONS = {
    resident: "<b>Maya's lens</b> — her own subgraph, nothing else. One identity vertex across every service line.",
    front:    "<b>Front-of-house lens</b> — today's work, with full resident context surfaced before she asks.",
    back:     "<b>Back-of-house lens</b> — occupancy, renewals, utilization. Aggregates, not private records.",
    provider: "<b>Provider lens</b> — neither staff nor customer. Her world is exactly as wide as her bindings.",
    operator: "<b>Operator lens</b> — the raw graph, real key grammar. On the live stack this is Loupe, the console.",
  };

  const state = { pkgs: { leasing: true, clinic: false, cafe: false, wellness: false }, persona: "resident" };

  // ---------------------------------------------------------------- helpers
  const el = (sel) => root.querySelector(sel);
  const nodeById = Object.fromEntries(NODES.map(n => [n.id, n]));
  const on = (p) => state.pkgs[p];
  const activePkgCount = () => Object.values(state.pkgs).filter(Boolean).length;

  function linkKey(l) {
    const a = nodeById[l.from].key.split(".");
    const b = nodeById[l.to].key.split(".");
    return `lnk.${a[1]}.${a[2]}.${l.rel}.${b[1]}.${b[2]}`;
  }

  function visibleNodes() {
    return NODES.filter(n => (n.anyOf ? n.anyOf.some(on) : n.pkg === "base" || on(n.pkg)));
  }
  function visibleLinks() {
    return LINKS.filter(l => (l.pkg === "base" || on(l.pkg)) && (!l.needs || on(l.needs)));
  }

  // A bowed link is a quadratic arc; its control point is offset perpendicular
  // to the straight run, and the curve's own midpoint is where its label goes.
  function linkGeom(l, a, b) {
    if (!l.bow) return { d: `M${a.x} ${a.y} L${b.x} ${b.y}`, mx: (a.x + b.x) / 2, my: (a.y + b.y) / 2 };
    const dx = b.x - a.x, dy = b.y - a.y, len = Math.hypot(dx, dy) || 1;
    const cx = (a.x + b.x) / 2 + (-dy / len) * l.bow;
    const cy = (a.y + b.y) / 2 + (dx / len) * l.bow;
    return { d: `M${a.x} ${a.y} Q${cx} ${cy} ${b.x} ${b.y}`, mx: (a.x + 2 * cx + b.x) / 4, my: (a.y + 2 * cy + b.y) / 4 };
  }

  // ---------------------------------------------------------------- graph
  function renderGraph() {
    const vis = VIS[state.persona];
    const pcol = PERSONAS[state.persona].color;
    const showKeys = state.persona === "operator";
    const sharedIdentity = on("leasing") && on("clinic");

    const links = visibleLinks().map(l => {
      const a = nodeById[l.from], b = nodeById[l.to];
      const emph = vis.hi.includes(l.from) && vis.hi.includes(l.to) ? "" : " dim";
      const named = l.emergent || l.binding;
      const cls = l.emergent ? "g-link emergent" : l.binding ? "g-link binding" : "g-link" + emph;
      const g = linkGeom(l, a, b);
      const relLabel = named
        ? `<text x="${g.mx}" y="${g.my - 5}" text-anchor="middle" class="gkey" fill="var(--faint)" font-size="8.5" font-family="var(--mono)">${l.rel}</text>`
        : "";
      return `<path class="${cls}" d="${g.d}"><title>${linkKey(l)}</title></path>${relLabel}`;
    }).join("");

    const nodes = visibleNodes().map(n => {
      let emph = "dim";
      if (vis.hi.includes(n.id)) emph = "hi";
      else if (vis.mid.includes(n.id)) emph = "";
      const pulse = sharedIdentity && n.id === "maya" ? " pulse" : "";
      const r = n.person ? 11 : 8;
      const keyLabel = showKeys ? `<text class="gkey" x="${n.x}" y="${n.y + r + 22}" text-anchor="middle">${n.key}</text>` : "";
      return `<g class="g-node ${n.person ? "person " : ""}${emph}${pulse}" style="--pcol:${pcol}">
        <circle cx="${n.x}" cy="${n.y}" r="${r}"><title>${n.key}</title></circle>
        <text x="${n.x}" y="${n.y + r + 12}" text-anchor="middle">${n.label}</text>
        ${keyLabel}
      </g>`;
    }).join("");

    el(".graph-svg").innerHTML = links + nodes;
    el(".graph-cap").innerHTML = CAPTIONS[state.persona];
  }

  // ---------------------------------------------------------------- panel
  function card(t, d, m, hint) {
    return `<div class="pcard">
      <div class="t">${t}</div>
      ${d ? `<div class="d">${d}</div>` : ""}
      ${m ? `<div class="m">${m}</div>` : ""}
      ${hint ? `<div class="hint">${hint}</div>` : ""}
    </div>`;
  }

  function renderPanel() {
    const p = state.persona;
    let title = "", sub = "", cards = [];

    if (p === "resident") {
      title = "Maya's portal"; sub = "What the resident sees — reads served by lens projections, never the core store.";
      cards.push(card("Maya", "One profile across every service in the building.", "vtx.identity.MAYA4kP"));
      if (on("leasing")) cards.push(card(
        `My home — Unit 204 <span class="pill">lease</span>`,
        "$2,350/mo · lease ends Sep 30.",
        "lens: leaseApplicationsRead · renewalsRead (Postgres, row-level security)",
        `<span class="pill glow">Renewal R-88</span> &nbsp;Terms proposed — review &amp; sign. <span style="color:var(--faint)">(the lease-renewal flow that runs on the real stack)</span>`
      ));
      if (on("clinic")) cards.push(card(
        "Next appointment",
        "Thu 10:15 · Dr. Okafor · ground-floor clinic. Booked from the slot grid — double-booking is impossible by construction.",
        "lens: clinicAppointmentsRead (self-anchored)"
      ));
      if (on("cafe")) cards.push(card(
        `House tab`,
        "$23.40 open." + (on("leasing") ? " Settles on your monthly statement — the ledger serves both packages." : ""),
        "vtx.account.ACC88"
      ));
      if (on("wellness")) cards.push(card(
        `Mobility class`,
        "Sat 9:00 · booked." + (on("leasing") ? " Resident rate applied via your lease." : ""),
        "vtx.booking.BK7"
      ));
      if (["leasing", "cafe", "wellness"].filter(on).length >= 2) cards.push(card(
        "One statement",
        "Rent, tab, classes — one bill, because every line item hangs off the same identity vertex.",
        ""
      ));
    }

    if (p === "front") {
      title = "Front desk"; sub = "Full resident context, surfaced before anyone asks.";
      cards.push(card(
        "Maya is at the desk",
        [
          on("leasing") && "Resident of Unit 204 (renewal in progress)",
          on("clinic") && "Clinic visit Thursday 10:15",
          on("cafe") && "Open tab: $23.40",
          on("wellness") && "Booked: Sat mobility class",
        ].filter(Boolean).join(" · ") || "No services installed yet — toggle packages above.",
        "one lookup, one graph — no swivel-chair between systems"
      ));
      if (on("leasing")) cards.push(card("Applications to review (1)", "APP-19 · Maya → Unit 204 · background check clear.", "lens: landlordLeaseApplicationsRead"));
      if (on("clinic")) cards.push(card("Today's schedule", "6 appointments · Dr. Okafor, who keeps her own hours · zero double-books (write-path slot claims).", "lens: clinicAppointments"));
      if (on("cafe")) cards.push(card(`Open tabs (3)`, "Table 2 · Maya · $23.40 — charge to residence?", ""));
      if (on("wellness")) cards.push(card(`Sat 9:00 roster`, "8 of 12 booked · 5 residents, 3 guests.", ""));
    }

    if (p === "back") {
      title = "Operations"; sub = "The building as a business — aggregates and queues, not private records.";
      if (on("leasing")) {
        cards.push(card("Renewals due (1)", "R-88 · Unit 204 · propose terms → guarantor → signature. The Weaver drives this toward 'renewed' as a goal, not a script.", "target lens: renewalsRead · goal-authored"));
        cards.push(card("Vacancy", "Unit 207 listed at $2,600 · 14 days on market.", "lens: availableListings"));
      }
      if (on("clinic")) cards.push(card("Provider utilization", "78% this week · Thursday is the bottleneck.", "lens: providerAppointmentsRead"));
      if (on("cafe")) cards.push(card(`Café`, "61% margin · restock Tuesday · residents = 70% of covers.", ""));
      if (on("wellness")) cards.push(card(`Studio`, "Sat 9:00 near capacity — consider a second class.", ""));
      if (activePkgCount() >= 3) cards.push(card("Portfolio pulse", "Occupancy 96% · service attach rate 2.4 packages per resident · churn risk: low.", "a view that only exists because the packages share one graph"));
      if (!cards.length) cards.push(card("Nothing installed", "Toggle packages above to give operations something to run.", ""));
    }

    if (p === "provider") {
      title = "Dr. Okafor's day"; sub = "The professional the business runs service through — inside the graph, on her own terms.";
      const hats = [on("clinic") && "clinician", on("wellness") && "instructor"].filter(Boolean);
      if (!hats.length) {
        cards.push(card(
          "Nothing binds her yet",
          "A provider world exists only where the graph binds a person to a professional record. Install Clinic or Wellness and she appears — no user-type column changes, because there isn't one.",
          "archetype = a role + a binding link, never runtime state"
        ));
      } else {
        cards.push(card(
          `Dr. Okafor <span class="pill">${hats.join(" + ")}</span>`,
          `One login. Her world is exactly what the graph binds her to — ${hats.length > 1 ? "two records, two hats" : "one record, one hat"}, no second account.`,
          "vtx.identity.OKAF2rMx · role provider"
        ));
      }
      if (on("clinic")) {
        cards.push(card(
          "My schedule — Thursday",
          "6 appointments · 10:15 Maya, annual. Hers alone: Dr. Patel practices in the same clinic and never appears here.",
          "lens: providerAppointmentsRead — same rows, anchored on her provider vertex"
        ));
        cards.push(card(
          `Close out the 10:15 <span class="pill glow">Record encounter</span>`,
          "Notes, orders, follow-up. The one clinical verb a clinician owns — and the op checks she is the provider on that appointment before it commits.",
          "op RecordEncounter · granted to role provider, guarded by withProvider + identifiedBy"
        ));
        cards.push(card(
          "My availability",
          "Tue–Thu 09:00–16:00 · time off Aug 12. She edits her own hours. Adding a provider to the clinic is still front-desk work — that op never grants to her.",
          "op SetProviderHours · scoped to her own entry"
        ));
      }
      if (on("wellness")) cards.push(card(
        "Saturday roster",
        "Mobility 9:00 · 8 of 12 booked · she leads it. Marking attendance is hers." + (on("clinic") ? " Same lens shape as the clinic list, a different binding underneath." : ""),
        "lens: classRosterRead — anchored on her instructor vertex"
      ));
      if (hats.length) cards.push(card(
        "What the lens never returns",
        "The rent roll, lease applications, another provider's list, the café ledger. Not hidden in her app — never projected to her at all.",
        "access is a lens, not a check the app remembers to run"
      ));
    }

    if (p === "operator") {
      title = "Operator console"; sub = "On the real stack this is Loupe — inspector, lens explorer, Time Machine.";
      const lenses = [
        on("leasing") && "availableListings · leaseApplicationsRead · renewalsRead",
        on("clinic") && "clinicAppointments · clinicPatientsRead · providerAppointmentsRead",
        on("cafe") && "cafeLedgerHistory · leaseAccounts",
        on("wellness") && "classSchedule · classRosterRead",
      ].filter(Boolean).join("<br>");
      cards.push(card("Lens projections", lenses || "No packages installed.", "reads = lenses (P5) · writes = operations through the one Processor (P2)"));
      cards.push(card("Visible vertices", "", visibleNodes().map(n => n.key).join("<br>")));
      cards.push(card("Every mutation is attributed", "Who submitted it, which capability allowed it, what it changed — replayable end to end.", "Loupe Time Machine scrubs this history on the live stack"));
    }

    el(".panel-title").textContent = title;
    el(".panel-sub").textContent = sub;
    el(".panel-cards").innerHTML = cards.join("");
  }

  // ---------------------------------------------------------------- emergence
  function renderEmergence() {
    const items = [];
    if (on("leasing") && on("clinic")) items.push(`<li><b>Shared identity:</b> Maya's patient record points at the same vertex as her lease — one profile, no sync job. <span class="m">lnk.patient.P88.identifiedBy.identity.MAYA4kP</span></li>`);
    if (on("leasing") && on("cafe")) items.push(`<li><b>One bill:</b> café charges settle on the monthly statement — the ledger package serves both. <span class="m">lnk.account.ACC88.billedWith.lease.L204</span></li>`);
    if (on("leasing") && on("wellness")) items.push(`<li><b>Resident perk:</b> member rate applies automatically because the booking can see the lease. <span class="m">lnk.booking.BK7.residentRate.lease.L204</span></li>`);
    if (on("clinic") && on("wellness")) items.push(`<li><b>Care to wellness:</b> post-visit, the provider suggests the mobility class — the one she leads, bookable because both share the scheduling shape.</li>`);
    if (on("clinic") && on("wellness")) items.push(`<li><b>Two hats, one login:</b> the clinic's provider and the studio's instructor are the same person — two bindings on one identity vertex, not two accounts and not a role column. <span class="m">lnk.instructor.YOGA6.identifiedBy.identity.OKAF2rMx</span></li>`);
    if (activePkgCount() === 4) items.push(`<li class="capstone"><b>Mixed-use mode:</b> residences, care, food &amp; beverage, wellness — one property, one graph. No integrations were written between these packages; they compose because they share the substrate.</li>`);

    const box = el(".emergence");
    if (!items.length) {
      box.innerHTML = `<div class="et">Composition</div><ul><li>Install a second package to see what composition buys — the packages don't know about each other; the graph connects them.</li></ul>`;
    } else {
      box.innerHTML = `<div class="et">More than the sum — what composition unlocked</div><ul>${items.join("")}</ul>`;
    }
  }

  // ---------------------------------------------------------------- ticker
  const tickerLines = [];
  function pushTick(lines) {
    for (const l of lines) tickerLines.push(l);
    while (tickerLines.length > 4) tickerLines.shift();
    el(".tk-lines").innerHTML = tickerLines
      .map((l, i) => `<div class="line${i === tickerLines.length - 1 ? " new" : ""}">${l}</div>`)
      .join("");
  }

  const TICKS = {
    leasing: [
      `<span class="op">op SetListing</span> → Processor → PUT vtx.unit.U204.listing · seq 4118`,
      `CDC → Refractor → <span class="lens">lens availableListings</span> ⇒ row upsert`,
      `<span class="op">op OpenRenewal</span> → PUT vtx.renewal.R88 · <span class="lens">renewalsRead</span> ⇒ tenant + landlord rows`,
    ],
    clinic: [
      `<span class="op">op CreateAppointment</span> → slot-claim aspects (provider + patient) · collision = rejected`,
      `CDC → Refractor → <span class="lens">lens clinicAppointments</span> ⇒ row upsert`,
      `<span class="op">op BindProviderIdentity</span> → role provider granted · <span class="lens">providerAppointmentsRead</span> ⇒ her rows, hers only`,
    ],
    cafe: [
      `<span class="op">op DebitAccount</span> → PUT vtx.transaction.T90210 (append-only)`,
      `CDC → <span class="lens">lens ledgerHistory</span> ⇒ row upsert`,
    ],
    wellness: [
      `<span class="op">op CreateBooking</span> → PUT vtx.booking.BK7`,
      `CDC → <span class="lens">lens classSchedule</span> ⇒ row upsert`,
      `<span class="op">op BindInstructorIdentity</span> → second hat on the same identity · no new login`,
    ],
  };
  const UNTICKS = {
    leasing: [`<span class="op">op Tombstone…</span> lease records retired · lenses retract rows`],
    clinic: [`clinic package uninstalled · lens rows retracted`],
    cafe: [`café package uninstalled · ledger lenses retracted`],
    wellness: [`wellness package uninstalled · schedule lens retracted`],
  };

  // ---------------------------------------------------------------- wiring
  function renderAll() {
    root.dataset.persona = state.persona;
    renderGraph(); renderPanel(); renderEmergence();
  }

  el(".pkg-toggles").innerHTML = Object.entries(PKGS).map(([id, p]) =>
    `<button class="pkg-toggle${state.pkgs[id] ? " on" : ""}" data-pkg="${id}" type="button">
      <span class="dot"></span>${p.label}
    </button>`
  ).join("");

  el(".persona-tabs").innerHTML = Object.entries(PERSONAS).map(([id, p]) =>
    `<button class="persona-tab${state.persona === id ? " on" : ""}" data-persona-id="${id}" type="button">${p.label}</button>`
  ).join("");

  function setPkg(id, want, quiet) {
    if (state.pkgs[id] === want) return;
    state.pkgs[id] = want;
    const btn = root.querySelector(`.pkg-toggle[data-pkg="${id}"]`);
    if (btn) btn.classList.toggle("on", want);
    if (quiet) return;
    pushTick(want
      ? [`<span class="op">install ${id} package</span> → DDL via ops.meta.> · entities + lenses + operations registered`, ...TICKS[id]]
      : UNTICKS[id]);
  }

  function setPersona(id) {
    state.persona = id;
    root.querySelectorAll(".persona-tab").forEach(t => t.classList.toggle("on", t.dataset.personaId === id));
  }

  // ---------------------------------------------------------------- auto-tour
  // The demo drives itself so a reader who never touches a control still sees
  // the point: one graph, packages composing, each persona a different lens.
  // Any manual click hands the controls over and parks the tour.
  const ALL_PKGS = Object.keys(PKGS);
  const TOUR = [
    { pkgs: ["leasing"], persona: "resident", dwell: 4000,
      note: "One package installed. Maya's lens is her own subgraph — nothing else." },
    { pkgs: ["leasing"], persona: "front", dwell: 4800,
      note: "Same graph, different lens: the front desk sees today's work." },
    { pkgs: ["leasing"], persona: "back", dwell: 4800,
      note: "Operations sees the business — aggregates, not private records." },
    { pkgs: ["leasing", "clinic"], persona: "resident", dwell: 6000,
      note: "A second package installs — onto the identity Maya already had." },
    { pkgs: ["leasing", "clinic"], persona: "front", dwell: 5200,
      note: "No integration was written; the desk just sees both service lines." },
    { pkgs: ["leasing", "clinic"], persona: "provider", dwell: 6000,
      note: "The clinician is neither staff nor customer — one binding link wide." },
    { pkgs: ["leasing", "clinic", "cafe"], persona: "resident", dwell: 5800,
      note: "Café next: the tab settles on the lease — both hang off Maya." },
    { pkgs: ALL_PKGS, persona: "resident", dwell: 5800,
      note: "Wellness completes the building — the resident rate rides the lease." },
    { pkgs: ALL_PKGS, persona: "provider", dwell: 5600,
      note: "Two hats now — clinician and instructor, one identity, no new account." },
    { pkgs: ALL_PKGS, persona: "back", dwell: 5600,
      note: "Four packages, one graph: a view no single package could produce." },
    { pkgs: ALL_PKGS, persona: "operator", dwell: 6800,
      note: "Every vertex, every key. Reads are lenses; writes are operations." },
  ];

  const tour = (function () {
    const box = el(".demo-tour"), btn = el(".tour-btn");
    const bar = el(".tour-bar i"), note = el(".tour-note"), step = el(".tour-step");
    // A stale cached page without the tour strip still gets a working demo.
    if (!box || !btn || !bar || !note || !step) return { toggle() {}, takeOver() {} };
    const autoOK = !window.matchMedia("(prefers-reduced-motion: reduce)").matches;

    let idx = -1, timer = null, running = false, taken = false, inView = false;
    let remaining = 0, startedAt = 0, frozen = 0;

    function barRun(ms, from) {
      bar.style.transition = "none";
      bar.style.transform = `scaleX(${from})`;
      void bar.offsetWidth;
      bar.style.transition = `transform ${ms}ms linear`;
      bar.style.transform = "scaleX(1)";
    }
    function barFreeze() {
      const m = /matrix\(([-\d.]+)/.exec(getComputedStyle(bar).transform);
      frozen = m ? parseFloat(m[1]) : 0;
      bar.style.transition = "none";
      bar.style.transform = `scaleX(${frozen})`;
    }

    // Each step is an absolute target state, so resuming after a manual detour
    // never has to replay anything — it just states where it wants to be.
    function show(i) {
      const s = TOUR[i], want = new Set(s.pkgs);
      const drop = ALL_PKGS.filter(id => state.pkgs[id] && !want.has(id));
      if (drop.length > 1) {
        drop.forEach(id => setPkg(id, false, true));
        pushTick([`<span class="op">uninstall ${drop.join(" · ")}</span> → lens rows retracted · the graph keeps only what is installed`]);
      } else {
        drop.forEach(id => setPkg(id, false));
      }
      ALL_PKGS.filter(id => want.has(id)).forEach(id => setPkg(id, true));
      setPersona(s.persona);
      renderAll();
      note.textContent = s.note;
      step.textContent = `${i + 1}/${TOUR.length}`;
    }

    function advance() {
      idx = (idx + 1) % TOUR.length;
      show(idx);
      remaining = TOUR[idx].dwell;
      startedAt = performance.now();
      barRun(remaining, 0);
      timer = setTimeout(advance, remaining);
    }

    function start() {
      if (running) return;
      // Resuming after a manual detour moves on rather than finishing a step
      // whose caption no longer describes what is on screen.
      const detoured = taken;
      running = true; taken = false;
      box.classList.remove("paused");
      btn.setAttribute("aria-label", "Pause the auto tour");
      if (idx < 0 || detoured || remaining <= 400) { advance(); return; }
      startedAt = performance.now();
      barRun(remaining, frozen);
      timer = setTimeout(advance, remaining);
    }

    function halt(byUser) {
      clearTimeout(timer); timer = null;
      if (running) remaining = Math.max(800, remaining - (performance.now() - startedAt));
      running = false;
      barFreeze();
      box.classList.add("paused");
      btn.setAttribute("aria-label", "Play the auto tour");
      if (byUser) {
        taken = true;
        note.textContent = "You have the controls — toggle packages, switch lenses. Press play to resume the tour.";
        step.textContent = "";
      }
    }

    note.textContent = autoOK
      ? "A guided tour of package composition — take over any time."
      : "Press play for a guided tour of package composition.";

    if ("IntersectionObserver" in window) {
      new IntersectionObserver((entries) => {
        inView = entries[0].isIntersecting;
        if (inView) { if (autoOK && !taken && !document.hidden) start(); }
        else if (running) halt(false);
      }, { threshold: 0.25 }).observe(el(".demo-shell"));
    }

    document.addEventListener("visibilitychange", () => {
      if (document.hidden) { if (running) halt(false); }
      else if (inView && autoOK && !taken) start();
    });

    return { toggle: () => (running ? halt(true) : start()), takeOver: () => halt(true) };
  })();

  root.addEventListener("click", (e) => {
    if (e.target.closest(".tour-btn")) { tour.toggle(); return; }
    const pkgBtn = e.target.closest(".pkg-toggle");
    if (pkgBtn) {
      const id = pkgBtn.dataset.pkg;
      setPkg(id, !state.pkgs[id]);
      renderAll();
      tour.takeOver();
      return;
    }
    const tab = e.target.closest(".persona-tab");
    if (tab) {
      setPersona(tab.dataset.personaId);
      renderAll();
      tour.takeOver();
    }
  });

  // Keyboard users land on a control before they press it; stop the tour from
  // moving the target out from under them.
  root.addEventListener("focusin", (e) => {
    if (e.target.closest(".pkg-toggle, .persona-tab")) tour.takeOver();
  });

  pushTick([`bootstrap · kernel verified · <span class="op">install leasing package</span>`, ...TICKS.leasing.slice(0, 2)]);
  renderAll();
})();
