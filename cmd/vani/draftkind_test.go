package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/draft"
)

// `vani draft` says what an ungrounded refusal found unbacked, as a field the
// extension and the Mac app word for the person, beside the unchanged code and
// message.
func TestDraftRefusalCarriesWhatWasUnbacked(t *testing.T) {
	for _, tc := range []struct{ name, reply, kind string }{
		{"figure", "I cut latency by 73% at Acme.", draft.KindFigure},
		{"years", "I have 12 years of experience.", draft.KindYears},
		{"credential", "I hold a PhD.", draft.KindCredential},
		{"acronym", "I shipped it on GKE.", draft.KindAcronym},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draftSetup(t, goodDraftReq, &draftLLM{text: tc.reply}, false)
			stdout, _ := mustCode(t, 1, "draft")
			r := draftOut(t, stdout)
			if r.Code != draft.CodeUngrounded || r.Kind != tc.kind || r.Draft != "" || !strings.Contains(r.Error, "not in") {
				t.Errorf("reply = %+v, want code %s kind %s", r, draft.CodeUngrounded, tc.kind)
			}
		})
	}
}

// Only an ungrounded refusal has a kind: every other reply is as it was.
func TestOtherDraftRepliesHaveNoKind(t *testing.T) {
	draftSetup(t, goodDraftReq, &draftLLM{text: "A plain draft."}, false)
	stdout, _ := mustCode(t, 0, "draft")
	if strings.Contains(stdout, `"kind"`) {
		t.Errorf("a draft carries a kind: %q", stdout)
	}
	draftSetup(t, goodDraftReq, &draftLLM{err: errors.New("503")}, false)
	stdout, _ = mustCode(t, 1, "draft")
	if r := draftOut(t, stdout); r.Code != draft.CodeProvider || r.Kind != "" || strings.Contains(stdout, `"kind"`) {
		t.Errorf("provider failure = %q", stdout)
	}
}
