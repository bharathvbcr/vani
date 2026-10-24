package rkbresume

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

func TestIsContinuation(t *testing.T) {
	yes := []string{
		"**Maintain the ingestion, retrieval and serving layers** beneath those features, so an agent failure can be traced.",
		"**Own the retrieval and context layer** beneath those features: ingestion pipelines.",
		"**Operated the production serving and observability systems beneath those features:** owned inference serving",
		"**Build the ingestion pipelines** that feed those models, with fallback handling.",
		"**Brought the same lessons to NVIDIA:** CUDA Graphs and a TensorRT path.",
		"**Brought the same work to NVIDIA GPUs:** CUDA Graphs.",
		"**{{clmHazardSpeedup}} of that from phase-edge GPU synchronization. On:** a machine",
		"**42% of that from phase-edge synchronization**",
		"**Confirmed it with 2 independent methods:** scrambling timing",
		"Runs behind these services and keeps them up",
		"The same approach to inference at scale",
		"the same methods across teams",
	}
	for _, s := range yes {
		if !isContinuation(s) {
			t.Errorf("not recognised: %q", s)
		}
	}
	// Not continuations: a demonstrative or "the same" inside a self-contained
	// bullet, a pronoun that has its antecedent in the bullet, ordinary prose.
	no := []string{
		"**Built DevMap, a code graph:** indexing 1.6x faster; these include four languages.",
		"**Accelerated Mamba-2 training {{clmMambaSpeedup}}× over its sequential version on the same model:** a chunk-parallel scan",
		"**Applied AI engineer who raised coding-agent success by {{clmBenchAvg}}:** retrieval and review run in the same loop as the model.",
		"**Overturned a negative result:** a learning rule had failed a test; after fixing the input scale the same rule went from",
		"**Caught errors in the pipeline (the same audits found a board reporting its regularizer as loss):** confirmed to four",
		"Found four audits of that boundary and recovered **54%** of that loss",
		"**Profiled and optimized it (GitPulse loads the index):** cut start-up time",
		"**Built an observability pipeline for model serving**",
		"Raised throughput 3x with a lock-free queue, beneath load of 512 calls",
		"Shipped features that those teams asked for", // "those teams" without a back-reference verb or "beneath"
		"The same study was repeated on a second machine",
		"An approach to the same problem",
	}
	for _, s := range no {
		if isContinuation(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}

// The live bank, when this Mac has it: every bullet is tested, the matches are
// logged (go test -v), and the ones the owner named must be among them while
// the known self-contained look-alikes must not.
func TestContinuationPatternsOverTheLiveBank(t *testing.T) {
	const path = "/Users/bharath/Code/career/CurriculumVitae/resume/bank.json"
	if _, err := os.Stat(path); err != nil {
		t.Skip("the live bank is not on this machine")
	}
	bank, err := kbsync.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var matched []string
	for id, b := range bank.Bullets {
		if isContinuation(b.Text) {
			matched = append(matched, id)
		}
	}
	sort.Strings(matched)
	for _, id := range matched {
		t.Logf("continuation: %s", id)
	}
	got := map[string]bool{}
	for _, id := range matched {
		got[id] = true
	}
	for _, want := range []string{
		"ai-engineer-own-the-systems-beneath-those-features",
		"ai-engineer-own-the-retrieval-and-context-layer",
		"ai-engineer-maintain-the-ingestion-retrieval-and-serving",
		"ai-engineer-maintain-the-ingestion-retrieval-and-inference-s",
		"ai-engineer-build-the-ingestion-retrieval-and-context-constr",
		"gpu-nvidia-serving",
		"gpu-performance-brought-the-same-lessons-to-nvidia",
		"gpu-kernels-brought-the-same-work-to-nvidia",
		"gpu-performance-of-that-from-smarter-gpu-synchronization",
		"finding-out-confirmed-it-with-two-independent-methods",
	} {
		if _, ok := bank.Bullets[want]; !ok {
			t.Errorf("%s is not in the bank any more; update this list", want)
		}
		if !got[want] {
			t.Errorf("%s is a continuation and is not matched", want)
		}
	}
	for _, not := range []string{
		"finding-out-overturned-a-negative-result-a-learning", "ft-sum-task-success", "nanolab-mixer-kernels",
		"why-model-caught-errors-in-the-training-pipeline", "gpu-kernels-profiled-against-the-roofline-on-a",
		"sum-agent-tools-builder", "sum-engineer-who-builds-and-grades-long-horizon", "developer-tools-devmap-gives-agents-a-code-graph",
		"gpu-decode-time-traced", "int4-llm-made-decode-faster-over-a-series", "binn-timing-readout", "lessons-that-a-check-that-could-not-run",
	} {
		if got[not] {
			t.Errorf("%s is self-contained and is matched", not)
		}
	}
}

func selWith(entry string, ids ...string) kbsync.Selection {
	return kbsync.Selection{Sections: []kbsync.SelSection{{Kind: "entries", Entries: []kbsync.SelEntry{{Entry: entry, Bullets: ids}}}}}
}

func textsOf(m map[string]string) func(string) (string, bool) {
	return func(id string) (string, bool) { t, ok := m[id]; return t, ok }
}

func TestContinuationsTakeTheirPredecessorFromTheFirstSelectionThatListsThem(t *testing.T) {
	texts := map[string]string{
		"a": "Built the serving layer", "b": "Maintained the layers beneath those features", "c": "Plain bullet",
		"d": "Brought the same lessons to NVIDIA", "x": "Another plain one",
	}
	first := selWith("job", "a", "b", "c")
	second := selWith("job", "x", "b")  // lists b again, after x: ignored, the first listing wins
	third := selWith("other", "d", "c") // d is first in its list: no predecessor
	sec := kbsync.Selection{Sections: []kbsync.SelSection{{Kind: "bullets", Bullets: []string{"c", "d"}}}}
	c := buildContinuations([]kbsync.Selection{first, second, third, sec}, textsOf(texts))
	if got := c.pred("job", "b"); got != "a" {
		t.Errorf("pred(job, b) = %q, want a", got)
	}
	if !c.is("job", "b") || c.is("job", "a") || c.is("job", "c") {
		t.Errorf("is() wrong: %v", c)
	}
	if c.pred("other", "d") != "" || !c.is("other", "d") {
		t.Errorf("a continuation first in its list has no predecessor: %v", c["other"])
	}
	if got := c.pred("", "d"); got != "c" {
		t.Errorf("a summary continuation follows its summary neighbour: %q", got)
	}
	// An id the bank lacks is skipped, not a panic.
	_ = buildContinuations([]kbsync.Selection{selWith("job", "ghost", "b")}, textsOf(texts))
}

func TestContinuationCheck(t *testing.T) {
	c := continuations{"job": {"b": "a", "e": "b", "d": ""}}
	cases := []struct {
		name string
		ids  []string
		ok   bool
		want string
	}{
		{"alone is fine without continuations", []string{"a", "z"}, true, ""},
		{"directly after", []string{"a", "b"}, true, ""},
		{"directly after, others around", []string{"z", "a", "b", "y"}, true, ""},
		{"chain", []string{"a", "b", "e"}, true, ""},
		{"first in the list", []string{"b", "a"}, false, `must come directly after "a"`},
		{"another bullet between", []string{"a", "z", "b"}, false, `must come directly after "a"`},
		{"predecessor after it", []string{"b", "a"}, false, `"b" continues the bullet before it`},
		{"predecessor absent", []string{"z", "b"}, false, `must come directly after "a"`},
		{"chain broken", []string{"a", "b", "z", "e"}, false, `"e" continues the bullet before it`},
		{"no predecessor known", []string{"z", "d"}, false, "cannot be used"},
		{"empty list", nil, true, ""},
	}
	for _, tc := range cases {
		err := c.check("job", tc.ids)
		if (err == nil) != tc.ok || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	// Another entry is unconstrained, and a nil map is safe.
	if err := c.check("elsewhere", []string{"b"}); err != nil {
		t.Errorf("other entry: %v", err)
	}
	if err := (continuations)(nil).check("job", []string{"b"}); err != nil {
		t.Errorf("nil continuations: %v", err)
	}
}

func TestFilterListDropsContinuationsWithTheirPredecessor(t *testing.T) {
	isCont := func(id string) bool { return strings.HasPrefix(id, "c") }
	all := func(string) bool { return true }
	cases := []struct {
		name     string
		ids      []string
		keep     func(string) bool
		wantKept string
		wantCas  string
	}{
		{"coherent list is unchanged", []string{"a", "c1", "b", "c2"}, all, "a,c1,b,c2", ""},
		{"predecessor dropped", []string{"a", "c1", "b"}, func(id string) bool { return id != "a" }, "b", "c1"},
		{"chain follows", []string{"a", "c1", "c2", "b"}, func(id string) bool { return id != "a" }, "b", "c1,c2"},
		{"first in the list", []string{"c1", "a"}, all, "a", "c1"},
		{"only the continuation is ineligible", []string{"a", "c1", "b"}, func(id string) bool { return id != "c1" }, "a,b", ""},
		{"empty", nil, all, "", ""},
	}
	for _, tc := range cases {
		kept, cas := filterList(tc.ids, tc.keep, isCont)
		if strings.Join(kept, ",") != tc.wantKept || strings.Join(cas, ",") != tc.wantCas {
			t.Errorf("%s: kept %v cascaded %v", tc.name, kept, cas)
		}
	}
}

func TestChoiceWithoutCascadesToContinuations(t *testing.T) {
	isCont := func(id string) bool { return id == "c" }
	ch := choice{secs: []secChoice{{entries: []entChoice{{id: "e", bullets: []string{"a", "u", "c"}}, {id: "f", bullets: []string{"x"}}}}}}
	out, deps := ch.without(map[string]bool{"u": true}, isCont)
	if got := out.bulletIDs(); strings.Join(got, ",") != "a,x" || strings.Join(deps, ",") != "c" {
		t.Errorf("bullets %v dependents %v", got, deps)
	}
	// An entry left empty is removed.
	out, _ = ch.without(map[string]bool{"x": true}, isCont)
	if len(out.secs[0].entries) != 1 {
		t.Errorf("entries = %+v", out.secs[0].entries)
	}
}
