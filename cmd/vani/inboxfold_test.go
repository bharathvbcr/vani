package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/inbox"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The fold line's job buckets add up to the job count; whatever no count
// names is printed as unaccounted (negative when the counts overrun), never
// folded into another bucket, and records marked as duplicates (not jobs of
// the batch) are said apart.
func TestInboxFoldLineAddsUp(t *testing.T) {
	for name, tc := range map[string]struct {
		r    drainResult
		want string
	}{
		"every job named": {
			r:    drainResult{Report: inbox.DrainReport{Folded: 2, Jobs: 100}, New: 3, Known: 97},
			want: "inbox: folded 2 batch(es), 100 job(s): 3 new, 97 already known",
		},
		"invalid and repeated": {
			r:    drainResult{Report: inbox.DrainReport{Folded: 1, Jobs: 10}, New: 4, Known: 3, Invalid: 2, Repeated: 1},
			want: "inbox: folded 1 batch(es), 10 job(s): 4 new, 3 already known, 2 invalid, 1 repeated in a batch",
		},
		"listed and marked duplicates": {
			r:    drainResult{Report: inbox.DrainReport{Folded: 1, Jobs: 200}, New: 5, Known: 95, ListedDuplicate: 100, Duplicate: 103},
			want: "inbox: folded 1 batch(es), 200 job(s): 5 new, 95 already known, 100 duplicate of a stored job; 3 other record(s) marked as duplicates",
		},
		"a remainder": {
			r:    drainResult{Report: inbox.DrainReport{Folded: 1, Jobs: 200}, New: 5, Known: 95},
			want: "inbox: folded 1 batch(es), 200 job(s): 5 new, 95 already known, 100 unaccounted",
		},
		"counts overrun": {
			r:    drainResult{Report: inbox.DrainReport{Folded: 1, Jobs: 2}, New: 2, Known: 1},
			want: "inbox: folded 1 batch(es), 2 job(s): 2 new, 1 already known, -1 unaccounted",
		},
	} {
		if got := inboxFoldLine(tc.r); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
}

// A repeat is a valid key seen earlier in the batch; a posting with no key is
// the fold's Invalid and is never also a repeat.
func TestRepeatedKeys(t *testing.T) {
	a, _ := posting.PageKey("acme", "https://acme.com/jobs/a")
	b, _ := posting.PageKey("acme", "https://acme.com/jobs/b")
	for name, tc := range map[string]struct {
		ps   []posting.Posting
		want int
	}{
		"none":       {nil, 0},
		"distinct":   {[]posting.Posting{{Key: a}, {Key: b}}, 0},
		"one repeat": {[]posting.Posting{{Key: a}, {Key: b}, {Key: a}}, 1},
		"thrice":     {[]posting.Posting{{Key: a}, {Key: a}, {Key: a}}, 2},
		"empty keys": {[]posting.Posting{{}, {}, {Key: "noats"}}, 0},
	} {
		if got := repeatedKeys(tc.ps); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}

// drainInbox counts every folded job: new ones, ones stored as a duplicate of
// an ATS record, and ones the ledger already held (whatever the fold did to
// them), leaving none unaccounted.
func TestDrainInboxAccountsForEveryJob(t *testing.T) {
	w, _ := openWired(t, config.Default())
	gh, _ := posting.GreenhouseKey("acme", "9")
	if err := w.led.Append(context.Background(), ledger.Event{V: 1, At: time.Now(), Key: gh, Status: posting.Live, Reason: "new",
		Posting: &posting.Posting{Key: gh, ATS: posting.Greenhouse, Company: "Acme", Title: "Platform Engineer", Location: "Remote", URL: "https://boards.greenhouse.io/acme/jobs/9"}}); err != nil {
		t.Fatal(err)
	}
	protect := func(posting.Key) bool { return false }

	spoolBatch(t, w.stateDir, "careers-acme-com", "Platform Engineer", "Data Engineer", "Site Reliability Engineer")
	res, err := drainInbox(t.Context(), w.led, w.stateDir, protect)
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Jobs != 3 || res.New != 2 || res.ListedDuplicate != 1 || res.Known != 0 || res.Duplicate != 1 || res.unaccounted() != 0 {
		t.Fatalf("first fold: jobs %d new %d listed duplicate %d known %d duplicate %d unaccounted %d",
			res.Report.Jobs, res.New, res.ListedDuplicate, res.Known, res.Duplicate, res.unaccounted())
	}
	if line := inboxFoldLine(res); !strings.HasSuffix(line, "3 job(s): 2 new, 0 already known, 1 duplicate of a stored job") {
		t.Fatalf("line %q", line)
	}

	// The same page again: every job is already known, the duplicate record too.
	spoolBatch(t, w.stateDir, "careers-acme-com", "Platform Engineer", "Data Engineer", "Site Reliability Engineer")
	res, err = drainInbox(t.Context(), w.led, w.stateDir, protect)
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Jobs != 3 || res.New != 0 || res.Known != 3 || res.unaccounted() != 0 {
		t.Fatalf("second fold: jobs %d new %d known %d unaccounted %d", res.Report.Jobs, res.New, res.Known, res.unaccounted())
	}
	if line := inboxFoldLine(res); !strings.HasSuffix(line, "3 job(s): 0 new, 3 already known") {
		t.Fatalf("line %q", line)
	}
}
