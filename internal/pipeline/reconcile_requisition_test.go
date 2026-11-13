package pipeline

import (
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// The same company, title and place under two ATSes is not by itself one job:
// a company reuses a title across requisitions, and one role opened twice is two
// jobs. Without a shared link or requisition id the records stay separate, in
// either arrival order.
func TestSameTupleOnTwoATSesWithNoSharedIDStaysTwoRecords(t *testing.T) {
	gh := func(t *testing.T) posting.Posting { return ghBare(t, "1", "Staff Engineer", "Remote") }
	lv := func(t *testing.T) posting.Posting { return lvBare(t, "a1", "Staff Engineer", "Remote") }
	t.Run("gh then lv", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, board, t0, gh(t))
		s := reconcileOne(t, l, lvBoard, t0.Add(time.Hour), lv(t))
		if s.Duplicate != 0 || s.New != 1 {
			t.Fatalf("summary %+v", s)
		}
		wantLive(t, l, gh(t).Key, lv(t).Key)
	})
	t.Run("lv then gh", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, lvBoard, t0, lv(t))
		s := reconcileOne(t, l, board, t0.Add(time.Hour), gh(t))
		if s.Duplicate != 0 || s.New != 1 {
			t.Fatalf("summary %+v", s)
		}
		wantLive(t, l, gh(t).Key, lv(t).Key)
	})
	t.Run("a listing that arrives together", func(t *testing.T) {
		l := openLedger(t)
		reconcileOne(t, l, board, t0, gh(t))
		reconcileOne(t, l, lvBoard, t0, lv(t))
		reconcileOne(t, l, board, t0.Add(24*time.Hour), gh(t)) // a later run changes nothing
		reconcileOne(t, l, lvBoard, t0.Add(24*time.Hour), lv(t))
		wantLive(t, l, gh(t).Key, lv(t).Key)
	})
}

// A requisition id both records carry is proof, whichever way each carries it,
// and a different one is proof of the opposite.
func TestCrossATSFoldNeedsASharedRequisitionID(t *testing.T) {
	withReq := func(p posting.Posting, link string) posting.Posting {
		p.URL = link
		return p
	}
	for _, tc := range []struct {
		name     string
		ghURL    string
		lvURL    string
		wantFold bool
	}{
		{"the same req_id in both links", "https://boards.greenhouse.io/acme/jobs/1?req_id=R-10234", "https://jobs.lever.co/acmelv/a1?req_id=r10234", true},
		{"the same job_id parameter", "https://boards.greenhouse.io/acme/jobs/1?job_id=77123", "https://jobs.lever.co/acmelv/a1?jobid=77123", true},
		{"another req_id", "https://boards.greenhouse.io/acme/jobs/1?req_id=R-10234", "https://jobs.lever.co/acmelv/a1?req_id=R-10235", false},
		{"an id on one side only", "https://boards.greenhouse.io/acme/jobs/1?req_id=R-10234", "https://jobs.lever.co/acmelv", false},
		{"too short to be an id", "https://boards.greenhouse.io/acme/jobs/1?req_id=12", "https://jobs.lever.co/acmelv/a1?req_id=12", false},
		{"no digit to be an id", "https://boards.greenhouse.io/acme/jobs/1?req_id=abcd", "https://jobs.lever.co/acmelv/a1?req_id=abcd", false},
		// gh_jid is Greenhouse's own id, not a number the company gave the job.
		{"gh_jid is not a requisition", "https://acme.example/careers?gh_jid=4012345", "https://jobs.lever.co/acmelv/a1?gh_jid=4012345", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			gh := withReq(ghBare(t, "1", "Staff Engineer", "Remote"), tc.ghURL)
			lv := withReq(lvBare(t, "a1", "Staff Engineer", "Remote"), tc.lvURL)
			reconcileOne(t, l, board, t0, gh)
			reconcileOne(t, l, lvBoard, t0.Add(time.Hour), lv)
			if tc.wantFold {
				wantDuplicate(t, l, lv.Key, gh.Key)
				wantLive(t, l, gh.Key)
			} else {
				wantLive(t, l, gh.Key, lv.Key)
			}
		})
	}
}

// Workday, Oracle HCM, Jibe and IBM keys end in the requisition number, so two
// records of those ATSes share one when their keys do.
func TestRequisitionIDsFromKeysFoldAcrossATSes(t *testing.T) {
	wdKey, err := posting.WorkdayKey("acme", "ext", "JR-1234")
	if err != nil {
		t.Fatal(err)
	}
	ocKey, err := posting.OracleHCMKey("acme", "cx", "jr1234")
	if err != nil {
		t.Fatal(err)
	}
	other, err := posting.OracleHCMKey("acme", "cx", "jr9999")
	if err != nil {
		t.Fatal(err)
	}
	wd := posting.Posting{Key: wdKey, ATS: posting.Workday, Company: "Acme", Title: "Data Engineer", Location: "Austin, TX", Description: "d"}
	oc := posting.Posting{Key: ocKey, ATS: posting.OracleHCM, Company: "Acme", Title: "Data Engineer", Location: "Austin, TX", Description: "d"}
	oc2 := posting.Posting{Key: other, ATS: posting.OracleHCM, Company: "Acme", Title: "Data Engineer", Location: "Austin, TX", Description: "d"}
	wdBoard := sources.Board{ATS: posting.Workday, Token: "acme/ext", Company: "Acme"}
	ocBoard := sources.Board{ATS: posting.OracleHCM, Token: "acme/cx", Company: "Acme"}

	l := openLedger(t)
	reconcileOne(t, l, wdBoard, t0, wd)
	reconcileOne(t, l, ocBoard, t0.Add(time.Hour), oc, oc2)
	wantDuplicate(t, l, oc.Key, wd.Key)
	wantLive(t, l, wd.Key, oc2.Key) // another number at the same place is another requisition
}

// The same ATS lists distinct requisitions as distinct ids, so a shared
// requisition id never folds two records of one ATS: that is unchanged.
func TestSameATSNeverFoldsOnARequisitionID(t *testing.T) {
	l := openLedger(t)
	a := ghJob(t, "1", "Staff Engineer", "Remote", "https://acme.example/jobs/a?req_id=R-10234")
	b := ghJob(t, "2", "Staff Engineer", "Remote", "https://acme.example/jobs/b?req_id=R-10234")
	reconcileOne(t, l, board, t0, a, b)
	wantLive(t, l, a.Key, b.Key)
}

// Records an earlier build folded on the tuple alone stay folded: this build
// unfolds nothing. A duplicate whose survivor stands is left alone when its
// board lists it again, and one whose survivor is gone comes back live, not
// folded again on a tuple with no shared id.
func TestTwinsFoldedByTheTupleOnlyStayFoldedAndReviveSafely(t *testing.T) {
	gh := ghBare(t, "1", "Staff Engineer", "Remote")
	lv := lvBare(t, "a1", "Staff Engineer", "Remote")
	seed := func(t *testing.T) ledger.Ledger {
		l := openLedger(t)
		reconcileOne(t, l, board, t0, gh)
		// The record an older build wrote: stored as a duplicate of gh on the
		// tuple alone.
		e := ledger.Event{V: ledger.SchemaVersion, At: t0, Key: lv.Key, Status: posting.Duplicate, Posting: &lv,
			Annotations: map[string]string{DuplicateOfKey: string(gh.Key), DescHashKey: sourceHash(lv.Description)},
			Reason:      "duplicate of " + string(gh.Key) + " (same role on another ats)"}
		if err := l.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
		return l
	}
	t.Run("the survivor stands", func(t *testing.T) {
		l := seed(t)
		for run := 1; run <= 3; run++ {
			now := t0.Add(time.Duration(run) * 24 * time.Hour)
			reconcileOne(t, l, board, now, gh)
			s := reconcileOne(t, l, lvBoard, now, lv)
			if s.Revived != 0 || s.Unchanged != 1 {
				t.Fatalf("run %d: summary %+v", run, s)
			}
		}
		wantDuplicate(t, l, lv.Key, gh.Key)
	})
	t.Run("the survivor is gone", func(t *testing.T) {
		l := seed(t)
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: gh.Key, Status: posting.Dead}); err != nil {
			t.Fatal(err)
		}
		s := reconcileOne(t, l, lvBoard, t0.Add(48*time.Hour), lv)
		if s.Revived != 1 {
			t.Fatalf("summary %+v", s)
		}
		wantLive(t, l, lv.Key)
		if r := rec(t, l, lv.Key); r.Annotations[DuplicateOfKey] != "" {
			t.Errorf("the revived record still names a survivor: %v", r.Annotations)
		}
	})
	t.Run("a new listing of the survivor does not unfold it", func(t *testing.T) {
		l := seed(t)
		reconcileOne(t, l, board, t0.Add(time.Hour), ghBare(t, "1", "Staff Engineer", "Remote"))
		wantDuplicate(t, l, lv.Key, gh.Key)
	})
}

// A fallback- or page-keyed posting (an inbox or page job) is held to the same
// rule as an ATS one: it folds into an ATS record only when the two share a
// link or a requisition id. The same company, title and place alone leaves both
// records, in either arrival order.
func TestWeakKeyedPostingFoldsOnlyOnASharedLinkOrRequisitionID(t *testing.T) {
	fk, err := posting.FallbackKey("Acme", "Staff Engineer", "Remote")
	if err != nil {
		t.Fatal(err)
	}
	pgKey, err := posting.PageKey("acmepage", "https://careers.acme.test/jobs/777")
	if err != nil {
		t.Fatal(err)
	}
	pageBoard := sources.Board{ATS: posting.Page, Token: "acmepage", Company: "Acme", URL: "https://careers.acme.test/jobs"}
	xBoard := sources.Board{ATS: posting.Other, Token: "x"}
	for _, tc := range []struct {
		name     string
		ghURL    string
		weak     posting.Posting
		bd       sources.Board
		wantFold bool
	}{
		{"x key, tuple only", "https://boards.greenhouse.io/acme", posting.Posting{Key: fk, ATS: posting.Other}, xBoard, false},
		{"pg key, tuple only", "https://boards.greenhouse.io/acme", posting.Posting{Key: pgKey, ATS: posting.Page, URL: "https://careers.acme.test/jobs/777"}, pageBoard, false},
		{"x key, another requisition id", "https://boards.greenhouse.io/acme/jobs/1?req_id=R-10234", posting.Posting{Key: fk, ATS: posting.Other, URL: "https://x.example/a?req_id=R-99999"}, xBoard, false},
		{"x key, the same requisition id", "https://boards.greenhouse.io/acme/jobs/1?req_id=R-10234", posting.Posting{Key: fk, ATS: posting.Other, URL: "https://x.example/a?req_id=r10234"}, xBoard, true},
		{"pg key, the same requisition id", "https://boards.greenhouse.io/acme/jobs/1?req_id=R-10234", posting.Posting{Key: pgKey, ATS: posting.Page, URL: "https://careers.acme.test/jobs/777?req_id=R10234"}, pageBoard, true},
		{"pg key, the same link", "https://careers.acme.test/jobs/777", posting.Posting{Key: pgKey, ATS: posting.Page, URL: "https://careers.acme.test/jobs/777"}, pageBoard, true},
	} {
		weak := tc.weak
		weak.Company, weak.Title, weak.Location, weak.Description = "Acme", "Staff Engineer", "Remote", "d"
		gh := ghBare(t, "1", "Staff Engineer", "Remote")
		gh.URL = tc.ghURL
		check := func(t *testing.T, l ledger.Ledger) {
			if tc.wantFold {
				wantDuplicate(t, l, weak.Key, gh.Key)
				wantLive(t, l, gh.Key)
			} else {
				wantLive(t, l, weak.Key, gh.Key)
			}
		}
		t.Run(tc.name+", ats first", func(t *testing.T) {
			l := openLedger(t)
			reconcileOne(t, l, board, t0, gh)
			reconcileOne(t, l, tc.bd, t0.Add(time.Hour), weak)
			check(t, l)
		})
		t.Run(tc.name+", weak first", func(t *testing.T) {
			l := openLedger(t)
			reconcileOne(t, l, tc.bd, t0, weak)
			reconcileOne(t, l, board, t0.Add(time.Hour), gh)
			check(t, l)
		})
	}
}

func TestRequisitionIDsReadKeysAndLinksAndFoldThem(t *testing.T) {
	wdKey, _ := posting.WorkdayKey("acme", "ext", "JR-1234")
	ghKey, _ := posting.GreenhouseKey("acme", "4012345")
	for _, tc := range []struct {
		name string
		key  posting.Key
		p    posting.Posting
		want []string
	}{
		{"a workday key", wdKey, posting.Posting{}, []string{"jr1234"}},
		{"a greenhouse key is not a requisition", ghKey, posting.Posting{}, nil},
		{"req_id in the link", ghKey, posting.Posting{URL: "https://x.example/job/5?req_id=R%2D55"}, []string{"r55"}},
		{"both links, once each", ghKey, posting.Posting{URL: "https://x.example/a?req_id=AB123", ApplyURL: "https://x.example/b?req_id=ab-123"}, []string{"ab123"}},
		{"a key and a link", wdKey, posting.Posting{URL: "https://x.example/a?jid=999"}, []string{"999", "jr1234"}},
		{"too short", ghKey, posting.Posting{URL: "https://x.example/a?req_id=12"}, nil},
		{"no digit", ghKey, posting.Posting{URL: "https://x.example/a?req_id=abcdef"}, nil},
		{"gh_jid is not one", ghKey, posting.Posting{URL: "https://x.example/a?gh_jid=4012345"}, nil},
		{"an unusable link", ghKey, posting.Posting{URL: "not a url ?? req_id=999"}, nil},
		{"no key, no link", "", posting.Posting{}, nil},
	} {
		got := requisitionIDs(tc.key, tc.p)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if !sharesRequisition([]string{"a1b", "c22"}, []string{"c22", "d33"}) || sharesRequisition([]string{"a1b"}, []string{"c22"}) || sharesRequisition(nil, []string{"c22"}) {
		t.Error("sharesRequisition")
	}
}
