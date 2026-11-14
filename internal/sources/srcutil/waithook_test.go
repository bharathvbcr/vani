package srcutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeM func() int

func (f fakeM) Run() int { return f() }

func TestRunWithoutWaitsAnswersAtOnceAndRestores(t *testing.T) {
	var before []time.Duration
	t.Cleanup(SetWaitForTest(func(ctx context.Context, d time.Duration) error {
		before = append(before, d)
		return ctx.Err()
	}))
	code := RunWithoutWaits(fakeM(func() int {
		start := time.Now()
		if err := sleep(context.Background(), time.Hour); err != nil {
			t.Errorf("wait: %v", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("an hour's wait took %v", took)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
			t.Errorf("a done ctx waited without its error: %v", err)
		}
		return 7
	}))
	if code != 7 {
		t.Errorf("code %d, want the run's 7", code)
	}
	if len(before) != 0 {
		t.Errorf("the run reached the earlier wait: %v", before)
	}
	_ = sleep(context.Background(), time.Minute)
	if len(before) != 1 || before[0] != time.Minute {
		t.Errorf("the earlier wait was not put back: %v", before)
	}
}
