package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/localserver"
	"github.com/bharathvbcr/Vani/internal/posting"

	"github.com/bharathvbcr/decide"
)

// `vani bench local` measures local decision models against what the run's
// primary backend already read. It takes postings whose four triage slots the
// primary answered, asks each local model the exact request the run's shadow
// gets (localBackend + jdtriage.Triager, target summary included), and
// reports per-slot agreement with the primary, the confusion between them,
// agreement by the local model's probability, and latency.
//
// Agreement is not accuracy: the primary can be the one that is wrong.
// Accuracy needs labels (`vani label`, `vani eval`). The bench reads the
// ledger as a snapshot, writes nothing, needs no API key and talks only to
// loopback: llama-server instances it starts itself (Metal only, as a run
// does), or with --endpoint one /v1/systemone server already running there
// (a model llama.cpp cannot load, such as RSI-Jev).

const (
	defaultBenchPostings = 400
	maxBenchPostings     = 2000
	maxBenchModels       = 4
	benchProgressEvery   = 50
	// A model that fails this many postings in a row has stopped answering;
	// the bench gives up on it instead of spending a timeout per posting.
	benchMaxConsecutiveErrors = 10
)

// Probability buckets for the calibration table: [lo, hi).
var benchBuckets = []float64{0, 0.5, 0.7, 0.9, 1.0000001}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

type benchBucket struct {
	Lo    float64 `json:"lo"`
	Hi    float64 `json:"hi"`
	N     int     `json:"n"`
	Agree int     `json:"agree"`
}

type benchSlot struct {
	Slot string `json:"slot"`
	// N is postings where both the primary and the local model answered;
	// Agree how many of those chose the same option.
	N     int `json:"n"`
	Agree int `json:"agree"`
	// Unanswered is postings the local model gave no answer for this slot
	// (failed, abstained, unavailable): never counted as agreement.
	Unanswered int `json:"unanswered"`
	// Repro compares with the shadow reading the run recorded, when there is
	// one: for the model that ran as the shadow it shows the bench reproduces
	// production (same request, same server).
	ReproN     int                       `json:"repro_n"`
	ReproAgree int                       `json:"repro_agree"`
	Confusion  map[string]map[string]int `json:"confusion"` // primary -> local -> n
	ByLocalP   []benchBucket             `json:"by_local_p"`
	ByPrimaryP []benchBucket             `json:"by_primary_p"`
}

type benchLatency struct {
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	Max float64 `json:"max_ms"`
}

type benchModel struct {
	Name      string       `json:"name"`
	Path      string       `json:"path"`
	Questions string       `json:"questions"`
	StartMS   int64        `json:"start_ms"`
	Asked     int          `json:"asked"`
	Errors    int          `json:"errors"`
	Stopped   string       `json:"stopped,omitempty"` // why the bench gave up early
	Error     string       `json:"error,omitempty"`   // the model never ran
	Latency   benchLatency `json:"latency"`
	Slots     []benchSlot  `json:"slots"`
}

type benchReport struct {
	Postings int            `json:"postings"`
	Eligible int            `json:"eligible"`
	Primary  map[string]int `json:"primary"` // primary backend -> postings
	Models   []benchModel   `json:"models"`
}

// benchCase is one posting with the primary's readings.
type benchCase struct {
	rec      ledger.Record
	primary  map[string]string
	primaryP map[string]float64
	shadow   map[string]string
}

func (e *env) cmdBench(args []string) error {
	if len(args) == 0 || args[0] != "local" {
		return usagef("bench: expected local")
	}
	fs := e.newFlags("bench local")
	asJSON := fs.Bool("json", false, "one JSON report on stdout")
	var models stringList
	fs.Var(&models, "model", "an absolute GGUF path to measure (repeatable; default the configured local model)")
	n := fs.Int("n", defaultBenchPostings, fmt.Sprintf("at most this many postings (1-%d)", maxBenchPostings))
	questions := fs.String("questions", "combined", "combined (the request the OpenRouter decision model gets) or per_slot")
	batch := fs.Int("batch", 0, "llama.cpp batch and micro-batch size (0 = its default; vllm-d3 needs the longest prompt)")
	parallel := fs.Int("parallel", 0, fmt.Sprintf("llama.cpp -np (0 = auto, up to %d)", localserver.MaxParallel))
	ctxSize := fs.Int("ctx", 0, "context size (0 = the configured one, else 8192)")
	timeoutMS := fs.Int("timeout-ms", 20000, "per-posting timeout in milliseconds (1000-60000)")
	binary := fs.String("binary", "", "llama-server binary (default the configured one, else llama-server on PATH)")
	endpoint := fs.String("endpoint", "", "measure a /v1/systemone server already running on loopback (e.g. http://127.0.0.1:8000/v1/systemone) instead of starting llama-server")
	name := fs.String("name", "", "with --endpoint: the model name sent in each request and shown in the report (default endpoint)")
	if err := parse(fs, args[1:]); err != nil {
		return err
	}
	switch {
	case *n < 1 || *n > maxBenchPostings:
		return usagef("bench local: -n must be 1-%d", maxBenchPostings)
	case *questions != "combined" && *questions != "per_slot":
		return usagef("bench local: --questions must be combined or per_slot")
	case *timeoutMS < 1000 || *timeoutMS > 60000:
		return usagef("bench local: --timeout-ms must be 1000-60000")
	case len(models) > maxBenchModels:
		return usagef("bench local: at most %d --model", maxBenchModels)
	case *name != "" && *endpoint == "":
		return usagef("bench local: --name needs --endpoint")
	case *endpoint != "" && (len(models) > 0 || *binary != "" || *batch != 0 || *parallel != 0 || *ctxSize != 0):
		return usagef("bench local: --endpoint starts no server, so it takes no --model, --binary, --batch, --parallel or --ctx")
	}
	if *endpoint != "" {
		if err := loopbackEndpoint(*endpoint); err != nil {
			return usagef("bench local: --endpoint: %v", err)
		}
		if *name == "" {
			*name = "endpoint"
		}
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.TargetSummary) == "" {
		return errors.New("bench local: the config has no target_summary; the run's request includes it, so the bench needs it too")
	}
	if *endpoint != "" {
		return e.runBench(*asJSON, *n, func(ctx context.Context, cases []benchCase) []benchModel {
			return []benchModel{e.benchServer(ctx, *name, *endpoint, *endpoint, *questions, time.Duration(*timeoutMS)*time.Millisecond, cfg.TargetSummary, cases)}
		})
	}
	srv := localserver.Config{BatchSize: *batch, Parallel: *parallel, ContextSize: *ctxSize}
	if l := cfg.Decide.Local; l != nil && l.Server != nil {
		if srv.ContextSize == 0 {
			srv.ContextSize = l.Server.ContextSize
		}
		srv.StartTimeout = time.Duration(l.Server.StartTimeoutMS) * time.Millisecond
		// The configured batch and slots apply unless a flag says otherwise,
		// so a bare `bench local` measures the run's own shadow server.
		if srv.BatchSize == 0 {
			srv.BatchSize = l.Server.BatchSize
		}
		if srv.Parallel == 0 {
			srv.Parallel = l.Server.Parallel
		}
		if *binary == "" {
			*binary = l.Server.Binary
		}
		if len(models) == 0 {
			models = append(models, l.Server.ModelPath)
		}
	}
	if len(models) == 0 {
		return usagef("bench local: no --model and no decide.local.server.model_path in the config")
	}
	if *binary == "" {
		p, err := exec.LookPath("llama-server")
		if err != nil {
			return fmt.Errorf("bench local: llama-server not found on PATH (pass --binary): %w", err)
		}
		if *binary, err = filepath.Abs(p); err != nil {
			return err
		}
	}
	srv.Binary = *binary
	for _, m := range models {
		if !filepath.IsAbs(m) || !strings.HasSuffix(strings.ToLower(m), ".gguf") {
			return usagef("bench local: --model %q must be an absolute path to a .gguf file", m)
		}
	}

	return e.runBench(*asJSON, *n, func(ctx context.Context, cases []benchCase) []benchModel {
		var out []benchModel
		for _, path := range models {
			out = append(out, e.benchModel(ctx, srv, path, *questions, time.Duration(*timeoutMS)*time.Millisecond, cfg.TargetSummary, cases))
			if ctx.Err() != nil {
				break
			}
		}
		return out
	})
}

// runBench picks the postings, measures them with measure, and reports. Any
// model that did not run, stopped early or was asked nothing fails the command.
func (e *env) runBench(asJSON bool, n int, measure func(context.Context, []benchCase) []benchModel) error {
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		return fmt.Errorf("bench local: read the ledger: %w", err)
	}
	cases, eligible, primary := benchCases(snap, n)
	if len(cases) == 0 {
		return errors.New("bench local: no posting in the ledger has all four triage slots answered by a primary backend")
	}
	rep := benchReport{Postings: len(cases), Eligible: eligible, Primary: primary}

	ctx, stop := signalContext()
	defer stop()
	failed := false
	rep.Models = measure(ctx, cases)
	for _, m := range rep.Models {
		// Errors == Asked: it answered no posting, which a run too short for
		// the error streak would otherwise report as a pass.
		if m.Error != "" || m.Stopped != "" || m.Asked == 0 || m.Errors == m.Asked {
			failed = true
		}
	}
	if asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else {
		writeBenchText(e, rep)
	}
	switch {
	case ctx.Err() != nil:
		return errors.New("bench local: interrupted; the report covers what ran")
	case failed:
		// A model that could not run, or stopped early, must not read as a pass.
		return errors.New("bench local: at least one model did not complete; see its error")
	}
	return nil
}

// benchCases picks up to n postings whose four triage slots a primary backend
// (anything but the local shadow) answered. When there are more than n, it
// takes them round-robin across companies (each company in key order), so a
// partial run is not one company's postings: keys sort by ATS, then company.
func benchCases(snap ledger.Snapshot, n int) ([]benchCase, int, map[string]int) {
	var all []benchCase
	backends := map[posting.Key]string{}
	for _, r := range snap.Records {
		ann := r.Annotations
		c := benchCase{rec: r, primary: map[string]string{}, primaryP: map[string]float64{}, shadow: map[string]string{}}
		backend, ok := "", true
		for _, slot := range digest.TriageSlots {
			pre := ledger.AnnTriagePrefix + slot
			b := ann[pre+ledger.AnnTriageSuffixBackend]
			if ann[pre+ledger.AnnTriageSuffixReading] != string(decide.Answered) || ann[pre] == "" || b == "" || config.IsLocalShadow(b) {
				ok = false
				break
			}
			if backend == "" {
				backend = b
			}
			c.primary[slot] = ann[pre]
			if p, err := parseProb(ann[pre+ledger.AnnTriageSuffixP]); err == nil {
				c.primaryP[slot] = p
			}
			if v := ann[pre+ledger.AnnTriageSuffixShadow]; v != "" {
				c.shadow[slot] = v
			}
		}
		if !ok || strings.TrimSpace(r.Posting.Description) == "" {
			continue
		}
		all = append(all, c)
		backends[r.Key] = backend
	}
	out := all
	if len(all) > n {
		byCompany := map[string][]benchCase{}
		var order []string
		for _, c := range all {
			co := strings.ToLower(strings.TrimSpace(c.rec.Posting.Company))
			if _, seen := byCompany[co]; !seen {
				order = append(order, co)
			}
			byCompany[co] = append(byCompany[co], c)
		}
		out = make([]benchCase, 0, n)
		for i := 0; len(out) < n; i++ {
			for _, co := range order {
				if i < len(byCompany[co]) && len(out) < n {
					out = append(out, byCompany[co][i])
				}
			}
		}
	}
	primary := map[string]int{}
	for _, c := range out {
		primary[backends[c.rec.Key]]++
	}
	return out, len(all), primary
}

func parseProb(s string) (float64, error) {
	var p float64
	if _, err := fmt.Sscan(s, &p); err != nil {
		return 0, err
	}
	if math.IsNaN(p) || p < 0 || p > 1 {
		return 0, fmt.Errorf("probability %q out of range", s)
	}
	return p, nil
}

// loopbackEndpoint accepts only an http URL on this Mac's loopback with an
// explicit port: the bench never sends postings anywhere else.
func loopbackEndpoint(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme != "http" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q is not http://<loopback>:<port>/<path>", s)
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
		return nil
	}
	return fmt.Errorf("%q is not on loopback", s)
}

func newBenchModel(name, path, questions string) benchModel {
	m := benchModel{Name: name, Path: path, Questions: questions}
	for _, slot := range digest.TriageSlots {
		bs := benchSlot{Slot: slot, Confusion: map[string]map[string]int{}}
		for i := 0; i+1 < len(benchBuckets); i++ {
			bs.ByLocalP = append(bs.ByLocalP, benchBucket{Lo: benchBuckets[i], Hi: math.Min(benchBuckets[i+1], 1)})
			bs.ByPrimaryP = append(bs.ByPrimaryP, benchBucket{Lo: benchBuckets[i], Hi: math.Min(benchBuckets[i+1], 1)})
		}
		m.Slots = append(m.Slots, bs)
	}
	return m
}

// benchModel starts llama-server on the GGUF at path, measures it and stops it.
func (e *env) benchModel(ctx context.Context, srvCfg localserver.Config, path, questions string, timeout time.Duration, summary string, cases []benchCase) benchModel {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	srvCfg.Model = path
	t0 := time.Now()
	srv, err := localserver.Start(ctx, srvCfg)
	startMS := time.Since(t0).Milliseconds()
	if err != nil {
		m := newBenchModel(name, path, questions)
		m.StartMS = startMS
		m.Error = err.Error()
		fmt.Fprintf(e.stderr, "bench: %s did not start: %v\n", name, err)
		return m
	}
	defer srv.Stop()
	m := e.benchServer(ctx, name, path, srv.Endpoint, questions, timeout, summary, cases)
	m.StartMS = startMS
	return m
}

// benchServer asks the decision server at endpoint every case, with the run's
// shadow request, and tallies agreement with the primary.
func (e *env) benchServer(ctx context.Context, name, path, endpoint, questions string, timeout time.Duration, summary string, cases []benchCase) benchModel {
	m := newBenchModel(name, path, questions)
	// The backend's name never reaches the ledger here, so a long model file
	// name must not stop a bench that the shadow's 32-byte name limit would.
	lb, err := localBackendNamed(&config.Local{Model: name, Questions: questions, TimeoutMS: int(timeout / time.Millisecond)}, endpoint, "local-bench")
	if err != nil {
		m.Error = err.Error()
		return m
	}
	tr := jdtriage.Triager{D: lb, TargetSummary: summary}
	var lat []float64
	streak := 0
	for i, c := range cases {
		if ctx.Err() != nil {
			m.Stopped = "interrupted"
			break
		}
		t := time.Now()
		ans, err := tr.Triage(ctx, c.rec.Posting)
		lat = append(lat, float64(time.Since(t).Microseconds())/1000)
		m.Asked++
		answered := false
		for si, slot := range digest.TriageSlots {
			bs := &m.Slots[si]
			rd, ok := ans.Slots[slot]
			if err != nil || !ok || rd.Reading != decide.Answered || rd.Value == "" {
				bs.Unanswered++
				continue
			}
			answered = true
			pv := c.primary[slot]
			bs.N++
			agree := rd.Value == pv
			if agree {
				bs.Agree++
			}
			if bs.Confusion[pv] == nil {
				bs.Confusion[pv] = map[string]int{}
			}
			bs.Confusion[pv][rd.Value]++
			if rd.Jev != nil {
				addBucket(bs.ByLocalP, rd.Jev.Probability, agree)
			}
			if p, ok := c.primaryP[slot]; ok {
				addBucket(bs.ByPrimaryP, p, agree)
			}
			if sv, ok := c.shadow[slot]; ok {
				bs.ReproN++
				if sv == rd.Value {
					bs.ReproAgree++
				}
			}
		}
		if err != nil || !answered {
			m.Errors++
			streak++
			if streak >= benchMaxConsecutiveErrors {
				m.Stopped = fmt.Sprintf("%d postings in a row got no answer (last: %v)", streak, benchErr(err, ans))
				break
			}
		} else {
			streak = 0
		}
		if (i+1)%benchProgressEvery == 0 {
			fmt.Fprintf(e.stderr, "bench: %s %d/%d\n", name, i+1, len(cases))
		}
	}
	m.Latency = latencyOf(lat)
	return m
}

func benchErr(err error, ans decide.Answer) string {
	if err != nil {
		return oneLine(err.Error(), 200)
	}
	for _, slot := range digest.TriageSlots {
		if rd, ok := ans.Slots[slot]; ok && rd.Reading != decide.Answered {
			return string(rd.Reading) + " " + oneLine(rd.Detail, 120)
		}
	}
	return "no slot answered"
}

func addBucket(bs []benchBucket, p float64, agree bool) {
	for i := range bs {
		if p >= bs[i].Lo && (p < bs[i].Hi || (i == len(bs)-1 && p <= bs[i].Hi)) {
			bs[i].N++
			if agree {
				bs[i].Agree++
			}
			return
		}
	}
}

func latencyOf(ms []float64) benchLatency {
	if len(ms) == 0 {
		return benchLatency{}
	}
	s := append([]float64(nil), ms...)
	sort.Float64s(s)
	q := func(f float64) float64 { return math.Round(s[min(len(s)-1, int(f*float64(len(s))))]*10) / 10 }
	return benchLatency{P50: q(0.5), P95: q(0.95), Max: math.Round(s[len(s)-1]*10) / 10}
}

func pct(k, n int) string {
	if n == 0 {
		return "   -  "
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(k)/float64(n))
}

func writeBenchText(e *env, rep benchReport) {
	w := e.stdout
	var prim []string
	for b, k := range rep.Primary {
		prim = append(prim, fmt.Sprintf("%s %d", b, k))
	}
	sort.Strings(prim)
	fmt.Fprintf(w, "postings: %d of %d eligible (primary: %s)\n", rep.Postings, rep.Eligible, strings.Join(prim, ", "))
	fmt.Fprintln(w, "agreement with the primary is not accuracy; label postings (vani label) for that")
	fmt.Fprintln(w, "=ledger: same choice as the shadow the run recorded (reproduction when it is the same model)")
	for _, m := range rep.Models {
		fmt.Fprintf(w, "\n%s (%s questions)\n", m.Name, m.Questions)
		if m.Error != "" {
			fmt.Fprintf(w, "  did not run: %s\n", m.Error)
			continue
		}
		fmt.Fprintf(w, "  started in %d ms; asked %d, %d with no answer; latency p50 %.0f ms, p95 %.0f ms, max %.0f ms\n",
			m.StartMS, m.Asked, m.Errors, m.Latency.P50, m.Latency.P95, m.Latency.Max)
		if m.Stopped != "" {
			fmt.Fprintf(w, "  STOPPED EARLY: %s\n", m.Stopped)
		}
		fmt.Fprintf(w, "  %-12s %9s %8s %10s %9s   agreement by local p (<.5 .5-.7 .7-.9 >=.9)\n", "slot", "agree", "n", "unanswered", "=ledger")
		for _, s := range m.Slots {
			var b []string
			for _, bk := range s.ByLocalP {
				b = append(b, fmt.Sprintf("%s/%d", strings.TrimSpace(pct(bk.Agree, bk.N)), bk.N))
			}
			fmt.Fprintf(w, "  %-12s %9s %8d %10d %9s   %s\n", s.Slot, pct(s.Agree, s.N), s.N, s.Unanswered, pct(s.ReproAgree, s.ReproN), strings.Join(b, "  "))
		}
		for _, s := range m.Slots {
			type pair struct {
				p, l string
				n    int
			}
			var off []pair
			for p, row := range s.Confusion {
				for l, k := range row {
					if p != l {
						off = append(off, pair{p, l, k})
					}
				}
			}
			sort.Slice(off, func(i, j int) bool {
				if off[i].n != off[j].n {
					return off[i].n > off[j].n
				}
				return off[i].p+off[i].l < off[j].p+off[j].l
			})
			if len(off) == 0 {
				continue
			}
			var top []string
			for _, o := range off[:min(3, len(off))] {
				top = append(top, fmt.Sprintf("%s→%s %d", o.p, o.l, o.n))
			}
			fmt.Fprintf(w, "  %s disagreements (primary→local): %s\n", s.Slot, strings.Join(top, ", "))
		}
	}
}
