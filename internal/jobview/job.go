package jobview

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/decide/jdreasons"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// The `vani jobs` view-model: the Job a posting becomes, the freshness, score,
// keyword and reason fields the ranking adds, and the pure rules that decide
// which views list it. Everything here is display-only and read-only: nothing
// writes the ledger, and no engine rule reads any of it. cmd/vani owns flag
// parsing, I/O and printing; the config-, semantic- and logo-backed ranking
// context that fills a Job's scores stays there behind Ranker.

// Field bounds for text a Job carries.
const (
	FieldRunes = 300
	NoteRunes  = 2000
	DescRunes  = 60000
)

// InsightInfo tells the client what the scores rest on, so a missing target
// summary reads as "not computed", not as "nothing matched".
type InsightInfo struct {
	// Target is true when the target-roles summary was read, so keywords can
	// be marked as wanted; false leaves every keyword unmarked.
	Target       bool    `json:"target"`
	TargetTerms  int     `json:"target_terms"`
	HalfLifeDays float64 `json:"half_life_days"`
	Vocab        int     `json:"vocab"`
	// Boilerplate is how many company-blurb terms were left out (learn).
	Boilerplate int `json:"boilerplate"`
	// Weights says which weights Score ranks by: "hand", or "fitted <date>"
	// for a rank_weights_file (cmd/vani rankWeights).
	Weights string `json:"weights"`
	Warning string `json:"warning,omitempty"`
}

// Place is one place a posting's location names (internal/location).
type Place struct {
	Area   string `json:"area,omitempty"`
	State  string `json:"state,omitempty"` // USPS code
	Region string `json:"region,omitempty"`
}

// Locate fills j's places and remote_listed from the posting's location.
func Locate(j *Job, p posting.Posting) location.Location {
	loc := location.Parse(p.Location, p.Remote)
	j.Places = nil
	for _, pl := range loc.Places {
		j.Places = append(j.Places, Place(pl))
	}
	j.RemoteListed, j.RemoteScope = loc.Remote, loc.RemoteScope
	return loc
}

// InPlaces applies --state and --area: a job passes when one of its places is
// in the set, and "none" takes a job with no state (or no area) listed.
func InPlaces(states, areas map[string]bool, places []Place) bool {
	if states != nil {
		hit, listed := false, false
		for _, p := range places {
			if p.State != "" {
				listed = true
				hit = hit || states[p.State]
			}
		}
		if !hit && !(states["none"] && !listed) {
			return false
		}
	}
	if areas != nil {
		hit, listed := false, false
		for _, p := range places {
			if a := location.NormArea(p.Area); a != "" {
				listed = true
				hit = hit || areas[a]
			}
		}
		if !hit && !(areas["none"] && !listed) {
			return false
		}
	}
	return true
}

// reasonPct is the " (91%)" a picked reason ends with (jdreasons.Note).
var reasonPct = regexp.MustCompile(`(?:^| )\((\d{1,3})%\)$`)

// ParseReasons splits the picked note ("Fits: X (91%). Gap: Y (40%).") into
// its reasons, using jdreasons' own headings. A note in any other shape comes
// back whole as one "other" reason, never half-parsed.
func ParseReasons(note string) []Reason {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil
	}
	heads := jdreasons.Headings()
	var out []Reason
	rest := note
	for rest != "" {
		head := ""
		for _, h := range heads {
			if strings.HasPrefix(rest, h+": ") {
				head = h
				break
			}
		}
		if head == "" {
			return []Reason{{Kind: "other", Text: note}}
		}
		rest = rest[len(head)+2:]
		end := len(rest)
		for _, h := range heads {
			if i := strings.Index(rest, ". "+h+": "); i >= 0 && i < end {
				end = i
			}
		}
		text := strings.TrimSuffix(strings.TrimSpace(rest[:end]), ".")
		rest = strings.TrimSpace(strings.TrimPrefix(rest[end:], "."))
		rj := Reason{Kind: strings.ToLower(head), Text: text}
		if m := reasonPct.FindStringSubmatch(text); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil && p <= 100 {
				rj.Pct, rj.Text = &p, strings.TrimSpace(strings.TrimSuffix(text, m[0]))
			}
		}
		if rj.Text == "" {
			return []Reason{{Kind: "other", Text: note}}
		}
		out = append(out, rj)
	}
	return out
}

// MatchesQuery is the --q rule: q (already lowered and trimmed) is empty or
// appears in the job's title, company, location, team or note.
func MatchesQuery(j Job, q string) bool {
	return q == "" || strings.Contains(strings.ToLower(strings.Join([]string{j.Title, j.Company, j.Location, j.Department, j.Note}, "\n")), q)
}

// InSet: a nil set takes everything; "none" stands for no reading.
func InSet(set map[string]bool, v string) bool {
	if set == nil {
		return true
	}
	if v == "" {
		v = "none"
	}
	return set[v]
}

// KeptStatus reports whether a posting passed triage and is still listed:
// these are the retrieved roles worth browsing. Dropped, dead and duplicate
// postings appear only when marked.
func KeptStatus(s posting.Status) bool {
	switch s {
	case posting.Triaged, posting.Unsure, posting.Scored, posting.Explained, posting.Digested:
		return true
	}
	return false
}

type Job struct {
	Key        posting.Key    `json:"key"`
	Company    string         `json:"company"`
	Title      string         `json:"title"`
	Location   string         `json:"location,omitempty"`
	Remote     bool           `json:"remote,omitempty"`
	Department string         `json:"department,omitempty"`
	URL        string         `json:"url,omitempty"` // http(s) only (digest.HTTPURL)
	ApplyURL   string         `json:"apply_url,omitempty"`
	Status     posting.Status `json:"status"`
	Closed     bool           `json:"closed,omitempty"` // the board no longer lists it
	Rank       *float64       `json:"rank,omitempty"`
	// Similar counts the other listings of this title at this company that the
	// default list folded into this row (jobinsight.Diversify); 0 when none.
	Similar     int               `json:"similar,omitempty"`
	Coverage    *float64          `json:"coverage,omitempty"`
	Explanation string            `json:"explanation,omitempty"`
	Missing     string            `json:"missing,omitempty"`
	Unsure      string            `json:"unsure,omitempty"`
	LCA         string            `json:"lca,omitempty"`
	Employer    string            `json:"employer,omitempty"`
	Triage      map[string]string `json:"triage,omitempty"`
	Category    string            `json:"category,omitempty"`
	FirstSeen   time.Time         `json:"first_seen"`
	PostedAt    time.Time         `json:"posted_at,omitzero"`
	Mark        marks.Mark        `json:"mark,omitempty"`
	MarkedAt    time.Time         `json:"marked_at,omitzero"`
	Note        string            `json:"note,omitempty"`
	NoteAt      time.Time         `json:"note_at,omitzero"`
	Description string            `json:"description,omitempty"` // `jobs show` only

	// Stage, StageAt and FurthestStage are what happened after applying
	// (internal/marks stages.go). Mark stays applied for every stage, so a
	// reader that does not know them sees what it always saw; MarkedAt is the
	// date first applied.
	Stage         marks.Stage `json:"stage,omitempty"`
	StageAt       time.Time   `json:"stage_at,omitzero"`
	FurthestStage marks.Stage `json:"furthest_stage,omitempty"`

	// Display-only insight (jobsinsight.go): freshness, scores, keywords and
	// the note split into reasons. No engine rule reads them.
	FreshAt        time.Time            `json:"fresh_at,omitzero"`
	FreshSrc       string               `json:"fresh_src,omitempty"` // posted or seen
	AgeDays        *int                 `json:"age_days,omitempty"`
	Relevance      float64              `json:"relevance"`
	RelevanceBasis jobinsight.Basis     `json:"relevance_basis,omitempty"`
	RelevanceKnown float64              `json:"relevance_known,omitempty"` // 0-1 share of the additive weight relevance rests on
	Priority       float64              `json:"priority"`
	ScoreFactors   []jobinsight.Factor  `json:"score_factors,omitempty"`
	Keywords       []jobinsight.Keyword `json:"keywords,omitempty"`
	Reasons        []Reason             `json:"reasons,omitempty"`
	Semantic       *Semantic            `json:"semantic,omitempty"` // cached knowledge-base match (semanticwire.go)
	// Places, RemoteListed and RemoteScope are what the board's location says
	// (internal/location), not triage's work_mode reading.
	Places       []Place `json:"places,omitempty"`
	RemoteListed bool    `json:"remote_listed,omitempty"`
	RemoteScope  string  `json:"remote_scope,omitempty"`
	// Pay is the pay the posting's text states (jobinsight.ParsePay), at most
	// one range per currency.
	Pay []jobinsight.Pay `json:"pay,omitempty"`

	// Near-duplicate display (pipeline.AnnotateNearDuplicates): the posting
	// this one repeats (status duplicate), may repeat (candidate) or is the
	// same role elsewhere as (sibling), and the description overlap behind it.
	DupOf          string  `json:"dup_of,omitempty"`
	DupCandidateOf string  `json:"dup_candidate_of,omitempty"`
	DupSiblingOf   string  `json:"dup_sibling_of,omitempty"`
	DupScore       float64 `json:"dup_score,omitempty"`
	// RepeatApply warns that other postings at this company were applied to
	// lately and Repost that this is likely an earlier role relisted
	// (internal/samecompany); notes for a person, never a filter.
	RepeatApply *RepeatApplyNote `json:"repeat_apply,omitempty"`
	Repost      *RepostNote      `json:"repost,omitempty"`
	// Logo is the absolute path of the company's cached icon (internal/logo),
	// set only when the file is there; the app shows the monogram otherwise.
	Logo string `json:"logo,omitempty"`
	// Row is the versioned contract every surface reads (internal/jobview).
	Row *Row `json:"row,omitempty"`
	// ManualTriage is the reading the owner had a Claude model make by hand
	// (`vani jobs retriage`), shown whether or not ranking still uses it. Display
	// only: it orders the list when Current and never drops or undrops a posting.
	ManualTriage *ManualTriage `json:"manual_triage,omitempty"`
	// Resume is the job's tailored resume (internal/resumestore): nil when it has
	// none, Error set when the store could not be read. Display only.
	Resume *ResumeSummary `json:"resume,omitempty"`
	// ApplyFormNote is one line about the posting's application form (see
	// ApplyFormNote): its flagged questions, or "not read" for an Ashby
	// posting whose form was not. ApplyForm is the summary itself, `jobs show`
	// only. Display only: no engine rule reads either, and nothing is dropped
	// or blocked for them.
	ApplyFormNote string             `json:"apply_form_note,omitempty"`
	ApplyForm     *posting.ApplyForm `json:"apply_form,omitempty"`
	// ApplyFlags are the flagged gates in a few words (ApplyFormFlags), for a
	// list row.
	ApplyFlags []string `json:"apply_flags,omitempty"`

	// Untriaged marks a posting no run has triaged yet (status live): it has no
	// fit, relevance, priority, keywords or row, and the reply carries none of
	// them (absent is "not known", never 0). It is set from the status, so a
	// live posting the owner saved or hid is flagged in its mark view too.
	Untriaged bool `json:"untriaged,omitempty"`
	// PrefilterDrop says the next run's prefilter would drop this untriaged
	// posting before triage: "excluded" (the config's exclusions) or "title"
	// (a title_signals.read reading a drop rule names). Only the kind, never
	// the rule: rule values can reveal the owner's work authorization.
	PrefilterDrop string `json:"prefilter_drop,omitempty"`
}

// MarshalJSON writes a Job as its struct tags say, except that an untriaged
// posting's relevance and priority are left out instead of written as 0, which
// readers would take for a very low score. Every other Job is byte-identical to
// the default encoding (HTML is not escaped, as the jobs replies are encoded).
func (j Job) MarshalJSON() ([]byte, error) {
	type plain Job // no methods, so no recursion
	var v any = plain(j)
	if j.Untriaged {
		v = struct {
			plain
			Relevance      *float64 `json:"relevance,omitempty"`
			Priority       *float64 `json:"priority,omitempty"`
			RelevanceKnown *float64 `json:"relevance_known,omitempty"`
		}{plain: plain(j)}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// ViewPending is the view of postings no run has triaged yet (status live).
// It is the only view with no fit, relevance or priority to show, and it is
// never mixed into the others: "new" and "all" list triaged roles only.
const ViewPending = "pending"

// InView reports whether a record with mark belongs in view. The pending view,
// like "new", leaves out what the owner marked: a saved or hidden posting is in
// its mark view, so Pending.Total and the view's count agree.
func InView(view string, r ledger.Record, mark marks.Mark) bool {
	switch view {
	case "new":
		return mark == marks.None && KeptStatus(r.Status)
	case ViewPending:
		return mark == marks.None && r.Status == posting.Live
	case "all":
		return mark != marks.None || KeptStatus(r.Status)
	}
	return string(mark) == view
}

func ToJob(r ledger.Record, entry marks.Entry) Job {
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

// CleanText makes untrusted posting text safe to show as plain text: control
// characters (and, unless keepLines, newlines) become single spaces, runs of
// spaces collapse, and the result is cut at maxRunes. It does no Markdown
// escaping; the app shows it verbatim, never as Markdown or HTML.
func CleanText(s string, maxRunes int, keepLines bool) string {
	var b strings.Builder
	n, space, newline := 0, false, false
	for _, r := range strings.ToValidUTF8(s, "�") {
		if keepLines && r == '\n' {
			newline = b.Len() > 0
			space = false
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == ' ' || r == ' ' {
			space = b.Len() > 0
			continue
		}
		// A pending separator is written with this rune, so it counts too.
		sep := 0
		if newline || space {
			sep = 1
		}
		if n+sep >= maxRunes {
			b.WriteString("…")
			break
		}
		switch {
		case newline:
			b.WriteByte('\n')
			n++
		case space:
			b.WriteByte(' ')
			n++
		}
		newline, space = false, false
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// RowOf is the one place a Job becomes the versioned row contract
// (internal/jobview) the Mac app and the browser extension read. Every list,
// show, mark and find reply gets its row from here, after enrich has set the
// score, reasons and age.
func RowOf(j Job) Row {
	return Build(Inputs{
		Key: string(j.Key), Title: j.Title, Company: j.Company, Location: j.Location,
		Status: string(j.Status), Relevance: j.Relevance, Priority: j.Priority,
		RelevanceBasis: string(j.RelevanceBasis), RoleFit: j.Triage["role_fit"],
		Reasons:  j.Reasons,
		DupLinks: []string{j.DupOf, j.DupCandidateOf, j.DupSiblingOf},
		AgeDays:  j.AgeDays, FreshSrc: j.FreshSrc,
	})
}

// ManualTriage is a job's manual reading as `vani jobs` reports it. Current false
// means the posting text changed (or is gone) since the model read it, so the
// ranking ignores the reading; StaleReason then says which ("text_changed" or
// "text_gone").
type ManualTriage struct {
	Slots       map[string]string `json:"slots"`
	Model       string            `json:"model"`
	At          time.Time         `json:"at"`
	Current     bool              `json:"current"`
	StaleReason string            `json:"stale_reason,omitempty"`
}

// Semantic is a job's knowledge-base match as `vani jobs` reports it.
type Semantic struct {
	Score    float64             `json:"score"`
	Evidence []semantic.Evidence `json:"evidence,omitempty"`
	// Thin: the posting had too little text; the ranker treated it as unknown.
	Thin bool `json:"thin,omitempty"`
}
