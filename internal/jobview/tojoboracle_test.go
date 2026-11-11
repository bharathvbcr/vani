package jobview

import (
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"testing"

	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// toJobOracle is ToJob as it was before a selection worked on candidates
// (9965dcd), verbatim but for its name: the oracle the rowStatic split is held
// to.
func toJobOracle(r ledger.Record, entry marks.Entry) Job {
	p := r.Posting
	ann := r.Annotations
	j := Job{
		Key:         r.Key,
		Company:     CleanText(p.Company, FieldRunes, false),
		Title:       CleanText(p.Title, FieldRunes, false),
		Location:    CleanText(p.Location, FieldRunes, false),
		Remote:      p.Remote,
		Department:  CleanText(p.Department, FieldRunes, false),
		Status:      r.Status,
		Closed:      r.Status == posting.Dead,
		Untriaged:   r.Status == posting.Live,
		Explanation: CleanText(ann[ledger.AnnExplanation], NoteRunes, false),
		Missing:     CleanText(ann[ledger.AnnMissing], NoteRunes, false),
		LCA:         CleanText(ann[ledger.AnnLCA], FieldRunes, false),
		Employer:    CleanText(ann[ledger.AnnEmployer], FieldRunes, false),
		FirstSeen:   r.FirstSeen,
		PostedAt:    p.PostedAt,
		Mark:        entry.Mark,
		MarkedAt:    entry.MarkedAt,
		Note:        entry.Note,
		NoteAt:      entry.NoteAt,
	}
	j.ApplyFormNote, j.ApplyFlags = ApplyFormNote(p), ApplyFormFlags(p.Form)
	j.Stage, j.StageAt, j.FurthestStage = entry.Stage, entry.StageAt, entry.Furthest
	if j.Title == "" {
		j.Title = "(untitled)"
	}
	if u, ok := digest.HTTPURL(p.URL); ok {
		j.URL = u
	}
	if u, ok := digest.HTTPURL(p.ApplyURL); ok {
		j.ApplyURL = u
	}
	if v, ok := digest.Rank(r); ok {
		j.Rank = &v
	}
	if v, ok := digest.Coverage(r); ok {
		j.Coverage = &v
	}
	if digest.IsUnsure(r) {
		j.Unsure = digest.WhyUnsure(ann)
	}
	for _, slot := range digest.TriageSlots {
		if v := CleanText(ann[ledger.AnnTriagePrefix+slot], 80, false); v != "" {
			if j.Triage == nil {
				j.Triage = map[string]string{}
			}
			j.Triage[slot] = v
		}
	}
	if cat := CleanText(ann[ledger.AnnCategory], 80, false); cat != "" {
		j.Category = cat
	} else if cat := CleanText(ann[ledger.AnnTriagePrefix+"category"], 80, false); cat != "" {
		j.Category = cat
	}
	if j.Category != "" {
		if j.Triage == nil {
			j.Triage = map[string]string{}
		}
		j.Triage["category"] = j.Category
	}
	j.DupOf = CleanText(ann[pipeline.DuplicateOfKey], FieldRunes, false)
	j.DupCandidateOf = CleanText(ann[pipeline.AnnDupCandidateOf], FieldRunes, false)
	j.DupSiblingOf = CleanText(ann[pipeline.AnnDupSiblingOf], FieldRunes, false)
	if s := ann[pipeline.AnnDupScore]; s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 && f <= 1 {
			j.DupScore = f
		}
	}
	return j
}

// ToJob answers what it answered before its static fields came from staticOf,
// over adversarial text and every pairing of the category annotations (both
// set and different, whitespace only, one or neither).
func TestToJobMatchesPinnedOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(19, 23))
	cats := []string{"", " ", "\t\n", "ml", "infra", "x\u0000y", "\xff bad"}
	recs, m := randomRecords(rng, 400)
	for i, r := range recs {
		ann := map[string]string{}
		for k, v := range r.Annotations {
			ann[k] = v
		}
		delete(ann, ledger.AnnCategory)
		delete(ann, ledger.AnnTriagePrefix+"category")
		if c := cats[i%len(cats)]; c != "" {
			ann[ledger.AnnCategory] = c
		}
		if c := cats[(i/len(cats))%len(cats)]; c != "" {
			ann[ledger.AnnTriagePrefix+"category"] = c
		}
		if i%5 == 0 {
			ann[ledger.AnnTriagePrefix+"role_fit"] = "   "
		}
		r.Annotations = ann
		got, _ := json.Marshal(ToJob(r, m[r.Key]))
		want, _ := json.Marshal(toJobOracle(r, m[r.Key]))
		if string(got) != string(want) {
			t.Fatalf("%s: ToJob differs from the pinned oracle\ngot:  %s\nwant: %s", r.Key, got, want)
		}
	}
}
