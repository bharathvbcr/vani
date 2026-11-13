package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/decide"
)

// outageReading reports whether rd says no backend read the slot: it was not
// reachable (Unavailable) or was not asked (NotAsked). It is a positive check
// on exactly these two. Failed is NOT an outage: a backend read the posting and
// produced unusable output (unparseable reply, a value outside the options), so
// that is a reading about this text on this model, and asking again every run
// would only repeat it, leave the posting Live and invisible, and feed the
// unread streak. A Failed slot stays what it was before outages were retried:
// DropPolicy.Evaluate marks the posting Unsure, which is visible. Answered and
// Abstained are real readings, Refused is a backend's settled "not this
// question", and a value this build does not know is never guessed at; none of
// those is an outage.
func outageReading(rd decide.Reading) bool {
	return rd == decide.Unavailable || rd == decide.NotAsked
}

// Unread reports whether r was triaged without any backend reading it: it has
// at least one "triage.<slot>.reading" annotation and every one of them is an
// outage reading ("unavailable" or "not_asked"). Such a record carries no
// decision, only an outage. One real reading, a refusal, a failed (unusable)
// reading or a value that is not a known reading makes it not unread.
func Unread(r ledger.Record) bool {
	n := 0
	for k, v := range r.Annotations {
		if !strings.HasPrefix(k, ledger.AnnTriagePrefix) || !strings.HasSuffix(k, ledger.AnnTriageSuffixReading) {
			continue
		}
		n++
		if !outageReading(decide.Reading(v)) {
			return false
		}
	}
	return n > 0
}

// retriageable are the statuses a record can reach after an unread triage
// without anyone having seen it: unsure, then scored and explained. A digested,
// dropped, dead or duplicate record is left alone.
var retriageable = map[posting.Status]bool{posting.Unsure: true, posting.Triaged: true, posting.Scored: true, posting.Explained: true}

// RetriageUnread returns every record that was triaged while no backend could
// read it (before ErrTriageUnavailable kept such postings Live) to Live, so the
// next run triages it. It forgets the triage, scoring and explanation
// annotations built on that non-answer and keeps everything else (badges,
// description hash). The ledger is append-only, so the old events remain as
// history. With dryRun it only reports. Keys are in ledger order.
func RetriageUnread(ctx context.Context, l ledger.Ledger, now time.Time, dryRun bool) ([]posting.Key, error) {
	return reopen(ctx, l, now, dryRun, "retriage: no decision backend could read it", func(r ledger.Record) bool {
		return retriageable[r.Status] && Unread(r)
	})
}

// TitleDropped reports whether r was dropped on its title alone, with no
// decision backend asked (DropPolicy.EvaluateTitle).
func TitleDropped(r ledger.Record) bool {
	return r.Status == posting.Dropped && r.Annotations[ledger.AnnTriagePrefix+"backend"] == TitleBackend
}

// RetriageTitleDrops returns to Live every record dropped on its title that p
// would no longer drop on its title: a phrase list or drop rule changed, or
// title readings were switched off. The next run triages it with the
// backends. A record p still drops is left alone, since the next run would
// only drop it again, and so is one sc does not let back (an unwatched board,
// an unreadable body; see ReopenScope). Annotations are forgotten as
// RetriageUnread forgets them. With dryRun it only reports. Keys are in ledger
// order. It is the title class of RetriagePolicyDrops; a caller that needs the
// stale and unreadable counts calls that with TitleOnly.
func RetriageTitleDrops(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, sc ReopenScope, dryRun bool) ([]posting.Key, error) {
	sc.TitleOnly = true
	got, err := RetriagePolicyDrops(ctx, l, now, p, sc, dryRun)
	return got.Title, err
}

const (
	reasonTitleDrop   = "retriage: the title rules no longer drop it"
	reasonModelDrop   = "retriage: the drop rules no longer drop its reading"
	reasonFreshDrop   = "retriage: the drop rules no longer drop its reading, but its title, location or description moved since it was read"
	reasonRefreshDrop = "retriage: its reading cannot be judged and no drop rule names its values; read again"
)

// ReopenScope is what a drop reopen needs besides the drop policy: what the
// current triage question is, and which boards are still listed.
type ReopenScope struct {
	// ModelID is the id fresh triage records readings under (triage.model):
	// the models, routing and question context (cmd/vani triageModelID). A
	// model drop's stored reading is judged only when it was made under this
	// id, so it is exactly the reading a fresh triage would make or inherit
	// now. Empty judges no model drop.
	ModelID string
	// Options are the legal values of each slot the current question asks.
	// A stored reading with an answered slot not named here, or a value not
	// among its slot's options (a renamed option), is not judged. Nil judges
	// no model drop.
	Options map[string][]string
	// Watched reports whether a key belongs to a board on the current
	// watchlist. A dropped posting is closed, so no listing ages it: one on a
	// board that is no longer listed would stay advanceable for good if it
	// were reopened. It is counted stale instead. Nil watches nothing, so
	// nothing is reopened: a check that could not run is not a pass.
	Watched func(posting.Key) bool
	// TitleOnly reopens title drops alone (RetriageTitleDrops): model drops
	// are neither reopened nor counted.
	TitleOnly bool
	// Defer names the keys whose reopen waits for their board's next full
	// listing to show them (a pending reopen, kept in Pending and applied by
	// Reconciler.ReopenOnSighting): a dropped posting is closed, so the ledger
	// cannot say whether its board still lists it. The retriage command
	// defers every watched board's drops. Nil defers nothing: a key is
	// reopened at once, as one with no full listing to wait for (an inbox
	// page posting) always is.
	Defer func(posting.Key) bool
	// Pending is where deferred reopens are kept. A scope with Defer and no
	// Pending is refused.
	Pending *PendingReopens
	// RefreshUnjudged sends an unjudged model drop back to Live for a fresh
	// read when no current drop rule names a value of its stored reading
	// (were the reading trusted, nothing would drop it). One a rule would
	// still drop on its value stays dropped and counted. At most
	// MaxRefreshUnjudged per call.
	RefreshUnjudged bool
}

// reachable reports whether a reopened k would be in a listing's care: on a
// watched board, whose next listing ages it, or weakly keyed (an inbox page
// posting or a fallback key), which no listing ages while live either. A
// scope whose Watched is nil reaches nothing: a check that could not run is
// not a pass.
func (sc ReopenScope) reachable(k posting.Key) bool {
	return sc.Watched != nil && (sc.Watched(k) || weakKey(k))
}

// deferred reports whether k's reopen waits for a sighting.
func (sc ReopenScope) deferred(k posting.Key) bool { return sc.Defer != nil && sc.Defer(k) }

// WatchedBy is the ReopenScope.Watched of a watchlist: a key is watched when
// it falls under one of the boards' key prefixes, the same test the absence
// pass ages a board's records by (Reconcile, ledgerIndex.board), so a
// reopened posting is one the next listing of its board can age. A
// fallback-keyed posting belongs to no board's prefix and is never watched.
func WatchedBy(boards []sources.Board) func(posting.Key) bool {
	prefixes := make([]string, 0, len(boards))
	for _, b := range boards {
		if p := b.KeyPrefix(); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	return func(k posting.Key) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(string(k), p) {
				return true
			}
		}
		return false
	}
}

// PolicyReopen is what RetriagePolicyDrops returned (or, in a dry run, would
// return), by the class of the drop, and what it left dropped and why. The
// class lists hold every posting let back, reopened now or deferred (see
// Deferred).
type PolicyReopen struct {
	// Title were dropped on their title alone. They go back to Live, since no
	// backend read them, and the next run asks the backends.
	Title []posting.Key
	// Model were dropped on a decision backend's calibrated reading that the
	// current rules no longer drop. They go back on that reading, to Triaged
	// or Unsure as DropPolicy.Evaluate now says, and the next run scores them
	// without asking a backend again.
	Model []posting.Key
	// Fresh are model drops the current rules no longer drop whose title,
	// location or description moved since the reading (triage.*_sha256): the
	// reading is of other text, so they go back to Live for a fresh read.
	Fresh []posting.Key
	// Refresh are unjudged model drops sent for a fresh read
	// (ReopenScope.RefreshUnjudged): back to Live.
	Refresh []posting.Key
	// Deferred are the ones among those that wait, still dropped, for their
	// board to list them again (ReopenScope.Defer).
	Deferred []posting.Key
	// AlreadyPending counts drops already waiting with the same class; they
	// are not marked again.
	AlreadyPending int
	// OverCap counts drops left out because a bound was reached: the pending
	// set's (MaxPendingReopens) or the fresh reads' (MaxRefreshUnjudged).
	OverCap int
	// Unjudged counts model drops the current policy cannot judge (see
	// ReopenScope.judged): the reading was made under another model id or
	// question, or names a slot or value today's question does not ask, or a
	// slot carries a calibration from a backend the policy cannot read, or no
	// slot carries one it can. They stay dropped, and are counted so that
	// "nothing to return" is never read as "every drop was checked".
	Unjudged int
	// Stale counts drops the current rules would let back that sit on a
	// board no longer watched (or one Watched could not place). They stay
	// dropped.
	Stale int
	// Unreadable counts drops the current rules would let back whose stored
	// description cannot be read back from the ledger. Reopening needs it
	// (and an exclusion may read it), so they stay dropped; they do not stop
	// the others.
	Unreadable int
}

// Returned is how many postings were let back, now or deferred (or would
// be, in a dry run).
func (r PolicyReopen) Returned() int {
	return len(r.Title) + len(r.Model) + len(r.Fresh) + len(r.Refresh)
}

// Reopened is how many of them came back at once (or would, in a dry run).
func (r PolicyReopen) Reopened() int { return r.Returned() - len(r.Deferred) }

// RetriagePolicyDrops returns every dropped posting that p would no longer
// drop, title drops and model drops alike: a drop rule lost a value or a slot,
// a threshold rose past the stored reading, or title readings changed. It
// never drops anything: a rule that tightened applies only to readings the
// pipeline makes from now on (Rule 7). An exclusion drop is ApplyExclusions'
// to reopen, and a posting the current exclusions or title rules would drop
// before asking a backend stays dropped. sc bounds what may come back. With
// dryRun it only reports, through the same classification. Keys are in
// ledger order.
func RetriagePolicyDrops(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, sc ReopenScope, dryRun bool) (PolicyReopen, error) {
	var out PolicyReopen
	if sc.Defer != nil && sc.Pending == nil {
		return out, errors.New("pipeline: a deferring reopen needs somewhere to keep its pending reopens")
	}
	class := map[posting.Key]dropClass{}
	refreshLeft := MaxRefreshUnjudged
	added := 0 // pending reopens this call adds (a dry run adds none to Pending)
	add := func(k posting.Key, c dropClass) {
		switch c {
		case dropTitle:
			out.Title = append(out.Title, k)
		case dropModel:
			out.Model = append(out.Model, k)
		case dropFresh:
			out.Fresh = append(out.Fresh, k)
		case dropRefresh:
			out.Refresh = append(out.Refresh, k)
		}
	}
	keys, unreadable, err := reopenEach(ctx, l, dryRun, func(r ledger.Record) (ledger.Event, bool) {
		c := planPolicyDrop(l, r, p, sc)
		switch c {
		case dropTitle, dropModel, dropFresh, dropRefresh:
		case dropUnjudged:
			out.Unjudged++
			return ledger.Event{}, false
		case dropStale:
			out.Stale++
			return ledger.Event{}, false
		case dropUnreadable:
			out.Unreadable++
			return ledger.Event{}, false
		default:
			return ledger.Event{}, false
		}
		if sc.deferred(r.Key) {
			name := reopenClassOf(c)
			if prev, ok := sc.Pending.Items[r.Key]; ok && prev.Class == name {
				out.AlreadyPending++
				return ledger.Event{}, false
			}
			if _, ok := sc.Pending.Items[r.Key]; !ok && sc.Pending.Len()+added >= MaxPendingReopens {
				out.OverCap++
				return ledger.Event{}, false
			}
			if c == dropRefresh {
				if refreshLeft == 0 {
					out.OverCap++
					return ledger.Event{}, false
				}
				refreshLeft--
			}
			add(r.Key, c)
			out.Deferred = append(out.Deferred, r.Key)
			if dryRun {
				added++
			} else {
				sc.Pending.Items[r.Key] = PendingReopen{Class: name, At: now.UTC(), Reason: reasonOf(c)}
			}
			return ledger.Event{}, false
		}
		if c == dropRefresh {
			if refreshLeft == 0 {
				out.OverCap++
				return ledger.Event{}, false
			}
			refreshLeft--
		}
		class[r.Key] = c
		switch c {
		case dropModel:
			return keepReadingEvent(r, p, now), true
		default:
			return reopenEvent(r, now, reasonOf(c)), true
		}
	})
	out.Unreadable += unreadable
	for _, k := range keys {
		add(k, class[k])
	}
	return out, err
}

// reasonOf is the ledger reason of a reopen of class c.
func reasonOf(c dropClass) string {
	switch c {
	case dropTitle:
		return reasonTitleDrop
	case dropFresh:
		return reasonFreshDrop
	case dropRefresh:
		return reasonRefreshDrop
	}
	return reasonModelDrop
}

// dropClass is what planPolicyDrop makes of one record.
type dropClass int

const (
	dropStays      dropClass = iota // not dropped, or still dropped under p, or not this function's to reopen
	dropTitle                       // a title drop p no longer makes
	dropModel                       // a model drop whose stored reading p no longer drops
	dropUnjudged                    // a model drop whose stored reading p cannot judge
	dropStale                       // p would let it back, but its board is not watched
	dropUnreadable                  // p would let it back, but its stored body cannot be read
	dropFresh                       // a model drop p no longer drops whose text moved since its reading
	dropRefresh                     // an unjudged model drop no rule names a value of, read again
)

// planPolicyDrop classifies r under p and sc. It is the one selector for both
// kinds of drop, for a dry run and a real one alike.
func planPolicyDrop(l ledger.Ledger, r ledger.Record, p DropPolicy, sc ReopenScope) dropClass {
	if r.Status != posting.Dropped {
		return dropStays
	}
	backend := r.Annotations[ledger.AnnTriagePrefix+"backend"]
	var c dropClass
	switch {
	case backend == TitleBackend:
		// l.All() hands a dropped record back without its body, and a staff
		// reading is set aside when the description states fewer than staff
		// years: judged on an empty description it would stay dropped for
		// ever. Read the body (one bounded read) only when the verdict can
		// depend on it; one that cannot be read back is not judged.
		tr := r
		if r.BodyEvicted && p.titleReadsStaff(r.Posting.Title) {
			if tr = ledger.Full(l, r); tr.BodyEvicted {
				return dropStays
			}
		}
		if _, _, still := p.EvaluateTitle(tr.Posting.Title, tr.Posting.Description); still {
			return dropStays
		}
		c = dropTitle
	case backend == ExcludeBackend || r.Annotations[AnnExcluded] != "":
		return dropStays // ApplyExclusions owns these
	case sc.TitleOnly:
		return dropStays
	default:
		switch {
		case sc.judged(p, r.Annotations):
			if st, _ := p.Evaluate(answerFromAnnotations(r.Annotations)); st == posting.Dropped {
				return dropStays
			}
			c = dropModel
		case sc.RefreshUnjudged && !p.namesAValueOf(answerFromAnnotations(r.Annotations)):
			c = dropRefresh
		default:
			return dropUnjudged
		}
	}
	// Reopening to a status that keeps its body needs the body back, and an
	// exclusion may read it; a dropped record keeps it out of memory. One that
	// cannot be read back is never judged on an empty description: a deferred
	// reopen is judged at its sighting, on the listed text; any other stays
	// dropped and is counted.
	full := ledger.Full(l, r)
	if full.BodyEvicted {
		switch {
		case !sc.reachable(r.Key):
			return dropStale
		case sc.deferred(r.Key):
			return c
		}
		return dropUnreadable
	}
	// A fresh triage applies the exclusions and title rules before any
	// backend: what they drop now stays dropped, title drops included.
	if _, _, dropped := p.EvaluatePosting(full.Posting); dropped {
		return dropStays
	}
	// Only now, with the rules agreed it may come back, does its board count:
	// a drop the rules still hold is not stale.
	if !sc.reachable(r.Key) {
		return dropStale
	}
	if c == dropModel && textMoved(full.Posting, r.Annotations) {
		c = dropFresh
	}
	return c
}

// textMoved reports whether a reading's bound text (triage.*_sha256, written
// on the event of a triage that asked the backends) differs from the
// posting's now. A binding that is absent (a copied reading, or one made
// before readings were bound) says nothing, and is not a move.
func textMoved(p posting.Posting, ann map[string]string) bool {
	for k, v := range map[string]string{
		ledger.AnnTriageDescSHA256:     p.Description,
		ledger.AnnTriageTitleSHA256:    p.Title,
		ledger.AnnTriageLocationSHA256: p.Location,
	} {
		if h := ann[k]; h != "" && h != posting.RawSHA256(v) {
			return true
		}
	}
	return false
}

// namesAValueOf reports whether a drop rule that can drop (a threshold set)
// names the value a's reading gives its slot, whatever the calibration: were
// the reading trusted, the rule could drop it. An unjudged reading is sent for
// a fresh read only when no rule does.
func (p DropPolicy) namesAValueOf(a decide.Answer) bool {
	for _, rule := range p.Rules {
		if rule.MinOpenRouterProb <= 0 && rule.MinLappiScore <= 0 {
			continue
		}
		if sr, ok := a.Slots[rule.Slot]; ok && sr.Reading == decide.Answered && contains(rule.Values, sr.Value) {
			return true
		}
	}
	return false
}

// judged reports whether a model drop's stored reading (its triage
// annotations) is one the current policy may re-judge, so that a reopen is
// the rules' doing and never the reading's: it was made under sc.ModelID;
// every answered slot is one today's question asks, with a value it still
// offers; every answered slot that carries a calibration carries one p can
// read (a probability from the configured OpenRouter model, a Lappi score);
// and at least one does. A calibration p cannot read is from a backend that
// could have made the drop (the decision model under an old name), so the
// whole reading is unjudged, whatever another slot carries. A slot with no
// calibration (an LLM answer) never dropped anything and is left to Evaluate.
func (sc ReopenScope) judged(p DropPolicy, ann map[string]string) bool {
	if sc.ModelID == "" || ann[AnnTriageModel] != sc.ModelID || sc.Options == nil {
		return false
	}
	a := answerFromAnnotations(ann)
	readable := false
	for slot, sr := range a.Slots {
		if sr.Reading != decide.Answered {
			continue
		}
		if !contains(sc.Options[slot], sr.Value) {
			return false
		}
		if sr.Jev == nil && sr.Lappi == nil {
			continue
		}
		switch b := slotBackend(a, sr); {
		case p.OpenRouter != "" && b == p.OpenRouter && sr.Jev != nil:
			readable = true
		case b == "lappi" && sr.Lappi != nil:
			readable = true
		default:
			return false
		}
	}
	return readable
}

// keepReadingEvent returns a model-dropped r to the status p now gives its
// stored reading, keeping the reading, its calibration and the badges. The
// reading is still the backend's word on this text (a changed description
// would have reopened it already), so it is not asked again.
func keepReadingEvent(r ledger.Record, p DropPolicy, now time.Time) ledger.Event {
	st, why := p.Evaluate(answerFromAnnotations(r.Annotations))
	ann := map[string]string{}
	if st == posting.Unsure {
		ann[ledger.AnnTriageUnsure] = why
	}
	return ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: st, Annotations: ann,
		Reason: reasonModelDrop + " (" + why + ")"}
}

// reopen returns each record match selects to Live, forgetting the triage,
// scoring and explanation annotations and keeping everything else (badges,
// description hash). It writes one event at a time, so a failure part way
// leaves exactly the records before it returned (RetriageUnread's contract,
// pinned by TestRetriageUnreadReportsOnlyWrittenKeys); the drop reopenings,
// which can run to thousands, batch through reopenEach instead.
func reopen(ctx context.Context, l ledger.Ledger, now time.Time, dryRun bool, reason string, match func(ledger.Record) bool) ([]posting.Key, error) {
	var keys []posting.Key
	for _, r := range l.All() {
		if err := ctx.Err(); err != nil {
			return keys, err
		}
		if !match(r) {
			continue
		}
		if dryRun {
			keys = append(keys, r.Key)
			continue
		}
		if err := l.Append(ctx, reopenEvent(r, now, reason)); err != nil {
			return keys, err
		}
		keys = append(keys, r.Key)
	}
	return keys, nil
}

// reopenEach builds the event plan gives each record it selects and writes
// them in bounded batches (appendBatched), not one fsync per posting. A batch
// the ledger refuses is written again one event at a time, so the records
// before a bad one still land, as they did when every reopen was its own
// append; an event refused because its stored body cannot be read back is
// skipped and counted (unreadable), and the rest go on. Any other refusal
// stops it. It returns the keys written, in ledger order; on an error, the
// ones written before it. With dryRun it writes nothing and returns every
// selected key. A rerun is idempotent: a reopened record is no longer
// dropped, so no plan selects it again.
func reopenEach(ctx context.Context, l ledger.Ledger, dryRun bool, plan func(ledger.Record) (ledger.Event, bool)) (keys []posting.Key, unreadable int, err error) {
	var evs []ledger.Event
	for _, r := range l.All() {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if ev, ok := plan(r); ok {
			evs = append(evs, ev)
		}
	}
	if dryRun {
		keys = make([]posting.Key, len(evs))
		for i, e := range evs {
			keys[i] = e.Key
		}
		return keys, 0, nil
	}
	for len(evs) > 0 {
		done, err := appendBatched(ctx, l, evs)
		for _, e := range evs[:done] {
			keys = append(keys, e.Key)
		}
		evs = evs[done:]
		if err == nil {
			return keys, unreadable, nil
		}
		if ctx.Err() != nil || len(evs) == 0 {
			return keys, unreadable, err
		}
		// The refused batch is all-or-nothing, and appendBatched does not say
		// how many events it held (its byte budget may have cut it short of
		// MaxBatchEvents), so the next MaxBatchEvents events, at least the
		// refused batch, are written one at a time before batching resumes.
		// appendBatched's own refusals (an event it cannot encode, one over
		// the byte budget) land here too and are tried singly the same way.
		n := min(len(evs), ledger.MaxBatchEvents)
		for _, e := range evs[:n] {
			if cerr := ctx.Err(); cerr != nil {
				return keys, unreadable, cerr
			}
			switch aerr := l.Append(ctx, e); {
			case aerr == nil:
				keys = append(keys, e.Key)
			case errors.Is(aerr, ledger.ErrBodyUnavailable):
				unreadable++
			default:
				return keys, unreadable, aerr
			}
		}
		evs = evs[n:]
	}
	return keys, unreadable, nil
}

// reopenEvent returns r to Live, forgetting the triage, scoring, explanation
// and exclusion annotations and keeping everything else.
func reopenEvent(r ledger.Record, now time.Time, reason string) ledger.Event {
	forget := map[string]string{
		ledger.AnnRank: "", ledger.AnnCoverage: "", ledger.AnnMissing: "", ledger.AnnBlocks: "", ledger.AnnExplanation: "",
		ledger.AnnCategory: "", // derived from triage.category, so it goes with the triage
	}
	for k := range r.Annotations {
		if strings.HasPrefix(k, ledger.AnnTriagePrefix) || k == AnnExcluded {
			forget[k] = ""
		}
	}
	return ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: posting.Live, Annotations: forget, Reason: reason}
}
