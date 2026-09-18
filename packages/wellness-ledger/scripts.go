package wellnessledger

import "fmt"

// ArrearsGraceDays is the net term between a charge posting and the balance it
// opened counting overdue. The package OWNS the term, so there is exactly one
// source for it: the Starlark below reads it as a Go duration string
// (ArrearsGraceDays × 24 hours, the form time.rfc3339_add takes) and
// cmd/wellness-app's statement math reads the constant directly. A member's
// displayed due date and the instant the arrears reminder fires are the same
// fact, and two copies of it drift into a statement that says one thing while
// the notification says another.
//
// Days × 24h is exact here because every timestamp this ledger stores is
// canonical UTC (time.rfc3339_utc at write time), where a calendar day is
// always 24 hours — Go's AddDate(0, 0, ArrearsGraceDays) over a UTC instant
// lands on the same second.
const ArrearsGraceDays = 15

// ArrearsPageLimit is the number of postedTo entries ONE dispatch of
// EvaluateWellnessArrears consumes — one kv.Links page — and ArrearsMaxPages
// the number of such pages an account's history may run to before the op
// records historyTooLong instead. The replay is RESUMABLE: a history longer
// than one page is folded page by page across successive dispatches, each
// recording its running aggregate and the cursor to resume from on the
// account's own .arrears.replay checkpoint, and Weaver chaining the
// dispatches through the lens's two phase gaps (lenses.go). The page, not the
// history, is what one execution pays for — the same mechanism clinic-ledger
// ships (clinic-ledger/scripts.go's identical constants carry the round-trip
// arithmetic against the Processor's 250 ms script wall this ledger's own
// per-entry cost is measured against too: a .entry read plus, on every
// credit, the two extra round trips of this ledger's settlesRefund → reverses
// walk below).
//
// The page cap bounds the checkpoint the account carries (twenty pages of
// per-debit aggregate) and the number of dispatches one evaluation may chain;
// a history past ArrearsPageLimit × ArrearsMaxPages entries records
// historyTooLong together with the budget it exhausted (historyBudget), so a
// later, larger budget reaches the accounts a smaller one parked.
const (
	ArrearsPageLimit = 30
	ArrearsMaxPages  = 20
)

// ArrearsPhaseA / ArrearsPhaseB are the two values of the replay checkpoint's
// phase, which flips on every page. The lens projects one continuation gap
// per value (lenses.go), so the page written under one phase closes the gap
// that dispatched it and opens the other; the op, the lens's cypher and the
// aspect DDL's schema all read the literal from here.
const (
	ArrearsPhaseA = "a"
	ArrearsPhaseB = "b"
)

// arrearsGracePrelude binds ArrearsGraceDays into Starlark, once, as the Go
// duration string time.rfc3339_add takes, together with the replay's page
// constants and phase literals. Only the account DDL computes an arrears due
// date (EvaluateWellnessArrears — this ledger stores no balance, so
// post_entry never names a head and never derives a date) or runs the paged
// replay, so only that script opens with it.
//
// Prepended rather than interpolated with a format verb, so no future edit to
// the script body is responsible for escaping a literal '%'.
var arrearsGracePrelude = fmt.Sprintf(`
ARREARS_GRACE_DURATION = "%dh"
ARREARS_PAGE_LIMIT = %d
ARREARS_MAX_PAGES = %d
ARREARS_PHASE_A = %q
ARREARS_PHASE_B = %q
`, ArrearsGraceDays*24, ArrearsPageLimit, ArrearsMaxPages, ArrearsPhaseA, ArrearsPhaseB)

// accountDDLScript is the account DDL's Starlark, opened by the grace-term
// binding above.
var accountDDLScript = arrearsGracePrelude + accountDDLScriptBody

// accountDDLScriptBody handles WellnessCreateAccount. The account gets its OWN
// independently-minted NanoID — vertex NanoIDs are unique identifiers across
// all of Core KV, never reused across vertex types, even deliberately (see
// clinic-ledger/scripts.go's adjacency-shared-nanoid-collision-design.md
// note, which this mirrors exactly). "One account per member" is instead
// enforced by a deterministic CREATE-ONLY guard aspect on the PRE-EXISTING
// identity (identityKey + ".wellnessLedgerAccount") — a second WellnessCreateAccount
// for the same identity conflicts on that already-existing aspect key, the
// same "let the key shape be the uniqueness guard" idiom, just anchored on
// the pre-existing parent instead of a freshly-minted sibling. Root data
// stays {} on the account (D5): the balance is derived by the
// wellnessLedgerHistory lens, never stored here.
//
// It ALSO handles EvaluateWellnessArrears, the Weaver-dispatched arrears
// evaluation: it recomputes the account's FIFO-oldest open charge over a
// resumable, page-per-dispatch replay of the postedTo history (a history
// longer than one page records its running aggregate as a checkpoint on
// .arrears.replay and Weaver dispatches the next page), records the resulting
// due date in the account's own .arrears aspect, and — once that date has
// passed and no reminder has gone out for it — fires the external.notification
// the bridge turns into a real message to the member. The FIFO is NOT
// maintained incrementally by post_entry: this ledger stores no balance, so a
// posted entry cannot even tell an episode opening from one continuing, let
// alone name the head a partial payment moved to. The head is recomputed only
// when it matters — post_entry keeps only the coarse "what is recorded no
// longer describes this account" mark (stale) the convergence lens reads as a
// request for this recomputation, and the never-evaluated gap (evaluatedAt
// absent) is what first evaluates every account.
const accountDDLScriptBody = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_update(vtx_key, local_name, cls, data):
    # Deliberately NO expectedRevision — a bare update on a key the operation
    # DECLARED is auto-conditioned on the step-4 hydrated revision (Contract
    # #3 §3.2) and marked retry-eligible, so a lost race re-hydrates and
    # re-runs instead of hard-conflicting; this script's own derive_reads is
    # what guarantees the declaration. It is also the reviving verb for a
    # tombstoned aspect, which a create would only collide with (Contract #3
    # §3.3).
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

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
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def parts_of(key, name, want_type):
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

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

# EvaluateWellnessArrears replays the account's postedTo history ONE PAGE PER
# DISPATCH: arrears_entries consumes a single kv.Links page of
# ARREARS_PAGE_LIMIT entries (bound from Go's ArrearsPageLimit via the prelude,
# where the round-trip arithmetic against the Processor's 250 ms script wall
# lives) and folds it into a running dict keyed by transaction id. A history
# that fits one page finalizes in the same execution; a longer one records the
# dict so far and the cursor to resume from on the account's .arrears.replay
# checkpoint, and the next dispatch — Weaver's, through the lens's phase gaps
# — continues from there. A page pays the same whatever the history's length,
# so any history up to ARREARS_PAGE_LIMIT × ARREARS_MAX_PAGES entries is
# reached exactly. The self-credit cap in transactionDDLScript replays the
# same history under its own separate numbers (SELF_CREDIT_PAGE_LIMIT /
# SELF_CREDIT_MAX_PAGES) — a different budget for a different caller, not
# this one.
#
# UNLIKE clinic-ledger's (and loftspace-ledger's) checkpoint, the entry keyed
# by each transaction id keeps its OWN postedAt and type rather than folding
# into per-debit sums and a single running credit total. Clinic's netting
# shortcut ("credits cover debits oldest-first whatever their own timing, so
# the final open set does not depend on where in the walk a credit sat") is
# exact for the HEAD and the BALANCE, but this ledger's arrears_head ALSO
# computes the EPISODE START — the postedAt of the debit that took the queue
# from empty to non-empty, walked in true chronological order — and that
# computation is NOT order-independent: collapsing every credit into one sum
# sorted as if paid before every debit retroactively makes the queue look
# like it never went empty, which can rename a LATER debit as the episode's
# opener when the real temporal walk kept an earlier one. Each entry also
# keeps its own reversesKey (the id of the charge a refund credit gives
# back): this ledger's netting pre-pass (arrears_head's own) is exact only
# when it can still see which credit reverses which charge; loftspace-ledger
# carries no reverses relation at all, so its rows always net as plain FIFO.
# arrears_rows below rebuilds arrears_head's usual per-row input from the
# dict once the enumeration is exhausted, so arrears_head itself runs the
# same sort-then-walk over the whole history in one execution regardless of
# how many dispatches filled the dict.
#
# An account whose history runs past ARREARS_MAX_PAGES pages is not aged
# against a truncated FIFO — a partial replay would name the wrong head and
# the reminder that went out would name a charge the member had already paid.
# But it does not fail either: see the degrade branch in execute(). A refusal
# there is a PERMANENT silent stop, because the only thing that re-drives the
# op is the very gap this account's row opens, and a rejected op never closes
# it — Weaver would re-dispatch a doomed evaluation on every window, forever,
# with no reminder and no operator signal. Instead the exhaustion is RECORDED
# (historyTooLong, with the entry budget it exhausted as historyBudget) so the
# row goes quiet, the operator can see it in the read model, the next posted
# entry re-arms one more attempt, and a raised budget reaches the accounts the
# smaller one parked, once.
def arrears_entries(acct_key, cursor, entries):
    # One page of the account's live postedTo entries, folded into entries =
    # {txId: {postedAt, type, amountCents, reversesKey}}: keyed by transaction
    # id so a page is idempotent to re-fold (identity is also what makes a
    # reversal exact when two charges share a second — mirrors clinic-ledger's
    # per-txId keying). reversesKey is the id of the charge a refund credit
    # gives back — None for a payment or a waiver, or for a refund whose
    # marker link is tombstoned — and arrears_rows/arrears_head's netting
    # pre-pass reads it to retire that specific charge instead of aging it by
    # plain FIFO order (mirrors cmd/wellness-app/ledger.go deriveStatement,
    # which reads the same fact off wellnessLedgerHistory's reversesKey
    # column). Returns (entries, next_cursor); next_cursor is None once the
    # enumeration is exhausted. An entry missing any of
    # postedAt/type/amountCents is skipped rather than guessed at, exactly as
    # the self-credit replay skips it.
    #
    # A refund's charge is TWO hops away here, not one: this ledger's refund
    # credit carries a settlesRefund link to wellness-domain's wellnessrefund
    # marker, and the MARKER carries the reverses link to the charge it gives
    # back (the booking the charge was for is tombstoned by the time the
    # refund posts, which is the whole reason the marker exists). Both hops
    # are written once, atomically, at their own mint sites and never added to
    # afterward, so a limit of 1 with no cursor loop is exact on each.
    #
    # read-posture: (e) relation=postedTo epoch=none -- one page per dispatch;
    # the cursor is carried on the account's .arrears.replay checkpoint and
    # the caller degrades past ARREARS_MAX_PAGES pages.
    page, next_cursor = kv.Links(acct_key, "postedTo", "in", cursor, ARREARS_PAGE_LIMIT)
    for lk in page:
        if lk.isDeleted:
            continue
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above -- each transaction's own .entry aspect, data-derived and
        # unknowable client-side.
        tx_entry = kv.Read(lk.sourceVertex + ".entry")
        if tx_entry == None or tx_entry.isDeleted:
            continue
        tx_amount = tx_entry.data.get("amountCents")
        tx_posted_at = tx_entry.data.get("postedAt")
        tx_type = tx_entry.data.get("type")
        if tx_amount == None or tx_posted_at == None or tx_type == None:
            continue
        _, tx_id = parts_of(lk.sourceVertex, "postedTo source", "wellnesstransaction")
        reverses_id = None
        if tx_type == "credit":
            # read-posture: (e) relation=settlesRefund epoch=none -- a refund
            # credit carries exactly one settlesRefund link, written
            # atomically by WellnessCreditAccount and never added to
            # afterward, so a limit of 1 (no cursor loop) is exact, never a
            # keyspace scan. The limit is not optional: this runs once per
            # CREDIT on the page, so an unbounded page here would be charged
            # at the 256 default against the script's live-read budget, and a
            # page of credits blows it with a script error instead of the
            # paged replay this function's own doc comment promises.
            refund_page, _ = kv.Links(lk.sourceVertex, "settlesRefund", "out", None, 1)
            marker_key = None
            for lk2 in refund_page:
                if not lk2.isDeleted:
                    marker_key = lk2.targetVertex
            if marker_key != None:
                # read-posture: (e) relation=reverses epoch=none -- the marker
                # carries exactly one reverses link, written by the op that
                # minted it (wellness-domain/ddls.go) and never repointed, so
                # a limit of 1 is exact here for the same reason as the hop
                # above.
                reverses_page, _ = kv.Links(marker_key, "reverses", "out", None, 1)
                for lk3 in reverses_page:
                    if not lk3.isDeleted:
                        _, reverses_id = parts_of(lk3.targetVertex, "reverses target", "wellnesstransaction")
        entries[tx_id] = {"postedAt": tx_posted_at, "type": tx_type,
                          "amountCents": tx_amount, "reversesKey": reverses_id}
    return entries, next_cursor

def arrears_checkpoint(prior):
    # The replay checkpoint the recorded .arrears carries, or None when it
    # carries none — or one this op cannot resume. A checkpoint is resumable
    # only when every field the fold needs has the shape the fold wrote: a
    # non-empty cursor string, a phase the lens projects a gap for, a page
    # count of at least one, and a dict of entries. Anything else is treated
    # as ABSENT — the evaluation starts again at page 1 over a fresh dict —
    # rather than resumed or refused: resuming would fold new pages onto a
    # corrupt dict and name a wrong head, and a refusal would leave whichever
    # gap dispatched this op open for Weaver to re-dispatch the same doomed
    # read until its retry budget parked the row. Restarting costs one replay
    # and records a well-formed checkpoint in its place.
    replay = prior.get("replay")
    if replay == None or type(replay) != type({}):
        return None
    cursor = replay.get("cursor")
    phase = replay.get("phase")
    pages = replay.get("pages")
    entries = replay.get("entries")
    if type(cursor) != type("") or cursor == "":
        return None
    if phase != ARREARS_PHASE_A and phase != ARREARS_PHASE_B:
        return None
    if type(pages) != type(0) or pages < 1:
        return None
    if type(entries) != type({}):
        return None
    return {"cursor": cursor, "phase": phase, "pages": pages, "entries": entries}

def arrears_rows(entries):
    # arrears_head's usual per-row input, rebuilt from the finished dict:
    # every entry the replay folded, keyed back out into
    # {postedAt, key, type, amountCents, reversesKey} — reversesKey resolved
    # to the reversed charge's FULL key, since arrears_head compares it
    # against other rows' own "key" field. This reconstructs EXACTLY the rows
    # a single, whole-history execution has always produced (arrears_head
    # itself is unchanged), so its FIFO walk and its episode-start tracking
    # both still see every credit at its own true postedAt.
    rows = []
    for tx_id, entry in entries.items():
        reverses_key = None
        if entry["reversesKey"] != None:
            reverses_key = "vtx.wellnesstransaction." + entry["reversesKey"]
        rows.append({"postedAt": entry["postedAt"], "key": "vtx.wellnesstransaction." + tx_id,
                     "type": entry["type"], "amountCents": entry["amountCents"],
                     "reversesKey": reverses_key})
    return rows

def arrears_head(entries):
    # The FIFO the member's own statement runs, reproduced exactly
    # (cmd/wellness-app/ledger.go deriveStatement + sortLedgerRows): entries
    # in (postedAt, transactionKey) order.
    #
    # A pre-pass nets every credit that names the charge it reverses
    # (reversesKey) against that debit's own face amount — capped there,
    # accumulated across however many reversing credits name the same
    # debit — before the FIFO walk ever runs, so a refund of a NEWER charge
    # does not pay off an OLDER, unrelated one. A debit fully retired this
    # way never opens; a partially-retired one opens for the remainder.
    #
    # Everything else still FIFOs: credits offset the OLDEST still-open
    # debit first, and a credit with no open debit to apply to (net of
    # whatever it retired above) carries its remainder forward as surplus
    # that prepays whichever debits arrive next. The survivor at the front
    # of the queue is the charge that has actually been unpaid longest —
    # not merely the most recent one — which is the whole reason a
    # reminder can name a date the member recognises.
    #
    # The sort key is the PAIR, not postedAt alone: postedAt is whole-second
    # canonical UTC (time.rfc3339_utc), so two charges posted in the same
    # second are indistinguishable by time and the transaction key is what
    # makes the order total. Without it the op and the statement can disagree
    # about which of the two is the head, and so about the due date.
    rows = sorted(entries, key=lambda e: (e["postedAt"], e["key"]))

    debit_amount = {}
    for r in rows:
        if r["type"] == "debit":
            debit_amount[r["key"]] = r["amountCents"]
    absorbed_total = {}
    absorbed_by_credit = {}
    for r in rows:
        if r["type"] != "credit" or r["reversesKey"] == None:
            continue
        target = debit_amount.get(r["reversesKey"])
        if target == None:
            continue
        remaining = target - absorbed_total.get(r["reversesKey"], 0)
        absorbed = r["amountCents"]
        if absorbed > remaining:
            absorbed = remaining
        absorbed_by_credit[r["key"]] = absorbed
        absorbed_total[r["reversesKey"]] = absorbed_total.get(r["reversesKey"], 0) + absorbed

    open_debits = []
    surplus = 0
    balance_cents = 0
    episode_start = None
    for r in rows:
        amount = r["amountCents"]
        if r["type"] == "debit":
            balance_cents += amount
            amount -= absorbed_total.get(r["key"], 0)
            if amount <= 0:
                continue
            if surplus >= amount:
                surplus -= amount
                continue
            amount -= surplus
            surplus = 0
            if len(open_debits) == 0:
                episode_start = r["postedAt"]
            open_debits.append({"postedAt": r["postedAt"], "remaining": amount})
        elif r["type"] == "credit":
            balance_cents -= amount
            remaining = amount - absorbed_by_credit.get(r["key"], 0)
            # Starlark has no while: each pass either zeroes the remainder or
            # retires one open debit, so len+1 passes is an exact bound, not a
            # budget that can run out mid-walk.
            for _i in range(len(open_debits) + 1):
                if remaining <= 0 or len(open_debits) == 0:
                    break
                if open_debits[0]["remaining"] > remaining:
                    open_debits[0]["remaining"] -= remaining
                    remaining = 0
                else:
                    remaining -= open_debits[0]["remaining"]
                    open_debits = open_debits[1:]
            surplus += remaining
    # An empty queue and a non-positive balance are the same condition under
    # FIFO consumption (credits >= debits leaves nothing open, and vice versa),
    # which is why deriveStatement's early "balance <= 0 has nothing to age"
    # return needs no counterpart here.
    #
    # The walk ALSO tracks the EPISODE START, and execute() reads it as the
    # boundary a recorded send is judged against. An episode is the stretch
    # from the charge that took the account from square (nothing open) to
    # owing, until the queue is next empty; formally, the episode start is
    # the postedAt of the debit that was appended onto an EMPTY open queue —
    # a debit the surplus could not prepay outright, since a prepaid (or
    # fully reversed) debit never opens. The head is NOT necessarily that
    # opener: a partial payment that exactly retires the opener moves the
    # head to the next open charge while the member stays continuously in
    # arrears, and that charge posted after the opener — so a boundary read
    # off the head's own postedAt would mistake a moved head for a new
    # episode and send a second reminder for a debt the member is visibly
    # paying down. Every debit processed before the opener was retired (the
    # queue was empty when it arrived), so the opener is the oldest charge of
    # the current episode and its postedAt is the instant the episode began;
    # a payment to zero empties the queue and the next debit that opens it
    # starts the next episode, resetting the instant. Netting does not
    # disturb this: a reversed charge and its reversing credit are removed
    # from the FIFO pair-wise before the walk, so "square" is judged net of
    # reversals, which is the statement's own rule. No second walk over the
    # history is needed to find the boundary.
    if len(open_debits) == 0:
        return None, balance_cents
    return {"postedAt": open_debits[0]["postedAt"], "episodeStart": episode_start}, balance_cents

def carry_arrears(doc):
    out = {}
    for k, v in doc.data.items():
        out[k] = v
    return out

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_wellnessaccount_key(key):
    # Contract #1's whole vertex grammar for a wellnessaccount, not a prefix
    # test — derive_reads returns keys the Processor validates against that
    # grammar and answers a malformed one with a DeriveReadsInvalid hydration
    # fault raised BEFORE the operation's own validation runs, which would
    # turn this branch's clean "InvalidArgument: accountKey" into an opaque
    # hydration failure. The same helper transactionDDLScript carries, for
    # the same reason.
    if key == None or type(key) != type(""):
        return False
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "wellnessaccount":
        return False
    if len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

def identity_for_account(acct_key):
    # The member this wellnessaccount is held for, or None where no live
    # heldFor link exists -- an account whose identity has since gone dead
    # still ages its own history normally. Mirrors transactionDDLScript's own
    # self-credit ownership walk: a wellnessaccount's heldFor link targets
    # the IDENTITY directly (WellnessCreateAccount above), one hop.
    # read-posture: (e) relation=heldFor epoch=none -- a wellnessaccount
    # carries at most one heldFor link, so this is never a keyspace scan.
    page, _ = kv.Links(acct_key, "heldFor", "out", None, 1)
    identity = None
    for lk in page:
        if not lk.isDeleted:
            identity = lk.targetVertex
    if identity == None:
        return None
    # read-posture: (e) per-candidate follow-up read off the enumeration
    # above -- the identity ROOT (never a sensitive aspect: the root carries
    # no PII, so a shredded holder answers here like any other), data-derived
    # and unknowable client-side.
    identity_doc = kv.Read(identity)
    if identity_doc == None or identity_doc.isDeleted:
        return None
    return identity

def derive_reads(op):
    # Contract #2 §2.5 class (g), for the same reason transactionDDLScript's
    # own derive_reads exists: the .arrears write below is a bare update
    # auto-conditioned on the step-4 hydrated revision ONLY for a key the
    # operation declared (Contract #3 §3.2), and contextHint is
    # caller-supplied and never enforced. A dispatch that omitted the
    # declaration would get a live read and an UNCONDITIONED write, so two
    # evaluations racing one account (a redelivery alongside a fresh
    # dispatch) could each decide to send against the same prior state. The
    # wellnessArrearsReminders target declares the same key (targets.go);
    # that DOCUMENTS the read set, this GUARANTEES it.
    #
    # optionalReads, never reads: every account alive today carries no
    # .arrears at all, and a required read's absence is a HydrationMiss that
    # would block the very first evaluation of each one.
    #
    # The account ROOT itself rides the same declaration, for a distinct
    # reason: vertex_alive(state, acct_key) below decides UnknownAccount by
    # testing acct_key not in state, which cannot tell "genuinely absent"
    # from "never declared or derived" apart. Any dispatcher that omits the
    # root from its own contextHint would otherwise see a live account
    # rejected as unknown.
    if op.operationType != "EvaluateWellnessArrears":
        return {}
    acct_key = optional_string(op.payload, "accountKey")
    if not is_wellnessaccount_key(acct_key):
        return {}
    return {"optionalReads": [acct_key, acct_key + ".arrears"]}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "EvaluateWellnessArrears":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the
        # wellnessArrearsReminders directOp playbook. accountKey arrives off the
        # payload and the account it names is forwarded in the
        # external.notification body the bridge turns into a real message to a
        # member, so a wider submitter set is a forged send: an arbitrary
        # operator naming any account it likes and having the platform tell
        # that member they owe money. First statement in the branch: it also
        # denies the payload-shape, vertex-alive and history-shape oracles
        # beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: EvaluateWellnessArrears is restricted to Weaver's dispatch actor; got " + op.actor)

        acct_key = required_string(p, "accountKey")
        parts_of(acct_key, "accountKey", "wellnessaccount")

        # Liveness guard: never mint arrears state (or a 4-segment aspect key)
        # on an absent or tombstoned account. The root is hydrated whatever the
        # dispatcher declared (derive_reads above).
        if not vertex_alive(state, acct_key):
            fail("UnknownAccount: " + acct_key + " is absent or tombstoned; no arrears evaluated")

        # The member this account is held for, resolved LIVE from the
        # account's own heldFor out-link -- never from the payload, so the
        # identity a member is told they owe against cannot be forged by an
        # arbitrary submitter (the same forged-send surface the actor guard
        # above closes, one step further along). Optional: an account with no
        # live heldFor identity is still evaluated -- the arrears fact is
        # about the ACCOUNT -- and the resolved identity is routed into the
        # notification's params purely for the adapter's own addressing;
        # nothing this op decides depends on it. It is also never a Params
        # hop on the Weaver row: the strategist refuses to dispatch a row
        # whose Params reference a null column, which would starve exactly
        # the accounts most worth aging.
        identity_key = identity_for_account(acct_key)

        # The op's own timestamp, normalized to canonical UTC so the lexical
        # compare against dueAt below is sound to the second (a raw compare
        # mis-answers for the first second after an instant, '.' sorting
        # below 'Z').
        evaluated_at = time.rfc3339_utc(op.submittedAt)

        arrears_key = acct_key + ".arrears"
        # read-posture: (d) optionalReads — derived server-side by this script's
        # own derive_reads(op) for EvaluateWellnessArrears (Contract #2 §2.5
        # class (g)), and declared statically by the wellnessArrearsReminders
        # target's GapActionSpec.OptionalReads (targets.go). Two questions,
        # not one: absence decides the WRITE VERB (a create is refused against
        # a tombstone, Contract #3 §3.3, so only a genuinely absent key is
        # minted), presence-and-live decides whether there is prior episode
        # state to carry.
        arrears_doc = kv.Read(arrears_key)
        arrears_absent = arrears_doc == None
        prior = {}
        if arrears_doc != None and not arrears_doc.isDeleted:
            # The CLASS, not just the key: this package is the sole writer of
            # a .arrears aspect and writes exactly that class, so a document
            # of any other class here is a fault to refuse, never state to
            # decide a send on.
            if not hasattr(arrears_doc, "class") or getattr(arrears_doc, "class") != "wellnessAccountArrears":
                fail("InvalidState: this account's arrears aspect is not a wellnessAccountArrears")
            prior = carry_arrears(arrears_doc)

        # The replay checkpoint, if a previous dispatch left one: the pages
        # consumed so far, the cursor to resume from and the entries folded
        # so far. Absent, this dispatch starts at page 1 over a fresh dict. A
        # recorded value is read as the fact it records — a checkpoint is
        # progress through an enumeration, not an evaluation, so nothing here
        # decides a send.
        replay = arrears_checkpoint(prior)
        cursor = None
        entries = {}
        pages = 1
        if replay != None:
            cursor = replay["cursor"]
            entries = replay["entries"]
            pages = replay["pages"] + 1

        if pages > ARREARS_MAX_PAGES:
            # DEGRADE, never refuse. The account's history outran the replay
            # budget, so the FIFO head is unknown and no send can be justified —
            # but a rejection would be a permanent silent stop: the row's own
            # gap is the only thing that re-drives this op, and a rejected op
            # leaves it open, so Weaver would re-dispatch the same doomed
            # evaluation every window and nothing would ever be sent or seen.
            # Recording the exhaustion instead makes it OBSERVABLE and QUIET:
            # the lens suppresses both the gap and the timer on
            # historyTooLong, so the dispatch loop stops while the row stays
            # in the weaver-targets bucket for an operator to find. The
            # budget exhausted is recorded beside the flag (historyBudget, in
            # entries), so the lens can tell an account parked under a smaller
            # budget from one parked under this one and re-arm the former
            # exactly once. What was already recorded is carried untouched —
            # a reminder already sent stays recorded as sent, and a due date
            # already armed is not erased by an evaluation that could not
            # read the history. stale is dropped: it asks for a recomputation
            # this op has just attempted, and re-asking would re-open the gap
            # the degrade is closing. The checkpoint is dropped with it: a
            # degrade ends the replay, and a surviving checkpoint would keep a
            # phase gap open under the flag. post_entry's own carry drops
            # historyTooLong and historyBudget in turn, so the next posted
            # entry buys exactly one more attempt — bounded to one replay per
            # entry, never a loop.
            data = {"evaluatedAt": evaluated_at, "historyTooLong": True,
                    "historyBudget": ARREARS_PAGE_LIMIT * ARREARS_MAX_PAGES}
            for carried in ["dueAt", "remindedFor", "sentAt"]:
                carried_value = prior.get(carried)
                if carried_value != None:
                    data[carried] = carried_value
            if arrears_absent:
                mutations = [make_aspect(acct_key, "arrears", "wellnessAccountArrears", data)]
            else:
                mutations = [make_aspect_update(acct_key, "arrears", "wellnessAccountArrears", data)]
            return {"mutations": mutations,
                    "events": [{"class": "account.arrearsEvaluated",
                                "data": {"accountKey": acct_key, "dueAt": data.get("dueAt"),
                                         "sentAt": data.get("sentAt"), "balanceCents": None,
                                         "historyTooLong": True}}],
                    "response": {"primaryKey": acct_key}}

        entries, next_cursor = arrears_entries(acct_key, cursor, entries)

        if next_cursor != None:
            # MID-REPLAY: the enumeration has more pages. Every recorded field
            # of the episode is carried verbatim — dueAt, remindedFor, sentAt,
            # stale, evaluatedAt all still describe the account exactly as the
            # last COMPLETED evaluation or posted entry left them; this
            # dispatch has evaluated nothing, so evaluatedAt is not re-stamped
            # and nothing is sent — and the checkpoint is rewritten whole: the
            # advanced cursor, the page count, the entries folded so far, and
            # a phase that flips on every page. The phase is Weaver's
            # continuation trigger: the lens projects one gap per phase, so
            # the page that closes the gap that dispatched it opens the
            # other, and each gap episode is exactly one dispatch (lenses.go).
            # A redelivered dispatch reads the advanced checkpoint and
            # consumes the NEXT page — progress, never repetition — and two
            # dispatches racing one checkpoint serialize on the .arrears
            # revision this write is conditioned on (make_aspect_update).
            phase = ARREARS_PHASE_A
            if replay != None and replay["phase"] == ARREARS_PHASE_A:
                phase = ARREARS_PHASE_B
            data = {}
            for k, v in prior.items():
                if k != "replay":
                    data[k] = v
            data["replay"] = {"phase": phase, "cursor": next_cursor, "pages": pages,
                              "entries": entries}
            if arrears_absent:
                mutations = [make_aspect(acct_key, "arrears", "wellnessAccountArrears", data)]
            else:
                mutations = [make_aspect_update(acct_key, "arrears", "wellnessAccountArrears", data)]
            return {"mutations": mutations,
                    "events": [{"class": "account.arrearsReplayPaged",
                                "data": {"accountKey": acct_key, "pages": pages}}],
                    "response": {"primaryKey": acct_key}}

        # FINALIZE: the enumeration is exhausted, so entries is the whole
        # history and arrears_rows rebuilds arrears_head's usual per-row
        # input from it — the head is computed exactly as a single-page
        # evaluation always has. Nothing below carries the checkpoint — a
        # finished evaluation has no replay in progress.
        head, balance_cents = arrears_head(arrears_rows(entries))

        # evaluatedAt is written on EVERY outcome, including "owes nothing":
        # its absence is what opens the convergence gap for an account that
        # has never been evaluated, so a run that recorded nothing would
        # re-dispatch forever.
        data = {"evaluatedAt": evaluated_at}
        events = []

        if head != None:
            due_at = time.rfc3339_add(head["postedAt"], ARREARS_GRACE_DURATION)
            data["dueAt"] = due_at
            reminded_for = prior.get("remindedFor")
            sent_at = prior.get("sentAt")
            # The EPISODE BOUNDARY. This ledger stores no balance, so nothing
            # ends an episode at the entry that pays it off: a payment to
            # zero and a fresh charge can both post before any evaluation
            # runs, and the recorded send record then belongs to an episode
            # that is already over. arrears_head's episodeStart is the
            # postedAt of the charge that OPENED the current episode (not the
            # head, which a partial payment may have moved past the opener
            # while the member stayed in arrears), and a reminder can only
            # ever have gone out for a head at least the net term old — so a
            # send stamped AT or BEFORE the current episode's opener was
            # necessarily a send for an earlier one, and is dropped here
            # (remindedFor with it: the two are written together and mean
            # nothing apart). Ties resolve the same way and for the same
            # reason: a charge that posts in the very second a reminder goes
            # out cannot be in the episode that reminder was for, since that
            # episode's head posted a whole term earlier; the compare is on
            # the canonical whole-second UTC instants both values are written
            # as. A send AFTER the episode start is this episode's own and is
            # carried, whether the head has since moved (a partial payment)
            # or not.
            episode_start = head["episodeStart"]
            if sent_at != None and sent_at <= episode_start:
                sent_at = None
                reminded_for = None
            if due_at <= evaluated_at:
                # The head has come due. remindedFor records THIS due date
                # whatever else happens: it is the conjunct the convergence
                # lens reads, so leaving it naming an older head would hold
                # the gap open and have Weaver re-dispatch this same
                # evaluation forever.
                data["remindedFor"] = due_at
                # Whether anything is SENT is a different question, and its
                # answer is sentAt's absence. The unit of "one reminder" is the
                # EPISODE — the stretch from the charge that took the account
                # from square to owing, until the balance comes back to zero —
                # not the head, which a partial payment moves from one open
                # charge to the next while the member stays continuously in
                # arrears. Keying the send off remindedFor instead would nag
                # on every part-payment: pay some, the head shifts to a charge
                # whose own term has also passed, and a second notification
                # goes out for a debt the member is visibly paying down.
                # sentAt is carried across every write of a live episode and
                # dropped only where the episode itself ends (the no-open-head
                # branch below, and the episode-boundary check above when a
                # newer episode has already opened — post_entry never ends
                # one, since it has no balance to see zero on), so its
                # absence means exactly "nothing has gone out in this
                # episode". sentAt records the
                # SEND INTENT — stamped on the commit that emits the outbox
                # event; the adapter's delivery outcome lands on
                # .arrearsNotification (notifications.go).
                if sent_at != None:
                    data["sentAt"] = sent_at
                else:
                    data["sentAt"] = evaluated_at
                    # Keyed on (accountKey, dueAt): a redelivery of the same
                    # due episode reuses the key so the adapter dedups, while
                    # a later episode (a new head, a new due date) mints a
                    # fresh one and sends again. Fired off this op's own
                    # transactional outbox — no Loom pattern, the bridge's
                    # dispatch path is fully generic.
                    ext_ref = acct_key + ":" + due_at
                    notif_params = {"accountKey": acct_key, "reminderType": "wellnessArrears",
                                    "dueAt": due_at, "balanceCents": balance_cents}
                    if identity_key != None:
                        notif_params["identityKey"] = identity_key
                    events.append({"class": "external.notification",
                                   "data": {"instanceKey": ext_ref, "adapter": "notification",
                                            "replyOp": "RecordWellnessArrearsReminderNotification",
                                            "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                            "params": notif_params}})
            else:
                # The head is not due yet: the timer re-arms at this dueAt,
                # and the episode's record — whichever head it was reminded
                # for, and when — is carried forward unchanged. Dropping it
                # would re-open the gate and send a second time for the same
                # episode.
                if reminded_for != None:
                    data["remindedFor"] = reminded_for
                if sent_at != None:
                    data["sentAt"] = sent_at
        # head == None: nothing is owed, so the episode is over and
        # {evaluatedAt} alone is written — dueAt, remindedFor, sentAt and stale
        # all go with it, which is what lets the NEXT charge open a clean
        # episode rather than inherit this one's send record. An episode ends
        # ONLY in this op: post_entry has no balance to reason from, so a
        # payment to zero marks the state stale and this recomputation is
        # what clears it — here when the history nets to nothing owed, and
        # in the boundary check above when a fresh charge had already opened
        # the next episode before this evaluation ran.
        #
        # stale is never carried on ANY path: recomputing the head from the
        # account's own history is precisely what stale asks for, and it has
        # just happened. historyTooLong and historyBudget go the same way for
        # the same reason — this evaluation read the whole history, so
        # whatever recorded that it once could not is answered, and the lens
        # un-suppresses the row. The replay checkpoint is not carried either:
        # the enumeration it recorded progress through is finished.

        if arrears_absent:
            mutations = [make_aspect(acct_key, "arrears", "wellnessAccountArrears", data)]
        else:
            mutations = [make_aspect_update(acct_key, "arrears", "wellnessAccountArrears", data)]

        events.append({"class": "account.arrearsEvaluated",
                       "data": {"accountKey": acct_key, "dueAt": data.get("dueAt"),
                                "sentAt": data.get("sentAt"), "balanceCents": balance_cents}})
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    if ot == "WellnessCreateAccount":
        identity_key = required_string(p, "identityKey")
        _, identity_id = parts_of(identity_key, "identityKey", "identity")

        # Consumer self-scope (scope=self grant only): step 3 authorizes via
        # authContext.target == actor (Contract #6); payload.identityKey IS
        # the identity the account is opened for, so the script closes the
        # gap with a direct field compare — no extra kv.Read, mirroring
        # wellness-domain's CreateBooking/payload.booker check. Empty for
        # the standing operator/frontOfHouse grant (scope=any never sets
        # authContext), so this check is a no-op there.
        # authcontext-target: (payload-bind) the target must equal payload.identityKey
        if op.authContextTarget != "" and op.authContextTarget != identity_key:
            fail("AuthDenied: a consumer may only open their OWN ledger account")

        # No-orphan invariant: the identity MUST be alive.
        if not vertex_alive(state, identity_key):
            fail("UnknownIdentity: " + identity_key)

        # One account per member, guarded by a deterministic aspect on the
        # IDENTITY (not the account — the account's own id is independent and
        # unknown until minted below). Only meaningful when the caller declared
        # the guard key in contextHint.reads (a repeat/racing caller checking
        # before it retries); the FIRST WellnessCreateAccount for an identity declares
        # only identityKey (the guard doesn't exist yet — declaring an
        # as-yet-absent key in reads would HydrationMiss on first touch,
        # deferred past hydration), so on that path the
        # guard aspect's own create-only write is the actual uniqueness
        # enforcement: a genuine race's loser hits a raw substrate conflict
        # here rather than this clean rejection.
        guard_key = identity_key + ".wellnessLedgerAccount"
        if vertex_alive(state, guard_key):
            fail("AccountAlreadyExists: " + identity_key)

        acct_id = nanoid.new()
        acct_key = "vtx.wellnessaccount." + acct_id

        # heldFor: the account (later-arriving) is the source, the pre-existing
        # identity is the target (Contract #1 §1.1). Reads as "this account is
        # held for this identity."
        held_for_lnk = "lnk.wellnessaccount." + acct_id + ".heldFor.identity." + identity_id

        # Root data minimal (D5): {} on root. The balance is derived by the
        # wellnessLedgerHistory lens summing linked transactions, never stored here.
        mutations = [
            make_vtx(acct_key, "wellnessaccount", {}),
            make_aspect(identity_key, "wellnessLedgerAccount", "wellnessLedgerAccountGuard", {"accountKey": acct_key}),
            make_link(held_for_lnk, acct_key, identity_key, "heldFor", "heldFor", {}),
        ]
        events = [{"class": "account.created",
                   "data": {"accountKey": acct_key, "identityKey": identity_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    fail("account DDL: unknown operationType: " + ot)
`

// transactionDDLScript handles WellnessDebitAccount and WellnessCreditAccount. Each mints a
// fresh transaction vertex + a .entry aspect + the postedTo link to the
// account. The ledger is append-only: no aspect on the account is read or
// mutated here, so concurrent debits/credits against the same account never
// race a read-modify-write — the balance is derived by the
// wellnessLedgerHistory lens summing entries. Unlike clinic-ledger's
// transaction DDL, there is no billedTo/insurance payer dimension — wellness
// billing has no insurance concept, so a debit entry stays the plain
// {type, amountCents, memo?, postedAt} shape — memo optional except on a
// MANUAL charge (a debit carrying neither booking ref), where it is required
// non-blank because the entry is append-only and nothing can annotate it
// later. A credit entry additionally
// carries reason (payment|waiver|refund, default payment) — a waiver forgives
// debt (e.g. a waived no-show fee) and a refund returns money already
// collected (a settled class price or no-show fee reversed by
// wellnessRefundSettlement), rather than recording cash freshly collected;
// rejected on a debit, and rejected on a self-scoped (member) credit, which
// may only pay down a balance, mirroring clinic-ledger exactly.
//
// The one aspect on the ACCOUNT a posted entry does touch is its .arrears
// episode state (class wellnessAccountArrears, ddls.go), and only ever to
// mark it: this ledger stores no balance, so an entry has no before/after
// number to tell an episode opening from one continuing, or a payment that
// clears the balance from one that leaves some. It therefore records exactly
// one thing — "what is recorded no longer describes this account" (stale),
// carrying every other field except a replay checkpoint — and mints nothing
// where nothing exists, since an account with no arrears state is already
// opening the never-evaluated gap. A checkpoint (.arrears.replay, present
// only while EvaluateWellnessArrears is mid-way through a history longer
// than one page) is always dropped: this entry changes the postedTo set the
// checkpoint's cursor pages over, so a page resumed from it would skip or
// double-count an entry, and the stale mark this write sets re-opens the
// evaluation, which restarts cleanly at page 1. Which charge is now
// oldest-and-open, whether the account is square, and whether a reminder is
// due are all functions of the whole history, and that recomputation belongs
// to EvaluateWellnessArrears (accountDDLScript), which the stale mark asks
// for. The write is a BARE update auto-conditioned on the revision the key
// hydrated at (Contract #3 §3.2), and this script's own derive_reads is what
// guarantees the key is hydrated whatever the submitter declared — so
// concurrent entries against one account serialize on it and retry rather
// than dropping a mark.
const transactionDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_update(vtx_key, local_name, cls, data):
    # Deliberately NO expectedRevision here — leaving it unset is what makes
    # this update RETRY-eligible, not less safe. Contract #3 §3.2 in
    # commit_path.go's applyHydratedRevisions auto-conditions any bare update
    # on a key the op declared in reads/optionalReads (so still safe, still
    # OCC-guarded) using the step-4 hydrated revision, and marks it
    # defaulted — the retry-eligible set. An update that supplies its OWN
    # expectedRevision instead is treated as an explicit-caller compensating
    # assertion and is EXCLUDED from that retry ("never overridden") — it
    # hard-conflicts instead of serializing, which is the opposite of what a
    # maintained mark two ops can race on needs.
    #
    # It is also the reviving verb for a TOMBSTONED aspect. A create against a
    # tombstone is refused (Contract #3 §3.3), so post_entry's .arrears write
    # comes here for any present key and the auto-conditioning above pins the
    # document's own revision, so the write races nothing.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def carry_arrears(doc):
    # Every field of the account's recorded arrears state, copied forward,
    # with TWO exceptions. The mark post_entry adds is an ADDITION to that
    # state, never a rewrite of it: the send record (remindedFor, sentAt) is
    # what stops a second notification going out for an episode already
    # reminded for, and dropping it while marking the state stale would send
    # twice for one debt.
    #
    # historyTooLong (with the historyBudget recorded beside it) is the first
    # exception, and only this script drops it. It records that an evaluation
    # could not read the account's history inside its replay budget, and the
    # lens holds the row QUIET while it stands — no gap, no timer. Carrying it
    # across a posted entry would make that quiet permanent for the life of
    # the account. Dropping it here is what buys exactly one more attempt per
    # entry: this write also sets stale, so the gap re-opens once, the
    # evaluation runs once, and if the history is still too long it records
    # the mark again and the row goes quiet again. One op per entry, never a
    # loop.
    #
    # replay is the second: the checkpoint of an evaluation part-way through
    # the account's postedTo history — the pages consumed, the cursor, the
    # running aggregate. This entry changes the enumerated set under that
    # cursor, so the checkpoint no longer describes a prefix of the history
    # and is dropped; the stale mark this write sets re-opens the evaluation
    # gap, and the next evaluation starts again at page 1. Every posted entry
    # drops it the same way — this ledger has only the one post_entry branch
    # (mark stale, carry forward), unlike clinic-ledger's four .balance-driven
    # shapes, so there is no separate no-write shape that would otherwise let
    # a stale checkpoint survive a posted entry.
    out = {}
    for k, v in doc.data.items():
        if k == "historyTooLong" or k == "historyBudget" or k == "replay":
            continue
        out[k] = v
    return out

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_wellnessaccount_key(key):
    # Contract #1's whole vertex grammar for a wellnessaccount, not a prefix
    # test — derive_reads returns keys the Processor validates against that
    # grammar and answers a malformed one with a DeriveReadsInvalid hydration
    # fault raised BEFORE the operation's own validation runs, which would
    # turn post_entry's clean "InvalidArgument: accountKey" into an opaque
    # hydration failure.
    if key == None or type(key) != type(""):
        return False
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "wellnessaccount":
        return False
    if len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

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
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def require_number(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        fail("InvalidArgument: " + name + ": required number")
    return v

def parts_of(key, name, want_type):
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

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

# Self-credit balance-verification budget (post_entry's authContextTarget
# branch), mirroring clinic-ledger's identical constants: 10 pages of 50
# postedTo entries covers many years of a billing history; an account that
# exceeds it fails the self-credit closed rather than trust a partial sum.
SELF_CREDIT_PAGE_LIMIT = 50
SELF_CREDIT_MAX_PAGES = 10

def require_not_own_account(acct_key, verb):
    # Nobody clears their own debt from the desk. The staff leg proves STANDING
    # (the frontOfHouse / operator grant) and nothing about ownership, so a
    # staffer who is also a member passes it against their own account. This is
    # the other half: the account's holder is resolved off its OWN heldFor
    # link (never the payload) and compared with the actor, and a match refuses
    # the clearing verb. The self leg's ownership proof reads the same link to
    # prove the account IS the caller's; this proves it is NOT.
    #
    # The operator is NOT exempt, on purpose: every other guard on this leg is
    # about standing, and root has all of it, but this one is about whose money
    # it is. An operator who is a member is a member. Another staffer clears
    # it. A Weaver-dispatched refund (wellnessRefundSettlement) passes on the
    # same terms as any staffer -- the dispatch actor holds no account.
    # read-posture: (e) relation=heldFor epoch=none -- an account carries
    # exactly one heldFor link, so this is never a keyspace scan.
    held_for_page, _ = kv.Links(acct_key, "heldFor", "out", None, 1)
    for lk in held_for_page:
        if not lk.isDeleted and lk.targetVertex == op.actor:
            # No account key in the text: it is toasted verbatim at the
            # staffer, and a raw vtx key tells them nothing the rule does not.
            fail("SelfClearing: a staffer may not " + verb + " their own account — another staffer must")

def post_entry(state, op, entry_type, event_class, allow_booking_ref, allow_refund_ref):
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "wellnessaccount")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)

    booking_key = None
    booking_id = None
    price_booking_key = None
    price_booking_id = None
    if allow_booking_ref:
        booking_key = optional_string(p, "bookingRef")
        if booking_key != None:
            _, booking_id = parts_of(booking_key, "bookingRef", "booking")
            if not vertex_alive(state, booking_key):
                fail("UnknownBooking: " + booking_key)

        # priceBookingRef is independent of bookingRef — a WellnessDebitAccount may
        # carry either, both, or neither. Same validation shape, a DISTINCT
        # settlesClassPrice link below so the no-show and class-price
        # settlement gaps never collide in a count().
        price_booking_key = optional_string(p, "priceBookingRef")
        if price_booking_key != None:
            _, price_booking_id = parts_of(price_booking_key, "priceBookingRef", "booking")
            if not vertex_alive(state, price_booking_key):
                fail("UnknownBooking: " + price_booking_key)

    # refundRef (WellnessCreditAccount only, the mirror of bookingRef/
    # priceBookingRef above being WellnessDebitAccount only): the vertex it
    # names is a wellnessrefund marker (wellness-domain's CancelBooking,
    # ddls.go), not a booking — a cancelled booking is tombstoned by the
    # time any refund posts, so validating alive against class=booking here
    # would always UnknownBooking a genuine refund.
    refund_key = None
    refund_id = None
    if allow_refund_ref:
        refund_key = optional_string(p, "refundRef")
        if refund_key != None:
            _, refund_id = parts_of(refund_key, "refundRef", "wellnessrefund")
            if not vertex_alive(state, refund_key):
                fail("UnknownRefund: " + refund_key)

    amount_cents = require_number(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    memo = optional_string(p, "memo")

    # A manual charge -- a debit carrying no settlement back-reference -- must
    # say what it is for. The ledger is append-only, so a memo-less charge can
    # never be explained afterwards: the .entry aspect is written once, here,
    # and nothing mutates it.
    # Weaver-dispatched settlements are exempt: each carries bookingRef (the
    # no-show fee) or priceBookingRef (the class price), which the
    # wellnessLedgerHistory lens resolves to the class name on its own, and
    # their memo Param may hydrate to null (targets.go passes row.memo /
    # row.sessionName). refundRef rides a credit, never a debit (targets.go),
    # so a debit is manual exactly when both booking refs are absent.
    if entry_type == "debit" and memo == None and booking_key == None and price_booking_key == None:
        fail("InvalidArgument: a manual charge needs a memo describing it")

    # reason distinguishes a credit that is cash actually collected from one
    # that forgives debt (a waived no-show fee) or that returns money already
    # collected (a settled class price or no-show fee reversed by
    # wellnessRefundSettlement) -- all three reduce the derived balance
    # identically (owed_cents -= amount_cents above, and the ledgerHistory
    # lens's sum(debits)-sum(credits)), but the lens projects reason so a
    # reader never mistakes forgiven debt or a refund for money freshly
    # received. Credit-only, mirroring clinic-ledger's post_entry.
    reason = optional_string(p, "reason")
    if entry_type == "credit":
        if reason == None:
            reason = "payment"
        if reason != "payment" and reason != "waiver" and reason != "refund":
            fail("InvalidArgument: reason: must be \"payment\", \"waiver\", or \"refund\", got " + reason)
    elif reason != None:
        fail("InvalidArgument: reason: only valid on a credit (payment/waiver/refund), not a debit (charge)")

    # Member-self ownership + amount trust (WellnessCreditAccount only —
    # permissions.go grants no self-scope WellnessDebitAccount), mirroring
    # clinic-ledger's post_entry. The mere PRESENCE of authContextTarget
    # selects this branch, same idiom as ClinicCreditAccount/CreateBooking —
    # it does not change what grant actually authorized the op (a scope=any
    # operator/frontOfHouse submit never attaches a target), it only ever
    # narrows behavior.
    # authcontext-target: (selector) a branch selector, not a confinement
    # exemption -- so it reads the raw target (did the caller declare a self
    # target at all) rather than authTargetValidated. Safe because presence
    # only pushes the caller onto the STRICTER branch below (the ownership +
    # amount proofs), never grants anything a scope=any submit would not
    # already.
    if op.authContextTarget != "":
        if entry_type != "credit":
            fail("AuthDenied: a member may only credit (pay down) their own account, not charge it")
        if reason == "waiver":
            fail("AuthDenied: a member may only pay down their own account, not waive a charge")
        if reason == "refund":
            fail("AuthDenied: a member may only pay down their own account, not refund a charge — refunds are staff/automation only")
        # authcontext-target: (ownership) unlike clinic-ledger's account→
        # patient→identifiedBy chain, a wellnessaccount's heldFor link
        # targets the IDENTITY directly (accountDDLScript above), so
        # ownership is a single link compare, no follow-up identifiedBy
        # read needed. The identity is recovered from the account's OWN
        # heldFor topology, never the payload, so a forged target only
        # fails closed.
        # read-posture: (e) relation=heldFor epoch=none -- an account carries
        # exactly one heldFor link, so this is never a keyspace scan.
        held_for_page, _ = kv.Links(acct_key, "heldFor", "out", None, 1)
        held_identity_key = None
        for lk in held_for_page:
            if not lk.isDeleted:
                held_identity_key = lk.targetVertex
        if held_identity_key == None:
            fail("AuthDenied: account " + acct_key + " carries no live identity")
        if held_identity_key != op.authContextTarget:
            fail("AuthDenied: a member may only pay down their own account")

        # Amount trust: nothing on this platform verifies a self-submitted
        # payment actually happened (no payment-rail integration — out of
        # scope for a reference vertical), so an unbounded self-credit would
        # let a member forgive their own debt for free. The outstanding
        # balance is recomputed from the account's OWN postedTo transaction
        # history (never trusted from the payload), paginated + bounded: an
        # account whose history exhausts the page budget fails closed
        # (denies) rather than trusts a partial sum. A self-credit may never
        # exceed what is actually owed.
        owed_cents = 0
        cursor = None
        budget_exhausted = True
        for _page in range(SELF_CREDIT_MAX_PAGES):
            # read-posture: (e) relation=postedTo epoch=none -- bounded by the
            # page budget; exhausting it below fails closed.
            page, cursor = kv.Links(acct_key, "postedTo", "in", cursor, SELF_CREDIT_PAGE_LIMIT)
            for lk in page:
                if lk.isDeleted:
                    continue
                # read-posture: (e) per-candidate follow-up read off the
                # enumeration above -- each transaction's own .entry aspect,
                # data-derived and unknowable client-side.
                tx_entry = kv.Read(lk.sourceVertex + ".entry")
                if tx_entry == None or tx_entry.isDeleted:
                    continue
                tx_amount = tx_entry.data.get("amountCents")
                if tx_amount == None:
                    continue
                if tx_entry.data.get("type") == "debit":
                    owed_cents += tx_amount
                elif tx_entry.data.get("type") == "credit":
                    owed_cents -= tx_amount
            if cursor == None:
                budget_exhausted = False
                break
        if budget_exhausted:
            fail("AuthDenied: could not verify account " + acct_key + "'s balance (too much transaction history)")
        if owed_cents <= 0:
            fail("NoBalanceToPay: account " + acct_key + " has no outstanding balance to pay")
        if amount_cents > owed_cents:
            fail("PaymentExceedsBalance: amountCents exceeds account " + acct_key + "'s outstanding balance of " + str(owed_cents))
    elif entry_type == "credit":
        # The staff leg's ownership check, the mirror image of the self leg's
        # above. The two verbs that give the studio's money up -- a waiver
        # (debt forgiven) and a refund (money given back) -- are refused on
        # the actor's own account. A payment credit records money coming IN
        # and is untouched, as is every charge.
        if reason == "waiver":
            require_not_own_account(acct_key, "forgive")
        elif reason == "refund":
            require_not_own_account(acct_key, "refund")

    tx_id = nanoid.new()
    tx_key = "vtx.wellnesstransaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)

    entry_data = {"type": entry_type, "amountCents": amount_cents, "postedAt": posted_at}
    if memo != None:
        entry_data["memo"] = memo
    if entry_type == "credit":
        entry_data["reason"] = reason

    # postedTo: the transaction (later-arriving) is the source, the
    # pre-existing account is the target (Contract #1 §1.1). Reads as
    # "this transaction posted to this account."
    posted_to_lnk = "lnk.wellnesstransaction." + tx_id + ".postedTo.wellnessaccount." + acct_id

    # Root data minimal (D5): {} on root. The charge/payment fact is the
    # .entry aspect; the account itself is untouched (append-only ledger).
    mutations = [
        make_vtx(tx_key, "wellnesstransaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]

    # settles: the transaction (later-arriving) is the source, the
    # pre-existing booking is the target (Contract #1 §1.1). Only written
    # when the caller supplied bookingRef — a plain human-submitted
    # WellnessDebitAccount is unaffected. The wellnessNoShowSettlement lens walks
    # this link to converge the no-show-fee gap once posted.
    if booking_key != None:
        settles_lnk = "lnk.wellnesstransaction." + tx_id + ".settles.booking." + booking_id
        mutations.append(make_link(settles_lnk, tx_key, booking_key, "settles", "settles", {}))

    # settlesClassPrice: the transaction (later-arriving) is the source, the
    # pre-existing booking is the target (Contract #1 §1.1). A DISTINCT
    # relation from settles — only written when the caller supplied
    # priceBookingRef, independent of bookingRef/settles above. The
    # wellnessClassPriceSettlement lens walks this link to converge the
    # class-price gap once posted.
    if price_booking_key != None:
        settles_price_lnk = "lnk.wellnesstransaction." + tx_id + ".settlesClassPrice.booking." + price_booking_id
        mutations.append(make_link(settles_price_lnk, tx_key, price_booking_key, "settlesClassPrice", "settlesClassPrice", {}))

    # settlesRefund: the transaction (later-arriving) is the source, the
    # pre-existing wellnessrefund marker is the target (Contract #1 §1.1).
    # Only written when the caller supplied refundRef. The
    # wellnessRefundSettlement lens walks this link to converge the refund
    # gap once posted, mirroring settlesClassPrice's exact shape.
    if refund_key != None:
        settles_refund_lnk = "lnk.wellnesstransaction." + tx_id + ".settlesRefund.wellnessrefund." + refund_id
        mutations.append(make_link(settles_refund_lnk, tx_key, refund_key, "settlesRefund", "settlesRefund", {}))

    # The account's .arrears episode state (class wellnessAccountArrears,
    # ddls.go). This ledger stores no balance, so a posted entry cannot tell an
    # episode opening from one continuing, or a clearing payment from a
    # partial one. It does the one thing it can: mark what already EXISTS
    # stale — a request for EvaluateWellnessArrears to recompute the head
    # (and whether there still is one) from the account's own history — and
    # mint nothing where nothing exists, since an account with no arrears
    # state is already opening the never-evaluated gap. Every debit and every
    # credit lands here alike: a charge behind an older head changes nothing
    # the head reads, but this op cannot know it is behind one.
    arrears_key = acct_key + ".arrears"
    # read-posture: (d) optionalReads — derived server-side by this script's own
    # derive_reads(op) for both entry ops (Contract #2 §2.5 class (g)), and
    # declared statically by opmetas.go's OpDispatchSpec.OptionalReads + this
    # package's own targets.go GapActionSpec.OptionalReads. Absence-tolerant:
    # no account carries .arrears until an evaluation has run on it.
    arrears_doc = kv.Read(arrears_key)
    if arrears_doc != None and not arrears_doc.isDeleted:
        # The CLASS, not just the key: this package is the sole writer of a
        # .arrears aspect and writes exactly that class, so a document of any
        # other class here is a fault to refuse, never state to carry.
        if not hasattr(arrears_doc, "class") or getattr(arrears_doc, "class") != "wellnessAccountArrears":
            fail("InvalidState: this account's arrears aspect is not a wellnessAccountArrears")
        arrears_data = carry_arrears(arrears_doc)
        arrears_data["stale"] = True
        mutations.append(make_aspect_update(acct_key, "arrears", "wellnessAccountArrears", arrears_data))

    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def derive_reads(op):
    # Contract #2 §2.5 class (g): the keys post_entry's .arrears write depends
    # on, returned server-side for EVERY dispatch of the two entry ops,
    # whatever the submitter declared. The write is a bare update that is
    # only auto-conditioned on the step-4 hydrated revision (Contract #3
    # §3.2) for a key that WAS hydrated — a submitter that omitted the
    # declaration would get a live read and an UNCONDITIONED update, so two
    # concurrent entries against one account could each carry the same prior
    # state and one mark would be lost. A guard a caller can switch off by
    # not mentioning it is not a guard, and contextHint is caller-supplied
    # and never enforced — hence this, the channel the platform owns. The
    # dispatchers' own static declarations (opmetas.go, targets.go) stay:
    # they document the read set, this guarantees it.
    #
    # optionalReads, never reads: no account carries .arrears until an
    # evaluation has run on it, and a required read's absence is a
    # HydrationMiss that would block every entry against such an account.
    #
    # The account ROOT itself rides the same declaration too, for a distinct
    # reason: post_entry's vertex_alive(state, acct_key) decides
    # UnknownAccount by testing acct_key not in state, which cannot tell
    # "genuinely absent" from "never declared or derived" apart. Every
    # dispatcher's own static declaration already names the root too, but
    # that is a hint a caller may ignore, not an enforcement.
    #
    # The op argument is a struct -- op.operationType, op.actor, op.payload
    # (also a struct). No kv, no nanoid: both are fail-closed stubs in this
    # pass, and a derivation that reads state is a read, not a derivation.
    ot = op.operationType
    if ot != "WellnessDebitAccount" and ot != "WellnessCreditAccount":
        return {}
    # optional_string, never required_string: a missing or malformed
    # accountKey derives nothing rather than faulting the pre-pass --
    # post_entry's own required_string/parts_of still raise the real
    # InvalidArgument.
    acct_key = optional_string(op.payload, "accountKey")
    if not is_wellnessaccount_key(acct_key):
        return {}
    return {"optionalReads": [acct_key, acct_key + ".arrears"]}

def execute(state, op):
    ot = op.operationType

    if ot == "WellnessDebitAccount":
        # workplace-exempt: (ownership-bound) post_entry's own authContextTarget
        # branch fails closed for a debit (permissions.go grants no self-scope
        # WellnessDebitAccount, so authContextTarget is never legitimately set here).
        return post_entry(state, op, "debit", "account.debited", True, False)

    if ot == "WellnessCreditAccount":
        # workplace-exempt: (ownership-bound) post_entry proves ownership itself --
        # a self-scoped credit is allowed only once the account's own heldFor
        # link resolves to op.authContextTarget.
        return post_entry(state, op, "credit", "account.credited", False, True)

    fail("transaction DDL: unknown operationType: " + ot)
`
