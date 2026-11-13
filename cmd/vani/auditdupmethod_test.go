package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The audit's closed strata tell a wrongly folded duplicate from a wrongly
// dead posting, and one fold method from another.
func TestClosedOrDroppedSplitsDuplicatesByMethod(t *testing.T) {
	cell := func(st posting.Status, ann map[string]string) string {
		c, ok := closedOrDropped(ledger.Record{Key: "gh:x:1", Status: st, Annotations: ann}, nil)
		if !ok || !c.Closed {
			t.Fatalf("%s %v: not a closed candidate: %+v", st, ann, c)
		}
		return eval.AuditCell(c)
	}
	for _, tc := range []struct {
		st   posting.Status
		ann  map[string]string
		want string
	}{
		{posting.Dead, nil, "closed:dead"},
		{posting.Dead, map[string]string{pipeline.AnnDupMethod: "shingle"}, "closed:dead"},
		{posting.Duplicate, map[string]string{pipeline.AnnDupMethod: pipeline.DupMethodShingle}, "closed:duplicate:shingle"},
		{posting.Duplicate, nil, "closed:duplicate:other"},
		{posting.Duplicate, map[string]string{pipeline.DuplicateOfKey: "gh:y:1"}, "closed:duplicate:other"},
		{posting.Duplicate, map[string]string{pipeline.AnnDupMethod: ""}, "closed:duplicate:other"},
		{posting.Duplicate, map[string]string{pipeline.AnnDupMethod: "Weird Method!:x"}, "closed:duplicate:weirdmethod:x"},
	} {
		if got := cell(tc.st, tc.ann); got != tc.want {
			t.Errorf("%s %v -> %q, want %q", tc.st, tc.ann, got, tc.want)
		}
	}
	if c, ok := closedOrDropped(ledger.Record{Key: "gh:x:2", Status: posting.Digested}, nil); ok {
		t.Errorf("a kept posting is a candidate: %+v", c)
	}
}
