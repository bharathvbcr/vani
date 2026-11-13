package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
)

// maxPrecisionK bounds one -k value.
const maxPrecisionK = 1000

// cmdRelevance prints what the owner's would_apply labels measure: precision@k
// of the New and Top-open orderings as they stand now, the share wanted per
// score decile and per drop rule, and each backend's p against the owner's
// answers. Raw counts with Wilson intervals; zero labels prints "no data".
func (e *env) cmdRelevance(args []string) error {
	fs := e.newFlags("relevance")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	ks := fs.String("k", "", "comma list of k for precision@k (default 5,10,20)")
	whatIf := fs.String("what-if-summary", "", "a file holding a draft target_summary: report what it would do to the judged postings' order (keyword and semantic parts, computed locally, nothing written)")
	asJSON := e.jsonFlag(fs, `{"relevance","drop_recall","teacher_proxy","drop_agreement","readers","part_auc"} on stdout: the raw counts the tables are built from`)
	if err := parse(fs, args); err != nil {
		return err
	}
	kk := eval.DefaultPrecisionKs
	if *ks != "" {
		kk = nil
		for _, f := range strings.Split(*ks, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil || v < 1 || v > maxPrecisionK {
				return usagef("relevance: -k wants whole numbers within 1..%d, got %q", maxPrecisionK, f)
			}
			kk = append(kk, v)
		}
	}
	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	// A label set that cannot be read gives no figures at all: a report over
	// part of it would look like a measurement.
	done, bad, err := s.Labels()
	if err != nil {
		return fmt.Errorf("labels unreadable, refusing to report: %w", err)
	}
	// sampled is done less the disagreement-cell labels, for the reports below
	// that read the labels as a sample and have no exclusion of their own (the
	// seniority shadow, the summary what-if, the score parts). The relevance
	// counts, drop recall and the reader table exclude them themselves and say
	// how many; drop agreement excludes them without a count (it counts only
	// labelled disagreements); the yes yield is where they are counted.
	sampled, _ := eval.WithoutSelectedOnReading(done)
	yield := eval.BuildYesYield(done)
	// With nothing judged there is nothing to order or score: no ledger read.
	if rep := eval.BuildRelevance(done, nil, nil, nil, eval.Names{}); rep.Judged == 0 {
		if *whatIf != "" {
			return errors.New("relevance --what-if-summary: no judged would_apply labels yet (run `vani audit`); there is no order to compare")
		}
		rep.Malformed = bad
		// The drop-candidate agreement needs no labels, only the ledger. If it
		// cannot be read the reply says so: that is not "no data".
		agree, err := e.dropAgreement(done)
		recall := eval.InsufficientDropRecall("no judged would_apply labels yet (run `vani audit`)")
		out := relevanceReply{Relevance: rep, DropAgreement: agree, DropRecall: &recall, YesYield: &yield}
		if err != nil {
			out.DropAgreement, out.DropAgreementError = nil, err.Error()
		}
		return e.reply(*asJSON, out, nil, func() {
			fmt.Fprint(e.stdout, rep.Markdown())
			fmt.Fprint(e.stdout, yield.Markdown())
			fmt.Fprint(e.stdout, recall.Markdown())
			if err != nil {
				fmt.Fprintf(e.stdout, "\n## Drop candidates\n\nnot computed: %v\n", err)
			} else {
				fmt.Fprint(e.stdout, agree.Markdown())
			}
		})
	}
	names, specs, err := e.namesAndDropRules()
	if err != nil {
		return err
	}
	b, err := e.browse()
	if err != nil {
		return err
	}
	newView, err := b.view("new", "default", false)
	if err != nil {
		return err
	}
	topOpen, err := b.view("new", "priority", true)
	if err != nil {
		return err
	}
	keysOf := func(sel []jobview.Selected) []posting.Key {
		out := make([]posting.Key, 0, len(sel))
		for _, x := range sel {
			out = append(out, x.Job.Key)
		}
		return out
	}
	orderings := []eval.Ordering{
		{Name: "New", Keys: keysOf(newView)},
		{Name: "Top-open", Keys: keysOf(topOpen)},
	}
	rep := eval.BuildRelevance(done, b.snap.Records, orderings, kk, names)
	rep.Malformed = bad
	agree := eval.BuildDropAgreement(done, b.snap.Records, specs, names)
	rules := make([]eval.DropRule, 0, len(specs))
	for _, s := range specs {
		rules = append(rules, eval.DropRule{Slot: s.Slot, Values: s.Values})
	}
	recall := eval.BuildDropRecall(done, b.recallMembers(rules))
	proxy := eval.BuildTeacherDropProxy(b.snap.Records, rules, e.openTeacherReadings())
	store, status, detail := e.openTeacherLog()
	readers := eval.BuildReaderAgreement(done, b.snap.Records, store, status, names)
	readers.TeacherDetail = detail
	out := relevanceReply{Relevance: rep, DropAgreement: &agree, DropRecall: &recall, TeacherProxy: &proxy, Readers: &readers, YesYield: &yield}
	base := jobinsight.DefaultWeights()
	items, itemsErr := e.judgedItems(sampled, b.snap, base, nil)
	if itemsErr != nil {
		out.PartAUCError = itemsErr.Error()
	} else {
		out.PartAUC = partAUCOf(items, base)
	}
	eng := engineeringOf(b.snap)
	sens := buildSensitivity(sampled, b.snap.Records, store, status, names, items, itemsErr, base, eng)
	out.Sensitivity = &sens
	shadow := e.buildSeniorityShadow(sampled, b.snap)
	out.SeniorityShadow = &shadow
	if *whatIf != "" {
		ctx, stop := signalContext()
		w := e.buildSummaryWhatIf(ctx, sampled, b.snap, *whatIf)
		stop()
		out.SummaryWhatIf = &w
	}
	return e.reply(*asJSON, out, nil, func() {
		fmt.Fprint(e.stdout, rep.Markdown())
		fmt.Fprint(e.stdout, yield.Markdown())
		fmt.Fprint(e.stdout, recall.Markdown())
		fmt.Fprint(e.stdout, proxy.Markdown())
		fmt.Fprint(e.stdout, agree.Markdown())
		fmt.Fprint(e.stdout, readers.Markdown())
		fmt.Fprint(e.stdout, "\n## Score parts against the owner\n")
		if out.PartAUCError != "" {
			fmt.Fprintf(e.stdout, "\nnot computed: %s\n", out.PartAUCError)
		}
		for _, p := range out.PartAUC {
			fmt.Fprint(e.stdout, p.Markdown())
		}
		fmt.Fprint(e.stdout, sens.Markdown())
		fmt.Fprint(e.stdout, shadow.Markdown())
		if out.SummaryWhatIf != nil {
			fmt.Fprint(e.stdout, out.SummaryWhatIf.Markdown())
		}
	})
}

// engineeringOf reports eval.TitleIsEngineering of each posting in snap, by
// key, from its title as the ledger holds it; an unknown key is false.
func engineeringOf(snap ledger.Snapshot) func(posting.Key) bool {
	titles := make(map[posting.Key]string, len(snap.Records))
	for _, r := range snap.Records {
		if _, ok := titles[r.Key]; !ok {
			titles[r.Key] = r.Posting.Title
		}
	}
	return func(k posting.Key) bool {
		t, ok := titles[k]
		return ok && eval.TitleIsEngineering(t)
	}
}

// partAUCOf is BuildPartAUC over each scope of judged items.
func partAUCOf(items []rankfit.Item, base jobinsight.Weights) []rankfit.PartAUC {
	out := make([]rankfit.PartAUC, 0, len(partAUCScopes))
	for _, s := range partAUCScopes {
		out = append(out, rankfit.BuildPartAUC(items, base, s.name, s.keep, rankfit.DefaultPartAUCResamples, 1))
	}
	return out
}

// partAUCScopes are the judged postings the part AUC is computed over: all of
// them, and the scored stratum alone (the postings the ranking orders).
var partAUCScopes = []struct {
	name string
	keep func(*rankfit.Item) bool
}{
	{"all judged postings", nil},
	{"scored stratum (score deciles)", func(it *rankfit.Item) bool { return rankfit.StratumOf(it.Source) == rankfit.StratumScored }},
}

// judgedItems builds each audit-judged posting's Score parts exactly as `vani
// rankfit` does (rankfitItems) under base. Marks and impressions are read for
// the scores the owner saw and the company factor; neither becomes a label
// here. keep, when not nil, admits only the postings it is true for (`vani
// teacher report` keeps the gold labels made after the exemplar freeze). A
// source that cannot be read is an error, never an empty table.
func (e *env) judgedItems(done map[posting.Key]eval.Entry, snap ledger.Snapshot, base jobinsight.Weights, keep func(posting.Key) bool) ([]rankfit.Item, error) {
	me, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		return nil, fmt.Errorf("read marks: %w", err)
	}
	shown, err := feedback.ReadImpressions(e.stateDir)
	if err != nil {
		return nil, fmt.Errorf("read impressions: %w", err)
	}
	targets, _, err := rankfit.Targets(done, shown.Rows, nil)
	if err != nil {
		return nil, err
	}
	for k, t := range targets {
		if !t.Judged || (keep != nil && !keep(k)) {
			delete(targets, k) // passed-over impressions: not the owner's answer
		}
	}
	items, _, _, err := e.rankfitItems(snap, me, targets, base)
	return items, err
}

// relevanceReply is `vani relevance --json`. DropAgreementError replaces
// DropAgreement when the ledger or config could not be read for it.
// DropRecall is the weighted estimate of wanted postings the engine dropped
// (status "insufficient" with a reason until the labels can carry it), and
// TeacherProxy the model-against-model count of candidate misses; with no
// judged labels the ledger is not read and TeacherProxy is absent.
type relevanceReply struct {
	Relevance eval.Relevance `json:"relevance"`
	// YesYield is the raw yes yield per stratum and disagreement cell, with the
	// yes-rich stratum named when one is clearly above the random strata.
	YesYield *eval.YesYield `json:"yes_yield,omitempty"`

	DropRecall         *eval.DropRecall       `json:"drop_recall,omitempty"`
	TeacherProxy       *eval.TeacherDropProxy `json:"teacher_proxy,omitempty"`
	DropAgreement      *eval.DropAgreement    `json:"drop_agreement,omitempty"`
	DropAgreementError string                 `json:"drop_agreement_error,omitempty"`
	// Readers is every triage reader (engine backend, teacher model) against
	// the owner's would_apply; PartAUC each Score part's AUC on the judged
	// postings, or PartAUCError why it could not be computed. Absent with no
	// judged labels.
	Readers      *eval.ReaderAgreement `json:"readers,omitempty"`
	PartAUC      []rankfit.PartAUC     `json:"part_auc,omitempty"`
	PartAUCError string                `json:"part_auc_error,omitempty"`
	// Sensitivity repeats Readers and the all-postings PartAUC under each
	// eval.LabelMode, so a verdict that holds in one reading only shows as such.
	Sensitivity *sensitivityReply `json:"sensitivity,omitempty"`
	// SeniorityShadow is what sparing mismatch_seniority at some levels would
	// do to the judged postings' order (measurement only).
	SeniorityShadow *seniorityShadow `json:"seniority_shadow,omitempty"`
	// SummaryWhatIf is present with --what-if-summary only.
	SummaryWhatIf *summaryWhatIf `json:"summary_what_if,omitempty"`
}

// dropAgreement is the drop-candidate agreement for a run that has not read
// the ledger yet; a ledger or config that cannot be read is an error, never an
// empty table.
func (e *env) dropAgreement(done map[posting.Key]eval.Entry) (*eval.DropAgreement, error) {
	names, specs, err := e.namesAndDropRules()
	if err != nil {
		return nil, err
	}
	b, err := e.browse()
	if err != nil {
		return nil, fmt.Errorf("ledger unreadable: %w", err)
	}
	a := eval.BuildDropAgreement(done, b.snap.Records, specs, names)
	return &a, nil
}

// namesAndDropRules loads the config once for the OpenRouter backend's name
// (see evalNames) and the drop rules the agreement report reads.
func (e *env) namesAndDropRules() (eval.Names, []eval.DropRuleSpec, error) {
	cfg, err := e.loadConfig()
	if err != nil {
		return eval.Names{}, nil, fmt.Errorf("relevance needs the config to name the OpenRouter backend and read the drop rules: %w", err)
	}
	var n eval.Names
	if cfg.OpenRouterConfigured() {
		n.OpenRouter = cfg.Decide.OpenRouterName()
	}
	if cfg.LLM.Provider != "" {
		n.LLMModel = cfg.LLM.EffectiveTriageModel()
	}
	specs := make([]eval.DropRuleSpec, 0, len(cfg.DropRules))
	for _, r := range cfg.DropRules {
		specs = append(specs, eval.DropRuleSpec{Slot: r.Slot, Values: r.Values, MinP: r.MinOpenRouterProb})
	}
	return n, specs, nil
}
