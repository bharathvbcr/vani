package jobinsight

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// One builder turns a ledger record into Score's Inputs, so the Jobs list, the
// Mac app's Home list and the digest cannot disagree about what a posting's
// annotations mean. Callers differ only in which optional parts they can
// supply (vocabulary hits, semantic similarity, title signals).

// DropRule is a configured drop rule as the ranker needs it: the slot and the
// values that drop. A kept posting whose answered value is in Values did not
// meet the rule's confidence threshold, so it is penalised in proportion to
// the reading's confidence. The values can reveal the owner's work
// authorization: nothing here logs, prints or returns them.
type DropRule struct {
	Slot   string
	Values []string
}

// RankContext is everything InputsFor needs beyond the record itself. Build it
// once per call with NewRankContext, then set the optional parts.
type RankContext struct {
	Now time.Time
	// FirstCrawl is, per board (BoardOf), the earliest first-seen among the
	// records NewRankContext was given.
	FirstCrawl map[string]time.Time
	// Company counts marks per company, keyed by lower-cased trimmed name.
	Company map[string]CompanyMarks
	// Marks are the marks Company was counted from; InputsFor takes a
	// record's own mark out of its company's counts, so a saved posting does
	// not vouch for itself.
	Marks map[posting.Key]marks.Mark

	// TitleRank is the configured title signals' Rank; nil means none.
	TitleRank func(title string) int
	// Target is true when the config has a target summary. With Hits nil the
	// keyword part is unknown.
	Target bool
	// Hits returns the distinct target terms the record's title names and the
	// distinct further ones the rest of the posting names.
	Hits func(r ledger.Record) (title, body int)
	// Semantic returns the calibrated 0-1 similarity, or false when unknown.
	Semantic func(r ledger.Record) (float64, bool)

	DropRules    []DropRule
	HalfLifeDays float64
}

// NewRankContext counts the first crawl per board and the marks per company
// across recs. m may be nil.
func NewRankContext(recs []ledger.Record, m map[posting.Key]marks.Mark, now time.Time) RankContext {
	c := RankContext{
		Now:        now,
		FirstCrawl: map[string]time.Time{},
		Company:    map[string]CompanyMarks{},
		Marks:      m,
	}
	for _, r := range recs {
		if !r.FirstSeen.IsZero() {
			b := BoardOf(string(r.Key))
			if t, ok := c.FirstCrawl[b]; !ok || r.FirstSeen.Before(t) {
				c.FirstCrawl[b] = r.FirstSeen
			}
		}
		cm := c.Company[companyKey(r.Posting.Company)]
		switch m[r.Key] {
		case marks.Saved, marks.Applied:
			cm.Liked++
		case marks.Hidden:
			cm.Hidden++
		default:
			continue
		}
		c.Company[companyKey(r.Posting.Company)] = cm
	}
	return c
}

func companyKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// IsUnsure reports whether triage could not decide this posting. The pipeline
// keeps scoring unsure postings, so after scoring their status is Scored or
// Explained and the flag lives on as the "triage.unsure" annotation.
func IsUnsure(r ledger.Record) bool {
	return r.Status == posting.Unsure || strings.TrimSpace(r.Annotations[ledger.AnnTriagePrefix+"unsure"]) != ""
}

// parseFloat reads a float annotation strictly: surrounding space is allowed,
// NaN, infinities and anything else are absent.
func parseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// SlotOf reads one triage slot from a record's annotations. A probability is
// kept only for an answered reading and only inside [0,1].
func SlotOf(ann map[string]string, slot string) Slot {
	pre := ledger.AnnTriagePrefix + slot
	s := Slot{
		Value:    strings.TrimSpace(ann[pre]),
		Answered: strings.TrimSpace(ann[pre+ledger.AnnTriageSuffixReading]) == "answered",
		Disagree: strings.TrimSpace(ann[pre+ledger.AnnTriageSuffixDisagree]) == "true",

		ShadowBackend: strings.TrimSpace(ann[pre+ledger.AnnTriageSuffixShadowBackend]),
	}
	if s.Answered {
		if p, ok := parseFloat(ann[pre+ledger.AnnTriageSuffixP]); ok && p >= 0 && p <= 1 {
			s.P, s.HasP = p, true
		}
	}
	return s
}

// employerBoostSlack absorbs binary rounding in rank - coverage.
const employerBoostSlack = 1e-6

// InputsFor builds Score's Inputs for r.
//
// PreferredEmployer is derived from the ledger: the pipeline adds
// pipeline.EmployerRankBoost to the rank of a preferred employer and records
// the unboosted must-have coverage separately, so a rank at least that far
// above the coverage means the boost was applied. Nothing about which
// employers the owner prefers is stored (see pipeline.Runner.preferredEmployer).
// A record without a coverage key predates the split: its rank stands in for
// coverage, as digest.Coverage does, and no boost can be seen.
func InputsFor(r ledger.Record, c RankContext) Inputs {
	ann := r.Annotations
	in := Inputs{
		RoleFit:      SlotOf(ann, "role_fit"),
		Unsure:       IsUnsure(r),
		HalfLifeDays: c.HalfLifeDays,
		AgeDays:      -1,
	}

	rank, hasRank := parseFloat(ann[ledger.AnnRank])
	if cov, ok := parseFloat(ann[ledger.AnnCoverage]); ok {
		in.Coverage, in.HasCoverage = cov, true
		in.PreferredEmployer = hasRank && rank-cov >= pipeline.EmployerRankBoost-employerBoostSlack
	} else if hasRank {
		in.Coverage, in.HasCoverage = rank, true
	}

	if c.TitleRank != nil {
		in.TitleSignals, in.TitleRank = true, c.TitleRank(r.Posting.Title)
	}
	if c.Target && c.Hits != nil {
		in.Target = true
		in.TitleHits, in.BodyHits = c.Hits(r)
	}
	if c.Semantic != nil {
		in.Semantic, in.HasSemantic = c.Semantic(r)
		if !in.HasSemantic {
			in.Semantic = 0
		}
	}

	if at, _, known := FreshAge(r.Posting.PostedAt, r.FirstSeen, c.FirstCrawl[BoardOf(string(r.Key))], c.Now); known {
		in.AgeDays, in.AgeKnown = AgeDays(at, c.Now), true
	} else if !at.IsZero() {
		// Age unknown, but the first sighting bounds it from below (Score).
		in.SeenDays, in.SeenKnown = AgeDays(at, c.Now), true
	}

	seen := map[string]bool{}
	for _, rule := range c.DropRules {
		if seen[rule.Slot] {
			continue
		}
		s := SlotOf(ann, rule.Slot)
		if !s.Answered {
			continue
		}
		for _, v := range rule.Values {
			if strings.TrimSpace(v) == s.Value && s.Value != "" {
				seen[rule.Slot] = true
				in.Penalties = append(in.Penalties, Penalty{Slot: s})
				break
			}
		}
	}

	in.Company = c.Company[companyKey(r.Posting.Company)]
	switch c.Marks[r.Key] {
	case marks.Saved, marks.Applied:
		in.Company.Liked--
	case marks.Hidden:
		in.Company.Hidden--
	}
	in.Company.Liked, in.Company.Hidden = max(in.Company.Liked, 0), max(in.Company.Hidden, 0)
	return in
}
