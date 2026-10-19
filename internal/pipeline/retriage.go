package pipeline

import (
	"context"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Unread reports whether r was triaged without any backend reading it: it has
// at least one "triage.<slot>.reading" annotation and every one of them is
// "unavailable". Such a record carries no decision, only an outage.
func Unread(r ledger.Record) bool {
	n := 0
	for k, v := range r.Annotations {
		if !strings.HasPrefix(k, ledger.AnnTriagePrefix) || !strings.HasSuffix(k, ledger.AnnTriageSuffixReading) {
			continue
		}
		n++
		if v != "unavailable" {
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
	var keys []posting.Key
	for _, r := range l.All() {
		if !retriageable[r.Status] || !Unread(r) {
			continue
		}
		keys = append(keys, r.Key)
		if dryRun {
			continue
		}
		forget := map[string]string{
			ledger.AnnRank: "", ledger.AnnCoverage: "", ledger.AnnMissing: "", ledger.AnnBlocks: "", ledger.AnnExplanation: "",
		}
		for k := range r.Annotations {
			if strings.HasPrefix(k, ledger.AnnTriagePrefix) {
				forget[k] = ""
			}
		}
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: posting.Live,
			Annotations: forget, Reason: "retriage: no decision backend could read it"}); err != nil {
			return keys, err
		}
	}
	return keys, nil
}
