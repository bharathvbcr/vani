package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// maxLabelEmbed bounds how many labelled postings one call embeds; the rest
// wait for the next run.
const maxLabelEmbed = 200

// labelledDocs are the owner-judged postings (would_apply yes, maybe or no; a
// skip labels nothing) whose current text has no vector in the store, sorted
// by key, at most maxLabelEmbed. The warm embeds kept and marked postings only,
// in Score order, so a labelled posting that was dropped, or kept low, may never
// get one; the taste column and the yes-neighbour cell need it (the audit
// benchmark found 31 of 32 judged postings without a vector, 2026-11-14).
//
// Only answered postings: a drawn card nobody answered is not embedded, so a
// draw never puts a posting into the neighbour pool. has reports whether the
// store holds a current vector for a text (nil: there is no store yet); text
// gives a record's description, an evicted one read back. A record whose text
// cannot be read is counted in unreadable and not sent.
func labelledDocs(recs []ledger.Record, labels map[posting.Key]eval.Entry, has func(title, desc string) bool, text func(ledger.Record) (string, error)) (docs []semantic.Doc, unreadable, more int) {
	byKey := make(map[posting.Key]ledger.Record, len(labels))
	for _, r := range recs {
		if _, ok := labels[r.Key]; ok {
			byKey[r.Key] = r
		}
	}
	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		switch labels[k].Labels[eval.WouldApply] {
		case "yes", "maybe", "no":
		default:
			continue
		}
		r := byKey[k]
		desc, err := text(r)
		if err != nil {
			unreadable++
			continue
		}
		if has != nil && has(r.Posting.Title, desc) {
			continue
		}
		if len(docs) == maxLabelEmbed {
			more++
			continue
		}
		docs = append(docs, semantic.Doc{Key: string(k), Title: r.Posting.Title, Description: desc})
	}
	return docs, unreadable, more
}

// embedLabelled embeds labelledDocs through the warm's own writer
// (warmSemanticWith), keeping the run's live set for the vector log: ranks are
// the browsable postings' Scores (warmRanked), and the labelled texts rank
// above every one of them, so a compaction keeps the owner's judged postings
// longest. It never fails a run: whatever stopped it is in the report.
func embedLabelled(ctx context.Context, cfg config.Config, stateDir string, recs []ledger.Record, mk map[posting.Key]marks.Mark, labels map[posting.Key]eval.Entry, text func(ledger.Record) (string, error)) semanticReport {
	var rep semanticReport
	if !cfg.SemanticOn() {
		return notRun(rep, "semantic.snapshot and kb.bank are not both set", nil)
	}
	var has func(title, desc string) bool
	vr, err := semantic.OpenVectorReader(semanticDir(stateDir))
	switch {
	case err == nil:
		has = func(title, desc string) bool {
			_, st := vr.Pooled(title, desc)
			return st == semantic.PoolOK
		}
	case errors.Is(err, fs.ErrNotExist): // no store yet: every labelled posting is sent
	default:
		return notRun(rep, "the vector store could not be read", err)
	}
	docs, unreadable, more := labelledDocs(recs, labels, has, text)
	if vr != nil {
		vr.Close() // the warm appends to the log this reader holds open
	}
	if len(docs) > 0 {
		_, ranks := warmRanked(recs, mk, cfg)
		top := 0.0
		for _, s := range ranks {
			top = max(top, s)
		}
		for _, d := range docs {
			ranks[semantic.TextHash(d.Title, d.Description)] = top + 1
		}
		rep = warmSemanticWith(ctx, cfg, stateDir, docs, ranks, len(docs))
	} else {
		rep.Ran = true
	}
	var notes []string
	if rep.Note != "" {
		notes = append(notes, rep.Note)
	}
	if unreadable > 0 {
		notes = append(notes, fmt.Sprintf("%d labelled posting(s) not embedded: stored description unreadable", unreadable))
	}
	if more > 0 {
		notes = append(notes, fmt.Sprintf("%d more labelled posting(s) wait for the next run (at most %d a run)", more, maxLabelEmbed))
	}
	rep.Note = strings.Join(notes, "; ")
	return rep
}

// embedLabelledNow reads the labels and runs embedLabelled, logging what it
// did under "labelled postings". Labels that cannot be read are a note, never
// a failed run.
func (e *env) embedLabelledNow(ctx context.Context, cfg config.Config, recs []ledger.Record, mk map[posting.Key]marks.Mark, text func(ledger.Record) (string, error)) semanticReport {
	labels, bad, err := eval.LabelsAt(e.labelsDir())
	if err != nil {
		rep := semanticReport{Note: "labelled postings not embedded: the labels could not be read: " + oneLine(err.Error(), 200)}
		fmt.Fprintf(e.stderr, "vani: semantic: %s\n", rep.Note)
		return rep
	}
	if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	rep := embedLabelled(ctx, cfg, e.stateDir, recs, mk, labels, text)
	if rep.Ran {
		fmt.Fprintf(e.stderr, "vani: semantic: labelled postings: %s\n", rep.countsLine())
	}
	if rep.Note != "" {
		fmt.Fprintf(e.stderr, "vani: semantic: labelled postings: %s\n", rep.Note)
	}
	return rep
}
