package semantic

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func attackSim(t *testing.T, x *TitleIndex, a, b string) (float64, bool, error) {
	t.Helper()
	return NewTitledSimilarity(labelSpy(), nil, x)(context.Background(), a, b)
}

func TestAttackIdenticalPostingsStillReadOne(t *testing.T) {
	x := NewTitleIndex()
	d := strings.Repeat("Build things. ", 200)
	x.Add("Engineer", d)
	x.Add("Engineer", d)
	cos, ok, err := attackSim(t, x, d, d)
	if err != nil || !ok || cos < 0.999 {
		t.Fatalf("identical: %v %v %v", cos, ok, err)
	}
	// Short identical description, no index at all.
	cos, ok, err = NewSimilarity(labelSpy(), nil)(context.Background(), "same text", "same text")
	if err != nil || !ok || cos != 1 {
		t.Fatalf("identical, untitled: %v %v %v", cos, ok, err)
	}
}

func TestAttackSameDescriptionDifferentTitleIsAmbiguousNotWrong(t *testing.T) {
	x := NewTitleIndex()
	d := "one shared description"
	x.Add("Backend Engineer", d)
	x.Add("Pastry Chef", d)
	if got := x.Title(d); got != "" {
		t.Errorf("ambiguous description kept a title: %q", got)
	}
	// Ambiguity is sticky across a third add of either title.
	x.Add("Backend Engineer", d)
	x.Add("Backend Engineer", d)
	if got := x.Title(d); got != "" {
		t.Errorf("ambiguity forgotten: %q", got)
	}
	cos, ok, err := attackSim(t, x, d, d)
	if err != nil || !ok || cos < 0.999 {
		t.Errorf("a posting against itself with an ambiguous title: %v %v %v", cos, ok, err)
	}
}

func TestAttackWhitespaceOnlyDifferences(t *testing.T) {
	x := NewTitleIndex()
	a, b := "Build payment services", "Build payment services "
	x.Add("Engineer", a)
	x.Add("Engineer", b)
	if x.Title(a) != "Engineer" || x.Title(b) != "Engineer" {
		t.Fatal("whitespace variants lost their title")
	}
	// Within the embedded prefix the texts differ, so there is a reading.
	if _, ok, err := attackSim(t, x, a, b); err != nil || !ok {
		t.Errorf("within-prefix whitespace difference: ok=%v err=%v", ok, err)
	}
	// Past the embedded prefix the texts are equal: no reading, never an error.
	pre := strings.Repeat("x ", MaxDupTextBytes)
	cos, ok, err := attackSim(t, x, pre+"A", pre+"A ")
	if err != nil || ok || cos != 0 {
		t.Errorf("tail-only difference: %v %v %v", cos, ok, err)
	}
}

func TestAttackBlankAndUnicodeInputs(t *testing.T) {
	x := NewTitleIndex()
	for _, s := range []string{"", " ", "\u00a0", "\t\n", "\x7f"} {
		if x.Add("T", s) || x.Add(s, "d") {
			t.Errorf("blank %q recorded", s)
		}
	}
	// A multi-byte description cut mid-rune at the embed boundary, and invalid UTF-8.
	d := strings.Repeat("\u00e9", MaxDupTextBytes) // 2 bytes each: 2048 bytes
	bad := string([]byte{0xff, 0xfe, 'a'})
	long := strings.Repeat("\u6f22", 200) // title 600 bytes: clipped to 256 on a rune boundary
	for _, s := range []string{d, bad, d + bad} {
		if !x.Add(long, s) {
			t.Fatalf("add %q refused", s[:3])
		}
		if got := x.Title(s); got == "" || len(got) > MaxDupTitleBytes {
			t.Errorf("title %d bytes", len(got))
		}
	}
	if _, ok, err := attackSim(t, x, d, bad); err != nil || !ok {
		t.Errorf("unicode pair: ok=%v err=%v", ok, err)
	}
	var nilIdx *TitleIndex
	if nilIdx.Title("x") != "" {
		t.Error("nil index")
	}
}

func TestAttackHugeDescriptionHashCost(t *testing.T) {
	big := strings.Repeat("a", 64<<20)
	x := NewTitleIndex()
	start := time.Now()
	if !x.Add("T", big) {
		t.Fatal("refused")
	}
	if x.Title(big) != "T" {
		t.Fatal("lost")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("hashing a 64 MiB description twice took %v", el)
	}
}

func TestAttackCapUnderConcurrentAddIsExactAndFullOnce(t *testing.T) {
	x := NewTitleIndex()
	var wg sync.WaitGroup
	const workers, per = 8, 14000 // 112000 distinct > MaxTitleIndex
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				x.Add("T", strings.Repeat("d", 3)+string(rune('A'+w))+itoa(i))
				if i%500 == 0 {
					_ = x.Full()
					_ = x.Title("dddA1")
				}
			}
		}(w)
	}
	wg.Wait()
	if len(x.t) != MaxTitleIndex {
		t.Errorf("held %d, want exactly %d", len(x.t), MaxTitleIndex)
	}
	if !x.Full() {
		t.Error("not full after 112000 distinct adds")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestAttackSimilarityContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok, err := NewTitledSimilarity(labelSpy(), nil, NewTitleIndex())(ctx, "alpha text", "beta text")
	if ok && err == nil {
		t.Error("a cancelled context produced a reading")
	}
}
