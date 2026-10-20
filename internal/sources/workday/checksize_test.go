package workday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func sizeDoer(t *testing.T, total int, facets string, bodies *[]listRequest) *srctest.Doer {
	t.Helper()
	return srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if r.Method != "POST" || r.URL != jobsURL {
			return srctest.Reply{}, false
		}
		var req listRequest
		if err := json.Unmarshal(r.Body, &req); err != nil {
			t.Errorf("request body: %v", err)
		}
		*bodies = append(*bodies, req)
		return ok(fmt.Sprintf(`{"total":%d,"jobPostings":[{"title":"x","externalPath":"/job/a/b_1"}],"facets":%s}`, total, facets)), true
	})
}

func facetsJSON(counts map[string][]int) string {
	var parts []string
	n := 0
	for _, p := range []string{"timeType", "workerSubType", "jobFamilyGroup"} {
		cs, ok := counts[p]
		if !ok {
			continue
		}
		var vals []string
		for _, c := range cs {
			n++
			vals = append(vals, fmt.Sprintf(`{"id":"%032x","count":%d}`, n, c))
		}
		parts = append(parts, fmt.Sprintf(`{"facetParameter":%q,"values":[%s]}`, p, strings.Join(vals, ",")))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// CheckSize refuses a board whose search counts over the cap before any
// detail is fetched, with the error List gives, and asks one page per term.
func TestCheckSizeRefusesOverCapWithOnePageRequestPerTerm(t *testing.T) {
	var bodies []listRequest
	b := board
	b.Search, b.Searches = "ml", []string{"go"}
	big := facetsJSON(map[string][]int{"timeType": {MaxPostings, 1}, "workerSubType": {MaxPostings - 1, 2}})
	err := CheckSize(context.Background(), sizeDoer(t, 2000, big, &bodies), b)
	if !errors.Is(err, srcutil.ErrTooManyPostings) || !strings.Contains(err.Error(), fmt.Sprint(MaxPostings+1)) {
		t.Fatalf("err = %v, want ErrTooManyPostings naming the count", err)
	}
	if len(bodies) != 1 || bodies[0].Offset != 0 || bodies[0].SearchText != "ml" {
		t.Fatalf("requests = %+v, want one offset-0 request for the first term", bodies)
	}
}

func TestCheckSizeAcceptsWhatListCanListAndChecksEveryTerm(t *testing.T) {
	var bodies []listRequest
	b := board
	b.Search, b.Searches = "ml", []string{"go"}
	if err := CheckSize(context.Background(), sizeDoer(t, 150, "[]", &bodies), b); err != nil {
		t.Fatalf("a small search: %v", err)
	}
	if len(bodies) != 2 || bodies[1].SearchText != "go" {
		t.Fatalf("requests = %+v, want one per term", bodies)
	}
	bodies = nil
	if err := CheckSize(context.Background(), sizeDoer(t, 5, "[]", &bodies), board); err != nil || len(bodies) != 1 || bodies[0].SearchText != "" {
		t.Fatalf("no search term: err %v, requests %+v", err, bodies)
	}
}

// NVIDIA answers total 2000 for a board of 2753 that its facets count and
// split: List lists it, so it can be saved. Without facets that corroborate
// and split the count, the capped total is refused.
func TestCheckSizeReadsTheWindowedTotalByItsFacets(t *testing.T) {
	var bodies []listRequest
	nvidia := facetsJSON(map[string][]int{"timeType": {2700, 53}, "workerSubType": {2650, 103}, "jobFamilyGroup": {1500, 600, 653}})
	if err := CheckSize(context.Background(), sizeDoer(t, 2000, nvidia, &bodies), board); err != nil {
		t.Fatalf("NVIDIA-shaped board refused: %v", err)
	}
	for name, facets := range map[string]string{
		"no facets": "[]",
		"one facet": facetsJSON(map[string][]int{"jobFamilyGroup": {1500, 600, 653}}),
		"no split":  facetsJSON(map[string][]int{"timeType": {2753}, "workerSubType": {2753}}),
		"partial":   `[{"facetParameter":"timeType","values":[{"id":"x","count":2753}]}]`,
	} {
		if err := CheckSize(context.Background(), sizeDoer(t, 2000, facets, &bodies), board); !errors.Is(err, srcutil.ErrTooManyPostings) {
			t.Errorf("%s: err = %v, want refused", name, err)
		}
	}
}

// A reply that cannot be read, or a failing host, is an error and never a
// small count.
func TestCheckSizeUnreadableReplyIsAnError(t *testing.T) {
	for name, reply := range map[string]srctest.Reply{
		"no array":    ok(`{"total":3}`),
		"not json":    ok(`<html>`),
		"server down": {Status: 500, Body: []byte("x")},
		"not found":   {Status: 404, Body: []byte("x")},
		"bad row":     ok(`{"total":1,"jobPostings":[{"title":"x","externalPath":"/../admin"}]}`),
	} {
		d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return reply, true })
		err := CheckSize(context.Background(), d, board)
		if err == nil || errors.Is(err, srcutil.ErrTooManyPostings) {
			t.Errorf("%s: err = %v, want a non-cap error", name, err)
		}
	}
}

func TestCheckSizeRefusesABadBoardAndACancelledContext(t *testing.T) {
	d := srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return ok(`{"total":1,"jobPostings":[]}`), true })
	for name, b := range map[string]sources.Board{
		"host not the tenant's": {ATS: posting.Workday, Token: "nvidia/Site", Host: "evil.example.com"},
		"no host":               {ATS: posting.Workday, Token: "nvidia/Site"},
		"bad token":             {ATS: posting.Workday, Token: "nvidia", Host: host},
	} {
		if err := CheckSize(context.Background(), d, b); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckSize(ctx, d, board); err == nil {
		t.Error("a cancelled context passed")
	}
}
