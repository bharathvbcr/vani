package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// HeldoutExportVersion is the "v" of every owner_labels.jsonl line.
const HeldoutExportVersion = 1

// MaxNormalizedBytes caps the text that is normalised and hashed. It is
// qd_data.minhash.DEFAULT_MAX_DOC_BYTES (262,144) in Lappi-decision, so a
// posting longer than that hashes its first 262,144 bytes on both sides.
const MaxNormalizedBytes = 262_144

// ErrEmptyText is returned for text with nothing in it after normalisation.
// Every empty posting would share one hash, so an empty text is refused
// rather than exported as a key that matches every other empty one.
var ErrEmptyText = errors.New("eval: posting text is empty after normalisation")

// isSpace is Python's str.isspace, which is what qd_data's regexp \s matches
// on a str: Go's unicode.IsSpace (ASCII space and C0 whitespace, U+0085,
// U+00A0, U+1680, U+2000-200A, U+2028/9, U+202F, U+205F, U+3000) plus the
// four separators U+001C-U+001F that Python also counts and Go does not.
func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// NormalizeText is the one definition of a posting's normalised text for the
// held-out export. It matches Lappi-decision's
// python/qd_data/minhash.py shingle() preprocessing, in the same order:
//
//  1. drop every byte that is not part of a valid UTF-8 sequence, as
//     bytes.decode("utf-8", errors="ignore") does: a lone continuation byte,
//     an invalid lead byte, an overlong or surrogate encoding, and a
//     truncated multi-byte sequence all vanish (nothing is substituted);
//  2. cut the valid bytes at MaxNormalizedBytes (encode, slice, then
//     decode("utf-8", errors="ignore") in Python), so a sequence the cap
//     splits is dropped whole;
//  3. replace every run of Unicode whitespace (isSpace) with one space;
//  4. trim leading and trailing space.
//
// Steps 1 and 2 are one pass: invalid bytes cost no budget, as in Python,
// where they are gone before the cap is applied. Case is kept and nothing
// else is changed. Text that is empty after step 4 returns ErrEmptyText.
func NormalizeText(text string) (string, error) {
	var b strings.Builder
	b.Grow(min(len(text), MaxNormalizedBytes))
	pendingSpace := false
	valid := 0 // bytes of valid UTF-8 consumed so far, spaces included
	for i := 0; i < len(text); {
		r, w := utf8.DecodeRuneInString(text[i:])
		i += w
		if r == utf8.RuneError && w == 1 {
			continue // invalid byte: dropped, not replaced
		}
		if valid += w; valid > MaxNormalizedBytes {
			break // the cap splits this rune: it and the rest are cut
		}
		if isSpace(r) {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "", ErrEmptyText
	}
	return b.String(), nil
}

// NormalizedTextSHA256 is the lower-case hex sha256 of NormalizeText(text).
func NormalizedTextSHA256(text string) (string, error) {
	n, err := NormalizeText(text)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:]), nil
}

// Signer is the seam for a MinHash signature of a posting's normalised text,
// to be exported next to the hash once Lappi and the owner agree the format
// (5-shingle MinHash against 8-gram containment at 0.5 is still open, and the
// hash family is blake2b). Nothing implements it and the export writes no
// "sig" field, so no reader can come to depend on a signature whose
// definition may change.
type Signer interface {
	Sign(normalized string) ([]byte, error)
}

// HeldoutRow is one owner_labels.jsonl line. It carries a key and a hash and
// nothing else: never text, vault, headers or marks notes.
type HeldoutRow struct {
	V          int         `json:"v"`
	Key        posting.Key `json:"key"`
	TextSHA256 string      `json:"text_sha256"`
}

// HeldoutExport is a built export: the file's bytes and what could not be in
// it.
type HeldoutExport struct {
	// Data is the file content: one JSON line per row, sorted by key, so the
	// same labels and texts always give the same bytes.
	Data []byte
	// Rows is the number of lines in Data.
	Rows int
	// Missing lists labelled keys left out because no usable text was found
	// (absent from the ledger, body evicted with no cached copy, or empty).
	// A held-out set that silently lacks a key would let it leak into
	// training, so callers must report these.
	Missing []posting.Key
}

// BuildHeldoutExport builds the export for every posting in the label store.
// Owner labels are every entry in the store: a label is only ever written
// from the owner's answer, whether given by `vani label` (Source empty), the
// weekly audit (Source is the stratum, RANKING_PLAN Phase 1) or by hand
// (SourceManual), so audit-drawn labels count, and so does an entry whose
// slots are all "skip" (the owner saw the posting; keeping it out of
// training is the safe side). ledgerText returns the posting's description
// from the ledger; when it has none the label's cached copy (Entry.TextSHA256)
// is used.
func BuildHeldoutExport(s *Store, ledgerText func(posting.Key) (string, bool)) (HeldoutExport, error) {
	all, bad, err := s.Labels()
	if err != nil {
		return HeldoutExport{}, err
	}
	if bad > 0 {
		return HeldoutExport{}, fmt.Errorf("eval: %d malformed label lines; refusing to export a partial held-out set", bad)
	}
	keys := make([]posting.Key, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var out HeldoutExport
	var buf bytes.Buffer
	for _, k := range keys {
		text, ok := "", false
		if ledgerText != nil {
			text, ok = ledgerText(k)
		}
		if !ok || strings.TrimSpace(text) == "" {
			if h := all[k].TextSHA256; h != "" {
				if t, err := s.Text(h); err == nil {
					text, ok = t, true
				}
			}
		}
		h, err := NormalizedTextSHA256(text)
		if !ok || err != nil {
			out.Missing = append(out.Missing, k)
			continue
		}
		line, err := json.Marshal(HeldoutRow{V: HeldoutExportVersion, Key: k, TextSHA256: h})
		if err != nil {
			return HeldoutExport{}, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		out.Rows++
	}
	out.Data = buf.Bytes()
	return out, nil
}

// WriteHeldoutExport replaces path with the export atomically
// (internal/atomicfile: temp file, fsync, rename; a symlink at path is
// replaced, never written through).
func WriteHeldoutExport(path string, x HeldoutExport) error {
	return atomicfile.Write(path, x.Data)
}

// exportLockWait bounds how long an export waits for another one; a variable
// only so a test need not wait it out.
var exportLockWait = 30 * time.Second

// ErrExportBusy is returned when another export held the export lock for the
// whole of exportLockWait.
var ErrExportBusy = errors.New("eval: another held-out export is running")

// ExportHeldoutLocked builds the export from s and replaces path with it,
// with exports serialised on an flock beside the output (the resolved path
// plus ".lock", the repo's convention: the file itself is replaced by rename,
// so a lock on it would be lost). The label snapshot is read inside the lock,
// so of two racing exports whichever writes last has read the freshest labels
// and no older snapshot can land over a newer one. ledgerText is as in
// BuildHeldoutExport and is called inside the lock.
func ExportHeldoutLocked(path string, s *Store, ledgerText func(posting.Key) (string, bool)) (HeldoutExport, error) {
	target, err := atomicfile.ResolveLeaf(path)
	if err != nil {
		return HeldoutExport{}, fmt.Errorf("eval: export path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return HeldoutExport{}, fmt.Errorf("eval: export dir: %w", err)
	}
	f, err := os.OpenFile(target+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return HeldoutExport{}, fmt.Errorf("eval: open export lock: %w", err)
	}
	defer f.Close()
	deadline := time.Now().Add(exportLockWait)
	for {
		err := filelock.TryLock(f, true)
		if err == nil {
			break
		}
		if !errors.Is(err, filelock.ErrWouldBlock) {
			return HeldoutExport{}, fmt.Errorf("eval: lock export: %w", err)
		}
		if !time.Now().Before(deadline) {
			return HeldoutExport{}, ErrExportBusy
		}
		time.Sleep(lockRetry)
	}
	defer func() { _ = filelock.Unlock(f) }()
	x, err := BuildHeldoutExport(s, ledgerText)
	if err != nil {
		return HeldoutExport{}, err
	}
	if err := WriteHeldoutExport(target, x); err != nil {
		return HeldoutExport{}, err
	}
	return x, nil
}
