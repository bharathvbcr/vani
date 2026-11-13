package srcutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// Starved boards. A board whose host was rate limited when its turn came (it
// got the 429, or the host's memory refused it) is left unchanged and retried
// the next run. If boards are always tried in the same order, the same boards
// on a shared host are the ones refused every run. The run records which
// boards were rate limited, and the next run tries them first, so the host's
// patience is spent on the boards that went without.
//
// The file says nothing about a posting: it only orders the next run.

const (
	starvedVer = 1
	// maxStarvedFile bounds the file read; maxStarvedBoards the boards kept;
	// maxStarvedKey one key's length in bytes.
	maxStarvedFile   = 1 << 20
	maxStarvedBoards = 4096
	maxStarvedKey    = 512
	// maxStarvedAge is how long a record the runs no longer refresh is kept.
	maxStarvedAge = 30 * 24 * time.Hour
)

type starvedDoc struct {
	Version int      `json:"version"`
	At      string   `json:"at"`
	Boards  []string `json:"boards"`
}

// LoadStarved reads the boards the last run left starved from path, in file
// order. It never fails a run: a missing file is none, and one that cannot be
// used (too big, not JSON, another version, older than maxStarvedAge) is none
// too, with a warning that says so. Keys that are empty, over maxStarvedKey, or
// beyond maxStarvedBoards are dropped.
func LoadStarved(path string, at time.Time) ([]string, string) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	if err != nil {
		return nil, fmt.Sprintf("starved boards not read: %v", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxStarvedFile+1))
	if err != nil {
		return nil, fmt.Sprintf("starved boards not read: %v", err)
	}
	if len(raw) > maxStarvedFile {
		return nil, fmt.Sprintf("starved boards ignored: %s is over %d bytes", path, maxStarvedFile)
	}
	var doc starvedDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != starvedVer {
		return nil, fmt.Sprintf("starved boards ignored: %s is not a version %d starved-boards file", path, starvedVer)
	}
	when, err := time.Parse(time.RFC3339, doc.At)
	if err != nil || at.Sub(when) > maxStarvedAge {
		return nil, "" // stale or unstamped: the boards it names have had their turn
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range doc.Boards {
		if k == "" || len(k) > maxStarvedKey || seen[k] || len(out) >= maxStarvedBoards {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out, ""
}

// SaveStarved writes keys (at most maxStarvedBoards, each at most maxStarvedKey
// bytes, each once) to path atomically, stamped at. An empty list is written as
// a file with no boards, so a stale or corrupt file is replaced.
func SaveStarved(path string, keys []string, at time.Time) error {
	doc := starvedDoc{Version: starvedVer, At: at.UTC().Format(time.RFC3339), Boards: []string{}}
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" || len(k) > maxStarvedKey || seen[k] || len(doc.Boards) >= maxStarvedBoards {
			continue
		}
		seen[k] = true
		doc.Boards = append(doc.Boards, k)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("starved boards: %w", err)
	}
	if err := atomicfile.Write(path, append(raw, '\n')); err != nil {
		return fmt.Errorf("starved boards: %w", err)
	}
	return nil
}
