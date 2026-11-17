// Package jev is the decide.Backend for the OpenRouter Decisions API (alpha)
// running typesafe/jev, or any Jev-compatible server. The wire shape is the
// one n8n-email-engine pins in config/jev_requests.json.
package jev

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/decide"
	"github.com/bharathvbcr/decide/internal/jsonwire"
)

// Doer sends one HTTP request. A caller passes its egress client, which owns
// the host allowlist, redirects and body caps; this package adds none of its
// own beyond the response cap below.
type Doer interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}

const (
	// DefaultEndpoint is the OpenRouter Decisions API. A key read from the
	// environment is only ever sent to its host.
	DefaultEndpoint = "https://openrouter.ai/api/alpha/decisions"
	// DefaultModel is the model the Decisions API was validated against.
	DefaultModel = "typesafe/jev-1.13"
	// DefaultKeyEnv names the environment variable holding the API key.
	DefaultKeyEnv = "OPENROUTER_API_KEY"

	DefaultTimeout = 20 * time.Second
	MaxTimeout     = 60 * time.Second

	DefaultMaxAttempts = 3
	MaxAttempts        = 8

	DefaultMaxRetryAfter = 30 * time.Second // never above the call's Timeout

	DefaultBackoff = 500 * time.Millisecond
	MinBackoff     = time.Millisecond
	MaxBackoff     = 10 * time.Second

	MaxModelBytes = 256

	openRouterHost  = "openrouter.ai"
	maxResponseSize = 1 << 20
	defaultSpendKey = "jev"
	defaultName     = "jev"
)

// ErrAuth is decide.ErrAuth: the credentials were rejected (401/402/403).
var ErrAuth = decide.ErrAuth

// ErrNoKey is wrapped by New and Validate when the endpoint is OpenRouter and
// no key was found. A caller may treat exactly this as "Jev is off"; every
// other configuration error is a real fault.
var ErrNoKey = errors.New("jev: no API key for the OpenRouter endpoint")

// Config configures New. The zero value is usable once the key env var is set.
type Config struct {
	// Endpoint is the Decisions URL; default DefaultEndpoint. It must be
	// https, or plain http on a loopback host, so request content never
	// crosses a network unencrypted.
	Endpoint string
	Model    string // default DefaultModel
	// Key is sent to Endpoint whatever host it names: the caller chose both.
	// Empty means KeyEnv.
	Key string
	// KeyEnv names the env var read when Key is empty; default DefaultKeyEnv.
	// Its value is only ever sent to openrouter.ai: moving Endpoint elsewhere
	// does not move the key with it. OpenRouter requires a key (ErrNoKey);
	// any other endpoint without Key gets no Authorization header.
	KeyEnv string
	Getenv func(string) string // reads KeyEnv; default os.Getenv
	// Timeout is the whole-call deadline including retries; default 20s, max 60s.
	Timeout time.Duration
	// MaxAttempts bounds tries on transport errors, 429 and 5xx; default 3,
	// max MaxAttempts.
	MaxAttempts int
	// MaxRetryAfter caps how long a server's Retry-After is honoured; default
	// 30s (or Timeout when shorter), never above Timeout.
	MaxRetryAfter time.Duration
	// Backoff is the first retry's base wait, doubled per attempt with up to
	// 50% jitter; default 500ms, within [MinBackoff, MaxBackoff].
	Backoff time.Duration
	// State builds the request's "state" object from a question; it must
	// return a JSON-encodable value. Default {"text": string(q.Context)}.
	State func(q decide.Question) any
	// Descriptions maps slot -> option -> description. Jev judges by
	// description; an option without one is described by its own label.
	Descriptions map[string]map[string]string
	// Instructions maps slot -> the question text sent for that slot; a slot
	// without one gets the Question's Prompt. Decision models that answer
	// named questions one by one (llama.cpp's /v1/systemone) may do better
	// with a question per slot than with one combined prompt.
	Instructions map[string]string
	Spend        decide.Spend // optional daily cap / meter
	SpendKey     string       // default "jev"
	// InputUSDPerMTok prices a call from its usage.input_tokens when the
	// response reports no usage.cost. SubQ's /v1/systemone bills input tokens
	// only and reports no cost, so without a price its calls would never reach
	// Spend. A reported cost always wins. Zero charges nothing for such a call,
	// as before; at most MaxInputUSDPerMTok.
	InputUSDPerMTok float64
	// Wire is the request dialect; default WireSystemOne. WireGemini sends
	// State's value as the "input" string (a non-string value is sent as its
	// JSON text); its default State is the question's Context as a string.
	Wire Wire
	// Name is the backend's name in routing, answers and ledgers; default
	// "jev". A second instance speaking the same wire shape (a local
	// llama.cpp /v1/systemone serving another decision model) needs its own
	// name, or its readings would be filed as Jev's. 1..MaxNameBytes of
	// [a-z0-9_-].
	Name string
}

// Wire names a Decisions request dialect. Every dialect asks the same typed
// choice questions and reads the same answer (choice, probabilities,
// confidence); they differ in the field that carries the shared context, the
// auth header, the usage counter and how a refusal is reported.
type Wire string

const (
	// WireSystemOne is OpenRouter's Decisions API and every /v1/systemone
	// server (SubQ, llama.cpp): {"model","questions","state"} with
	// Authorization: Bearer. The default.
	WireSystemOne Wire = ""
	// WireGemini is Google's /v1beta/decisions (gemini-decision-1):
	// {"input","model","questions"} with the context as a string, the key in
	// x-goog-api-key, usage.total_input_tokens, question keys of
	// [A-Za-z0-9_-]{1,64}, and an input safety block as HTTP 400 with
	// error.code, read here as Refused rather than an outage.
	WireGemini Wire = "gemini"
)

// Gemini's documented key limits (aistudio.google.com/docs/decisions-api#limits).
const (
	geminiMaxQuestionKey = 64
	geminiMaxOptionKey   = 128
	geminiMaxOptionBytes = 512
)

// geminiRefusalCodes are the error.code values Google documents for a request
// its input safety filters blocked.
var geminiRefusalCodes = []string{"safety", "prohibited_content", "blocklist", "image_safety", "content_blocked"}

// MaxInputUSDPerMTok bounds Config.InputUSDPerMTok ($1,000 per million input
// tokens): far above any decision model's price, low enough that a typo in a
// config cannot meter one call as a day's budget.
const MaxInputUSDPerMTok = 1000

// maxBilledInputTokens bounds the token count a response may claim before it
// is priced, so a corrupt reply cannot overflow the cost into Inf.
const maxBilledInputTokens = 1 << 40

// MaxNameBytes bounds Config.Name.
const MaxNameBytes = 32

// Backend is a Jev decide.Backend.
type Backend struct {
	name          string
	endpoint      string
	model         string
	key           string // "" sends no Authorization header
	timeout       time.Duration
	maxAttempts   int
	maxRetryAfter time.Duration
	backoffBase   time.Duration
	state         func(decide.Question) any
	descs         map[string]map[string]string
	instr         map[string]string
	spend         decide.Spend
	spendKey      string
	inputPerMTok  float64
	wire          Wire
	doer          Doer

	disabled     atomic.Bool
	chargeErrors atomic.Int64

	// sleep waits d or until ctx ends; jitter returns a value in [0, n).
	// Replaceable by tests.
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func(n time.Duration) time.Duration
}

var _ decide.Backend = (*Backend)(nil)

// Validate reports whether New would accept cfg, without a Doer. Errors name
// settings and env vars, never a key.
func (cfg Config) Validate() error {
	_, err := cfg.resolve()
	return err
}

// resolve fills defaults, checks every bound and decides which key, if any,
// travels with the request.
func (cfg Config) resolve() (Config, error) {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	u, err := checkEndpoint(cfg.Endpoint)
	if err != nil {
		return Config{}, err
	}
	cfg.Endpoint = u.String()

	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	switch {
	case cfg.Model == "":
		return Config{}, errors.New("jev: model must not be blank")
	case len(cfg.Model) > MaxModelBytes:
		return Config{}, fmt.Errorf("jev: model is %d bytes; at most %d", len(cfg.Model), MaxModelBytes)
	case strings.IndexFunc(cfg.Model, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0:
		return Config{}, fmt.Errorf("jev: model %q must not contain spaces or control characters", cfg.Model)
	}

	switch {
	case cfg.Timeout == 0:
		cfg.Timeout = DefaultTimeout
	case cfg.Timeout < 0 || cfg.Timeout > MaxTimeout:
		return Config{}, fmt.Errorf("jev: timeout %s outside (0, %s]", cfg.Timeout, MaxTimeout)
	}
	switch {
	case cfg.MaxAttempts == 0:
		cfg.MaxAttempts = DefaultMaxAttempts
	case cfg.MaxAttempts < 1 || cfg.MaxAttempts > MaxAttempts:
		return Config{}, fmt.Errorf("jev: max attempts %d outside [1, %d]", cfg.MaxAttempts, MaxAttempts)
	}
	switch {
	case cfg.MaxRetryAfter == 0:
		cfg.MaxRetryAfter = min(DefaultMaxRetryAfter, cfg.Timeout)
	case cfg.MaxRetryAfter < 0 || cfg.MaxRetryAfter > cfg.Timeout:
		// A Retry-After wait that cannot fit the deadline is a guaranteed failure.
		return Config{}, fmt.Errorf("jev: max Retry-After %s outside (0, timeout %s]", cfg.MaxRetryAfter, cfg.Timeout)
	}
	switch {
	case cfg.Backoff == 0:
		cfg.Backoff = DefaultBackoff
	case cfg.Backoff < MinBackoff || cfg.Backoff > MaxBackoff:
		return Config{}, fmt.Errorf("jev: backoff %s outside [%s, %s]", cfg.Backoff, MinBackoff, MaxBackoff)
	}
	if p := cfg.InputUSDPerMTok; math.IsNaN(p) || p < 0 || p > MaxInputUSDPerMTok {
		return Config{}, fmt.Errorf("jev: input price %v USD per million tokens outside [0, %d]", p, MaxInputUSDPerMTok)
	}
	switch cfg.Wire {
	case WireSystemOne:
		if cfg.State == nil {
			cfg.State = textState
		}
	case WireGemini:
		if cfg.State == nil {
			cfg.State = func(q decide.Question) any { return string(q.Context) }
		}
	default:
		return Config{}, fmt.Errorf("jev: wire %q is not %q or %q", cfg.Wire, WireSystemOne, WireGemini)
	}
	if cfg.SpendKey == "" {
		cfg.SpendKey = defaultSpendKey
	}
	if cfg.Name == "" {
		cfg.Name = defaultName
	}
	if len(cfg.Name) > MaxNameBytes || strings.IndexFunc(cfg.Name, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-'
	}) >= 0 {
		return Config{}, fmt.Errorf("jev: name %q must be 1..%d bytes of [a-z0-9_-]", cfg.Name, MaxNameBytes)
	}

	if cfg.KeyEnv == "" {
		cfg.KeyEnv = DefaultKeyEnv
	}
	if cfg.Getenv == nil {
		cfg.Getenv = os.Getenv
	}
	if strings.ContainsAny(cfg.Key, "\r\n") {
		return Config{}, errors.New("jev: the key must not contain line breaks")
	}
	cfg.Key = strings.TrimSpace(cfg.Key)
	if cfg.Key != "" {
		return cfg, nil
	}
	if !strings.EqualFold(u.Hostname(), openRouterHost) {
		return cfg, nil // the env key stays home; send no Authorization header
	}
	key := cfg.Getenv(cfg.KeyEnv)
	if strings.ContainsAny(key, "\r\n") {
		return Config{}, fmt.Errorf("jev: environment variable %s holds an invalid key", cfg.KeyEnv)
	}
	cfg.Key = strings.TrimSpace(key)
	if cfg.Key == "" {
		return Config{}, fmt.Errorf("%w: environment variable %s is not set", ErrNoKey, cfg.KeyEnv)
	}
	return cfg, nil
}

// checkEndpoint accepts an absolute https URL, or http on a loopback host,
// with no credentials or fragment.
func checkEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return nil, errors.New("jev: endpoint is not a valid URL")
	}
	switch {
	case u.User != nil:
		return nil, errors.New("jev: endpoint must not carry credentials in the URL; use Config.Key")
	case !u.IsAbs() || u.Host == "" || u.Hostname() == "":
		return nil, fmt.Errorf("jev: endpoint %q must be an absolute http(s) URL with a host", endpoint)
	case u.Fragment != "" || strings.Contains(endpoint, "#"):
		return nil, fmt.Errorf("jev: endpoint %q must not have a fragment", endpoint)
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopbackHost(u.Hostname()):
	case u.Scheme == "http":
		return nil, fmt.Errorf("jev: endpoint %q: plain http is only allowed for a loopback host (localhost, 127.0.0.0/8, ::1)", endpoint)
	default:
		return nil, fmt.Errorf("jev: endpoint %q: scheme must be https (or http on loopback)", endpoint)
	}
	return u, nil
}

// isLoopbackHost is true for "localhost" and loopback IP literals only. It
// never resolves a name: "127.0.0.1.example.com" is not loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func textState(q decide.Question) any { return map[string]string{"text": string(q.Context)} }

// New validates cfg (see Config) and returns a backend. A missing key is an
// error wrapping ErrNoKey that names the variable, never its value.
func New(cfg Config, d Doer) (*Backend, error) {
	if d == nil {
		return nil, errors.New("jev: a Doer is required")
	}
	cfg, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	return &Backend{
		name: cfg.Name, endpoint: cfg.Endpoint, model: cfg.Model, key: cfg.Key, timeout: cfg.Timeout,
		maxAttempts: cfg.MaxAttempts, maxRetryAfter: cfg.MaxRetryAfter, backoffBase: cfg.Backoff,
		state: cfg.State, descs: cfg.Descriptions, instr: cfg.Instructions, spend: cfg.Spend, spendKey: cfg.SpendKey, inputPerMTok: cfg.InputUSDPerMTok, wire: cfg.Wire, doer: d,
		sleep: sleepCtx,
		jitter: func(n time.Duration) time.Duration {
			if n <= 0 {
				return 0
			}
			return rand.N(n)
		},
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *Backend) Name() string { return b.name }

// Serves is true for any task until the credentials have been rejected.
func (b *Backend) Serves(string) bool { return !b.disabled.Load() }

// ChargeErrors counts successful calls whose cost could not be recorded.
func (b *Backend) ChargeErrors() int64 { return b.chargeErrors.Load() }

// The request structs list their fields in key order, so a body comes out
// byte-for-byte as encoding/json v1 wrote the same object built from maps
// (n8n-email-engine's config/jev_requests.json pins those bytes).
type criteriaQuestion struct {
	Criteria     map[string]string `json:"criteria"`
	Instructions string            `json:"instructions"`
	Type         string            `json:"type"`
}

type requestBody struct {
	Model     string                      `json:"model"`
	Questions map[string]criteriaQuestion `json:"questions"`
	State     any                         `json:"state"`
}

// geminiRequestBody is requestBody in Google's dialect, keys in order.
type geminiRequestBody struct {
	Input     string                      `json:"input"`
	Model     string                      `json:"model"`
	Questions map[string]criteriaQuestion `json:"questions"`
}

// errEmptyInput is a Gemini request whose input would be empty, which Google
// refuses ("Input can't be empty"); it is never sent.
var errEmptyInput = errors.New("jev: gemini input is empty")

// checkGeminiKeys reports the first slot name or option Google's documented
// key rules would refuse, so such a question fails here, named, rather than
// as a bare HTTP 400 from the server.
func checkGeminiKeys(q decide.Question) error {
	for _, s := range q.Slots {
		if len(s.Name) == 0 || len(s.Name) > geminiMaxQuestionKey || strings.IndexFunc(s.Name, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-'
		}) >= 0 {
			return fmt.Errorf("jev: gemini refuses question key %q (1..%d of [A-Za-z0-9_-])", s.Name, geminiMaxQuestionKey)
		}
		for _, o := range s.Options {
			bad := o == "" || len(o) > geminiMaxOptionBytes || utf8.RuneCountInString(o) > geminiMaxOptionKey ||
				o != strings.Trim(o, " ") || strings.IndexFunc(o, func(r rune) bool {
				return unicode.IsControl(r) || (unicode.IsSpace(r) && r != ' ')
			}) >= 0
			if bad {
				return fmt.Errorf("jev: gemini refuses option key %q of question %q", o, s.Name)
			}
		}
	}
	return nil
}

func (b *Backend) body(q decide.Question) ([]byte, error) {
	rb := requestBody{
		Model:     b.model,
		State:     b.state(q),
		Questions: make(map[string]criteriaQuestion, len(q.Slots)),
	}
	for _, s := range q.Slots {
		crit := make(map[string]string, len(s.Options))
		for _, o := range s.Options {
			desc := b.descs[s.Name][o]
			if desc == "" {
				desc = o
			}
			crit[o] = desc
		}
		instructions := q.Prompt
		if t := b.instr[s.Name]; t != "" {
			instructions = t
		}
		rb.Questions[s.Name] = criteriaQuestion{Type: "choice", Instructions: instructions, Criteria: crit}
	}
	if b.wire != WireGemini {
		return json.Marshal(rb, jsonwire.V1Bytes)
	}
	input, ok := rb.State.(string)
	if !ok {
		// Google: "Serialize JSON objects and arrays to a string and pass them as text".
		raw, err := json.Marshal(rb.State, jsonwire.V1Bytes)
		if err != nil {
			return nil, err
		}
		input = string(raw)
	}
	if strings.TrimSpace(input) == "" {
		return nil, errEmptyInput
	}
	return json.Marshal(geminiRequestBody{Input: input, Model: rb.Model, Questions: rb.Questions}, jsonwire.V1Bytes)
}

type slotAnswer struct {
	Choice        string             `json:"choice"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type responseBody struct {
	Answers map[string]slotAnswer `json:"answers"`
	Usage   struct {
		Cost        float64 `json:"cost"`
		InputTokens int64   `json:"input_tokens"`       // SubQ
		TotalInput  int64   `json:"total_input_tokens"` // Gemini
	} `json:"usage"`
}

// cost is what the call cost: the reported usage.cost when positive, else
// usage.input_tokens at the configured input price, else 0. A value that is
// not a finite non-negative amount reads as 0.
func (b *Backend) cost(rb responseBody) float64 {
	c := rb.Usage.Cost
	if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
		c = 0
	}
	tokens := rb.Usage.InputTokens
	if tokens <= 0 {
		tokens = rb.Usage.TotalInput
	}
	if c == 0 && b.inputPerMTok > 0 && tokens > 0 {
		c = float64(min(tokens, maxBilledInputTokens)) * b.inputPerMTok / 1e6
	}
	return c
}

// all returns an Answer holding one reading for every slot.
func (b *Backend) all(q decide.Question, rd decide.SlotReading, cost float64) decide.Answer {
	a := decide.Answer{Backend: b.name, Slots: make(map[string]decide.SlotReading, len(q.Slots)), CostUSD: cost}
	for _, s := range q.Slots {
		a.Slots[s.Name] = rd
	}
	return a
}

// Decide asks Jev every slot of q in one call.
func (b *Backend) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	if err := decide.ValidateQuestion(q); err != nil {
		return decide.Answer{}, err
	}
	if b.disabled.Load() {
		return b.all(q, decide.SlotReading{Reading: decide.Unavailable, Detail: "auth"}, 0), ErrAuth
	}
	if b.spend != nil {
		if err := b.spend.Allow(ctx, b.spendKey); err != nil {
			return b.all(q, decide.SlotReading{Reading: decide.NotAsked, Detail: "budget"}, 0), nil
		}
	}
	if b.wire == WireGemini {
		// Checked before the budget is asked: a question this provider cannot
		// take is not a spend.
		if err := checkGeminiKeys(q); err != nil {
			return b.all(q, decide.SlotReading{Reading: decide.Refused, Detail: "unsupported_question"}, 0), err
		}
	}
	reqBody, err := b.body(q)
	if errors.Is(err, errEmptyInput) {
		return b.all(q, decide.SlotReading{Reading: decide.Refused, Detail: "empty_input"}, 0), nil
	}
	if err != nil {
		return decide.Answer{}, fmt.Errorf("jev: encode request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	raw, err := b.send(ctx, reqBody)
	var refused refusalError
	if errors.As(err, &refused) {
		// The provider read the question and declined it: the router asks the
		// next backend, and nothing marks this backend as down.
		return b.all(q, decide.SlotReading{Reading: decide.Refused, Detail: "safety:" + refused.code}, 0), nil
	}
	if err != nil {
		detail := "transport"
		if errors.Is(err, ErrAuth) {
			b.disabled.Store(true)
			detail = "auth"
		} else if ctx.Err() != nil {
			detail = "deadline"
		}
		return b.all(q, decide.SlotReading{Reading: decide.Unavailable, Detail: detail}, 0), err
	}
	return b.parse(ctx, q, raw), nil
}

func (b *Backend) parse(ctx context.Context, q decide.Question, raw []byte) decide.Answer {
	var rb responseBody
	if err := json.Unmarshal(raw, &rb); err != nil {
		return b.all(q, decide.SlotReading{Reading: decide.Failed, Detail: "bad_response"}, 0)
	}
	cost := b.cost(rb)
	if cost > 0 && b.spend != nil {
		// The call is already paid for; record it even if ctx has ended.
		if err := b.spend.Charge(context.WithoutCancel(ctx), b.spendKey, cost); err != nil {
			b.chargeErrors.Add(1)
		}
	}
	ans := decide.Answer{Backend: b.name, Slots: make(map[string]decide.SlotReading, len(q.Slots)), CostUSD: cost}
	for _, s := range q.Slots {
		ans.Slots[s.Name] = readSlot(s, rb.Answers[s.Name])
	}
	return ans
}

func readSlot(s decide.Slot, a slotAnswer) decide.SlotReading {
	if a.Choice == "" {
		return decide.SlotReading{Reading: decide.Failed, Detail: "missing_choice"}
	}
	if decide.CheckValue(s, a.Choice) != nil {
		return decide.SlotReading{Reading: decide.Failed, Detail: "unknown_choice"}
	}
	p := a.Probabilities[a.Choice]
	if a.Confidence != nil {
		p = *a.Confidence
	}
	if math.IsNaN(p) || p < 0 || p > 1 {
		return decide.SlotReading{Reading: decide.Failed, Detail: "bad_probability"}
	}
	// Jev has no abstain: every valid choice is Answered, and the caller
	// decides what probability is enough.
	return decide.SlotReading{Reading: decide.Answered, Value: a.Choice, Jev: &decide.JevCalib{Probability: p}}
}

// send posts body, retrying transport errors, 429 and 5xx up to
// b.maxAttempts. Errors never contain the key.
func (b *Backend) send(ctx context.Context, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < b.maxAttempts; attempt++ {
		raw, status, retryAfter, err := b.once(ctx, body)
		switch {
		case err == nil && status == http.StatusOK:
			return raw, nil
		case err == nil && (status == 401 || status == 402 || status == 403):
			return nil, fmt.Errorf("%w: HTTP %d", ErrAuth, status)
		case err == nil && status == http.StatusBadRequest && b.wire == WireGemini:
			if code, ok := geminiRefusal(raw); ok {
				return nil, refusalError{code: code}
			}
			return nil, fmt.Errorf("jev: HTTP %d", status)
		case err == nil && status != 429 && status < 500:
			return nil, fmt.Errorf("jev: HTTP %d", status)
		case err == nil:
			lastErr = fmt.Errorf("jev: HTTP %d", status)
		default:
			lastErr = err
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("jev: %w", ctx.Err())
		}
		if attempt == b.maxAttempts-1 {
			break
		}
		if err := b.sleep(ctx, b.backoff(attempt, retryAfter)); err != nil {
			return nil, fmt.Errorf("jev: %w", err)
		}
	}
	return nil, lastErr
}

// refusalError is a request the provider declined on its content (a Gemini
// input safety block). code is one of geminiRefusalCodes, never message text.
type refusalError struct{ code string }

func (e refusalError) Error() string { return "jev: the provider refused the input (" + e.code + ")" }

// geminiRefusal reads a Gemini 400 body and reports its error.code when it is
// one of the documented safety codes.
func geminiRefusal(raw []byte) (string, bool) {
	var body struct {
		Error struct {
			Code any `json:"code"` // a string for a refusal; other errors may carry a number
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return "", false
	}
	code, ok := body.Error.Code.(string)
	if !ok || !slices.Contains(geminiRefusalCodes, code) {
		return "", false
	}
	return code, true
}

func (b *Backend) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, b.maxRetryAfter)
	}
	d := b.backoffBase << attempt
	return d + b.jitter(d/2)
}

// once performs one HTTP exchange. A non-nil error is a transport failure.
func (b *Backend) once(ctx context.Context, body []byte) (raw []byte, status int, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, errors.New("jev: build request failed")
	}
	switch {
	case b.key == "":
	case b.wire == WireGemini:
		req.Header.Set("x-goog-api-key", b.key)
	default:
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.doer.Do(ctx, req)
	if err != nil {
		return nil, 0, 0, b.redact(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, 0, 0, b.redact(err)
	}
	if len(raw) > maxResponseSize {
		return nil, 0, 0, errors.New("jev: response too large")
	}
	return raw, resp.StatusCode, parseRetryAfter(resp.Header.Get("Retry-After")), nil
}

// redact turns a transport error into one that cannot carry the key.
func (b *Backend) redact(err error) error {
	msg := err.Error()
	if b.key != "" { // ReplaceAll with "" would insert the marker between every byte
		msg = strings.ReplaceAll(msg, b.key, "[redacted]")
	}
	return errors.New("jev: transport: " + msg)
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(min(n, 3600)) * time.Second // far above MaxTimeout; avoids overflow
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}
