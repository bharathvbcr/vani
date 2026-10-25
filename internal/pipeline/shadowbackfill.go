package pipeline

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// ReasonShadowBackfilled is the reason of the event RecordShadows writes.
const ReasonShadowBackfilled = "shadow_backfilled"

// ShadowPending reports whether slot of a triaged record is still waiting for a
// reading from the shadow tier named backend: it holds no shadow reading, or only
// an outage reading of that tier ("unavailable": the tier was asked while it could
// not answer, as when the daily cap ran out mid-triage), which a later pass may
// replace. A reading of any other kind, or of another shadow, is not pending.
func ShadowPending(ann map[string]string, slot, backend string) bool {
	pre := ledger.AnnTriagePrefix + slot
	switch strings.TrimSpace(ann[pre+ledger.AnnTriageSuffixShadowReading]) {
	case "":
		return true
	case string(decide.Unavailable):
		return strings.TrimSpace(ann[pre+ledger.AnnTriageSuffixShadowBackend]) == backend
	}
	return false
}

// RecordShadows records the escalation tier's readings of slots of an already
// triaged posting, beside the primary's, with exactly the annotations a triage
// writes for them (shadowAnnotations). It is the backfill's one write path.
//
// asked maps each slot to the value of the primary's reading the escalation was
// asked about; shadows holds the tier's readings by slot. It writes one event that
// keeps the record's status (Rule 7: a reading only annotates, nothing here drops
// or reopens a posting), so the record's primary reading, p and status are never
// touched, and returns how many slots it recorded.
//
// A slot is skipped, not recorded, when
//   - its reading is an outage (Unavailable, NotAsked): it was not read, and a
//     later run asks again. Recording it would make a budget refusal permanent.
//   - the record's primary reading is no longer the asked one (the posting was
//     triaged again meanwhile) or is no longer an answer.
//   - the record already holds a reading for it from a shadow, or one of the tier
//     named backend other than an outage (ShadowPending), so a repeated run
//     writes nothing.
//
// Only a record that is Triaged, Unsure, Scored, Explained or Digested is written:
// a closed posting's last event day is what the digest footer counts. A record
// that is gone or in another status is an error. The write is bounded by ctx.
func (r *Runner) RecordShadows(ctx context.Context, key posting.Key, backend string, asked map[string]string, shadows map[string]decide.SlotReading) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	rec, ok := r.Ledger.Get(key)
	if !ok {
		return 0, fmt.Errorf("pipeline: unknown key %q", key)
	}
	switch rec.Status {
	case posting.Triaged, posting.Unsure, posting.Scored, posting.Explained, posting.Digested:
	default:
		return 0, fmt.Errorf("pipeline: %s is %s; a shadow reading is recorded on a kept posting only", key, rec.Status)
	}
	slots := make([]string, 0, len(asked))
	for s := range asked {
		slots = append(slots, s)
	}
	sort.Strings(slots)
	ann := map[string]string{}
	n := 0
	for _, slot := range slots {
		sh, ok := shadows[slot]
		if !ok || outageReading(sh.Reading) {
			continue
		}
		pre := ledger.AnnTriagePrefix + slot
		if !ShadowPending(rec.Annotations, slot, backend) {
			continue
		}
		if rec.Annotations[pre+ledger.AnnTriageSuffixReading] != string(decide.Answered) || rec.Annotations[pre] != asked[slot] {
			continue
		}
		maps.Copy(ann, shadowAnnotations(slot, true, asked[slot], sh))
		n++
	}
	if n == 0 {
		return 0, nil
	}
	if err := r.emit(ctx, key, rec.Status, ReasonShadowBackfilled, ann); err != nil {
		return 0, err
	}
	return n, nil
}
