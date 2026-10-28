package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/posting"
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
	asJSON := e.jsonFlag(fs, `{"relevance","drop_agreement"} on stdout: the raw counts the tables are built from`)
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
	// With nothing judged there is nothing to order or score: no ledger read.
	if rep := eval.BuildRelevance(done, nil, nil, nil, eval.Names{}); rep.Judged == 0 {
		rep.Malformed = bad
		// The drop-candidate agreement needs no labels, only the ledger. If it
		// cannot be read the reply says so: that is not "no data".
		agree, err := e.dropAgreement(done)
		out := relevanceReply{Relevance: rep, DropAgreement: agree}
		if err != nil {
			out.DropAgreement, out.DropAgreementError = nil, err.Error()
		}
		return e.reply(*asJSON, out, nil, func() {
			fmt.Fprint(e.stdout, rep.Markdown())
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
	return e.reply(*asJSON, relevanceReply{Relevance: rep, DropAgreement: &agree}, nil, func() {
		fmt.Fprint(e.stdout, rep.Markdown())
		fmt.Fprint(e.stdout, agree.Markdown())
	})
}

// relevanceReply is `vani relevance --json`. DropAgreementError replaces
// DropAgreement when the ledger or config could not be read for it.
type relevanceReply struct {
	Relevance          eval.Relevance      `json:"relevance"`
	DropAgreement      *eval.DropAgreement `json:"drop_agreement,omitempty"`
	DropAgreementError string              `json:"drop_agreement_error,omitempty"`
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
