package greenhouse

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

const keptPay = `{"pay_input_ranges":[{"min_cents":10000000,"max_cents":12000000,"currency_type":"USD","title":"Annual"}]}`

// keptBoard counts pay requests by posting id and answers each with keptPay.
func keptBoard(list *string) (doerFunc, func() []string) {
	var (
		mu  sync.Mutex
		ids []string
	)
	d := varyingList(list, func(_ context.Context, r *http.Request) (*http.Response, error) {
		mu.Lock()
		ids = append(ids, payID(r.URL.String()))
		mu.Unlock()
		return reply(200, keptPay), nil
	})
	return d, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ids...)
	}
}

// A posting the engine dropped publishes pay, so its answer is never a
// remembered "no pay", and the lines are never stored, so the stored body
// never has them: without KeepsBody it is asked on every run, which on the
// live Anthropic board spent the run's whole pay budget. With KeepsBody it is
// not asked while its text is unchanged, and its stored body comes back as is.
func TestKeptPostingIsNotAskedWhileUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "greenhouse-pay.json")
	list := listOf("1", "2")
	d, asked := keptBoard(&list)
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme"}
	dropped := ghKey(t, "1")
	stored := []posting.Posting{{Key: dropped, Description: "Body 1."}, {Key: ghKey(t, "2"), Description: "Body 2."}}

	for run := range 3 {
		ps := newPaySourceFile(t, d, path, nil)
		ps.pay.cfg.KeepsBody = func(k posting.Key) bool { return k == dropped }
		ps.store(stored)
		got, err := ps.List(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Description != "Body 1." {
			t.Fatalf("run %d: kept posting's description %q, want the stored body", run, got[0].Description)
		}
		st := ps.lastReport(t)
		if st.Kept != 1 || st.Reused+st.Kept+st.Fetched+st.Failed+st.NotFound+st.Skipped != 2 {
			t.Fatalf("run %d: stats %+v", run, st)
		}
		// Posting 2 is live: its lines are stored, as reconcile does.
		stored[1] = got[1]
	}
	for _, id := range asked() {
		if id == "1" {
			t.Fatalf("pay requests %v: the kept posting was asked", asked())
		}
	}
	if n := len(asked()); n != 1 {
		t.Fatalf("pay requests %v: want posting 2 once", asked())
	}
}

// The rule is for unchanged text only: changed text makes the dropped posting
// live again, and that posting needs its pay.
func TestKeptPostingWithChangedTextIsAsked(t *testing.T) {
	list := listOf("1")
	d, asked := keptBoard(&list)
	ps := newPaySource(t, d)
	ps.pay.cfg.KeepsBody = func(posting.Key) bool { return true }
	ps.store([]posting.Posting{{Key: ghKey(t, "1"), Description: "An older body."}})
	got, err := ps.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if ids := asked(); len(ids) != 1 || !strings.HasPrefix(got[0].Description, "Pay range: USD 100000") {
		t.Fatalf("asked %v, description %q", ids, got[0].Description)
	}
	if st := ps.lastReport(t); st.Kept != 0 || st.Fetched != 1 {
		t.Errorf("stats %+v", st)
	}
}

// A kept posting leaves no mark: once its record takes a body again (a
// retriage made it live), the next run asks for its pay.
func TestKeptPostingIsAskedOnceItsRecordTakesABody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "greenhouse-pay.json")
	list := listOf("1")
	d, asked := keptBoard(&list)
	b := sources.Board{ATS: posting.Greenhouse, Token: "acme"}
	stored := []posting.Posting{{Key: ghKey(t, "1"), Description: "Body 1."}}

	ps := newPaySourceFile(t, d, path, nil)
	ps.pay.cfg.KeepsBody = func(posting.Key) bool { return true }
	ps.store(stored)
	if _, err := ps.List(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if len(asked()) != 0 {
		t.Fatalf("kept posting asked: %v", asked())
	}

	next := newPaySourceFile(t, d, path, nil)
	next.pay.cfg.KeepsBody = func(posting.Key) bool { return false }
	next.store(stored)
	got, err := next.List(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked()) != 1 || !strings.HasPrefix(got[0].Description, "Pay range: ") {
		t.Fatalf("revived posting: asked %v, description %q", asked(), got[0].Description)
	}
}
