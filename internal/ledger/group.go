package ledger

import (
	"context"
	"errors"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Grouper is a Ledger that can make many appends durable with one fsync
// (File.Group). It is optional: a Ledger that is not one (a test fake, a
// snapshot) makes each append durable on its own, and InGroup runs fn on it
// as it is.
type Grouper interface {
	Group(ctx context.Context, fn func(Ledger) error) error
}

// InGroup runs fn inside l's group when l is a Grouper, and with l itself
// otherwise.
func InGroup(ctx context.Context, l Ledger, fn func(Ledger) error) error {
	if g, ok := l.(Grouper); ok {
		return g.Group(ctx, fn)
	}
	return fn(l)
}

var _ Grouper = (*File)(nil)

// errCloseInGroup is a Close through a group's Ledger: the group's owner
// closes the File, after the group.
var errCloseInGroup = errors.New("ledger: Close inside a group; close the File after the group returns")

// Group runs fn with a Ledger whose appends are written and folded at once but
// made durable together: one fsync when fn returns, and one whenever
// MaxBatchEvents events or MaxBatchBytes bytes are un-synced. Inside fn, Get and
// All answer events that are not yet durable; when Group returns nil every one
// is. A crash inside a group leaves a whole-line prefix of its events, as a
// crash inside AppendBatch does, so group only events that are each valid alone
// and that a re-run would write again. If an fsync fails the ledger is broken,
// the file is cut to the last synced line and memory is rebuilt to it: nothing
// un-durable is answered after Group returns.
//
// The closing fsync runs however fn ends (an error, a cancelled ctx, a panic),
// and its error is joined to fn's. An Append made directly on the File while a
// group is open is durable when it returns, as always, and so is every group
// line written before it. Groups may nest or run on several goroutines: each
// one's end syncs whatever is pending.
//
// Measured on the owner's Mac on 2026-11-14: one fsync (F_FULLFSYNC) has a
// median of 7.9 ms, and a board listing writes one event per new, changed or
// reappeared posting, up to about 22,000 on the burst day of 2026-11-07.
func (f *File) Group(ctx context.Context, fn func(Ledger) error) (err error) {
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		var serr error
		switch {
		case f.closed:
		case f.broken != nil:
			serr = f.broken
		case f.goodSize > f.synced:
			serr = f.syncTo(f.goodSize)
		}
		if serr != nil {
			err = errors.Join(err, serr)
		}
	}()
	return fn(groupLedger{f})
}

// groupLedger is the Ledger a Group hands fn: its appends defer the fsync.
type groupLedger struct{ f *File }

func (g groupLedger) Append(ctx context.Context, e Event) error {
	return g.f.appendOne(ctx, e, true)
}

func (g groupLedger) AppendBatch(ctx context.Context, evs []Event) error {
	return g.f.appendMany(ctx, evs, true)
}

func (g groupLedger) Get(k posting.Key) (Record, bool) { return g.f.Get(k) }

func (g groupLedger) All() []Record { return g.f.All() }

func (g groupLedger) Close() error { return errCloseInGroup }

// Broken is the File's, so a holder that checks it through the group (the
// daemon's ledgerBroken) sees a failed group sync.
func (g groupLedger) Broken() error { return g.f.Broken() }

// Hydrate is the File's, so a caller that hydrates through the group still can.
func (g groupLedger) Hydrate(k posting.Key) (Record, error) { return g.f.Hydrate(k) }
