package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/feedback"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The weekly audit (docs/RANKING_PLAN.md, Phase 1): the owner judges a
// stratified sample, "would I apply?", and the verdicts are the ground truth
// the ranking and the drop rules are measured against and, once there are
// enough, fitted to. Verdicts live in <state>/feedback/judgments.jsonl with
// the posting's scores as they were when judged.

const (
	defaultAuditN = 20
	// maxAuditAnswer bounds one interactive answer line.
	maxAuditAnswer = 1 << 10
)

func (e *env) cmdAudit(args []string) error {
	if len(args) == 0 {
		return usagef("audit: expected subcommand \"next\", \"run\", \"judge\" or \"stats\"")
	}
	switch args[0] {
	case "next":
		return e.auditNext(args[1:])
	case "run":
		return e.auditRun(args[1:])
	case "judge":
		return e.auditJudge(args[1:])
	case "stats":
		return e.auditStats(args[1:])
	}
	return usagef("audit: unknown subcommand %q", args[0])
}

// auditItem is one posting of a batch, for the terminal or as JSON. Text
// fields come from job boards: they are flattened to one line before printing.
type auditItem struct {
	Key       posting.Key `json:"key"`
	Source    string      `json:"source"`
	Company   string      `json:"company"`
	Title     string      `json:"title"`
	Location  string      `json:"location,omitempty"`
	URL       string      `json:"url,omitempty"`
	Status    string      `json:"status"`
	Priority  float64     `json:"priority"`
	Relevance float64     `json:"relevance"`
	DropClass string      `json:"drop_class,omitempty"`
}

// auditState is everything the audit commands read.
type auditState struct {
	snap   ledger.Snapshot
	marks  map[posting.Key]marks.Entry
	rules  []jobinsight.DropRule
	rc     jobinsight.RankContext
	judged feedback.Read[feedback.Judgment]
	shown  feedback.Read[feedback.Impression]
	warn   []string
}

func (e *env) auditLoad() (*auditState, error) {
	snap, m, err := e.jobsState()
	if err != nil {
		return nil, err
	}
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, m)
	if err != nil {
		return nil, err
	}
	s := &auditState{snap: snap, marks: m, rc: in.rc}
	// Like `vani jobs`, the audit works without a readable config, but says
	// so: dropped postings then sample as one "unknown" drop class.
	if cfg, err := e.loadConfig(); err != nil {
		s.warn = append(s.warn, "drop rules unknown, so dropped postings are not told apart by rule: "+oneLine(err.Error(), 200))
	} else {
		s.rules = dropRulesOf(cfg)
	}
	if in.info.Warning != "" {
		s.warn = append(s.warn, in.info.Warning)
	}
	if snap.Torn {
		s.warn = append(s.warn, "the ledger's last line is torn (a run was mid-append); the audit reads everything before it")
	}
	if s.judged, err = feedback.ReadJudgments(e.stateDir); err != nil {
		return nil, err
	}
	if s.shown, err = feedback.ReadImpressions(e.stateDir); err != nil {
		return nil, err
	}
	for name, r := range map[string][2]int{"judgments": {s.judged.Skipped, s.judged.Capped}, "impressions": {s.shown.Skipped, s.shown.Capped}} {
		if r[0] > 0 || r[1] > 0 {
			s.warn = append(s.warn, fmt.Sprintf("%s: %d unreadable lines skipped, %d older lines past the read cap left out", name, r[0], r[1]))
		}
	}
	return s, nil
}

// dropRulesOf is the config's drop rules alone (not rank penalties): what a
// dropped posting can have been dropped by.
func dropRulesOf(cfg config.Config) []jobinsight.DropRule {
	var rules []jobinsight.DropRule
	for _, r := range cfg.DropRules {
		rules = append(rules, jobinsight.DropRule{Slot: r.Slot, Values: r.Values})
	}
	return rules
}

// batch is this week's audit: the roles the digest's Explore section showed
// that are not judged yet (newest first), then a stratified sample.
func (s *auditState) batch(n int) []feedback.Pick {
	judged := map[posting.Key]bool{}
	for k := range feedback.Latest(s.judged.Rows) {
		judged[k] = true
	}
	var out []feedback.Pick
	for i := len(s.shown.Rows) - 1; i >= 0 && len(out) < n; i-- {
		r := s.shown.Rows[i]
		if r.Section != feedback.SectionExplore || judged[r.Key] {
			continue
		}
		if _, ok := findRecord(s.snap, r.Key); !ok {
			continue
		}
		judged[r.Key] = true
		out = append(out, feedback.Pick{Key: r.Key, Source: feedback.SourceExplore})
	}
	var cands []feedback.Candidate
	for _, r := range s.snap.Records {
		switch mk := s.marks[r.Key].Mark; {
		case mk == marks.Hidden || mk == marks.Applied:
			// already judged by the owner in Jobs
		case keptStatus(r.Status):
			cands = append(cands, feedback.Candidate{Key: r.Key, Priority: s.score(r).Score})
		case r.Status == posting.Dropped:
			cands = append(cands, feedback.Candidate{Key: r.Key, Dropped: true, DropClass: digest.DropClass(r, s.rules)})
		}
	}
	return append(out, feedback.Sample(cands, judged, n-len(out), feedback.WeekSeed(now()))...)
}

func (s *auditState) score(r ledger.Record) jobinsight.Result {
	return s.rc.ScoreOf(r)
}

func (s *auditState) item(p feedback.Pick) (auditItem, bool) {
	r, ok := findRecord(s.snap, p.Key)
	if !ok {
		return auditItem{}, false
	}
	res := s.score(r)
	it := auditItem{Key: r.Key, Source: p.Source, Company: plain(r.Posting.Company, 120), Title: plain(r.Posting.Title, 200),
		Location: plain(r.Posting.Location, 120), Status: string(r.Status), Priority: res.Score, Relevance: res.Relevance,
		DropClass: digest.DropClass(r, s.rules)}
	if u, ok := digest.HTTPURL(r.Posting.URL); ok {
		it.URL = u
	}
	return it, true
}

// plain flattens board text to one printable line of at most n runes, so a
// title cannot move the cursor, clear the screen or fake a prompt.
func plain(s string, n int) string {
	var b strings.Builder
	k, space := 0, false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || !unicode.IsPrint(r) {
			space = b.Len() > 0
			continue
		}
		if k == n {
			b.WriteString("…")
			break
		}
		if space {
			b.WriteByte(' ')
			k++
			space = false
		}
		b.WriteRune(r)
		k++
	}
	return b.String()
}

func (e *env) auditNext(args []string) error {
	fs := e.newFlags("audit next")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	n := fs.Int("n", defaultAuditN, fmt.Sprintf("batch size (1-%d)", feedback.MaxSample))
	if err := parse(fs, args); err != nil {
		return err
	}
	type reply struct {
		Week     string      `json:"week"`
		Items    []auditItem `json:"items"`
		Warnings []string    `json:"warnings,omitempty"`
	}
	var out reply
	err := func() error {
		if *n < 1 || *n > feedback.MaxSample {
			return usagef("audit next: --n must be 1-%d", feedback.MaxSample)
		}
		s, err := e.auditLoad()
		if err != nil {
			return err
		}
		out = reply{Week: feedback.WeekSeed(now()), Items: []auditItem{}, Warnings: s.warn}
		for _, p := range s.batch(*n) {
			if it, ok := s.item(p); ok {
				out.Items = append(out.Items, it)
			}
		}
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		e.printWarnings(out.Warnings)
		if len(out.Items) == 0 {
			fmt.Fprintln(e.stdout, "nothing to audit: every eligible posting is judged")
			return
		}
		for i, it := range out.Items {
			e.printAuditItem(i+1, len(out.Items), it)
		}
		fmt.Fprintf(e.stdout, "%s: %d to judge (vani audit run, or vani audit judge <key> yes|maybe|no|skip)\n", out.Week, len(out.Items))
	})
}

func (e *env) printWarnings(ws []string) {
	for _, w := range ws {
		fmt.Fprintf(e.stdout, "note: %s\n", w)
	}
}

func (e *env) printAuditItem(i, n int, it auditItem) {
	fmt.Fprintf(e.stdout, "[%d/%d] %s at %s", i, n, it.Title, it.Company)
	if it.Location != "" {
		fmt.Fprintf(e.stdout, " (%s)", it.Location)
	}
	fmt.Fprintf(e.stdout, "\n       relevance %.0f · priority %.1f · %s · %s", it.Relevance, it.Priority, it.Source, it.Status)
	if it.DropClass != "" {
		fmt.Fprintf(e.stdout, " · dropped by %s", it.DropClass)
	}
	fmt.Fprintf(e.stdout, "\n       %s  [%s]\n", it.URL, it.Key)
}

// judge records verdict on key, with the source it was sampled as (this
// week's batch or the digest's Explore section), else "manual".
func (s *auditState) judge(ctx context.Context, stateDir string, key posting.Key, verdict string, batch []feedback.Pick) error {
	if !feedback.ValidVerdict(verdict) {
		return usagef("audit judge: verdict %q must be yes, maybe, no or skip", verdict)
	}
	r, ok := findRecord(s.snap, key)
	if !ok {
		return fmt.Errorf("audit judge: no posting %s in the ledger", key)
	}
	source := "manual"
	for _, p := range batch {
		if p.Key == key {
			source = p.Source
			break
		}
	}
	if source == "manual" {
		for _, im := range s.shown.Rows {
			if im.Key == key && im.Section == feedback.SectionExplore {
				source = feedback.SourceExplore
			}
		}
	}
	return feedback.AppendJudgment(ctx, stateDir, feedback.Judgment{At: now(), Key: key, Verdict: verdict, Source: source,
		Status: string(r.Status), DropReason: digest.DropClass(r, s.rules), Scores: feedback.ScoresOf(s.score(r))})
}

func (e *env) auditJudge(args []string) error {
	fs := e.newFlags("audit judge")
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return usagef("audit judge: expected <key> <yes|maybe|no|skip>")
	}
	key, err := checkJobKey(fs.Arg(0))
	if err != nil {
		return err
	}
	verdict := strings.ToLower(strings.TrimSpace(fs.Arg(1)))
	if !feedback.ValidVerdict(verdict) {
		return usagef("audit judge: verdict %q must be yes, maybe, no or skip", fs.Arg(1))
	}
	s, err := e.auditLoad()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	if err := s.judge(ctx, e.stateDir, key, verdict, s.batch(defaultAuditN)); err != nil {
		return err
	}
	fmt.Fprintf(e.stdout, "judged %s: %s\n", key, verdict)
	return nil
}

// auditRun asks about each posting of the batch on the terminal: y, m, n, s,
// or q to stop. End of input stops too; what was answered is kept.
func (e *env) auditRun(args []string) error {
	fs := e.newFlags("audit run")
	n := fs.Int("n", defaultAuditN, fmt.Sprintf("batch size (1-%d)", feedback.MaxSample))
	if err := parse(fs, args); err != nil {
		return err
	}
	if *n < 1 || *n > feedback.MaxSample {
		return usagef("audit run: --n must be 1-%d", feedback.MaxSample)
	}
	s, err := e.auditLoad()
	if err != nil {
		return err
	}
	e.printWarnings(s.warn)
	batch := s.batch(*n)
	if len(batch) == 0 {
		fmt.Fprintln(e.stdout, "nothing to audit: every eligible posting is judged")
		return nil
	}
	ctx, stop := signalContext()
	defer stop()
	in := bufio.NewScanner(stdin)
	in.Buffer(make([]byte, 0, maxAuditAnswer), maxAuditAnswer)
	answers := map[string]string{"y": feedback.VerdictYes, "yes": feedback.VerdictYes, "m": feedback.VerdictMaybe,
		"maybe": feedback.VerdictMaybe, "n": feedback.VerdictNo, "no": feedback.VerdictNo, "s": feedback.VerdictSkip, "skip": feedback.VerdictSkip}
	done := 0
	defer func() { fmt.Fprintf(e.stdout, "judged %d of %d\n", done, len(batch)) }()
	for i, p := range batch {
		it, ok := s.item(p)
		if !ok {
			continue
		}
		e.printAuditItem(i+1, len(batch), it)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			fmt.Fprint(e.stdout, "       would you apply? [y]es [m]aybe [n]o [s]kip [q]uit: ")
			if !in.Scan() {
				if err := in.Err(); err != nil && !errors.Is(err, io.EOF) {
					return fmt.Errorf("audit run: read answer: %w", err)
				}
				fmt.Fprintln(e.stdout)
				return nil
			}
			a := strings.ToLower(strings.TrimSpace(in.Text()))
			if a == "q" || a == "quit" {
				return nil
			}
			v, ok := answers[a]
			if !ok {
				continue
			}
			if err := s.judge(ctx, e.stateDir, it.Key, v, batch); err != nil {
				return err
			}
			done++
			break
		}
	}
	return nil
}

func (e *env) auditStats(args []string) error {
	fs := e.newFlags("audit stats")
	asJSON := fs.Bool("json", false, "one JSON object on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	type reply struct {
		feedback.Summary
		Impressions     int      `json:"impressions"`
		ShownPostings   int      `json:"shown_postings"`
		DigestDays      int      `json:"digest_days"`
		Saved           int      `json:"saved"`
		Hidden          int      `json:"hidden"`
		Applied         int      `json:"applied"`
		CalibrationNeed int      `json:"calibration_need"`
		WeightFitNeed   int      `json:"weight_fit_need"`
		Warnings        []string `json:"warnings,omitempty"`
	}
	var out reply
	err := func() error {
		judged, err := feedback.ReadJudgments(e.stateDir)
		if err != nil {
			return err
		}
		shown, err := feedback.ReadImpressions(e.stateDir)
		if err != nil {
			return err
		}
		m, err := marks.Read(marks.Dir(e.stateDir))
		if err != nil {
			return fmt.Errorf("audit stats: read marks: %w", err)
		}
		out.Summary = feedback.Summarize(judged.Rows, now())
		keys, days := map[posting.Key]bool{}, map[string]bool{}
		for _, r := range shown.Rows {
			keys[r.Key] = true
			if r.Surface == feedback.SurfaceDigest {
				days[r.At.Format("2006-01-02")] = true
			}
		}
		out.Impressions, out.ShownPostings, out.DigestDays = len(shown.Rows), len(keys), len(days)
		for _, en := range m {
			switch en.Mark {
			case marks.Saved:
				out.Saved++
			case marks.Hidden:
				out.Hidden++
			case marks.Applied:
				out.Applied++
			}
		}
		out.CalibrationNeed = max(feedback.CalibrationMin-out.Usable, 0)
		out.WeightFitNeed = max(feedback.WeightFitMin-out.Usable, 0)
		for name, r := range map[string][2]int{"judgments": {judged.Skipped, judged.Capped}, "impressions": {shown.Skipped, shown.Capped}} {
			if r[0] > 0 || r[1] > 0 {
				out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %d unreadable lines skipped, %d past the read cap", name, r[0], r[1]))
			}
		}
		sort.Strings(out.Warnings)
		return nil
	}()
	return e.jobsReply(*asJSON, out, err, func() {
		e.printWarnings(out.Warnings)
		fmt.Fprintf(e.stdout, "judged %d (%d usable, %d this week): yes %d, maybe %d, no %d, skip %d\n", out.Judged, out.Usable, out.ThisWeek,
			out.ByVerdict[feedback.VerdictYes], out.ByVerdict[feedback.VerdictMaybe], out.ByVerdict[feedback.VerdictNo], out.ByVerdict[feedback.VerdictSkip])
		fmt.Fprintf(e.stdout, "marks: saved %d, hidden %d, applied %d\n", out.Saved, out.Hidden, out.Applied)
		fmt.Fprintf(e.stdout, "impressions: %d rows, %d postings, %d digest days\n", out.Impressions, out.ShownPostings, out.DigestDays)
		readiness := func(name string, ready bool, need int) {
			if ready {
				fmt.Fprintf(e.stdout, "%s: ready\n", name)
			} else {
				fmt.Fprintf(e.stdout, "%s: cannot run yet, %d more usable judgments needed\n", name, need)
			}
		}
		readiness("calibration", out.CalibrationReady, out.CalibrationNeed)
		readiness("weight fit", out.WeightFitReady, out.WeightFitNeed)
		printRates(e.stdout, "would apply, by priority decile at sampling", out.WouldApplyByDecile)
		printRates(e.stdout, "false drops, by drop class", out.FalseDropByClass)
		printRates(e.stdout, "would apply, explore picks", map[string]feedback.Rate{"explore": out.Explore})
	})
}

func printRates(w io.Writer, title string, rates map[string]feedback.Rate) {
	fmt.Fprintf(w, "%s:\n", title)
	if len(rates) == 0 {
		fmt.Fprintln(w, "  nothing judged yet")
		return
	}
	names := make([]string, 0, len(rates))
	for k := range rates {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		r := rates[k]
		if r.Rate == nil {
			fmt.Fprintf(w, "  %s: unmeasured (%d judged, %d needed)\n", k, r.Judged, feedback.MeasureMin)
			continue
		}
		fmt.Fprintf(w, "  %s: %.0f%% (%d of %d)\n", k, 100**r.Rate, r.Hits, r.Judged)
	}
}
