package rankfit

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func goodFile() File {
	return File{V: FileVersion, FittedAt: t0,
		Shares:      &Shares{Fit: 0.2, Coverage: 0.5, Semantic: 0.2, Keyword: 0.01, Title: 0.05, Employer: 0.04, KBMatch: 0.1},
		Calibration: map[string]jobinsight.Calibration{"d1-ultra": {A: 0.8, B: -0.3, UsesP: true}, "claude": {B: 1.2}},
		Report:      &Report{Labels: 200, Judged: 160}}
}

func TestFileRoundTripAndApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rank", "weights.json")
	if err := Write(path, goodFile()); err != nil {
		t.Fatal(err)
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	base := jobinsight.DefaultWeights()
	base.Calibration = map[string]jobinsight.Calibration{"lappi": {B: 2}}
	w := f.Apply(base)
	if w.Coverage != 0.5 || w.KBMatch != 0.1 || w.Fit != 0.2 {
		t.Errorf("shares not applied: %+v", w)
	}
	if w.UnknownPrior != base.UnknownPrior || w.HalfLifeDays != base.HalfLifeDays || w.AnsweredConf != base.AnsweredConf {
		t.Errorf("Apply changed constants the file does not carry")
	}
	if len(w.Calibration) != 3 || w.Calibration["claude"].B != 1.2 || w.Calibration["lappi"].B != 2 {
		t.Errorf("calibration %+v", w.Calibration)
	}
	if len(base.Calibration) != 1 {
		t.Errorf("Apply wrote into base's calibration map: %+v", base.Calibration)
	}

	// A calibration-only file leaves the hand shares.
	f.Shares = nil
	if w := f.Apply(jobinsight.DefaultWeights()); w.Semantic != jobinsight.DefaultWeights().Semantic {
		t.Errorf("calibration-only file changed the shares")
	}
}

func TestLoadRefusesDefectiveFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","shares":{"fit":0.3,"coverage":0.2,"semantic":0.35,"keyword":0.03,"title":0.1,"employer":0.05,"kb_match":0.15}}`
	if _, err := Load(write("ok.json", ok)); err != nil {
		t.Fatalf("a good file: %v", err)
	}
	for name, body := range map[string]string{
		"empty":            ``,
		"not json":         `weights`,
		"version 2":        strings.Replace(ok, `"v":1`, `"v":2`, 1),
		"no version":       strings.Replace(ok, `"v":1,`, ``, 1),
		"no fitted_at":     strings.Replace(ok, `"fitted_at":"2026-10-20T12:00:00Z",`, ``, 1),
		"nothing fitted":   `{"v":1,"fitted_at":"2026-10-20T12:00:00Z"}`,
		"negative share":   strings.Replace(ok, `"fit":0.3`, `"fit":-0.3`, 1),
		"huge share":       strings.Replace(ok, `"fit":0.3`, `"fit":1e300`, 1),
		"overflow share":   strings.Replace(ok, `"fit":0.3`, `"fit":1e400`, 1),
		"string share":     strings.Replace(ok, `"fit":0.3`, `"fit":"NaN"`, 1),
		"all zero":         `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","shares":{"fit":0,"coverage":0,"semantic":0,"keyword":0,"title":0,"employer":0,"kb_match":0}}`,
		"unknown field":    strings.Replace(ok, `"v":1,`, `"v":1,"gate_exp":2,`, 1),
		"trailing data":    ok + `{}`,
		"two documents":    ok + "\n" + ok,
		"runaway calib":    `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","calibration":{"d1-ultra":{"a":80,"b":0,"uses_p":true}}}`,
		"empty backend":    `{"v":1,"fitted_at":"2026-10-20T12:00:00Z","calibration":{" ":{"a":1,"b":0,"uses_p":true}}}`,
		"control backend":  "{\"v\":1,\"fitted_at\":\"2026-10-20T12:00:00Z\",\"calibration\":{\"d1\\u001b\":{\"a\":1,\"b\":0,\"uses_p\":true}}}",
		"oversized":        ok[:len(ok)-1] + `,"report":{"labels":1,"platt_refused":{"` + strings.Repeat("x", MaxFileBytes) + `":1}}}`,
	} {
		if _, err := Load(write(strings.ReplaceAll(name, " ", "_")+".json", body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestWriteRefusesAnInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.json")
	f := goodFile()
	f.Shares.Semantic = math.NaN()
	if err := Write(path, f); err == nil {
		t.Fatal("wrote a NaN share")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a refused write left a file: %v", err)
	}
}
