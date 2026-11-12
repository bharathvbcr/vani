package main

import (
	"encoding/json"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
)

// The recall estimate's settings are the same whether or not anything is
// judged yet: the no-labels reply used to leave bound_target at 0 while every
// other reply said 0.05.
func TestRelevanceNoLabelsRecallCarriesItsSettings(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	o, _ := mustCode(t, 0, "relevance", "--json")
	var r struct {
		DropRecall *struct {
			Status      string  `json:"status"`
			BoundTarget float64 `json:"bound_target"`
			Draws       int     `json:"draws"`
			MinJudged   int     `json:"min_judged_per_cell"`
		} `json:"drop_recall"`
	}
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	d := r.DropRecall
	if d == nil || d.Status != eval.RecallInsufficient || d.BoundTarget != eval.RecallBoundTarget ||
		d.Draws != eval.RecallDraws || d.MinJudged != eval.MinRecallCellJudged {
		t.Errorf("no labels: %s", o)
	}
}
