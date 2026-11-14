package main

import (
	"fmt"
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// Why a posting has no pooled vector, as postingVectors.of reports it.
const (
	vecNoText         = "no_text"         // nothing to chunk
	vecNeverEmbedded  = "never_embedded"  // the store holds nothing for this text (semantic.PoolNoRecord)
	vecStaleChunking  = "stale_chunking"  // stored for another chunk count than the posting cuts into now
	vecCorrupt        = "corrupt"         // the record failed its checks
	vecDegenerate     = "degenerate"      // the chunk vectors cancel out
	vecTextUnreadable = "text_unreadable" // an evicted description could not be read back
	vecNotInLedger    = "not_in_ledger"   // the posting is not in the snapshot
)

// postingVectors reads postings' pooled vectors from the semantic store, the
// one place that turns a ledger record into the text its vector is stored
// under. A record the snapshot holds without its description
// (ledger.Record.BodyEvicted: a dropped, dead or duplicate posting nobody
// marked) has it read back from the events file first: hashing the title with
// an empty description finds no vector, which made every dropped posting look
// never embedded to the taste column and the yes-neighbour cell (found by the
// audit benchmark, 2026-11-14).
//
// The ledger reader is opened only when an evicted record is met, from the
// read model's checkpoint, and never saves one. A reader that cannot be opened
// is kept as openErr, and each evicted record then reports vecTextUnreadable,
// so a caller can say so rather than count those postings as never embedded.
type postingVectors struct {
	vr      *semantic.VectorReader
	dir     string // ledger directory
	ckpt    string // read model checkpoint
	rd      *ledger.Reader
	own     bool // rd was opened here and is closed by Close
	opened  bool
	openErr error
}

// newPostingVectors reads vectors from vr. rd, when not nil, is a reader the
// caller already holds for bodies (and closes itself).
func (e *env) newPostingVectors(vr *semantic.VectorReader, rd *ledger.Reader) *postingVectors {
	return &postingVectors{
		vr:     vr,
		dir:    filepath.Join(e.stateDir, "ledger"),
		ckpt:   filepath.Join(e.stateDir, "readmodel", "ledger.ckpt"),
		rd:     rd,
		opened: rd != nil,
	}
}

// Close closes the ledger reader if it was opened here.
func (p *postingVectors) Close() {
	if p.own && p.rd != nil {
		p.rd.Close()
	}
}

// text is r's description, read back when the snapshot left it out.
func (p *postingVectors) text(r ledger.Record) (string, error) {
	if !r.BodyEvicted {
		return r.Posting.Description, nil
	}
	if !p.opened {
		p.opened = true
		// SaveAfter 0: a report never writes a checkpoint.
		p.rd, p.openErr = ledger.OpenReader(p.dir, ledger.ReaderOptions{Checkpoint: p.ckpt})
		p.own = p.openErr == nil
	}
	if p.openErr != nil {
		return "", fmt.Errorf("the ledger could not be read back for evicted descriptions: %w", p.openErr)
	}
	return p.rd.Body(r.Key)
}

// of is r's pooled vector, or nil and why it has none.
func (p *postingVectors) of(r ledger.Record) ([]float32, string) {
	desc, err := p.text(r)
	if err != nil {
		return nil, vecTextUnreadable
	}
	v, st := p.vr.Pooled(r.Posting.Title, desc)
	switch st {
	case semantic.PoolOK:
		return v, ""
	case semantic.PoolNoText:
		return nil, vecNoText
	case semantic.PoolNoRecord:
		return nil, vecNeverEmbedded
	case semantic.PoolStale:
		return nil, vecStaleChunking
	case semantic.PoolDegenerate:
		return nil, vecDegenerate
	}
	return nil, vecCorrupt
}

// textHash is the store's key for r's text, with an evicted description read
// back; ok is false when it could not be.
func (p *postingVectors) textHash(r ledger.Record) (string, bool) {
	desc, err := p.text(r)
	if err != nil {
		return "", false
	}
	return semantic.TextHash(r.Posting.Title, desc), true
}
