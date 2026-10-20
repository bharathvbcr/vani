package eval

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// escalateShadow is the backend name escalated readings carry in
// triage.<slot>.shadow_backend (jobinsight.ShadowEscalate). They are a cheaper
// Claude tier's readings on low-p slots, not a second opinion on a drop.
const escalateShadow = "escalate"

// DropRuleSpec is one drop rule as the agreement report needs it: a record's
// slot is a drop candidate when the primary answered one of Values with a
// probability of at least MinP (zero never drops on a probability).
type DropRuleSpec struct {
	Slot   string
	Values []string
	MinP   float64
}

// AgreementRow is one slot and shadow backend's second opinion on the
// postings the drop policy would drop on a calibrated primary reading. Asked is
// every candidate with a shadow reading recorded; Answered of them have a
// usable value, Unavailable do not (never an agreement). Agree + Disagree =
// Answered. DisagreeJudged of the disagreements have an owner would_apply
// label, DisagreeWanted of those say yes or maybe.
type AgreementRow struct {
	Slot, Shadow   string
	NotAsked       int // candidates with no shadow reading at all
	Asked          int
	Answered       int
	Unavailable    int
	Agree          int
	Disagree       int
	DisagreeJudged int
	DisagreeWanted int
}

// DropAgreement is the report: the primary's name and one row per slot and shadow.
type DropAgreement struct {
	Primary string
	Rows    []AgreementRow

	llmModel string // Names.LLMModel, shown beside the "llm" shadow
}

// shadowLabel is a shadow backend as the report names it: "llm" carries the
// model it ran on; every other name (an OpenRouter model, a local model and
// mode) already says what it is.
func (d DropAgreement) shadowLabel(name string) string {
	if name == "llm" && d.llmModel != "" {
		return name + " (" + d.llmModel + ")"
	}
	return name
}

// BuildDropAgreement counts, over recs, how often each shadow backend agreed
// with the primary on the slots the rules would drop on. Inherited readings are
// copies, not observations, and are left out; so are escalation readings. A
// slot read by the primary under a rule's threshold is not a candidate.
func BuildDropAgreement(labels map[posting.Key]Entry, recs []ledger.Record, rules []DropRuleSpec, names Names) DropAgreement {
	out := DropAgreement{Primary: names.probability(), llmModel: names.LLMModel}
	byslot := map[string]DropRuleSpec{}
	for _, r := range rules {
		if r.MinP > 0 && r.MinP <= 1 {
			if _, dup := byslot[r.Slot]; !dup {
				byslot[r.Slot] = r
			}
		}
	}
	wantedLabel := func(k posting.Key) (judged, hit bool) {
		e, ok := labels[k]
		if !ok {
			return false, false
		}
		v, ok := e.Labels[WouldApply]
		if !ok {
			return false, false
		}
		h, j := wanted(v)
		return j, h
	}
	rows := map[[2]string]*AgreementRow{}
	row := func(slot, shadow string) *AgreementRow {
		k := [2]string{slot, shadow}
		if rows[k] == nil {
			rows[k] = &AgreementRow{Slot: slot, Shadow: shadow}
		}
		return rows[k]
	}
	for _, r := range recs {
		if _, inherited := r.Annotations[ledger.AnnTriageInheritedFrom]; inherited {
			continue
		}
		for slot, rule := range byslot {
			pre := ledger.AnnTriagePrefix + slot
			if r.Annotations[pre+ledger.AnnTriageSuffixReading] != "answered" {
				continue
			}
			if b, _ := names.primary(r.Annotations[pre+ledger.AnnTriageSuffixBackend]); b != out.Primary {
				continue
			}
			value := r.Annotations[pre]
			if !containsString(rule.Values, value) {
				continue
			}
			p, err := strconv.ParseFloat(r.Annotations[pre+ledger.AnnTriageSuffixP], 64)
			if err != nil || math.IsNaN(p) || p < rule.MinP {
				continue
			}
			shadow := r.Annotations[pre+ledger.AnnTriageSuffixShadowBackend]
			if shadow == escalateShadow {
				continue
			}
			if shadow == "" {
				row(slot, "").NotAsked++
				continue
			}
			c := row(slot, shadow)
			c.Asked++
			if r.Annotations[pre+ledger.AnnTriageSuffixShadowReading] != "answered" {
				c.Unavailable++
				continue
			}
			c.Answered++
			if r.Annotations[pre+ledger.AnnTriageSuffixShadow] == value {
				c.Agree++
				continue
			}
			c.Disagree++
			if j, h := wantedLabel(r.Key); j {
				c.DisagreeJudged++
				if h {
					c.DisagreeWanted++
				}
			}
		}
	}
	for _, c := range rows {
		out.Rows = append(out.Rows, *c)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		if out.Rows[i].Slot != out.Rows[j].Slot {
			return out.Rows[i].Slot < out.Rows[j].Slot
		}
		return out.Rows[i].Shadow < out.Rows[j].Shadow
	})
	return out
}

func containsString(vs []string, v string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}

// Markdown renders the agreement of each shadow with the primary on drop
// candidates. Agreement is Agree of Answered with its Wilson interval; a shadow
// that was never asked, or never answered, says "no data", never 0%.
func (d DropAgreement) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Drop candidates: shadow vs %s\n\n", d.Primary)
	b.WriteString("A drop candidate is a slot the drop policy would drop on: the primary answered a rule's value with p at or above the rule's threshold. The shadow is asked about each before the drop (`decide.shadow_mode: drop_candidates`) and never changes it. Unavailable shadow readings are not agreement. Agreement is agree/answered with a 95% Wilson interval. Each disagreement is a posting the primary would drop that the shadow reads differently; \"labelled\" counts those the owner judged (would_apply), \"wanted\" the yes or maybe among them (a drop the owner would have regretted).\n\n")
	asked := false
	for _, r := range d.Rows {
		if r.Asked > 0 {
			asked = true
		}
	}
	if !asked {
		b.WriteString("no data: no drop candidate has a shadow reading yet\n")
		for _, r := range d.Rows {
			if r.NotAsked > 0 {
				fmt.Fprintf(&b, "- %s: %d candidate(s), none asked\n", r.Slot, r.NotAsked)
			}
		}
		return b.String()
	}
	b.WriteString("| slot | shadow | asked | unavailable | answered | agreement | disagreements | labelled | wanted |\n|---|---|---|---|---|---|---|---|---|\n")
	var unasked []AgreementRow
	for _, r := range d.Rows {
		if r.Shadow == "" {
			unasked = append(unasked, r)
			continue
		}
		agree := "no data (n=0)"
		if lo, hi, ok := Wilson(r.Agree, r.Answered); ok {
			agree = fmt.Sprintf("%d/%d = %.0f%% (95%% CI %.0f-%.0f%%)", r.Agree, r.Answered, 100*float64(r.Agree)/float64(r.Answered), 100*lo, 100*hi)
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %s | %d | %d | %d |\n", r.Slot, d.shadowLabel(r.Shadow), r.Asked, r.Unavailable, r.Answered, agree, r.Disagree, r.DisagreeJudged, r.DisagreeWanted)
	}
	for _, r := range unasked {
		fmt.Fprintf(&b, "\n- %s: %d candidate(s) with no shadow reading (before the mode was on, or not asked)\n", r.Slot, r.NotAsked)
	}
	return b.String()
}
