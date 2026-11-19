package jarvisio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func scriptJarvis(t *testing.T, body string, timeout time.Duration) Client {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "jarvis")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Client{Binary: bin, Root: t.TempDir(), Timeout: timeout}
}

const replies = "printf '%s\\n%s\\n' '{\"id\":\"h\",\"ok\":true}' '{\"id\":\"c\",\"ok\":true}'\n"

// elapsed runs one call in a goroutine and fails if it has not returned within
// limit: a hang is reported, not waited out.
func elapsed(t *testing.T, c Client, limit time.Duration) (time.Duration, error) {
	t.Helper()
	type res struct{ err error }
	ch := make(chan res, 1)
	start := time.Now()
	go func() { ch <- res{c.call(context.Background(), "list", nil, nil, "")} }()
	select {
	case r := <-ch:
		return time.Since(start), r.err
	case <-time.After(limit):
		t.Fatalf("call still running after %s", limit)
		return 0, nil
	}
}

// A grandchild that inherits stderr and outlives the child must not hold the
// call: the replies are in, the child is stopped, the call returns.
// Same, with the grandchild writing to stderr steadily.
// A child that floods stderr forever and never replies: the call times out at
// its own bound and returns.
// stderr closed at once, replies still read.
func TestAttackStderrClosedEarly(t *testing.T) {
	c := scriptJarvis(t, "exec 2>&-\n"+replies+"cat >/dev/null", 3*time.Second)
	if d, err := elapsed(t, c, 6*time.Second); err != nil {
		t.Fatalf("call = %v after %s", err, d)
	}
}

// A child that writes its replies, floods stderr and keeps running after the
// replies: the reply is in hand, the call returns without waiting out the flood.
