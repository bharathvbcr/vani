package greenhouse

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// skipLog is what a listing reports to its BadRowSink.
type skipLog struct {
	calls, skipped int
	first          string
}

func (l *skipLog) ctx() context.Context {
	return srcutil.WithBadRowSink(context.Background(), func(_ sources.Board, n int, first string) {
		l.calls++
		l.skipped += n
		l.first = first
	})
}

// rowsBody is n good jobs, with the given raw JSON rows replacing the good job
// at those 0-based positions.
func rowsBody(n int, bad map[int]string) string {
	var sb strings.Builder
	sb.WriteString(`{"jobs":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		if raw, ok := bad[i]; ok {
			sb.WriteString(raw)
			continue
		}
		fmt.Fprintf(&sb, `{"id":%d,"title":"T%d","absolute_url":"https://x.test/%d"}`, i+1, i+1, i+1)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

const (
	noTitle   = `{"id":900,"title":"  ","absolute_url":"https://x.test/900"}`
	jsURL     = `{"id":901,"title":"T","absolute_url":"javascript:alert(1)"}`
	wrongType = `1`
	deptsText = `{"id":902,"title":"T","absolute_url":"https://x.test/902","departments":"x"}`
)

// One malformed row used to fail the whole board, which then stayed unread
// until Greenhouse fixed the row. With a sink the row is skipped and counted,
// so the run reconciles the listing as a short one.
func TestBadRowIsSkippedAndCounted(t *testing.T) {
	for name, raw := range map[string]string{
		"empty title": noTitle, "javascript url": jsURL,
		"row of the wrong type": wrongType, "field of the wrong type": deptsText,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(one(200, rowsBody(12, map[int]string{5: raw})))
			var log skipLog
			got, err := s.List(log.ctx(), board)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 11 {
				t.Fatalf("got %d postings, want the 11 good rows", len(got))
			}
			for _, p := range got {
				if p.Key == "gh:gitlab:900" || p.Key == "gh:gitlab:901" || p.Key == "gh:gitlab:902" {
					t.Fatalf("bad row listed: %+v", p)
				}
			}
			if log.calls != 1 || log.skipped != 1 || log.first == "" {
				t.Fatalf("sink saw %+v, want one report of one skipped row", log)
			}
		})
	}
}

// Without a sink the caller cannot hold what the listing leaves out, so the
// board fails as it always did.
func TestBadRowFailsTheBoardWithoutASink(t *testing.T) {
	s, _ := newSource(one(200, rowsBody(12, map[int]string{5: noTitle})))
	if got, err := s.List(context.Background(), board); err == nil {
		t.Fatalf("want an error, got %d postings", len(got))
	}
}

// A tenth of the rows (at least one) may be skipped; more is the payload
// changing shape and fails the board.
func TestBadRowBound(t *testing.T) {
	cases := []struct {
		name string
		n    int
		bad  []int
		ok   bool
	}{
		{"one of one", 1, []int{0}, true},
		{"one of three", 3, []int{1}, true},
		{"two of three", 3, []int{0, 1}, false},
		{"one of twelve", 12, []int{3}, true},
		{"two of twelve", 12, []int{3, 4}, false},
		{"two of twenty", 20, []int{0, 19}, true},
		{"three of twenty", 20, []int{0, 1, 19}, false},
		{"all rows", 30, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := map[int]string{}
			for _, i := range c.bad {
				bad[i] = noTitle
			}
			if c.bad == nil {
				for i := 0; i < c.n; i++ {
					bad[i] = noTitle
				}
			}
			s, _ := newSource(one(200, rowsBody(c.n, bad)))
			var log skipLog
			got, err := s.List(log.ctx(), board)
			if c.ok {
				if err != nil || len(got) != c.n-len(c.bad) || log.skipped != len(c.bad) {
					t.Fatalf("got %d postings, %v, sink %+v", len(got), err, log)
				}
				return
			}
			if err == nil || got != nil {
				t.Fatalf("want the board to fail, got %d postings, %v", len(got), err)
			}
			if log.calls != 0 {
				t.Fatalf("a failed board reported a shortfall: %+v", log)
			}
		})
	}
}

// A clean listing reports nothing.
func TestCleanListingReportsNothing(t *testing.T) {
	s, _ := newSource(one(200, rowsBody(12, nil)))
	var log skipLog
	if got, err := s.List(log.ctx(), board); err != nil || len(got) != 12 || log.calls != 0 {
		t.Fatalf("got %d postings, %v, sink %+v", len(got), err, log)
	}
}

// The pay step reads each posting's job by position: a skipped row must not
// shift a later posting onto another job's pay request.
func TestBadRowKeepsPayAlignedWithItsJob(t *testing.T) {
	body := rowsBody(3, map[int]string{0: noTitle})
	d := srctest.New(map[string]srctest.Reply{
		listURL("gitlab"):       {Status: 200, Body: []byte(body)},
		payURL("gitlab", "2"):   {Status: 200, Body: []byte(`{"id":2}`)},
		payURL("gitlab", "3"):   {Status: 200, Body: []byte(`{"id":3}`)},
		payURL("gitlab", "900"): {Status: 200, Body: []byte(`{"id":900}`)},
	})
	ps := newPaySource(t, d)
	var log skipLog
	got, err := ps.List(log.ctx(), board)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "gh:gitlab:2" || got[1].Key != "gh:gitlab:3" {
		t.Fatalf("got %+v", got)
	}
	for _, u := range d.URLs() {
		if strings.Contains(u, "/900") {
			t.Fatalf("pay asked for the skipped row: %s", u)
		}
	}
	if payRequests(d) != 2 {
		t.Fatalf("pay requests %v, want one each for jobs 2 and 3", d.URLs())
	}
}
