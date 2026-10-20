package joburl

import (
	"strings"
	"testing"
)

// The same fold as postingKey in extension/src/applied.ts (its own cases are in
// extension/test/applied.test.ts); Normalize itself stays literal.
func TestWorkdayPostingFoldsRoutes(t *testing.T) {
	const host = "https://acme.wd5.myworkdayjobs.com"
	const want = host + "/careers/job/Engineer_JR1"
	for _, in := range []string{
		host + "/careers/job/Austin/Engineer_JR1",
		host + "/en-US/careers/job/Austin/Engineer_JR1",
		host + "/careers/job/Austin-TX/Engineer_JR1",
		host + "/careers/job/Engineer_JR1",
		host + "/fr-FR/careers/details/Engineer_JR1",
		host + "/careers/details/Engineer_JR1/apply",
		host + "/en-US/careers/job/Austin/Engineer_JR1/APPLY/applyManually",
		host + "/careers/job/Austin/Engineer_JR1?jid=7",
	} {
		if got, ok := WorkdayPosting(in); !ok || got != want {
			t.Errorf("WorkdayPosting(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if got, ok := WorkdayPosting("http://acme.wd5.myworkdayjobs.com:8080/en-US/careers/job/A/B"); !ok || got != "http://acme.wd5.myworkdayjobs.com:8080/careers/job/B" {
		t.Errorf("scheme and port are kept: %q %v", got, ok)
	}
}

func TestWorkdayPostingLeavesEverythingElseAlone(t *testing.T) {
	const host = "https://acme.wd5.myworkdayjobs.com"
	for _, in := range []string{
		"",
		"not a url",
		host,
		host + "/careers",
		host + "/en-US/careers",
		host + "/careers/login",
		host + "/en-US/Jobs",
		host + "/careers/job",
		host + "/careers/job/apply",
		host + "/careers/details/apply/applyManually",
		host + "/careers/jobs/Austin/Engineer_JR1", // not /job or /details
		host + "/en-us/careers/job/Austin/Eng",     // a locale is xx-XX
		host + "/xx-XX",                            // locale only
		"https://jobs.lever.co/careers/job/Austin/Engineer_JR1",
		"https://myworkdayjobs.com/careers/job/Austin/Engineer_JR1", // no tenant host
		"https://acme.wd5.myworkdayjobs.com.evil.example/careers/job/Austin/Engineer_JR1",
		"https://evil-myworkdayjobs.com/careers/job/Austin/Engineer_JR1",
		"https://acme.wd5.myworkdaysite.com/recruiting/acme/careers/job/Austin/Engineer_JR1",
	} {
		if got, ok := WorkdayPosting(in); ok {
			t.Errorf("WorkdayPosting(%q) = %q; want no fold", in, got)
		}
	}
	// A very deep path is cut to its last segment, not a panic.
	if _, ok := WorkdayPosting(host + "/careers/job/" + strings.Repeat("a/", 5000) + "x"); !ok {
		t.Error("a deep path still folds to its last segment")
	}
}
