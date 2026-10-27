package feedback

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

func stealLog(t *testing.T, name string) (state, outside string, before []byte) {
	t.Helper()
	srcState := t.TempDir()
	if n := appendN(t, srcState, imp("gh:secret:1", 1)); n != 1 {
		t.Fatalf("wrote %d", n)
	}
	src := filepath.Join(Dir(srcState), ImpressionsFile)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.Rename(src, outside); err != nil {
		t.Fatal(err)
	}
	state = t.TempDir()
	if err := os.MkdirAll(Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(Dir(state), name)); err != nil {
		t.Fatal(err)
	}
	return state, outside, before
}

func TestReadImpressionsDoesNotFollowSymlink(t *testing.T) {
	for _, name := range []string{ImpressionsFile, RotatedFile} {
		t.Run(name, func(t *testing.T) {
			state, outside, before := stealLog(t, name)
			got, err := ReadImpressions(state)
			if err == nil && len(got.Rows) > 0 {
				t.Fatalf("ReadImpressions followed %s and returned %+v", name, got.Rows)
			}
			if err == nil && got.Skipped > 0 {
				t.Fatalf("ReadImpressions followed %s and skipped %d target lines", name, got.Skipped)
			}
			if !errors.Is(err, atomicfile.ErrLinkedLeaf) {
				t.Fatalf("ReadImpressions(%s) = %+v, %v; want ErrLinkedLeaf", name, got, err)
			}
			if b, rerr := os.ReadFile(outside); rerr != nil || !bytes.Equal(b, before) {
				t.Fatalf("target = %q %v", b, rerr)
			}
			st, lerr := os.Lstat(filepath.Join(Dir(state), name))
			if lerr != nil || st.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("leaf = %v %v, want a symlink", st, lerr)
			}
		})
	}
}
