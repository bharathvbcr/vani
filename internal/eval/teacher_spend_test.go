package eval

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// A run whose meter cannot be read once every question is done must say so:
// reporting a spend of 0 for a run that spent money is the silent failure the
// review found (SpentNano left at 0, no error).
func TestTeacherRunFailedSpendReadAfterTheQuestionsIsAnError(t *testing.T) {
	var meter atomic.Int64
	var answered atomic.Int64
	const n = 3
	d := &tkDecider{meter: &meter, onCall: func(int) { answered.Add(1) }}
	run, _ := tkRun(t, d, &meter, 10*tkCost, 1)
	run.Spent = func(context.Context) (int64, error) {
		if answered.Load() >= n {
			return 0, errors.New("ledger vanished")
		}
		return meter.Load(), nil
	}
	res, err := run.Run(context.Background(), tkCands(n))
	if err == nil {
		t.Fatalf("a run whose spend could not be read back returned no error (SpentNano %d)", res.SpentNano)
	}
	if !strings.Contains(err.Error(), "spend") {
		t.Errorf("the error does not say the spend could not be read: %v", err)
	}
	if res.Stored != n {
		t.Errorf("stored %d of %d: the answers already paid for must still be kept", res.Stored, n)
	}
}
