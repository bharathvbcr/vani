package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
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
func (b *browsing) view(view, sortBy string, openOnly bool) ([]jobview.Selected, error) {
	var o jobsOpts
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addSelectFlags(fs, &o)
	if err := fs.Parse(nil); err != nil {
		return nil, err
	}
	o.View, o.Sort, o.Open = view, sortBy, openOnly
	if err := o.check("audit", true); err != nil {
		return nil, err
	}
	sel, _ := jobview.Select(b.snap, b.m, &o.Opts, b.in, 0)
	jobview.Sort(sel, view, sortBy)
	return sel, nil
}

// maxAuditKeyBytes bounds --key of `audit answer`.
const maxAuditKeyBytes = 512

// auditDraw is one week's draw with the state it was made from.
type auditDraw struct {
	b     *browsing
	s     *eval.Store
	done  map[posting.Key]eval.Entry
	seed  string
	plan  eval.AuditPlan
	picks []eval.AuditPick // plan.Interleaved(seed)
}

// checkAuditDraw validates the draw flags the audit commands share.
func checkAuditDraw(scored, dropped int, seed string) error {
	if scored < 0 || dropped < 0 || scored+dropped < 1 || scored > maxAuditQuota || dropped > maxAuditQuota {
		return usagef("audit: --scored and --dropped must be within 0..%d and not both 0", maxAuditQuota)
	}
	if len(seed) > maxSeedBytes {
		return usagef("audit: --seed is longer than %d bytes", maxSeedBytes)
	}
	return nil
}

// drawAudit makes the draw for seed ("" is this ISO week). It fails closed,
// as `label` does: a draw made without the labels could repeat a posting
// already answered.
func (e *env) drawAudit(scored, dropped int, seed, labels string) (*auditDraw, error) {
	s, err := e.openEvalStore(labels)
	if err != nil {
		return nil, err
	}
	done, bad, err := s.Labels()
	if err != nil {
		return nil, fmt.Errorf("labels unreadable, refusing to draw (would repeat work): %w", err)
	}
	if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	b, err := e.browse()
	if err != nil {
		return nil, err
	}
	kept, err := b.view("all", "default", false)
	if err != nil {
		return nil, err
	}
	var cands []eval.AuditCandidate
	for _, k := range kept {
		if b.m[k.Job.Key].Mark != marks.None {
			continue // judged by its mark already, as in the digest's Explore
		}
		cands = append(cands, eval.AuditCandidate{Key: k.Job.Key, Score: k.Job.Priority})
	}
	rules := e.dropRules()
	for _, r := range b.snap.Records {
		if r.Status == posting.Dropped && b.m[r.Key].Mark == marks.None {
			cands = append(cands, eval.AuditCandidate{Key: r.Key, Dropped: true, Class: eval.DropClass(r, rules)})
		}
	}
	if seed == "" {
		seed = eval.WeekSeed(now())
	}
	plan := eval.AuditSample(cands, done, seed, scored, dropped)
	return &auditDraw{b: b, s: s, done: done, seed: seed, plan: plan, picks: plan.Interleaved(seed)}, nil
}

// auditJSON is `audit --json`: the draw as a screen needs it. It holds keys,
// strata and inclusion probabilities and no posting text; a card's title,
// company and place come from the jobs row contract (internal/jobview).
type auditJSON struct {
	Seed   string             `json:"seed"`
	Blind  bool               `json:"blind"`
	Picks  []auditPickJSON    `json:"picks"`
	Strata []auditStratumJSON `json:"strata"`
}

type auditPickJSON struct {
	Key     posting.Key `json:"key"`
	Stratum string      `json:"stratum"`
	PSelect float64     `json:"p_select"`
}

type auditStratumJSON struct {
	Stratum string `json:"stratum"`
	Pool    int    `json:"pool"`
	Taken   int    `json:"taken"`
}

// cmdAudit draws the week's stratified audit sample and walks the owner
// through it: would they apply to each posting. The answers go to the same
// label store `vani label` uses, as would_apply labels that record the stratum
// and the posting's inclusion probability, so `vani relevance` can count them.
// `audit answer` records one answer for a screen that does the asking.
func (e *env) cmdAudit(args []string) error {
	if len(args) > 0 && args[0] == "answer" {
		return e.cmdAuditAnswer(args[1:])
	}
	fs := e.newFlags("audit")
	scored := fs.Int("scored", eval.AuditScored, "postings drawn across the score deciles")
	dropped := fs.Int("dropped", eval.AuditDropped, "postings drawn across the drop classes")
	seed := fs.String("seed", "", "draw seed (default: this ISO week, so a batch stays the same all week)")
	labels := fs.String("labels", "", "label directory (default: state dir; use the repo's eval/jd_triage to keep labels in git)")
	list := fs.Bool("list", false, "print the draw (stratum, p_select, key) and ask nothing")
	asJSON := fs.Bool("json", false, "print the draw as JSON (keys, strata, p_select; no posting text) and ask nothing")
	blind := fs.Bool("blind", false, "hide the model's current readings (and the strata) while you answer")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *list && *asJSON {
		return usagef("audit: --list and --json are two ways to print the draw; pick one")
	}
	if err := checkAuditDraw(*scored, *dropped, *seed); err != nil {
		return err
	}
	d, err := e.drawAudit(*scored, *dropped, *seed, *labels)
	if *asJSON { // a failure is {"error"} on stdout, as every --json command reports it
		out := auditJSON{Seed: *seed, Blind: *blind, Picks: []auditPickJSON{}, Strata: []auditStratumJSON{}}
		if err == nil {
			out.Seed = d.seed
			for _, p := range d.picks {
				out.Picks = append(out.Picks, auditPickJSON{Key: p.Key, Stratum: p.Source, PSelect: p.PSelect})
			}
			for _, st := range d.plan.Strata {
				out.Strata = append(out.Strata, auditStratumJSON{Stratum: st.Source, Pool: st.Pool, Taken: st.Taken})
			}
		}
		return e.jobsReply(true, out, err, nil)
	}
	if err != nil {
		return err
	}
	if len(d.picks) == 0 {
		fmt.Fprintln(e.stdout, "nothing to audit: no unlabelled postings in the scored or dropped pool")
		return nil
	}
	fmt.Fprintf(e.stdout, "audit draw %s: %d postings (asked for %d scored + %d dropped)\n", d.seed, len(d.picks), *scored, *dropped)
	if !*blind { // a stratum name says whether the model dropped the posting
		for _, st := range d.plan.Strata {
			fmt.Fprintf(e.stdout, "  %-34s pool %4d, drawn %d\n", st.Source, st.Pool, st.Taken)
		}
	}
	if *list {
		for _, p := range d.picks {
			fmt.Fprintf(e.stdout, "%s\tp_select=%.4f\t%s\n", p.Source, p.PSelect, p.Key)
		}
		return nil
	}
	recs := make([]ledger.Record, 0, len(d.picks))
	for _, p := range d.picks {
		r, ok := findRecord(d.b.snap, p.Key)
		if !ok {
			return fmt.Errorf("audit: drawn posting %s is not in the ledger", p.Key)
		}
		recs = append(recs, r)
	}
	names, err := e.evalNames()
	if err != nil {
		return err
	}
	n, err := eval.LabelAudit(stdin, e.stdout, d.picks, recs, d.s, names, *blind)
	fmt.Fprintf(e.stdout, "audited %d of %d drawn\n", n, len(d.picks))
	// The cards the session showed are impressions: every answered card, and
	// the one it stopped on unless it ran out of cards. A recording failure
	// never fails the audit, but it is said.
	shown := min(n+1, len(d.picks))
	if ierr := recordAuditCards(context.Background(), e.stateDir, now(), d.seed, d.picks[:shown]); ierr != nil {
		fmt.Fprintf(e.stderr, "vani: the audit cards' impressions were not recorded: %v\n", ierr)
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// cmdAuditAnswer records one would_apply answer: vani audit answer --key K
// --verdict yes|maybe|no|skip. The label carries the stratum and p_select the
// draw gave the posting: pass them back (--source, --p-select) from the
// `audit --json` the card came from, or leave both out and they are looked up
// in the current draw (same --seed, --scored and --dropped). A posting already
// answered is refused, so a retried tap never counts it twice.
func (e *env) cmdAuditAnswer(args []string) error {
	fs := e.newFlags("audit answer")
	key := fs.String("key", "", "the posting's key")
	verdict := fs.String("verdict", "", "yes, maybe, no or skip")
	source := fs.String("source", "", "the stratum the posting was drawn from (with --p-select)")
	p := fs.Float64("p-select", 0, "the posting's inclusion probability in that draw (with --source)")
	seed := fs.String("seed", "", "draw to look the stratum up in (default: this ISO week)")
	scored := fs.Int("scored", eval.AuditScored, "scored quota of that draw")
	dropped := fs.Int("dropped", eval.AuditDropped, "dropped quota of that draw")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	switch {
	case *key == "" || len(*key) > maxAuditKeyBytes:
		return usagef("audit answer: --key is required and at most %d bytes", maxAuditKeyBytes)
	case *verdict != eval.Skip && !slices.Contains(eval.WouldApplyOptions, *verdict):
		return usagef("audit answer: --verdict %q must be one of %s or %s", *verdict, strings.Join(eval.WouldApplyOptions, ", "), eval.Skip)
	case given["source"] != given["p-select"]:
		return usagef("audit answer: --source and --p-select go together")
	}
	if err := checkAuditDraw(*scored, *dropped, *seed); err != nil {
		return err
	}
	reply, err := e.answerAudit(posting.Key(*key), *verdict, *source, *p, given["source"], *seed, *scored, *dropped, *labels)
	return e.jobsReply(*asJSON, reply, err, func() {
		fmt.Fprintf(e.stdout, "answered %s: %s (stratum %s, p_select %s)\n", reply.Key, reply.Verdict, reply.Stratum, strconv.FormatFloat(reply.PSelect, 'g', 6, 64))
	})
}

// auditAnswerJSON is what `audit answer --json` reports: the label written.
type auditAnswerJSON struct {
	Key     posting.Key `json:"key"`
	Verdict string      `json:"verdict"`
	Stratum string      `json:"stratum"`
	PSelect float64     `json:"p_select"`
}

// answerAudit writes one would_apply label (see cmdAuditAnswer). explicit says
// source and p came from the caller; otherwise they are the draw's.
func (e *env) answerAudit(k posting.Key, verdict, source string, p float64, explicit bool, seed string, scored, dropped int, labels string) (auditAnswerJSON, error) {
	var s *eval.Store
	var snap ledger.Snapshot
	src, prob := source, p
	if explicit {
		var err error
		if s, err = e.openEvalStore(labels); err != nil {
			return auditAnswerJSON{}, err
		}
		b, err := e.browse()
		if err != nil {
			return auditAnswerJSON{}, err
		}
		snap = b.snap
	} else {
		d, err := e.drawAudit(scored, dropped, seed, labels)
		if err != nil {
			return auditAnswerJSON{}, err
		}
		if _, ok := d.done[k].Labels[eval.WouldApply]; ok {
			return auditAnswerJSON{}, fmt.Errorf("audit answer: %s is already answered", k)
		}
		i := slices.IndexFunc(d.picks, func(pk eval.AuditPick) bool { return pk.Key == k })
		if i < 0 {
			return auditAnswerJSON{}, fmt.Errorf("audit answer: %s is not in the draw for seed %s; pass --source and --p-select", k, d.seed)
		}
		s, snap, src, prob = d.s, d.b.snap, d.picks[i].Source, d.picks[i].PSelect
	}
	r, ok := findRecord(snap, k)
	if !ok {
		return auditAnswerJSON{}, fmt.Errorf("audit answer: %s is not in the ledger", k)
	}
	answer := eval.Entry{Key: k, Labels: map[string]string{eval.WouldApply: verdict}, Source: src, PSelect: prob}
	if err := answer.Validate(); err != nil {
		return auditAnswerJSON{}, fmt.Errorf("audit answer: %w", err)
	}
	hash, err := s.PutText(r.Posting.Description)
	if err != nil {
		return auditAnswerJSON{}, err
	}
	answer.TextSHA256 = hash
	// The already-answered check and the write share the store's lock, so of
	// concurrent answers for one key exactly one lands.
	err = s.Update(k, func(prev eval.Entry, _ bool) (eval.Entry, error) {
		if _, ok := prev.Labels[eval.WouldApply]; ok {
			return eval.Entry{}, fmt.Errorf("audit answer: %s is already answered", k)
		}
		return eval.Merge(prev, answer), nil
	})
	if err != nil {
		return auditAnswerJSON{}, err
	}
	return auditAnswerJSON{Key: k, Verdict: verdict, Stratum: src, PSelect: prob}, nil
}
