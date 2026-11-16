package decide

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Spend meters a paid backend. Allow is asked before each call; Charge records
// what the call cost.
type Spend interface {
	Charge(ctx context.Context, key string, usd float64) error
	Allow(ctx context.Context, key string) error
}

// ErrBudget is returned by Allow when the key's daily cap is reached.
var ErrBudget = errors.New("decide: daily budget reached")

const (
	spendVersion = 1
	// spendKeepDays bounds the file: older days are dropped on write.
	spendKeepDays = 31
	maxSpendFile  = 1 << 20
)

type spendFile struct {
	Version int                           `json:"version"`
	Days    map[string]map[string]float64 `json:"days"` // UTC date -> key -> usd
}

// DailyCap is a file-backed per-key daily spend cap (UTC days). It is safe for
// concurrent use within a process. The file is re-read on every operation, so
// a restart or a rollover never trusts stale memory; separate processes sharing
// the file can lose a concurrent charge (last writer wins), which is why this
// is a guard rail, not an accounting ledger.
type DailyCap struct {
	path   string
	capUSD float64
	now    func() time.Time
	mu     sync.Mutex
}

var _ Spend = (*DailyCap)(nil)

// NewDailyCap returns a cap of capUSD per key per UTC day, persisted at path
// (absolute). A corrupt existing file is an error, never silently reset.
func NewDailyCap(path string, capUSD float64) (*DailyCap, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("decide: spend file path %q must be absolute", path)
	}
	if math.IsNaN(capUSD) || math.IsInf(capUSD, 0) || capUSD <= 0 {
		return nil, errors.New("decide: daily cap must be a positive finite amount")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	d := &DailyCap{path: path, capUSD: capUSD, now: time.Now}
	if _, err := d.load(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DailyCap) today() string { return d.now().UTC().Format("2006-01-02") }

func (d *DailyCap) load() (spendFile, error) {
	empty := spendFile{Version: spendVersion, Days: map[string]map[string]float64{}}
	f, err := os.Open(d.path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxSpendFile+1))
	if err != nil {
		return empty, err
	}
	if len(raw) > maxSpendFile {
		return empty, fmt.Errorf("decide: spend file exceeds %d bytes", maxSpendFile)
	}
	// Unknown or miscased keys, duplicate keys and trailing data are all
	// corruption: an amount this cap cannot read must never count as zero.
	var s spendFile
	if err := json.Unmarshal(raw, &s, json.RejectUnknownMembers(true)); err != nil {
		return empty, fmt.Errorf("decide: spend file is corrupt: %w", err)
	}
	if s.Version != spendVersion {
		return empty, fmt.Errorf("decide: spend file version %d, want %d", s.Version, spendVersion)
	}
	if s.Days == nil {
		s.Days = map[string]map[string]float64{}
	}
	for _, keys := range s.Days {
		for _, v := range keys {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return empty, errors.New("decide: spend file holds an invalid amount")
			}
		}
	}
	return s, nil
}

func (d *DailyCap) save(s spendFile) error {
	if len(s.Days) > spendKeepDays {
		days := make([]string, 0, len(s.Days))
		for day := range s.Days {
			days = append(days, day)
		}
		sort.Strings(days)
		for _, day := range days[:len(days)-spendKeepDays] {
			delete(s.Days, day)
		}
	}
	raw, err := json.Marshal(s, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(d.path), ".spend-*") // created 0600
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // ENOENT after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, d.path)
}

// Allow returns ErrBudget when key has already spent the day's cap, and any
// read error otherwise (a cap that cannot be read refuses).
func (d *DailyCap) Allow(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s, err := d.load()
	if err != nil {
		return err
	}
	if s.Days[d.today()][key] >= d.capUSD {
		return ErrBudget
	}
	return nil
}

// Charge adds usd to key's total for today. A negative, NaN or infinite amount
// is refused. Charge records even when it pushes the total past the cap: the
// money is already spent, and the next Allow refuses.
func (d *DailyCap) Charge(ctx context.Context, key string, usd float64) error {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return errors.New("decide: charge must be a finite non-negative amount")
	}
	if key == "" {
		return errors.New("decide: charge needs a key")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s, err := d.load()
	if err != nil {
		return err
	}
	day := d.today()
	if s.Days[day] == nil {
		s.Days[day] = map[string]float64{}
	}
	s.Days[day][key] += usd
	return d.save(s)
}
