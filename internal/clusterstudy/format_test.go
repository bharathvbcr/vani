package clusterstudy

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func unit(dim, i int) []float32 {
	v := make([]float32, dim)
	v[i] = 1
	return v
}

func TestWriteVectorsLayout(t *testing.T) {
	dir := t.TempDir()
	vp, kp := filepath.Join(dir, "v.bin"), filepath.Join(dir, "k.txt")
	keys := []string{"gh:a:1", "lv:b:2", "ab:c:3"}
	rows := [][]float32{unit(4, 0), unit(4, 1), {0.5, 0.5, 0.5, 0.5}}
	if err := WriteVectors(vp, kp, keys, rows); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(vp)
	if err != nil {
		t.Fatal(err)
	}
	// The documented layout, read by hand rather than by this package.
	if string(raw[:8]) != "VANIPVEC" {
		t.Errorf("magic %q", raw[:8])
	}
	if v, n, d := binary.LittleEndian.Uint32(raw[8:]), binary.LittleEndian.Uint32(raw[12:]), binary.LittleEndian.Uint32(raw[16:]); v != 1 || n != 3 || d != 4 {
		t.Errorf("version %d n %d dim %d", v, n, d)
	}
	if len(raw) != 52+3*4*4 {
		t.Errorf("file is %d bytes, want %d", len(raw), 52+3*4*4)
	}
	kb, err := os.ReadFile(kp)
	if err != nil {
		t.Fatal(err)
	}
	if string(kb) != "gh:a:1\nlv:b:2\nab:c:3\n" {
		t.Errorf("keys file %q", kb)
	}
	if got := sha256.Sum256(kb); string(raw[20:52]) != string(got[:]) {
		t.Error("header digest is not the sha256 of the keys file")
	}
	for i, want := range []float32{1, 0, 0, 0, 0, 1, 0, 0, 0.5, 0.5, 0.5, 0.5} {
		if got := math.Float32frombits(binary.LittleEndian.Uint32(raw[52+4*i:])); got != want {
			t.Errorf("value %d = %v, want %v", i, got, want)
		}
	}
	back, err := ReadKeys(kp)
	if err != nil || len(back) != 3 || back[2] != "ab:c:3" {
		t.Errorf("ReadKeys = %v, %v", back, err)
	}
}

func TestWriteVectorsRefusesBadInputAndWritesNothing(t *testing.T) {
	nan := unit(3, 0)
	nan[1] = float32(math.NaN())
	inf := unit(3, 0)
	inf[2] = float32(math.Inf(1))
	for _, tc := range []struct {
		name string
		keys []string
		rows [][]float32
	}{
		{"no rows", nil, nil},
		{"fewer keys than rows", []string{"a:1"}, [][]float32{unit(3, 0), unit(3, 1)}},
		{"NaN", []string{"a:1"}, [][]float32{nan}},
		{"Inf", []string{"a:1"}, [][]float32{inf}},
		{"zero vector", []string{"a:1"}, [][]float32{make([]float32, 3)}},
		{"not unit length", []string{"a:1"}, [][]float32{{2, 0, 0}}},
		{"ragged", []string{"a:1", "a:2"}, [][]float32{unit(3, 0), unit(2, 0)}},
		{"zero width", []string{"a:1"}, [][]float32{{}}},
		{"over the width bound", []string{"a:1"}, [][]float32{append(make([]float32, MaxDim), 1)}},
		{"duplicate key", []string{"a:1", "a:1"}, [][]float32{unit(3, 0), unit(3, 1)}},
		{"empty key", []string{""}, [][]float32{unit(3, 0)}},
		{"newline in key", []string{"a\n1"}, [][]float32{unit(3, 0)}},
		{"carriage return in key", []string{"a\r1"}, [][]float32{unit(3, 0)}},
		{"NUL in key", []string{"a\x001"}, [][]float32{unit(3, 0)}},
		{"invalid UTF-8 key", []string{"a\xff1"}, [][]float32{unit(3, 0)}},
		{"oversized key", []string{strings.Repeat("k", MaxKeyBytes+1)}, [][]float32{unit(3, 0)}},
	} {
		dir := t.TempDir()
		err := WriteVectors(filepath.Join(dir, "v.bin"), filepath.Join(dir, "k.txt"), tc.keys, tc.rows)
		if !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", tc.name, err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Errorf("%s: refused export left %d files", tc.name, len(ents))
		}
	}
}

func TestWriteVectorsRefusesTooManyRows(t *testing.T) {
	keys := make([]string, MaxRows+1)
	rows := make([][]float32, MaxRows+1)
	for i := range keys {
		keys[i] = "k:" + strings.Repeat("x", 3)
	}
	dir := t.TempDir()
	if err := WriteVectors(filepath.Join(dir, "v"), filepath.Join(dir, "k"), keys, rows); !errors.Is(err, ErrFormat) {
		t.Fatalf("err = %v", err)
	}
}

func writeFile(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.assign")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAssignmentsRoundTrip(t *testing.T) {
	sum := KeysSHA256([]string{"a:1", "a:2", "a:3"})
	p := writeFile(t, EncodeAssignments(sum, 2, []uint32{0, 1, 1}))
	a, err := ReadAssignments(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.N != 3 || a.K != 2 || a.KeysSHA != sum || len(a.Labels) != 3 || a.Labels[1] != 1 {
		t.Errorf("read %+v", a)
	}
}

func TestReadAssignmentsRefusesWhatIsNotWhole(t *testing.T) {
	var sum [32]byte
	good := EncodeAssignments(sum, 2, []uint32{0, 1, 1})
	patch := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"empty file", nil},
		{"shorter than the header", good[:30]},
		{"truncated labels", good[:len(good)-2]},
		{"trailing bytes", append(append([]byte(nil), good...), 0, 0, 0, 0)},
		{"bad magic", patch(func(b []byte) { b[0] = 'X' })},
		{"bad version", patch(func(b []byte) { b[8] = 9 })},
		{"zero rows", EncodeAssignments(sum, 1, nil)},
		{"k of zero", patch(func(b []byte) { binary.LittleEndian.PutUint32(b[16:], 0) })},
		{"k over n", patch(func(b []byte) { binary.LittleEndian.PutUint32(b[16:], 4) })},
		{"label at k", EncodeAssignments(sum, 2, []uint32{0, 2, 1})},
		{"unassigned sentinel", EncodeAssignments(sum, 2, []uint32{0, math.MaxUint32, 1})},
		{"n larger than the file", patch(func(b []byte) { binary.LittleEndian.PutUint32(b[12:], 4_000_000_000) })},
		{"n over the bound", patch(func(b []byte) { binary.LittleEndian.PutUint32(b[12:], MaxRows+1) })},
	} {
		if _, err := ReadAssignments(writeFile(t, tc.raw)); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", tc.name, err)
		}
	}
	if _, err := ReadAssignments(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
	if _, err := ReadAssignments(t.TempDir()); !errors.Is(err, ErrFormat) {
		t.Errorf("a directory: err = %v", err)
	}
}

func TestReadKeysRefusesACutOrRepeatedFile(t *testing.T) {
	for name, content := range map[string]string{
		"empty":             "",
		"no final newline":  "a:1\na:2",
		"blank line":        "a:1\n\na:2\n",
		"duplicate":         "a:1\na:1\n",
		"carriage return":   "a:1\r\n",
		"too long a key":    strings.Repeat("k", MaxKeyBytes+1) + "\n",
		"invalid utf-8 key": "a:\xff\n",
	} {
		p := filepath.Join(t.TempDir(), "k.txt")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadKeys(p); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", name, err)
		}
	}
}
