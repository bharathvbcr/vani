package eval

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func judged(v, src string) Entry {
	return Entry{Labels: map[string]string{WouldApply: v}, Source: src, PSelect: 0.5}
}

func agreeRow(k, model, prompt, roleFit string, at time.Time) TeacherRow {
	return TeacherRow{V: TeacherSchema, Key: posting.Key(k), TextSHA256: "t", Model: model, FixtureSHA: "f", PromptHash: prompt,
		Slots: map[string]TeacherSlot{"role_fit": {Reading: "answered", Value: roleFit}}, At: at}
}

func TestReaderAgreementCountsEachReaderAgainstTheOwner(t *testing.T) {
	labels := map[posting.Key]Entry{
		"gh:a:1": judged("yes", "decile:9"),
		"gh:a:2": judged("maybe", "decile:5"),
		"gh:a:3": judged("no", "dropped:model:role_fit"),
		"gh:a:4": judged("no", "decile:2"),
		"gh:a:5": judged("skip", "decile:1"),                            // not judged: never counted
		"gh:a:6": {Labels: map[string]string{"role_fit": "strong_fit"}}, // a slot label, not would_apply
		"gh:a:7": judged("yes", "decile:8"),                             // no reading from anyone
		"gh:a:8": judged("no", "decile:3"),                              // inherited reading: left out
	}
	ann := func(backend, roleFit, seniority string) map[string]string {
		return map[string]string{
			"triage.role_fit": roleFit, "triage.role_fit.reading": "answered", "triage.role_fit.backend": backend,
			"triage.seniority": seniority, "triage.seniority.reading": "answered", "triage.seniority.backend": backend,
		}
	}
	inherited := ann("d1-ultra", "strong_fit", "senior")
	inherited[ledger.AnnTriageInheritedFrom] = "gh:a:1"
	abstained := map[string]string{"triage.role_fit.reading": "abstained", "triage.role_fit.backend": "d1-ultra"}
	recs := []ledger.Record{
		{Key: "gh:a:1", Annotations: ann("d1-ultra", "strong_fit", "senior")},
		{Key: "gh:a:1", Annotations: ann("d1-ultra", "mismatch_domain", "senior")}, // listed twice: the first counts
		{Key: "gh:a:2", Annotations: ann("d1-ultra", "partial_fit", "mid")},
		{Key: "gh:a:3", Annotations: ann("d1-ultra", "mismatch_domain", "mid")},
		{Key: "gh:a:4", Annotations: abstained}, // asked, not answered: no reading
		{Key: "gh:a:5", Annotations: ann("d1-ultra", "strong_fit", "mid")},
		{Key: "gh:a:8", Annotations: inherited},
		{Key: "gh:z:9", Annotations: ann("d1-ultra", "strong_fit", "mid")}, // not labelled
	}
	path := filepath.Join(t.TempDir(), TeacherFile)
	store, err := OpenTeacherStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, r := range []TeacherRow{
		agreeRow("gh:a:1", "sonnet", "p1", "strong_fit", t0),
		agreeRow("gh:a:1", "sonnet", "p2", "mismatch_domain", t0.Add(time.Hour)), // newest wins
		agreeRow("gh:a:2", "sonnet", "p2", "adjacent", t0),
		agreeRow("gh:a:3", "sonnet", "p2", "mismatch_domain", t0),
		agreeRow("gh:a:4", "haiku", "p9", "strong_fit", t0),
		{V: TeacherSchema, Key: "gh:a:3", Task: "career.kb_match", TextSHA256: "t", Model: "sonnet", FixtureSHA: "f", PromptHash: "k",
			Slots: map[string]TeacherSlot{"kb_match": {Reading: "answered", Value: "strong"}}, At: t0}, // another task: ignored
	} {
		if err := store.Append(r); err != nil {
			t.Fatal(err)
		}
	}

	a := BuildReaderAgreement(labels, recs, store, TeacherProxyAvailable, Names{})
	if a.Judged != 6 || a.Yes != 2 || a.Maybe != 1 || a.No != 3 {
		t.Fatalf("judged counts: %+v", a)
	}
	byName := map[string]ReaderRow{}
	for _, r := range a.Readers {
		byName[r.Kind+"/"+r.Reader] = r
	}
	if len(byName) != 3 {
		t.Fatalf("readers: %+v", a.Readers)
	}
	eng := byName[ReaderEngine+"/d1-ultra"]
	// d1-ultra read 1 (strong), 2 (partial), 3 (mismatch); 4 abstained, 8 inherited.
	if eng.Read != 3 || eng.Sensitivity != (Count{Wanted: 2, N: 2}) || eng.Specificity != (Count{Wanted: 1, N: 1}) {
		t.Errorf("engine row: %+v", eng)
	}
	if got := cell(eng, "role_fit", "strong_fit"); got != (ReaderCell{Value: "strong_fit", Yes: 1}) {
		t.Errorf("engine strong_fit: %+v", got)
	}
	son := byName[ReaderTeacher+"/sonnet"]
	// Newest row of gh:a:1 is mismatch_domain: the owner's yes reads as a miss.
	if son.Read != 3 || son.Sensitivity != (Count{Wanted: 0, N: 2}) || son.Specificity != (Count{Wanted: 1, N: 1}) || son.Prompts != 1 {
		t.Errorf("sonnet row: %+v", son)
	}
	if got := cell(son, "role_fit", "mismatch_domain"); got != (ReaderCell{Value: "mismatch_domain", Yes: 1, No: 1}) {
		t.Errorf("sonnet mismatch: %+v", got)
	}
	if got := cell(son, "role_fit", NoReading); got != (ReaderCell{Value: NoReading, Yes: 1, No: 2}) {
		t.Errorf("sonnet no reading: %+v", got)
	}
	hai := byName[ReaderTeacher+"/haiku"]
	if hai.Read != 1 || hai.Sensitivity.N != 0 || hai.Specificity != (Count{Wanted: 0, N: 1}) {
		t.Errorf("haiku row: %+v", hai)
	}
	md := a.Markdown()
	for _, want := range []string{"## Readers against the owner", "teacher sonnet", "0/2 = 0%", "no data (n=0)", "(no reading)"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func cell(r ReaderRow, slot, value string) ReaderCell {
	for _, s := range r.Slots {
		if s.Slot != slot {
			continue
		}
		for _, c := range s.Rows {
			if c.Value == value {
				return c
			}
		}
	}
	return ReaderCell{}
}

func TestReaderAgreementWithoutTeacherSaysWhy(t *testing.T) {
	labels := map[posting.Key]Entry{"gh:a:1": judged("yes", "decile:9")}
	for _, status := range []string{TeacherProxyMissing, TeacherProxyUnreadable, TeacherProxyEmpty} {
		a := BuildReaderAgreement(labels, nil, nil, status, Names{})
		if a.TeacherStatus != status || len(a.Readers) != 0 {
			t.Errorf("%s: %+v", status, a)
		}
		if md := a.Markdown(); !strings.Contains(md, "teacher readings: "+status) || !strings.Contains(md, "no reader answered") {
			t.Errorf("%s markdown:\n%s", status, md)
		}
	}
	// "available" with a nil store is not available.
	if a := BuildReaderAgreement(labels, nil, nil, TeacherProxyAvailable, Names{}); a.TeacherStatus != TeacherProxyMissing {
		t.Errorf("nil store reported %q", a.TeacherStatus)
	}
}

func TestReaderAgreementWithNothingJudged(t *testing.T) {
	a := BuildReaderAgreement(nil, nil, nil, TeacherProxyMissing, Names{})
	if a.Judged != 0 || !strings.Contains(a.Markdown(), "no data") {
		t.Errorf("%+v\n%s", a, a.Markdown())
	}
}

func TestReaderAgreementBoundsDistinctValues(t *testing.T) {
	labels := map[posting.Key]Entry{}
	var recs []ledger.Record
	for i := range maxReaderValues + 10 {
		k := posting.Key("gh:a:" + strings.Repeat("x", i+1))
		labels[k] = judged("no", "decile:1")
		recs = append(recs, ledger.Record{Key: k, Annotations: map[string]string{
			"triage.role_fit": "v" + strings.Repeat("y", i), "triage.role_fit.reading": "answered", "triage.role_fit.backend": "b"}})
	}
	a := BuildReaderAgreement(labels, recs, nil, TeacherProxyMissing, Names{})
	var rows []ReaderCell
	for _, s := range a.Readers[0].Slots {
		if s.Slot == "role_fit" {
			rows = s.Rows
		}
	}
	total := 0
	for _, c := range rows {
		total += c.Yes + c.Maybe + c.No
	}
	if len(rows) > maxReaderValues+2 || total != maxReaderValues+10 || cell(a.Readers[0], "role_fit", OtherReading).No == 0 {
		t.Errorf("%d rows, %d counted: %+v", len(rows), total, rows)
	}
}
