package semantic

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Nearest picks the lines of the owner's knowledge base closest to one posting,
// so a model asked about that posting sees the evidence that bears on it.

func TestNearestFindsTheLinesThatBearOnThePosting(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	got, err := m.Nearest(context.Background(), k8sDoc(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || len(got) > 3 {
		t.Fatalf("want 1..3 neighbours, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "Kubernetes") && !strings.Contains(got[0].Text, "Go services") {
		t.Errorf("closest line to a k8s posting is %q", got[0].Text)
	}
	for i, n := range got {
		if n.Label == skillsLabel || n.Text == n.Label {
			t.Errorf("neighbour %d (%q / %q) is a skills or title line, which the compact digest already carries", i, n.Label, n.Text)
		}
		if i > 0 && n.Cosine > got[i-1].Cosine {
			t.Errorf("neighbours are not best first: %.3f after %.3f", n.Cosine, got[i-1].Cosine)
		}
	}
	// A posting about something else is nearest to different lines.
	far, err := m.Nearest(context.Background(), pastryDoc(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(far) > 0 && len(got) > 0 && far[0].Cosine >= got[0].Cosine {
		t.Errorf("a pastry posting (%.3f) is as close as a k8s one (%.3f)", far[0].Cosine, got[0].Cosine)
	}
}

func TestNearestIsBoundedAndDistinct(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	one, err := m.Nearest(context.Background(), k8sDoc(), 1)
	if err != nil || len(one) != 1 {
		t.Fatalf("k=1: %v, %d", err, len(one))
	}
	all, err := m.Nearest(context.Background(), k8sDoc(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) > MaxNearest {
		t.Errorf("k=1000 returned %d, the cap is %d", len(all), MaxNearest)
	}
	seen := map[string]bool{}
	for _, n := range all {
		if seen[strings.ToLower(n.Text)] {
			t.Errorf("duplicate neighbour %q", n.Text)
		}
		seen[strings.ToLower(n.Text)] = true
	}
	for _, k := range []int{0, -4} {
		if got, err := m.Nearest(context.Background(), k8sDoc(), k); err != nil || len(got) != 0 {
			t.Errorf("k=%d: %v, %d neighbours", k, err, len(got))
		}
	}
}

func TestNearestFailsClosed(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	if _, err := m.Nearest(context.Background(), Doc{Key: "x"}, 3); err == nil {
		t.Errorf("a posting with no text must be an error, not an empty answer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Nearest(ctx, k8sDoc(), 3); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
	down := newSpy()
	down.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, ErrUnavailable, true
	}
	m2, _ := newMatcher(t, down)
	if _, err := m2.Nearest(context.Background(), k8sDoc(), 3); !errors.Is(err, ErrUnavailable) {
		t.Errorf("an embedder that is down: %v", err)
	}
	// Bad rows from the embedder are an error, never a neighbour list from garbage.
	bad := newSpy()
	m3, _ := newMatcher(t, bad)
	mustWarm(t, m3, k8sDoc()) // profile embedded with the good embedder
	bad.hook = func(_ context.Context, texts []string, _ Kind, _ int) ([][]float32, error, bool) {
		rows := make([][]float32, len(texts))
		for i := range rows {
			rows[i] = []float32{0, 0, 0}
		}
		return rows, nil, true
	}
	if _, err := m3.Nearest(context.Background(), k8sDoc(), 3); !errors.Is(err, ErrEmbed) {
		t.Errorf("unusable rows: %v", err)
	}
}

func TestNearestDoesNotTouchTheScoreCache(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	before := m.Len()
	if _, err := m.Nearest(context.Background(), k8sDoc(), 3); err != nil {
		t.Fatal(err)
	}
	if m.Len() != before {
		t.Errorf("Nearest stored a score")
	}
}
