// Package clusterstudy is the offline test of whether clustering posting
// embeddings gives better audit strata than the ones the audit uses now
// (docs/CLUSTER_STUDY.md). It holds the file formats Vani and the separate
// ojas-cluster process exchange, and the within-stratum variance measure. It
// changes no audit draw and writes no label.
//
// The process contract is plain files, so the Rust clustering engine and Vani
// stay separately versioned and no binding is needed:
//
//	vectors file, written here, read by the fit binary
//	  magic     8 bytes  "VANIPVEC"
//	  version   u32 LE   1
//	  n         u32 LE   rows
//	  dim       u32 LE   columns
//	  keys_sha  32 bytes sha256 of the keys file, byte for byte
//	  rows      n*dim f32 LE, row-major, each row unit length
//
//	keys file: n lines, "<posting key>\n", in row order.
//
//	assignments file, written by the fit binary, read here
//	  magic     8 bytes  "VANIASGN"
//	  version   u32 LE   1
//	  n         u32 LE   rows
//	  k         u32 LE   clusters
//	  keys_sha  32 bytes copied from the vectors file
//	  labels    n u32 LE, each < k
//
// Every reader checks that the file is exactly the size its header implies and
// refuses a header over the bounds below before it allocates.
package clusterstudy

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

const (
	VectorsMagic     = "VANIPVEC"
	AssignmentsMagic = "VANIASGN"
	FormatVersion    = 1

	// MaxRows bounds one export. The chunk-vector log holds a few thousand
	// postings (semantic.MaxVectorBytes); this leaves room and no more.
	MaxRows = 200_000
	// MaxDim bounds a vector's width; it is the semantic package's own bound.
	MaxDim = 8192
	// MaxVectorsBytes bounds the rows of one vectors file.
	MaxVectorsBytes = 512 << 20
	// MaxKeyBytes bounds one posting key.
	MaxKeyBytes = 512

	vectorsHeaderLen     = 8 + 4 + 4 + 4 + sha256.Size
	assignmentsHeaderLen = 8 + 4 + 4 + 4 + sha256.Size

	// unitTolerance is how far from 1 a row's length may be: the pooled vectors
	// are normalised in float64 and rounded to float32.
	unitTolerance = 1e-3
)

// ErrFormat marks a file that is not a valid file of its kind.
var ErrFormat = errors.New("clusterstudy: invalid file")

func formatErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrFormat, fmt.Sprintf(format, a...))
}

// checkKey refuses a key that could not sit on one line of the keys file.
func checkKey(k string) error {
	switch {
	case k == "" || len(k) > MaxKeyBytes:
		return formatErr("key of %d bytes", len(k))
	case !utf8.ValidString(k) || strings.ContainsAny(k, "\n\r\x00"):
		return formatErr("key %q is not one line of valid text", truncateKey(k))
	}
	return nil
}

func truncateKey(k string) string {
	if len(k) > 40 {
		return k[:40] + "..."
	}
	return k
}

// KeysSHA256 is the digest that ties a vectors file and its assignments to one
// keys file.
func KeysSHA256(keys []string) [sha256.Size]byte {
	h := sha256.New()
	for _, k := range keys {
		_, _ = io.WriteString(h, k)
		_, _ = h.Write([]byte{'\n'})
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// WriteVectors writes the keys file and the vectors file, each atomically. A
// row that is not finite, not unit length or not dim wide, a duplicate or a
// bad key, no rows, or a size over the bounds refuses the whole export:
// nothing is written.
func WriteVectors(vectorsPath, keysPath string, keys []string, rows [][]float32) error {
	n := len(keys)
	switch {
	case n == 0:
		return formatErr("no rows to write")
	case n != len(rows):
		return formatErr("%d keys for %d rows", n, len(rows))
	case n > MaxRows:
		return formatErr("%d rows, over the bound of %d", n, MaxRows)
	}
	dim := len(rows[0])
	if dim == 0 || dim > MaxDim {
		return formatErr("vectors of width %d (bound %d)", dim, MaxDim)
	}
	if int64(n)*int64(dim)*4 > MaxVectorsBytes {
		return formatErr("%d rows of width %d are over the %d byte bound", n, dim, int64(MaxVectorsBytes))
	}
	seen := make(map[string]struct{}, n)
	for i, k := range keys {
		if err := checkKey(k); err != nil {
			return err
		}
		if _, dup := seen[k]; dup {
			return formatErr("key %q appears twice", truncateKey(k))
		}
		seen[k] = struct{}{}
		if err := checkRow(rows[i], dim); err != nil {
			return formatErr("row %d: %v", i, err)
		}
	}
	sum := KeysSHA256(keys)
	var kb bytes.Buffer
	for _, k := range keys {
		kb.WriteString(k)
		kb.WriteByte('\n')
	}
	vb := make([]byte, 0, vectorsHeaderLen+n*dim*4)
	vb = append(vb, VectorsMagic...)
	vb = binary.LittleEndian.AppendUint32(vb, FormatVersion)
	vb = binary.LittleEndian.AppendUint32(vb, uint32(n))
	vb = binary.LittleEndian.AppendUint32(vb, uint32(dim))
	vb = append(vb, sum[:]...)
	for _, r := range rows {
		for _, x := range r {
			vb = binary.LittleEndian.AppendUint32(vb, math.Float32bits(x))
		}
	}
	if err := atomicfile.Write(keysPath, kb.Bytes()); err != nil {
		return fmt.Errorf("clusterstudy: write keys: %w", err)
	}
	if err := atomicfile.Write(vectorsPath, vb); err != nil {
		return fmt.Errorf("clusterstudy: write vectors: %w", err)
	}
	return nil
}

// checkRow refuses a row that is the wrong width, holds a non-finite value or
// is not unit length (a zero row included).
func checkRow(r []float32, dim int) error {
	if len(r) != dim {
		return fmt.Errorf("width %d, want %d", len(r), dim)
	}
	var sq float64
	for _, x := range r {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return errors.New("non-finite value")
		}
		sq += f * f
	}
	if math.Abs(math.Sqrt(sq)-1) > unitTolerance {
		return fmt.Errorf("length %g, want 1", math.Sqrt(sq))
	}
	return nil
}

// Assignments is a fit's cluster label for each row.
type Assignments struct {
	N, K    int
	KeysSHA [sha256.Size]byte
	Labels  []uint32
}

// ReadAssignments reads an assignments file. The file must be exactly the size
// its header gives, every label below k, and k within 1..n.
func ReadAssignments(path string) (Assignments, error) {
	f, err := os.Open(path)
	if err != nil {
		return Assignments{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Assignments{}, err
	}
	if !st.Mode().IsRegular() {
		return Assignments{}, formatErr("%s is not a regular file", path)
	}
	if st.Size() < assignmentsHeaderLen {
		return Assignments{}, formatErr("%s is %d bytes, shorter than its %d byte header", path, st.Size(), assignmentsHeaderLen)
	}
	hdr := make([]byte, assignmentsHeaderLen)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return Assignments{}, err
	}
	if string(hdr[:8]) != AssignmentsMagic {
		return Assignments{}, formatErr("%s is not an assignments file", path)
	}
	if v := binary.LittleEndian.Uint32(hdr[8:]); v != FormatVersion {
		return Assignments{}, formatErr("%s has version %d, want %d", path, v, FormatVersion)
	}
	n := int64(binary.LittleEndian.Uint32(hdr[12:]))
	k := int64(binary.LittleEndian.Uint32(hdr[16:]))
	switch {
	case n == 0 || n > MaxRows:
		return Assignments{}, formatErr("%s: %d rows (bound %d)", path, n, MaxRows)
	case k == 0 || k > n:
		return Assignments{}, formatErr("%s: k=%d for %d rows", path, k, n)
	case st.Size() != int64(assignmentsHeaderLen)+4*n:
		return Assignments{}, formatErr("%s is %d bytes, its header says %d", path, st.Size(), int64(assignmentsHeaderLen)+4*n)
	}
	a := Assignments{N: int(n), K: int(k), Labels: make([]uint32, n)}
	copy(a.KeysSHA[:], hdr[20:])
	body := make([]byte, 4*n)
	if _, err := io.ReadFull(f, body); err != nil {
		return Assignments{}, err
	}
	for i := range a.Labels {
		l := binary.LittleEndian.Uint32(body[4*i:])
		if int64(l) >= k {
			return Assignments{}, formatErr("%s: row %d has label %d, k is %d", path, i, l, k)
		}
		a.Labels[i] = l
	}
	return a, nil
}

// EncodeAssignments is the assignments file a fit binary writes; it exists so
// tests (and any Go clusterer) produce the one format.
func EncodeAssignments(keysSHA [sha256.Size]byte, k int, labels []uint32) []byte {
	b := make([]byte, 0, assignmentsHeaderLen+4*len(labels))
	b = append(b, AssignmentsMagic...)
	b = binary.LittleEndian.AppendUint32(b, FormatVersion)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(labels)))
	b = binary.LittleEndian.AppendUint32(b, uint32(k))
	b = append(b, keysSHA[:]...)
	for _, l := range labels {
		b = binary.LittleEndian.AppendUint32(b, l)
	}
	return b
}

// ReadKeys reads a keys file: one valid, distinct key per line.
func ReadKeys(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > int64(MaxRows)*(MaxKeyBytes+1) {
		return nil, formatErr("%s is not a regular file within the bound", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return nil, formatErr("%s does not end in a newline (cut short?)", path)
	}
	keys := strings.Split(string(raw[:len(raw)-1]), "\n")
	if len(keys) > MaxRows {
		return nil, formatErr("%s holds %d keys (bound %d)", path, len(keys), MaxRows)
	}
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if err := checkKey(k); err != nil {
			return nil, err
		}
		if _, dup := seen[k]; dup {
			return nil, formatErr("key %q appears twice", truncateKey(k))
		}
		seen[k] = struct{}{}
	}
	return keys, nil
}
