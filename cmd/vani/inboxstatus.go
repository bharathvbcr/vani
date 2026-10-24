package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// The inbox poll runs every couple of minutes and does not write a run record.
// runs/inbox.json is the one status it leaves: when it last ran, the last
// error as one line, and counts. Bad-file names stay out; a name can carry a
// posting. A missing file means no poll has been recorded, which is different
// from a poll that failed.

const (
	inboxStatusVersion = 1
	inboxStatusFile    = "inbox.json"
	maxInboxStatus     = 4 << 10
)

// inboxStatus is one poll. Counts are omitempty so a quiet empty inbox stays
// a timestamp. Error is one line, cut at maxPanicText (CleanText may add an ellipsis).
type inboxStatus struct {
	V          int       `json:"v"`
	At         time.Time `json:"at"`
	Error      string    `json:"error,omitempty"`
	Bad        int       `json:"bad,omitempty"`
	Unreadable int       `json:"unreadable,omitempty"`
	Remaining  int       `json:"remaining,omitempty"`
	Folded     int       `json:"folded,omitempty"`
	New        int       `json:"new,omitempty"`
	Duplicate  int       `json:"duplicate,omitempty"`
	Live       int       `json:"live,omitempty"`
}

func inboxStatusPath(stateDir string) string {
	return runsPath(stateDir, inboxStatusFile)
}

func writeInboxStatus(stateDir string, st inboxStatus) error {
	st.V = inboxStatusVersion
	if st.At.IsZero() {
		st.At = time.Now().UTC()
	}
	if st.Error != "" {
		st.Error = oneLine(st.Error, maxPanicText)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if len(raw) > maxInboxStatus {
		return fmt.Errorf("inbox status exceeds %d bytes", maxInboxStatus)
	}
	return atomicfile.Write(inboxStatusPath(stateDir), raw)
}

// readInboxStatus reads the poll status. A missing file is os.ErrNotExist.
// A file that is too big, truncated, or the wrong version is an error: a poll
// that could not be read is not a poll that succeeded.
func readInboxStatus(stateDir string) (inboxStatus, error) {
	f, err := os.Open(inboxStatusPath(stateDir))
	if err != nil {
		return inboxStatus{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxInboxStatus+1))
	if err != nil {
		return inboxStatus{}, err
	}
	if len(raw) > maxInboxStatus {
		return inboxStatus{}, fmt.Errorf("inbox status exceeds %d bytes", maxInboxStatus)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var st inboxStatus
	if err := dec.Decode(&st); err != nil {
		return inboxStatus{}, fmt.Errorf("inbox status is corrupt: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return inboxStatus{}, errors.New("inbox status has trailing data")
	}
	if st.V != inboxStatusVersion {
		return inboxStatus{}, fmt.Errorf("inbox status version %d, want %d", st.V, inboxStatusVersion)
	}
	if st.At.IsZero() {
		return inboxStatus{}, errors.New("inbox status has no time")
	}
	return st, nil
}
