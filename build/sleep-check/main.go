// Owner-run hardware check (build/ is ignored; not shipped). It drives the REAL
// daemon.Run with daemon.SystemClock{} against a schedule a few minutes ahead
// and reports whether the run fired promptly after the Mac slept across that
// minute. It touches no config, state directory, ledger or network.
//
//	go -C /Users/bharath/Code/apps/Vani run ./build/sleep-check -in 3m
//
// Exit status: 0 PASS, 1 FAIL, 3 INCONCLUSIVE, 2 usage error or interrupted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/daemon"
)

const (
	minIn          = time.Minute
	maxIn          = 12 * time.Hour
	sampleEvery    = time.Second
	sleepThreshold = 3 * time.Second // wall minus monotonic delta that counts as a sleep
	stamp          = "15:04:05.000"
)

// sampler watches for machine sleep. time.Now carries a monotonic reading that
// does not advance while the Mac sleeps on darwin, so the part of a wall-clock
// delta that the monotonic delta lacks is time spent asleep.
type sampler struct {
	mu    sync.Mutex
	prev  time.Time
	spans []sleepSpan
	slept time.Duration
}

func newSampler() *sampler { return &sampler{prev: time.Now()} }

func (s *sampler) sample() {
	cur := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	mono := cur.Sub(s.prev) // monotonic: excludes sleep
	wall := cur.Round(0).Sub(s.prev.Round(0))
	if gap := wall - mono; gap > sleepThreshold {
		s.spans = append(s.spans, sleepSpan{Start: s.prev.Round(0), Wake: cur.Round(0)})
		s.slept += gap
	}
	s.prev = cur
}

func (s *sampler) run(ctx context.Context) {
	t := time.NewTicker(sampleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sample()
		}
	}
}

func (s *sampler) snapshot() ([]sleepSpan, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sleepSpan(nil), s.spans...), s.slept
}

// localZone names the machine's IANA zone; config.Schedule rejects "Local".
func localZone() (string, error) {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz, nil
		}
	}
	link, err := os.Readlink("/etc/localtime")
	if err != nil {
		return "", fmt.Errorf("cannot determine the local IANA zone (set TZ, e.g. TZ=America/Chicago): %w", err)
	}
	const marker = "zoneinfo/"
	i := strings.LastIndex(link, marker)
	if i < 0 {
		return "", fmt.Errorf("cannot read an IANA zone from /etc/localtime -> %s (set TZ)", link)
	}
	name := link[i+len(marker):]
	if _, err := time.LoadLocation(name); err != nil {
		return "", fmt.Errorf("zone %q: %w", name, err)
	}
	return name, nil
}

func main() {
	os.Exit(run())
}

func run() int {
	in := flag.Duration("in", 3*time.Minute, "how far ahead the scheduled minute is (1m to 12h)")
	flag.Parse()
	if *in < minIn || *in > maxIn {
		fmt.Fprintf(os.Stderr, "-in must be between %s and %s\n", minIn, maxIn)
		return 2
	}
	zone, err := localZone()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	start := time.Now()
	// The first minute boundary at or after start+in, so it is never sooner than asked.
	scheduled := start.Add(*in).In(loc).Truncate(time.Minute)
	if scheduled.Before(start.Add(*in)) {
		scheduled = scheduled.Add(time.Minute)
	}
	sched := config.Schedule{DailyAt: scheduled.Format("15:04"), Timezone: zone}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var skipped bool
	daemon.Logf = func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		fmt.Printf("  [%s] %s\n", time.Now().Format(stamp), msg)
		if strings.Contains(msg, "skipping run") {
			skipped = true // Logf is called from daemon.Run's goroutine, which is this one
			cancel()
		}
	}

	var fired time.Time
	job := func(context.Context) error {
		fired = time.Now().Round(0)
		cancel() // one run is all this check needs
		return nil
	}

	fmt.Printf("sleep-check: scheduled run at %s (%s, %s ahead)\n",
		scheduled.Format("2006-01-02 15:04:05 MST"), zone, scheduled.Sub(start).Round(time.Second))
	fmt.Printf("Now run `pmset sleepnow` (or close the lid) and keep the Mac asleep until after %s, then wake it.\n",
		scheduled.Format("15:04"))
	fmt.Printf("The Mac must be asleep when %s passes; waking it before then makes the check INCONCLUSIVE.\n",
		scheduled.Format("15:04"))
	fmt.Println("Waiting (Ctrl-C to abort)...")

	smp := newSampler()
	go smp.run(ctx)
	runErr := daemon.Run(ctx, sched, job, daemon.SystemClock{})
	smp.sample() // catch a wake the ticker had not yet seen
	spans, slept := smp.snapshot()

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		fmt.Printf("RESULT: FAIL: daemon.Run returned an error: %v\n", runErr)
		return 1
	}
	if fired.IsZero() && !skipped {
		fmt.Println("RESULT: ABORTED: interrupted before the run fired; no verdict")
		return 2
	}

	obs := observation{Scheduled: scheduled, Fired: fired, Skipped: skipped, Spans: spans}
	kind, detail := judge(obs)

	fmt.Println()
	fmt.Printf("scheduled: %s\n", scheduled.Format(stamp))
	if fired.IsZero() {
		fmt.Println("fired:     never")
	} else {
		fmt.Printf("fired:     %s\n", fired.Format(stamp))
		fmt.Printf("lateness:  %s after the scheduled time\n", fired.Sub(scheduled).Round(time.Millisecond))
	}
	for _, s := range spans {
		fmt.Printf("slept:     from %s to %s (wall-clock jump over the monotonic clock)\n",
			s.Start.Format(stamp), s.Wake.Format(stamp))
		if !fired.IsZero() {
			fmt.Printf("lateness after wake: %s\n", max(fired.Sub(s.Wake), 0).Round(time.Millisecond))
		}
	}
	fmt.Printf("total time asleep observed: %s\n", slept.Round(time.Second))
	fmt.Printf("RESULT: %s: %s\n", kind, detail)

	switch kind {
	case outcomePass:
		return 0
	case outcomeFail:
		return 1
	default:
		return 3
	}
}
