package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
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
// only drop it again. Annotations are forgotten as RetriageUnread forgets
// them. With dryRun it only reports. Keys are in ledger order. It is the title
// class of RetriagePolicyDrops.
func RetriageTitleDrops(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, dryRun bool) ([]posting.Key, error) {
	return reopenEach(ctx, l, dryRun, func(r ledger.Record) (ledger.Event, bool) {
		if planPolicyDrop(l, r, p, now) != dropTitle {
			return ledger.Event{}, false
		}
		return reopenEvent(r, now, reasonTitleDrop), true
	})
}

const (
	reasonTitleDrop = "retriage: the title rules no longer drop it"
	reasonModelDrop = "retriage: the drop rules no longer drop its reading"
)

// PolicyReopen is what RetriagePolicyDrops returned (or, in a dry run, would
// return), by the class of the drop.
type PolicyReopen struct {
	// Title were dropped on their title alone. They go back to Live, since no
	// backend read them, and the next run asks the backends.
	Title []posting.Key
	// Model were dropped on a decision backend's calibrated reading that the
	// current rules no longer drop. They go back on that reading, to Triaged
	// or Unsure as DropPolicy.Evaluate now says, and the next run scores them
	// without asking a backend again.
	Model []posting.Key
	// Unjudged counts model drops the current policy cannot judge: no slot of
	// the stored reading carries a calibration from a backend the policy may
	// drop on (the configured OpenRouter model, or Lappi). They stay dropped,
	// and are counted so that "nothing to return" is never read as "every
	// drop was checked".
	Unjudged int
}

// Returned is how many postings came back (or would, in a dry run).
func (r PolicyReopen) Returned() int { return len(r.Title) + len(r.Model) }

// RetriagePolicyDrops returns every dropped posting that p would no longer
// drop, title drops and model drops alike: a drop rule lost a value or a slot,
// a threshold rose past the stored reading, or title readings changed. It
// never drops anything: a rule that tightened applies only to readings the
// pipeline makes from now on (Rule 7). An exclusion drop is ApplyExclusions'
// to reopen, and a posting the current exclusions or title rules would drop
// before asking a backend stays dropped. With dryRun it only reports. Keys
// are in ledger order.
func RetriagePolicyDrops(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, dryRun bool) (PolicyReopen, error) {
	var out PolicyReopen
	class := map[posting.Key]dropClass{}
	keys, err := reopenEach(ctx, l, dryRun, func(r ledger.Record) (ledger.Event, bool) {
		c := planPolicyDrop(l, r, p, now)
		switch c {
		case dropTitle:
			class[r.Key] = c
			return reopenEvent(r, now, reasonTitleDrop), true
		case dropModel:
			class[r.Key] = c
			return keepReadingEvent(r, p, now), true
		case dropUnjudged:
			out.Unjudged++
		}
		return ledger.Event{}, false
	})
	for _, k := range keys {
		if class[k] == dropTitle {
			out.Title = append(out.Title, k)
		} else {
			out.Model = append(out.Model, k)
		}
	}
	return out, err
}

// dropClass is what planPolicyDrop makes of one record.
type dropClass int

const (
	dropStays    dropClass = iota // not dropped, or still dropped under p, or not this function's to reopen
	dropTitle                     // a title drop p no longer makes
	dropModel                     // a model drop whose stored reading p no longer drops
	dropUnjudged                  // a model drop whose stored reading p cannot judge
)

// planPolicyDrop classifies r under p. It is the one selector for both kinds
// of drop.
func planPolicyDrop(l ledger.Ledger, r ledger.Record, p DropPolicy, now time.Time) dropClass {
	if r.Status != posting.Dropped {
		return dropStays
	}
	backend := r.Annotations[ledger.AnnTriagePrefix+"backend"]
	switch {
	case backend == TitleBackend:
		if _, _, still := p.EvaluateTitle(r.Posting.Title); still {
			return dropStays
		}
		return dropTitle
	case backend == ExcludeBackend || r.Annotations[AnnExcluded] != "":
		return dropStays // ApplyExclusions owns these
	}
	ans := answerFromAnnotations(r.Annotations)
	if !p.judges(ans) {
		return dropUnjudged
	}
	if st, _ := p.Evaluate(ans); st == posting.Dropped {
		return dropStays
	}
	// A fresh triage applies the exclusions and title rules before any
	// backend: what they drop now stays dropped. An exclusion may read the
	// description, which a dropped record keeps out of memory.
	if _, _, dropped := p.EvaluatePosting(ledger.Full(l, r).Posting); dropped {
		return dropStays
	}
	return dropModel
}

// judges reports whether some answered slot of a carries the calibration p
// may drop on: a probability from the configured OpenRouter model or a Lappi
// score. Without one, Evaluate would keep the posting whatever the rules say,
// because it cannot trust the reading, not because a rule changed: a renamed
// decision model or an old reading must not undo its drops.
func (p DropPolicy) judges(a decide.Answer) bool {
	for _, sr := range a.Slots {
		if sr.Reading != decide.Answered {
			continue
		}
		switch b := slotBackend(a, sr); {
		case p.OpenRouter != "" && b == p.OpenRouter && sr.Jev != nil:
			return true
		case b == "lappi" && sr.Lappi != nil:
			return true
		}
	}
	return false
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
// them in bounded batches (appendBatched), not one fsync per posting. It
// returns the keys written, in ledger order; on an error, the ones written
// before it. With dryRun it writes nothing and returns every selected key.
func reopenEach(ctx context.Context, l ledger.Ledger, dryRun bool, plan func(ledger.Record) (ledger.Event, bool)) ([]posting.Key, error) {
	var evs []ledger.Event
	for _, r := range l.All() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ev, ok := plan(r); ok {
			evs = append(evs, ev)
		}
	}
	keys := make([]posting.Key, len(evs))
	for i, e := range evs {
		keys[i] = e.Key
	}
	if dryRun {
		return keys, nil
	}
	done, err := appendBatched(ctx, l, evs)
	return keys[:done], err
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
