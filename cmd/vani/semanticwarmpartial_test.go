package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

const failMarker = "ZZFAILZZ"

// failingPostingEmbedder fails (with an error that is not "unavailable") any
// embed call carrying failMarker, so one batch of postings fails while the
// profile and the calibration anchors embed normally.
type failingPostingEmbedder struct{ semantic.Embedder }

func (f failingPostingEmbedder) Embed(ctx context.Context, texts []string, k semantic.Kind) ([][]float32, error) {
	for _, s := range texts {
		if strings.Contains(s, failMarker) {
			return nil, errors.New("embed batch refused")
		}
	}
	return f.Embedder.Embed(ctx, texts, k)
}

func seedPartialWarm(t *testing.T) {
	t.Helper()
	_, state := semanticSetup(t)
	old := openEmbedder
	openEmbedder = func(ctx context.Context, cfg config.Config) (semantic.Embedder, func() error, error) {
		e, c, err := old(ctx, cfg)
		return failingPostingEmbedder{e}, c, err
	}
	t.Cleanup(func() { openEmbedder = old })
	seedInsight(t, state,
		insightSeed{key: "gh:a:1-bad", company: "Acme", title: "Infrastructure Engineer", status: posting.Explained,
			desc: "Run k8s clusters and golang services " + failMarker, posted: daysAgo(3), seenAgo: 3600e9},
	)
}

// A warm in which a posting failed to embed is partial: it exits non-zero, and
// says how many failed, even though no Note was set.
func TestSemanticWarmPartialExitsNonZeroText(t *testing.T) {
	seedPartialWarm(t)
	code, out, errOut := do(t, "semantic", "warm")
	if code == 0 || code == 2 {
		t.Fatalf("exit %d, want a runtime failure\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, "failed 1") || !strings.Contains(errOut, "incomplete") {
		t.Errorf("stdout %q stderr %q: want the counts and an incomplete error", out, errOut)
	}
}

func TestSemanticWarmPartialExitsNonZeroJSON(t *testing.T) {
	seedPartialWarm(t)
	code, out, errOut := do(t, "semantic", "warm", "--json")
	if code == 0 || code == 2 {
		t.Fatalf("exit %d, want a runtime failure\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	var v struct {
		Report struct {
			Failed int `json:"failed"`
		} `json:"report"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out)
	}
	if v.Report.Failed != 1 || !strings.Contains(v.Error, "incomplete") {
		t.Errorf("reply %+v", v)
	}
}

// Postings left over because of the owner's --max are not a shortfall.
func TestSemanticWarmLimitSkipsStillExitZero(t *testing.T) {
	_, state := semanticSetup(t)
	seedInsight(t, state,
		insightSeed{key: "gh:a:1-a", company: "Acme", title: "Infrastructure Engineer", status: posting.Explained,
			desc: "Run k8s clusters in production", posted: daysAgo(3), seenAgo: 3600e9},
		insightSeed{key: "gh:a:2-b", company: "Acme", title: "Platform Engineer", status: posting.Explained,
			desc: "Run golang services in production", posted: daysAgo(3), seenAgo: 3600e9},
	)
	out, _ := mustCode(t, 0, "semantic", "warm", "--max", "1")
	if !strings.Contains(out, "scored 1") || !strings.Contains(out, "skipped 1") {
		t.Errorf("output %q", out)
	}
}

func TestWarmShortfall(t *testing.T) {
	for _, tc := range []struct {
		rep  semanticReport
		want string
	}{
		{semanticReport{Scored: 3, Skipped: 5}, ""},
		{semanticReport{Note: "semantic matching stopped early: x", Failed: 2}, "stopped early"},
		{semanticReport{Failed: 1}, "1 posting(s) failed"},
	} {
		err := warmShortfall(tc.rep)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%+v: err %v, want %q", tc.rep, err, tc.want)
		}
	}
}
