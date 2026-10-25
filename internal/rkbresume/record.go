package rkbresume

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// The types below map one to one onto the version record the resume store
// keeps (internal/resumestore.Meta), under the same JSON tags, so a Result
// marshals into a Meta without a translation step. rkbresume does not import
// the store; the CLI copies the fields across. None of them can hold personal
// data: ids, counts, hashes and fixed words only.

// Sources of Result.Source. A version made from a model's choice is "tailored",
// one built from the owner's base selection is "base", one the owner edited
// and Render built is "edited".
const (
	SourceTailored = "tailored"
	SourceBase     = "base"
	SourceEdited   = "edited"
)

// Check states. A check that did not run is never one that passed.
const (
	checkCurrent     = "current"
	checkPassed      = "passed"
	checkFailed      = "failed"
	checkSoft        = "soft"
	checkNotRun      = "not_run"
	checkCouldNotRun = "could_not_run"
	checkVerified    = "verified"
	checkDropped     = "dropped"
	checkUnknown     = "unknown"
)

// Cost is what a build's model turns cost: the sum of every turn that was sent.
// Complete is false when any sent turn's cost is unknown (no usage came back,
// the turn timed out or was cancelled, or the provider has no price), and USD
// is then a lower bound; unknown is never counted as zero.
type Cost struct {
	USD      float64 `json:"usd"`
	Complete bool    `json:"complete"`
	Turns    int     `json:"turns"`
}

// Checks says how each check of a build ended: Eligibility "current" (this
// build judged it on the live bank) or "unknown" (it never got that far);
// Lint "passed", "failed" or "not_run"; Fit "passed", "soft" (a layout note was
// accepted), "failed", "not_run" or "could_not_run" (Chrome, poppler or time);
// Figures "verified", "dropped" (some bullets were left off because rkb has
// not double-verified a figure in them) or "not_run".
type Checks struct {
	Eligibility string `json:"eligibility"`
	Lint        string `json:"lint"`
	Fit         string `json:"fit"`
	Figures     string `json:"figures"`
}

// notRun is the Checks of a build that has run nothing yet.
func notRun() Checks {
	return Checks{Eligibility: checkUnknown, Lint: checkNotRun, Fit: checkNotRun, Figures: checkNotRun}
}

// nothing is the Result of an error before the build began: nothing ran and
// nothing was spent (a complete cost of zero, not an unknown one).
func nothing() Result {
	return Result{Cost: Cost{Complete: true}, Checks: notRun()}
}

// BulletRef names a bullet on the page and the wording it was judged on: the
// 12-hex sha1 of its resolved plain text, the form rkb's reviews record.
type BulletRef struct {
	ID       string `json:"id"`
	TextHash string `json:"text_hash"`
}

// Fingerprints name the exact inputs a build read, each a sha256 in hex of the
// bytes it read (the bank's is of the snapshot rkb was given): later, a stored
// version is stale when one of them no longer matches. Each is a 64-hex sha256 or
// "" (what the store accepts). VMReport is "" only when no validator report is
// configured: a named one that cannot be read fails the build (CodeConfig).
// RKB is "" when the binary could not be read, which only a Runner that is not
// the real one can survive (the real one could not run it).
type Fingerprints struct {
	Bank          string `json:"bank"`
	BaseSelection string `json:"base_selection"`
	Selections    string `json:"selections"`
	VMReport      string `json:"vm_report"`
	Claims        string `json:"claims"`
	RKB           string `json:"rkb"`
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// maxFingerprintFile bounds one file read only to be hashed.
const maxFingerprintFile = 512 << 20

// fileSHA256 streams path through sha256, reading at most max bytes (a longer
// file is an error, not a hash of its start).
func fileSHA256(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", os.ErrInvalid
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	if err != nil {
		return "", err
	}
	if n > max {
		return "", os.ErrInvalid
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// claimsDigest is the sha256 of the claim ids and values read at load, sorted
// by id and written as JSON pairs, so no value can run into the next id.
func claimsDigest(c kbsync.Claims) string {
	ids := make([]string, 0, len(c.Values))
	for id := range c.Values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	pairs := make([][2]string, len(ids))
	for i, id := range ids {
		pairs[i] = [2]string{id, c.Values[id]}
	}
	raw, _ := json.Marshal(pairs)
	return sha256Hex(raw)
}

// selectionsDigest is the sha256 of the selection paths, sorted, each with the
// sha256 of the bytes that were parsed (kbsync.Selection.SHA256).
func selectionsDigest(sels []kbsync.Selection) string {
	pairs := make([][2]string, len(sels))
	for i, s := range sels {
		pairs[i] = [2]string{s.Path, s.SHA256}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	raw, _ := json.Marshal(pairs)
	return sha256Hex(raw)
}

// rkbFingerprint is the sha256 of the rkb binary, or "" when it cannot be read.
func rkbFingerprint(path string) string {
	sum, err := fileSHA256(path, maxFingerprintFile)
	if err != nil {
		return ""
	}
	return sum
}

// bulletRefs lists every bullet of ch in page order with the hash of its wording
// as rkb's renderer would print it.
func (b *builder) bulletRefs(ch choice) []BulletRef {
	ids := ch.bulletIDs()
	refs := make([]BulletRef, 0, len(ids))
	for _, id := range ids {
		plain := kbsync.RKBPlain(b.bank.Bullets[id].Text, b.claims.Values)
		refs = append(refs, BulletRef{ID: id, TextHash: kbsync.WordingHash(plain)})
	}
	return refs
}

// maxFitLines bounds a per-bullet line count taken from rkb's report: a bullet
// is a few lines, and anything past this is not a measurement.
const maxFitLines = 50

// fitLinesOf is the per-bullet rendered line count from rkb's fit report, for
// the bullets of ch only. Only ids and counts are read (the report's own text
// field is never decoded), an id that is not a bullet of ch is dropped, and a
// count outside 1..maxFitLines is not a measurement. Nil when the report has
// none (Chrome's measurement failed).
func fitLinesOf(rep fitReport, ch choice) map[string]int {
	on := map[string]bool{}
	for _, id := range ch.bulletIDs() {
		on[id] = true
	}
	var lines map[string]int
	for _, m := range rep.Bullets {
		if !on[m.ID] || m.Lines < 1 || m.Lines > maxFitLines {
			continue
		}
		if _, dup := lines[m.ID]; dup {
			continue
		}
		if lines == nil {
			lines = map[string]int{}
		}
		lines[m.ID] = m.Lines
	}
	return lines
}
