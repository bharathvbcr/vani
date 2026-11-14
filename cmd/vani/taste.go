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
// as it stands (postingVectors: an evicted description is read back first);
// nothing is embedded here, so a posting the store holds no current vector for
// counts as without one, by cause. A store that cannot be opened is a "no
// vectors" column, said so, never an error that hides the rest of a report.
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
	pv := e.newPostingVectors(vr, nil)
	defer pv.Close()
	return e.tasteWith(pv, items, snap)
}

// tasteWith is tasteOf over an open vector lookup.
func (e *env) tasteWith(pv *postingVectors, items []rankfit.Item, snap ledger.Snapshot) rankfit.Taste {
	in := make([]rankfit.TasteItem, 0, len(items))
	why := map[string]int{}
	for i := range items {
		it := &items[i]
		if !it.Judged {
			continue
		}
		k := posting.Key(it.Key)
		ti := rankfit.TasteItem{Key: k, Grade: it.Grade, Title: it.Value[rankfit.PartTitle]}
		if r, ok := findRecord(snap, k); ok {
			v, cause := pv.of(r)
			ti.Vector = v
			if v == nil {
				why[cause]++
			}
		} else {
			why[vecNotInLedger]++
		}
		in = append(in, ti)
	}
	t := rankfit.BuildTaste(in, tasteResamples, tasteSeed)
	if len(why) > 0 {
		t.WithoutVector = why
	}
	if pv.openErr != nil {
		t.VectorNote = fmt.Sprintf("evicted descriptions could not be read back (%v), so those postings count as text_unreadable", pv.openErr)
	}
	return t
}
