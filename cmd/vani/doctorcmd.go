package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/doctor"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani doctor` is the pipeline doctor (internal/doctor): Claude, through the
// configured Manvi provider (llm.model, Opus), reads what the last runs left
// behind (per-board signals over the run history, the distinct trouble lines
// of the run logs) and a seeded, stratified sample of dropped postings, and
// says which boards are failing in silence and which drops look wrong. It is a
// report: it changes no ledger status, annotation, rule or watchlist entry.
//
// What is sent: board ids, companies, listed counts and the run records' own
// error lines; log lines with secrets and the home directory removed; for each
// sampled drop, its title, company, location, description (cut) and the triage
// readings of role_fit, seniority, category and work_mode; the configured drop
// rules except any about sponsorship; and the target summary. No vault,
// header, mark, note or sponsorship reading is sent, and drops whose class is
// about sponsorship are not sampled.
//
// It runs only when asked with an explicit --budget-usd that covers the worst
// case of both requests; --dry-run prices them and sends nothing.

const (
	doctorMaxBudgetUSD    = 20.0
	doctorDefaultRuns     = 30
	doctorDefaultSample   = 40
	doctorPromptOverhead  = 256
	doctorReportDir       = "doctor"
	doctorMaxRunRecordErr = 300
)

// Seams tests replace.
var (
	doctorProvider = func(ctx context.Context, w *wired) (llm.Provider, error) { return w.provider(ctx) }
	doctorSpent    = func(ctx context.Context, path string) (int64, error) { return teacherSpent(ctx, path) }
)

type doctorOpts struct {
	dry       bool
	show      bool
	budgetUSD float64
	model     string
	runs      int
	sample    int
	seed      string
	asJSON    bool
}

// doctorPlanJSON is what a dry run (and the head of a real run) reports.
type doctorPlanJSON struct {
	Model          string            `json:"model"`
	RunsRead       int               `json:"runs_read"`
	RunProblems    []string          `json:"run_problems,omitempty"`
	BoardsSignaled int               `json:"boards_signaled"`
	Drops          int               `json:"drops_sampled"`
	Strata         []doctor.Stratum  `json:"strata,omitempty"`
	LeftOut        map[string]int    `json:"drops_left_out,omitempty"`
	WorstUSD       float64           `json:"worst_case_usd"`
	Requests       map[string]string `json:"requests,omitempty"` // part -> size line
	Report         string            `json:"report,omitempty"`   // the written report (real runs)
	Error          string            `json:"error,omitempty"`
}

func (e *env) cmdDoctor(args []string) error {
	fs := e.newFlags("doctor")
	dry := fs.Bool("dry-run", false, "build and price the two requests, and send nothing")
	show := fs.Bool("show", false, "with --dry-run: print both requests exactly as they would be sent")
	budgetUSD := fs.Float64("budget-usd", 0, fmt.Sprintf("the most this run may spend, in dollars (required for a real run, at most %.0f; it must cover the worst case of both requests)", doctorMaxBudgetUSD))
	model := fs.String("model", "", "the model that reads (default llm.model)")
	runs := fs.Int("runs", doctorDefaultRuns, fmt.Sprintf("read the newest N run records (1-%d)", maxRunHistoryScan))
	sample := fs.Int("sample", doctorDefaultSample, fmt.Sprintf("dropped postings to sample, stratified by drop class (0-%d; 0 skips the drop review)", doctor.MaxDrops))
	seed := fs.String("seed", "", "the sample's seed (default this ISO week, as vani audit)")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout: the plan and, for a real run, where the report was written")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *show && !*dry {
		return usagef("doctor: --show only goes with --dry-run")
	}
	o := doctorOpts{dry: *dry, show: *show, budgetUSD: *budgetUSD, model: *model, runs: *runs, sample: *sample, seed: *seed, asJSON: *asJSON}
	switch {
	case o.runs < 1 || o.runs > maxRunHistoryScan:
		return usagef("doctor: --runs must be within 1..%d", maxRunHistoryScan)
	case o.sample < 0 || o.sample > doctor.MaxDrops:
		return usagef("doctor: --sample must be within 0..%d", doctor.MaxDrops)
	case len(o.seed) > maxSeedBytes:
		return usagef("doctor: --seed is longer than %d bytes", maxSeedBytes)
	case !o.dry && (math.IsNaN(o.budgetUSD) || math.IsInf(o.budgetUSD, 0) || o.budgetUSD <= 0 || o.budgetUSD > doctorMaxBudgetUSD):
		return usagef("doctor: a real run needs --budget-usd in (0, %.0f]; --dry-run prices it first", doctorMaxBudgetUSD)
	}
	plan := &doctorPlanJSON{}
	err := e.doctor(context.Background(), o, plan)
	return e.reply(o.asJSON, plan, err, func() {})
}

func (e *env) doctor(ctx context.Context, o doctorOpts, plan *doctorPlanJSON) error {
	out := e.stdout
	if o.asJSON {
		out = e.stderr
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	model := cmp.Or(o.model, cfg.LLM.Model)
	if model == "" {
		return errors.New("doctor: no model: set llm.model or pass --model")
	}
	plan.Model = model
	summary, err := jdtriage.NormalizeSummary(cfg.TargetSummary)
	if err != nil {
		return err
	}
	redact := doctorRedactor()

	in, runFiles, problems := e.doctorHistory(cfg, o.runs, redact)
	plan.RunsRead, plan.RunProblems = len(runFiles), problems
	plan.BoardsSignaled = len(in.Boards)
	logs := e.doctorLogs(runFiles)
	in.Logs = doctor.Digest(logs, redact)
	doctor.AttachDetails(in.Boards, doctor.Details(logs))
	fmt.Fprintf(out, "doctor: %d run records read (%d unreadable); %d boards tripped a signal; %d log trouble lines (%d distinct)\n",
		len(runFiles), len(problems), len(in.Boards), in.Logs.LinesSeen, in.Logs.Distinct)

	if o.sample > 0 {
		seed := o.seed
		if seed == "" {
			seed = eval.WeekSeed(now())
		}
		drops, strata, leftOut, err := e.doctorDrops(o.sample, seed)
		if err != nil {
			return err
		}
		in.Drops, in.Strata, in.Seed = drops, strata, seed
		in.Rules = doctorRules(cfg)
		plan.Drops, plan.Strata, plan.LeftOut = len(drops), strata, leftOut
		fmt.Fprintf(out, "doctor: %d dropped postings sampled (seed %q) from %d strata\n", len(drops), seed, len(strata))
	}

	dcfg := doctor.Config{Model: model, TargetSummary: summary}
	rq, err := doctor.Build(dcfg, in)
	if err != nil {
		return err
	}
	prices, source, err := teacherPrices(cfg, model)
	if err != nil {
		return fmt.Errorf("doctor: prices: %w", err)
	}
	worst, sizes, err := doctorWorst(prices, rq)
	if err != nil {
		return err
	}
	plan.WorstUSD, plan.Requests = float64(worst)/1e9, sizes
	for _, part := range []string{"boards", "drops"} {
		if s, ok := sizes[part]; ok {
			fmt.Fprintf(out, "doctor: %s request: %s\n", part, s)
		}
	}
	fmt.Fprintf(out, "doctor: worst case %s at %s\n", usd(worst), source)
	if rq.Boards == nil && rq.Drops == nil {
		fmt.Fprintln(out, "doctor: nothing to ask: no board tripped a signal and no drop was sampled")
		return nil
	}
	if o.dry {
		if o.show {
			for _, part := range []struct {
				name string
				r    *llm.Request
			}{{"boards", rq.Boards}, {"drops", rq.Drops}} {
				if part.r != nil {
					fmt.Fprintf(out, "\n===== %s: system =====\n%s\n===== %s: user =====\n%s\n", part.name, part.r.System, part.name, part.r.Messages[0].Text())
				}
			}
		}
		fmt.Fprintln(out, "doctor: dry run; nothing was sent")
		return nil
	}

	limit := int64(o.budgetUSD * 1e9)
	if worst > limit {
		return fmt.Errorf("doctor: the worst case %s is over --budget-usd %s; nothing was sent", usd(worst), usd(limit))
	}
	w := &wired{cfg: cfg, stateDir: e.stateDir, warn: e.stderr}
	ledgerPath := w.budgetLedgerPath()
	committed, err := doctorSpent(ctx, ledgerPath)
	if err != nil {
		return fmt.Errorf("doctor: reading today's llm spend: %w", err)
	}
	if capNano := int64(cfg.Decide.DailyUSD.LLM * 1e9); worst > capNano-committed {
		return fmt.Errorf("doctor: the worst case %s is over the headroom of today's $%.2f llm cap (%s spent); nothing was sent",
			usd(worst), cfg.Decide.DailyUSD.LLM, usd(committed))
	}
	p, err := doctorProvider(ctx, w)
	if err != nil {
		return err
	}
	rep, err := doctor.Analyze(ctx, p, dcfg, in, now())
	if err != nil {
		return err
	}
	path, err := e.writeDoctorReport(rep, in.Drops)
	if err != nil {
		return err
	}
	plan.Report = path
	fmt.Fprint(out, doctor.Markdown(rep, in.Drops))
	fmt.Fprintf(out, "\ndoctor: report written to %s (and .json)\n", path)
	// A part that could not run is a failed command, not a clean report.
	var failed []string
	if rep.Boards.Error != "" {
		failed = append(failed, "boards: "+rep.Boards.Error)
	}
	if rep.Drops.Error != "" {
		failed = append(failed, "drops: "+rep.Drops.Error)
	}
	if len(failed) > 0 {
		plan.Error = strings.Join(failed, "; ")
		return errors.New("doctor: " + plan.Error)
	}
	return nil
}

// doctorWorst is the most the requests can cost: each prompt's byte count as
// its token ceiling (a token is at least a byte) and every output token
// ShapeHigh can allow.
func doctorWorst(p budget.Prices, rq doctor.Requests) (int64, map[string]string, error) {
	sizes := map[string]string{}
	var total int64
	for part, r := range map[string]*llm.Request{"boards": rq.Boards, "drops": rq.Drops} {
		if r == nil {
			continue
		}
		in := len(r.System) + len(r.Messages[0].Text()) + doctorPromptOverhead
		outTok := r.MaxTokens + llmbackend.ReasoningHeadroom + llmbackend.HighEffortRoom
		w, err := p.Reserve(in, outTok)
		if err != nil {
			return 0, nil, fmt.Errorf("doctor: pricing the %s request: %w", part, err)
		}
		total += w
		sizes[part] = fmt.Sprintf("%d prompt bytes, at most %d output tokens, worst %s", in-doctorPromptOverhead, outTok, usd(w))
	}
	return total, sizes, nil
}

// doctorHistory reads the newest n run records into the doctor's input.
func (e *env) doctorHistory(cfg config.Config, n int, redact func(string) string) (doctor.Input, []historyFile, []string) {
	var in doctor.Input
	var problems []string
	files, _, err := listRunHistory(e.stateDir, n)
	if err != nil {
		return in, nil, []string{"history: " + oneLine(err.Error(), doctorMaxRunRecordErr)}
	}
	var read []historyFile
	var history []doctor.BoardRun
	var summaries []doctor.RunSummary
	for _, f := range files {
		if !f.Regular {
			problems = append(problems, f.ID+": not a regular file")
			continue
		}
		r, err := readRunRecordFile(runHistoryPath(e.stateDir, f.ID))
		if err != nil {
			problems = append(problems, f.ID+": "+oneLine(err.Error(), doctorMaxRunRecordErr))
			continue
		}
		read = append(read, f)
		if r.StartedAt.After(in.Through) {
			in.Through = r.StartedAt
		}
		s := doctor.RunSummary{ID: f.ID, At: r.StartedAt}
		if b := r.Boards; b != nil && len(read) == 1 { // files are newest first
			in.Unrecorded = b.Unrecorded
		}
		if b := r.Boards; b != nil {
			s.Configured, s.Read, s.Failed, s.Unrecorded, s.Listed = b.Configured, b.Read, b.Failed, b.Unrecorded, b.Listed
			for _, it := range b.Items {
				br := doctor.BoardRun{RunID: f.ID, At: r.StartedAt, Board: it.ATS + ":" + it.Token, Company: it.Company,
					Outcome: it.Outcome, Listed: it.Listed, Error: redact(it.Error)}
				if c := it.Reconcile; c != nil {
					br.New, br.Missed, br.Dead = c.New, c.Missed, c.Dead
				}
				history = append(history, br)
			}
		}
		if a := r.Advance; a != nil {
			fo := a.FailOpen
			s.FailOpen = map[string]int{"empty_description": fo.EmptyDescription, "title_unread": fo.TitleUnread,
				"location_unreadable": fo.LocationUnreadable, "prescore_failed": fo.PrescoreFailed,
				"coverage_unknown": fo.CoverageUnknown, "other": fo.Other}
		}
		summaries = append(summaries, s)
	}
	watch := make([]string, 0, len(cfg.Watchlist))
	for _, b := range cfg.Watchlist {
		watch = append(watch, string(b.ATS)+":"+b.Token)
	}
	// A board no record names is only meaningful when some record names
	// boards at all (an empty history would make every board "never recorded").
	if len(history) == 0 {
		watch = nil
	}
	in.Boards = doctor.Signals(history, watch)
	in.Trend = doctor.RunTrend(summaries)
	return in, read, problems
}

// doctorLogs are the tails of each read run's own log and of the daemon's
// error log, at most doctor.MaxLogBytes each.
func (e *env) doctorLogs(files []historyFile) []doctor.Log {
	var paths []string
	for _, f := range files {
		p := filepath.Join(e.stateDir, runRecordDir, runHistoryDir, f.ID+".log")
		if _, err := os.Lstat(p); err == nil {
			paths = append(paths, p)
		}
	}
	paths = append(paths, filepath.Join(e.stateDir, "logs", "vani.err.log"))
	out := make([]doctor.Log, 0, len(paths))
	for _, p := range paths {
		l := doctor.Log{Name: filepath.Base(p)}
		l.Body, l.Truncated, l.Err = readTail(p, doctor.MaxLogBytes)
		out = append(out, l)
	}
	return out
}

// readTail reads at most n bytes from the end of the regular file at path.
func readTail(path string, n int64) ([]byte, bool, error) {
	f, err := atomicfile.OpenRead(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	off := max(st.Size()-n, 0)
	b, err := io.ReadAll(io.NewSectionReader(f, off, n))
	return b, off > 0, err
}

// doctorRedactor removes secrets and the home directory (which names the
// owner) from a line before it can be kept or sent.
func doctorRedactor() func(string) string {
	home, _ := os.UserHomeDir()
	user := filepath.Base(home)
	return func(s string) string {
		s = redactSecrets(s)
		if home != "" && home != "/" {
			s = strings.ReplaceAll(s, home, "~")
		}
		if len(user) >= 3 && user != "." && user != "/" {
			s = replaceFold(s, user, "<user>")
		}
		return s
	}
}

// replaceFold replaces every case-insensitive occurrence of old in s.
func replaceFold(s, old, repl string) string {
	low, lowOld := strings.ToLower(s), strings.ToLower(old)
	if len(low) != len(s) || !strings.Contains(low, lowOld) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(low, lowOld)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s, low = s[i+len(old):], low[i+len(old):]
	}
}

// sponsorshipSlot is whether a rule slot (or a drop class naming one) is about
// work authorization: its readings and its drops never leave the machine.
func sponsorshipSlot(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "sponsor") || strings.Contains(s, "authoriz") || strings.Contains(s, "visa")
}

// doctorRules are the configured drop rules, one line each, without any
// about sponsorship.
func doctorRules(cfg config.Config) []string {
	var out []string
	for _, r := range cfg.DropRules {
		if sponsorshipSlot(r.Slot) {
			continue
		}
		out = append(out, fmt.Sprintf("%s in %v", r.Slot, r.Values))
	}
	return out
}

// doctorReadingSlots are the triage slots whose readings a drop carries to the
// model: the ones about the job, never the job seeker.
var doctorReadingSlots = []string{"role_fit", "seniority", "category", "work_mode"}

// doctorDrops draws the drop sample with the weekly audit's sampler
// (eval.AuditDraw, dropped group only) over every dropped posting's DropClass,
// and reads back the description of the drawn ones only. leftOut counts the
// dropped postings no stratum could draw from (sponsorship classes).
func (e *env) doctorDrops(n int, seed string) ([]doctor.DropCase, []doctor.Stratum, map[string]int, error) {
	rd, snap, _, release, err := e.jobsReader()
	if err != nil {
		return nil, nil, nil, err
	}
	defer release()
	rules := e.dropRules()
	leftOut := map[string]int{}
	var cands []eval.AuditCandidate
	recs := map[posting.Key]ledger.Record{}
	for _, r := range snap.Records {
		if r.Status != posting.Dropped {
			continue
		}
		class := eval.DropClass(r, rules)
		if sponsorshipSlot(class) {
			leftOut["sponsorship"]++
			continue
		}
		cands = append(cands, eval.AuditCandidate{Key: r.Key, Dropped: true, Class: class})
		recs[r.Key] = r
	}
	draw := eval.AuditDraw(cands, nil, seed, eval.AuditQuotas{Dropped: n})
	picked := map[posting.Key]bool{}
	for _, p := range draw.Picks {
		picked[p.Key] = true
	}
	// Bodies of the drawn postings only: a dropped posting's text is evicted
	// from the lean snapshot.
	hydrated := slices.Clone(snap.Records)
	if err := rd.Hydrate(hydrated, func(r ledger.Record) bool { return picked[r.Key] }); err != nil && !errors.Is(err, ledger.ErrBodyUnavailable) {
		return nil, nil, nil, fmt.Errorf("doctor: reading the sampled postings: %w", err)
	}
	body := map[posting.Key]ledger.Record{}
	for _, r := range hydrated {
		if picked[r.Key] {
			body[r.Key] = r
		}
	}
	var out []doctor.DropCase
	for _, p := range draw.Picks {
		r := body[p.Key]
		if r.Key == "" {
			r = recs[p.Key]
		}
		c := doctor.DropCase{Key: string(p.Key), Stratum: p.Source, PSelect: p.PSelect, Company: r.Posting.Company,
			Title: r.Posting.Title, Location: r.Posting.Location, Readings: map[string]string{}}
		for _, slot := range doctorReadingSlots {
			pre := ledger.AnnTriagePrefix + slot
			for _, k := range []string{pre, pre + ".backend", pre + ".p"} {
				if v := strings.TrimSpace(r.Annotations[k]); v != "" {
					c.Readings[strings.TrimPrefix(k, ledger.AnnTriagePrefix)] = oneLine(v, 80)
				}
			}
		}
		if b := strings.TrimSpace(r.Posting.Description); b != "" && !r.BodyEvicted {
			c.Description = oneLine(b, doctor.MaxDescRunes)
		} else {
			c.BodyMissing = true
		}
		out = append(out, c)
	}
	strata := make([]doctor.Stratum, 0, len(draw.Strata))
	for _, s := range draw.Strata {
		strata = append(strata, doctor.Stratum{Name: s.Source, Pool: s.Pool, Taken: s.Taken})
	}
	sort.Slice(strata, func(i, j int) bool { return strata[i].Name < strata[j].Name })
	return out, strata, leftOut, nil
}

// writeDoctorReport writes the report as <state>/doctor/<stamp>.json and .md
// and returns the .md path.
func (e *env) writeDoctorReport(rep doctor.Report, drops []doctor.DropCase) (string, error) {
	dir := filepath.Join(e.stateDir, doctorReportDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("doctor: %w", err)
	}
	base := filepath.Join(dir, rep.At.UTC().Format("20060102T150405Z"))
	js, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", fmt.Errorf("doctor: %w", err)
	}
	if err := atomicfile.Write(base+".json", js); err != nil {
		return "", fmt.Errorf("doctor: %w", err)
	}
	if err := atomicfile.Write(base+".md", []byte(doctor.Markdown(rep, drops))); err != nil {
		return "", fmt.Errorf("doctor: %w", err)
	}
	return base + ".md", nil
}
