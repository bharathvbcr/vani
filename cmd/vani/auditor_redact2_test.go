package main

import (
	"strings"
	"testing"
)

// The auditor refuses a digest that held a term even when the term crosses the
// digest's per-bullet cut: the check reads the text before any cut, so it sees
// the whole term, where a cut copy holds only "Zephy" and would be sent stripped.
func TestAuditorRefusesATermThatCrossesTheBulletCut(t *testing.T) {
	e := auSetup(t)
	bullet := strings.Repeat("word ", 47) + "Zephyrnet engine" // the term spans byte 240
	tkBank(t, e, strings.Replace(kbmBank, "Built a request batching scheduler in Rust", bullet, 1))
	writeKBWords(t, e.state, "# test", "zephyrnet")
	f := &auFake{}
	auUse(t, f)
	code, _, stderr := do(t, "auditor", "calibrate", "--budget-usd", "1")
	if code == 0 || !strings.Contains(stderr, "the knowledge-base digest holds 1 identifying term") || !strings.Contains(stderr, "nothing was sent") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a refused run sent %d question(s)", len(f.sent))
	}
}
