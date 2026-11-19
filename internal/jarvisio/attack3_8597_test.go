package jarvisio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const helloReply = "printf '%s\n' '{\"id\":\"h\",\"ok\":true}'\n"

// bigGet answers hello and then a get reply whose text is n bytes, then exits
// at once (stdin is never read), optionally with stderr output.
func bigGet(t *testing.T, n int, stderr bool) Client {
	body := helloReply +
		"printf '%s' '{\"id\":\"c\",\"ok\":true,\"result\":{\"key\":\"k\",\"value\":{\"type\":\"string\",\"text\":\"'\n" +
		fmt.Sprintf("head -c %d /dev/zero | tr '\\0' x\n", n) +
		"printf '%s\n' '\"}}}'\n"
	if stderr {
		body += "head -c 200000 /dev/zero | tr '\\0' e >&2\n"
	}
	return scriptJarvis(t, body, 10*time.Second)
}

func TestAttackLargeFinalReplyThenImmediateExit(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		for _, n := range []int{1000, 500000, maxReplyBytes - 200} {
			c := bigGet(t, n, stderr)
			for i := range 20 {
				got, err := c.Get(context.Background(), "k")
				if err != nil || len(got) != n {
					t.Fatalf("stderr=%v n=%d iter %d: len=%d err=%v", stderr, n, i, len(got), err)
				}
			}
		}
	}
}

func TestAttackGrandchildHoldsStdoutAfterReplies(t *testing.T) {
	c := scriptJarvis(t, "sleep 6 &\n"+replies, 2*time.Second)
	d, err := elapsed(t, c, 6*time.Second)
	if err != nil || d > 4*time.Second {
		t.Fatalf("call = %v after %s", err, d)
	}
}

func TestAttackCancelMidReply(t *testing.T) {
	c := scriptJarvis(t, helloReply+"printf '{\"id\":\"c\",\"ok\":tr'\nsleep 6", 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	err := c.call(ctx, "list", nil, nil, "")
	if err == nil || time.Since(start) > 4*time.Second {
		t.Fatalf("call = %v after %s", err, time.Since(start))
	}
}

func TestAttackChildClosesStdoutKeepsRunning(t *testing.T) {
	c := scriptJarvis(t, "exec >&-\nsleep 6", 10*time.Second)
	d, err := elapsed(t, c, 6*time.Second)
	if err == nil || d > 4*time.Second {
		t.Fatalf("call = %v after %s", err, d)
	}
}

func TestAttackTruncatedLargeReplyIsAnError(t *testing.T) {
	c := bigGet(t, maxReplyBytes+10, false)
	if _, err := c.Get(context.Background(), "k"); err == nil {
		t.Fatal("oversized reply read as success")
	}
}

func TestAttackRepliesThenExitWithoutReadingStdin(t *testing.T) {
	c := scriptJarvis(t, replies, 5*time.Second)
	for i := range 300 {
		if err := c.call(context.Background(), "list", nil, nil, ""); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
}

// Replies already written are used even when the request write failed; anything
// short of two complete valid replies is still "did not start" or "did not
// answer", an Unavailable error either way.
func TestWriteErrorKeepsCompleteRepliesOnly(t *testing.T) {
	cases := map[string]string{
		"no output":      "exit 0\n",
		"hello only":     helloReply,
		"garbage":        "printf 'not json\\n'\n",
		"wrong call id":  "printf '%s\\n%s\\n' '{\"id\":\"h\",\"ok\":true}' '{\"id\":\"x\",\"ok\":true}'\n",
		"truncated call": helloReply + "printf '{\"id\":\"c\",\"ok\":tr'\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := scriptJarvis(t, body, 5*time.Second)
			for i := range 20 {
				err := c.call(context.Background(), "list", nil, nil, "")
				if !Unavailable(err) {
					t.Fatalf("iter %d: err = %v, want unavailable", i, err)
				}
			}
		})
	}
}

// A refusal the child wrote before exiting unread is reported as the refusal.
func TestWriteErrorKeepsRefusalReply(t *testing.T) {
	c := scriptJarvis(t, "printf '%s\\n%s\\n' '{\"id\":\"h\",\"ok\":true}' '{\"id\":\"c\",\"ok\":false,\"error\":{\"code\":\"E_JARVIS\",\"message\":\"refused\"}}'\n", 5*time.Second)
	for i := range 50 {
		err := c.call(context.Background(), "list", nil, nil, "")
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeJarvis || e.Message != "refused" {
			t.Fatalf("iter %d: err = %v", i, err)
		}
	}
}

// The request is larger than the pipe holds and the child closes stdin without
// reading it, so the write fails with EPIPE every time, not by a race: the
// replies the child wrote are still used.
func TestWriteErrorFromLargeRequestKeepsReplies(t *testing.T) {
	c := scriptJarvis(t, "exec <&-\n"+helloReply+"printf '%s\\n' '{\"id\":\"c\",\"ok\":true,\"result\":{\"stored\":false}}'\n", 10*time.Second)
	big := strings.Repeat("x", 4<<20)
	if err := c.Put(context.Background(), "k", big); err == nil {
		t.Fatal("put without a stored reply read as success")
	} else if !strings.Contains(err.Error(), "did not store") {
		t.Fatalf("err = %v, want the child's reply used", err)
	}
	c = scriptJarvis(t, "exec <&-\nexit 0\n", 10*time.Second)
	err := c.Put(context.Background(), "k", big)
	if !Unavailable(err) || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("err = %v, want unavailable: did not start", err)
	}
}
