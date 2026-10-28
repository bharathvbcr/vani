package oraclehcm

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func TestCheckSizeAsksOneFirstPagePerTermAndNothingElse(t *testing.T) {
	f := &fake{byTerm: map[string][]string{"a": ids(5), "b": ids(7)}}
	if err := CheckSize(context.Background(), f.doer(), jpmcWith("a", "b")); err != nil {
		t.Fatal(err)
	}
	if f.listings != 2 || f.details != 0 {
		t.Errorf("%d listing and %d detail requests, want 2 and 0", f.listings, f.details)
	}
	if len(f.offsets) != 2 || f.offsets[0] != 0 || f.offsets[1] != 0 {
		t.Errorf("offsets %v, want first pages only", f.offsets)
	}
}

func TestCheckSizeRefusesATermOverTheCap(t *testing.T) {
	f := &fake{ids: ids(3)}
	f.total = func(string, int) int { return MaxPostings + 1 }
	err := CheckSize(context.Background(), f.doer(), jpmc())
	if !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err %v, want ErrTooManyPostings", err)
	}
	// the whole unfiltered JPMorgan board counted 7,317 on 2026-10-27
	f.total = func(string, int) int { return 7317 }
	if err := CheckSize(context.Background(), f.doer(), jpmc()); !errors.Is(err, srcutil.ErrTooManyPostings) {
		t.Fatalf("err %v, want ErrTooManyPostings", err)
	}
	b := jpmc()
	b.MaxPostings = 8000
	if err := CheckSize(context.Background(), f.doer(), b); err != nil {
		t.Fatalf("max_postings 8000 over a count of 7317: %v", err)
	}
}

// A check that could not read the host is an error, never a small count.
func TestCheckSizeThatCannotRunIsAnError(t *testing.T) {
	for name, d := range map[string]*srctest.Doer{
		"500":      srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{Status: 500}, true }),
		"404":      srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{Status: 404}, true }),
		"html":     srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return ok([]byte("<html></html>")), true }),
		"no items": srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return ok([]byte(`{"items":[]}`)), true }),
		"no count": srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
			return ok([]byte(`{"items":[{"requisitionList":[]}]}`)), true
		}),
		"count under rows": srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
			return ok([]byte(`{"items":[{"TotalJobsCount":0,"requisitionList":[{"Id":"210000001","Title":"T"}]}]}`)), true
		}),
		"forbidden": srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{Status: http.StatusForbidden}, true }),
	} {
		if err := CheckSize(context.Background(), d, jpmc()); err == nil {
			t.Errorf("%s: check passed", name)
		}
	}
	if err := CheckSize(context.Background(), errDoer{context.DeadlineExceeded}, jpmc()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: err %v", err)
	}
}

func TestCheckSizeRefusesAHostileSearch(t *testing.T) {
	d := srctest.New(nil)
	if err := CheckSize(context.Background(), d, jpmcWith(`x",limit=1`)); err == nil {
		t.Fatal("check passed")
	}
	if n := len(d.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
}
