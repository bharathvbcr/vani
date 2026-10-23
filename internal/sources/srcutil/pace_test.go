package srcutil

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPacerSpacesAllButTheFirst(t *testing.T) {
	var waits []time.Duration
	p := Pacer{Every: 5 * time.Second, Sleep: func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}}
	for i := 0; i < 4; i++ {
		if err := p.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(waits) != 3 || waits[0] != 5*time.Second || waits[2] != 5*time.Second {
		t.Errorf("waits %v, want three of 5s", waits)
	}
}

// The wait that would pass the budget fails at once, without waiting.
func TestPacerBudget(t *testing.T) {
	var waits []time.Duration
	p := Pacer{Every: 2 * time.Second, Budget: 5 * time.Second, Sleep: func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}}
	ctx := context.Background()
	for i := 0; i < 3; i++ { // the first is free, then 2s and 4s of 5s
		if err := p.Wait(ctx); err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
	}
	err := p.Wait(ctx)
	if !errors.Is(err, ErrPaceBudget) || len(waits) != 2 {
		t.Fatalf("over budget: %v after %v", err, waits)
	}
	if !strings.Contains(err.Error(), "4 requests spaced 2s exceed 5s") {
		t.Errorf("message %q", err)
	}

	// A zero budget is Every times MaxPacedRequests.
	waits = nil
	q := Pacer{Every: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }}
	n := 0
	for ; n < MaxPacedRequests+5; n++ {
		if err := q.Wait(ctx); err != nil {
			if !errors.Is(err, ErrPaceBudget) {
				t.Fatal(err)
			}
			break
		}
	}
	if n != MaxPacedRequests+1 {
		t.Errorf("zero budget allowed %d requests, want %d", n, MaxPacedRequests+1)
	}
}

func TestPacerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var p Pacer
	if err := p.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled first wait: %v", err)
	}
	// A real Sleep ends with its context.
	p = Pacer{Every: time.Hour, sent: 1}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Errorf("wait ignored its context: %v after %s", err, time.Since(start))
	}
	// No spacing never sleeps.
	z := Pacer{Sleep: func(context.Context, time.Duration) error { t.Error("slept with Every 0"); return nil }}
	for i := 0; i < 3; i++ {
		if err := z.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := Sleep(context.Background(), -time.Second); err != nil {
		t.Errorf("negative sleep: %v", err)
	}
}

func TestTitle(t *testing.T) {
	long := strings.Repeat("é", MaxTitle) // 2 bytes a rune
	for _, c := range []struct{ name, in, want string }{
		{"amd double space", "Senior Graphics Machine Learning  Engineer", "Senior Graphics Machine Learning Engineer"},
		{"entities", "Graphics &amp; AI Processor Design Engineer", "Graphics & AI Processor Design Engineer"},
		{"tabs, newlines, no-break space", " Staff\tEngineer\n (Remote) ", "Staff Engineer (Remote)"},
		{"empty", "", ""},
		{"only space", "  \t", ""},
		{"plain stays", "Sr. Software Engineer– Kernel & Device Drivers", "Sr. Software Engineer– Kernel & Device Drivers"},
	} {
		if got := Title(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	got := Title(long)
	if len(got) > MaxTitle || !strings.HasPrefix(long, got) || len(got) < MaxTitle-1 {
		t.Errorf("long title: %d bytes", len(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("cut inside a rune")
		}
	}
}
