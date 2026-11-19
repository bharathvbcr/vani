package semantic

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// Two roles behind the same company introduction share their first kilobyte.
// Each keeps its own title, so the embedded texts differ and the cosine is a
// real reading, not 1.000.
func TestTitleIndexKeepsRolesBehindOneIntroductionApart(t *testing.T) {
	intro := strings.Repeat("About the company. ", 80) // well over MaxDupTextBytes
	if len(intro) <= MaxDupTextBytes {
		t.Fatal("fixture too short")
	}
	a, b := intro+"You will build payment services.", intro+"You will bake the bread."
	x := NewTitleIndex()
	x.Add("Backend Engineer", a)
	x.Add("Pastry Chef", b)
	if got := x.Title(a); got != "Backend Engineer" {
		t.Errorf("a: %q", got)
	}
	if got := x.Title(b); got != "Pastry Chef" {
		t.Errorf("b: %q", got)
	}
	cos, ok, err := NewTitledSimilarity(labelSpy(), nil, x)(context.Background(), a, b)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if cos >= 1 {
		t.Errorf("two different roles read as identical: %.3f", cos)
	}
}

// Different descriptions that reduce to the same embedded text give no
// reading rather than a cosine of 1.000; identical descriptions still do.
func TestSimilarityOfSameEmbeddedTextIsNoReading(t *testing.T) {
	intro := strings.Repeat("About the company. ", 80)
	a, b := intro+" tail one", intro+" tail two"
	sim := NewSimilarity(labelSpy(), nil)
	cos, ok, err := sim(context.Background(), a, b)
	if err != nil || ok || cos != 0 {
		t.Errorf("same embedded text: cos=%v ok=%v err=%v; want no reading", cos, ok, err)
	}
	if cos, ok, err := sim(context.Background(), a, a); err != nil || !ok || cos != 1 {
		t.Errorf("identical: %v %v %v", cos, ok, err)
	}
}

func TestTitleIndexReportsItsBound(t *testing.T) {
	x := NewBoundedTitleIndex(2)
	if !x.Add("A", "d1") || !x.Add("B", "d2") {
		t.Fatal("adds under the bound refused")
	}
	if x.Full() {
		t.Fatal("full before any refusal")
	}
	if x.Add("", "d3") || x.Add("C", " ") {
		t.Error("blank input recorded")
	}
	if x.Full() {
		t.Error("a blank refusal counted as full")
	}
	if !x.Add("A", "d1") {
		t.Error("a repeat of a held description is not new and must succeed")
	}
	if x.Add("C", "d3") {
		t.Error("add past the bound succeeded")
	}
	if !x.Full() {
		t.Error("Full not set after a refusal")
	}
	if x.Title("d1") != "A" || x.Title("d3") != "" {
		t.Error("held entries changed or the refused one was recorded")
	}
}

func TestTitleIndexDefaultBound(t *testing.T) {
	if got := NewBoundedTitleIndex(0).max; got != MaxTitleIndex {
		t.Errorf("max < 1 gave %d", got)
	}
}

func TestTitleIndexFullIsSafeConcurrently(t *testing.T) {
	x := NewBoundedTitleIndex(10)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				x.Add("T", strings.Repeat("d", i*50+j+1))
				_ = x.Full()
				_ = x.Title("d")
			}
		}(i)
	}
	wg.Wait()
	if !x.Full() || len(x.t) != 10 {
		t.Errorf("full=%v held=%d", x.Full(), len(x.t))
	}
}
