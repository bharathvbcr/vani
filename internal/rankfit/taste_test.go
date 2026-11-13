package rankfit

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// unit is a normalised 3-vector.
func unit(x, y, z float64) []float32 {
	n := math.Sqrt(x*x + y*y + z*z)
	return []float32{float32(x / n), float32(y / n), float32(z / n)}
}

// tasteWorld is n yes postings near the x axis, n no postings near the y
// axis, and title parts that order them as titleGood says.
func tasteWorld(n int, titleGood bool) []TasteItem {
	var out []TasteItem
	for i := range n {
		e := float64(i) * 0.01
		ty, tn := 0.2, 0.8 // the title part orders them backwards
		if titleGood {
			ty, tn = 0.8, 0.2
		}
		out = append(out,
			TasteItem{Key: posting.Key(fmt.Sprintf("y%d", i)), Grade: GradeYes, Title: ty, Vector: unit(1, e, 0.1)},
			TasteItem{Key: posting.Key(fmt.Sprintf("n%d", i)), Grade: GradeNo, Title: tn, Vector: unit(e, 1, 0.1)})
	}
	return out
}

func TestTasteBeatsABackwardsTitlePart(t *testing.T) {
	tt := BuildTaste(tasteWorld(6, false), 200, 1)
	if tt.Status != TasteOK || !tt.Passes {
		t.Fatalf("%+v: want ok and passing", tt)
	}
	if tt.AUC < 0.99 || tt.TitleAUC > 0.01 || tt.Pos != 6 || tt.Neg != 6 {
		t.Fatalf("auc %v title %v pos %d neg %d", tt.AUC, tt.TitleAUC, tt.Pos, tt.Neg)
	}
	if !strings.Contains(tt.Markdown(), "passes") {
		t.Errorf("markdown: %s", tt.Markdown())
	}
	// The same nearness against a title part that already orders perfectly
	// does not beat it.
	if tt := BuildTaste(tasteWorld(6, true), 200, 1); tt.Status != TasteOK || tt.Passes {
		t.Fatalf("against a perfect title: %+v, want ok and not passing", tt)
	}
}

// Leave-one-out: a lone yes far from every other yes is scored by its
// nearest *other* yes, not by itself (which would be cosine 1).
func TestTasteLeavesThePostingItselfOut(t *testing.T) {
	items := tasteWorld(5, false)
	// A yes on the y axis, next to the no postings: with itself left out its
	// taste is low, so it ranks among the no's and the AUC falls below 1.
	// Not identical to any no posting, so a self-score of 1 would be strictly
	// the highest and give AUC 1.
	items = append(items, TasteItem{Key: "yfar", Grade: GradeYes, Title: 0.2, Vector: unit(0.05, 1, 0.3)})
	tt := BuildTaste(items, 200, 1)
	if tt.Status != TasteOK || tt.Yes != 6 {
		t.Fatalf("%+v", tt)
	}
	if tt.AUC >= 1 {
		t.Fatalf("AUC %v: the far yes was scored against itself", tt.AUC)
	}
}

func TestTasteRefusesWhatItCannotMeasure(t *testing.T) {
	// Four yes postings: insufficient, never a pass.
	tt := BuildTaste(tasteWorld(4, false), 200, 1)
	if tt.Status != TasteInsufficient || tt.Passes || !strings.Contains(tt.Reason, "at least 5") {
		t.Fatalf("4 yes: %+v", tt)
	}
	// No vectors at all.
	noVec := tasteWorld(6, false)
	for i := range noVec {
		noVec[i].Vector = nil
	}
	if tt := BuildTaste(noVec, 200, 1); tt.Status != TasteNoVectors || tt.Passes || tt.NoVector != 12 {
		t.Fatalf("no vectors: %+v", tt)
	}
	// Postings without a vector are counted, and the title AUC is taken only
	// on the postings that have one.
	mixed := append(tasteWorld(6, false), TasteItem{Key: "nv", Grade: GradeNo, Title: 0.0})
	if tt := BuildTaste(mixed, 200, 1); tt.NoVector != 1 || tt.Neg != 6 {
		t.Fatalf("mixed: %+v, want 1 without a vector and 6 unwanted with one", tt)
	}
	// Five yes but no unwanted posting: no AUC, so insufficient.
	var onlyYes []TasteItem
	for _, it := range tasteWorld(6, false) {
		if it.Grade == GradeYes {
			onlyYes = append(onlyYes, it)
		}
	}
	if tt := BuildTaste(onlyYes, 200, 1); tt.Status != TasteInsufficient || tt.Passes {
		t.Fatalf("only yes: %+v", tt)
	}
	if tt := BuildTaste(nil, 200, 1); tt.Status != TasteNoVectors || tt.Passes {
		t.Fatalf("empty: %+v", tt)
	}
}
