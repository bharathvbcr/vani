package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func dupRecord(status posting.Status, ann map[string]string) ledger.Record {
	return ledger.Record{Key: "gh:acme:2", Status: status, Annotations: ann,
		Posting: posting.Posting{Key: "gh:acme:2", Company: "Acme", Title: "Engineer"}}
}

func TestToJobCarriesNearDuplicateAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status posting.Status
		ann    map[string]string
		want   jobJSON
	}{
		{"duplicate", posting.Duplicate,
			map[string]string{pipeline.DuplicateOfKey: "gh:acme:1", pipeline.AnnDupMethod: "shingle", pipeline.AnnDupScore: "0.97"},
			jobJSON{DupOf: "gh:acme:1", DupScore: 0.97}},
		{"candidate", posting.Live,
			map[string]string{pipeline.AnnDupCandidateOf: "gh:acme:1", pipeline.AnnDupScore: "0.66"},
			jobJSON{DupCandidateOf: "gh:acme:1", DupScore: 0.66}},
		{"sibling", posting.Live,
			map[string]string{pipeline.AnnDupSiblingOf: "gh:acme:1", pipeline.AnnDupScore: "1.00"},
			jobJSON{DupSiblingOf: "gh:acme:1", DupScore: 1}},
		{"none", posting.Live, nil, jobJSON{}},
		{"score out of range or not a number is dropped", posting.Live,
			map[string]string{pipeline.AnnDupCandidateOf: "gh:acme:1", pipeline.AnnDupScore: "7"},
			jobJSON{DupCandidateOf: "gh:acme:1"}},
		{"garbage score", posting.Live, map[string]string{pipeline.AnnDupScore: "NaN"}, jobJSON{}},
		{"control characters in a key are cleaned", posting.Live,
			map[string]string{pipeline.AnnDupSiblingOf: "gh:acme:1\n\x1b[31m"},
			jobJSON{DupSiblingOf: "gh:acme:1 [31m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := toJob(dupRecord(tc.status, tc.ann), marks.Entry{})
			if j.DupOf != tc.want.DupOf || j.DupCandidateOf != tc.want.DupCandidateOf ||
				j.DupSiblingOf != tc.want.DupSiblingOf || j.DupScore != tc.want.DupScore {
				t.Fatalf("got dup fields %q %q %q %v, want %q %q %q %v",
					j.DupOf, j.DupCandidateOf, j.DupSiblingOf, j.DupScore,
					tc.want.DupOf, tc.want.DupCandidateOf, tc.want.DupSiblingOf, tc.want.DupScore)
			}
		})
	}
}

func TestJobJSONOmitsDuplicateFieldsWhenThereAreNone(t *testing.T) {
	b, err := json.Marshal(toJob(dupRecord(posting.Live, nil), marks.Entry{}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "dup_") {
		t.Fatalf("a plain job carries duplicate fields: %s", b)
	}
	b, err = json.Marshal(toJob(dupRecord(posting.Duplicate, map[string]string{pipeline.DuplicateOfKey: "gh:acme:1", pipeline.AnnDupScore: "0.95"}), marks.Entry{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dup_of":"gh:acme:1"`, `"dup_score":0.95`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s missing from %s", want, b)
		}
	}
}
