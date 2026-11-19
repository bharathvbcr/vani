package jarvisio

import (
	"testing"
	"time"
)

func TestAttackGrandchildHoldingStderrSilent(t *testing.T) {
	c := scriptJarvis(t, "sleep 8 >/dev/null &\n"+replies+"cat >/dev/null", 2*time.Second)
	d, err := elapsed(t, c, 6*time.Second)
	if err != nil || d > 4*time.Second {
		t.Fatalf("call = %v after %s", err, d)
	}
}

func TestAttackGrandchildHoldingStderrChatty(t *testing.T) {
	c := scriptJarvis(t, "(i=0; while [ $i -lt 80 ]; do head -c 1000 /dev/zero >&2; sleep 0.1; i=$((i+1)); done) >/dev/null &\n"+replies+"cat >/dev/null", 2*time.Second)
	d, err := elapsed(t, c, 6*time.Second)
	if err != nil || d > 4*time.Second {
		t.Fatalf("call = %v after %s", err, d)
	}
}

func TestAttackInfiniteStderrNoReply(t *testing.T) {
	c := scriptJarvis(t, "yes xxxxxxxxxxxxxxxxxxxxxxxx >&2", 1*time.Second)
	d, err := elapsed(t, c, 6*time.Second)
	if err == nil || d > 4*time.Second {
		t.Fatalf("call = %v after %s", err, d)
	}
}

func TestAttackFloodAfterRepliesThenLingers(t *testing.T) {
	c := scriptJarvis(t, replies+"yes xxxxxxxx >&2 & cat >/dev/null; wait", 3*time.Second)
	if d, err := elapsed(t, c, 8*time.Second); err != nil || d > 5*time.Second {
		t.Fatalf("call = %v after %s", err, d)
	}
}
