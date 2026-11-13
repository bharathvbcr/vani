package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func noneRec(b sources.Board, id int, st posting.Status, ann map[string]string, missed int) ledger.Record {
	k, err := posting.GreenhouseKey(b.Token, fmt.Sprint(id))
	if err != nil {
		panic(err)
	}
	return ledger.Record{Key: k, Status: st, Annotations: ann, MissedRuns: missed}
}

func noneRead(b sources.Board, listed int) boardReport {
	e := bhRead(b)
	e.Listed = &listed
	return e
}

// A board that read fine and listed none while the ledger holds postings of its
// that died or wait out a mass hold is named, from the ledger alone. A board
// that never listed anything, one holding only dropped postings, a board that
// listed some, a failed read and the board that ended the run are not.
func TestEmptyBoardsAreDerivedFromTheLedger(t *testing.T) {
	closedBoard, heldBoard, neverBoard, droppedBoard, listingBoard, failedBoard, endedBoard :=
		bhBoard("closed"), bhBoard("held"), bhBoard("never"), bhBoard("dropped"), bhBoard("listing"), bhBoard("failed"), bhBoard("ended")
	recs := []ledger.Record{
		noneRec(closedBoard, 1, posting.Dead, nil, 2),
		noneRec(closedBoard, 2, posting.Dead, nil, 2),
		noneRec(heldBoard, 1, posting.Live, map[string]string{pipeline.MassMissedSinceKey: "2026-11-12T00:00:00Z"}, 3),
		noneRec(heldBoard, 2, posting.Live, nil, 0), // open and listed before: not held
		noneRec(droppedBoard, 1, posting.Dropped, nil, 0),
		noneRec(listingBoard, 1, posting.Dead, nil, 2),
		noneRec(failedBoard, 1, posting.Dead, nil, 2),
		noneRec(endedBoard, 1, posting.Dead, nil, 2),
		noneRec(heldBoard, 3, posting.Live, map[string]string{pipeline.MassMissedSinceKey: ""}, 1), // a cleared mark
	}
	ended := noneRead(endedBoard, 0)
	ended.endedRun = true
	tried := []boardReport{
		noneRead(closedBoard, 0), noneRead(heldBoard, 0), noneRead(neverBoard, 0), noneRead(droppedBoard, 0),
		noneRead(listingBoard, 4), bhFail(failedBoard, "boom"), ended,
	}
	got, n := emptyBoardsOf(recs, tried)
	if n != 2 || len(got) != 2 {
		t.Fatalf("named %+v (%d)", got, n)
	}
	if got[0].Token != "closed" || got[0].Closed != 2 || got[0].Held != 0 {
		t.Fatalf("closed board: %+v", got[0])
	}
	if got[1].Token != "held" || got[1].Closed != 0 || got[1].Held != 1 {
		t.Fatalf("held board: %+v", got[1])
	}
	if line := emptyBoardLine(got[1]); !strings.Contains(line, "1 held after a mass absence") || !strings.Contains(line, "remove it from the watchlist") {
		t.Fatalf("line %q", line)
	}
}

// Deleting board health forgets nothing here: the answer does not read it.
func TestEmptyBoardsSurviveDeletingBoardHealth(t *testing.T) {
	state := t.TempDir()
	b := bhBoard("closed")
	if err := saveBoardHealth(boardHealthPath(state), boardHealthDoc{Version: boardHealthVersion,
		Boards: map[string]boardHealthRec{boardHealthKey("gh", "closed"): {LastOK: bhNow}}}); err != nil {
		t.Fatal(err)
	}
	recs := []ledger.Record{noneRec(b, 1, posting.Dead, nil, 2)}
	tried := []boardReport{noneRead(b, 0)}
	before, _ := emptyBoardsOf(recs, tried)
	if err := os.Remove(boardHealthPath(state)); err != nil {
		t.Fatal(err)
	}
	after, _ := emptyBoardsOf(recs, tried)
	if len(before) != 1 || len(after) != 1 || before[0] != after[0] {
		t.Fatalf("before %+v after %+v", before, after)
	}
}

// The list is bounded and the count exact, whatever the number of boards.
func TestEmptyBoardsAreBounded(t *testing.T) {
	var recs []ledger.Record
	var tried []boardReport
	for i := 0; i < 3*maxStaleListed; i++ {
		b := bhBoard(fmt.Sprintf("b%03d", i))
		recs = append(recs, noneRec(b, 1, posting.Dead, nil, 2))
		tried = append(tried, noneRead(b, 0))
	}
	got, n := emptyBoardsOf(recs, tried)
	if len(got) != maxStaleListed || n != 3*maxStaleListed {
		t.Fatalf("%d named, count %d", len(got), n)
	}
	if got, n := emptyBoardsOf(recs, nil); got != nil || n != 0 {
		t.Fatalf("no tried boards: %v %d", got, n)
	}
}
