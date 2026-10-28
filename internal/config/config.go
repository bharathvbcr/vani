// Package config loads, validates and saves Vani's JSON configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/employer"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/avature"
	"github.com/bharathvbcr/Vani/internal/sources/ibm"
	"github.com/bharathvbcr/Vani/internal/sources/page"
)

const (
	// Version is the only config schema version this build reads.
	Version = 1
	// MaxDailyUSD is the ceiling for any per-day spend cap.
	MaxDailyUSD = 50.0
	// maxFileBytes bounds how much of a config file is read.
	maxFileBytes = 1 << 20

	hostOpenRouter = "openrouter.ai"
)

// Config is the whole on-disk configuration.
type Config struct {
	Version      int             `json:"version"`
	Watchlist    []sources.Board `json:"watchlist"`
	AllowedHosts []string        `json:"allowed_hosts"`
	Schedule     Schedule        `json:"schedule"`
	Decide       Decide          `json:"decide"`
	LLM          LLM             `json:"llm"`
	DevPrism     DevPrism        `json:"devprism"`
	DropRules    []DropRule      `json:"drop_rules"`
	// RankPenalties demote, never drop: a kept posting whose triage slot reads
	// one of Values ranks lower in Jobs and the digest, in proportion to how
	// sure the reading is (Rule 7: an ordering preference, not an engine rule).
	RankPenalties []RankPenalty `json:"rank_penalties,omitempty"`
	// AgeNeutralCompanies name employers that close postings by an application
	// cap, not by age (matched on the posting's company, ignoring case and outer
	// space). Their undated postings are not aged by when Vani first saw them; a
	// date the board gives still counts. An ordering preference only (Rule 7).
	AgeNeutralCompanies []string `json:"age_neutral_companies,omitempty"`
	// RankWeightsFile is a rank weights file `vani rankfit` wrote (fitted Score
	// shares and per-backend calibration). Empty means the hand weights. A file
	// that is missing or invalid also means the hand weights, with a warning on
	// `vani jobs` and the digest; it is never used half-read.
	RankWeightsFile string `json:"rank_weights_file,omitempty"`
	// KBMatch configures `vani kbmatch`, the language-model read of whether a
	// posting's work is the work in the knowledge base.
	KBMatch       KBMatch `json:"kbmatch,omitzero"`
	TargetSummary string  `json:"target_summary"`
	// MotivationNotes is the owner's own note on what they care about in
	// their work. `vani draft` gives it to motivation questions ("Why
	// <company>?") as trusted text in the prompt, so it must be
	// non-identifying like target_summary: no name, contact details,
	// location, work authorization, or names of people, employers or
	// schools. At most draft.MaxMotivationNotesBytes after whitespace
	// collapse. Tracks leave it alone.
	MotivationNotes string `json:"motivation_notes,omitempty"`
	// Track is the id of the track decider `vani track use` last applied.
	Track string `json:"track,omitempty"`
	// TracksDir holds track decider files (<id>.json). Empty means
	// <state dir>/tracks.
	TracksDir string `json:"tracks_dir,omitempty"`
	// EmployerPreferences are employer attributes (employer.Attributes) the
	// active track favours. They boost rank only, never change a posting's
	// status, and never enter a model prompt: they imply work authorization.
	EmployerPreferences []string `json:"employer_preferences,omitempty"`
	// TitleSignals order each run's triage: postings whose titles match the
	// active track's include signals go first, excluded ones last. Their read
	// entries read triage slots from the title before any model is asked; a
	// drop rule for the slot and value read drops the posting unasked.
	TitleSignals TitleSignals `json:"title_signals,omitzero"`
	// Exclude is the owner's hard filter on a posting's own title, company,
	// location and description: what it matches is dropped without asking any
	// backend, and kept postings it comes to match are dropped at the next
	// run. Tracks leave it alone.
	Exclude Exclude `json:"exclude,omitzero"`
	// Funnel tunes the triage funnel's spend (funnel.go): the thin-description
	// skip, the notes relevance floor and the notes' own daily cap.
	Funnel Funnel `json:"funnel,omitzero"`
	// KB locates the canonical career knowledge base (rkb's bank.json) that
	// `vani kb sync` copies one way into DevPrism.
	KB KB `json:"kb,omitzero"`
	// LightpandaBinary is the headless browser used for careers-page boards.
	// Empty means look up "lightpanda" on PATH when a page board exists.
	LightpandaBinary string `json:"lightpanda_binary,omitempty"`
	// PageIntervalMS spaces careers-page fetches to one host: a lightpanda run
	// starts only this long after the previous run to that host ended. It is a
	// floor: a longer Crawl-delay in the host's robots.txt wins. Zero leaves
	// Crawl-delay alone. At most page.MaxInterval.
	PageIntervalMS int `json:"page_interval_ms,omitempty"`
	// AdvancePerRun bounds how many postings one run triages, scores and
	// explains, which bounds a run's spend and duration. Postings past it stay
	// live (listed, not yet triaged) for the next run. Zero means
	// DefaultAdvancePerRun; at most MaxAdvancePerRun.
	AdvancePerRun int `json:"advance_per_run,omitempty"`
	// AdvanceConcurrency is how many postings one run advances at a time.
	// Ledger writes stay serialised, and the cap and the stop rules are
	// unchanged. Zero means DefaultAdvanceConcurrency; 1 is strictly one at a
	// time; at most MaxAdvanceConcurrency.
	AdvanceConcurrency int `json:"advance_concurrency,omitempty"`
	// Semantic matches postings against the knowledge base (kb.bank) with
	// EmbeddingGemma 2 on Tessl. It only orders and explains postings, never
	// drops one. Off when snapshot or kb.bank is empty.
	Semantic Semantic `json:"semantic,omitzero"`
	// Digest sizes the digest's Top open and Explore sections.
	Digest Digest `json:"digest,omitzero"`
}

// Digest sizes the digest's extra sections. A nil field reads as its default;
// an explicit 0 switches the section off.
type Digest struct {
	// TopOpen is how many of the best already-digested, still open roles
	// "Top open" lists, by relevance. Default DefaultDigestTopOpen; at most
	// MaxDigestTopOpen.
	TopOpen *int `json:"top_open,omitempty"`
	// ExploreBelowFold and ExploreDropped are how many roles "Explore" draws
	// at random from lower-ranked digested roles and from dropped roles, so
	// the owner's judgement reaches what the ranking would never show. At
	// most MaxDigestExplore each.
	ExploreBelowFold *int `json:"explore_below_fold,omitempty"`
	ExploreDropped   *int `json:"explore_dropped,omitempty"`
}

// Digest section defaults and bounds (the bounds match digest.MaxTopOpen and
// digest.MaxExplore; a test in cmd/vani holds them equal).
const (
	DefaultDigestTopOpen          = 20
	DefaultDigestExploreBelowFold = 2
	DefaultDigestExploreDropped   = 1
	MaxDigestTopOpen              = 50
	MaxDigestExplore              = 10
)

func orDefault(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}

// DigestTopOpen is Digest.TopOpen with absent read as the default.
func (c Config) DigestTopOpen() int { return orDefault(c.Digest.TopOpen, DefaultDigestTopOpen) }

// DigestExploreBelowFold is Digest.ExploreBelowFold with absent read as the default.
func (c Config) DigestExploreBelowFold() int {
	return orDefault(c.Digest.ExploreBelowFold, DefaultDigestExploreBelowFold)
}

// DigestExploreDropped is Digest.ExploreDropped with absent read as the default.
func (c Config) DigestExploreDropped() int {
	return orDefault(c.Digest.ExploreDropped, DefaultDigestExploreDropped)
}

// Semantic configures embedding-based matching against the knowledge base.
type Semantic struct {
	// Snapshot is the absolute path of a google/embeddinggemma-2 Hugging Face
	// snapshot directory. Only a vani built with -tags tessl can use it.
	Snapshot string `json:"snapshot,omitempty"`
	// WarmPerRun bounds how many postings one daily run embeds. Zero means
	// DefaultSemanticWarmPerRun; at most MaxSemanticWarmPerRun.
	WarmPerRun int `json:"warm_per_run,omitempty"`
}

// MaxRankPenalties bounds Config.RankPenalties.
const MaxRankPenalties = 32

// MaxAgeNeutralCompanies and MaxAgeNeutralNameBytes bound
// Config.AgeNeutralCompanies.
const (
	MaxAgeNeutralCompanies = 64
	MaxAgeNeutralNameBytes = 80
)

// Semantic warm bounds (Semantic.WarmPerRun).
const (
	DefaultSemanticWarmPerRun = 1000
	MaxSemanticWarmPerRun     = 5000
)

// SemanticOn reports whether semantic matching is configured: a snapshot and
// a bank to build the profile from.
func (c Config) SemanticOn() bool { return c.Semantic.Snapshot != "" && c.KB.Bank != "" }

// SemanticWarmLimit is WarmPerRun with zero read as the default.
func (c Config) SemanticWarmLimit() int {
	if c.Semantic.WarmPerRun == 0 {
		return DefaultSemanticWarmPerRun
	}
	return c.Semantic.WarmPerRun
}

// Per-run advance bounds (Config.AdvancePerRun, Config.AdvanceConcurrency).
const (
	DefaultAdvancePerRun      = 300
	MaxAdvancePerRun          = 5000
	DefaultAdvanceConcurrency = 4
	MaxAdvanceConcurrency     = 16
)

// AdvanceLimit is AdvancePerRun with zero read as the default.
func (c Config) AdvanceLimit() int {
	if c.AdvancePerRun == 0 {
		return DefaultAdvancePerRun
	}
	return c.AdvancePerRun
}

// AdvanceWorkers is AdvanceConcurrency with zero read as the default.
func (c Config) AdvanceWorkers() int {
	if c.AdvanceConcurrency == 0 {
		return DefaultAdvanceConcurrency
	}
	return c.AdvanceConcurrency
}

// PageInterval is PageIntervalMS as a duration; zero means no wait.
func (c Config) PageInterval() time.Duration {
	return time.Duration(c.PageIntervalMS) * time.Millisecond
}

// KB locates rkb and its bank. Paths are absolute.
type KB struct {
	Bank     string `json:"bank,omitempty"`      // rkb bank.json
	RKB      string `json:"rkb,omitempty"`       // rkb binary
	CodeRoot string `json:"code_root,omitempty"` // folder holding career/ (rkb's RKB_CODE_ROOT)
	// Selections are the rkb selection files (resumes/*.json) the owner keeps
	// current. Only bullets they list, and that pass kbsync.Eligible, leave
	// the career pipeline (kb sync, answer drafts, a built resume). Empty
	// means no bullet is eligible: the bank alone never vouches for one.
	Selections []string `json:"selections,omitempty"`
	// BaseSelection is the selection `vani resume` tailors: its header,
	// layout and section skeleton, rendered by rkb in the owner's template.
	// It must be one of Selections.
	BaseSelection string `json:"base_selection,omitempty"`
	// VMReport is the rkb validator report (`rkb claims vm --out …`) that
	// `vani resume` passes to rkb's claims export: which figures count as
	// verified, and so which bullets may print, depends on it. Absolute.
	VMReport string `json:"vm_report,omitempty"`
}

// MaxKBSelections caps kb.selections.
const MaxKBSelections = 32

// Schedule says when the daily run fires.
type Schedule struct {
	DailyAt  string `json:"daily_at"` // "HH:MM", 24-hour
	Timezone string `json:"timezone"` // IANA zone name
}

// Decide configures the decision engines and their daily spend caps.
type Decide struct {
	// OpenRouterModel is the decision model asked through the OpenRouter
	// Decisions API (e.g. "liquid/d1-ultra"); empty turns it off. It answers
	// under OpenRouterName.
	OpenRouterModel  string   `json:"openrouter_model"`
	OpenRouterKeyEnv string   `json:"openrouter_key_env"`
	DailyUSD         DailyUSD `json:"daily_usd"`
	// Shadow names a backend asked alongside the primary chain whose answers
	// are recorded but never used ("" off, "llm", "openrouter", "lappi",
	// "local"). Disagreements feed the labelling queue. It costs a second
	// call per posting (nothing for lappi or local, which run on this Mac).
	Shadow string `json:"shadow,omitempty"`
	// ShadowEvery asks the shadow about one posting in this many, picked by a
	// hash of the posting key so a posting is always in or out of the sample.
	// An unsampled posting has no shadow reading, which `vani eval` reads as
	// not asked. The primary waits for the shadow only on sampled postings.
	// Zero means DefaultShadowEvery; 1 asks it about every posting; at most
	// MaxShadowEvery.
	ShadowEvery int `json:"shadow_every,omitempty"`
	// ShadowMode picks which postings the shadow reads: ShadowModeSample (the
	// default, "") is the shadow_every hash sample; ShadowModeDropCandidates
	// asks it about every slot the drop policy would drop on a calibrated
	// reading, before the drop is applied. Either way the shadow only
	// annotates: the drop follows the primary's rule.
	ShadowMode string `json:"shadow_mode,omitempty"`
	// Lappi configures the local Lappi agent. It runs as the shadow until
	// `vani eval` reports it promotable; Primary then puts it first in the
	// chain, ahead of the OpenRouter model and the LLM.
	Lappi *Lappi `json:"lappi,omitempty"`
	// Local is a third-party decision model served on this Mac by llama.cpp's
	// /v1/systemone endpoint (e.g. Liquid d1-3B). It is measured as the shadow
	// only: if `vani eval` finds it promotable, it is served through Lappi's
	// socket rather than promoted here (owner decision 2026-10-18).
	Local *Local `json:"local,omitempty"`
	// Escalate re-asks the LLM (llm.triage_model) about the slots the primary
	// chain read with low confidence. Its readings are recorded beside the
	// primary's, never in place of them. It uses the shadow's recording, so it
	// is exclusive with a sampled shadow; with shadow_mode drop_candidates the
	// two read disjoint slots and coexist. Absent means off.
	Escalate *Escalate `json:"escalate,omitempty"`
}

// Escalate configures decide.escalate. BelowP and MaxPerRun are pointers so an
// explicit zero in the file is refused rather than read as "use the default".
type Escalate struct {
	Enabled bool `json:"enabled"`
	// BelowP re-asks a slot whose primary probability is under it: within
	// (0, 1], default DefaultEscalateBelowP.
	BelowP *float64 `json:"below_p,omitempty"`
	// MaxPerRun bounds the slots re-asked in one run: 1..MaxEscalatePerRun,
	// default DefaultEscalateMaxPerRun.
	MaxPerRun *int `json:"max_per_run,omitempty"`
}

// Escalation bounds (Escalate.BelowP, Escalate.MaxPerRun).
const (
	DefaultEscalateBelowP    = 0.5
	DefaultEscalateMaxPerRun = 200
	MaxEscalatePerRun        = 5000
)

// EscalateEnabled reports whether decide.escalate is present and switched on.
func (d Decide) EscalateEnabled() bool { return d.Escalate != nil && d.Escalate.Enabled }

// EscalateBelowP is the probability under which a slot is escalated, with an
// absent value read as the default.
func (d Decide) EscalateBelowP() float64 {
	if d.Escalate == nil || d.Escalate.BelowP == nil {
		return DefaultEscalateBelowP
	}
	return *d.Escalate.BelowP
}

// EscalateMaxPerRun is the per-run bound on escalated slots, with an absent
// value read as the default.
func (d Decide) EscalateMaxPerRun() int {
	if d.Escalate == nil || d.Escalate.MaxPerRun == nil {
		return DefaultEscalateMaxPerRun
	}
	return *d.Escalate.MaxPerRun
}

// Shadow sampling bounds (Decide.ShadowEvery). One in 12 (owner's choice,
// 2026-10-19) keeps a parallel-1 local shadow at about the rate it can serve
// while four postings advance at a time.
const (
	DefaultShadowEvery = 12
	MaxShadowEvery     = 1000
)

// Decide.ShadowMode values.
const (
	ShadowModeSample         = "sample"
	ShadowModeDropCandidates = "drop_candidates"
)

// ShadowOnDropCandidates reports whether the shadow is asked about drop
// candidates instead of the hash sample.
func (d Decide) ShadowOnDropCandidates() bool { return d.ShadowMode == ShadowModeDropCandidates }

// ShadowSampleEvery is ShadowEvery with zero read as the default.
func (d Decide) ShadowSampleEvery() int {
	if d.ShadowEvery == 0 {
		return DefaultShadowEvery
	}
	return d.ShadowEvery
}

// MaxLocalTimeoutMS bounds one local decision call.
const MaxLocalTimeoutMS = 60_000

// Local locates the local decision server: either Endpoint, http(s) on a
// loopback host, for a server you keep running, or Server, which Vani starts
// on a free loopback port for each run and stops afterwards (exactly one of
// the two). Model is recorded in the request and names the model in use.
type Local struct {
	Endpoint  string       `json:"endpoint,omitempty"`
	Server    *LocalServer `json:"server,omitempty"`
	Model     string       `json:"model"`
	TimeoutMS int          `json:"timeout_ms,omitempty"` // zero means 20 s
	// Questions is "combined" (default: the one question the OpenRouter decision model gets, so the eval
	// compares models on identical requests) or "per_slot" (each slot asked
	// its own fixture family question, as Lappi is). Which suits a given
	// model is what `vani eval` measures.
	Questions string `json:"questions,omitempty"`
}

// LocalServer is a llama-server Vani runs for the length of one run.
type LocalServer struct {
	Binary         string `json:"binary"`                     // absolute, e.g. /opt/homebrew/bin/llama-server
	ModelPath      string `json:"model_path"`                 // absolute path of the GGUF
	ContextSize    int    `json:"context_size,omitempty"`     // zero means 8192
	StartTimeoutMS int    `json:"start_timeout_ms,omitempty"` // zero means 90 s; at most 300000
	// BatchSize sets llama.cpp's batch and micro-batch (-b, -ub). Zero keeps
	// llama.cpp's default (512). A model that reads the whole prompt in one
	// batch (vllm-d3) needs it at least as large as the longest prompt: 8192.
	BatchSize int `json:"batch_size,omitempty"`
	// Parallel sets -np; zero keeps llama.cpp's auto (a unified KV pool). An
	// explicit value splits context_size across that many slots.
	Parallel int `json:"parallel,omitempty"`
}

// MaxLappiTimeoutMS is the caller contract's batch ceiling (10 s).
const MaxLappiTimeoutMS = 10_000

// Bounds on decide.lappi.primary_slots validation: the slot set has four
// members, so a list over maxPrimarySlotsListed is refused outright, and
// per-entry problems are reported only up to maxPrimarySlotProblems.
const (
	maxPrimarySlotsListed  = 16
	maxPrimarySlotProblems = 5
)

// Lappi locates the agent. Socket empty means LAPPI_SOCKET or
// ~/Library/Caches/qd/qd.sock; TimeoutMS zero means 5 s.
type Lappi struct {
	Socket    string `json:"socket,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	// Primary puts Lappi first for every triage slot.
	Primary bool `json:"primary,omitempty"`
	// PrimarySlots promotes Lappi slot by slot: each named slot goes to Lappi
	// first and the OpenRouter decision model no longer answers it, while the
	// other slots keep the arrangement they have (shadow, or the chain alone).
	// Exclusive with Primary; every name must be a triage slot.
	PrimarySlots []string `json:"primary_slots,omitempty"`
}

// PromotedSlots returns the slots routed to Lappi first under PrimarySlots, in
// the triage fixture's order. Primary (all slots) is not reported here: it
// keeps its own, older arrangement.
func (l *Lappi) PromotedSlots() []string {
	if l == nil || len(l.PrimarySlots) == 0 {
		return nil
	}
	var out []string
	for _, f := range jdtriage.Families {
		if slices.Contains(l.PrimarySlots, f.Slot.Name) {
			out = append(out, f.Slot.Name)
		}
	}
	return out
}

// DailyUSD holds the per-engine daily spend caps in US dollars.
type DailyUSD struct {
	OpenRouter float64 `json:"openrouter"`
	LLM        float64 `json:"llm"`
}

// LLM selects the Manvi provider and model.
type LLM struct {
	Provider string `json:"provider"` // "gemini" or "anthropic"
	Model    string `json:"model"`
	// Conservative per-token prices in nano-USD, used by Manvi's budget ledger
	// to reserve before each request. Zero for gemini means the conservative
	// defaults below; anthropic has no built-in default and must set both.
	InputNanoUSDPerToken  int64 `json:"input_nano_usd_per_token,omitempty"`
	OutputNanoUSDPerToken int64 `json:"output_nano_usd_per_token,omitempty"`
	// TriageModel is the model that answers posting triage when the LLM is
	// asked as a cheaper tier (decide.escalate); empty means Model. Drafts, form
	// fields and the reasons catalogue keep using Model.
	TriageModel string `json:"triage_model,omitempty"`
}

// MaxModelName bounds a configured model id (llm.model, llm.triage_model,
// kbmatch.model).
const MaxModelName = 128

// The backends `vani kbmatch` may ask.
const (
	// KBMatchLLM asks the Manvi llm provider (llm.provider).
	KBMatchLLM = "llm"
	// KBMatchOpenRouter asks the decision model (decide.openrouter_model,
	// Liquid D) over OpenRouter, from the OpenRouter daily cap. It was "jev"
	// until 2026-10-20, which still loads (legacy.go).
	KBMatchOpenRouter = "openrouter"
)

// KBMatch configures `vani kbmatch`. The zero value asks the llm on
// llm.triage_model, else llm.model.
type KBMatch struct {
	// Backend is KBMatchLLM (the default) or KBMatchOpenRouter.
	Backend string `json:"backend,omitempty"`
	// Model is the llm model that answers when Backend is KBMatchLLM. Empty means
	// llm.triage_model, else llm.model. It is separate from llm.triage_model so a
	// cheap model can read postings here without changing how triage runs.
	Model string `json:"model,omitempty"`
}

// EffectiveBackend is Backend with empty read as KBMatchLLM and the old
// "jev" read as KBMatchOpenRouter.
func (k KBMatch) EffectiveBackend() string {
	switch k.Backend {
	case "":
		return KBMatchLLM
	case legacyKBMatchBackend:
		return KBMatchOpenRouter
	}
	return k.Backend
}

// KBMatchModel is the llm model `vani kbmatch` asks.
func (c Config) KBMatchModel() string {
	if c.KBMatch.Model != "" {
		return c.KBMatch.Model
	}
	return c.LLM.EffectiveTriageModel()
}

// EffectiveTriageModel is TriageModel with empty read as Model.
func (l LLM) EffectiveTriageModel() string {
	if l.TriageModel == "" {
		return l.Model
	}
	return l.TriageModel
}

// Conservative Gemini Flash rates (the table Jarvis uses for its Gemini
// campaign ledger, internal/app/ledger.go): over-stating is safe, the ledger
// only refuses early.
const (
	GeminiInputNanoUSD  = 1500
	GeminiOutputNanoUSD = 7500
	// maxNanoUSDPerToken bounds a configured price ($1,000 per million tokens).
	maxNanoUSDPerToken = 1_000_000
)

// Prices returns the per-token prices to meter the configured provider with.
func (l LLM) Prices() (input, output int64, err error) {
	in, out := l.InputNanoUSDPerToken, l.OutputNanoUSDPerToken
	if l.Provider == "gemini" {
		if in == 0 {
			in = GeminiInputNanoUSD
		}
		if out == 0 {
			out = GeminiOutputNanoUSD
		}
	}
	if in <= 0 || out <= 0 {
		return 0, 0, fmt.Errorf("llm: %s needs input_nano_usd_per_token and output_nano_usd_per_token", l.Provider)
	}
	if in > maxNanoUSDPerToken || out > maxNanoUSDPerToken {
		return 0, 0, fmt.Errorf("llm: per-token price above %d nano-USD", maxNanoUSDPerToken)
	}
	return in, out, nil
}

// DevPrism locates the deterministic resume tools.
type DevPrism struct {
	Binary    string `json:"binary"`
	ToolsHash string `json:"tools_hash"`
}

// RankPenalty demotes a kept posting whose triage slot reads one of Values.
type RankPenalty struct {
	Slot   string   `json:"slot"`
	Values []string `json:"values"`
}

// DropRule drops a posting when a triage slot reads one of Values with enough
// confidence. Dropping is an engine rule keyed on an annotation.
type DropRule struct {
	Slot   string   `json:"slot"`
	Values []string `json:"values"`
	// MinOpenRouterProb is the OpenRouter model's probability of the chosen
	// value at or above which the rule drops; zero never drops on it.
	MinOpenRouterProb float64 `json:"min_openrouter_prob"`
	MinLappiScore     float64 `json:"min_lappi_score"`
}

// Paths resolves the config file and state directory from the environment.
// VANI_CONFIG and VANI_STATE_DIR win; otherwise macOS keeps both under
// ~/Library/Application Support/Vani and Linux follows the XDG base
// directories: $XDG_CONFIG_HOME/vani/config.json and $XDG_STATE_HOME/vani.
// Windows uses %AppData%\Vani (compiled, untested, unsupported).
func Paths(getenv func(string) string, home string) (configPath, stateDir string) {
	return pathsFor(runtime.GOOS, getenv, home)
}

func pathsFor(goos string, getenv func(string) string, home string) (configPath, stateDir string) {
	switch goos {
	case "linux":
		configPath = filepath.Join(xdgDir(getenv, "XDG_CONFIG_HOME", home, ".config"), "vani", "config.json")
		stateDir = filepath.Join(xdgDir(getenv, "XDG_STATE_HOME", home, ".local", "state"), "vani")
	case "windows":
		// %AppData%\Vani, as os.UserConfigDir resolves it; the profile's
		// Roaming folder when the variable is missing.
		appData := getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		base := filepath.Join(appData, "Vani")
		configPath = filepath.Join(base, "config.json")
		stateDir = filepath.Join(base, "state")
	default:
		base := filepath.Join(home, "Library", "Application Support", "Vani")
		configPath = filepath.Join(base, "config.json")
		stateDir = filepath.Join(base, "state")
	}
	if v := getenv("VANI_CONFIG"); v != "" {
		configPath = v
	}
	if v := getenv("VANI_STATE_DIR"); v != "" {
		stateDir = v
	}
	return configPath, stateDir
}

// xdgDir is the XDG variable's value when it is an absolute path, else the
// spec's default under home. The spec says a relative value is invalid and
// must be ignored.
func xdgDir(getenv func(string) string, key, home string, def ...string) string {
	if v := getenv(key); filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(append([]string{home}, def...)...)
}

// Default returns an example configuration with an empty watchlist.
func Default() Config {
	return Config{
		Version:      Version,
		Watchlist:    []sources.Board{},
		AllowedHosts: []string{hostOpenRouter},
		Schedule:     Schedule{DailyAt: "08:30", Timezone: "America/Los_Angeles"},
		Decide: Decide{
			// Liquid D, the decision model Vani runs (FOLLOWUPS: d1-ultra).
			OpenRouterModel:  "liquid/d1-ultra",
			OpenRouterKeyEnv: "OPENROUTER_API_KEY",
			DailyUSD:         DailyUSD{OpenRouter: 0.25, LLM: 1.0},
		},
		// No llm: a fresh config names no paid language model (owner's call,
		// 2026-10-20). Set llm.provider, llm.model and, for anthropic, both
		// per-token prices to turn it on.
		// Empty until Phase 0 confirms the installed DevPrism answers --mcp
		// without migrating career.db; scoring is skipped while it is empty.
		// The installed binary is /Applications/DevPrism.app/Contents/MacOS/claude-prism-desktop.
		DevPrism:  DevPrism{},
		DropRules: []DropRule{},
	}
}

// Load reads and validates the config at path. Unknown fields are an error.
func Load(path string) (Config, error) {
	c, _, err := LoadWithDeprecations(path)
	return c, err
}

// LoadWithDeprecations is Load that also returns the deprecated key names the
// file still uses (see Deprecations), for the caller to warn about.
func LoadWithDeprecations(path string) (Config, []string, error) {
	// Stat (following symlinks) before opening: opening a FIFO for reading
	// blocks until a writer appears, and a device or directory is not a config.
	st, err := os.Stat(path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("config: %w", err)
	}
	if !st.Mode().IsRegular() {
		return Config{}, nil, fmt.Errorf("config: %s is not a regular file (%s)", path, st.Mode().Type())
	}
	f, err := os.Open(path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return Config{}, nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(data) > maxFileBytes {
		return Config{}, nil, fmt.Errorf("config: %s exceeds %d bytes", path, maxFileBytes)
	}
	c, err := Parse(data)
	if err != nil {
		return Config{}, nil, err
	}
	return c, Deprecations(data), nil
}

// Parse decodes and validates config bytes. Deprecated key names are read as
// their replacements (legacy.go).
func Parse(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config: decode: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Config{}, errors.New("config: trailing data after JSON document")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save replaces the whole file with c: `vani init` and test fixtures, where
// there is nothing to read first. It takes the same lock as Update. A change
// to what the file holds already goes through Update, never Load then Save.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	target, unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer unlock()
	return writeLocked(target, c)
}

// Create writes c as a new file and fails with an error wrapping os.ErrExist
// when the path is already there, checked under the lock: two `vani init`
// racing cannot both win.
func Create(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// The file that would be created is the resolved target: a dangling link
	// is not "already there", its target is.
	target, unlock, err := lockConfig(path)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("config: %s already exists: %w", target, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config: %w", err)
	}
	return writeLocked(target, c)
}

// writeLocked validates c and writes it atomically with mode 0600 (see
// atomicfile.Write). The caller holds the config's lock and passes the target
// lockConfig resolved: the lock and the write name one file.
func writeLocked(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	if err := atomicfile.Write(path, append(data, '\n')); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// ParseDailyAt parses "HH:MM" (24-hour).
func ParseDailyAt(s string) (hour, minute int, err error) {
	t, perr := time.Parse("15:04", s)
	if perr != nil || len(s) != 5 {
		return 0, 0, fmt.Errorf("daily_at %q is not HH:MM (24-hour)", s)
	}
	return t.Hour(), t.Minute(), nil
}

// Parsed returns the hour, minute and location of the schedule.
func (s Schedule) Parsed() (hour, minute int, loc *time.Location, err error) {
	hour, minute, err = ParseDailyAt(s.DailyAt)
	if err != nil {
		return 0, 0, nil, err
	}
	if s.Timezone == "" || s.Timezone == "Local" {
		return 0, 0, nil, fmt.Errorf("timezone %q must be an explicit IANA zone", s.Timezone)
	}
	loc, err = time.LoadLocation(s.Timezone)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("timezone %q: %w", s.Timezone, err)
	}
	return hour, minute, loc, nil
}

// OpenRouterConfigured reports whether the OpenRouter decision model is set up.
func (c Config) OpenRouterConfigured() bool { return c.Decide.OpenRouterModel != "" }

// BoardHost returns the API host a board is fetched from. Workday hosts are
// tenant-specific and not derivable from the token, so ok is false for them.
func BoardHost(b sources.Board) (host string, ok bool) {
	switch b.ATS {
	case posting.Greenhouse:
		return "boards-api.greenhouse.io", true
	case posting.Lever:
		if b.Region == "eu" {
			return "api.eu.lever.co", true
		}
		return "api.lever.co", true
	case posting.Ashby:
		return "api.ashbyhq.com", true
	case posting.Rippling:
		return "api.rippling.com", true
	case posting.Workable:
		return "apply.workable.com", true
	case posting.Gem:
		return "api.gem.com", true
	case posting.Recruitee, posting.Pinpoint, posting.BambooHR, posting.Personio:
		// One host per tenant, built from the token, so the token must be a
		// single host label (ValidBoard checks the same rule).
		if sources.TenantLabelProblem(b.Token) != "" {
			return "", false
		}
		return b.Token + sources.TenantHostSuffix[b.ATS], true
	case posting.TikTok:
		return "api.lifeattiktok.com", true
	case posting.Amazon:
		return "www.amazon.jobs", true
	case posting.Google:
		return "www.google.com", true
	case posting.Apple:
		return "jobs.apple.com", true
	case posting.SmartRecruiters:
		return "api.smartrecruiters.com", true
	case posting.IBM:
		return "www-api.ibm.com", true
	case posting.DEShaw:
		return "www.deshaw.com", true
	case posting.Atlassian:
		return "www.atlassian.com", true
	case posting.Workday, posting.Eightfold, posting.Jibe, posting.Avature, posting.OracleHCM:
		if h := strings.ToLower(strings.TrimSpace(b.Host)); h != "" {
			return h, true
		}
	case posting.Page, posting.Feed:
		if u, err := url.Parse(b.URL); err == nil && u.Hostname() != "" {
			return strings.ToLower(u.Hostname()), true
		}
	}
	return "", false
}

// ashbyJobHost is the public job page. The posting API is api.ashbyhq.com;
// a board that has turned that API off is read from this host instead, so
// every Ashby board needs it allowed.
const ashbyJobHost = "jobs.ashbyhq.com"

// BoardHosts is every host a board fetches: BoardHost, plus the job-page host
// an Ashby board falls back to when the posting API answers 404.
func BoardHosts(b sources.Board) []string {
	var out []string
	if h, ok := BoardHost(b); ok {
		out = append(out, h)
	}
	if b.ATS == posting.Ashby {
		out = append(out, ashbyJobHost)
	}
	return out
}

// workdayName matches a Workday "tenant/site" token whose parts are the safe
// path names the adapter accepts (workday.parseToken).
var workdayName = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)

// maxLinkPattern bounds a page board's regexp source.
const maxLinkPattern = 256

// RequiredHosts lists the hosts the config needs allowed, sorted and unique.
func (c Config) RequiredHosts() []string {
	set := map[string]bool{}
	for _, b := range c.Watchlist {
		for _, h := range BoardHosts(b) {
			set[h] = true
		}
		if b.Domain != "" {
			set[b.Domain] = true
		}
		if h, ok := IconHost(b.Icon); ok {
			set[h] = true
		}
	}
	if c.OpenRouterConfigured() {
		set[hostOpenRouter] = true
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// hostname is a DNS name of at least two labels (no IP literal, no port).
var hostname = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// searchATS take a search (Workday and Jibe optionally, since they page a
// whole company's listing; the search sites require one, because their
// unsearched listing is the whole company and cannot be paged to the end).
func searchATS(a posting.ATS) bool {
	switch a {
	case posting.Workday, posting.OracleHCM, posting.Amazon, posting.Google, posting.Apple, posting.Eightfold, posting.Jibe, posting.IBM, posting.TikTok:
		return true
	}
	return false
}

// optionalSearchATS list their whole board when given no search. An Oracle HCM
// board over the adapter's cap (jpmc/CX_1001 counts 7,317) fails at the first
// page with the way out; a small tenant needs no search.
func optionalSearchATS(a posting.ATS) bool {
	return a == posting.Workday || a == posting.Jibe || a == posting.OracleHCM || a == posting.TikTok
}

// oracleHCMName matches an Oracle HCM "tenant/site" token whose parts are the
// safe path and finder names the adapter accepts (oraclehcm.parseToken).
var oracleHCMName = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_-]+$`)

// validOracleHCM checks an oc board: the token is "tenant/site" and the host
// "{tenant}.fa[.{region}].oraclecloud.com" whose first label is the tenant.
func validOracleHCM(b sources.Board) error {
	if !oracleHCMName.MatchString(b.Token) {
		return fmt.Errorf("oc token %q must be tenant/site, each of letters, digits, '-' and '_'", b.Token)
	}
	tenant, _, _ := strings.Cut(strings.ToLower(b.Token), "/")
	label, ok := sources.OracleHCMHostTenant(b.Host)
	if !ok {
		return fmt.Errorf("oc board %s needs host {tenant}.fa.oraclecloud.com or {tenant}.fa.{region}.oraclecloud.com, got %q", b.Token, b.Host)
	}
	if label != tenant {
		return fmt.Errorf("oc host %q does not belong to tenant %q", b.Host, tenant)
	}
	// The adapter quotes a term into the finder, where a quote ends it and a
	// backslash escapes the next character, so it refuses both.
	for _, s := range b.SearchTerms() {
		if strings.ContainsAny(s, `"\`) {
			return fmt.Errorf("oc board %s: search %q must not contain a quote or a backslash", b.Token, s)
		}
	}
	return nil
}

// jibeClient matches a Jibe client code, a Jibe board's token.
var jibeClient = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// smartRecruitersCompany matches a SmartRecruiters company identifier, a
// board's token (smartrecruiters.tokenRE); srCountry the one two-letter ISO
// code its country filter takes (the adapter lower-cases it).
var (
	smartRecruitersCompany = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
	srCountry              = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

// avatureToken matches an Avature board's token, the owner's slug for it.
var avatureToken = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// SingleBoardATS are systems with one tenant: two boards of one would key the
// same posting twice, so a watchlist holds at most one of each.
func SingleBoardATS(a posting.ATS) bool {
	return a == posting.Amazon || a == posting.Google || a == posting.Apple || a == posting.DEShaw || a == posting.Atlassian
}

func cleanText(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// validSearches bounds the board's search terms and filters.
func validSearches(b sources.Board) error {
	if !searchATS(b.ATS) {
		if b.Search != "" || len(b.Searches) > 0 {
			return fmt.Errorf("board %s/%s: search applies to wd, oc, az, gg, ap, ef, jb, ib and tt only", b.ATS, b.Token)
		}
	}
	if len(b.Searches) > sources.MaxSearches {
		return fmt.Errorf("board %s/%s: %d searches, more than %d", b.ATS, b.Token, len(b.Searches), sources.MaxSearches)
	}
	for _, s := range append([]string{b.Search}, b.Searches...) {
		if len(s) > sources.MaxSearchLen || !cleanText(s) {
			return fmt.Errorf("board %s/%s: search %q must be at most %d bytes with no control characters", b.ATS, b.Token, s, sources.MaxSearchLen)
		}
	}
	if !optionalSearchATS(b.ATS) && searchATS(b.ATS) && len(b.SearchTerms()) == 0 {
		return fmt.Errorf("board %s/%s needs at least one search: its unsearched listing is the whole company", b.ATS, b.Token)
	}
	if len(b.Filters) == 0 {
		return nil
	}
	allowed := sources.FilterKeys[b.ATS]
	if len(allowed) == 0 {
		return fmt.Errorf("board %s/%s: filters are not supported for %s", b.ATS, b.Token, b.ATS)
	}
	for k, vs := range b.Filters {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("board %s/%s: filter %q is not one of %s", b.ATS, b.Token, k, strings.Join(allowed, ", "))
		}
		if len(vs) == 0 || len(vs) > sources.MaxFilterValues {
			return fmt.Errorf("board %s/%s: filter %q needs 1..%d values", b.ATS, b.Token, k, sources.MaxFilterValues)
		}
		for _, v := range vs {
			if strings.TrimSpace(v) == "" || len(v) > sources.MaxSearchLen || !cleanText(v) {
				return fmt.Errorf("board %s/%s: filter %q value %q must be 1..%d bytes with no control characters", b.ATS, b.Token, k, v, sources.MaxSearchLen)
			}
		}
	}
	return nil
}

// ScrapeSuffix ends the token of every batch of jobs scraped in the browser
// that is not a saved page board's own page (`vani jobs ingest`). ValidBoard
// refuses it as a board token's end, so no board's absence pass can reach
// those records.
const ScrapeSuffix = "-scrape"

// httpsBoardURL requires the https URL a page or feed board is fetched from.
func httpsBoardURL(kind string, b sources.Board) error {
	u, err := url.Parse(b.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("%s board %s needs an https url, got %q", kind, b.Token, b.URL)
	}
	return nil
}

// ValidBoard checks a single watchlist entry.
func ValidBoard(b sources.Board) error {
	switch b.ATS {
	case posting.Greenhouse, posting.Lever, posting.Ashby, posting.Workday,
		posting.Amazon, posting.Google, posting.Apple, posting.Eightfold, posting.Jibe,
		posting.Gem, posting.SmartRecruiters, posting.IBM, posting.Avature, posting.DEShaw, posting.Atlassian, posting.OracleHCM,
		posting.Rippling, posting.Workable, posting.Recruitee, posting.Pinpoint, posting.BambooHR, posting.Personio,
		posting.TikTok, posting.Feed, posting.Page:
	default:
		return fmt.Errorf("board ats %q is not one of gh, lv, ab, wd, az, gg, ap, ef, jb, gm, sr, ib, av, ds, at, oc, rp, wk, rc, pp, bh, ps, tt, fd, pg", b.ATS)
	}
	if strings.TrimSpace(b.Token) == "" {
		return fmt.Errorf("board (%s) has an empty token", b.ATS)
	}
	if why := sources.TokenProblem(b.Token); why != "" {
		return fmt.Errorf("board (%s) token %q %s", b.ATS, b.Token, why)
	}
	if strings.ContainsAny(b.Token, " \t\r\n?#") || strings.Contains(b.Token, "..") {
		return fmt.Errorf("board (%s) token %q has unsafe characters", b.ATS, b.Token)
	}
	if b.BoardName != "" {
		// The token stays the safe slug every key and ref is built from; the
		// literal name ("Hippocratic AI") only ever reaches the request URL.
		if b.ATS != posting.Ashby {
			return fmt.Errorf("board %s/%s: board_name applies to ab only", b.ATS, b.Token)
		}
		if why := sources.BoardNameProblem(b.BoardName); why != "" {
			return fmt.Errorf("board (%s) %s board_name %q %s", b.ATS, b.Token, b.BoardName, why)
		}
	}
	if t := strings.ToLower(b.Token); b.ATS == posting.IBM && t != ibm.TokenIBM && t != ibm.TokenConfluent {
		// The token picks which of IBM's two disjoint listings the board reads.
		return fmt.Errorf("board (ib) token %q must be %s or %s", b.Token, ibm.TokenIBM, ibm.TokenConfluent)
	}
	if strings.HasSuffix(strings.ToLower(b.Token), ScrapeSuffix) {
		return fmt.Errorf("board token %q ends in %q, which is reserved for jobs scraped in the browser", b.Token, ScrapeSuffix)
	}
	if b.ATS == posting.Workday {
		if strings.Count(b.Token, "/") != 1 {
			return fmt.Errorf("workday token %q must be tenant/site", b.Token)
		}
		tenant, _, _ := strings.Cut(strings.ToLower(b.Token), "/")
		if sources.WorkdaySiteHost(b.Host) {
			// The shared host names no tenant, so the token is all that keeps a
			// board on it from reaching another tenant's paths.
			if !workdayName.MatchString(b.Token) {
				return fmt.Errorf("workday token %q must be tenant/site, each of letters, digits, '-' and '_'", b.Token)
			}
		} else {
			hostTenant, ok := sources.WorkdayTenantHost(b.Host)
			if !ok {
				return fmt.Errorf("workday board %s needs host {tenant}.wd{N}.myworkdayjobs.com or wd{N}.myworkdaysite.com, got %q", b.Token, b.Host)
			}
			if hostTenant != tenant {
				return fmt.Errorf("workday host %q does not belong to tenant %q", b.Host, tenant)
			}
		}
	} else if b.ATS == posting.OracleHCM {
		if err := validOracleHCM(b); err != nil {
			return err
		}
	} else if strings.Contains(b.Token, "/") {
		return fmt.Errorf("board (%s) token %q must not contain '/'", b.ATS, b.Token)
	}
	if b.ATS == posting.Eightfold {
		// The token is the tenant's domain, the host its careers site.
		if !hostname.MatchString(strings.ToLower(b.Token)) {
			return fmt.Errorf("eightfold token %q must be the company's domain, e.g. microsoft.com", b.Token)
		}
		if !hostname.MatchString(strings.ToLower(strings.TrimSpace(b.Host))) {
			return fmt.Errorf("eightfold board %s needs host, its careers site, e.g. apply.careers.microsoft.com; got %q", b.Token, b.Host)
		}
	} else if b.ATS == posting.Jibe {
		// The token is the site's client code, the host its careers site.
		if !jibeClient.MatchString(strings.ToLower(b.Token)) {
			return fmt.Errorf("jibe token %q must be the site's client code, e.g. amd", b.Token)
		}
		if !hostname.MatchString(strings.ToLower(strings.TrimSpace(b.Host))) {
			return fmt.Errorf("jibe board %s needs host, its careers site, e.g. careers.amd.com; got %q", b.Token, b.Host)
		}
	} else if b.ATS == posting.SmartRecruiters {
		// The token is the company identifier, sent in the request path. The
		// API answers zero postings for any country but one lower-case
		// two-letter code, so a malformed one is refused here, not listed empty.
		if !smartRecruitersCompany.MatchString(b.Token) {
			return fmt.Errorf("smartrecruiters token %q must be the company identifier (letters, digits, '-' and '_'), e.g. servicenow", b.Token)
		}
		if b.Host != "" {
			return fmt.Errorf("board %s/%s: host applies to wd, oc, ef, jb and av only", b.ATS, b.Token)
		}
		for _, cc := range b.Filters["country"] {
			if !srCountry.MatchString(cc) {
				return fmt.Errorf("smartrecruiters board %s: country %q must be one two-letter ISO code such as us", b.Token, cc)
			}
		}
	} else if b.ATS == posting.Avature {
		// The token is the owner's slug, the host the portal's, and the url its
		// listing page on that host: the portal path differs per customer.
		if !avatureToken.MatchString(b.Token) {
			return fmt.Errorf("avature token %q must be a lower-case slug of letters, digits and '-', e.g. twosigma", b.Token)
		}
		if !hostname.MatchString(strings.ToLower(strings.TrimSpace(b.Host))) {
			return fmt.Errorf("avature board %s needs host, its careers portal, e.g. careers.twosigma.com; got %q", b.Token, b.Host)
		}
		if _, err := avature.ParseListURL(b.URL, b.Host); err != nil {
			return fmt.Errorf("avature board %s: %w", b.Token, err)
		}
	} else if b.ATS != posting.Workday && b.ATS != posting.OracleHCM && b.Host != "" {
		return fmt.Errorf("board %s/%s: host applies to wd, oc, ef, jb and av only", b.ATS, b.Token)
	}
	if _, perTenant := sources.TenantHostSuffix[b.ATS]; perTenant {
		// The token is the host's first label, which is what BoardHost allows.
		if why := sources.TenantLabelProblem(b.Token); why != "" {
			return fmt.Errorf("board (%s) token %q %s", b.ATS, b.Token, why)
		}
	}
	if b.ATS == posting.TikTok && !strings.EqualFold(b.Token, "tiktok") {
		// The one site the adapter reads (tiktok.sites); its API host is fixed.
		return fmt.Errorf("board (%s) token %q must be tiktok", b.ATS, b.Token)
	}
	if err := validSearches(b); err != nil {
		return err
	}
	if b.MaxPostings != 0 {
		if b.ATS != posting.Workday && b.ATS != posting.OracleHCM {
			return fmt.Errorf("board %s/%s: max_postings applies to wd and oc only", b.ATS, b.Token)
		}
		if b.MaxPostings < 1 || b.MaxPostings > sources.MaxPostingsCeiling {
			return fmt.Errorf("board %s/%s: max_postings %d must be within 1..%d (0 means the default)", b.ATS, b.Token, b.MaxPostings, sources.MaxPostingsCeiling)
		}
	}
	if b.ATS == posting.Page {
		if err := httpsBoardURL("page", b); err != nil {
			return err
		}
		if b.LinkPattern == "" || len(b.LinkPattern) > maxLinkPattern {
			return fmt.Errorf("page board %s needs a link_pattern of 1..%d bytes", b.Token, maxLinkPattern)
		}
		if _, err := regexp.Compile(b.LinkPattern); err != nil {
			return fmt.Errorf("page board %s link_pattern: %v", b.Token, err)
		}
	} else if b.ATS == posting.Feed {
		if err := httpsBoardURL("feed", b); err != nil {
			return err
		}
		if b.LinkPattern != "" {
			return fmt.Errorf("board %s/%s: link_pattern applies to page boards only", b.ATS, b.Token)
		}
	} else if (b.URL != "" && b.ATS != posting.Avature) || b.LinkPattern != "" {
		return fmt.Errorf("board %s/%s: url applies to pg, fd and av boards, link_pattern to page boards only", b.ATS, b.Token)
	}
	switch b.Region {
	case "":
	case "eu":
		if b.ATS != posting.Lever {
			return fmt.Errorf("region eu applies to lever only, got %s/%s", b.ATS, b.Token)
		}
	default:
		return fmt.Errorf("board %s/%s region %q is not supported", b.ATS, b.Token, b.Region)
	}
	// The domain is a host name, never a URL: it becomes an allowed host and
	// the name of the cached icon file, so it is checked exactly as written.
	if b.Domain != "" && !hostname.MatchString(b.Domain) {
		return fmt.Errorf("board %s/%s domain %q must be a lower-case host name such as www.example.com, with no scheme, path or port", b.ATS, b.Token, b.Domain)
	}
	if b.Icon != "" {
		if b.Domain == "" {
			return fmt.Errorf("board %s/%s has an icon but no domain; the icon is kept under the domain", b.ATS, b.Token)
		}
		if _, ok := IconHost(b.Icon); !ok {
			return fmt.Errorf("board %s/%s icon %q must be an https URL of at most %d bytes on a lower-case host name, with no port, user or fragment", b.ATS, b.Token, b.Icon, MaxIconURL)
		}
	}
	return nil
}

// MaxIconURL bounds a board's icon URL.
const MaxIconURL = 2048

// IconHost is the host of a board's icon URL, and whether the URL is one Vani
// fetches: https, a lower-case host name with no port, no user and no
// fragment, at most MaxIconURL bytes. The host becomes an allowed host, so it
// is checked exactly as written.
func IconHost(icon string) (string, bool) {
	if icon == "" || len(icon) > MaxIconURL || strings.ContainsAny(icon, " \t\r\n#\\") {
		return "", false
	}
	u, err := url.Parse(icon)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" ||
		u.Port() != "" || !hostname.MatchString(u.Host) {
		return "", false
	}
	return u.Host, true
}

func badUSD(v float64) bool { return math.IsNaN(v) || v < 0 || v > MaxDailyUSD }

// trackID is a track decider id. It is also the file stem (<id>.json), so it
// admits no path separator, dot or upper case.
var trackID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

const trackIDRule = "lower-case letters, digits and '-', 1-64 characters, not starting with '-'"

// ValidTrackID reports whether id is a well-formed track id.
func ValidTrackID(id string) bool { return trackID.MatchString(id) }

// Validate checks every rule and returns all problems joined, so one run of
// "config check" shows everything to fix.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Version != Version {
		add("version is %d, want %d", c.Version, Version)
	}

	seen := map[string]bool{}
	// icons is the icon each domain names; boards sharing a domain share its
	// one cached icon, so they may not name two.
	icons := map[string]string{}
	for i, b := range c.Watchlist {
		if err := ValidBoard(b); err != nil {
			add("watchlist[%d]: %v", i, err)
			continue
		}
		if b.Icon != "" {
			if prev, ok := icons[b.Domain]; ok && prev != b.Icon {
				add("watchlist[%d]: domain %s already has icon %s; boards that share a domain share its icon", i, b.Domain, prev)
			}
			icons[b.Domain] = b.Icon
		}
		id := string(b.ATS) + "/" + strings.ToLower(b.Token)
		if seen[id] {
			add("watchlist[%d]: duplicate board %s", i, id)
		}
		seen[id] = true
		if SingleBoardATS(b.ATS) {
			if seen[string(b.ATS)] {
				add("watchlist[%d]: a second %s board; list every search on one board", i, b.ATS)
			}
			seen[string(b.ATS)] = true
		}
	}

	for i, h := range c.AllowedHosts {
		if err := egress.ValidAllowedHost(h); err != nil {
			add("allowed_hosts[%d]: %v", i, err)
		}
	}
	var missing []string
	for _, h := range c.RequiredHosts() {
		if !egress.AllowsHost(c.AllowedHosts, h) {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		add("allowed_hosts is missing: %s", strings.Join(missing, ", "))
	}

	if _, _, _, err := c.Schedule.Parsed(); err != nil {
		add("schedule: %v", err)
	}

	if badUSD(c.Decide.DailyUSD.OpenRouter) {
		add("decide.daily_usd.openrouter %v must be within 0..%v", c.Decide.DailyUSD.OpenRouter, MaxDailyUSD)
	}
	if badUSD(c.Decide.DailyUSD.LLM) {
		add("decide.daily_usd.llm %v must be within 0..%v", c.Decide.DailyUSD.LLM, MaxDailyUSD)
	}
	if c.OpenRouterConfigured() {
		if c.Decide.OpenRouterKeyEnv == "" {
			add("decide.openrouter_key_env is required when decide.openrouter_model is set")
		}
		if err := checkBackendName(c.Decide.OpenRouterName()); err != nil {
			add("decide.openrouter_model %q: %v", c.Decide.OpenRouterModel, err)
		}
	}
	for name, p := range map[string]string{"kb.bank": c.KB.Bank, "kb.rkb": c.KB.RKB, "kb.code_root": c.KB.CodeRoot, "kb.vm_report": c.KB.VMReport,
		"lightpanda_binary": c.LightpandaBinary, "devprism.binary": c.DevPrism.Binary, "tracks_dir": c.TracksDir,
		"semantic.snapshot": c.Semantic.Snapshot, "rank_weights_file": c.RankWeightsFile} {
		if p != "" && !filepath.IsAbs(p) {
			add("%s %q must be an absolute path", name, p)
		}
	}
	if maxMS := int(page.MaxInterval / time.Millisecond); c.PageIntervalMS < 0 || c.PageIntervalMS > maxMS {
		add("page_interval_ms %d must be within 0..%d", c.PageIntervalMS, maxMS)
	}
	if c.Semantic.WarmPerRun < 0 || c.Semantic.WarmPerRun > MaxSemanticWarmPerRun {
		add("semantic.warm_per_run %d must be within 0..%d (0 means %d)", c.Semantic.WarmPerRun, MaxSemanticWarmPerRun, DefaultSemanticWarmPerRun)
	}
	if c.Semantic.Snapshot != "" && c.KB.Bank == "" {
		add("semantic.snapshot needs kb.bank: the profile is built from the knowledge base")
	}
	if n := len(c.KB.Selections); n > MaxKBSelections {
		add("kb.selections lists %d files, at most %d", n, MaxKBSelections)
	}
	seenSel := map[string]bool{}
	for _, p := range c.KB.Selections {
		switch {
		case !filepath.IsAbs(p):
			add("kb.selections entry %q must be an absolute path", p)
		case seenSel[filepath.Clean(p)]:
			add("kb.selections lists %q twice", p)
		}
		seenSel[filepath.Clean(p)] = true
	}
	if p := c.KB.BaseSelection; p != "" && !seenSel[filepath.Clean(p)] {
		add("kb.base_selection %q must be one of kb.selections", p)
	}
	if len(c.KB.Selections) > 0 && c.KB.Bank == "" {
		add("kb.selections needs kb.bank")
	}
	if n := c.DigestTopOpen(); n < 0 || n > MaxDigestTopOpen {
		add("digest.top_open %d must be within 0..%d (absent means %d)", n, MaxDigestTopOpen, DefaultDigestTopOpen)
	}
	if n := c.DigestExploreBelowFold(); n < 0 || n > MaxDigestExplore {
		add("digest.explore_below_fold %d must be within 0..%d (absent means %d)", n, MaxDigestExplore, DefaultDigestExploreBelowFold)
	}
	if n := c.DigestExploreDropped(); n < 0 || n > MaxDigestExplore {
		add("digest.explore_dropped %d must be within 0..%d (absent means %d)", n, MaxDigestExplore, DefaultDigestExploreDropped)
	}
	if c.AdvancePerRun < 0 || c.AdvancePerRun > MaxAdvancePerRun {
		add("advance_per_run %d must be within 0..%d (0 means %d)", c.AdvancePerRun, MaxAdvancePerRun, DefaultAdvancePerRun)
	}
	if c.Decide.ShadowEvery < 0 || c.Decide.ShadowEvery > MaxShadowEvery {
		add("decide.shadow_every %d must be within 0..%d (0 means %d)", c.Decide.ShadowEvery, MaxShadowEvery, DefaultShadowEvery)
	}
	if c.AdvanceConcurrency < 0 || c.AdvanceConcurrency > MaxAdvanceConcurrency {
		add("advance_concurrency %d must be within 0..%d (0 means %d)", c.AdvanceConcurrency, MaxAdvanceConcurrency, DefaultAdvanceConcurrency)
	}
	switch c.Decide.ShadowMode {
	case "", ShadowModeSample:
	case ShadowModeDropCandidates:
		if c.Decide.Shadow == "" {
			add("decide.shadow_mode %s needs decide.shadow", ShadowModeDropCandidates)
		}
		if c.Decide.ShadowEvery != 0 {
			add("decide.shadow_every does not apply with decide.shadow_mode %s (every drop candidate is asked)", ShadowModeDropCandidates)
		}
		if c.Decide.EscalateEnabled() {
			// One slot has one shadow reading: escalation covers p below_p, the
			// shadow the drop candidates at or above a rule's p, and the two stay
			// apart only while below_p does not reach a rule's p.
			for _, r := range c.DropRules {
				if r.MinOpenRouterProb > 0 && c.Decide.EscalateBelowP() > r.MinOpenRouterProb {
					add("decide.escalate.below_p %v exceeds the min_openrouter_prob %v of the %q drop rule: escalation and the drop-candidate shadow would claim the same slots", c.Decide.EscalateBelowP(), r.MinOpenRouterProb, r.Slot)
				}
			}
		}
	default:
		add("decide.shadow_mode %q is not %q or %q", c.Decide.ShadowMode, ShadowModeSample, ShadowModeDropCandidates)
	}
	switch c.Decide.Shadow {
	case "":
	case "llm":
		if c.LLM.Provider == "" {
			add("decide.shadow llm needs llm.provider")
		} else if c.Decide.ShadowOnDropCandidates() && (c.LLM.TriageModel == "" || c.LLM.TriageModel == c.LLM.Model) {
			// Every drop candidate is asked, so without a cheaper model of its own
			// the shadow would spend the llm daily cap on llm.model's expensive tier.
			add("decide.shadow llm with shadow_mode %s needs llm.triage_model set to a model other than llm.model (%q): it would read every drop candidate on the expensive tier", ShadowModeDropCandidates, c.LLM.Model)
		}
	case ShadowOpenRouter:
		if !c.OpenRouterConfigured() {
			add("decide.shadow openrouter needs decide.openrouter_model")
		}
	case "lappi":
		if c.Decide.Lappi == nil {
			add("decide.shadow lappi needs decide.lappi")
		}
	case "local":
		if c.Decide.Local == nil {
			add("decide.shadow local needs decide.local")
		}
	default:
		add("decide.shadow %q is not llm, openrouter, lappi or local", c.Decide.Shadow)
	}
	if l := c.Decide.Local; l != nil && l.Server != nil {
		s := l.Server
		if l.Endpoint != "" {
			add("decide.local: set endpoint or server, not both (a started server's endpoint is chosen per run)")
		}
		for name, p := range map[string]string{"decide.local.server.binary": s.Binary, "decide.local.server.model_path": s.ModelPath} {
			if !filepath.IsAbs(p) {
				add("%s %q must be an absolute path", name, p)
			}
		}
		if s.ContextSize != 0 && (s.ContextSize < 512 || s.ContextSize > 65536) {
			add("decide.local.server.context_size %d must be within 512..65536", s.ContextSize)
		}
		if s.StartTimeoutMS < 0 || s.StartTimeoutMS > 300_000 {
			add("decide.local.server.start_timeout_ms %d must be within 0..300000", s.StartTimeoutMS)
		}
		ctxSize := s.ContextSize
		if ctxSize == 0 {
			ctxSize = 8192 // localserver.DefaultContextSize
		}
		if s.BatchSize != 0 && (s.BatchSize < 32 || s.BatchSize > ctxSize) {
			add("decide.local.server.batch_size %d must be 0 or within 32..%d (the context size)", s.BatchSize, ctxSize)
		}
		if s.Parallel < 0 || s.Parallel > 16 {
			add("decide.local.server.parallel %d must be within 0..16", s.Parallel)
		}
	}
	if l := c.Decide.Local; l != nil {
		u, err := url.Parse(l.Endpoint)
		switch {
		case l.Server != nil:
			// The endpoint is the started server's; checked above.
		case err != nil || l.Endpoint == "" || u.Host == "":
			add("decide.local.endpoint %q must be an absolute URL", l.Endpoint)
		case u.Scheme != "http" && u.Scheme != "https":
			add("decide.local.endpoint %q must be http or https", l.Endpoint)
		case u.User != nil:
			add("decide.local.endpoint must not carry credentials")
		case !egress.IsLoopbackHost(u.Hostname()):
			// Postings stay on this Mac: a remote model belongs in openrouter_model.
			add("decide.local.endpoint %q must be on a loopback host (127.0.0.1, ::1, localhost)", l.Endpoint)
		}
		if strings.TrimSpace(l.Model) == "" {
			add("decide.local.model is required (it names the model the eval measures)")
		}
		if l.TimeoutMS < 0 || l.TimeoutMS > MaxLocalTimeoutMS {
			add("decide.local.timeout_ms %d must be within 0..%d", l.TimeoutMS, MaxLocalTimeoutMS)
		}
		if l.Questions != "" && l.Questions != "combined" && l.Questions != "per_slot" {
			add("decide.local.questions %q is not combined or per_slot", l.Questions)
		}
		if strings.TrimSpace(l.Model) != "" || l.Server != nil {
			if err := checkLocalShadowName(*l); err != nil {
				add("decide.local: %v", err)
			}
		}
		if c.Decide.Shadow != "local" {
			add("decide.local is configured but unused: set decide.shadow to local")
		}
	}
	if l := c.Decide.Lappi; l != nil {
		if l.Socket != "" && !filepath.IsAbs(l.Socket) {
			add("decide.lappi.socket %q must be an absolute path", l.Socket)
		}
		if l.TimeoutMS < 0 || l.TimeoutMS > MaxLappiTimeoutMS {
			add("decide.lappi.timeout_ms %d must be within 0..%d", l.TimeoutMS, MaxLappiTimeoutMS)
		}
		slotsOK := true
		if len(l.PrimarySlots) > 0 {
			if l.Primary {
				slotsOK = false
				add("decide.lappi.primary (all slots) and decide.lappi.primary_slots are exclusive: set one")
			}
			known := map[string]bool{}
			names := make([]string, 0, len(jdtriage.Families))
			for _, f := range jdtriage.Families {
				known[f.Slot.Name] = true
				names = append(names, f.Slot.Name)
			}
			seen := map[string]bool{}
			problems := 0
			problem := func(format string, a ...any) {
				slotsOK = false
				if problems++; problems <= maxPrimarySlotProblems {
					add(format, a...)
				}
			}
			if len(l.PrimarySlots) > maxPrimarySlotsListed {
				// The slot set is tiny; a list this long is a broken or hostile
				// file, refused without reading its entries.
				slotsOK = false
				add("decide.lappi.primary_slots lists %d entries; at most %d are accepted (there are only %d slots)", len(l.PrimarySlots), maxPrimarySlotsListed, len(names))
			} else {
				for _, s := range l.PrimarySlots {
					switch {
					case !known[s]:
						problem("decide.lappi.primary_slots: %.64q is not a triage slot (%s)", s, strings.Join(names, ", "))
					case seen[s]:
						problem("decide.lappi.primary_slots: %q is listed twice", s)
					}
					seen[s] = true
				}
				if problems > maxPrimarySlotProblems {
					add("decide.lappi.primary_slots: and %d more problem(s)", problems-maxPrimarySlotProblems)
				}
			}
		}
		switch {
		case l.Primary && c.Decide.Shadow == "lappi":
			add("decide.lappi.primary and decide.shadow lappi are exclusive")
		case slotsOK && len(l.PrimarySlots) >= len(jdtriage.Families) && c.Decide.Shadow == "lappi":
			// Every slot promoted leaves the shadow nothing to read.
			add("decide.lappi.primary_slots names every slot, so decide.shadow lappi has nothing to read: use decide.lappi.primary or drop the shadow")
		case !l.Primary && len(l.PrimarySlots) == 0 && c.Decide.Shadow != "lappi":
			add("decide.lappi is configured but unused: set decide.shadow to lappi, or decide.lappi.primary after `vani eval` says promotable")
		}
	}
	// A configured backend with a zero cap refuses every call, which would
	// stall postings silently instead of failing where it can be seen.
	if c.OpenRouterConfigured() && c.Decide.DailyUSD.OpenRouter == 0 {
		add("decide.daily_usd.openrouter must be above 0 when decide.openrouter_model is set")
	}
	if c.LLM.Provider != "" && c.Decide.DailyUSD.LLM == 0 {
		add("decide.daily_usd.llm must be above 0 when llm.provider is set")
	}

	switch c.LLM.Provider {
	case "", "gemini", "anthropic":
	default:
		add("llm.provider %q is not gemini or anthropic", c.LLM.Provider)
	}
	if c.LLM.Provider != "" && c.LLM.Model == "" {
		add("llm.model is required when llm.provider is set")
	}
	if c.LLM.Provider != "" {
		if _, _, err := c.LLM.Prices(); err != nil {
			add("%v", err)
		}
	}
	if m := c.LLM.TriageModel; m != "" {
		switch {
		case c.LLM.Provider == "":
			add("llm.triage_model needs llm.provider")
		case len(m) > MaxModelName || strings.ContainsFunc(m, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
			add("llm.triage_model %q must be a model id of at most %d bytes with no whitespace", m, MaxModelName)
		}
	}
	switch c.KBMatch.EffectiveBackend() {
	case KBMatchLLM:
		if m := c.KBMatch.Model; m != "" {
			switch {
			case c.LLM.Provider == "":
				add("kbmatch.model needs llm.provider")
			case len(m) > MaxModelName || strings.ContainsFunc(m, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
				add("kbmatch.model %q must be a model id of at most %d bytes with no whitespace", m, MaxModelName)
			}
		}
	case KBMatchOpenRouter:
		if !c.OpenRouterConfigured() {
			add("kbmatch.backend %q needs decide.openrouter_model", KBMatchOpenRouter)
		}
		if c.KBMatch.Model != "" {
			add("kbmatch.model applies to the %q backend only", KBMatchLLM)
		}
	default:
		add("kbmatch.backend %q must be %q or %q", c.KBMatch.Backend, KBMatchLLM, KBMatchOpenRouter)
	}
	if e := c.Decide.Escalate; e != nil {
		if e.BelowP != nil && (math.IsNaN(*e.BelowP) || *e.BelowP <= 0 || *e.BelowP > 1) {
			add("decide.escalate.below_p %v must be within (0, 1] (absent means %v)", *e.BelowP, DefaultEscalateBelowP)
		}
		if e.MaxPerRun != nil && (*e.MaxPerRun < 1 || *e.MaxPerRun > MaxEscalatePerRun) {
			add("decide.escalate.max_per_run %d must be within 1..%d (absent means %d)", *e.MaxPerRun, MaxEscalatePerRun, DefaultEscalateMaxPerRun)
		}
		if e.Enabled {
			if c.LLM.Provider == "" {
				add("decide.escalate needs llm.provider")
			}
			// Escalation re-asks every low-confidence slot (up to max_per_run a
			// run) on llm.triage_model. Without a cheaper model of its own it would
			// do that on llm.model, spending the llm daily cap on the expensive tier.
			if c.LLM.Provider != "" && (c.LLM.TriageModel == "" || c.LLM.TriageModel == c.LLM.Model) {
				add("decide.escalate needs llm.triage_model set to a model other than llm.model (%q): it would escalate on the expensive tier", c.LLM.Model)
			}
			if c.Decide.Shadow != "" && !c.Decide.ShadowOnDropCandidates() {
				add("decide.escalate and decide.shadow are exclusive (escalated readings are recorded as the shadow's)")
			}
		}
	}

	// A rule must name a triage slot and that slot's values: an unknown slot
	// is never in an answer, so the rule would read every posting as missing
	// it and keep all of them unsure; an unknown value never matches.
	slotValues := jdtriage.SlotValues()
	for i, r := range c.DropRules {
		legal, known := slotValues[r.Slot]
		switch {
		case r.Slot == "":
			add("drop_rules[%d]: slot is empty", i)
		case !known:
			add("drop_rules[%d]: slot %q is not a triage slot (want one of %s)", i, r.Slot, strings.Join(slices.Sorted(maps.Keys(slotValues)), ", "))
		}
		if len(r.Values) == 0 {
			add("drop_rules[%d]: values is empty", i)
		}
		for _, v := range r.Values {
			if known && !slices.Contains(legal, v) {
				add("drop_rules[%d]: %q is not a %s value (want one of %s)", i, v, r.Slot, strings.Join(legal, ", "))
			}
		}
		if math.IsNaN(r.MinOpenRouterProb) || r.MinOpenRouterProb < 0 || r.MinOpenRouterProb > 1 {
			add("drop_rules[%d]: min_openrouter_prob %v must be within 0..1", i, r.MinOpenRouterProb)
		}
		if math.IsNaN(r.MinLappiScore) || r.MinLappiScore < 0 {
			add("drop_rules[%d]: min_lappi_score %v must be >= 0", i, r.MinLappiScore)
		}
	}
	if len(c.RankPenalties) > MaxRankPenalties {
		add("rank_penalties has %d entries, max %d", len(c.RankPenalties), MaxRankPenalties)
	}
	for i, r := range c.RankPenalties {
		legal, known := slotValues[r.Slot]
		switch {
		case r.Slot == "":
			add("rank_penalties[%d]: slot is empty", i)
		case !known:
			add("rank_penalties[%d]: slot %q is not a triage slot (want one of %s)", i, r.Slot, strings.Join(slices.Sorted(maps.Keys(slotValues)), ", "))
		}
		if len(r.Values) == 0 {
			add("rank_penalties[%d]: values is empty", i)
		}
		for _, v := range r.Values {
			if known && !slices.Contains(legal, v) {
				add("rank_penalties[%d]: %q is not a %s value (want one of %s)", i, v, r.Slot, strings.Join(legal, ", "))
			}
		}
	}

	if len(c.AgeNeutralCompanies) > MaxAgeNeutralCompanies {
		add("age_neutral_companies has %d entries, max %d", len(c.AgeNeutralCompanies), MaxAgeNeutralCompanies)
	}
	for i, n := range c.AgeNeutralCompanies {
		switch {
		case strings.TrimSpace(n) == "":
			add("age_neutral_companies[%d]: name is empty", i)
		case len(n) > MaxAgeNeutralNameBytes:
			add("age_neutral_companies[%d]: name is %d bytes, max %d", i, len(n), MaxAgeNeutralNameBytes)
		case strings.ContainsFunc(n, unicode.IsControl):
			add("age_neutral_companies[%d]: name contains a control character", i)
		}
	}

	if n := len(strings.Join(strings.Fields(c.MotivationNotes), " ")); n > draft.MaxMotivationNotesBytes {
		add("motivation_notes is %d bytes after whitespace collapse, max %d", n, draft.MaxMotivationNotesBytes)
	}
	if strings.ContainsFunc(c.MotivationNotes, func(r rune) bool { return unicode.IsControl(r) && !unicode.IsSpace(r) }) {
		add("motivation_notes contains a control character")
	}

	if c.Track != "" && !ValidTrackID(c.Track) {
		add("track %q is not a track id (%s)", c.Track, trackIDRule)
	}
	seenPref := map[string]bool{}
	for i, p := range c.EmployerPreferences {
		switch {
		case !slices.Contains(employer.Attributes, p):
			add("employer_preferences[%d] %q is not one of %s", i, p, strings.Join(employer.Attributes, ", "))
		case seenPref[p]:
			add("employer_preferences[%d]: duplicate %q", i, p)
		}
		seenPref[p] = true
	}
	c.TitleSignals.validate(add)
	c.Exclude.validate(add)
	c.Funnel.validate(add)

	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("config invalid: %w", errors.Join(errs...))
}
