package joburl

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The extension reduces URLs with the same rules (extension/src/applied.ts);
// both sides replay this file.
func TestNormalizeVectors(t *testing.T) {
	raw, err := os.ReadFile("../../extension/test/vectors/joburl.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Cases []struct {
			In  string  `json:"in"`
			Out *string `json:"out"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || len(v.Cases) < 20 {
		t.Fatalf("vectors: %d cases, %v", len(v.Cases), err)
	}
	for _, c := range v.Cases {
		got, err := Normalize(c.In)
		switch {
		case c.Out == nil && err == nil:
			t.Errorf("Normalize(%q) = %q, want a refusal", c.In, got)
		case c.Out == nil && !errors.Is(err, ErrInvalid):
			t.Errorf("Normalize(%q) refusal %v does not wrap ErrInvalid", c.In, err)
		case c.Out != nil && (err != nil || got != *c.Out):
			t.Errorf("Normalize(%q) = %q, %v; want %q", c.In, got, err, *c.Out)
		}
	}
}

func TestNormalizeBounds(t *testing.T) {
	base := "https://example.com/"
	if _, err := Normalize(base + strings.Repeat("a", MaxBytes-len(base))); err != nil {
		t.Fatalf("a URL of exactly %d bytes is accepted: %v", MaxBytes, err)
	}
	if _, err := Normalize(base + strings.Repeat("a", MaxBytes-len(base)+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one byte over is refused: %v", err)
	}
	// The cap applies to the raw text, so a long query cannot hide behind being dropped.
	if _, err := Normalize(base + "?" + strings.Repeat("q", MaxBytes)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an oversized query is refused: %v", err)
	}
	if _, err := Normalize("https://example.com/\xff\xfe"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 is refused: %v", err)
	}
}

// extension/src/applied.ts keeps the same list; extension/test/applied.test.ts pins it too.
func TestIDParamsMatchTheExtension(t *testing.T) {
	if got := strings.Join(IDParams, ","); got != "gh_jid,jid,job_id,jobid,req_id" {
		t.Fatalf("IDParams = %s", got)
	}
}

func TestApplyBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://jobs.lever.co/acme/uuid-1/apply", "https://jobs.lever.co/acme/uuid-1"},
		{"https://jobs.ashbyhq.com/acme/uuid-2/application", "https://jobs.ashbyhq.com/acme/uuid-2"},
		{"https://acme.wd5.myworkdayjobs.com/en-US/careers/job/Austin/Engineer_JR1/apply/applyManually", "https://acme.wd5.myworkdayjobs.com/en-US/careers/job/Austin/Engineer_JR1"},
		{"https://acme.example/jobs/Apply/start", "https://acme.example/jobs"},
		{"https://acme.example/careers/open?gh_jid=5", ""}, // no apply segment
		{"https://acme.example/apply", ""},      // nothing would remain
		{"https://acme.example/applynow/1", ""}, // a segment, not a prefix
		{"https://acme.example/jobs/1/application?gh_jid=5", "https://acme.example/jobs/1?gh_jid=5"},
		{"not a url", ""},
	}
	for _, c := range cases {
		got, ok := ApplyBase(c.in)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("ApplyBase(%q) = %q, %v; want %q", c.in, got, ok, c.want)
		}
	}
}

func TestNormalizeIPv6(t *testing.T) {
	got, err := Normalize("http://[::1]:8080/jobs/1?x=1")
	if err != nil || got != "http://[::1]:8080/jobs/1" {
		t.Fatalf("got %q, %v", got, err)
	}
}
