package eightfold

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func id(i int) string { return fmt.Sprint(7000000000 + i) }

func notFound() srctest.Reply { return srctest.Reply{Status: http.StatusNotFound, Body: []byte("{}")} }

// goneSite is a fake site of n positions found by the terms "t" and "u"
// alike; details answer replies[id] instead of the fixture where one is set.
func goneSite(t *testing.T, n int, replies map[string]srctest.Reply) *source {
	t.Helper()
	f := fixtureSite(t)
	var ids []string
	for i := 0; i < n; i++ {
		ids = append(ids, id(i))
	}
	f.positions = map[string][]string{"t": ids, "u": ids}
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if u, err := url.Parse(r.URL); err == nil && u.Path == "/api/pcsx/position_details" {
			if rep, ok := replies[u.Query().Get("position_id")]; ok {
				return rep, true
			}
		}
		return f.handle(r)
	})
	return &source{d: d, now: func() time.Time { return fixedNow }}
}

func TestDetail404IsBoundedLikeWorkday(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		gone    []int
		want    int // postings, when the board succeeds
		wantErr bool
	}{
		{"one 404 among twelve", 12, []int{3}, 11, false},
		{"two 404 among twelve exceed a tenth", 12, []int{3, 4}, 0, true},
		{"two 404 among twenty are within a tenth", 20, []int{0, 19}, 18, false},
		{"all 404", 3, []int{0, 1, 2}, 0, true},
		{"the only position 404", 1, []int{0}, 0, true},
	} {
		replies := map[string]srctest.Reply{}
		for _, i := range tc.gone {
			replies[id(i)] = notFound()
		}
		ps, err := goneSite(t, tc.n, replies).List(context.Background(), board("t"))
		if !tc.wantErr {
			if err != nil || len(ps) != tc.want {
				t.Errorf("%s: got %d postings, err %v; want %d", tc.name, len(ps), err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: got %d postings, want an error", tc.name, len(ps))
		} else if errors.Is(err, srcutil.ErrBoardNotFound) || !strings.Contains(err.Error(), "are gone") {
			t.Errorf("%s: want a detail error, not board-not-found: %v", tc.name, err)
		}
	}
}

// The search answering 404 is still a board that is not there.
func TestSearch404IsBoardNotFound(t *testing.T) {
	s := &source{d: srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return notFound(), true }), now: time.Now}
	if _, err := s.List(context.Background(), board("t")); !errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Errorf("err %v, want ErrBoardNotFound", err)
	}
}

// A detail that could not be read is not a gone posting: it fails the board
// with its own error.
func TestDetailDependencyDownIsNotGone(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"410":           {Status: http.StatusGone, Body: []byte("{}")},
		"429":           {Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"0"}}},
		"500":           {Status: http.StatusInternalServerError, Body: []byte("{}")},
		"malformed":     {Status: 200, Body: []byte("{not json")},
		"oversized":     {Status: 200, Body: []byte(strings.Repeat("x", srcutil.MaxBody+1))},
		"empty data":    {Status: 200, Body: []byte(`{"status":200,"data":null}`)},
		"envelope 404":  {Status: 200, Body: []byte(`{"status":404,"error":{},"data":null}`)},
		"html not json": {Status: 200, Body: []byte("<html>blocked</html>")},
	} {
		ps, err := goneSite(t, 12, map[string]srctest.Reply{id(5): rep}).List(context.Background(), board("t"))
		if err == nil {
			t.Errorf("%s: got %d postings, want the board to fail", name, len(ps))
		} else if errors.Is(err, srcutil.ErrBoardNotFound) || strings.Contains(err.Error(), "are gone") {
			t.Errorf("%s: must not read as gone: %v", name, err)
		}
	}
}

type timeoutDetails struct{ inner *srctest.Doer }

func (d timeoutDetails) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "position_details") {
		return nil, context.DeadlineExceeded
	}
	return d.inner.Do(ctx, r)
}

func TestDetailTimeoutIsNotGone(t *testing.T) {
	s := goneSite(t, 12, nil)
	s.d = timeoutDetails{inner: s.d.(*srctest.Doer)}
	_, err := s.List(context.Background(), board("t"))
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "are gone") {
		t.Errorf("err %v, want the deadline error", err)
	}
}
