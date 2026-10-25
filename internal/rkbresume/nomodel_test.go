package rkbresume

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
)

func wantNoModel(t *testing.T, f *fixture, res Result, err error, rkbSteps, turns int, complete bool) {
	t.Helper()
	e := wantCode(t, err, CodeNoModel)
	if len(res.PDF) != 0 {
		t.Errorf("no_model returned a page")
	}
	if len(f.rkb.steps) != rkbSteps {
		t.Errorf("rkb ran %d steps %v, want %d", len(f.rkb.steps), f.rkb.steps, rkbSteps)
	}
	if f.rkb.count("render") != 0 || f.rkb.count("fit") != 0 {
		t.Errorf("a base was built although no_model: %v", f.rkb.steps)
	}
	if res.Cost.Turns != turns || res.Cost.Complete != complete {
		t.Errorf("cost = %+v, want %d turns, complete=%v", res.Cost, turns, complete)
	}
	if strings.Contains(e.Msg, ownerName) {
		t.Errorf("message holds personal data: %q", e.Msg)
	}
}

// No provider and no AllowBase: refused before anything runs, nothing spent.
func TestNoProviderIsNoModelBeforeAnythingRuns(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel, f.cfg.AllowBase = nil, "the model provider is not available", false
	f.cfg.LockDir = t.TempDir()
	res, err := f.build()
	wantNoModel(t, f, res, err, 0, 0, true)
	if e := err.(*Error); !strings.Contains(e.Msg, "the model provider is not available") {
		t.Errorf("the reason is lost: %q", e.Msg)
	}
	// The build lock was never taken: a second build is not busy.
	f.cfg.Provider, f.cfg.AllowBase = nil, true
	if _, err := f.build(); err != nil {
		t.Errorf("a refused build left the lock held: %v", err)
	}
}

// With AllowBase the same config builds the base and says why.
func TestAllowBaseBuildsTheBaseAndSaysWhy(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel, f.cfg.AllowBase = nil, "no llm provider is configured", true
	res := mustBuild(t, f)
	if res.Source != SourceBase || res.Reason != "no llm provider is configured" {
		t.Fatalf("result = %+v", res)
	}
}

// A budget that refuses the turn, a model that cannot be reached, one that does
// not answer in time and a build with no time left for a turn are each the model
// not running: no_model, with the turns that were sent counted.
func TestAModelThatCannotRunIsNoModelNotASilentBase(t *testing.T) {
	t.Run("budget spent", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.AllowBase = false
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{err: fmt.Errorf("reserve: %w", budget.ErrExhausted)}}}
		res, err := f.build()
		wantNoModel(t, f, res, err, 2, 0, true) // the claims export and the probe export only
	})
	t.Run("unreachable", func(t *testing.T) {
		f := newFixture(t)
		f.cfg.AllowBase = false
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{err: errors.New("connection refused")}}}
		res, err := f.build()
		wantNoModel(t, f, res, err, 2, 1, false)
	})
	t.Run("no answer in time", func(t *testing.T) {
		shrinkTurn(t)
		f := newFixture(t)
		f.cfg.AllowBase = false
		f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{block: true}}}
		res, err := f.build()
		wantNoModel(t, f, res, err, 2, 1, false)
	})
	t.Run("no time for a turn", func(t *testing.T) {
		old := minForModel
		minForModel = time.Hour
		t.Cleanup(func() { minForModel = old })
		f := newFixture(t)
		f.cfg.AllowBase = false
		sp := &scriptedProvider{prices: testRates, calls: []call{{text: validReply()}}}
		f.cfg.Provider = sp
		res, err := f.build()
		wantNoModel(t, f, res, err, 2, 0, true)
		if sp.n != 0 {
			t.Errorf("%d turns were sent with no time for one", sp.n)
		}
	})
}

// With AllowBase the same failures build the base with their reasons.
func TestAllowBaseTurnsEachNoModelCauseIntoABaseWithItsReason(t *testing.T) {
	f := newFixture(t)
	f.cfg.AllowBase = true
	f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{err: fmt.Errorf("reserve: %w", budget.ErrExhausted)}}}
	res := mustBuild(t, f)
	if res.Source != SourceBase || !strings.Contains(res.Reason, "budget is spent") {
		t.Fatalf("result = %+v", res)
	}
}

// A model that answered but whose choice was refused twice did run, and its
// turns are paid: the base is built with the reason even without AllowBase.
func TestAModelThatAnsweredBadlyStillFallsBackToTheBase(t *testing.T) {
	f := newFixture(t)
	f.cfg.AllowBase = false
	f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{{text: "not a choice", usage: llm.Usage{InputTokens: 1000, OutputTokens: 100}}}}
	res := mustBuild(t, f)
	if res.Source != SourceBase || !strings.Contains(res.Reason, "refused") || res.Cost.Turns != 2 {
		t.Fatalf("result = %+v", res)
	}
}

// A model that answered once and then could not be reached for the retry did
// run: the base stands, not no_model.
func TestAModelThatAnsweredThenFailedTheRetryIsNotNoModel(t *testing.T) {
	f := newFixture(t)
	f.cfg.AllowBase = false
	f.cfg.Provider = &scriptedProvider{prices: testRates, calls: []call{
		{text: "not a choice", usage: llm.Usage{InputTokens: 1000, OutputTokens: 100}},
		{err: errors.New("connection reset")},
	}}
	res := mustBuild(t, f)
	if res.Source != SourceBase || res.Cost.Turns != 2 || res.Cost.Complete {
		t.Fatalf("result = %+v", res)
	}
}
