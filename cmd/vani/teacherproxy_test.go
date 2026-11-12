package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// tpProxy is the teacher_proxy part of `vani relevance --json`.
type tpProxy struct {
	Available bool   `json:"available"`
	Status    string `json:"status"`
	Rows      []struct {
		Class string `json:"class"`
		Read  int    `json:"read"`
	} `json:"rows"`
}

// tpSetup is ltSetup's config and postings plus one model drop the owner
// judged, so relevance reads the ledger and builds the teacher proxy.
func tpSetup(t *testing.T) string {
	t.Helper()
	state := ltSetup(t)
	seedMore(t, state, "gh:dr", 1, posting.Dropped, map[string]string{
		"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain",
		"triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra",
	})
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(eval.Entry{Key: "gh:dr:000", Labels: map[string]string{eval.WouldApply: "no"}, Source: eval.SourceManual}); err != nil {
		t.Fatal(err)
	}
	return state
}

func tpRelevance(t *testing.T) (tpProxy, string, string) {
	t.Helper()
	o, e := mustCode(t, 0, "relevance", "--json")
	var r struct {
		TeacherProxy *tpProxy `json:"teacher_proxy"`
	}
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("%v:\n%s", err, o)
	}
	if r.TeacherProxy == nil {
		t.Fatalf("no teacher_proxy:\n%s", o)
	}
	txt, _ := mustCode(t, 0, "relevance")
	return *r.TeacherProxy, txt, e
}

// A teacher log that exists but holds no reading under the current question
// (the target summary or the model changed since it was written) is not a
// proxy that read nothing: it is unavailable, and says why. Before, it came out
// available with read 0 in every row, a table that looks like "the teacher saw
// these drops and found nothing".
func TestRelevanceTeacherProxyWithNoCurrentReadingIsUnavailable(t *testing.T) {
	state := tpSetup(t)
	ltStore(t, state, ltRow("gh:dr:000", eval.PromptHash("an older target summary"), "strong_fit"))
	p, txt, _ := tpRelevance(t)
	if p.Available || p.Status != eval.TeacherProxyNoCurrent {
		t.Errorf("stale teacher log: %+v; want unavailable, status %q", p, eval.TeacherProxyNoCurrent)
	}
	if len(p.Rows) != 0 {
		t.Errorf("stale teacher log still printed rows: %+v", p.Rows)
	}
	if !strings.Contains(txt, "question or model changed") {
		t.Errorf("text does not say why:\n%s", txt)
	}
}

// Each way of having no usable teacher is named apart, and a current log is
// available with its reading counted.
func TestRelevanceTeacherProxyNamesEachUnavailableCause(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		tpSetup(t)
		if p, _, _ := tpRelevance(t); p.Available || p.Status != eval.TeacherProxyMissing {
			t.Errorf("no log: %+v", p)
		}
	})
	t.Run("empty", func(t *testing.T) {
		state := tpSetup(t)
		if err := os.WriteFile(filepath.Join(state, eval.TeacherFile), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if p, _, _ := tpRelevance(t); p.Available || p.Status != eval.TeacherProxyEmpty {
			t.Errorf("empty log: %+v", p)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		state := tpSetup(t)
		// A directory where the log should be: open fails, which is not "missing".
		if err := os.Mkdir(filepath.Join(state, eval.TeacherFile), 0o700); err != nil {
			t.Fatal(err)
		}
		p, txt, e := tpRelevance(t)
		if p.Available || p.Status != eval.TeacherProxyUnreadable {
			t.Errorf("unreadable log: %+v", p)
		}
		if strings.Contains(e, "queueing") || !strings.Contains(txt, "unreadable") {
			t.Errorf("unreadable log: stderr %q, text:\n%s", e, txt)
		}
	})
	t.Run("current", func(t *testing.T) {
		state := tpSetup(t)
		ltStore(t, state, ltRow("gh:dr:000", eval.PromptHash(ltSummary), "strong_fit"))
		p, _, _ := tpRelevance(t)
		if !p.Available || p.Status != eval.TeacherProxyAvailable {
			t.Fatalf("current log: %+v", p)
		}
		read := 0
		for _, r := range p.Rows {
			read += r.Read
		}
		if read != 1 {
			t.Errorf("current log read %d drops, want 1: %+v", read, p.Rows)
		}
	})
}
