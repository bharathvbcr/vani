package lever

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

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

// rowsBody is n good postings, with raw JSON rows replacing those at the given
// 0-based positions.
func rowsBody(n int, bad map[int]string) string {
	rows := make([]string, n)
	for i := range rows {
		if raw, ok := bad[i]; ok {
			rows[i] = raw
			continue
		}
		rows[i] = fmt.Sprintf(`{"id":"p%d","text":"T%d","hostedUrl":"https://x.test/%d"}`, i+1, i+1, i+1)
	}
	return "[" + strings.Join(rows, ",") + "]"
}

const (
	noText    = `{"id":"bad1","text":"","hostedUrl":"https://x.test/b"}`
	fileURL   = `{"id":"bad2","text":"T","hostedUrl":"file:///etc/passwd"}`
	wrongType = `1`
	listsText = `{"id":"bad3","text":"T","hostedUrl":"https://x.test/c","lists":"x"}`
	badDate   = `{"id":"bad4","text":"T","hostedUrl":"https://x.test/d","createdAt":"yesterday"}`
)

func TestBadRowIsSkippedAndCounted(t *testing.T) {
	for name, raw := range map[string]string{
		"empty text": noText, "file url": fileURL, "row of the wrong type": wrongType,
		"field of the wrong type": listsText, "date of the wrong type": badDate,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newSource(us(200, rowsBody(12, map[int]string{4: raw})))
			var log skipLog
			got, err := s.List(log.ctx(), board)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 11 {
				t.Fatalf("got %d postings, want the 11 good rows", len(got))
			}
			for _, p := range got {
				if strings.Contains(string(p.Key), "bad") {
					t.Fatalf("bad row listed: %+v", p)
				}
			}
			if log.calls != 1 || log.skipped != 1 || log.first == "" {
				t.Fatalf("sink saw %+v, want one report of one skipped row", log)
			}
		})
	}
}

func TestBadRowFailsTheBoardWithoutASink(t *testing.T) {
	s, _ := newSource(us(200, rowsBody(12, map[int]string{4: noText})))
	if got, err := s.List(context.Background(), board); err == nil {
		t.Fatalf("want an error, got %d postings", len(got))
	}
}

func TestBadRowBound(t *testing.T) {
	for _, c := range []struct {
		name string
		n    int
		bad  []int
		ok   bool
	}{
		{"one of one", 1, []int{0}, true},
		{"two of three", 3, []int{0, 1}, false},
		{"one of twelve", 12, []int{3}, true},
		{"two of twelve", 12, []int{3, 4}, false},
		{"two of twenty", 20, []int{0, 19}, true},
		{"three of twenty", 20, []int{0, 1, 19}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			bad := map[int]string{}
			for _, i := range c.bad {
				bad[i] = noText
			}
			s, _ := newSource(us(200, rowsBody(c.n, bad)))
			var log skipLog
			got, err := s.List(log.ctx(), board)
			if c.ok {
				if err != nil || len(got) != c.n-len(c.bad) || log.skipped != len(c.bad) {
					t.Fatalf("got %d postings, %v, sink %+v", len(got), err, log)
				}
				return
			}
			if err == nil || got != nil || log.calls != 0 {
				t.Fatalf("want the board to fail unreported, got %d postings, %v, sink %+v", len(got), err, log)
			}
		})
	}
}

func TestCleanListingReportsNothing(t *testing.T) {
	s, _ := newSource(us(200, rowsBody(12, nil)))
	var log skipLog
	if got, err := s.List(log.ctx(), board); err != nil || len(got) != 12 || log.calls != 0 {
		t.Fatalf("got %d postings, %v, sink %+v", len(got), err, log)
	}
}
