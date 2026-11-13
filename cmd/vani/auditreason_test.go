package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
)

// The one-tap answer may say what kept a posting from a yes: the reason lands
// in the label beside the answer, the reply says it, and a reason on a yes, an
// unknown reason or one on a skip is refused with nothing written.
func TestAuditAnswerRecordsTheReason(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)

	o, _ := mustCode(t, 0, "--json", "audit", "answer", "--key", "gh:k:003", "--verdict", "maybe", "--reason", "level", "--source", "decile:3", "--p-select", "0.5")
	var r auditAnswerJSON
	decodeOne(t, o, &r)
	if r.Reason != "level" || r.Verdict != "maybe" {
		t.Fatalf("reply %s", o)
	}
	e := readLabels(t, state)["gh:k:003"]
	if e.Labels[eval.WouldApply] != "maybe" || e.Labels[eval.WouldApplyReason] != "level" {
		t.Fatalf("label %+v", e)
	}
	text, _ := mustCode(t, 0, "audit", "answer", "--key", "gh:k:004", "--verdict", "no", "--reason", "domain", "--source", "decile:3", "--p-select", "0.5")
	if !strings.Contains(text, "reason domain") {
		t.Errorf("text reply: %s", text)
	}

	before := labelsFileLines(t, state)
	for _, args := range [][]string{
		{"--verdict", "yes", "--reason", "level"},
		{"--verdict", "skip", "--reason", "level"},
		{"--verdict", "no", "--reason", "salary"},
		{"--verdict", "no", "--reason", "Level"},
	} {
		base := []string{"audit", "answer", "--key", "gh:k:002", "--source", "decile:3", "--p-select", "0.5"}
		mustCode(t, 2, append(base, args...)...)
	}
	if after := labelsFileLines(t, state); len(after) != len(before) {
		t.Errorf("a refused reason wrote %d line(s)", len(after)-len(before))
	}
}
