package main

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A page of an Avature portal the owner has added is that board; any other
// host or portal is still a careers page, never a guessed Avature board.
func TestDeriveBoardKnownAvaturePortal(t *testing.T) {
	av := sources.Board{ATS: posting.Avature, Token: "twosigma", Company: "Two Sigma",
		Host: "careers.twosigma.com", URL: "https://careers.twosigma.com/careers/OpenRoles"}
	wl := []sources.Board{
		{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"},
		// A hand-edited board with a URL that does not parse as a listing page
		// must not match, or panic.
		{ATS: posting.Avature, Token: "broken", Host: "careers.broken.example", URL: "not a url"},
		av,
	}
	for name, u := range map[string]string{
		"listing page":     "https://careers.twosigma.com/careers/OpenRoles/?jobRecordsPerPage=10&jobOffset=10",
		"detail page":      "https://careers.twosigma.com/careers/JobDetail/New-York-United-States-AI-Fluency-Program-Manager/14291",
		"upper-case host":  "https://CAREERS.twosigma.com/careers/OpenRoles",
		"another page":     "https://careers.twosigma.com/careers/SearchJobs",
		"fragment and utm": "https://careers.twosigma.com/careers/OpenRoles?utm_source=x#top",
	} {
		b, res := deriveBoard(u, nil, "Two Sigma", wl)
		if res.Status != "" || b.ATS != posting.Avature || b.Token != "twosigma" {
			t.Errorf("%s: board %+v result %+v, want the avature board", name, b, res)
		}
	}
	for name, u := range map[string]string{
		"other portal, same host": "https://careers.twosigma.com/events/OpenRoles",
		"unknown host":            "https://careers.acme.example/careers/OpenRoles",
		"broken board's host":     "https://careers.broken.example/careers/OpenRoles",
		"host suffix":             "https://evil.careers.twosigma.com/careers/OpenRoles",
		"host prefix":             "https://careers.twosigma.com.evil.example/careers/OpenRoles",
		"no path":                 "https://careers.twosigma.com/",
	} {
		b, res := deriveBoard(u, nil, "Co", wl)
		if b.ATS == posting.Avature || (res.Status == "" && b.ATS != posting.Page) {
			t.Errorf("%s: board %+v result %+v, want a careers page (or a refusal), never avature", name, b, res)
		}
	}
	// With no board on the list, the same page is no Avature board either.
	if b, res := deriveBoard("https://careers.twosigma.com/careers/OpenRoles", nil, "Co", nil); b.ATS == posting.Avature {
		t.Errorf("an Avature board was invented: %+v %+v", b, res)
	}
}
