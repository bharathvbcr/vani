package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// maxAuditQuota bounds each quota of one audit draw.
const maxAuditQuota = 100

// maxSeedBytes bounds --seed.
const maxSeedBytes = 128

// dropRules is the config's drop rules as the drop-class names need them. A
// config that cannot be read leaves every drop classed "<who>:other"; it says so.
func (e *env) dropRules() []eval.DropRule {
	cfg, err := e.loadConfig()
	if err != nil {
		fmt.Fprintf(e.stderr, "vani: drop classes unavailable (%v); dropped postings are classed <who>:other\n", err)
		return nil
	}
	out := make([]eval.DropRule, 0, len(cfg.DropRules))
	for _, r := range cfg.DropRules {
		out = append(out, eval.DropRule{Slot: r.Slot, Values: r.Values})
	}
	return out
}

// browsing is the read-only state the audit and the relevance report share: the
// ledger snapshot, the marks and the insight the Jobs views score with.
type browsing struct {
	snap ledger.Snapshot
	m    map[posting.Key]marks.Entry
	in   *insight
}

func (e *env) browse() (*browsing, error) {
	snap, m, err := e.jobsState()
	if err != nil {
		return nil, err
	}
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, m)
	if err != nil {
		return nil, err
	}
	return &browsing{snap: snap, m: m, in: in}, nil
}

// view lists the jobs of a Jobs view, as `jobs list` would, in sortBy order.
func (b *browsing) view(view, sortBy string, openOnly bool) ([]selected, error) {
	var o jobsOpts
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addSelectFlags(fs, &o)
	if err := fs.Parse(nil); err != nil {
		return nil, err
	}
	o.view, o.sort, o.open = view, sortBy, openOnly
	if err := o.check("audit", true); err != nil {
		return nil, err
	}
	sel, _ := selectJobs(b.snap, b.m, &o, b.in, 0)
	sortJobs(sel, view, sortBy)
	return sel, nil
}

// cmdAuditSample draws the week's stratified audit sample and walks the owner
// through it: would they apply to each posting. The answers go to the same
// label store `vani label` uses, as would_apply labels that record the stratum
// and the posting's inclusion probability, so `vani relevance` can count them.
func (e *env) cmdAuditSample(args []string) error {
	fs := e.newFlags("audit-sample")
	scored := fs.Int("scored", eval.AuditScored, "postings drawn across the score deciles")
	dropped := fs.Int("dropped", eval.AuditDropped, "postings drawn across the drop classes")
	seed := fs.String("seed", "", "draw seed (default: this ISO week, so a batch stays the same all week)")
	labels := fs.String("labels", "", "label directory (default: state dir; use the repo's eval/jd_triage to keep labels in git)")
	list := fs.Bool("list", false, "print the draw (stratum, p_select, key) and ask nothing")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *scored < 0 || *dropped < 0 || *scored+*dropped < 1 || *scored > maxAuditQuota || *dropped > maxAuditQuota {
		return usagef("audit-sample: --scored and --dropped must be within 0..%d and not both 0", maxAuditQuota)
	}
	if len(*seed) > maxSeedBytes {
		return usagef("audit-sample: --seed is longer than %d bytes", maxSeedBytes)
	}
	s, err := e.openEvalStore(*labels)
	if err != nil {
		return err
	}
	// Fail closed, as `label` does: a draw made without the labels could
	// repeat a posting already answered.
	done, bad, err := s.Labels()
	if err != nil {
		return fmt.Errorf("labels unreadable, refusing to draw (would repeat work): %w", err)
	}
	if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	b, err := e.browse()
	if err != nil {
		return err
	}
	kept, err := b.view("all", "default", false)
	if err != nil {
		return err
	}
	var cands []eval.AuditCandidate
	for _, k := range kept {
		cands = append(cands, eval.AuditCandidate{Key: k.job.Key, Score: k.job.Priority})
	}
	rules := e.dropRules()
	for _, r := range b.snap.Records {
		if r.Status == posting.Dropped {
			cands = append(cands, eval.AuditCandidate{Key: r.Key, Dropped: true, Class: eval.DropClass(r, rules)})
		}
	}
	sd := *seed
	if sd == "" {
		sd = eval.WeekSeed(now())
	}
	plan := eval.AuditSample(cands, done, sd, *scored, *dropped)
	picks := plan.Interleaved(sd)
	if len(picks) == 0 {
		fmt.Fprintln(e.stdout, "nothing to audit: no unlabelled postings in the scored or dropped pool")
		return nil
	}
	fmt.Fprintf(e.stdout, "audit draw %s: %d postings (asked for %d scored + %d dropped)\n", sd, len(picks), *scored, *dropped)
	for _, st := range plan.Strata {
		fmt.Fprintf(e.stdout, "  %-34s pool %4d, drawn %d\n", st.Source, st.Pool, st.Taken)
	}
	if *list {
		for _, p := range picks {
			fmt.Fprintf(e.stdout, "%s\tp_select=%.4f\t%s\n", p.Source, p.PSelect, p.Key)
		}
		return nil
	}
	recs := make([]ledger.Record, 0, len(picks))
	for _, p := range picks {
		r, ok := findRecord(b.snap, p.Key)
		if !ok {
			return fmt.Errorf("audit-sample: drawn posting %s is not in the ledger", p.Key)
		}
		recs = append(recs, r)
	}
	names, err := e.evalNames()
	if err != nil {
		return err
	}
	n, err := eval.LabelAudit(stdin, e.stdout, picks, recs, s, names)
	fmt.Fprintf(e.stdout, "audited %d of %d drawn\n", n, len(picks))
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
