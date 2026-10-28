package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

const (
	ashbyAPI  = "https://api.ashbyhq.com/posting-api/job-board/acme?includeCompensation=true"
	ashbyForm = "https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiJobPosting"
)

// The run reads Ashby through the form-reading adapter, wired to the run's
// ledger (a stored form is reused while the text is unchanged) and the drop
// policy (a posting it drops is never asked), and reports counts only.
func TestAshbySourceIsWiredToTheLedgerAndThePolicy(t *testing.T) {
	l, err := ledger.Open(filepath.Join(t.TempDir(), "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	stored := posting.Posting{Key: "ab:acme:a", ATS: posting.Ashby, Company: "Acme", Title: "T a", URL: "https://x.test/a",
		Description: "text of a", Form: &posting.ApplyForm{V: posting.ApplyFormVersion, Questions: 9, Required: 2}}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: stored.Key, Status: posting.Live, Posting: &stored}); err != nil {
		t.Fatal(err)
	}

	var asked []string
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case ashbyAPI:
			var js []string
			for _, id := range []string{"a", "b", "c"} {
				js = append(js, fmt.Sprintf(`{"id":%q,"title":"T %s","jobUrl":"https://x.test/%s","isListed":true,"descriptionPlain":"text of %s"}`, id, id, id, id))
			}
			return srctest.Reply{Status: 200, Body: []byte(`{"jobs":[` + strings.Join(js, ",") + `]}`)}, true
		case ashbyForm:
			var q struct {
				Variables struct {
					ID string `json:"jobPostingId"`
				} `json:"variables"`
			}
			if json.Unmarshal(r.Body, &q) != nil {
				return srctest.Reply{}, false
			}
			asked = append(asked, q.Variables.ID)
			return srctest.Reply{Status: 200, Body: []byte(`{"data":{"jobPosting":{"id":"` + q.Variables.ID + `","applicationForm":{"sections":[{"fieldEntries":[` +
				`{"isRequired":true,"field":{"path":"q","title":"Do you require visa sponsorship at Acme Secret?","type":"Boolean"}}]}]}}}}`)}, true
		}
		return srctest.Reply{}, false
	})

	var warn bytes.Buffer
	w := &wired{led: l, warn: &warn}
	policy := pipeline.DropPolicy{Exclude: func(p posting.Posting) (string, bool) { return "excluded", p.Title == "T c" }}
	src, err := w.ashbySource(d, policy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.List(context.Background(), sources.Board{ATS: posting.Ashby, Token: "acme", Company: "Acme"})
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Form == nil || got[0].Form.Questions != 9 {
		t.Fatalf("the stored form was not reused: %+v", got[0].Form)
	}
	if got[1].Form == nil || len(got[1].Form.Gates) != 1 || got[1].Form.Gates[0].Kind != posting.GateSponsorship {
		t.Fatalf("b: %+v", got[1].Form)
	}
	if got[2].Form != nil {
		t.Fatalf("a posting the policy drops was read: %+v", got[2].Form)
	}
	if fmt.Sprint(asked) != "[b]" {
		t.Fatalf("asked %v", asked)
	}
	out := warn.String()
	if !strings.Contains(out, "board ab/acme: forms {Reused:1 Fetched:1 Unread:0 Prefiltered:1") {
		t.Fatalf("no form report: %q", out)
	}
	if strings.Contains(out, "Acme Secret") || strings.Contains(out, "sponsorship") {
		t.Fatalf("the report carries question text: %q", out)
	}
}
