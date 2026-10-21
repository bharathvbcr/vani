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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
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
	V           int                               `json:"v"`
	FittedAt    time.Time                         `json:"fitted_at"`
	Shares      *Shares                           `json:"shares,omitempty"`
	Calibration map[string]jobinsight.Calibration `json:"calibration,omitempty"`
	Report      *Report                           `json:"report,omitempty"`
}

// Validate refuses a file Apply could not use whole.
func (f File) Validate() error {
	if f.V != FileVersion {
		return fmt.Errorf("rankfit: file version %d, want %d", f.V, FileVersion)
	}
	if f.FittedAt.IsZero() {
		return errors.New("rankfit: file has no fitted_at")
	}
	if f.Shares == nil && len(f.Calibration) == 0 {
		return errors.New("rankfit: file has neither shares nor calibration")
	}
	if f.Shares != nil {
		sum := 0.0
		for i, v := range f.Shares.Array() {
			if math.IsNaN(v) || v < 0 || v > maxShare {
				return fmt.Errorf("rankfit: share %s is %v, want 0..%g", PartNames[i], v, float64(maxShare))
			}
			sum += v
		}
		if !(sum > 0) {
			return errors.New("rankfit: every share is 0")
		}
	}
	if len(f.Calibration) > MaxCalibrations {
		return fmt.Errorf("rankfit: %d calibrations, max %d", len(f.Calibration), MaxCalibrations)
	}
	for name, c := range f.Calibration {
		if strings.TrimSpace(name) == "" || len(name) > MaxBackendNameBytes || strings.ContainsFunc(name, unicode.IsControl) {
			return fmt.Errorf("rankfit: calibration backend name %q is empty, too long or has a control character", name)
		}
		if !c.Valid() {
			return fmt.Errorf("rankfit: calibration for %q is not finite or exceeds |%g|", name, jobinsight.MaxCalibrationCoef)
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
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("rankfit: decode %s: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return File{}, fmt.Errorf("rankfit: %s has data after the file", path)
	}
	if err := f.Validate(); err != nil {
		return File{}, err
	}
	return f, nil
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
	return w
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
