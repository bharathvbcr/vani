package jarvisio

import (
	"context"
	"fmt"
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
