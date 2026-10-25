package rkbresume

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// buildDirPrefix names every build folder. The sweep removes folders with this
	// prefix and nothing else.
	buildDirPrefix = "vani-resume-"
	// tmpDirName is the folder inside a build folder that rkb gets as TMPDIR, so
	// its Chrome profile and fit work folders live and die with the build.
	tmpDirName = "tmp"

	buildLockName  = "resume-build.lock"
	chromeLockName = "resume-chrome.lock"

	// maxSweep bounds how many entries of the work folder one sweep looks at.
	maxSweep = 1000
)

// makeBuildDir makes the private folder one build works in: under workDir when
// it is set (after removing the folders an earlier build left there), else in
// the system temp folder. It holds an empty tmp folder for rkb.
func makeBuildDir(workDir string, now time.Time) (string, error) {
	base := ""
	if workDir != "" {
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return "", err
		}
		sweepStale(workDir, MaxDeadline, now)
		base = workDir
	}
	dir, err := os.MkdirTemp(base, buildDirPrefix)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(filepath.Join(dir, tmpDirName), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// sweepStale removes the build folders in workDir that were last changed more
// than older ago: a build never outlives MaxDeadline, so such a folder belongs
// to a build that was killed, and holds the page it was rendering (with the
// owner's header). Only real directories whose names carry buildDirPrefix are
// touched; a symlink is neither followed nor removed. It is best effort and
// returns how many folders it removed.
func sweepStale(workDir string, older time.Duration, now time.Time) int {
	d, err := os.Open(workDir)
	if err != nil {
		return 0
	}
	defer d.Close()
	entries, _ := d.ReadDir(maxSweep)
	removed := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), buildDirPrefix) || e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.IsDir() || now.Sub(info.ModTime()) <= older {
			continue
		}
		if os.RemoveAll(filepath.Join(workDir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}
