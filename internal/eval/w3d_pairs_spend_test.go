package eval

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/decide"
)

// A pair run whose meter cannot be read after the questions are done must say
// the spend is unknown, never report SpentNano 0 as if nothing had been spent.
func TestW3dPairRunUnreadableSpendIsUnknownNotZero(t *testing.T) {
	f := &pairFake{answer: answerWith("b"), cost: 10}
	run, s, _ := newPairRun(t, f, 1_000_000)
	var answered atomic.Int64
	inner := f.answer
	f.answer = func(n int, q decide.Question) (decide.Answer, error) {
		answered.Add(1)
		return inner(n, q)
	}
	run.Spent = func(context.Context) (int64, error) {
		if answered.Load() >= 2 { // every question is done: the read after the run fails
			return 0, errors.New("ledger vanished")
		}
		return f.meter.Load(), nil
	}
	res, err := run.Run(context.Background(), []PairWork{pairWork("k:1", "k:2"), pairWork("k:3", "k:4")})
	if err == nil || !strings.Contains(err.Error(), "spend") || !strings.Contains(err.Error(), "ledger vanished") {
		t.Fatalf("the unreadable spend did not reach the caller: %v", err)
	}
	if !res.SpentUnknown {
		t.Errorf("SpentUnknown = false with SpentNano %d: a failed read looks like a measurement", res.SpentNano)
	}
	if res.Stored != 2 || s.Len() != 2 {
		t.Errorf("stored %d (rows %d): the answers already paid for must be kept", res.Stored, s.Len())
	}
}

// A readable meter stays a measurement.
func TestW3dPairRunReadableSpendIsKnown(t *testing.T) {
	f := &pairFake{answer: answerWith("b"), cost: 10}
	run, _, _ := newPairRun(t, f, 1_000_000)
	res, err := run.Run(context.Background(), []PairWork{pairWork("k:1", "k:2")})
	if err != nil || res.SpentUnknown || res.SpentNano != 10 {
		t.Errorf("err %v unknown %v spent %d, want a measured 10", err, res.SpentUnknown, res.SpentNano)
	}
}
