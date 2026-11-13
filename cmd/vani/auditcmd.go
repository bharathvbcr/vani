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

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
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
	return evalDropRules(cfg)
}

// evalDropRules is cfg's drop rules as the drop-class names need them.
func evalDropRules(cfg config.Config) []eval.DropRule {
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
func checkAuditDraw(q eval.AuditQuotas, seed string) error {
	for _, n := range []int{q.Scored, q.Dropped, q.Closed} {
		if n < 0 || n > maxAuditQuota {
			return usagef("audit: --scored, --dropped and --closed must be within 0..%d and not all 0", maxAuditQuota)
		}
	}
	if q.Scored+q.Dropped+q.Closed < 1 {
		return usagef("audit: --scored, --dropped and --closed must be within 0..%d and not all 0", maxAuditQuota)
	}
	if len(seed) > maxSeedBytes {
		return usagef("audit: --seed is longer than %d bytes", maxSeedBytes)
	}
	return nil
}

// drawAudit makes the draw for seed ("" is this ISO week). It fails closed,
// as `label` does: a draw made without the labels could repeat a posting
// already answered.
func (e *env) drawAudit(q eval.AuditQuotas, seed, labels string) (*auditDraw, error) {
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
	cands, err := b.auditCandidates(e.dropRules(), true)
	if err != nil {
		return nil, err
	}
	cands = b.withoutInherited(cands)
	if seed == "" {
		seed = eval.WeekSeed(now())
	}
	plan := eval.AuditDraw(cands, done, seed, q)
	return &auditDraw{b: b, s: s, done: done, seed: seed, plan: plan, picks: plan.Interleaved(seed)}, nil
}

// auditCandidates is every posting the audit's strata stand over: each kept
// posting with its priority score, each dropped one with its drop class and each
// closed one with its status. skipMarked leaves out a posting the owner already
// judged with a mark, as the digest's Explore does.
func (b *browsing) auditCandidates(rules []eval.DropRule, skipMarked bool) ([]eval.AuditCandidate, error) {
	kept, err := b.view("all", "default", false)
	if err != nil {
		return nil, err
	}
	var cands []eval.AuditCandidate
	for _, k := range kept {
		if skipMarked && b.m[k.Job.Key].Mark != marks.None {
			continue // judged by its mark already, as in the digest's Explore
		}
		cands = append(cands, eval.AuditCandidate{Key: k.Job.Key, Score: k.Job.Priority})
	}
	for _, r := range b.snap.Records {
		if skipMarked && b.m[r.Key].Mark != marks.None {
			continue
		}
		if c, ok := closedOrDropped(r, rules); ok {
			cands = append(cands, c)
		}
	}
	return cands, nil
}

// withoutInherited is cands less the kept and dropped postings whose triage
// reading was copied from another posting (eval.Inherited, the one owner, as
// the label queue uses it). A label on a copy is no evidence about the model
// that would have read it, so the weekly draw leaves copies out. A closed
// posting stays: the engine closed it, not a reading. Only the draw applies
// this; the pair planner's strata and the cluster study still stand over the
// whole pool, copies included.
func (b *browsing) withoutInherited(cands []eval.AuditCandidate) []eval.AuditCandidate {
	inherited := map[posting.Key]bool{}
	for _, r := range b.snap.Records {
		if eval.Inherited(r) {
			inherited[r.Key] = true
		}
	}
	out := cands[:0:0]
	for _, c := range cands {
		if inherited[c.Key] && !c.Closed {
			continue
		}
		out = append(out, c)
	}
	return out
}

// closedOrDropped is r as an audit candidate when the engine dropped it (with
// its DropClass) or closed it (dead, or folded into another as a duplicate);
// ok is false for any other status.
func closedOrDropped(r ledger.Record, rules []eval.DropRule) (eval.AuditCandidate, bool) {
	switch r.Status {
	case posting.Dropped:
		return eval.AuditCandidate{Key: r.Key, Dropped: true, Class: eval.DropClass(r, rules)}, true
	case posting.Dead:
		return eval.AuditCandidate{Key: r.Key, Closed: true, Class: string(r.Status)}, true
	case posting.Duplicate:
		// A duplicate is its own stratum per fold method (dup.method); one folded
		// by identity rather than overlap carries none.
		method := r.Annotations[pipeline.AnnDupMethod]
		if method == "" {
			method = "other"
		}
		return eval.AuditCandidate{Key: r.Key, Closed: true, Class: string(r.Status) + ":" + method}, true
	}
	return eval.AuditCandidate{}, false
}

// recallMembers is every kept and dropped posting as the drop-recall estimate
// counts it: its cell now and, for a marked one, whether the mark says wanted
// (saved or applied) or not (hidden). Closed and untriaged postings are not
// members.
func (b *browsing) recallMembers(rules []eval.DropRule) []eval.RecallMember {
	out := make([]eval.RecallMember, 0, len(b.snap.Records))
	for _, r := range b.snap.Records {
		var cell, legacy string
		switch {
		case jobview.KeptStatus(r.Status):
			cell = eval.CellKept
		case r.Status == posting.Dropped:
			c, _ := closedOrDropped(r, rules)
			cell = eval.AuditCell(c)
			legacy = eval.LegacyDropCell(r, rules)
		default:
			continue
		}
		mk := b.m[r.Key].Mark
		out = append(out, eval.RecallMember{Key: r.Key, Cell: cell, LegacyCell: legacy, Marked: mk != marks.None,
			MarkedWanted: mk == marks.Saved || mk == marks.Applied})
	}
	return out
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
	closed := fs.Int("closed", eval.AuditClosed, "postings drawn across the closed statuses (dead, duplicate)")
	seed := fs.String("seed", "", "draw seed (default: this ISO week, so a batch stays the same all week)")
	labels := fs.String("labels", "", "label directory (default: state dir; use the repo's eval/jd_triage to keep labels in git)")
	list := fs.Bool("list", false, "print the draw (stratum, p_select, key) and ask nothing")
	asJSON := e.jsonFlag(fs, "print the draw as JSON (keys, strata, p_select; no posting text) and ask nothing")
	blind := fs.Bool("blind", false, "hide the model's current readings (and the strata) while you answer")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *list && *asJSON {
		return usagef("audit: --list and --json are two ways to print the draw; pick one")
	}
	q := eval.AuditQuotas{Scored: *scored, Dropped: *dropped, Closed: *closed}
	if err := checkAuditDraw(q, *seed); err != nil {
		return err
	}
	d, err := e.drawAudit(q, *seed, *labels)
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
		return e.reply(true, out, err, nil)
	}
	if err != nil {
		return err
	}
	if len(d.picks) == 0 {
		fmt.Fprintln(e.stdout, "nothing to audit: no unlabelled postings in the scored or dropped pool")
		return nil
	}
	fmt.Fprintf(e.stdout, "audit draw %s: %d postings (asked for %d scored + %d dropped + %d closed)\n", d.seed, len(d.picks), *scored, *dropped, *closed)
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
	reason := fs.String("reason", "", "with --verdict maybe or no: the one factor that kept it from a yes ("+strings.Join(eval.WouldApplyReasons, ", ")+")")
	source := fs.String("source", "", "the stratum the posting was drawn from (with --p-select)")
	p := fs.Float64("p-select", 0, "the posting's inclusion probability in that draw (with --source)")
	seed := fs.String("seed", "", "draw to look the stratum up in (default: this ISO week)")
	scored := fs.Int("scored", eval.AuditScored, "scored quota of that draw")
	dropped := fs.Int("dropped", eval.AuditDropped, "dropped quota of that draw")
	closed := fs.Int("closed", eval.AuditClosed, "closed quota of that draw")
	labels := fs.String("labels", "", "label directory (default: state dir)")
	shown := fs.Bool("shown", false, "answered from a surface row (a digest Explore pick, a Jobs row): take the stratum and p_select of the newest sampled impression, else record a manual label")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
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
	case *reason != "" && !eval.ReasonAllowed(*verdict):
		return usagef("audit answer: --reason goes with --verdict maybe or no (what kept it from a yes)")
	case *reason != "" && !slices.Contains(eval.WouldApplyReasons, *reason):
		return usagef("audit answer: --reason %q must be one of %s", *reason, strings.Join(eval.WouldApplyReasons, ", "))
	case given["source"] != given["p-select"]:
		return usagef("audit answer: --source and --p-select go together")
	case *shown && (given["source"] || given["seed"]):
		return usagef("audit answer: --shown takes the stratum from the impression log; leave out --source, --p-select and --seed")
	}
	q := eval.AuditQuotas{Scored: *scored, Dropped: *dropped, Closed: *closed}
	if err := checkAuditDraw(q, *seed); err != nil {
		return err
	}
	src, prob, explicit := *source, *p, given["source"]
	if *shown {
		var err error
		if src, prob, err = e.shownSampling(posting.Key(*key)); err != nil {
			return err
		}
		explicit = true
	}
	reply, err := e.answerAudit(posting.Key(*key), *verdict, *reason, src, prob, explicit, *seed, q, *labels)
	return e.reply(*asJSON, reply, err, func() {
		why := ""
		if reply.Reason != "" {
			why = ", reason " + reply.Reason
		}
		fmt.Fprintf(e.stdout, "answered %s: %s%s (stratum %s, p_select %s)\n", reply.Key, reply.Verdict, why, reply.Stratum, strconv.FormatFloat(reply.PSelect, 'g', 6, 64))
	})
}

// shownSampling is the stratum and inclusion probability of an answer given
// from a surface row (`audit answer --shown`): those of the posting's newest
// impression that carried both (an Explore pick or an audit card, drawn with a
// known probability), else eval.SourceManual with none. An impression log that
// cannot be read is an error: guessing "manual" would drop a real stratum.
func (e *env) shownSampling(k posting.Key) (source string, p float64, err error) {
	shown, err := feedback.ReadImpressions(e.stateDir)
	if err != nil {
		return "", 0, fmt.Errorf("audit answer --shown: read impressions: %w", err)
	}
	best := -1
	for i, im := range shown.Rows {
		if im.Key != k || im.Source == "" || !(im.PSelect >= eval.MinInclusionProbability && im.PSelect <= 1) {
			continue
		}
		if best < 0 || !im.At.Before(shown.Rows[best].At) {
			best = i
		}
	}
	if best < 0 {
		return eval.SourceManual, 0, nil
	}
	return shown.Rows[best].Source, shown.Rows[best].PSelect, nil
}

// auditAnswerJSON is what `audit answer --json` reports: the label written.
type auditAnswerJSON struct {
	Key     posting.Key `json:"key"`
	Verdict string      `json:"verdict"`
	Reason  string      `json:"reason,omitempty"`
	Stratum string      `json:"stratum"`
	PSelect float64     `json:"p_select"`
}

// answerAudit writes one would_apply label (see cmdAuditAnswer), with its
// reason when one is given. explicit says source and p came from the caller;
// otherwise they are the draw's.
func (e *env) answerAudit(k posting.Key, verdict, reason, source string, p float64, explicit bool, seed string, q eval.AuditQuotas, labels string) (auditAnswerJSON, error) {
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
		d, err := e.drawAudit(q, seed, labels)
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
	if reason != "" {
		answer.Labels[eval.WouldApplyReason] = reason
	}
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
	return auditAnswerJSON{Key: k, Verdict: verdict, Reason: reason, Stratum: src, PSelect: prob}, nil
}
