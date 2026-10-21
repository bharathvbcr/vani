package apple

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// goneSource is a fake site of n postings found by the search "t"; details
// answer replies[i] instead of a good page where one is set.
func goneSource(t *testing.T, n int, replies map[int]srctest.Reply) *source {
	t.Helper()
	jd := fixture(t)
	pages := map[string]string{}
	var rows [][3]string
	for i := 0; i < n; i++ {
		id := fmt.Sprint(400000000 + i)
		rows = append(rows, [3]string{id, "0001", "role-" + id})
		pages[detailURL(id, "0001", "role-"+id)] = detailsPage(t, with(jd, "positionId", id, "jobNumber", id+"-0001", "postingTitle", "Role "+id))
	}
	pages[pageURL("t", nil, 1)] = searchPage(fmt.Sprint(n), rows...)
	s, d := newSource(pages)
	for i, rep := range replies {
		d.Replies[detailURL(rows[i][0], "0001", rows[i][2])] = rep
	}
	return s
}

func notFound() srctest.Reply {
	return srctest.Reply{Status: http.StatusNotFound, Body: []byte("gone")}
}

func TestDetail404IsBoundedLikeWorkday(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		gone    []int
		want    int
		wantErr bool
	}{
		{"one 404 among twelve", 12, []int{3}, 11, false},
		{"two 404 among twelve exceed a tenth", 12, []int{3, 4}, 0, true},
		{"two 404 among twenty are within a tenth", 20, []int{0, 19}, 18, false},
		{"all 404", 3, []int{0, 1, 2}, 0, true},
		{"the only posting 404", 1, []int{0}, 0, true},
	} {
		replies := map[int]srctest.Reply{}
		for _, i := range tc.gone {
			replies[i] = notFound()
		}
		ps, err := goneSource(t, tc.n, replies).List(context.Background(), board("t"))
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

// The search page answering 404 is still a board that is not there.
func TestSearch404IsBoardNotFound(t *testing.T) {
	s, _ := newSource(map[string]string{})
	s.d = srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return notFound(), true })
	if _, err := s.List(context.Background(), board("t")); !errors.Is(err, srcutil.ErrBoardNotFound) {
		t.Errorf("err %v, want ErrBoardNotFound", err)
	}
}

// A details page that could not be read is not a gone posting.
func TestDetailDependencyDownIsNotGone(t *testing.T) {
	for name, rep := range map[string]srctest.Reply{
		"410":          {Status: http.StatusGone, Body: []byte("x")},
		"429":          {Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"0"}}},
		"500":          {Status: http.StatusInternalServerError, Body: []byte("x")},
		"no hydration": {Status: 200, Body: []byte("<html>blocked</html>")},
		"malformed":    {Status: 200, Body: []byte("<script>window.__staticRouterHydrationData = JSON.parse(\"{bad\")</script>")},
		"oversized":    {Status: 200, Body: []byte(strings.Repeat("x", srcutil.MaxBody+1))},
	} {
		ps, err := goneSource(t, 12, map[int]srctest.Reply{5: rep}).List(context.Background(), board("t"))
		if err == nil {
			t.Errorf("%s: got %d postings, want the board to fail", name, len(ps))
		} else if errors.Is(err, srcutil.ErrBoardNotFound) || strings.Contains(err.Error(), "are gone") {
			t.Errorf("%s: must not read as gone: %v", name, err)
		}
	}
}

type timeoutDetails struct{ inner *srctest.Doer }

func (d timeoutDetails) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/details/") {
		return nil, context.DeadlineExceeded
	}
	return d.inner.Do(ctx, r)
}

func TestDetailTimeoutIsNotGone(t *testing.T) {
	s := goneSource(t, 12, nil)
	s.d = timeoutDetails{inner: s.d.(*srctest.Doer)}
	_, err := s.List(context.Background(), board("t"))
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "are gone") {
		t.Errorf("err %v, want the deadline error", err)
	}
}
