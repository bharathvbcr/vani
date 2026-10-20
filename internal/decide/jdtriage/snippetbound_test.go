package jdtriage

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/inbox"
)

// The extension sends a scraped posting's role text in the inbox `snippet`, up to inbox.MaxSnippet bytes.
// This pins that the largest snippet the inbox accepts reaches triage whole: it becomes
// Posting.Description unchanged, and the rendered context holds it without a cut even when the title,
// location and target summary are at their own bounds.
func TestLargestInboxSnippetReachesTriageWhole(t *testing.T) {
	// Two-byte letters, so a cut at a byte count that is not a rune boundary would show.
	snippet := strings.Repeat("é", inbox.MaxSnippet/2)
	if len(snippet) != inbox.MaxSnippet {
		t.Fatalf("test snippet is %d bytes, want %d", len(snippet), inbox.MaxSnippet)
	}
	req := inbox.Request{Jobs: []inbox.Job{{
		Title: strings.Repeat("t", inbox.MaxTitle), Location: strings.Repeat("l", inbox.MaxLocation),
		URL: "https://careers.acme.com/jobs/1", Snippet: snippet,
	}}}
	jobs, _, rejected := req.Validate()
	if len(jobs) != 1 || len(rejected) != 0 {
		t.Fatalf("the largest snippet was refused: %d valid, %+v", len(jobs), rejected)
	}
	ps := inbox.Batch{Token: "careers-acme-com", Company: "Acme", Jobs: jobs}.Postings()
	if len(ps) != 1 || ps[0].Description != snippet {
		t.Fatalf("Description is not the snippet as sent (%d postings)", len(ps))
	}

	summary, err := NormalizeSummary(strings.Repeat("s", MaxTargetSummaryBytes))
	if err != nil {
		t.Fatal(err)
	}
	out, truncated := Render(ps[0].Title, ps[0].Location, ps[0].Description, summary)
	if truncated {
		t.Errorf("a %d-byte description with every other field at its bound was cut (context %d bytes, cap %d)", len(snippet), len(out), MaxContextBytes)
	}
	if !strings.Contains(string(out), snippet) {
		t.Error("the rendered context does not hold the whole description")
	}
}
