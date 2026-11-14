package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A board's postings close only when a full listing of that board leaves them
// out (Reconcile's absence pass), and a board taken off the watchlist is never
// listed again, so its open postings would stay open for ever. Nothing in the
// ledger says which open postings a removed board stranded: the extension
// ingests postings under the prefixes of boards nobody watches, and those must
// stay. So the removal itself says it: `vani watchlist remove` leaves a
// tombstone naming the board's key prefix, and the next run, which holds the
// ledger lock, closes the board's open postings and deletes the tombstone.
// Adding the board back revives them: its listing finds them Dead and returns
// each to the status it died in (Reconcile, "reappeared").
const (
	// RemovedBoardDir is where the tombstones live, under the state dir: one
	// file a board, named from its key prefix, so a second removal of the
	// same board replaces the first and two writers never share a file.
	RemovedBoardDir = "watchlist/removed"
	// MaxRemovedBoards bounds the tombstones one run reads. The rest wait for
	// later runs (they are read in name order, and each acted on is deleted).
	MaxRemovedBoards = 256
	// maxRemovedBoardBytes bounds one tombstone file. A real one is under 200.
	maxRemovedBoardBytes = 4 << 10
	// RemovedSettle is how much older than a run's config read a tombstone of
	// a board that is on the watchlist must be before the run deletes it as a
	// board added back. The removal writes its tombstone before it saves the
	// config, and a run reads the config without the config lock, so a run
	// can read the config between the two and see the board still watched;
	// such a tombstone is seconds old at most, never an hour.
	RemovedSettle = time.Hour
	// RemovedBoardReason is the reason on the event that closes a removed
	// board's posting.
	RemovedBoardReason = "board removed from the watchlist"

	removedBoardVersion = 1
)

// RemovedBoard is one tombstone: the board, its key prefix, and when it was
// removed. A posting the ledger first saw after RemovedAt under that prefix can
// only have been ingested, never listed by the board, and is left open.
type RemovedBoard struct {
	V int `json:"v"`
	// Board is "ats/token", for the lines that name it.
	Board     string    `json:"board"`
	Prefix    string    `json:"prefix"`
	RemovedAt time.Time `json:"removed_at"`
}

// RemovedBoardFor is the tombstone of b removed at at. It refuses a board whose
// key prefix names no single board (a blank token, a Workday token without its
// site): closing by such a prefix could reach other boards' postings.
func RemovedBoardFor(b sources.Board, at time.Time) (RemovedBoard, error) {
	rb := RemovedBoard{V: removedBoardVersion, Board: string(b.ATS) + "/" + strings.TrimSpace(b.Token), Prefix: b.KeyPrefix(), RemovedAt: at.UTC()}
	if err := rb.valid(); err != nil {
		return RemovedBoard{}, err
	}
	return rb, nil
}

func (rb RemovedBoard) valid() error {
	if rb.V != removedBoardVersion {
		return fmt.Errorf("version %d, want %d", rb.V, removedBoardVersion)
	}
	if rb.RemovedAt.IsZero() {
		return errors.New("no removal time")
	}
	return validRemovedPrefix(rb.Prefix)
}

// validRemovedPrefix holds a prefix to the shape sources.Board.KeyPrefix
// gives one board: a known ATS (not the fallback one), then one or more
// non-empty parts, each ended by ':'.
func validRemovedPrefix(p string) error {
	ats := posting.Key(p).ATS()
	if ats == "" || ats == posting.Other {
		return fmt.Errorf("prefix %q names no board system", p)
	}
	rest, ok := strings.CutSuffix(strings.TrimPrefix(p, string(ats)+":"), ":")
	if !ok || rest == "" {
		return fmt.Errorf("prefix %q names no board", p)
	}
	for _, part := range strings.Split(rest, ":") {
		if part == "" || strings.TrimSpace(part) != part {
			return fmt.Errorf("prefix %q has an empty or padded part", p)
		}
	}
	return nil
}

// removedBoardPath is where rb's tombstone lives under stateDir.
func removedBoardPath(stateDir, prefix string) string {
	sum := sha256.Sum256([]byte(prefix))
	ats := posting.Key(prefix).ATS()
	return filepath.Join(stateDir, RemovedBoardDir, string(ats)+"-"+hex.EncodeToString(sum[:8])+".json")
}

// WriteRemovedBoard saves rb's tombstone under stateDir, replacing an earlier
// one of the same board, and returns its path.
func WriteRemovedBoard(stateDir string, rb RemovedBoard) (string, error) {
	if stateDir == "" {
		return "", errors.New("removed board: no state dir")
	}
	if err := rb.valid(); err != nil {
		return "", fmt.Errorf("removed board %s: %w", rb.Board, err)
	}
	data, err := json.Marshal(rb)
	if err != nil {
		return "", fmt.Errorf("removed board %s: %w", rb.Board, err)
	}
	path := removedBoardPath(stateDir, rb.Prefix)
	if err := atomicfile.Write(path, data); err != nil {
		return "", fmt.Errorf("removed board %s: %w", rb.Board, err)
	}
	return path, nil
}

// RemovedBoardFile is a tombstone read back, with the file it came from.
type RemovedBoardFile struct {
	Path string
	RemovedBoard
}

// LoadRemovedBoards reads the tombstones under stateDir, in name order, at
// most MaxRemovedBoards; over says how many more there are. A missing
// directory holds none. A file that cannot be read or is not a tombstone is a
// problem, left where it is: it closes nothing.
func LoadRemovedBoards(stateDir string) (out []RemovedBoardFile, over int, problems []error, err error) {
	dir := filepath.Join(stateDir, RemovedBoardDir)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil, nil
	}
	if err != nil {
		return nil, 0, nil, fmt.Errorf("removed boards: %w", err)
	}
	var names []string
	for _, e := range ents {
		// atomicfile's temp files start with '.'.
		if n := e.Name(); strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	if len(names) > MaxRemovedBoards {
		over, names = len(names)-MaxRemovedBoards, names[:MaxRemovedBoards]
	}
	for _, n := range names {
		path := filepath.Join(dir, n)
		rb, rerr := readRemovedBoard(path)
		if rerr != nil {
			problems = append(problems, fmt.Errorf("removed board %s: %w", n, rerr))
			continue
		}
		out = append(out, RemovedBoardFile{Path: path, RemovedBoard: rb})
	}
	return out, over, problems, nil
}

func readRemovedBoard(path string) (RemovedBoard, error) {
	f, err := atomicfile.OpenRead(path)
	if err != nil {
		return RemovedBoard{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRemovedBoardBytes+1))
	if err != nil {
		return RemovedBoard{}, err
	}
	if len(data) > maxRemovedBoardBytes {
		return RemovedBoard{}, fmt.Errorf("over %d bytes", maxRemovedBoardBytes)
	}
	var rb RemovedBoard
	if err := json.Unmarshal(data, &rb); err != nil {
		return RemovedBoard{}, err
	}
	if err := rb.valid(); err != nil {
		return RemovedBoard{}, err
	}
	return rb, nil
}

// RemovedClose is what one run did with one tombstone it acted on.
type RemovedClose struct {
	Board  string
	Prefix string
	// Closed is the board's open postings this run closed.
	Closed int
	// KeptIngested is open postings under the prefix the ledger first saw
	// after the removal: ingested, never listed by the board, left open.
	KeptIngested int
	// KeptWatched is open postings under the prefix that a board still on the
	// watchlist covers, left for that board's listing.
	KeptWatched int
	// DeleteErr is why the tombstone could not be deleted once its postings
	// were closed; the next run finds them closed and deletes it then.
	DeleteErr error
}

// RemovedReport is what CloseRemovedBoards did.
type RemovedReport struct {
	Closed []RemovedClose
	// Waiting is tombstones left for a later run: removed after this run read
	// its config, or of a board this run's config still watches that has not
	// settled (RemovedSettle).
	Waiting int
	// Rewatched is tombstones deleted because their board is on the watchlist
	// again.
	Rewatched int
	// Problems is tombstones that could not be deleted as re-watched.
	Problems []error
}

// CloseRemovedBoards closes the open postings of each removed board in tombs
// and deletes its tombstone. watched is the config's whole watchlist (never a
// run's cut of it, which would make every board outside the cut look removed)
// as read at configRead. The caller holds the ledger lock and checks that the
// watchlist was read and is not empty: an unreadable or empty config says
// nothing about which boards were removed.
//
// A tombstone written after configRead waits: the config this run holds may
// predate the removal. One whose board watched covers is deleted once it is
// older than configRead by RemovedSettle (the board was added back) and waits
// until then. Otherwise every open posting under its prefix is closed (Dead,
// died_from its status, miss marks cleared) except one a watched board covers
// and one first seen after the removal. A board's closings are written before
// its tombstone is deleted, so a crash between the two leaves a tombstone the
// next run acts on again and finds nothing open. A ledger error ends the pass
// with the tombstone kept.
func CloseRemovedBoards(ctx context.Context, l ledger.Ledger, tombs []RemovedBoardFile, watched func(posting.Key) bool, configRead, now time.Time) (RemovedReport, error) {
	var rep RemovedReport
	if len(tombs) == 0 {
		return rep, nil
	}
	recs := l.All()
	done := map[posting.Key]bool{} // closed by an earlier tombstone of this pass
	for _, t := range tombs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if watched(posting.Key(t.Prefix)) {
			if t.RemovedAt.Before(configRead.Add(-RemovedSettle)) {
				if err := os.Remove(t.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
					rep.Problems = append(rep.Problems, fmt.Errorf("removed board %s is watched again; its tombstone: %w", t.Board, err))
					continue
				}
				rep.Rewatched++
				continue
			}
			rep.Waiting++
			continue
		}
		if !t.RemovedAt.Before(configRead) {
			rep.Waiting++
			continue
		}
		rc := RemovedClose{Board: t.Board, Prefix: t.Prefix}
		var evs []ledger.Event
		for _, r := range recs {
			if !strings.HasPrefix(string(r.Key), t.Prefix) || closed(r.Status) || done[r.Key] {
				continue
			}
			switch {
			case watched(r.Key):
				rc.KeptWatched++
			case r.FirstSeen.After(t.RemovedAt):
				rc.KeptIngested++
			default:
				ann := clearMissMarks(map[string]string{DiedFromKey: string(r.Status), ledger.MissedRunsKey: "0"})
				evs = append(evs, ledger.Event{V: ledger.SchemaVersion, At: now, Key: r.Key, Status: posting.Dead,
					Annotations: ann, Reason: RemovedBoardReason})
			}
		}
		n, err := appendBatched(ctx, l, evs)
		rc.Closed = n
		for _, e := range evs[:n] {
			done[e.Key] = true
		}
		if err != nil {
			rep.Closed = append(rep.Closed, rc)
			return rep, fmt.Errorf("removed board %s: closed %d of %d postings: %w", t.Board, n, len(evs), err)
		}
		if err := os.Remove(t.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rc.DeleteErr = err
		}
		rep.Closed = append(rep.Closed, rc)
	}
	return rep, nil
}
