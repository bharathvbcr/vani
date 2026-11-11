package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Phase 0 of docs/JOBS_READ_MODEL_PLAN.md: what each piece of one `jobs list`
// costs on a real ledger, so the plan's per-request budget is measured, not
// assumed. It reads a state dir read-only (the same lock-free ReadSnapshot
// `jobs list` uses) and prints timings only, never a posting or a mark.
//
// It is skipped unless VANI_BENCH_STATE names a state dir (and, for the
// config-dependent parts, VANI_BENCH_CONFIG a config file):
//
//	VANI_NO_KEYCHAIN=1 VANI_BENCH_STATE=<state> VANI_BENCH_CONFIG=<config.json> \
//	  go -C <repo> test -run '^$' -bench BenchmarkLiveReadModel -benchmem -benchtime=3x -count=3 ./cmd/vani/

type liveFixture struct {
	e      *env
	cfg    config.Config
	cfgErr error
	snap   ledger.Snapshot
	m      map[posting.Key]marks.Entry
	full   *insight // newInsight as `jobs list` builds it
	err    error
}

var (
	liveOnce sync.Once
	live     liveFixture
)

func loadLive(b *testing.B) *liveFixture {
	b.Helper()
	state := os.Getenv("VANI_BENCH_STATE")
	if state == "" {
		b.Skip("VANI_BENCH_STATE is not set")
	}
	liveOnce.Do(func() {
		e := &env{stateDir: state, configPath: os.Getenv("VANI_BENCH_CONFIG"), stdout: os.Stderr, stderr: os.Stderr}
		live.e = e
		live.cfg, live.cfgErr = e.loadConfig()
		live.snap, live.m, live.err = e.jobsState()
		if live.err != nil {
			return
		}
		live.full, live.err = newInsightFor(live.cfg, live.cfgErr, state, jobinsight.DefaultScoreHalfLifeDays, live.snap, live.m)
	})
	if live.err != nil {
		b.Fatal(live.err)
	}
	if live.cfgErr != nil {
		b.Logf("config not read (%v): config-dependent parts run without it", live.cfgErr)
	}
	return &live
}

func BenchmarkLiveReadModel(b *testing.B) {
	f := loadLive(b)
	hl := jobinsight.DefaultScoreHalfLifeDays
	inView := 0
	for _, r := range f.snap.Records {
		if jobview.InView("all", r, f.m[r.Key].Mark) {
			inView++
		}
	}
	b.Logf("records %d, in view all %d, marks %d", len(f.snap.Records), inView, len(f.m))

	// Today's costs, which the plan removes or caches.
	b.Run("fold_ReadSnapshot+marks+exclude", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := f.e.jobsState(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("insight_full", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := newInsightFor(f.cfg, f.cfgErr, f.e.stateDir, hl, f.snap, f.m); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("learn_tokenize_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			in := &insight{vocab: f.full.vocab}
			in.learn(f.snap, f.m, "")
		}
	})

	// What stays per request in a resident serve.
	b.Run("insight_without_learn", func(b *testing.B) {
		// Every newInsight step except tokenising: rank context, company
		// history, semantic, kbmatch, resumes, manual readings, logos. serve
		// would cache the file-backed ones, so this is an upper bound.
		b.ReportAllocs()
		for b.Loop() {
			if _, err := newInsightScoped(f.cfg, f.cfgErr, f.e.stateDir, hl, f.snap, f.m, "\x00no company"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("boilerplate_from_cached_hits", func(b *testing.B) {
		var companies []string
		var all [][]jobinsight.Hit
		for _, r := range f.snap.Records {
			if h, ok := f.full.hits[r.Key]; ok {
				companies, all = append(companies, r.Posting.Company), append(all, h)
			}
		}
		b.ReportAllocs()
		for b.Loop() {
			jobinsight.Boilerplate(companies, all)
		}
	})
	b.Run("rank_context", func(b *testing.B) {
		marked := make(map[posting.Key]marks.Mark, len(f.m))
		for k, e := range f.m {
			marked[k] = e.Mark
		}
		b.ReportAllocs()
		for b.Loop() {
			jobinsight.NewRankContext(f.snap.Records, marked, f.full.now)
		}
	})
	b.Run("same_company", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			newSameCompany(f.cfg, f.cfgErr, f.snap, f.m, f.full.now)
		}
	})
	for _, c := range []struct{ view, sort string }{{"new", "default"}, {"all", "default"}, {"all", "company"}} {
		b.Run("buildlist_"+c.view+"_"+c.sort, func(b *testing.B) {
			o := jobview.Opts{View: c.view, Sort: c.sort, HalfLife: hl}
			b.ReportAllocs()
			for b.Loop() {
				jobview.BuildList(f.snap, f.m, &o, f.full, f.full.info, listKeywords, defaultJobsLimit)
			}
		})
	}
	b.Run("json_encode_list_new", func(b *testing.B) {
		o := jobview.Opts{View: "new", Sort: "default", HalfLife: hl}
		l := jobview.BuildList(f.snap, f.m, &o, f.full, f.full.info, listKeywords, defaultJobsLimit)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := json.Marshal(l); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("marks_read", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := marks.Read(marks.Dir(f.e.stateDir)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("config_load", func(b *testing.B) {
		if f.cfgErr != nil {
			b.Skip("no config")
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := f.e.loadConfig(); err != nil {
				b.Fatal(err)
			}
		}
	})

	// A checkpoint candidate: the lean records (descriptions left in the log),
	// gob-encoded. Decode time is the cold-start load; strings are not interned.
	b.Run("checkpoint_gob_decode_lean", func(b *testing.B) {
		lean := make([]ledger.Record, len(f.snap.Records))
		copy(lean, f.snap.Records)
		for i := range lean {
			lean[i].Posting.Description = ""
		}
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(lean); err != nil {
			b.Fatal(err)
		}
		raw := buf.Bytes()
		b.ReportMetric(float64(len(raw))/(1<<20), "MiB")
		b.ReportAllocs()
		for b.Loop() {
			var out []ledger.Record
			if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&out); err != nil {
				b.Fatal(err)
			}
		}
	})
}
