package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/credentials"
	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/anthropic"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Manvi/manvi/llm/gemini"
	"github.com/bharathvbcr/Manvi/manvi/llm/transport"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/localserver"
	"github.com/bharathvbcr/Vani/internal/notify"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/ashby"
	"github.com/bharathvbcr/Vani/internal/sources/greenhouse"
	"github.com/bharathvbcr/Vani/internal/sources/lever"
	"github.com/bharathvbcr/Vani/internal/sources/page"
	"github.com/bharathvbcr/Vani/internal/sources/workday"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
	"github.com/bharathvbcr/decide/lappi"
)

// maxAdvancePerRun bounds how many postings one run triages, scores and
// explains, which bounds a run's spend and duration. Postings beyond it wait
// for the next run; nothing is lost.
const maxAdvancePerRun = 300

// The OpenRouter model's daily cap lives in <state dir>/budget/jev.json under
// spend key "jev": the names it had before the backend was named after its
// model. Keeping them carries a day's spend across the rename.
const (
	openRouterBudgetFile = "jev.json"
	openRouterSpendKey   = "jev"
)

// openRouterName is the configured OpenRouter model's backend name, or ""
// when none is configured (then no backend drops on a probability).
func (w *wired) openRouterName() string {
	if !w.cfg.OpenRouterConfigured() {
		return ""
	}
	return w.cfg.Decide.OpenRouterName()
}

// shadowName maps decide.shadow to the backend name it selects: the role
// "openrouter" answers under the model's name, "local" under the model and
// question mode (config.Local.BackendName); the others are their names.
func (w *wired) shadowName() string {
	switch w.cfg.Decide.Shadow {
	case config.ShadowOpenRouter:
		return w.openRouterName()
	case "local":
		if l := w.cfg.Decide.Local; l != nil {
			return l.BackendName()
		}
	}
	return w.cfg.Decide.Shadow
}

// maxBoardBytes bounds one board listing. Greenhouse returns a whole board,
// descriptions included, in one response (GitLab's was 3.6 MB on 2026-10-18).
const maxBoardBytes = 32 << 20

// wireApp builds the real components. It opens only what every command needs
// (the ledger); network clients and child processes start on the first run,
// so `vani status` never spawns DevPrism or reads an API key.
func wireApp(cfg config.Config, stateDir string, warn io.Writer) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	led, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if warn == nil {
		warn = io.Discard
	}
	w := &wired{cfg: cfg, stateDir: stateDir, led: led, warn: warn}
	return &App{
		Ledger:   led,
		RunOnce:  w.runOnce,
		Notifier: notify.New(nil),
		Closer:   w.close,
	}, nil
}

type wired struct {
	cfg      config.Config
	stateDir string
	led      *ledger.File
	warn     io.Writer

	mu     sync.Mutex
	scorer *lazyScorer

	// localEndpoint is the address of the local decision model's server when
	// runOnce started one; empty otherwise.
	localEndpoint string
}

func (w *wired) close() error {
	w.mu.Lock()
	s := w.scorer
	w.mu.Unlock()
	var errs []error
	if s != nil {
		errs = append(errs, s.Close())
	}
	errs = append(errs, w.led.Close())
	return errors.Join(errs...)
}

func (w *wired) warnf(format string, a ...any) { fmt.Fprintf(w.warn, "vani: "+format+"\n", a...) }

// runOnce lists every board, reconciles it, then advances postings. It fails
// as a whole only when no board could be read or the ledger fails; a single
// bad board is a warning, and its postings are left untouched (an unread
// board must never count as "every posting disappeared").
func (w *wired) runOnce(ctx context.Context) error {
	d, err := egress.New(egress.Config{AllowedHosts: w.cfg.AllowedHosts, MaxBodyBytes: maxBoardBytes})
	if err != nil {
		return fmt.Errorf("egress: %w", err)
	}
	// A local decision model configured with a server is started for this run
	// only. If it does not come up, the run goes on without the shadow. The
	// endpoint lives exactly as long as its server: a daemon reuses w, and a
	// failed start must never inherit the last run's stopped address.
	w.localEndpoint = ""
	if l := w.cfg.Decide.Local; l != nil && l.Server != nil {
		srv, err := localserver.Start(ctx, localserver.Config{
			Binary: l.Server.Binary, Model: l.Server.ModelPath, ContextSize: l.Server.ContextSize,
			StartTimeout: time.Duration(l.Server.StartTimeoutMS) * time.Millisecond,
			BatchSize:    l.Server.BatchSize, Parallel: l.Server.Parallel,
		})
		if err != nil {
			w.warnf("local decision model not started; running without the shadow: %v", err)
		} else {
			defer func() { w.localEndpoint = ""; srv.Stop() }()
			w.localEndpoint = srv.Endpoint
		}
	}
	// Assemble the stages first: a run that cannot triage must not fetch
	// boards for nothing.
	runner, err := w.runner(ctx, d)
	if err != nil {
		return err
	}
	srcs := map[posting.ATS]sources.Source{
		posting.Greenhouse: greenhouse.New(d),
		posting.Lever:      lever.New(d),
		posting.Ashby:      ashby.New(d),
		posting.Workday:    workday.New(d),
	}
	if w.hasPageBoard() {
		src, stop, err := w.pageSource(ctx, d)
		if err != nil {
			// Page boards are skipped with a warning below; API boards still run.
			w.warnf("careers-page boards disabled: %v", err)
		} else {
			defer stop()
			srcs[posting.Page] = src
		}
	}

	read := 0
	for _, b := range w.cfg.Watchlist {
		src, ok := srcs[b.ATS]
		if !ok {
			if b.ATS == posting.Page {
				w.warnf("board %s/%s: careers-page source disabled this run; skipped", b.ATS, b.Token)
			} else {
				w.warnf("board %s/%s: no adapter for %s; skipped", b.ATS, b.Token, b.ATS)
			}
			continue
		}
		listed, err := src.List(ctx, b)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.warnf("board %s/%s: %v; left unchanged", b.ATS, b.Token, err)
			continue
		}
		read++
		sum, err := pipeline.Reconcile(ctx, w.led, b, listed, time.Now())
		if err != nil {
			return fmt.Errorf("reconcile %s/%s: %w", b.ATS, b.Token, err)
		}
		fmt.Fprintf(w.warn, "vani: board %s/%s: %+v\n", b.ATS, b.Token, sum)
	}
	if len(w.cfg.Watchlist) > 0 && read == 0 {
		return errors.New("no board could be read")
	}

	err = advanceAll(ctx, w.led.All(), runner.Advance, w.warnf)
	w.warnShadowErrors(runner)
	return err
}

// maxConsecutiveUnread stops a run's advance phase once this many postings in
// a row could not be read by any decision backend: a refused key or an outage
// fails every call, and spending the whole cap on it repeats the same failure
// (and the shadow's work) hundreds of times.
const maxConsecutiveUnread = 20

// maxUnreadExamples bounds the per-posting lines for unread postings; the rest
// are counted in one summary line.
const maxUnreadExamples = 3

// maxDistinctCauses bounds how many different advance failures a run reports
// one by one; later new causes are only counted.
const maxDistinctCauses = 20

// advanceAll advances up to maxAdvancePerRun postings in ledger order. A
// posting no backend could read stays Live for the next run. The run fails,
// so a shell loop stops and no empty digest is published, when no backend
// read maxConsecutiveUnread postings in a row, or none of the postings it
// tried. Any other stage failure is printed once per cause (the posting key
// and provider request id are not part of the cause) and counted after that.
func advanceAll(ctx context.Context, recs []ledger.Record, advance func(context.Context, posting.Key) error, warnf func(string, ...any)) error {
	advanced, unreadN, streak := 0, 0, 0
	type cause struct {
		first posting.Key
		more  int
	}
	causes := map[string]*cause{}
	var order []string
	uncounted := 0
	for _, r := range recs {
		if advanced >= maxAdvancePerRun {
			warnf("advance cap %d reached; the rest wait for the next run", maxAdvancePerRun)
			break
		}
		switch r.Status {
		case posting.Live, posting.Triaged, posting.Unsure, posting.Scored:
		default:
			continue
		}
		advanced++
		err := advance(ctx, r.Key)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, pipeline.ErrTriageUnavailable) {
			unreadN++
			streak++
			if unreadN <= maxUnreadExamples {
				warnf("advance %s: %v", r.Key, err)
			}
			if streak >= maxConsecutiveUnread {
				return fmt.Errorf("no decision backend read %d postings in a row; stopped advancing after %d (they stay live; see the backend errors above): %w",
					streak, advanced, pipeline.ErrTriageUnavailable)
			}
			continue
		}
		streak = 0
		if err == nil {
			continue
		}
		key := errorKey(strings.ReplaceAll(err.Error(), string(r.Key), "<posting>"))
		if c, ok := causes[key]; ok {
			c.more++
			continue
		}
		if len(causes) >= maxDistinctCauses {
			uncounted++
			continue
		}
		causes[key] = &cause{first: r.Key}
		order = append(order, key)
		warnf("advance %s: %v", r.Key, err)
	}
	for _, k := range order {
		if c := causes[k]; c.more > 0 {
			warnf("%d more posting(s) failed the same way as %s; they wait for the next run", c.more, c.first)
		}
	}
	if uncounted > 0 {
		warnf("%d more posting(s) failed with other errors (more than %d distinct causes); they wait for the next run", uncounted, maxDistinctCauses)
	}
	if unreadN > 0 {
		if unreadN == advanced {
			return fmt.Errorf("no decision backend read any posting: %d of %d unread (they stay live; see the backend errors above): %w",
				unreadN, advanced, pipeline.ErrTriageUnavailable)
		}
		warnf("%d posting(s) could not be read by any decision backend; they stay live for the next run", unreadN)
	}
	return nil
}

// lightpandaTimeout bounds one lightpanda fetch, start to exit.
const lightpandaTimeout = 60 * time.Second

func (w *wired) hasPageBoard() bool {
	for _, b := range w.cfg.Watchlist {
		if b.ATS == posting.Page {
			return true
		}
	}
	return false
}

// pageSource starts the per-run egress proxy that lightpanda must tunnel
// through, so a careers page gets the same allowlist, https-only and
// private-address rules as an API board. The proxy lives for this run only;
// stop closes it.
func (w *wired) pageSource(ctx context.Context, c *egress.Client) (sources.Source, func(), error) {
	bin, err := lightpandaBinary(w.cfg.LightpandaBinary)
	if err != nil {
		return nil, nil, err
	}
	p, err := egress.NewProxy(c, egress.ProxyConfig{})
	if err != nil {
		return nil, nil, err
	}
	addr, token, err := p.Start(ctx)
	if err != nil {
		_ = p.Close()
		return nil, nil, fmt.Errorf("egress proxy: %w", err)
	}
	stop := func() {
		if err := p.Close(); err != nil {
			w.warnf("egress proxy close: %v", err)
		}
	}
	return page.New(page.ExecRunner{Binary: bin, Timeout: lightpandaTimeout}, page.ProxyInfo{Addr: addr, Token: token}), stop, nil
}

// lightpandaBinary resolves the configured binary, or "lightpanda" on PATH,
// to an absolute path. ExecRunner refuses a relative one.
func lightpandaBinary(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	p, err := exec.LookPath("lightpanda")
	if err != nil {
		return "", fmt.Errorf("lightpanda not found on PATH (set lightpanda_binary): %w", err)
	}
	return filepath.Abs(p)
}

// localBackend builds the shadow client for a decision model served on this
// Mac. By default it gets exactly the request Jev gets (the combined question,
// all slots, the same option descriptions), so `vani eval` compares models,
// not prompts; questions "per_slot" asks each slot its fixture family's own
// question instead. llama.cpp's /v1/systemone takes the state as a plain string.
//
// The backend is named after the model and the question mode
// (config.Local.BackendName), so two candidate models are two shadow backends in
// the ledger and `vani eval` and never pool.
func localBackend(l *config.Local, endpoint string) (decide.Backend, error) {
	return localBackendNamed(l, endpoint, l.BackendName())
}

// localBackendNamed is localBackend under an explicit backend name, for callers
// (`vani bench`) whose backend name never reaches the ledger.
func localBackendNamed(l *config.Local, endpoint, name string) (decide.Backend, error) {
	if name == "" {
		return nil, errors.New("the local model leaves no shadow backend name")
	}
	if endpoint == "" {
		return nil, errors.New("no endpoint: the local server was not started this run")
	}
	timeout := 20 * time.Second
	if l.TimeoutMS > 0 {
		timeout = time.Duration(l.TimeoutMS) * time.Millisecond
	}
	d, err := egress.NewLoopback(timeout+5*time.Second, 0)
	if err != nil {
		return nil, err
	}
	var instr map[string]string
	if l.Questions == "per_slot" {
		instr = make(map[string]string, len(jdtriage.Families))
		for _, f := range jdtriage.Families {
			instr[f.Slot.Name] = f.Question
		}
	}
	return jev.New(jev.Config{
		Name:         name,
		Endpoint:     endpoint,
		Model:        l.Model,
		Timeout:      timeout,
		MaxAttempts:  1, // a local server that failed once will fail again this second
		State:        func(q decide.Question) any { return string(q.Context) },
		Descriptions: jdtriage.Descriptions(),
		Instructions: instr,
	}, d)
}

// runner assembles today's pipeline stages. The LLM pieces are rebuilt per
// run so the daily budget ledger rolls over with the date.
func (w *wired) runner(ctx context.Context, d egress.Doer) (*pipeline.Runner, error) {
	var backends []decide.Backend
	// One cap for every OpenRouter client this run (triage and notes): two
	// DailyCaps on one file would each lock only themselves. The file and its
	// spend key keep their pre-rename name "jev" (jev.Config's default
	// SpendKey), so renaming the backend did not reset a day's spend.
	var spend *decide.DailyCap
	if w.cfg.OpenRouterConfigured() {
		var err error
		spend, err = decide.NewDailyCap(filepath.Join(w.stateDir, "budget", openRouterBudgetFile), w.cfg.Decide.DailyUSD.OpenRouter)
		if err != nil {
			return nil, fmt.Errorf("openrouter budget: %w", err)
		}
		jb, err := jev.New(jev.Config{
			Name:         w.cfg.Decide.OpenRouterName(),
			Model:        w.cfg.Decide.OpenRouterModel,
			KeyEnv:       w.cfg.Decide.OpenRouterKeyEnv,
			Descriptions: jdtriage.Descriptions(),
			Spend:        spend,
			SpendKey:     openRouterSpendKey,
		}, d)
		if err != nil {
			w.warnf("openrouter (%s) disabled: %v", w.cfg.Decide.OpenRouterModel, err)
		} else {
			backends = append(backends, jb)
		}
	}

	if w.cfg.LLM.Provider != "" {
		p, err := w.provider(ctx)
		if err != nil {
			w.warnf("llm disabled: %v", err)
		} else {
			lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.LLM.Model})
			if err != nil {
				return nil, fmt.Errorf("llm backend: %w", err)
			}
			backends = append(backends, logErrors(lb, w.warnf))
		}
	}
	// Lappi is local and free. It is the shadow until `vani eval` reports it
	// promotable; promoted, it goes first so the OpenRouter model and the LLM only see what it
	// abstained on.
	if l := w.cfg.Decide.Lappi; l != nil {
		// Lappi trains one slot per family, so each triage slot goes to its
		// own family with the fixture's exact question.
		routes := make([]lappi.Route, len(jdtriage.Families))
		for i, f := range jdtriage.Families {
			routes[i] = lappi.Route{Task: jdtriage.Task, Slot: f.Slot.Name, Family: f.Task, Question: f.Question}
		}
		lb, err := lappi.NewBackend(lappi.BackendConfig{
			App:     "vani",
			Routes:  routes,
			Socket:  l.Socket,
			Timeout: time.Duration(l.TimeoutMS) * time.Millisecond,
		})
		if err != nil {
			w.warnf("lappi disabled: %v", err)
		} else if l.Primary {
			backends = append([]decide.Backend{lb}, backends...)
		} else {
			backends = append(backends, lb)
		}
	}

	// A local decision model (llama.cpp /v1/systemone) speaks the Jev wire shape,
	// so it is a second Jev client under its own name, reaching only this Mac.
	// It is only ever the shadow (config enforces it): measured, never deciding.
	if l := w.cfg.Decide.Local; l != nil {
		endpoint := l.Endpoint
		if l.Server != nil {
			endpoint = w.localEndpoint // set by runOnce when it started the server
		}
		lb, err := localBackend(l, endpoint)
		if err != nil {
			w.warnf("local decision model disabled: %v", err)
		} else {
			backends = append(backends, lb)
		}
	}

	// The shadow backend is asked alongside the chain and never decides, so it
	// leaves the chain: asking it twice would pay twice for one answer.
	var shadow decide.Backend
	if name := w.shadowName(); name != "" {
		for i, b := range backends {
			if b.Name() == name {
				shadow = b
				backends = append(backends[:i:i], backends[i+1:]...)
				break
			}
		}
		if shadow == nil {
			w.warnf("shadow %q unavailable; running without a shadow", name)
		}
	}
	if len(backends) == 0 {
		return nil, errors.New("no decision backend is available (configure decide.openrouter_model or llm)")
	}
	router := decide.NewRouter(backends...)
	router.Shadow = shadow

	rules := make([]pipeline.DropRule, 0, len(w.cfg.DropRules))
	for _, r := range w.cfg.DropRules {
		rules = append(rules, pipeline.DropRule{Slot: r.Slot, Values: r.Values, MinOpenRouterProb: r.MinOpenRouterProb, MinLappiScore: r.MinLappiScore})
	}
	explainer, skipExplain := w.notes(d, spend)
	r := &pipeline.Runner{
		Ledger:      w.led,
		Triager:     &jdtriage.Triager{D: router, TargetSummary: w.cfg.TargetSummary},
		Explainer:   explainer,
		Policy:      pipeline.DropPolicy{Rules: rules, OpenRouter: w.openRouterName()},
		SkipExplain: skipExplain,
	}
	x, err := loadLCA(w.stateDir)
	if err != nil {
		return nil, err
	}
	if x != nil {
		r.Badger = lcaBadger{x}
	}
	ex, err := loadEmployerView(w.stateDir, w.warnf)
	if err != nil {
		return nil, err
	}
	if ex != nil {
		r.Employer = employerLookup{ex}
		r.PreferEmployers = w.cfg.EmployerPreferences
	} else if len(w.cfg.EmployerPreferences) > 0 {
		w.warnf("employer_preferences set but no employer lists imported; no rank boost (docs/EMPLOYER.md)")
	}
	if w.cfg.DevPrism.Binary == "" {
		w.warnf("devprism.binary empty; scoring disabled, the digest will not be ranked (docs/FOLLOWUPS.md)")
		r.SkipScoring = true
	} else {
		r.Scorer = w.lazyScorer()
	}
	return r, nil
}

// provider builds the configured Manvi adapter, metered by a budget ledger
// that is one file per UTC day, so decide.daily_usd.llm is a daily cap.
func (w *wired) provider(ctx context.Context) (llm.Provider, error) {
	in, out, err := w.cfg.LLM.Prices()
	if err != nil {
		return nil, err
	}
	name := w.cfg.LLM.Provider
	resolver := credentials.NewResolver()
	if _, err := resolver.Resolve(name); err != nil {
		return nil, err
	}
	resolve := func() (credentials.Secret, error) { return resolver.Resolve(name) }
	var inner llm.Provider
	switch name {
	case "gemini":
		inner = gemini.New("", resolve)
	case "anthropic":
		// As Manvi's own CLI does: ANTHROPIC_WORKSPACE_ID names the workspace
		// for a key not scoped to one (Anthropic refuses such a key's requests
		// with a 400 otherwise), and ANTHROPIC_BASE_URL overrides the host.
		a, err := anthropic.NewForWorkspace(os.Getenv("ANTHROPIC_BASE_URL"), os.Getenv(credentials.AnthropicWorkspaceEnv), resolve)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", credentials.AnthropicWorkspaceEnv, err)
		}
		inner = a
	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	day := time.Now().UTC().Format("2006-01-02")
	limit := int64(w.cfg.Decide.DailyUSD.LLM * 1e9)
	l, err := budget.Open(ctx, filepath.Join(w.stateDir, "budget", "llm-"+day+".json"), limit)
	if err != nil {
		return nil, fmt.Errorf("llm budget: %w", err)
	}
	prices := budget.Prices{Rates: budget.Rates{Input: in, Output: out, CacheWrite: in, CacheRead: in}}
	return &budget.Provider{Inner: pricedProvider{Provider: inner, prices: prices}, Ledger: l}, nil
}

// pricedProvider meters the adapter at the configured prices (the operator's
// conservative figures, which win over any table the adapter publishes).
//
// Embedding llm.Provider exposes only that interface, so every optional
// contract budget.Provider looks for on its inner provider is forwarded here
// by hand. Without AttemptGateSupported, budget.Provider refuses every request
// before it is sent ("does not route every attempt through the admission gate").
type pricedProvider struct {
	llm.Provider
	prices budget.Prices
}

func (p pricedProvider) PriceOf(string) (budget.Prices, bool) { return p.prices, true }

// AttemptGateSupported is true only when the adapter itself makes that promise.
func (p pricedProvider) AttemptGateSupported() bool {
	g, ok := p.Provider.(transport.AttemptGatedProvider)
	return ok && g.AttemptGateSupported()
}

// AppliedMaxTokens forwards the adapter's output bound; without one it is what
// budget.Provider would assume: the request's bound, else the model ceiling.
func (p pricedProvider) AppliedMaxTokens(req llm.Request) int {
	if r, ok := p.Provider.(budget.MaxTokensReporter); ok {
		return r.AppliedMaxTokens(req)
	}
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	c, _ := p.Provider.Capability(req.Model)
	return c.MaxOutputTokens
}

// RejectionUnbilled forwards the adapter's rule; without one nothing is unbilled.
func (p pricedProvider) RejectionUnbilled(status int) bool {
	r, ok := p.Provider.(budget.UnbilledRejector)
	return ok && r.RejectionUnbilled(status)
}

// ReplayableOn forwards the adapter's rule; without one only the same model replays.
func (p pricedProvider) ReplayableOn(fromModel, toModel string) bool {
	if r, ok := p.Provider.(llm.ReasoningReplayer); ok {
		return r.ReplayableOn(fromModel, toModel)
	}
	return fromModel == toModel
}

func (w *wired) lazyScorer() *lazyScorer {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.scorer == nil {
		w.scorer = &lazyScorer{cfg: resume.Config{BinaryPath: w.cfg.DevPrism.Binary, PinnedToolsHash: w.cfg.DevPrism.ToolsHash}}
	}
	return w.scorer
}

// lazyScorer starts DevPrism on first use, so commands that never score never
// spawn it. A failed start is remembered for the process: retrying a broken
// binary on every posting would only repeat the error.
type lazyScorer struct {
	cfg    resume.Config
	once   sync.Once
	client *resume.Client
	err    error
}

func (s *lazyScorer) get(ctx context.Context) (*resume.Client, error) {
	s.once.Do(func() { s.client, s.err = resume.Start(ctx, s.cfg) })
	if s.err != nil {
		return nil, fmt.Errorf("devprism: %w", s.err)
	}
	return s.client, nil
}

func (s *lazyScorer) Gap(ctx context.Context, jd, persona string) (resume.Gap, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Gap{}, err
	}
	return c.Gap(ctx, jd, persona)
}

func (s *lazyScorer) Score(ctx context.Context, jd, persona string) (resume.Score, error) {
	c, err := s.get(ctx)
	if err != nil {
		return resume.Score{}, err
	}
	return c.Score(ctx, jd, persona)
}

func (s *lazyScorer) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}
