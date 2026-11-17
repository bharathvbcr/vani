package main

import (
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The manual readings store holds at most manualtriage.MaxEntries readings, one
// per posting. Every reading in it was paid for, and none is removed to make
// room: a reading ranking no longer uses (its posting's text changed) can still
// be paired with the text it was asked about, which the ledger's append-only
// events file keeps. So a posting the store holds no reading of, once the store
// is full, is refused (rtStoreFull) before any model is asked, and the owner
// clears readings to make room.

// rtStoreFull is the code of a reading refused because the store has no room:
// nothing was asked and nothing was paid.
const rtStoreFull = "store_full"

// retriageRoom is how many postings the store, as read into m, holds no reading
// of and can still take a reading of. Reading a posting m already holds a
// reading of again takes no new place.
func retriageRoom(m map[posting.Key]manualtriage.Reading) int {
	return max(manualtriage.MaxEntries-len(m), 0)
}

// retriageWouldBeFull is a dry run's word on room: whether asking about k
// would be refused because the store has none. A store that does not read is
// the error the run would give, not "room".
func retriageWouldBeFull(dir string, k posting.Key) (bool, *retriageError) {
	stored, err := manualtriage.Read(dir)
	if err != nil {
		return false, storeErr(err, nil)
	}
	return manualNew(stored, []posting.Key{k}) > retriageRoom(stored), nil
}

// storeFullErr is the refusal of a posting the store has no room for.
func storeFullErr() *retriageError {
	return rtErr(rtStoreFull, "jobs retriage: the manual readings store is full (%d readings), so nothing was asked and nothing was paid; clear readings you no longer need (vani jobs retriage --clear KEY) to make room", manualtriage.MaxEntries)
}

// manualNew counts the keys m holds no reading of.
func manualNew(m map[posting.Key]manualtriage.Reading, keys []posting.Key) int {
	n := 0
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			n++
		}
	}
	return n
}
