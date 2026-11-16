package jev

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/decide"
)

const testKey = "sk-or-TESTKEY-do-not-leak"

type step struct {
	status int
	body   string
	header map[string]string
	err    error
}

type fakeDoer struct {
	mu     sync.Mutex
	steps  []step
	reqs   []*http.Request
	bodies []string
}

func (f *fakeDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(req.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	f.bodies = append(f.bodies, string(b))
	i := len(f.reqs) - 1
	if i >= len(f.steps) {
		i = len(f.steps) - 1
	}
	s := f.steps[i]
	if s.err != nil {
		return nil, s.err
	}
	h := http.Header{}
	for k, v := range s.header {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: s.status, Header: h, Body: io.NopCloser(strings.NewReader(s.body))}, nil
}

func ok(body string) step { return step{status: 200, body: body} }

func newBackend(t *testing.T, cfg Config, steps ...step) (*Backend, *fakeDoer, *[]time.Duration) {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", testKey)
	d := &fakeDoer{steps: steps}
	b, err := New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	var sleeps []time.Duration
	b.sleep = func(ctx context.Context, dur time.Duration) error {
		sleeps = append(sleeps, dur)
		return ctx.Err()
	}
	b.jitter = func(time.Duration) time.Duration { return 0 }
	return b, d, &sleeps
}

func question() decide.Question {
	return decide.Question{
		Task: "career.jd_triage", Prompt: "Classify it.", Context: []byte("hello world"),
		Slots: []decide.Slot{
			{Name: "mode", Options: []string{"remote", "onsite"}},
			{Name: "level", Options: []string{"junior", "senior"}},
		},
	}
}

const goodResp = `{"answers":{"mode":{"choice":"remote","confidence":0.9,"probabilities":{"remote":0.8,"onsite":0.2}},
"level":{"choice":"senior","probabilities":{"junior":0.3,"senior":0.7}}},"usage":{"cost":0.002}}`

func TestRequestShapeGolden(t *testing.T) {
	b, d, _ := newBackend(t, Config{Descriptions: map[string]map[string]string{"mode": {"remote": "fully remote"}}}, ok(goodResp))
	if _, err := b.Decide(context.Background(), question()); err != nil {
		t.Fatal(err)
	}
	// Keys in sorted order at every level, as encoding/json writes a map.
	want := `{"model":"typesafe/jev-1.13","questions":{` +
		`"level":{"criteria":{"junior":"junior","senior":"senior"},"instructions":"Classify it.","type":"choice"},` +
		`"mode":{"criteria":{"onsite":"onsite","remote":"fully remote"},"instructions":"Classify it.","type":"choice"}},` +
		`"state":{"text":"hello world"}}`
	if d.bodies[0] != want {
		t.Errorf("body:\n got %s\nwant %s", d.bodies[0], want)
	}
	r := d.reqs[0]
	if r.URL.String() != "https://openrouter.ai/api/alpha/decisions" || r.Method != "POST" {
		t.Errorf("%s %s", r.Method, r.URL)
	}
	if r.Header.Get("Authorization") != "Bearer "+testKey || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", r.Header)
	}
}

func TestParsesAnswersAndCost(t *testing.T) {
	b, _, _ := newBackend(t, Config{}, ok(goodResp))
	a, err := b.Decide(context.Background(), question())
	if err != nil {
		t.Fatal(err)
	}
	m := a.Slots["mode"]
	if m.Reading != decide.Answered || m.Value != "remote" || m.Jev == nil || m.Jev.Probability != 0.9 {
		t.Errorf("mode = %+v", m)
	}
	l := a.Slots["level"] // confidence absent: falls back to probabilities[choice]
	if l.Value != "senior" || l.Jev == nil || l.Jev.Probability != 0.7 || l.Lappi != nil {
		t.Errorf("level = %+v", l)
	}
	if a.CostUSD != 0.002 || a.Backend != "jev" {
		t.Errorf("cost=%v backend=%q", a.CostUSD, a.Backend)
	}
}

func TestStrictParsing(t *testing.T) {
	cases := map[string]string{
		"unknown choice":  `{"answers":{"mode":{"choice":"hybrid","confidence":0.9},"level":{"choice":"senior","confidence":0.5}},"usage":{"cost":0.01}}`,
		"case differs":    `{"answers":{"mode":{"choice":"Remote","confidence":0.9},"level":{"choice":"senior","confidence":0.5}}}`,
		"missing slot":    `{"answers":{"level":{"choice":"senior","confidence":0.5}}}`,
		"empty choice":    `{"answers":{"mode":{"choice":"","confidence":0.9},"level":{"choice":"senior","confidence":0.5}}}`,
		"probability > 1": `{"answers":{"mode":{"choice":"remote","confidence":7},"level":{"choice":"senior","confidence":0.5}}}`,
		"probability < 0": `{"answers":{"mode":{"choice":"remote","confidence":-1},"level":{"choice":"senior","confidence":0.5}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			b, _, _ := newBackend(t, Config{}, ok(body))
			a, err := b.Decide(context.Background(), question())
			if err != nil {
				t.Fatal(err)
			}
			if got := a.Slots["mode"]; got.Reading != decide.Failed || got.Value != "" || got.Jev != nil {
				t.Errorf("mode = %+v", got)
			}
			if got := a.Slots["level"]; name != "missing slot" && got.Reading != decide.Answered {
				t.Errorf("level = %+v: one bad slot must not poison the others", got)
			}
		})
	}
	// A failed parse still reports what the call cost.
	b, _, _ := newBackend(t, Config{}, ok(`{"answers":{},"usage":{"cost":0.01}}`))
	if a, _ := b.Decide(context.Background(), question()); a.CostUSD != 0.01 {
		t.Errorf("cost = %v", a.CostUSD)
	}
	// Unparseable body: every slot Failed.
	b, _, _ = newBackend(t, Config{}, ok(`<html>`))
	a, _ := b.Decide(context.Background(), question())
	for n, rd := range a.Slots {
		if rd.Reading != decide.Failed {
			t.Errorf("%s = %+v", n, rd)
		}
	}
}

func TestRetries429ThenSucceeds(t *testing.T) {
	b, d, sleeps := newBackend(t, Config{}, step{status: 429, body: "slow down"}, step{status: 503}, ok(goodResp))
	a, err := b.Decide(context.Background(), question())
	if err != nil || a.Slots["mode"].Reading != decide.Answered {
		t.Fatalf("err=%v a=%+v", err, a)
	}
	if len(d.reqs) != 3 {
		t.Errorf("attempts = %d", len(d.reqs))
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != 500*time.Millisecond || (*sleeps)[1] != time.Second {
		t.Errorf("sleeps = %v, want exponential 500ms, 1s", *sleeps)
	}
}

func TestRetryAfterHonouredAndCapped(t *testing.T) {
	cases := []struct {
		cfg     Config
		wantCap time.Duration
	}{
		{Config{Timeout: 60 * time.Second}, 30 * time.Second},                                  // the default cap
		{Config{}, DefaultTimeout},                                                             // never above the deadline
		{Config{Timeout: 60 * time.Second, MaxRetryAfter: 45 * time.Second}, 45 * time.Second}, // configured
	}
	for _, tc := range cases {
		b, _, sleeps := newBackend(t, tc.cfg,
			step{status: 429, header: map[string]string{"Retry-After": "7"}},
			step{status: 429, header: map[string]string{"Retry-After": "99999"}},
			ok(goodResp))
		if _, err := b.Decide(context.Background(), question()); err != nil {
			t.Fatal(err)
		}
		if len(*sleeps) != 2 || (*sleeps)[0] != 7*time.Second || (*sleeps)[1] != tc.wantCap {
			t.Errorf("%+v: sleeps = %v, want 7s then the %s cap", tc.cfg, *sleeps, tc.wantCap)
		}
	}
}

func TestAttemptsAndBackoffConfigurable(t *testing.T) {
	b, d, sleeps := newBackend(t, Config{MaxAttempts: 5, Backoff: time.Millisecond}, step{status: 503})
	if _, err := b.Decide(context.Background(), question()); err == nil || len(d.reqs) != 5 {
		t.Fatalf("err=%v attempts=%d, want an error after exactly 5", err, len(d.reqs))
	}
	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 8 * time.Millisecond}
	if len(*sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", *sleeps, want)
	}
	for i := range want {
		if (*sleeps)[i] != want[i] {
			t.Errorf("sleeps = %v, want %v", *sleeps, want)
			break
		}
	}
}

func TestConfigBoundsRefused(t *testing.T) {
	bad := []Config{
		{MaxAttempts: -1},
		{MaxAttempts: MaxAttempts + 1},
		{MaxRetryAfter: -time.Second},
		{MaxRetryAfter: DefaultTimeout + time.Second}, // cannot fit the deadline
		{Timeout: 10 * time.Second, MaxRetryAfter: 11 * time.Second},
		{Backoff: -time.Millisecond},
		{Backoff: time.Microsecond},
		{Backoff: MaxBackoff + time.Second},
		{Model: "   "},
		{Model: "jev latest"},
		{Model: "jev\x00"},
		{Model: strings.Repeat("m", MaxModelBytes+1)},
		{Key: "k\r\nX-Injected: 1"},
	}
	for _, c := range bad {
		c.Getenv = env(map[string]string{DefaultKeyEnv: testKey})
		if err := c.Validate(); err == nil {
			t.Errorf("%+v accepted", c)
		}
		if _, err := New(c, &fakeDoer{}); err == nil {
			t.Errorf("New accepted %+v", c)
		}
	}
	good := Config{MaxAttempts: MaxAttempts, Backoff: MinBackoff, Timeout: MaxTimeout, MaxRetryAfter: MaxTimeout,
		Getenv: env(map[string]string{DefaultKeyEnv: testKey})}
	if err := good.Validate(); err != nil {
		t.Errorf("upper bounds refused: %v", err)
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestEndpointRefusesUnsafeOrMalformedInput(t *testing.T) {
	for _, ep := range []string{
		"/v1/systemone",
		"localhost:8000/v1/systemone",
		"http://",
		"https:///v1",
		"ftp://127.0.0.1/v1",
		"file:///etc/passwd",
		"http://example.com/v1/systemone",
		"http://10.0.0.5:8000/v1/systemone",
		"http://0.0.0.0:8000/v1/systemone",
		"http://127.0.0.1.example.com/v1",
		"http://localhost.example.com/v1",
		"https://user:pw@example.com/v1",
		"https://example.com/v1#frag",
		"https://exa mple.com/v1",
	} {
		c := Config{Endpoint: ep, Key: "explicit"}
		if err := c.Validate(); err == nil {
			t.Errorf("endpoint %q accepted", ep)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("endpoint %q: error echoes credentials: %v", ep, err)
		}
	}
}

// A non-OpenRouter endpoint needs no key, and sends no Authorization header
// at all (not an empty "Bearer ") when it has none.
func TestLoopbackHttpWithoutKeySendsNoAuthorization(t *testing.T) {
	for _, ep := range []string{
		"http://127.0.0.1:8000/v1/systemone",
		"http://127.8.9.10:8000/v1/systemone",
		"http://localhost:8000/v1/systemone",
		"http://LOCALHOST:8000/v1/systemone",
		"http://[::1]:8000/v1/systemone",
		"https://localhost:8443/v1/systemone",
	} {
		d := &fakeDoer{steps: []step{ok(goodResp)}}
		b, err := New(Config{Endpoint: ep, Model: "rsi-jev-v6.0-vl-4b", Getenv: env(nil)}, d)
		if err != nil {
			t.Errorf("%s: %v", ep, err)
			continue
		}
		if _, err := b.Decide(context.Background(), question()); err != nil {
			t.Fatalf("%s: %v", ep, err)
		}
		if _, present := d.reqs[0].Header["Authorization"]; present {
			t.Errorf("%s: an Authorization header was sent with no key", ep)
		}
		if !strings.Contains(d.bodies[0], `"model":"rsi-jev-v6.0-vl-4b"`) || d.reqs[0].URL.Host != strings.Split(strings.TrimPrefix(strings.TrimPrefix(ep, "http://"), "https://"), "/")[0] {
			t.Errorf("%s: request went to %s with body %s", ep, d.reqs[0].URL, d.bodies[0])
		}
	}
}

// The env key belongs to OpenRouter: moving the endpoint must not move the
// key with it. Only Config.Key goes to another host.
func TestEnvKeyNeverLeavesOpenRouter(t *testing.T) {
	getenv := env(map[string]string{DefaultKeyEnv: testKey, "CUSTOM_KEY": testKey})
	for _, ep := range []string{"https://decisions.example.com/v1", "http://127.0.0.1:9/v1"} {
		for _, keyEnv := range []string{"", "CUSTOM_KEY"} {
			d := &fakeDoer{steps: []step{ok(goodResp)}}
			b, err := New(Config{Endpoint: ep, KeyEnv: keyEnv, Getenv: getenv}, d)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.Decide(context.Background(), question()); err != nil {
				t.Fatal(err)
			}
			if got := d.reqs[0].Header.Get("Authorization"); got != "" {
				t.Errorf("%s (KeyEnv %q): env key forwarded to a non-OpenRouter host", ep, keyEnv)
			}
		}
		d := &fakeDoer{steps: []step{ok(goodResp)}}
		b, err := New(Config{Endpoint: ep, Key: "explicit", Getenv: getenv}, d)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Decide(context.Background(), question()); err != nil {
			t.Fatal(err)
		}
		if got := d.reqs[0].Header.Get("Authorization"); got != "Bearer explicit" {
			t.Errorf("%s: explicit key: Authorization = %q", ep, got)
		}
	}
	d := &fakeDoer{steps: []step{ok(goodResp)}}
	b, err := New(Config{Endpoint: "https://OpenRouter.ai/api/alpha/decisions", Getenv: getenv}, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Decide(context.Background(), question()); err != nil {
		t.Fatal(err)
	}
	if got := d.reqs[0].Header.Get("Authorization"); got != "Bearer "+testKey {
		t.Error("OpenRouter host (any case) must get its env key")
	}
}

func TestOpenRouterWithoutKeyIsErrNoKey(t *testing.T) {
	for _, c := range []Config{
		{Getenv: env(nil)},
		{Endpoint: "https://openrouter.ai/other", Getenv: env(map[string]string{DefaultKeyEnv: "  "})},
	} {
		if err := c.Validate(); !errors.Is(err, ErrNoKey) {
			t.Errorf("%+v: err = %v, want ErrNoKey", c.Endpoint, err)
		}
	}
	// Any other fault is not ErrNoKey, so a caller cannot mistake it for "off".
	if err := (Config{Endpoint: "http://example.com/v1", Getenv: env(nil)}).Validate(); err == nil || errors.Is(err, ErrNoKey) {
		t.Errorf("unsafe endpoint: err = %v", err)
	}
}

func TestEmptyKeyTransportErrorIsReadable(t *testing.T) {
	d := &fakeDoer{steps: []step{{err: errors.New("dial refused")}}}
	b, err := New(Config{Endpoint: "http://127.0.0.1:9/v1", Getenv: env(nil), MaxAttempts: 1}, d)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Decide(context.Background(), question())
	if err == nil || err.Error() != "jev: transport: dial refused" {
		t.Errorf("err = %v", err)
	}
}

func TestCallerShapedState(t *testing.T) {
	state := func(q decide.Question) any {
		return map[string]any{"subject": string(q.Context), "bulk_mail": true}
	}
	b, d, _ := newBackend(t, Config{State: state}, ok(goodResp))
	if _, err := b.Decide(context.Background(), question()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(d.bodies[0], `"state":{"bulk_mail":true,"subject":"hello world"}}`) {
		t.Errorf("body = %s", d.bodies[0])
	}
	// A state that cannot be encoded is an error before any request.
	b, d, _ = newBackend(t, Config{State: func(decide.Question) any { return func() {} }}, ok(goodResp))
	if _, err := b.Decide(context.Background(), question()); err == nil || len(d.reqs) != 0 {
		t.Errorf("unencodable state: err=%v reqs=%d", err, len(d.reqs))
	}
}

// A second instance speaking the same wire shape (a local llama.cpp serving
// another decision model) must not file its readings under "jev".
func TestNameCarriesThroughEveryReply(t *testing.T) {
	b, _, _ := newBackend(t, Config{}, ok(goodResp))
	if b.Name() != "jev" {
		t.Errorf("default name %q", b.Name())
	}
	local := Config{Name: "local", Endpoint: "http://127.0.0.1:8791/v1/systemone"}
	b, _, _ = newBackend(t, local, ok(goodResp))
	a, err := b.Decide(context.Background(), question())
	if err != nil || b.Name() != "local" || a.Backend != "local" {
		t.Fatalf("answered: name=%q backend=%q err=%v", b.Name(), a.Backend, err)
	}
	for name, s := range map[string]step{"transport": {err: errors.New("refused")}, "bad body": ok("{"), "http 400": {status: 400}} {
		b, _, _ = newBackend(t, local, s, s, s)
		a, _ = b.Decide(context.Background(), question())
		if a.Backend != "local" {
			t.Errorf("%s: backend %q", name, a.Backend)
		}
	}
	for _, bad := range []string{"Jev", "local model", "a/b", strings.Repeat("n", MaxNameBytes+1)} {
		if err := (Config{Name: bad, Endpoint: "http://127.0.0.1:1/x"}).Validate(); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestPerSlotInstructions(t *testing.T) {
	b, d, _ := newBackend(t, Config{Instructions: map[string]string{"mode": "Where is the work done?"}}, ok(goodResp))
	if _, err := b.Decide(context.Background(), question()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.bodies[0], `"mode":{"criteria":{"onsite":"onsite","remote":"remote"},"instructions":"Where is the work done?"`) ||
		!strings.Contains(d.bodies[0], `"level":{"criteria":{"junior":"junior","senior":"senior"},"instructions":"Classify it."`) {
		t.Errorf("body = %s", d.bodies[0])
	}
}

func TestAttemptsAreBounded(t *testing.T) {
	b, d, _ := newBackend(t, Config{}, step{status: 500})
	a, err := b.Decide(context.Background(), question())
	if err == nil || len(d.reqs) != 3 {
		t.Fatalf("err=%v attempts=%d, want an error after exactly 3", err, len(d.reqs))
	}
	if a.Slots["mode"].Reading != decide.Unavailable {
		t.Errorf("mode = %+v", a.Slots["mode"])
	}
	if !b.Serves("x") {
		t.Error("a 5xx must not disable the backend")
	}
}

func TestNonRetryableStatusFailsAtOnce(t *testing.T) {
	b, d, _ := newBackend(t, Config{}, step{status: 400, body: "bad"})
	if _, err := b.Decide(context.Background(), question()); err == nil || len(d.reqs) != 1 {
		t.Fatalf("err=%v attempts=%d", err, len(d.reqs))
	}
}

func TestTransportErrorRetriedAndKeyRedacted(t *testing.T) {
	leak := errors.New("dial failed carrying " + testKey)
	b, d, _ := newBackend(t, Config{}, step{err: leak}, step{err: leak}, step{err: leak})
	_, err := b.Decide(context.Background(), question())
	if err == nil || len(d.reqs) != 3 {
		t.Fatalf("err=%v attempts=%d", err, len(d.reqs))
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("key in error: %v", err)
	}
}

func TestAuthDisablesBackend(t *testing.T) {
	for _, status := range []int{401, 402, 403} {
		b, d, _ := newBackend(t, Config{}, step{status: status, body: "echo " + testKey})
		a, err := b.Decide(context.Background(), question())
		if !errors.Is(err, decide.ErrAuth) || !errors.Is(err, ErrAuth) {
			t.Fatalf("%d: err = %v", status, err)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Errorf("key in error: %v", err)
		}
		if len(d.reqs) != 1 {
			t.Errorf("%d: auth failure retried %d times", status, len(d.reqs))
		}
		if b.Serves("career.jd_triage") {
			t.Errorf("%d: still serves after auth failure", status)
		}
		if a.Slots["mode"].Reading != decide.Unavailable {
			t.Errorf("mode = %+v", a.Slots["mode"])
		}
		if _, err := b.Decide(context.Background(), question()); !errors.Is(err, ErrAuth) || len(d.reqs) != 1 {
			t.Errorf("disabled backend still called out: err=%v reqs=%d", err, len(d.reqs))
		}
	}
}

func TestRouterSkipsJevAfterAuthFailure(t *testing.T) {
	b, d, _ := newBackend(t, Config{}, step{status: 401})
	r := decide.NewRouter(b)
	for range 3 {
		if _, err := r.Decide(context.Background(), question()); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.reqs) != 1 {
		t.Errorf("requests = %d, want 1", len(d.reqs))
	}
}

type fakeSpend struct {
	allowErr  error
	charged   []float64
	keys      []string
	chargeErr error
}

func (f *fakeSpend) Allow(context.Context, string) error { return f.allowErr }
func (f *fakeSpend) Charge(_ context.Context, key string, usd float64) error {
	f.keys = append(f.keys, key)
	f.charged = append(f.charged, usd)
	return f.chargeErr
}

func TestBudgetRefusalIsNotAskedWithoutNetwork(t *testing.T) {
	sp := &fakeSpend{allowErr: decide.ErrBudget}
	b, d, _ := newBackend(t, Config{Spend: sp}, ok(goodResp))
	a, err := b.Decide(context.Background(), question())
	if err != nil {
		t.Fatal(err)
	}
	for n, rd := range a.Slots {
		if rd.Reading != decide.NotAsked || rd.Detail != "budget" {
			t.Errorf("%s = %+v", n, rd)
		}
	}
	if len(d.reqs) != 0 || len(sp.charged) != 0 {
		t.Errorf("reqs=%d charged=%v", len(d.reqs), sp.charged)
	}
	if !b.Serves("x") {
		t.Error("budget refusal must not disable the backend")
	}
}

func TestChargesCostOnce(t *testing.T) {
	sp := &fakeSpend{chargeErr: errors.New("disk full")}
	b, _, _ := newBackend(t, Config{Spend: sp, SpendKey: "k"}, step{status: 429}, ok(goodResp))
	a, err := b.Decide(context.Background(), question())
	if err != nil || a.Slots["mode"].Reading != decide.Answered {
		t.Fatalf("a charge failure must not lose the answer: %v %+v", err, a)
	}
	if len(sp.charged) != 1 || sp.charged[0] != 0.002 || sp.keys[0] != "k" {
		t.Errorf("charged = %v keys = %v", sp.charged, sp.keys)
	}
	if b.ChargeErrors() != 1 {
		t.Errorf("ChargeErrors = %d", b.ChargeErrors())
	}
}

func TestWithRealDailyCap(t *testing.T) {
	cap, err := decide.NewDailyCap(filepath.Join(t.TempDir(), "s.json"), 0.001)
	if err != nil {
		t.Fatal(err)
	}
	b, d, _ := newBackend(t, Config{Spend: cap}, ok(goodResp))
	if a, _ := b.Decide(context.Background(), question()); a.Slots["mode"].Reading != decide.Answered {
		t.Fatalf("first call: %+v", a.Slots)
	}
	a, _ := b.Decide(context.Background(), question()) // 0.002 spent > 0.001 cap
	if a.Slots["mode"].Reading != decide.NotAsked || len(d.reqs) != 1 {
		t.Errorf("second call: %+v reqs=%d", a.Slots["mode"], len(d.reqs))
	}
}

func TestContextCancelDuringRetryWait(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", testKey)
	d := &fakeDoer{steps: []step{{status: 429, header: map[string]string{"Retry-After": "20"}}, ok(goodResp)}}
	b, err := New(Config{}, d)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.sleep = func(ctx context.Context, _ time.Duration) error { cancel(); <-ctx.Done(); return ctx.Err() }
	a, err := b.Decide(ctx, question())
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(d.reqs) != 1 {
		t.Errorf("attempts after cancel: %d", len(d.reqs))
	}
	if a.Slots["mode"].Reading != decide.Unavailable {
		t.Errorf("mode = %+v", a.Slots["mode"])
	}
	if !b.Serves("x") {
		t.Error("cancel must not disable the backend")
	}
}

func TestWholeCallDeadlineBoundsRealSleep(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", testKey)
	d := &fakeDoer{steps: []step{{status: 429, header: map[string]string{"Retry-After": "20"}}}}
	b, err := New(Config{Timeout: 30 * time.Millisecond}, d)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := b.Decide(context.Background(), question()); err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Decide ran %s past a 30ms deadline", time.Since(start))
	}
}

func TestNewKeyHandling(t *testing.T) {
	d := &fakeDoer{}
	t.Setenv("OPENROUTER_API_KEY", "")
	if _, err := New(Config{}, d); err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Errorf("missing key: %v", err)
	}
	t.Setenv("MY_JEV_KEY", testKey)
	if _, err := New(Config{KeyEnv: "MY_JEV_KEY"}, d); err != nil {
		t.Errorf("custom env: %v", err)
	}
	t.Setenv("BAD_KEY", "abc\ndef")
	if _, err := New(Config{KeyEnv: "BAD_KEY"}, d); err == nil || strings.Contains(err.Error(), "abc") {
		t.Errorf("newline key: %v", err)
	}
	if _, err := New(Config{KeyEnv: "MY_JEV_KEY"}, nil); err == nil {
		t.Error("nil Doer accepted")
	}
	for _, to := range []time.Duration{-1, 61 * time.Second} {
		if _, err := New(Config{KeyEnv: "MY_JEV_KEY", Timeout: to}, d); err == nil {
			t.Errorf("timeout %s accepted", to)
		}
	}
	if _, err := New(Config{KeyEnv: "MY_JEV_KEY", Timeout: 60 * time.Second}, d); err != nil {
		t.Errorf("60s rejected: %v", err)
	}
}

func TestInvalidQuestionNeverReachesNetwork(t *testing.T) {
	b, d, _ := newBackend(t, Config{}, ok(goodResp))
	q := question()
	q.Slots[0].Options = []string{"one"}
	if _, err := b.Decide(context.Background(), q); !errors.Is(err, decide.ErrInvalidQuestion) || len(d.reqs) != 0 {
		t.Fatalf("err=%v reqs=%d", err, len(d.reqs))
	}
}

func TestOversizedResponseRejected(t *testing.T) {
	big := strings.Repeat("x", maxResponseSize+10)
	b, _, _ := newBackend(t, Config{}, ok(big))
	if _, err := b.Decide(context.Background(), question()); err == nil {
		t.Error("oversized response accepted")
	}
}
