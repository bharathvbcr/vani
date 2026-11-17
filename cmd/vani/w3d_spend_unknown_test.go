package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/eval"
)

// When neither the budget ledger nor the gate's meter could be read, the summary
// says the spend is unknown: no metered figure in the JSON, and no $0.00.
func TestW3dTeacherSummaryShowsUnknownSpendNotZero(t *testing.T) {
	var out bytes.Buffer
	e := &env{stdout: &out, stderr: &bytes.Buffer{}}
	ledgerErr := errors.New("ledger vanished")
	res := eval.TeacherResult{Asked: 2, Stored: 2, SpentUnknown: true}
	js := e.teacherSummary(res, 0, 2, budget.Snapshot{}, nil, budget.Snapshot{}, ledgerErr, 1_000_000)
	if js.MeteredUSD != nil {
		t.Errorf("metered_usd = %v, want absent (unknown)", *js.MeteredUSD)
	}
	if js.LedgerError == "" {
		t.Error("the ledger error was dropped")
	}
	if got := out.String(); !strings.Contains(got, "spend is unknown") || strings.Contains(got, "$0.00") {
		t.Errorf("summary does not show unknown:\n%s", got)
	}
}

// A measured spend is still shown as a number when only the ledger is down.
func TestW3dTeacherSummaryKeepsAMeasuredSpend(t *testing.T) {
	var out bytes.Buffer
	e := &env{stdout: &out, stderr: &bytes.Buffer{}}
	res := eval.TeacherResult{Asked: 1, Stored: 1, SpentNano: 5_000_000}
	js := e.teacherSummary(res, 0, 1, budget.Snapshot{}, nil, budget.Snapshot{}, errors.New("down"), 1_000_000)
	if js.MeteredUSD == nil || *js.MeteredUSD != 0.005 {
		t.Errorf("metered_usd = %v, want 0.005", js.MeteredUSD)
	}
}
