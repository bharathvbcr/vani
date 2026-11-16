package srcutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// dueOn and notDueOn are row ids whose refresh turn is, and is not, day.
func dueOn(t *testing.T, day int) (due, notDue string) {
	t.Helper()
	for i := 0; i < 10000 && (due == "" || notDue == ""); i++ {
		id := fmt.Sprint("id-", i)
		if RefreshTurn(id, day) {
			if due == "" {
				due = id
			}
		} else if notDue == "" {
			notDue = id
		}
	}
	if due == "" || notDue == "" {
		t.Fatal("no ids for both refresh turns")
	}
	return due, notDue
}

func TestPlanDetailsOrderAndAccounting(t *testing.T) {
	const day = 20000
	due, notDue := dueOn(t, day)
	rows := []DetailRow{
		{ID: "new-0"},                             // 0 new
		{ID: notDue, Stored: true},                // 1 reused
		{ID: due, Stored: true},                   // 2 scheduled
		{ID: "drop", Drop: true},                  // 3 prefiltered
		{ID: notDue, Stored: true, Changed: true}, // 4 changed
		{ID: "new-1"},                             // 5 new
		{ID: "x", Stored: true, Drop: true},       // 6 stored: Drop ignored, reused or scheduled
	}
	p := PlanDetails(rows, day, 10)
	wantFetch := []int{0, 5, 4, 2}
	if RefreshTurn("x", day) {
		wantFetch = append(wantFetch, 6)
	}
	if !slices.Equal(p.Fetch, wantFetch) || p.New != 2 {
		t.Fatalf("fetch %v new %d, want %v new 2", p.Fetch, p.New, wantFetch)
	}
	if p.Prefiltered != 1 || p.Deferred != 0 {
		t.Fatalf("prefiltered %d deferred %d", p.Prefiltered, p.Deferred)
	}
	if n := len(p.Fetch) + len(p.Reuse) + p.Deferred + p.Prefiltered; n != len(rows) {
		t.Fatalf("plan accounts for %d of %d rows: %+v", n, len(rows), p)
	}
}

func TestPlanDetailsCapDefersNewAndReusesOverflowRereads(t *testing.T) {
	const day = 20001
	due, _ := dueOn(t, day)
	var rows []DetailRow
	for i := 0; i < 4; i++ {
		rows = append(rows, DetailRow{ID: fmt.Sprint("new-", i)})
	}
	rows = append(rows, DetailRow{ID: due, Stored: true, Changed: true}, DetailRow{ID: due, Stored: true})

	p := PlanDetails(rows, day, 3)
	if !slices.Equal(p.Fetch, []int{0, 1, 2}) || p.New != 3 || p.Deferred != 1 {
		t.Fatalf("cap 3: %+v", p)
	}
	if !slices.Equal(p.Reuse, []int{4, 5}) {
		t.Fatalf("re-reads past the cap must be reused, got %v", p.Reuse)
	}

	p = PlanDetails(rows, day, 5)
	if !slices.Equal(p.Fetch, []int{0, 1, 2, 3, 4}) || p.New != 4 || !slices.Equal(p.Reuse, []int{5}) {
		t.Fatalf("cap 5: changed row goes before the scheduled one: %+v", p)
	}

	p = PlanDetails(rows, day, 0)
	if len(p.Fetch) != 0 || p.Deferred != 4 || len(p.Reuse) != 2 {
		t.Fatalf("cap 0 fetches nothing: %+v", p)
	}
	p = PlanDetails(rows, day, -1)
	if len(p.Fetch) != 0 || p.Deferred != 4 {
		t.Fatalf("a negative cap is 0: %+v", p)
	}
}

func TestPlanDetailsEmpty(t *testing.T) {
	p := PlanDetails(nil, 0, 300)
	if len(p.Fetch)+len(p.Reuse)+p.Deferred+p.Prefiltered != 0 {
		t.Fatalf("%+v", p)
	}
}

// Every stored id is re-read exactly once in any RefreshDays consecutive days.
func TestRefreshTurnOncePerCycle(t *testing.T) {
	for _, id := range []string{"", "a", "/job/X/T1_JR1", "0e2b-uuid"} {
		n := 0
		for day := 20000; day < 20000+RefreshDays; day++ {
			if RefreshTurn(id, day) {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%q due %d times in a cycle", id, n)
		}
	}
}

// Refusal words a cap hit, and only a cap hit, and keeps the cause.
func TestDetailMissesRefusalNamesTheCapHit(t *testing.T) {
	ctx := context.Background()
	timeout := fmt.Errorf("Get x: timeout awaiting response headers: %w", os.ErrDeadlineExceeded)
	m := NewDetailMisses(DetailPlan{Fetch: make([]int, 40), New: 0, Reuse: []int{40}}, true)
	for range maxMissesPerBoard {
		if !m.Take(ctx, 0, timeout) {
			t.Fatal("a miss under the cap was refused")
		}
	}
	if m.Take(ctx, 0, timeout) {
		t.Fatal("a miss past the cap was taken")
	}
	err := m.Refusal(ctx, 0, timeout)
	if !errors.Is(err, ErrTooManyMisses) || !errors.Is(err, os.ErrDeadlineExceeded) || !strings.Contains(err.Error(), "3 already left") {
		t.Fatalf("Refusal = %v", err)
	}
	// Not a cap hit: a non-transient error, and a new row that cannot be deferred.
	boom := errors.New("HTTP 500")
	if got := m.Refusal(ctx, 0, boom); got != boom {
		t.Fatalf("a non-transient error was reworded: %v", got)
	}
	fresh := NewDetailMisses(DetailPlan{Fetch: make([]int, 40), New: 10}, false)
	if got := fresh.Refusal(ctx, 0, timeout); got != timeout {
		t.Fatalf("a refusal that is not the cap was reworded: %v", got)
	}
}

func TestDayAndMaxGone(t *testing.T) {
	if got := Day(time.Date(1970, 1, 2, 23, 0, 0, 0, time.FixedZone("x", -5*3600))); got != 2 {
		t.Fatalf("Day is UTC: got %d", got)
	}
	for n, want := range map[int]int{0: 1, 1: 1, 19: 1, 20: 2, 300: 30} {
		if got := MaxGone(n); got != want {
			t.Fatalf("MaxGone(%d) = %d, want %d", n, got, want)
		}
	}
}
