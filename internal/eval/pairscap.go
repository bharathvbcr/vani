package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

// The pairwise auditor's own daily cap. It is a file of its own beside the
// budget ledgers (state/budget/wouldapply-cap.json) and it refuses by default:
// a missing file, a cap of zero or a file that cannot be read all mean no
// spend. Only an explicit `vani silver cap --usd N` makes money available, and
// the run's spend is counted against it per question, so the cap holds across
// runs and across a crash. It sits under, not instead of, the llm provider's
// own daily ceiling (decide.daily_usd.llm): a run must pass both.

const (
	// PairCapFile is the cap and spend file's name inside the budget directory.
	PairCapFile = "wouldapply-cap.json"
	// MaxPairCapUSD bounds the cap a person can set: the llm daily ceiling's own
	// bound, since the auditor never needs more than that day's whole budget.
	MaxPairCapUSD = 50.0

	pairCapVersion  = 1
	pairCapKeepDays = 31
	maxPairCapFile  = 1 << 20
	pairCapLockWait = 10 * time.Second
)

// ErrNoPairCap means no cap was ever set, so nothing may be spent.
var ErrNoPairCap = errors.New("eval: no daily cap is set for the would_apply auditor, so it spends nothing (set one with `vani silver cap --usd N`)")

type pairCapFile struct {
	Version   int              `json:"version"`
	DailyUSD  float64          `json:"daily_usd"`
	SpentNano map[string]int64 `json:"spent_nano"` // UTC date -> nano-USD
}

// PairCap is the auditor's cap file.
type PairCap struct {
	path string
}

// OpenPairCap names the cap file at path. Nothing is read or created.
func OpenPairCap(path string) *PairCap { return &PairCap{path: path} }

func (c *PairCap) lock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return nil, fmt.Errorf("silver cap: %w", err)
	}
	return lockBeside(ctx, c.path+".lock", "silver cap", errors.New("eval: another process is changing the auditor's cap"), pairCapLockWait)
}

// load reads the file; a missing one is the zero cap (which refuses).
func (c *PairCap) load() (pairCapFile, error) {
	empty := pairCapFile{Version: pairCapVersion, SpentNano: map[string]int64{}}
	f, err := atomicfile.OpenRead(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("silver cap: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPairCapFile+1))
	if err != nil {
		return empty, fmt.Errorf("silver cap: %w", err)
	}
	if len(raw) > maxPairCapFile {
		return empty, fmt.Errorf("silver cap: file exceeds %d bytes", maxPairCapFile)
	}
	var s pairCapFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return empty, fmt.Errorf("silver cap: file is corrupt: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return empty, errors.New("silver cap: file has trailing data")
	}
	if s.Version != pairCapVersion {
		return empty, fmt.Errorf("silver cap: file version %d, want %d", s.Version, pairCapVersion)
	}
	if math.IsNaN(s.DailyUSD) || s.DailyUSD < 0 || s.DailyUSD > MaxPairCapUSD {
		return empty, fmt.Errorf("silver cap: daily_usd %v is outside 0..%g", s.DailyUSD, MaxPairCapUSD)
	}
	for _, n := range s.SpentNano {
		if n < 0 {
			return empty, errors.New("silver cap: file holds a negative amount")
		}
	}
	if s.SpentNano == nil {
		s.SpentNano = map[string]int64{}
	}
	return s, nil
}

func (c *PairCap) save(s pairCapFile) error {
	if len(s.SpentNano) > pairCapKeepDays {
		days := make([]string, 0, len(s.SpentNano))
		for d := range s.SpentNano {
			days = append(days, d)
		}
		sort.Strings(days)
		for _, d := range days[:len(days)-pairCapKeepDays] {
			delete(s.SpentNano, d)
		}
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(c.path, append(raw, '\n'))
}

// Day is the UTC date a spend belongs to.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// State is the day's cap and what the auditor has spent against it, in
// nano-USD. A cap of zero (no file, or one set to zero) is a state the caller
// must refuse; a file that cannot be read is an error, never a zero spend.
func (c *PairCap) State(day string) (capNano, spentNano int64, err error) {
	s, err := c.load()
	if err != nil {
		return 0, 0, err
	}
	return int64(math.Round(s.DailyUSD * 1e9)), s.SpentNano[day], nil
}

// SetCap sets the daily cap in dollars, keeping the spend record. Zero is a
// valid cap and refuses everything; a negative, non-finite or over-bound
// amount is an error.
func (c *PairCap) SetCap(ctx context.Context, usd float64) error {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 || usd > MaxPairCapUSD {
		return fmt.Errorf("silver cap: %v is outside 0..%g dollars", usd, MaxPairCapUSD)
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := c.load()
	if err != nil {
		return err
	}
	s.DailyUSD = usd
	return c.save(s)
}

// AddSpent records nano more spent on day. Spend recorded with no cap set is
// kept too: the cap file is the record of what the auditor spent, whatever the
// cap was.
func (c *PairCap) AddSpent(ctx context.Context, day string, nano int64) error {
	if nano < 0 {
		return errors.New("silver cap: a spend cannot be negative")
	}
	if nano == 0 {
		return nil
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := c.load()
	if err != nil {
		return err
	}
	if nano > math.MaxInt64-s.SpentNano[day] {
		s.SpentNano[day] = math.MaxInt64
	} else {
		s.SpentNano[day] += nano
	}
	return c.save(s)
}
