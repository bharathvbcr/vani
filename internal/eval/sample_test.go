package eval

import (
	"fmt"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func keysN(n int) []posting.Key {
	var ks []posting.Key
	for i := 0; i < n; i++ {
		ks = append(ks, posting.Key(fmt.Sprintf("gh:a:%02d", i)))
	}
	return ks
}

func TestUniformSampleSeededAndProbability(t *testing.T) {
	pool := keysN(30)
	got, p := UniformSample("2026-10-20", pool, 3)
	if len(got) != 3 || p != 0.1 {
		t.Fatalf("picked %v with p %v, want 3 at 0.1", got, p)
	}
	rev := slices.Clone(pool)
	slices.Reverse(rev)
	again, _ := UniformSample("2026-10-20", rev, 3)
	if !slices.Equal(got, again) {
		t.Fatalf("input order changed the draw: %v vs %v", got, again)
	}
	if len(pool) != 30 || pool[0] != "gh:a:00" {
		t.Fatal("UniformSample reordered its input")
	}
	differs := false
	for d := 0; d < 5 && !differs; d++ {
		o, _ := UniformSample(fmt.Sprintf("seed-%d", d), pool, 3)
		differs = !slices.Equal(o, got)
	}
	if !differs {
		t.Fatal("five seeds drew the same postings")
	}
}

func TestUniformSampleEdges(t *testing.T) {
	if got, p := UniformSample("s", nil, 3); len(got) != 0 || p != 0 {
		t.Fatalf("empty pool: %v %v", got, p)
	}
	if got, p := UniformSample("s", keysN(5), 0); len(got) != 0 || p != 0 {
		t.Fatalf("n=0: %v %v", got, p)
	}
	if got, p := UniformSample("s", keysN(5), -2); len(got) != 0 || p != 0 {
		t.Fatalf("n<0: %v %v", got, p)
	}
	if got, p := UniformSample("s", keysN(2), 9); len(got) != 2 || p != 1 {
		t.Fatalf("n>pool: %v %v", got, p)
	}
	if got, _ := UniformSample("s", []posting.Key{"a", "a", ""}, 5); len(got) != 1 {
		t.Fatalf("duplicate and empty keys must collapse: %v", got)
	}
}

// The audit and the digest's Explore draw within a pool the same way.
func TestAuditMembersFollowUniformSample(t *testing.T) {
	var cands []AuditCandidate
	for _, k := range keysN(20) {
		cands = append(cands, AuditCandidate{Key: k, Dropped: true, Class: "title:role_fit"})
	}
	plan := AuditSample(cands, nil, "w", 0, 4)
	var drawn []posting.Key
	for _, p := range plan.Picks {
		drawn = append(drawn, p.Key)
	}
	want, p := UniformSample("w", keysN(20), 4)
	if !slices.Equal(drawn, want) || plan.Picks[0].PSelect != p {
		t.Fatalf("audit drew %v at %v, UniformSample %v at %v", drawn, plan.Picks[0].PSelect, want, p)
	}
}
