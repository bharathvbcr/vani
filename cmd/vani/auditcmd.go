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
	"time"

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

// The overlays' default quotas: every overlay is on by default (owner,
// 2026-11-13). 5 teacher-wants picks is Fable's suggested start (second
// review); the rest are kept small so the weekly draw stays near the owner's
// fifteen minutes: 17 random places and at most 13 overlay places.
const (
	defaultDisagree        = 5
	defaultDisagreeRejects = 2
	defaultYesNeighbours   = 3
	defaultEngineering     = 3
)

// disagreeFlags are the overlay flags `audit` and `audit answer` share: each
// disagreement cell's quota and whose readings define them, the yes-neighbour
// cell's quota and the engineering group's. 0 draws none; with both
// disagreement quotas at 0 the teacher log is never opened, and with the
// neighbour quota at 0 the vector store is never opened.
//
// A quota left at its default is soft (overlaySoft): when what the overlay
// needs does not exist yet (no teacher readings, a taste column that does not
// pass, no vector store), or the kill rule has turned its cell off, it draws
// nothing and the draw says why. A quota given on the command line is strict
// and fails closed as before, unless --soft-overlays makes it soft too (the
// Mac app's steppers: a draw that cannot fill a cell should still show the
// cards). A store that exists but cannot be read is an error either way.
type disagreeOpts struct {
	wants, rejects        *int
	neighbours, engineers *int
	teacher               *string
	soft                  *bool
}

func disagreeFlags(fs *flag.FlagSet) disagreeOpts {
	return disagreeOpts{
		wants:      fs.Int("disagree", defaultDisagree, "postings drawn from the cell where the teacher wants a posting the engine dropped or ranked in the lowest three deciles (kept out of every estimate)"),
		rejects:    fs.Int("disagree-rejects", defaultDisagreeRejects, "postings drawn from the cell where the teacher rejects a top-decile posting (kept out of every estimate)"),
		neighbours: fs.Int("yes-neighbours", defaultYesNeighbours, "postings drawn from the 50 unlabelled postings nearest the owner's yes answers (kept out of every estimate; given explicitly, refused unless the taste column beats the title part)"),
		engineers:  fs.Int("engineering", defaultEngineering, "postings drawn at random from the engineering-titled postings, kept and dropped (kept out of the deciles, drop recall and the weight fit)"),
		teacher:    fs.String("teacher", sonnetReader, "the teacher model whose stored role_fit readings define the disagreement cells"),
		soft:       fs.Bool("soft-overlays", false, "treat the overlay quotas given here as the defaults are treated: one that cannot run yet is skipped and named, not an error (the Mac app's draw)"),
	}
}

// overlaySoft says which overlay quotas came from their defaults (see
// disagreeOpts), and whether --teacher was given.
type overlaySoft struct {
	wants, rejects, neighbours, engineers bool
	teacherGiven                          bool
}

// overlays is the draw's overlay plan: the quotas, which of them are soft,
// whose readings define the cells, and what was cut before the draw (a
// default quota trimmed to the cap).
type overlays struct {
	q       eval.AuditQuotas
	soft    overlaySoft
	teacher string
	notes   []string
}

// quotas is q with the overlays' places set, read after fs is parsed. A
// default cell quota is trimmed so the cells stay within the 2x cap (at least
// a third of the draw random), with a note; a quota given on the command line
// is never trimmed, and checkAuditDraw refuses one over the cap.
func (d disagreeOpts) quotas(fs *flag.FlagSet, q eval.AuditQuotas) overlays {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	soft := func(name string) bool { return *d.soft || !given[name] }
	o := overlays{teacher: *d.teacher, soft: overlaySoft{
		wants: soft("disagree"), rejects: soft("disagree-rejects"),
		neighbours: soft("yes-neighbours"), engineers: soft("engineering"),
		teacherGiven: given["teacher"],
	}}
	q.Disagree, q.DisagreeRejects = *d.wants, *d.rejects
	q.Neighbour, q.Engineering = *d.neighbours, *d.engineers
	room := 2 * (q.Scored + q.Dropped)
	for _, c := range []struct {
		soft bool
		n    *int
	}{{o.soft.wants, &q.Disagree}, {o.soft.rejects, &q.DisagreeRejects}, {o.soft.neighbours, &q.Neighbour}} {
		if !c.soft {
			room -= *c.n
		}
	}
	for _, c := range []struct {
		soft bool
		n    *int
		flag string
	}{{o.soft.wants, &q.Disagree, "--disagree"}, {o.soft.rejects, &q.DisagreeRejects, "--disagree-rejects"}, {o.soft.neighbours, &q.Neighbour, "--yes-neighbours"}} {
		if !c.soft {
			continue
		}
		if take := max(0, min(*c.n, room)); take < *c.n {
			o.notes = append(o.notes, fmt.Sprintf("the default %s %d was cut to %d so at least a third of the draw stays random", c.flag, *c.n, take))
			*c.n = take
		}
		room -= *c.n
	}
	o.q = q
	return o
}

// overlayUnavailable is an overlay whose inputs do not exist yet: no teacher
// readings, a taste column that does not pass, no vector store. A soft quota
// draws nothing on it and the draw says why; a strict one fails with it.
type overlayUnavailable struct{ msg string }

func (u *overlayUnavailable) Error() string { return u.msg }

func unavailable(format string, a ...any) error {
	return &overlayUnavailable{msg: fmt.Sprintf(format, a...)}
}

// isUnavailable reports whether err is an overlayUnavailable.
func isUnavailable(err error) bool {
	var u *overlayUnavailable
	return errors.As(err, &u)
}

// overlayEligible reports whether an overlay (a disagreement cell, the
// yes-neighbour cell, the engineering group) may draw c: never a closed
// posting, nor a dropped one in a stratum the auditor leaves out
// (stratumLeftOut: work authorization, work mode, owner exclusions), so no
// such posting is drawn because of a reading, a nearness or a title.
func overlayEligible(c eval.AuditCandidate) bool {
	if c.Closed {
		return false
	}
	_, left := stratumLeftOut(eval.AuditCell(c))
	return !left
}

// auditDraw is one week's draw with the state it was made from. q is the
// quotas drawn with, after soft overlays that could not run were set to 0;
// skipped says which and why, in the order they were met.
type auditDraw struct {
	b       *browsing
	s       *eval.Store
	done    map[posting.Key]eval.Entry
	seed    string
	q       eval.AuditQuotas
	skipped []string
	plan    eval.AuditPlan
	picks   []eval.AuditPick // plan.Interleaved(seed)
	// prefilter holds the sampled pre-ledger drops by key (prefilterwire.go): a
	// pick whose key is here is not a ledger record.
	prefilter map[posting.Key]pipeline.PrefilterRow
	// pfWeek and pfSeen are the current week's sample size and how many
	// listing-time drops the week's runs reported, for the draw's header.
	pfWeek, pfSeen int
	// timings is how long each phase of the draw took, in order.
	timings []auditPhaseJSON
}

// auditPhaseJSON is one phase of a draw and its wall time.
type auditPhaseJSON struct {
	Phase string `json:"phase"`
	MS    int64  `json:"ms"`
}

// phaseClock times consecutive phases on the monotonic clock (not now(),
// which tests pin).
type phaseClock struct {
	last   time.Time
	phases []auditPhaseJSON
}

func newPhaseClock() *phaseClock { return &phaseClock{last: time.Now()} }

// mark ends the phase named name.
func (c *phaseClock) mark(name string) {
	t := time.Now()
	c.phases = append(c.phases, auditPhaseJSON{Phase: name, MS: t.Sub(c.last).Milliseconds()})
	c.last = t
}

// recordOf is the record a pick's card shows: the ledger's, or for a sampled
// pre-ledger drop its listed fields.
func (d *auditDraw) recordOf(k posting.Key) (ledger.Record, bool) {
	if r, ok := findRecord(d.b.snap, k); ok {
		return r, true
	}
	return prefilterRecord(d.prefilter, k)
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
	for _, n := range []int{q.Disagree, q.DisagreeRejects, q.Neighbour, q.Engineering} {
		if n < 0 || n > maxAuditQuota {
			return usagef("audit: --disagree, --disagree-rejects, --yes-neighbours and --engineering must be within 0..%d", maxAuditQuota)
		}
	}
	if !q.RandomShareOK() {
		return usagef("audit: --disagree + --disagree-rejects + --yes-neighbours (%d) is more than twice --scored + --dropped (%d): at least a third of the draw stays random (closed and engineering places count on neither side)", q.Cells(), q.Scored+q.Dropped)
	}
	if len(seed) > maxSeedBytes {
		return usagef("audit: --seed is longer than %d bytes", maxSeedBytes)
	}
	return nil
}

// drawAudit makes the draw for seed ("" is this ISO week). It fails closed,
// as `label` does: a draw made without the labels could repeat a posting
// already answered. With cell places the candidates carry teacher's
// role_fit readings (teacherViews), and a teacher log that cannot be read is
// an error; with none the log is never opened. A soft overlay (one left at its
// default) whose cell the kill rule has turned off, or whose inputs do not
// exist yet, draws nothing and is named in skipped.
func (e *env) drawAudit(o overlays, seed, labels string) (*auditDraw, error) {
	q, teacher := o.q, o.teacher
	clock := newPhaseClock()
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
	clock.mark("labels")
	b, err := e.browse()
	if err != nil {
		return nil, err
	}
	clock.mark("ledger")
	cands, err := b.auditCandidates(e.dropRules(), true)
	if err != nil {
		return nil, err
	}
	cands = b.withoutInherited(cands)
	clock.mark("candidates")
	if seed == "" {
		seed = eval.WeekSeed(now())
	}
	skipped := slices.Clone(o.notes)
	// The kill rule (internal/eval/yesyield.go, reworked 2026-11-14) turns a
	// yes-seeking cell off at its first look (0 or 1 yes in its first 20
	// answers) or its second (at 40, under 5 yes or under twice its
	// comparator's rate). The rejects cell is never turned off by it. A default
	// quota obeys it; a quota given on the command line is the owner overriding
	// it.
	for _, c := range eval.BuildYesYield(done).Cells {
		if c.Verdict != eval.CellTurnOff {
			continue
		}
		var n *int
		var soft bool
		var flagName string
		switch c.Cell {
		case eval.CellTeacherWants:
			n, soft, flagName = &q.Disagree, o.soft.wants, "--disagree"
		case eval.CellTeacherRejects:
			n, soft, flagName = &q.DisagreeRejects, o.soft.rejects, "--disagree-rejects"
		case eval.CellYesNeighbour:
			n, soft, flagName = &q.Neighbour, o.soft.neighbours, "--yes-neighbours"
		default:
			continue
		}
		if soft && *n > 0 {
			*n = 0
			skipped = append(skipped, fmt.Sprintf("%s: the kill rule turned this cell off (%d judged, %d yes); pass %s N to draw it anyway", c.Cell, c.N, c.Yes, flagName))
		}
	}
	if q.Disagree+q.DisagreeRejects > 0 {
		err := e.teacherViews(cands, teacher, o.soft.teacherGiven)
		cellsSoft := (q.Disagree == 0 || o.soft.wants) && (q.DisagreeRejects == 0 || o.soft.rejects)
		switch {
		case err != nil && cellsSoft && isUnavailable(err):
			q.Disagree, q.DisagreeRejects = 0, 0
			skipped = append(skipped, "disagreement cells: "+err.Error())
		case err != nil:
			return nil, err
		}
	}
	if q.Engineering > 0 {
		isEng := engineeringOf(b.snap)
		for i := range cands {
			cands[i].Engineering = overlayEligible(cands[i]) && isEng(cands[i].Key)
		}
	}
	if q.Neighbour > 0 {
		err := e.markYesNeighbours(cands, done, b.snap, seed)
		switch {
		case err != nil && o.soft.neighbours && isUnavailable(err):
			q.Neighbour = 0
			for i := range cands {
				cands[i].NearYes = false
			}
			skipped = append(skipped, "yes-neighbour cell: "+err.Error())
		case err != nil:
			return nil, err
		}
	}
	clock.mark("overlays")
	// The listing-time drops never reached the ledger, so the sample the run
	// kept stands in for them: each row is a dropped candidate of its own class.
	// They join after the inherited filter: a sampled row is no ledger record
	// and carries no inherited reading.
	sample := e.loadPrefilterSample()
	rows := prefilterRows(sample)
	for _, r := range sample.Rows() {
		if _, inLedger := findRecord(b.snap, r.Key); inLedger {
			continue // a key a ledger record has is that record's
		}
		cands = append(cands, eval.AuditCandidate{Key: r.Key, Dropped: true, Class: r.Class()})
	}
	clock.mark("prefilter")
	plan := eval.AuditDraw(cands, done, seed, q)
	skipped = append(skipped, shortCells(plan.Picks, q)...)
	held, seen := sample.Week(eval.WeekSeed(now()))
	picks := plan.Interleaved(seed)
	clock.mark("draw")
	return &auditDraw{b: b, s: s, done: done, seed: seed, q: q, skipped: skipped, plan: plan, picks: picks,
		prefilter: rows, pfWeek: len(held), pfSeen: seen, timings: clock.phases}, nil
}

// shortCells names each cell that drew fewer picks than its quota: its pool
// of unlabelled postings is running out. The kill rule's second look needs 40
// answers; a cell whose pool empties before then cannot reach it, and keeping
// the cell is the owner's call on its counts (Fable, 2026-11-14).
func shortCells(picks []eval.AuditPick, q eval.AuditQuotas) []string {
	drawn := map[string]int{}
	for _, p := range picks {
		drawn[p.Source]++
	}
	var out []string
	for _, c := range []struct {
		cell  string
		quota int
	}{{eval.CellTeacherWants, q.Disagree}, {eval.CellTeacherRejects, q.DisagreeRejects}, {eval.CellYesNeighbour, q.Neighbour}} {
		if c.quota > 0 && drawn[c.cell] < c.quota {
			out = append(out, fmt.Sprintf("%s: drew %d of %d; its pool of unlabelled postings is running out, and if it empties before %d answers the kill rule cannot reach its second look: keeping the cell is the owner's call on its counts (vani relevance)",
				c.cell, drawn[c.cell], c.quota, eval.KillSecondLook))
		}
	}
	return out
}

// teacherViews sets each candidate's Teacher from model's newest jd_triage
// role_fit reading in the teacher log (eval.TeacherViewOf), for the
// disagreement cells. A closed posting is never read, nor a dropped one in a
// stratum the auditor leaves out (stratumLeftOut: work authorization, work
// mode, owner exclusions), so no such posting is ever drawn because of a
// reading or shown as an exemplar from a cell. A log that is missing, empty or
// unreadable is an error, and so is one with no row by model for any
// candidate (a misspelt --teacher): cells drawn from nothing would look like
// cells with no disagreement. Rows that were read but disagree nowhere are a
// clean empty draw.
//
// A log that is missing or empty, and, when --teacher was not given, one with
// no reading by the default teacher, is an overlayUnavailable: `vani teacher`
// has not read these postings yet. With --teacher given, no row by that model
// stays a plain error (a misspelt model).
func (e *env) teacherViews(cands []eval.AuditCandidate, model string, teacherGiven bool) error {
	store, status, detail := e.openTeacherLog()
	if store == nil {
		msg := fmt.Sprintf("audit --disagree: the teacher readings log is not usable (%s %s); the disagreement cells need %s's role_fit readings (vani teacher)", status, strings.TrimSpace(detail), model)
		if status == eval.TeacherProxyMissing || status == eval.TeacherProxyEmpty {
			return unavailable("%s", msg)
		}
		return errors.New(msg)
	}
	rows := 0
	for i := range cands {
		c := &cands[i]
		c.Teacher = eval.TeacherUnread
		if !overlayEligible(*c) {
			continue
		}
		if row, ok := store.LatestByModel("", c.Key)[model]; ok {
			rows++
			c.Teacher = eval.TeacherViewOf(row)
		}
	}
	if rows == 0 {
		msg := fmt.Sprintf("audit --disagree: the teacher readings log has no %s reading of any posting the audit may draw (is --teacher the model `vani teacher` ran?)", model)
		if !teacherGiven {
			return unavailable("%s", msg)
		}
		return errors.New(msg)
	}
	return nil
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
// auditJSON is `audit --json`. Skipped names each overlay that drew less
// than its default asked for, and why (an empty list when none did).
type auditJSON struct {
	Seed    string             `json:"seed"`
	Blind   bool               `json:"blind"`
	Picks   []auditPickJSON    `json:"picks"`
	Strata  []auditStratumJSON `json:"strata"`
	Skipped []string           `json:"skipped"`
	// Timings is each phase of the draw (labels, ledger, candidates, overlays,
	// prefilter, draw) and its wall time: a draw near the app's timeout says
	// where the time went. A failed draw replies {"error"} alone.
	Timings []auditPhaseJSON `json:"timings"`
}

type auditPickJSON struct {
	Key     posting.Key `json:"key"`
	Stratum string      `json:"stratum"`
	PSelect float64     `json:"p_select"`
	// Listed is a sampled pre-ledger drop's listed fields (no description):
	// such a pick is no ledger row, so a screen has no jobs row to read them
	// from. Absent for a ledger pick.
	Listed *auditListedJSON `json:"listed,omitempty"`
}

// auditListedJSON is what a listing said of a row dropped before the ledger.
type auditListedJSON struct {
	ATS      posting.ATS `json:"ats"`
	Company  string      `json:"company,omitempty"`
	Title    string      `json:"title"`
	Location string      `json:"location,omitempty"`
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
	cells := disagreeFlags(fs)
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
	o := cells.quotas(fs, eval.AuditQuotas{Scored: *scored, Dropped: *dropped, Closed: *closed})
	if err := checkAuditDraw(o.q, *seed); err != nil {
		return err
	}
	d, err := e.drawAudit(o, *seed, *labels)
	if *asJSON { // a failure is {"error"} on stdout, as every --json command reports it
		out := auditJSON{Seed: *seed, Blind: *blind, Picks: []auditPickJSON{}, Strata: []auditStratumJSON{}, Skipped: []string{}, Timings: []auditPhaseJSON{}}
		if err == nil {
			out.Seed = d.seed
			out.Timings = append(out.Timings, d.timings...)
			for _, p := range d.picks {
				pj := auditPickJSON{Key: p.Key, Stratum: p.Source, PSelect: p.PSelect}
				if r, ok := d.prefilter[p.Key]; ok {
					pj.Listed = &auditListedJSON{ATS: r.ATS, Company: r.Company, Title: r.Title, Location: r.Location}
				}
				out.Picks = append(out.Picks, pj)
			}
			for _, st := range d.plan.Strata {
				out.Strata = append(out.Strata, auditStratumJSON{Stratum: st.Source, Pool: st.Pool, Taken: st.Taken})
			}
			out.Skipped = append(out.Skipped, d.skipped...)
		}
		return e.reply(true, out, err, nil)
	}
	if err != nil {
		return err
	}
	for _, s := range d.skipped {
		fmt.Fprintf(e.stdout, "overlay not drawn: %s\n", s)
	}
	if len(d.picks) == 0 {
		fmt.Fprintln(e.stdout, "nothing to audit: no unlabelled postings in the scored, dropped, closed, engineering, disagreement or yes-neighbour pools")
		return nil
	}
	q := d.q
	fmt.Fprintf(e.stdout, "audit draw %s: %d postings (asked for %d: %d scored + %d dropped + %d closed + %d engineering + %d teacher-wants + %d teacher-rejects + %d yes-neighbours)\n",
		d.seed, len(d.picks), q.Scored+q.Dropped+q.Closed+q.Engineering+q.Cells(), *scored, *dropped, *closed, q.Engineering, q.Disagree, q.DisagreeRejects, q.Neighbour)
	if random, all := d.plan.RandomShare(); q.Cells() > 0 && 3*random < all {
		fmt.Fprintf(e.stdout, "note: %d of %d picks come from the random scored and dropped strata, under a third (closed picks do not count, and a random stratum may have been drawn short)\n", random, all)
	}
	if !*blind { // a stratum name says whether the model dropped the posting
		for _, st := range d.plan.Strata {
			fmt.Fprintf(e.stdout, "  %-34s pool %4d, drawn %d\n", st.Source, st.Pool, st.Taken)
		}
	}
	if d.pfSeen > 0 { // a capped sample: say how much it stands for
		fmt.Fprintf(e.stdout, "  pre-ledger drops this week: %d kept in the sample of the %d the runs reported (cap %d a week)\n",
			d.pfWeek, d.pfSeen, pipeline.DefaultPrefilterSamplePerWeek)
	}
	if *list {
		for _, p := range d.picks {
			fmt.Fprintf(e.stdout, "%s\tp_select=%.4f\t%s\n", p.Source, p.PSelect, p.Key)
		}
		return nil
	}
	recs := make([]ledger.Record, 0, len(d.picks))
	for _, p := range d.picks {
		r, ok := d.recordOf(p.Key)
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
	cells := disagreeFlags(fs)
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
	o := cells.quotas(fs, eval.AuditQuotas{Scored: *scored, Dropped: *dropped, Closed: *closed})
	if err := checkAuditDraw(o.q, *seed); err != nil {
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
	reply, err := e.answerAudit(posting.Key(*key), *verdict, *reason, src, prob, explicit, *seed, o, *labels)
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
func (e *env) answerAudit(k posting.Key, verdict, reason, source string, p float64, explicit bool, seed string, o overlays, labels string) (auditAnswerJSON, error) {
	var s *eval.Store
	var find func(posting.Key) (ledger.Record, bool)
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
		find = func(k posting.Key) (ledger.Record, bool) {
			if r, ok := findRecord(b.snap, k); ok {
				return r, true
			}
			return prefilterRecord(prefilterRows(e.loadPrefilterSample()), k)
		}
	} else {
		d, err := e.drawAudit(o, seed, labels)
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
		s, find, src, prob = d.s, d.recordOf, d.picks[i].Source, d.picks[i].PSelect
	}
	r, ok := find(k)
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
