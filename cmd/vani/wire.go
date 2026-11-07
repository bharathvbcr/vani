package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/notify"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/jev"
	"github.com/bharathvbcr/decide/lappi"
)

// The OpenRouter decision model's (Liquid D's) daily cap lives in
// <state dir>/budget/jev.json under spend key "jev": the names it had before
// the backend was named after its model, and jev.Config's default SpendKey.
// They stay on purpose. decide.DailyCap re-reads its file on every call, reads
// a missing file as nothing spent, and takes no lock across processes, so a
// renamed file could not be migrated safely: a daemon or older binary still on
// the old path would start the day at zero again, a second full cap.
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
// descriptions included, in one response (Anduril's exceeds 32 MiB).
const maxBoardBytes = 64 << 20

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
	live := &liveNote{}
	w := &wired{cfg: cfg, stateDir: stateDir, led: led, warn: warn, live: live}
	appLock := notify.AppLockPath(stateDir)
	return &App{
		Ledger:    led,
		RunOnce:   w.runOnce,
		RunReport: w.runReport,
		Reload:    w.reload,
		PollInbox: w.pollInbox,
		Rescore:   w.rescore,
		Tune:      w.tune,
		ModelKey:  w.openRouterKey,
		// The Vani Apply app posts run notices itself while it runs. When it
		// does not, the daemon launches it to announce the run, and posts
		// through osascript only if the app cannot take over.
		Notifier: notify.UnlessHeld(appLock, notify.ViaApp(appLock, nil, notify.New(nil), warn)),
		Closer:   w.close,
		live:     live,
	}, nil
}

// reload replaces the config the next run, inbox poll or rescore reads
// (App.Reload; env.reloadConfig has already pinned the process-lifetime
// fields). Callers do not overlap it with a run.
func (w *wired) reload(cfg config.Config) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg = cfg
}

// tune keeps the next run's options for its advance phase (App.Tune; dailyJob
// calls it before every run).
func (w *wired) tune(opts runOptions) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tuned = opts
}

type wired struct {
	cfg      config.Config
	stateDir string
	led      *ledger.File
	warn     io.Writer
	warnMu   sync.Mutex // serialises writes to warn

	mu     sync.Mutex
	scorer *lazyScorer

	// day is the llm budget day (budgetLedgerPath). Empty until the first
	// read, then fixed until the next startRun sets it to that run's UTC day.
	day string

	// tuned is the run's options (tune). The advance phase reads Advance (a
	// cap other than advance_per_run when above 0), ListOnly (skip it) and
	// USFirst (US postings first, as advance_us_first).
	tuned runOptions

	// localEndpoint is the address of the local decision model's server when
	// runOnce started one; empty otherwise.
	localEndpoint string

	// backendErrs collects the current run's decision-backend errors; runOnce
	// replaces it at the start of every run. boards and advance are the last
	// run's board and advance reports. All are read by runReport, under mu.
	backendErrs *backendErrors
	boards      *boardsReport
	advance     *pipeline.AdvanceReport
	// semPre is the last run's knowledge-base warm before the advance phase
	// (semanticwire.go warmBeforeAdvance); nil when it did not run.
	semPre *semanticReport
	// backfill is the last run's escalation backfill (escalatebackfill.go); nil
	// when it did not run.
	backfill *escalateBackfillReport
	// nearDup, exclusions, logos and shadow are the current run's stage
	// summaries. startRun clears them. nil means that stage has not run.
	nearDup    *nearDupSummary
	exclusions *exclusionSummary
	logos      *logoSummary
	shadow     *shadowNote
	// explain is set when the digest's notes were off or paused on a run that
	// was going to ask a backend. Nil when notes ran, or the run was list-only.
	explain *explainNote
	// simOff and simNote are the near-duplicate cosine's load and cache lines
	// for this run. finishSimilarity copies them onto the summary.
	simOff  string
	simNote string
	// fetches is each board's detail and pay counts, keyed the way the board
	// report spells the token. applyFetchesLocked copies them onto the report.
	fetches map[boardKey]boardFetch
	// recordGaps is true when this run will ask a decision backend. A list-only
	// pass leaves shadow and notes gaps off the record.
	recordGaps bool
	// keychainRun counts runs (startRun), so a failed Keychain read is retried
	// once per run; keychain caches the OpenRouter key (keychain.go).
	keychainRun int
	keychain    keychainCache
	// live is shared with App. Nil on a wired value a test built by hand.
	live *liveNote
}

// runReport is what the last runOnce left for the run record.
func (w *wired) runReport() runDetail {
	w.mu.Lock()
	defer w.mu.Unlock()
	return runDetail{
		Boards: w.boards, Advance: w.advance, BackendErrors: w.backendErrs.report(), BackendStats: w.backendErrs.stats(), SemanticPre: w.semPre,
		NearDup: w.nearDup, Exclusions: w.exclusions, Logos: w.logos, Shadow: w.shadow, Explain: w.explain,
		EscalateBackfill: w.backfill,
	}
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

// noteLive reports one stage of the run that is going. It does not hold w.mu.
func (w *wired) noteLive(p runProgress) {
	if w == nil {
		return
	}
	w.live.call(p)
}

// warnf prints one warning line. Postings advance concurrently and their
// backends warn from their own goroutines, so lines are written under warnMu,
// whole.
func (w *wired) warnf(format string, a ...any) {
	line := fmt.Sprintf("vani: "+format+"\n", a...)
	w.warnMu.Lock()
	defer w.warnMu.Unlock()
	_, _ = io.WriteString(w.warn, line)
}

// localBackend builds the shadow client for a decision model served on this
// Mac. By default it gets exactly the request Liquid D gets (the combined question,
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
		Descriptions: jobcategory.UnifiedDescriptions(),
		Instructions: instr,
	}, d)
}

// runner assembles today's pipeline stages. The LLM pieces are rebuilt per
// run so the daily budget ledger rolls over with the date.
func (w *wired) runner(ctx context.Context, d egress.Doer) (*pipeline.Runner, error) {
	return w.runnerFor(ctx, d, true)
}

// errNoBackend is runnerFor's error when a pass that advances has no decision
// backend (no OpenRouter model or key, no llm).
var errNoBackend = errors.New("no decision backend is available (configure decide.openrouter_model or llm)")

// runnerFor is runner, where advance says whether the pass will ask a decision
// backend anything. A pass that will not (a --list-only run, which skips the
// advance phase) is built without one when none is available: the runner's
// other stages (drop policy, scoring, notes) do not need one, and the empty
// router it gets answers nothing (every slot NotAsked), so a posting it were
// asked about would read unsure and stay Live. A pass that advances still fails here, before any board
// is read, when no backend can be built.
func (w *wired) runnerFor(ctx context.Context, d egress.Doer, advance bool) (*pipeline.Runner, error) {
	var backends []decide.Backend
	// One cap for every OpenRouter client this run (triage and notes): two
	// DailyCaps on one file would each lock only themselves. The spend key keeps its
	// pre-rename name "jev" (jev.Config's default
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
			Getenv:       w.openRouterGetenv,
			Descriptions: jobcategory.UnifiedDescriptions(),
			Spend:        spend,
			SpendKey:     openRouterSpendKey,
		}, d)
		if err != nil {
			w.warnf("openrouter (%s) disabled: %v", w.cfg.Decide.OpenRouterModel, err)
		} else {
			backends = append(backends, logErrors(jb, w.warnf, w.backendErrs))
		}
	}

	// esc is the escalation tier (decide.escalate): a second backend on the same
	// metered provider, so one daily cap bounds all LLM spend.
	var esc decide.Backend
	if w.cfg.LLM.Provider != "" {
		p, err := w.provider(ctx)
		if err != nil {
			w.warnf("llm disabled: %v", err)
		} else {
			lb, err := llmbackend.New(p, llmbackend.Config{Model: w.cfg.LLM.EffectiveTriageModel()})
			if err != nil {
				return nil, fmt.Errorf("llm backend: %w", err)
			}
			backends = append(backends, logErrors(lb, w.warnf, w.backendErrs))
			if esc, err = w.escalateBackend(p); err != nil {
				return nil, err
			}
		}
	}
	// Lappi is local and free. It is the shadow until `vani eval` reports it
	// promotable; promoted, it goes first so the OpenRouter model and the LLM only see what it
	// abstained on. Per slot (primary_slots), a promoted slot goes to Lappi first
	// and the OpenRouter model stops answering it; the other slots keep the shadow.
	var lappiShadow decide.Backend
	promoted := w.cfg.Decide.Lappi.PromotedSlots()
	if l := w.cfg.Decide.Lappi; l != nil {
		// Lappi trains one slot per family, so each triage slot goes to its
		// own family with the fixture's exact question.
		routes := make([]lappi.Route, len(jdtriage.Families))
		for i, f := range jdtriage.Families {
			routes[i] = lappi.Route{Task: jdtriage.Task, Slot: f.Slot.Name, Family: f.Task, Question: f.Question}
		}
		newLappi := func() (*lappi.Backend, error) {
			return lappi.NewBackend(lappi.BackendConfig{
				App:     "vani",
				Routes:  routes,
				Socket:  l.Socket,
				Timeout: time.Duration(l.TimeoutMS) * time.Millisecond,
			})
		}
		lb, err := newLappi()
		if err != nil {
			msg := fmt.Sprintf("lappi disabled: %v", err)
			w.warnf("%s", msg)
			if advance {
				w.noteShadow(msg)
			}
		} else if len(promoted) > 0 {
			backends = append([]decide.Backend{onlySlots(logFallback(lb, w.warnf, w.backendErrs), promoted)}, backends...)
			if w.cfg.Decide.Shadow == "lappi" {
				// The shadow is its own client over the slots not yet promoted:
				// one backend cannot be both in the chain and the shadow.
				if sb, err := newLappi(); err != nil {
					msg := fmt.Sprintf("lappi shadow disabled: %v", err)
					w.warnf("%s", msg)
					if advance {
						w.noteShadow(msg)
					}
				} else {
					lappiShadow = exceptSlots(logErrors(sb, w.warnf, w.backendErrs), promoted)
				}
			}
		} else if l.Primary {
			backends = append([]decide.Backend{logFallback(lb, w.warnf, w.backendErrs)}, backends...)
		} else {
			backends = append(backends, logErrors(lb, w.warnf, w.backendErrs))
		}
	}

	// A local decision model (llama.cpp /v1/systemone) speaks the decide/jev wire shape,
	// so it is a second decide/jev client under its own name, reaching only this Mac.
	// It is only ever the shadow (config enforces it): measured, never deciding.
	if l := w.cfg.Decide.Local; l != nil {
		endpoint := l.Endpoint
		if l.Server != nil {
			endpoint = w.localEndpoint // set by startRun when it started the server
		}
		lb, err := localBackend(l, endpoint)
		switch {
		case err != nil && l.Server != nil && !advance:
			// A pass that asks nothing starts no server (startRun): no endpoint
			// is expected, and saying the model is disabled would be noise.
		case err != nil:
			msg := fmt.Sprintf("local decision model disabled: %v", err)
			w.warnf("%s", msg)
			if advance {
				w.noteShadow(msg)
			}
		default:
			backends = append(backends, logErrors(lb, w.warnf, w.backendErrs))
		}
	}

	// The shadow backend is asked alongside the chain and never decides, so it
	// leaves the chain: asking it twice would pay twice for one answer.
	var shadow decide.Backend
	if lappiShadow != nil {
		shadow = lappiShadow
	} else if name := w.shadowName(); name != "" && !(len(promoted) > 0 && name == "lappi") {
		for i, b := range backends {
			if b.Name() == name {
				shadow = b
				backends = append(backends[:i:i], backends[i+1:]...)
				break
			}
		}
		if shadow == nil && advance {
			msg := fmt.Sprintf("shadow %q unavailable; running without a shadow", name)
			w.warnf("%s", msg)
			w.noteShadow(msg)
		}
	}
	// d1-ultra is retired per promoted slot: the OpenRouter model no longer
	// answers them (Lappi does; if Lappi is down the slot falls to the LLM or
	// reads unsure, never to d1-ultra). As the shadow it keeps reading them.
	if on := w.openRouterName(); len(promoted) > 0 && on != "" {
		for i, b := range backends {
			if b.Name() == on {
				backends[i] = exceptSlots(b, promoted)
			}
		}
	}
	if len(backends) == 0 && advance {
		return nil, errNoBackend
	}
	router := decide.NewRouter(backends...)
	var base decide.Decider = router
	if w.cfg.Decide.ShadowOnDropCandidates() {
		base = w.dropShadowing(router, shadow) // the shadow reads drop candidates, not a sample
	} else {
		router.Shadow = shadow
		gateShadow(router, w.cfg.Decide.ShadowSampleEvery(), shadowInFlight(w.cfg.Decide))
	}
	triage := w.escalating(base, esc)

	nspend, err := notesSpend(w.stateDir, w.cfg, spend)
	if err != nil {
		return nil, err
	}
	explainer, skipExplain := w.notes(d, nspend)
	r := &pipeline.Runner{
		Ledger:      w.led,
		Triager:     &jdtriage.Triager{D: triage, TargetSummary: w.cfg.TargetSummary, ExtraSlots: []decide.Slot{jobcategory.DecideSlot()}},
		Explainer:   explainer,
		Policy:      dropPolicy(w.cfg, w.openRouterName()),
		SkipExplain: skipExplain,
	}
	x, err := loadLCA(w.stateDir)
	if err != nil {
		return nil, err
	}
	if x != nil {
		r.Badger = lcaBadger{x}
	}
	if err := w.scoring(r); err != nil {
		return nil, err
	}
	r.Funnel = buildFunnel(w.cfg, !r.SkipScoring, nspend, notesRelevance(w.cfg, w.stateDir, w.led.All, w.warnf))
	return r, nil
}

// scoring sets r's scoring stage: DevPrism (started lazily), or SkipScoring
// with a warning while devprism.binary is empty, and the employer lookup the
// rank boost reads. The run and `vani rescore` share it, so a rescored posting
// is ranked exactly as a freshly scored one.
func (w *wired) scoring(r *pipeline.Runner) error {
	ex, err := loadEmployerView(w.stateDir, w.warnf)
	if err != nil {
		return err
	}
	if ex != nil {
		r.Employer = employerLookup{ex}
		r.PreferEmployers = w.cfg.EmployerPreferences
	} else if len(w.cfg.EmployerPreferences) > 0 {
		w.warnf("employer_preferences set but no employer lists imported; no rank boost (docs/EMPLOYER.md)")
	}
	if w.cfg.DevPrism.Binary == "" {
		w.warnf("devprism.binary empty; scoring disabled, the digest will not be ranked (vani rescore scores these postings once it is set)")
		r.SkipScoring = true
	} else {
		r.Scorer = w.lazyScorer()
	}
	return nil
}
