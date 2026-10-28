package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// panic(ErrFatal) is a failed serve, not a fatal exit: the cursor advances
// and a later slot still runs. Blank panic text stays the existing line.
func TestAdvPanicErrFatalPanicDoesNotStopTheSchedule(t *testing.T) {
	logs := captureLogs(t)

	fatal := runJob(context.Background(), func(context.Context) error { panic(ErrFatal) })
	if fatal == nil || errors.Is(fatal, ErrFatal) {
		t.Fatalf("panic(ErrFatal) returned %v, want a non-fatal error", fatal)
	}
	if !logs.has("job panicked:") || !logs.has("job failed:") || logs.has("job failed, exiting") {
		t.Fatalf("scheduler log: %v", logs.lines)
	}

	for _, value := range []any{"", "   ", "\n\x01\t", "\r\n \x7f"} {
		err := runJob(context.Background(), func(context.Context) error { panic(value) })
		if err == nil || errors.Is(err, ErrFatal) || !strings.Contains(err.Error(), "unknown panic") {
			t.Fatalf("panic(%q) returned %v, want unknown panic and not ErrFatal", value, err)
		}
	}
	if logs.has("job failed, exiting") {
		t.Fatalf("blank panic logged as a fatal exit: %v", logs.lines)
	}
	assertPanicHeads(t, logs)

	huge := "kaboom\n\x01" + strings.Repeat("あ", 400) + "TAILMARKER"
	hugeErr := runJob(context.Background(), func(context.Context) error { panic(huge) })
	if hugeErr == nil || errors.Is(hugeErr, ErrFatal) || strings.Contains(hugeErr.Error(), "TAILMARKER") || strings.ContainsAny(hugeErr.Error(), "\n\x01") {
		t.Fatalf("long panic returned %v", hugeErr)
	}
	if utf8.RuneCountInString(hugeErr.Error()) > len("job panicked: ")+maxJobPanicText {
		t.Fatalf("long panic error is %d runes: %q", utf8.RuneCountInString(hugeErr.Error()), hugeErr.Error())
	}

	loc := la(t)
	var n atomic.Int32
	h := start(t, time.Date(2026, 10, 18, 7, 0, 0, 0, loc), func(context.Context, *harness) error {
		if n.Add(1) == 1 {
			panic(ErrFatal)
		}
		return nil
	})
	h.clock.Advance(time.Date(2026, 10, 18, 8, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	h.clock.Advance(time.Date(2026, 10, 19, 8, 30, 0, 0, loc))
	h.waitRan()
	h.clock.waitReg(t)
	if got := h.runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2 after panic(ErrFatal)", got)
	}
	if logs.has("job failed, exiting") || !logs.has("job failed:") {
		t.Fatalf("scheduler log: %v", logs.lines)
	}
	h.stop()
}

func assertPanicHeads(t *testing.T, logs *logSink) {
	t.Helper()
	logs.mu.Lock()
	defer logs.mu.Unlock()
	var heads int
	for _, l := range logs.lines {
		if !strings.Contains(l, "job panicked:") {
			continue
		}
		head, _, _ := strings.Cut(l, "\n")
		const prefix = "daemon: job panicked: "
		if !strings.HasPrefix(head, prefix) {
			continue
		}
		heads++
		value := strings.TrimPrefix(head, prefix)
		if strings.ContainsAny(value, "\n\x01\x7f") || utf8.RuneCountInString(value) > maxJobPanicText {
			t.Fatalf("panic value %q", value)
		}
		if strings.TrimSpace(value) == "" {
			t.Fatalf("empty panic value in %q", head)
		}
	}
	if heads < 1 {
		t.Fatal("no job panicked log")
	}
}
