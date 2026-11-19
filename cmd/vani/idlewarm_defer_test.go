package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// scoreFileVersions counts how many times the score file was replaced as seen
// by the embedder's hook, which runs once per embed call, under the cache
// lock, before that chunk's own save: so it sees every save of the chunks
// before it.
func scoreFileVersions(f *idleFixture, versions *atomic.Int32, onChunk func(chunk int32)) {
	path := filepath.Join(semanticDir(f.state), "semantic-scores.json")
	var last os.FileInfo
	var chunks atomic.Int32
	f.emb.hook = func(_ context.Context, texts []string) ([][]float32, error, bool) {
		for _, tx := range texts {
			if !strings.Contains(tx, "k8s") {
				continue
			}
			if fi, err := os.Stat(path); err == nil && (last == nil || !os.SameFile(last, fi) || !fi.ModTime().Equal(last.ModTime())) {
				last = fi
				versions.Add(1)
			}
			if onChunk != nil {
				onChunk(chunks.Add(1))
			}
			break
		}
		return nil, nil, false
	}
}

// An idle window of many small chunks does not rewrite the score file after
// every chunk: it holds the scores until the save interval, and saves what it
// holds before the window ends, so the next look finds every score. (The file
// is about 22 MB live; one rewrite per 16 postings ran all day.)
func TestIdleWarmDefersScoreSavesAcrossChunks(t *testing.T) {
	f := newIdleFixture(t, 100)
	idleChunkDocs = 4 // 25 chunks, well under the save interval
	var versions atomic.Int32
	scoreFileVersions(f, &versions, nil)
	f.w.step(context.Background())
	if st := f.state_(t); st.Last.Chunks != 25 || st.Last.Scored != 100 {
		t.Fatalf("state %+v", st)
	}
	if v := versions.Load(); v > 1 {
		t.Errorf("the score file was replaced %d times between chunks of one window, want at most 1", v)
	}
	if got := f.scored(); got != 100 {
		t.Errorf("%d scores on disk after the window, want 100", got)
	}
}

// Whichever way a window ends early, what the finished chunks scored is on
// disk by the time the gate is released: the daemon stopping (context cancel)
// and leaving AC power.
func TestIdleWarmSavesDeferredScoresWhenItYields(t *testing.T) {
	t.Run("context cancel", func(t *testing.T) {
		f := newIdleFixture(t, 60)
		idleChunkDocs = 4
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var versions atomic.Int32
		scoreFileVersions(f, &versions, func(chunk int32) {
			if chunk == 6 {
				cancel()
			}
		})
		f.w.step(ctx)
		st := f.state_(t)
		if st.Total.Scored < 20 || st.Total.Scored >= 60 {
			t.Fatalf("the window did not stop partway: %+v", st)
		}
		if got := f.scored(); got != st.Total.Scored {
			t.Errorf("%d scores on disk, the window scored %d", got, st.Total.Scored)
		}
		if f.closes.Load() != 1 {
			t.Errorf("model closed %d times", f.closes.Load())
		}
	})
	t.Run("power leaves", func(t *testing.T) {
		f := newIdleFixture(t, 60)
		idleChunkDocs = 4
		var versions atomic.Int32
		scoreFileVersions(f, &versions, func(chunk int32) {
			if chunk == 5 {
				f.power.Store(int32(powerBattery))
			}
		})
		f.w.step(context.Background())
		st := f.state_(t)
		if st.Kind != idleBattery || st.Last.Scored != 20 {
			t.Fatalf("state %+v", st)
		}
		if got := f.scored(); got != 20 {
			t.Errorf("%d scores on disk, the window scored 20", got)
		}
		if versions.Load() > 1 {
			t.Errorf("score file replaced %d times before the yield", versions.Load())
		}
	})
}
