package main

import (
	"fmt"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rankfit"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// tasteResamples and tasteSeed fix the taste column's bootstrap, so the
// report and the yes-neighbour gate read the same numbers.
const (
	tasteResamples = 1000
	tasteSeed      = 0x7461_7374 // "tast"
)

// tasteOf is the taste column over items (the judged items of the sampled
// labels: cell labels were drawn on a reading or for their nearness, so they
// are not in it). Each posting's pooled vector is read from the semantic store
// as it stands; nothing is embedded here, so a posting the store holds no
// current vector for counts as without one. A store that cannot be opened is
// a "no vectors" column, said so, never an error that hides the rest of a
// report.
func (e *env) tasteOf(items []rankfit.Item, snap ledger.Snapshot) rankfit.Taste {
	vr, err := semantic.OpenVectorReader(semanticDir(e.stateDir))
	if err != nil {
		t := rankfit.Taste{Status: rankfit.TasteNoVectors, Reason: fmt.Sprintf("the vector store could not be read: %v", err)}
		for i := range items {
			if items[i].Judged {
				t.NoVector++
			}
		}
		return t
	}
	defer vr.Close()
	in := make([]rankfit.TasteItem, 0, len(items))
	for i := range items {
		it := &items[i]
		if !it.Judged {
			continue
		}
		k := posting.Key(it.Key)
		ti := rankfit.TasteItem{Key: k, Grade: it.Grade, Title: it.Value[rankfit.PartTitle]}
		if r, ok := findRecord(snap, k); ok {
			if v, st := vr.Pooled(r.Posting.Title, r.Posting.Description); st == semantic.PoolOK {
				ti.Vector = v
			}
		}
		in = append(in, ti)
	}
	return rankfit.BuildTaste(in, tasteResamples, tasteSeed)
}
