package pipeline

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/decide"
)

// maxExplanationRunes bounds the explanation annotation (ledger values are short strings).
const maxExplanationRunes = 1000

// ErrStageMissing means Advance needed a stage the Runner was built without.
var ErrStageMissing = errors.New("pipeline: stage not configured")

// ErrTriageUnavailable means no backend could read the posting at all (budget
// spent, provider down, every call timed out). The posting stays Live and the
// next run asks again; only a real reading can make it Unsure.
var ErrTriageUnavailable = errors.New("pipeline: no decision backend could read the posting")

// noReading is true when every slot of a non-empty answer is an outage reading
// (Unavailable or NotAsked): no backend read the posting, so it stays Live and
// the next run asks again. A Failed slot is a backend's unusable reading, not
// an outage (see outageReading); an answer with one goes to the DropPolicy and
// becomes Unsure.
func noReading(a decide.Answer) bool {
	if len(a.Slots) == 0 {
		return false
	}
	for _, sr := range a.Slots {
		if !outageReading(sr.Reading) {
			return false
		}
	}
	return true
}

// Triager asks the decision backends about a posting. Its answer only
// annotates; the DropPolicy decides what happens.
type Triager interface {
	Triage(ctx context.Context, p posting.Posting) (decide.Answer, error)
}

// Explainer writes a short human-readable reason a posting ranked as it did.
type Explainer interface {
	Explain(ctx context.Context, r ledger.Record) (string, error)
}

// Runner advances postings one stage at a time.
type Runner struct {
	Ledger    ledger.Ledger
	Triager   Triager
	Scorer    resume.Scorer
	Explainer Explainer
	Policy    DropPolicy
	Now       func() time.Time
	// SkipScoring and SkipExplain switch a stage off on purpose (DevPrism not
	// set up yet, no LLM configured). The posting still moves forward, with a
	// reason saying the stage was skipped. A nil Scorer or Explainer without
	// the matching Skip flag is a configuration error, never a silent skip.
	SkipScoring bool
	SkipExplain bool
	// Badger, when set, attaches the sponsorship-history badge for the
	// posting's company at triage. Annotation only; no rule reads it.
	Badger Badger
	// Employer, when set, attaches the employer-attribute badge at triage,
	// after the status is decided, so it can never change the status.
	Employer EmployerLookup
	// PreferEmployers are the employer attributes the active track favours.
	// A posting whose confident employer attributes include one has
	// ledger.EmployerRankBoost added to its rank at scoring. Status never changes.
	PreferEmployers []string
	// Funnel holds the spend and fail-open hooks (see triagestep.go).
	Funnel
}

// Badger returns a short sponsorship-history badge for a company.
type Badger interface {
	Badge(company string) string
}

// EmployerInfo is an employer lookup result: the badge text, and the
// attributes of a confident match (empty for a weak or no match).
type EmployerInfo struct {
	Badge      string
	Attributes []string
}

// EmployerLookup looks a company up in the employer-attribute index.
type EmployerLookup interface {
	Employer(company string) EmployerInfo
}

// preferredEmployer reports whether company's confident employer attributes
// include one the active track prefers. It looks the company up again rather
// than storing attributes in the ledger: which employers the owner prefers
// implies work authorization and stays out of it.
func (r *Runner) preferredEmployer(company string) bool {
	if r.Employer == nil || len(r.PreferEmployers) == 0 || company == "" {
		return false
	}
	for _, a := range r.Employer.Employer(company).Attributes {
		if contains(r.PreferEmployers, a) {
			return true
		}
	}
	return false
}

// maxMissing bounds how many uncovered requirements are kept per posting.
const maxMissing = 6

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) emit(ctx context.Context, key posting.Key, st posting.Status, reason string, ann map[string]string) error {
	return r.Ledger.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: r.now(), Key: key,
		Status: st, Annotations: ann, Reason: reason})
}

// Advance moves the record forward through every stage it can complete:
// Live to Triaged, Unsure or Dropped, then Scored, then Explained. A stage
// error returns immediately and leaves the record at the last status that
// was successfully recorded.
func (r *Runner) Advance(ctx context.Context, key posting.Key) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rec, ok := r.Ledger.Get(key)
		if !ok {
			return fmt.Errorf("pipeline: unknown key %q", key)
		}
		switch rec.Status {
		case posting.Live:
			if r.Triager == nil {
				return fmt.Errorf("%w: triager", ErrStageMissing)
			}
			// read: exclusion or title drop, too-short description, inherited
			// reading or the backends (triagestep.go).
			rd, err := r.read(ctx, key, rec)
			if err != nil {
				return err
			}
			st, reason, ann := rd.status, rd.reason, rd.ann
			if st != posting.Dropped {
				maps.Copy(ann, rd.pre)
			}
			// This triage replaces any earlier one whole: a key it did not set
			// (a shadow reading from a run that sampled the shadow, a value the
			// new answer abstained on) is forgotten, so the eval never pairs
			// this run's primary with another run's shadow.
			for k := range rec.Annotations {
				if _, set := ann[k]; !set && (strings.HasPrefix(k, ledger.AnnTriagePrefix) || k == ledger.AnnCategory) {
					// The category is derived from the triage.category reading
					// (triageAnnotations), so an answer with none forgets it too.
					ann[k] = ""
				}
			}
			if r.Badger != nil && rec.Posting.Company != "" {
				if b := r.Badger.Badge(rec.Posting.Company); b != "" {
					ann[ledger.AnnLCA] = b
				}
			}
			if r.Employer != nil && rec.Posting.Company != "" {
				info := r.Employer.Employer(rec.Posting.Company)
				if info.Badge != "" {
					ann[ledger.AnnEmployer] = info.Badge
				}
			}
			if st == posting.Unsure {
				ann["triage.unsure"] = reason
			}
			switch {
			case rd.ans.Backend == ExcludeBackend:
				ann[AnnExcluded] = reason
			case rec.Annotations[AnnExcluded] != "":
				ann[AnnExcluded] = ""
			}
			if err := r.emit(ctx, key, st, reason, ann); err != nil {
				return err
			}
			if st == posting.Dropped {
				return nil
			}
		case posting.Triaged, posting.Unsure:
			if r.SkipScoring {
				if err := r.emit(ctx, key, posting.Scored, "scoring disabled", nil); err != nil {
					return err
				}
				continue
			}
			if r.Scorer == nil {
				return fmt.Errorf("%w: scorer", ErrStageMissing)
			}
			ann, why, err := r.scoreStage(ctx, rec)
			if err != nil {
				return err
			}
			if err := r.emit(ctx, key, posting.Scored, why, ann); err != nil {
				return err
			}
		case posting.Scored:
			if r.SkipExplain {
				return r.emit(ctx, key, posting.Explained, "explanations disabled", nil)
			}
			if r.Explainer == nil {
				return fmt.Errorf("%w: explainer", ErrStageMissing)
			}
			if handled, err := r.gateNotes(ctx, rec); handled || err != nil {
				return err
			}
			text, err := r.Explainer.Explain(ctx, rec)
			if err != nil {
				return fmt.Errorf("pipeline: explain %s: %w", key, err)
			}
			if text == "" {
				return fmt.Errorf("pipeline: explain %s: empty explanation", key)
			}
			if rs := []rune(text); len(rs) > maxExplanationRunes {
				text = string(rs[:maxExplanationRunes])
			}
			if err := r.emit(ctx, key, posting.Explained, "explained", map[string]string{ledger.AnnExplanation: text, AnnNotesDeferred: ""}); err != nil {
				return err
			}
			return nil
		default:
			return nil // Explained, or terminal: nothing further to run
		}
	}
}

// scoreAnnotations asks DevPrism about rec's description and returns what a
// score records: the rank (must-have coverage, plus ledger.EmployerRankBoost at a
// preferred employer), the coverage, the matched block count and the uncovered
// must-haves. It is the one owner of those annotations; Advance and Rescore
// both write what it returns.
func (r *Runner) scoreAnnotations(ctx context.Context, rec ledger.Record) (map[string]string, error) {
	if r.Scorer == nil {
		return nil, fmt.Errorf("%w: scorer", ErrStageMissing)
	}
	gap, err := r.Scorer.Gap(ctx, rec.Posting.Description, "")
	if err != nil {
		return nil, fmt.Errorf("pipeline: gap %s: %w", rec.Key, err)
	}
	sc, err := r.Scorer.Score(ctx, rec.Posting.Description, "")
	if err != nil {
		return nil, fmt.Errorf("pipeline: score %s: %w", rec.Key, err)
	}
	if gap.NoMustHaves {
		// DevPrism found no must-have to measure, so its 0% is 0 of 0: an
		// unknown, not a zero. Record no rank and no coverage (an empty value
		// forgets a stale one) so the ranker applies its unknown-coverage prior,
		// and count it: the posting is kept with a check that could not run.
		r.FailOpen.Note(CauseCoverageUnknown)
		return map[string]string{
			ledger.AnnRank: "", ledger.AnnCoverage: "", ledger.AnnMissing: "",
			ledger.AnnBlocks:          strconv.Itoa(len(sc.BlockIDs)),
			ledger.AnnCoverageUnknown: ledger.CoverageNoMustHaves,
		}, nil
	}
	rank := gap.CoveragePct
	if r.preferredEmployer(rec.Posting.Company) {
		rank += ledger.EmployerRankBoost
	}
	ann := map[string]string{
		ledger.AnnRank:            strconv.FormatFloat(rank, 'f', -1, 64),
		ledger.AnnCoverage:        strconv.FormatFloat(gap.CoveragePct, 'f', -1, 64),
		ledger.AnnBlocks:          strconv.Itoa(len(sc.BlockIDs)),
		ledger.AnnCoverageUnknown: "",
	}
	if m := missingRequirements(gap); m != "" {
		ann[ledger.AnnMissing] = m
	}
	return ann, nil
}

// missingRequirements lists the must-haves DevPrism found missing or weak,
// missing first, capped in count and length. The requirement names come from
// the JD, so they are untrusted text wherever they are shown or prompted.
func missingRequirements(g resume.Gap) string {
	var missing, weak []string
	for _, c := range g.MustHaves {
		name := strings.TrimSpace(c.Requirement)
		if rs := []rune(name); len(rs) > 60 {
			name = string(rs[:60])
		}
		switch {
		case name == "":
		case c.Status == "missing":
			missing = append(missing, name)
		case c.Status == "weak":
			weak = append(weak, name+" (weak)")
		}
	}
	all := append(missing, weak...)
	if len(all) > maxMissing {
		all = all[:maxMissing]
	}
	return strings.Join(all, "; ")
}

// MarkDigested records that keys were included in a digest. Already-digested
// keys are left alone; an unknown key is an error.
func (r *Runner) MarkDigested(ctx context.Context, keys []posting.Key) error {
	return MarkDigested(ctx, r.Ledger, keys, r.now(), "digested")
}

// MarkDigested is the one path that records a digest's roles as Digested (the
// daily run's publishDigest and Runner.MarkDigested). Every key is checked
// before anything is written (an unknown key is an error and writes nothing),
// already-digested keys and repeats are left alone, and the events go out in
// batches of at most ledger.MaxBatchEvents events and appendBatched's byte
// budget: one fsync per batch, not per role.
// A failed batch returns its error at once and leaves the later batches
// unwritten; each batch is all-or-nothing, so what was acknowledged is exactly
// the earlier batches.
func MarkDigested(ctx context.Context, l ledger.Ledger, keys []posting.Key, at time.Time, reason string) error {
	evs := make([]ledger.Event, 0, len(keys))
	seen := make(map[posting.Key]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		rec, ok := l.Get(k)
		if !ok {
			return fmt.Errorf("pipeline: digest unknown key %q", k)
		}
		if rec.Status == posting.Digested {
			continue
		}
		evs = append(evs, ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: posting.Digested, Reason: reason})
	}
	done, err := appendBatched(ctx, l, evs)
	if err != nil {
		return fmt.Errorf("pipeline: digest marks: %d of %d written before: %w", done, len(evs), err)
	}
	return nil
}

func triageAnnotations(a decide.Answer) map[string]string {
	ann := map[string]string{ledger.AnnTriagePrefix + "backend": a.Backend}
	for name, sr := range a.Slots {
		if sr.Value != "" {
			ann[ledger.AnnTriagePrefix+name] = sr.Value
			if name == "category" {
				ann[ledger.AnnCategory] = sr.Value
			}
		}
		ann[ledger.AnnTriagePrefix+name+".reading"] = string(sr.Reading)
		if b := slotBackend(a, sr); b != "" {
			ann[ledger.AnnTriagePrefix+name+".backend"] = b
		}
		// Calibration and shadow keys are evidence for the eval report only;
		// DropPolicy reads the answer, never these.
		if sr.Jev != nil {
			ann[ledger.AnnTriagePrefix+name+ledger.AnnTriageSuffixP] = strconv.FormatFloat(sr.Jev.Probability, 'f', 4, 64)
		}
		if sr.Lappi != nil {
			ann[ledger.AnnTriagePrefix+name+ledger.AnnTriageSuffixLappiScore] = strconv.FormatFloat(sr.Lappi.Score, 'f', 4, 64)
		}
		if sh, ok := a.Shadow[name]; ok {
			ann[ledger.AnnTriagePrefix+name+ledger.AnnTriageSuffixShadowReading] = string(sh.Reading)
			if b := slotBackend(decide.Answer{}, sh); b != "" {
				ann[ledger.AnnTriagePrefix+name+ledger.AnnTriageSuffixShadowBackend] = b
			}
			if sh.Reading == decide.Answered {
				ann[ledger.AnnTriagePrefix+name+ledger.AnnTriageSuffixShadow] = sh.Value
				if sr.Reading == decide.Answered && sr.Value != sh.Value {
					ann[ledger.AnnTriagePrefix+name+ledger.AnnTriageSuffixDisagree] = "true"
				}
			}
		}
	}
	return ann
}

// slotBackend names the backend whose reading sr is. The router merges slots
// answered by different backends and tags each one "backend=<name>" in
// Detail, so the whole answer's Backend ("d1-ultra+llm") cannot be used to pick a
// calibration. Without a tag (a single backend answered directly) the
// answer's Backend is the slot's.
func slotBackend(a decide.Answer, sr decide.SlotReading) string {
	for _, part := range strings.Split(sr.Detail, ";") {
		if b, ok := strings.CutPrefix(strings.TrimSpace(part), "backend="); ok {
			return b
		}
	}
	return a.Backend
}

// DropRule drops a posting whose Slot is answered with one of Values by a
// backend whose calibration clears that backend's threshold. A threshold of
// zero disables dropping on that backend, so an unset field never drops. A
// title reading (EvaluateTitle) is deterministic and has no threshold: the
// rule drops it whenever its slot and value are read from the title.
type DropRule struct {
	Slot              string
	Values            []string
	MinOpenRouterProb float64
	MinLappiScore     float64
}

// DropPolicy is Rule 7: decisions annotate, this engine rule decides.
type DropPolicy struct {
	Rules []DropRule
	// OpenRouter names the backend whose probability MinOpenRouterProb is
	// compared with: the configured OpenRouter decision model
	// (config.Decide.OpenRouterName). Empty means no backend drops on a
	// probability. The name is matched exactly, so another backend speaking
	// the same wire shape (the local shadow) never drops.
	OpenRouter string
	// ReadTitle reads triage slot values from a posting's title alone
	// (config.TitleSignals.Reads); nil reads nothing.
	ReadTitle func(title string) []TitleReading
	// Exclude reports why the owner's exclusions (config.Exclude.Match) drop a
	// posting; nil excludes nothing. It reads the posting's own fields, never
	// a model's reading, so it drops with no threshold.
	Exclude func(p posting.Posting) (why string, ok bool)
	// ExcludeUnreadable reports that a location rule of Exclude could not read
	// the posting's location, so Exclude kept it without having judged it
	// (config.Exclude.LocationUnreadable); nil reports nothing. The exclusion
	// stage counts such a posting as kept unchecked (CauseLocationUnreadable).
	ExcludeUnreadable func(p posting.Posting) bool
}

// TitleBackend is the backend name a title reading is recorded under.
const TitleBackend = "title"

// ExcludeBackend is the triage backend recorded on a posting an exclusion
// dropped before any backend was asked.
const ExcludeBackend = "exclude"

// AnnExcluded is the drop reason on a posting an exclusion dropped, at triage
// or by ApplyExclusions; it is cleared when the posting is reopened.
const AnnExcluded = "exclude.matched"

// excluded reports the drop reason p's exclusions give post, if any.
func (p DropPolicy) excluded(post posting.Posting) (string, bool) {
	if p.Exclude == nil {
		return "", false
	}
	why, ok := p.Exclude(post)
	if !ok {
		return "", false
	}
	return "excluded: " + why, true
}

// Prefilters reports whether the policy can drop a posting before any
// backend is asked: it has exclusions or reads titles.
func (p DropPolicy) Prefilters() bool { return p.Exclude != nil || p.ReadTitle != nil }

// EvaluatePosting is what triage applies before asking any backend: an
// exclusion drops first, then EvaluateTitle. dropped false means the backends
// must read the posting, and the answer is to be discarded.
func (p DropPolicy) EvaluatePosting(post posting.Posting) (a decide.Answer, reason string, dropped bool) {
	if why, ok := p.excluded(post); ok {
		return decide.Answer{Backend: ExcludeBackend}, why, true
	}
	return p.EvaluateTitle(post.Title)
}

// TitleReading is one slot value read from a title, and the phrase matched.
type TitleReading struct {
	Slot, Value, Matched string
}

// EvaluateTitle reads the title and reports whether a rule drops it. A title
// reading is deterministic, so a rule whose slot and value it matches drops
// with no calibration threshold; a ruled slot the title does not read is not
// a reason for Unsure, it is left to the backends. dropped false means the
// backends must read the posting, and the answer is to be discarded.
func (p DropPolicy) EvaluateTitle(title string) (a decide.Answer, reason string, dropped bool) {
	if p.ReadTitle == nil {
		return decide.Answer{}, "", false
	}
	reads := p.ReadTitle(title)
	if len(reads) == 0 {
		return decide.Answer{}, "", false
	}
	a = decide.Answer{Backend: TitleBackend, Slots: make(map[string]decide.SlotReading, len(reads))}
	for _, r := range reads {
		a.Slots[r.Slot] = decide.SlotReading{Reading: decide.Answered, Value: r.Value}
	}
	for _, rule := range p.Rules {
		for _, r := range reads {
			if r.Slot == rule.Slot && contains(rule.Values, r.Value) {
				return a, fmt.Sprintf("slot %q = %q: title matched %q", r.Slot, r.Value, r.Matched), true
			}
		}
	}
	return decide.Answer{}, "", false
}

// Evaluate maps an answer to Dropped, Unsure or Triaged with a reason that
// names the slot and value. A confident drop wins over an uncertain slot.
func (p DropPolicy) Evaluate(a decide.Answer) (posting.Status, string) {
	var unsure []string
	for _, rule := range p.Rules {
		sr, ok := a.Slots[rule.Slot]
		if !ok {
			unsure = append(unsure, fmt.Sprintf("slot %q missing from answer", rule.Slot))
			continue
		}
		if sr.Reading != decide.Answered {
			unsure = append(unsure, fmt.Sprintf("slot %q %s", rule.Slot, sr.Reading))
			continue
		}
		if !contains(rule.Values, sr.Value) {
			continue
		}
		if why, ok := p.calibrated(slotBackend(a, sr), sr, rule); ok {
			return posting.Dropped, fmt.Sprintf("slot %q = %q: %s", rule.Slot, sr.Value, why)
		}
	}
	if len(unsure) > 0 {
		sort.Strings(unsure)
		return posting.Unsure, unsure[0]
	}
	return posting.Triaged, "triaged"
}

// calibrated reports whether the backend's own calibration clears the rule.
// Only the configured OpenRouter model and Lappi can; an LLM answer carries
// none and never drops, and neither does any backend not named here.
func (p DropPolicy) calibrated(backend string, sr decide.SlotReading, rule DropRule) (string, bool) {
	switch {
	case p.OpenRouter != "" && backend == p.OpenRouter:
		if sr.Jev != nil && rule.MinOpenRouterProb > 0 && sr.Jev.Probability >= rule.MinOpenRouterProb {
			return fmt.Sprintf("%s p=%.3f >= %.3f", backend, sr.Jev.Probability, rule.MinOpenRouterProb), true
		}
	case backend == "lappi":
		if sr.Lappi != nil && rule.MinLappiScore > 0 && sr.Lappi.Score >= rule.MinLappiScore {
			return fmt.Sprintf("lappi score=%.3f >= %.3f", sr.Lappi.Score, rule.MinLappiScore), true
		}
	}
	return "", false
}

func contains(vs []string, v string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}
