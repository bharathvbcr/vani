package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// ErrPolicyUnjudgeable means the drop policy cannot tell a model drop from a
// stale one: a rule drops on an OpenRouter probability but the policy names no
// OpenRouter backend, so every model drop would look stale. Nothing is judged.
var ErrPolicyUnjudgeable = errors.New("pipeline: the drop policy names no OpenRouter backend, so a model drop cannot be re-judged")

// Why a dropped record was not judged. They are fixed strings (no posting text),
// counted in StaleReport.NotJudgedWhy.
const (
	notJudgedNoDropper  = "no recorded backend or exclusion"
	notJudgedNoBody     = "description could not be read back"
	notJudgedNoReadings = "no recorded readings"
	notJudgedUncalib    = "recorded under a backend today's policy does not calibrate"
	notJudgedNoP        = "recorded without a usable probability"
)

// Who dropped a record, as StaleDrop.By says.
const (
	DropByExclude = "exclude"
	DropByTitle   = "title"
	DropByModel   = "model"
	dropByUnknown = "unknown"
)

// StaleReading is one slot a dropped record carries, as it was recorded: the
// slot, the backend that read it and its probability. The value is kept except
// for the sponsorship slot, whose values can reveal the owner's work
// authorization (the drop class never names a value for the same reason).
type StaleReading struct {
	Slot    string   `json:"slot"`
	Value   string   `json:"value,omitempty"`
	Backend string   `json:"backend,omitempty"`
	P       *float64 `json:"p,omitempty"`
}

// StaleDrop is a dropped record that today's policy would not drop. It holds no
// posting text. Recorded is what the drop was made on; Today is what the
// policy says now: "live" (the next run asks a backend), "triaged" or "unsure".
type StaleDrop struct {
	Key      posting.Key    `json:"key"`
	Class    string         `json:"class"`
	By       string         `json:"dropped_by"`
	Recorded []StaleReading `json:"recorded,omitempty"`
	Today    string         `json:"today"`
	Why      string         `json:"today_reason"`
}

// StaleClass counts the stale drops of one class.
type StaleClass struct {
	Class string `json:"class"`
	Count int    `json:"count"`
}

// StaleReport is every dropped record re-judged under one policy. Dropped =
// StillDropped + Stale + NotJudged: a record that could not be judged is never
// counted as either of the others.
type StaleReport struct {
	Dropped      int            `json:"dropped"`
	StillDropped int            `json:"still_dropped"`
	Stale        int            `json:"stale"`
	NotJudged    int            `json:"not_judged"`
	NotJudgedWhy map[string]int `json:"not_judged_why"`
	// Classes are the stale drops by class, the largest first.
	Classes []StaleClass `json:"classes"`
	// Items are the stale drops, model drops first (the ones a rescue acts on,
	// and the few among thousands of title drops), then in key order.
	Items []StaleDrop `json:"items"`
}

// judgement is the outcome of re-judging one dropped record.
type judgement struct {
	still, stale bool
	by           string
	today, why   string
	notJudged    string // why not, when neither still nor stale
	rec          ledger.Record
}

// dropperOf says what dropped r, from what it recorded.
func dropperOf(r ledger.Record) string {
	switch {
	case Excluded(r) || r.Annotations[ledger.AnnTriagePrefix+"backend"] == ExcludeBackend:
		return DropByExclude
	case TitleDropped(r):
		return DropByTitle
	case r.Annotations[ledger.AnnTriagePrefix+"backend"] != "":
		return DropByModel
	}
	return dropByUnknown
}

// needsOpenRouter reports whether a rule of p drops on an OpenRouter
// probability while p names no OpenRouter backend.
func (p DropPolicy) needsOpenRouter() bool {
	if p.OpenRouter != "" {
		return false
	}
	for _, r := range p.Rules {
		if r.MinOpenRouterProb > 0 {
			return true
		}
	}
	return false
}

// judgeDrop re-judges one Dropped record against p: the exclusions and the title
// reading as triage applies them before any backend, then, for a record a model
// dropped, the drop rules on the readings it recorded (the same two steps
// Runner.read takes). A model drop is judged without its description unless p
// reads one (exclusions, title readings), because those read it. A check that
// could not run is notJudged, never stale.
func (p DropPolicy) judgeDrop(l ledger.Ledger, r ledger.Record) judgement {
	j := judgement{by: dropperOf(r), rec: r}
	if j.by == dropByUnknown {
		j.notJudged = notJudgedNoDropper
		return j
	}
	if p.Prefilters() {
		if j.rec = ledger.Full(l, r); j.rec.BodyEvicted {
			j.notJudged = notJudgedNoBody
			return j
		}
		if _, _, dropped := p.EvaluatePosting(j.rec.Posting); dropped {
			j.still = true
			return j
		}
	}
	switch j.by {
	case DropByExclude:
		j.stale, j.today, j.why = true, "live", "the exclusions no longer match it"
		return j
	case DropByTitle:
		j.stale, j.today, j.why = true, "live", "the title rules no longer drop it; a backend must read it"
		return j
	}
	a := answerFromAnnotations(r.Annotations)
	if len(a.Slots) == 0 {
		j.notJudged = notJudgedNoReadings
		return j
	}
	st, why := p.Evaluate(a)
	if st == posting.Dropped {
		j.still = true
		return j
	}
	if reason := p.unjudgeable(a); reason != "" {
		j.notJudged = reason
		return j
	}
	if st == posting.Triaged {
		why = "no current drop rule matches its recorded readings"
	}
	j.stale, j.today, j.why = true, string(st), why
	return j
}

// unjudgeable names why a model's recorded drop reading cannot be re-judged,
// or "": a rule's value was read by a backend whose calibration today's policy
// does not hold, or without the probability or score the rule compares. Not
// dropping on such a reading would be the policy not knowing, not the rule
// having changed.
func (p DropPolicy) unjudgeable(a decide.Answer) string {
	for _, rule := range p.Rules {
		sr, ok := a.Slots[rule.Slot]
		if !ok || sr.Reading != decide.Answered || !contains(rule.Values, sr.Value) {
			continue
		}
		switch b := slotBackend(a, sr); {
		case p.OpenRouter != "" && b == p.OpenRouter:
			if rule.MinOpenRouterProb <= 0 {
				continue // dropping on this backend is switched off: the rule changed
			}
			if sr.Jev == nil || math.IsNaN(sr.Jev.Probability) || sr.Jev.Probability < 0 || sr.Jev.Probability > 1 {
				return notJudgedNoP
			}
		case b == "lappi":
			if rule.MinLappiScore <= 0 {
				continue
			}
			if sr.Lappi == nil || math.IsNaN(sr.Lappi.Score) || math.IsInf(sr.Lappi.Score, 0) {
				return notJudgedNoP
			}
		default:
			return notJudgedUncalib
		}
	}
	return ""
}

// staleReadings lists the readings r recorded, in slot order. The sponsorship
// value is left out (see StaleReading).
func staleReadings(r ledger.Record) []StaleReading {
	a := answerFromAnnotations(r.Annotations)
	out := make([]StaleReading, 0, len(a.Slots))
	for slot, sr := range a.Slots {
		if sr.Reading != decide.Answered {
			continue
		}
		rd := StaleReading{Slot: slot, Value: sr.Value, Backend: slotBackend(a, sr)}
		if slot == "sponsorship" {
			rd.Value = ""
		}
		if raw, ok := r.Annotations[ledger.AnnTriagePrefix+slot+ledger.AnnTriageSuffixP]; ok {
			if p, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(p) && !math.IsInf(p, 0) {
				rd.P = &p
			}
		}
		out = append(out, rd)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Slot < out[k].Slot })
	return out
}

// StaleDrops re-judges every Dropped record of l under p and reports those the
// policy would no longer drop, by class. class names a dropped record
// (eval.DropClass); nil classes a drop by what dropped it. It reads the ledger
// and writes nothing. With a policy that cannot judge model drops it returns
// ErrPolicyUnjudgeable and no report. A cancelled ctx returns what was judged
// so far with ctx's error.
func StaleDrops(ctx context.Context, l ledger.Ledger, p DropPolicy, class func(ledger.Record) string) (StaleReport, error) {
	rep := StaleReport{NotJudgedWhy: map[string]int{}, Classes: []StaleClass{}, Items: []StaleDrop{}}
	if p.needsOpenRouter() {
		return rep, ErrPolicyUnjudgeable
	}
	counts := map[string]int{}
	for _, r := range l.All() {
		if r.Status != posting.Dropped {
			continue
		}
		if err := ctx.Err(); err != nil {
			return finishStale(rep, counts), err
		}
		rep.Dropped++
		j := p.judgeDrop(l, r)
		switch {
		case j.still:
			rep.StillDropped++
		case j.stale:
			rep.Stale++
			c := j.by + ":other"
			if class != nil {
				c = class(r)
			}
			counts[c]++
			rep.Items = append(rep.Items, StaleDrop{Key: r.Key, Class: c, By: j.by, Recorded: staleReadings(r), Today: j.today, Why: j.why})
		default:
			rep.NotJudged++
			rep.NotJudgedWhy[j.notJudged]++
		}
	}
	return finishStale(rep, counts), nil
}

func finishStale(rep StaleReport, counts map[string]int) StaleReport {
	sort.SliceStable(rep.Items, func(i, k int) bool {
		return (rep.Items[i].By == DropByModel) && (rep.Items[k].By != DropByModel)
	})
	for c, n := range counts {
		rep.Classes = append(rep.Classes, StaleClass{Class: c, Count: n})
	}
	sort.Slice(rep.Classes, func(i, k int) bool {
		if rep.Classes[i].Count != rep.Classes[k].Count {
			return rep.Classes[i].Count > rep.Classes[k].Count
		}
		return rep.Classes[i].Class < rep.Classes[k].Class
	})
	return rep
}

// StaleRescue is what RescueStaleDrops did, or with dryRun would do.
type StaleRescue struct {
	// Rescued are the model drops returned to Unsure, in ledger order.
	Rescued []posting.Key
	// LeftToOther counts the stale title and exclusion drops, which `retriage
	// --title-drops` and the run's exclusion stage return to Live (a backend has
	// to read them; Unsure would keep them unread).
	LeftToOther int
	// StillDropped counts the drops today's policy still makes.
	StillDropped int
	// NotJudged counts the drops that could not be re-judged, and those a
	// stale model drop was left because its description could not be read back
	// (reopening it needs the body).
	NotJudged int
}

// RescueStaleDrops returns to Unsure every record a model dropped that p would
// no longer drop, keeping what the model read (so nothing is asked again and
// nothing is spent) and recording why under triage.unsure. Unsure is kept
// (Rule 7): the posting is scored and shown like any other uncertain one. A
// record p still drops, or cannot judge, is left alone. The rescue asks no
// model; the unsure posting then goes through the next run's scoring and notes
// stages like any other (the notes call is the daily budget's). Each record is judged
// again just before it is written, so a drop the policy still makes is never
// touched, and a second run finds nothing. With dryRun it only reports.
func RescueStaleDrops(ctx context.Context, l ledger.Ledger, now time.Time, p DropPolicy, dryRun bool) (StaleRescue, error) {
	var out StaleRescue
	if p.needsOpenRouter() {
		return out, ErrPolicyUnjudgeable
	}
	for _, r := range l.All() {
		if r.Status != posting.Dropped {
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		j := p.judgeDrop(l, r)
		switch {
		case j.still:
			out.StillDropped++
			continue
		case !j.stale:
			out.NotJudged++
			continue
		case j.by != DropByModel:
			out.LeftToOther++
			continue
		}
		if !dryRun {
			if full := ledger.Full(l, r); full.BodyEvicted {
				out.NotJudged++ // reopening an evicted record is refused without its body
				continue
			}
			reason := "retriage: a drop today's rules no longer make (" + j.why + ")"
			ev := ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: posting.Unsure,
				Annotations: map[string]string{ledger.AnnTriageUnsure: staleUnsure(j.why)}, Reason: reason}
			if err := l.Append(ctx, ev); err != nil {
				return out, fmt.Errorf("pipeline: rescue %s: %w", r.Key, err)
			}
		}
		out.Rescued = append(out.Rescued, r.Key)
	}
	return out, nil
}

// staleUnsure is the triage.unsure reason of a rescued drop. why comes from the
// drop policy and names slots and thresholds, never posting text.
func staleUnsure(why string) string {
	return "dropped under an earlier rule; today " + strings.TrimSpace(why)
}
