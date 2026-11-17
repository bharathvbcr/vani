package page

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// w4cStops is one document per early stop of scanHTML, each with a well-formed
// link before the damage and one after it that the stop hides.
var w4cStops = map[string]string{
	"unterminated comment":    `<html><body><a href="/jobs/eng-1">x</a><!-- cut <a href="/jobs/eng-2">y</a>`,
	"unterminated tag":        `<html><body><a href="/jobs/eng-1">x</a><a `,
	"unterminated quote":      `<html><body><a href="/jobs/eng-1">x</a><a href="/jobs/eng-2`,
	"unterminated script":     `<html><body><a href="/jobs/eng-1">x</a><script>var s = 1; <a href="/jobs/eng-2">y</a>`,
	"unterminated style":      `<html><body><a href="/jobs/eng-1">x</a><style>a{}`,
	"unterminated textarea":   `<html><body><a href="/jobs/eng-1">x</a><textarea>`,
	"unterminated title":      `<html><head><title>cut</head><body><a href="/jobs/eng-1">x</a>`,
	"unterminated base tag":   `<html><head><base href="/x"`,
	"unterminated body tag":   `<html><body class="a`,
	"unterminated anchor tag": `<html><body><a href="/jobs/eng-1"`,
}

// Each early stop of the scan says why, and a complete document says nothing.
func TestW4cScanReportsEachEarlyStop(t *testing.T) {
	for name, doc := range w4cStops {
		if sc := scanHTML(doc); sc.truncated == "" {
			t.Errorf("%s: the scan stopped early and reported a complete read (%d links)", name, len(sc.links))
		}
	}
	for name, doc := range map[string]string{
		"well formed":      listing("/jobs/eng-1", "/jobs/eng-2"),
		"empty":            ``,
		"lone lt":          `< < <a href="/jobs/eng-1">x</a> <<<`,
		"closed comment":   `<!-- c --><a href="/jobs/eng-1">x</a>`,
		"closed script":    `<script>var a = "<a href='/x'>";</script><a href="/jobs/eng-1">x</a>`,
		"text after links": `<a href="/jobs/eng-1">x</a> trailing text and a stray < sign`,
	} {
		if sc := scanHTML(doc); sc.truncated != "" {
			t.Errorf("%s: a complete document reported %q", name, sc.truncated)
		}
	}
}

// A listing that stops early is a board error, never a partial link list, and no
// posting page is fetched from the prefix.
func TestW4cListingThatStopsEarlyFailsTheBoard(t *testing.T) {
	for name, doc := range w4cStops {
		t.Run(name, func(t *testing.T) {
			r := &fakeBrowser{pages: map[string]string{
				listURL:                                  doc,
				"https://careers.example.com/jobs/eng-1": ldPage("E", `{"@type":"JobPosting","title":"E"}`),
			}}
			got, err := newSrc(r).List(context.Background(), board())
			if !errors.Is(err, ErrTruncatedDocument) || got != nil {
				t.Fatalf("got %d postings, err %v; want nil and ErrTruncatedDocument", len(got), err)
			}
			if len(r.calls) != 1 {
				t.Fatalf("%d fetches (%v), want only the listing", len(r.calls), r.calls)
			}
			if !strings.Contains(err.Error(), "byte") {
				t.Fatalf("error %q does not say where the markup stopped", err)
			}
		})
	}
}

// A posting page cut off inside markup still yields its posting from what was
// read: it is one page's text, not a list that stands for a whole board.
func TestW4cTruncatedPostingPageStillBuildsItsPosting(t *testing.T) {
	r := &fakeBrowser{pages: map[string]string{
		listURL:                                  listing("/jobs/eng-1"),
		"https://careers.example.com/jobs/eng-1": `<html><head><title>Eng</title></head><body>text <!-- cut`,
	}}
	got, err := newSrc(r).List(context.Background(), board())
	if err != nil || len(got) != 1 || got[0].Title != "Eng" {
		t.Fatalf("%+v, err %v", got, err)
	}
}
