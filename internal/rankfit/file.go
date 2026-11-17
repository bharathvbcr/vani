// Package rankfit learns Score's constants from judged labels
// (docs/RANKING_PLAN.md, Phases 4 and 5): per-backend Platt scaling of triage
// readings' confidence, and a pairwise-logistic fit of Score's additive shares
// with an L2 prior centred on the hand weights. It runs offline and writes a
// rank weights file; scoring only reads that file (Load, then Apply), so
// nothing here runs on the ranking path. Below its label thresholds it refuses
// with a NotRunError: a fit that could not run never reads as one that ran.
package rankfit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

const (
	// FileVersion is the rank weights file's schema version.
	FileVersion = 1
	// MaxFileBytes bounds a rank weights file.
	MaxFileBytes = 1 << 20
	// MaxCalibrations bounds the backends one file calibrates, and
	// MaxBackendNameBytes one backend's name.
	MaxCalibrations     = 64
	MaxBackendNameBytes = 128
	// maxShare bounds one fitted share: ScoreWith normalizes the shares, so
	// only their ratios matter, and a share this large is a fit that ran away.
	maxShare = 1e6
)

// Shares are Score's additive shares (jobinsight.Weights' Fit through
// KBMatch). ScoreWith normalizes them, so only their ratios matter.
type Shares struct {
	Fit      float64 `json:"fit"`
	Coverage float64 `json:"coverage"`
	Semantic float64 `json:"semantic"`
	Keyword  float64 `json:"keyword"`
	Title    float64 `json:"title"`
	Employer float64 `json:"employer"`
	KBMatch  float64 `json:"kb_match"`
}

func (s Shares) Array() [NumParts]float64 {
	return [NumParts]float64{s.Fit, s.Coverage, s.Semantic, s.Keyword, s.Title, s.Employer, s.KBMatch}
}

func sharesFrom(a [NumParts]float64) Shares {
	return Shares{Fit: a[PartFit], Coverage: a[PartCoverage], Semantic: a[PartSemantic], Keyword: a[PartKeyword],
		Title: a[PartTitle], Employer: a[PartEmployer], KBMatch: a[PartKBMatch]}
}

// SharesOf reads w's additive shares as ScoreWith uses them: a share that is
// not a positive finite number counts as 0.
func SharesOf(w jobinsight.Weights) Shares {
	a := [NumParts]float64{w.Fit, w.Coverage, w.Semantic, w.Keyword, w.Title, w.Employer, w.KBMatch}
	for i, v := range a {
		if !(v > 0) || math.IsInf(v, 0) {
			a[i] = 0
		}
	}
	return sharesFrom(a)
}

// File is a rank weights file. Shares and Calibration are each optional (a
// file may carry only the part that could be fitted), but not both absent.
// Report is provenance for the reader; Apply does not read it.
type File struct {
	V int `json:"v"`
	// FittedAt is when the file was last fitted, of any part: a platt-only or a
	// weights-only fit refreshes it over the part it kept, and --spare-seniority
	// leaves it alone. It is not the age of the shares or of the calibration.
	FittedAt    time.Time                         `json:"fitted_at"`
	Shares      *Shares                           `json:"shares,omitempty"`
	Calibration map[string]jobinsight.Calibration `json:"calibration,omitempty"`
	// SpareSeniorityMismatch is jobinsight.Weights.SpareSeniorityMismatch:
	// seniority readings at which a mismatch_seniority role_fit is not a
	// mismatch. Set by the owner (`vani rankfit --spare-seniority`), never by a
	// fit; it only loosens.
	SpareSeniorityMismatch []string `json:"spare_seniority_mismatch,omitempty"`
	Report                 *Report  `json:"report,omitempty"`
	// ReportIgnored is what Load ignored of the report: the unknown fields a
	// newer vani wrote there, which an older one reads past (the report is
	// provenance, and no part of ranking reads it). It is "" for a clean report
	// and never written. ReportIgnoredPaths says where each ignored field was,
	// relative to the report ("weights.newer", "platt.d1.extra"); "?" is a field
	// the probe saw but could not place.
	ReportIgnored      string   `json:"-"`
	ReportIgnoredPaths []string `json:"-"`
}

// ReportIgnoredIn reports whether an ignored report field lies inside the
// report's section (its "weights" held-out result, say), or could not be placed.
func (f File) ReportIgnoredIn(section string) bool {
	for _, p := range f.ReportIgnoredPaths {
		if p == "?" || p == section || strings.HasPrefix(p, section+".") || strings.HasPrefix(p, section+"[") {
			return true
		}
	}
	return false
}

// PartError is a refusal that names the part of the file at fault: "shares",
// "calibration", "spare_seniority_mismatch", "report", or "file" (the envelope:
// an unknown top-level field, a bad version, a missing fitted_at, data after
// the file).
type PartError struct {
	Part string
	Err  error
}

func (e *PartError) Error() string { return e.Err.Error() }
func (e *PartError) Unwrap() error { return e.Err }

func partErr(part string, err error) error { return &PartError{Part: part, Err: err} }

// Validate refuses a file Apply could not use whole.
func (f File) Validate() error {
	if f.V != FileVersion {
		return partErr("file", fmt.Errorf("rankfit: file version %d, want %d", f.V, FileVersion))
	}
	if f.FittedAt.IsZero() {
		return partErr("file", errors.New("rankfit: file has no fitted_at"))
	}
	if f.Shares == nil && len(f.Calibration) == 0 && len(f.SpareSeniorityMismatch) == 0 {
		return partErr("file", errors.New("rankfit: file has no shares, calibration or spared seniority"))
	}
	if err := checkSpareSeniority(f.SpareSeniorityMismatch); err != nil {
		return partErr("spare_seniority_mismatch", err)
	}
	if f.Shares != nil {
		sum := 0.0
		for i, v := range f.Shares.Array() {
			if math.IsNaN(v) || v < 0 || v > maxShare {
				return partErr("shares", fmt.Errorf("rankfit: share %s is %v, want 0..%g", PartNames[i], v, float64(maxShare)))
			}
			sum += v
		}
		if !(sum > 0) {
			return partErr("shares", errors.New("rankfit: every share is 0"))
		}
	}
	if len(f.Calibration) > MaxCalibrations {
		return partErr("calibration", fmt.Errorf("rankfit: %d calibrations, max %d", len(f.Calibration), MaxCalibrations))
	}
	for name, c := range f.Calibration {
		if strings.TrimSpace(name) == "" || len(name) > MaxBackendNameBytes || strings.ContainsFunc(name, unicode.IsControl) {
			return partErr("calibration", fmt.Errorf("rankfit: calibration backend name %q is empty, too long or has a control character", name))
		}
		if !c.Valid() {
			return partErr("calibration", fmt.Errorf("rankfit: calibration for %q is not finite or exceeds |%g|", name, jobinsight.MaxCalibrationCoef))
		}
	}
	return nil
}

// Load reads and validates a rank weights file. Any defect is an error: the
// caller falls back to the hand weights and says so, never uses half a file.
func Load(path string) (File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return File{}, fmt.Errorf("rankfit: %w", err)
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, MaxFileBytes+1))
	if err != nil {
		return File{}, fmt.Errorf("rankfit: read %s: %w", path, err)
	}
	if len(b) > MaxFileBytes {
		return File{}, fmt.Errorf("rankfit: %s is over %d bytes", path, MaxFileBytes)
	}
	f, err := decodeFile(path, b)
	if err != nil {
		return File{}, err
	}
	if err := f.Validate(); err != nil {
		return File{}, err
	}
	return f, nil
}

// fileWire is a file with each part held raw, so each is decoded under its own
// rule and a refusal names the part.
type fileWire struct {
	V                      int             `json:"v"`
	FittedAt               time.Time       `json:"fitted_at"`
	Shares                 json.RawMessage `json:"shares"`
	Calibration            json.RawMessage `json:"calibration"`
	SpareSeniorityMismatch []string        `json:"spare_seniority_mismatch"`
	Report                 json.RawMessage `json:"report"`
}

// decodeFile decodes b under two rules. The envelope and the parts that rank
// (shares, calibration, spared seniority) are strict: an unknown field in any of
// them refuses the file, since an older reader ranking by half-understood
// weights is wrong. The report is provenance that nothing ranks by, so an
// unknown field there (a newer vani's addition) is read past and recorded in
// File.ReportIgnored; a report that is not decodable at all is still refused.
func decodeFile(path string, b []byte) (File, error) {
	var w fileWire
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return File{}, partErr("file", fmt.Errorf("rankfit: decode %s: %w", path, err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return File{}, partErr("file", fmt.Errorf("rankfit: %s has data after the file", path))
	}
	f := File{V: w.V, FittedAt: w.FittedAt, SpareSeniorityMismatch: w.SpareSeniorityMismatch}
	if err := decodePart(path, "shares", w.Shares, &f.Shares, true); err != nil {
		return File{}, err
	}
	if err := decodePart(path, "calibration", w.Calibration, &f.Calibration, true); err != nil {
		return File{}, err
	}
	if err := decodePart(path, "report", w.Report, &f.Report, false); err != nil {
		return File{}, err
	}
	if f.Report != nil {
		paths := unknownPaths(w.Report, reflect.TypeFor[Report](), "")
		strict := json.NewDecoder(bytes.NewReader(w.Report))
		strict.DisallowUnknownFields()
		var probe *Report
		if err := strict.Decode(&probe); err != nil && len(paths) == 0 {
			paths = []string{"?"}
		}
		if len(paths) > 0 {
			f.ReportIgnoredPaths = paths
			f.ReportIgnored = "unknown field " + strings.Join(paths, ", ")
		}
	}
	return f, nil
}

// unknownPaths lists, in document order within each object (sorted by key),
// the JSON fields of raw that t does not name, as dotted paths under prefix.
// Names match case-insensitively, as encoding/json decodes them; a field named
// by no struct tag falls back to the Go field name.
func unknownPaths(raw json.RawMessage, t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []string
	switch t.Kind() {
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		keys := slices.Sorted(maps.Keys(obj))
		for _, k := range keys {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			ft, ok := fieldByJSONName(t, k)
			if !ok {
				out = append(out, path)
				continue
			}
			out = append(out, unknownPaths(obj[k], ft, path)...)
		}
	case reflect.Map:
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		for _, k := range slices.Sorted(maps.Keys(obj)) {
			out = append(out, unknownPaths(obj[k], t.Elem(), prefix+"."+k)...)
		}
	case reflect.Slice:
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil {
			return nil
		}
		for _, e := range arr {
			out = append(out, unknownPaths(e, t.Elem(), prefix+"[]")...)
		}
	}
	return slices.Compact(out)
}

func fieldByJSONName(t reflect.Type, name string) (reflect.Type, bool) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		n, _, _ := strings.Cut(sf.Tag.Get("json"), ",")
		if n == "-" {
			continue
		}
		if n == "" {
			n = sf.Name
		}
		if strings.EqualFold(n, name) {
			return sf.Type, true
		}
	}
	return nil, false
}

// decodePart decodes one raw part into v, strictly (unknown fields refused) or
// tolerantly (they are ignored). An absent part leaves v as it is.
func decodePart(path, part string, raw json.RawMessage, v any, strict bool) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return partErr(part, fmt.Errorf("rankfit: decode %s: %s: %w", path, part, err))
	}
	return nil
}

// Apply overlays f on base: the shares when f has them, and the calibration
// for the backends f names. Everything else is base's.
func (f File) Apply(base jobinsight.Weights) jobinsight.Weights {
	w := base
	if s := f.Shares; s != nil {
		w.Fit, w.Coverage, w.Semantic, w.Keyword, w.Title, w.Employer, w.KBMatch =
			s.Fit, s.Coverage, s.Semantic, s.Keyword, s.Title, s.Employer, s.KBMatch
	}
	if len(f.Calibration) > 0 {
		w.Calibration = maps.Clone(base.Calibration)
		if w.Calibration == nil {
			w.Calibration = make(map[string]jobinsight.Calibration, len(f.Calibration))
		}
		maps.Copy(w.Calibration, f.Calibration)
	}
	if len(f.SpareSeniorityMismatch) > 0 {
		w.SpareSeniorityMismatch = slices.Clone(f.SpareSeniorityMismatch)
	}
	return w
}

// checkSpareSeniority refuses a spared level that is not one of the triage
// seniority slot's options, a duplicate, or "manager": sparing a people-manager
// role from a seniority mismatch is not what the relaxation is for, and the
// owner's live drop rule removes managers.
func checkSpareSeniority(levels []string) error {
	opts := jdtriage.SlotValues()["seniority"]
	seen := map[string]bool{}
	for _, v := range levels {
		switch {
		case !slices.Contains(opts, v):
			return fmt.Errorf("rankfit: spared seniority %q is not one of %s", v, strings.Join(opts, ", "))
		case v == "manager":
			return errors.New("rankfit: manager cannot be spared from a seniority mismatch")
		case seen[v]:
			return fmt.Errorf("rankfit: spared seniority %q is listed twice", v)
		}
		seen[v] = true
	}
	return nil
}

// Write validates f and writes it to path atomically, creating its directory.
func Write(path string, f File) error {
	if err := f.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("rankfit: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("rankfit: %w", err)
	}
	return atomicfile.Write(path, append(b, '\n'))
}

// updateLockWait bounds how long Update waits for another writer.
const updateLockWait = 10 * time.Second

// Update is the one read-modify-write of a rank weights file. It takes the
// file's lock (path + ".lock", the convention filelock's other users follow),
// loads what is there, hands it to fn, validates the result and writes it
// atomically, so two writers each replacing their own part both survive. A
// missing file reaches fn as a zero File with exists false; a file that cannot
// be read or fails Load's checks is refused with an error and left as it is.
// An error from fn, a cancelled ctx (checked once the lock is held and again
// after fn) or an invalid result writes nothing and returns that error. An
// Update that created the directory in this call and then wrote nothing removes
// the lock file and every directory it created that is empty, never one that
// existed before.
//
// path is resolved once, leaf link included (atomicfile.Resolve), and that one
// path names the lock, the read and the write, so a link and its target are one
// file with one lock and a write goes through the link, keeping it.
func Update(ctx context.Context, path string, fn func(cur *File, exists bool) error) (err error) {
	path, err = atomicfile.Resolve(path)
	if err != nil {
		return fmt.Errorf("rankfit: %w", err)
	}
	made, err := mkdirAllNew(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("rankfit: %w", err)
	}
	locked, wrote := false, false
	defer func() {
		// Runs after the lock is released (deferred before it), so the lock file
		// can be removed on every platform.
		if wrote || len(made) == 0 {
			return
		}
		if locked {
			_ = os.Remove(path + ".lock")
		}
		for _, d := range made { // deepest first; Remove refuses a non-empty directory
			_ = os.Remove(d)
		}
	}()
	wctx, cancel := context.WithTimeout(ctx, updateLockWait)
	defer cancel()
	lk, err := filelock.Acquire(wctx, path+".lock")
	if err != nil {
		return fmt.Errorf("rankfit: lock %s: %w", path, err)
	}
	locked = true
	defer func() { _ = lk.Release() }()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("rankfit: %w", err)
	}
	cur, exists := File{}, true
	if cur, err = Load(path); errors.Is(err, fs.ErrNotExist) {
		cur, exists = File{}, false
	} else if err != nil {
		return fmt.Errorf("rankfit: %s exists and cannot be read, so it is left as it is: %w", path, err)
	}
	if err := fn(&cur, exists); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("rankfit: %w", err)
	}
	if err := Write(path, cur); err != nil {
		return err
	}
	wrote = true
	return nil
}

// mkdirAllNew is os.MkdirAll that also returns the directories it had to
// create, deepest first.
func mkdirAllNew(dir string) ([]string, error) {
	var made []string
	for d := dir; ; {
		if _, err := os.Stat(d); err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
		made = append(made, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return made, nil
}
