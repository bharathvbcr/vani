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
// them. With dryRun it only reports. Keys are in ledger order.
func RetriageTitleDrops(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, dryRun bool) ([]posting.Key, error) {
	return reopen(ctx, l, now, dryRun, "retriage: the title rules no longer drop it", func(r ledger.Record) bool {
		if !TitleDropped(r) {
			return false
		}
		_, _, still := p.EvaluateTitle(r.Posting.Title)
		return !still
	})
}

// reopen returns each record match selects to Live, forgetting the triage,
// scoring and explanation annotations and keeping everything else (badges,
// description hash).
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

// reopenEvent returns r to Live, forgetting the triage, scoring, explanation
// and exclusion annotations and keeping everything else.
func reopenEvent(r ledger.Record, now time.Time, reason string) ledger.Event {
	forget := map[string]string{
		ledger.AnnRank: "", ledger.AnnCoverage: "", ledger.AnnMissing: "", ledger.AnnBlocks: "", ledger.AnnExplanation: "",
	}
	for k := range r.Annotations {
		if strings.HasPrefix(k, ledger.AnnTriagePrefix) || k == AnnExcluded {
			forget[k] = ""
		}
	}
	return ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: posting.Live, Annotations: forget, Reason: reason}
}
