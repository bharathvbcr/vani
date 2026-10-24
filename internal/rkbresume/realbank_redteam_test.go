package rkbresume

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// A continuation that follows different bullets in different selections has an
// order-dependent predecessor (the first selection in kb.selections wins).
func TestRedteamRealBankPredecessorsAreStableAcrossSelections(t *testing.T) {
	if os.Getenv("VANI_REDTEAM_REALBANK") != "1" {
		t.Skip("set VANI_REDTEAM_REALBANK=1 to run against the real bank (read-only; logs ids only)")
	}
	bank, err := kbsync.Load(rtRealBank)
	if err != nil {
		t.Skipf("no real bank: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(rtRealSelection, "*.json"))
	sort.Strings(files)
	preds := map[string]map[string]bool{} // entry|id -> predecessors seen
	for _, f := range files {
		s, err := kbsync.LoadSelection(f)
		if err != nil {
			continue
		}
		for _, sec := range s.Sections {
			if sec.Kind != "entries" {
				continue
			}
			for _, e := range sec.Entries {
				for i, id := range e.Bullets {
					if !isContinuation(bank.Bullets[id].Text) {
						continue
					}
					pred := ""
					if i > 0 {
						pred = e.Bullets[i-1]
					}
					k := e.Entry + "|" + id
					if preds[k] == nil {
						preds[k] = map[string]bool{}
					}
					preds[k][pred] = true
				}
			}
		}
	}
	var bad []string
	for k, ps := range preds {
		if len(ps) > 1 {
			var l []string
			for p := range ps {
				l = append(l, "<-"+p)
			}
			sort.Strings(l)
			bad = append(bad, k+strings.Join(l, ""))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("continuations whose predecessor depends on selection order (%d): %s", len(bad), strings.Join(bad, "; "))
	}
}
