package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/eval"
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
		fmt.Fprint(e.stdout, rep.Markdown())
		// The drop-candidate agreement needs no labels, only the ledger. If it
		// cannot be read the section says so: that is not "no data".
		fmt.Fprint(e.stdout, e.dropAgreementMarkdown(done))
		return nil
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
	keysOf := func(sel []selected) []posting.Key {
		out := make([]posting.Key, 0, len(sel))
		for _, x := range sel {
			out = append(out, x.job.Key)
		}
		return out
	}
	orderings := []eval.Ordering{
		{Name: "New", Keys: keysOf(newView)},
		{Name: "Top-open", Keys: keysOf(topOpen)},
	}
	rep := eval.BuildRelevance(done, b.snap.Records, orderings, kk, names)
	rep.Malformed = bad
	fmt.Fprint(e.stdout, rep.Markdown())
	fmt.Fprint(e.stdout, eval.BuildDropAgreement(done, b.snap.Records, specs, names).Markdown())
	return nil
}

// dropAgreementMarkdown is the drop-candidate agreement section for a run that
// has not read the ledger yet; a ledger or config that cannot be read gives a
// line saying so, never an empty table.
func (e *env) dropAgreementMarkdown(done map[posting.Key]eval.Entry) string {
	names, specs, err := e.namesAndDropRules()
	if err != nil {
		return fmt.Sprintf("\n## Drop candidates\n\nnot computed: %v\n", err)
	}
	b, err := e.browse()
	if err != nil {
		return fmt.Sprintf("\n## Drop candidates\n\nnot computed: ledger unreadable: %v\n", err)
	}
	return eval.BuildDropAgreement(done, b.snap.Records, specs, names).Markdown()
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
	specs := make([]eval.DropRuleSpec, 0, len(cfg.DropRules))
	for _, r := range cfg.DropRules {
		specs = append(specs, eval.DropRuleSpec{Slot: r.Slot, Values: r.Values, MinP: r.MinOpenRouterProb})
	}
	return n, specs, nil
}
