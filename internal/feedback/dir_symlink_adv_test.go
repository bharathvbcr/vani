package feedback

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAndAppendThroughSymlinkedStateDir(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if n := appendN(t, real, imp("gh:a:1", 1)); n != 1 {
		t.Fatalf("wrote %d", n)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := ReadImpressions(link)
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Key != "gh:a:1" {
		t.Fatalf("ReadImpressions via dir symlink = %+v %v", got, err)
	}
	if n := appendN(t, link, imp("gh:a:2", 2)); n != 1 {
		t.Fatalf("append via dir symlink wrote %d", n)
	}
	b, err := os.ReadFile(filepath.Join(Dir(real), ImpressionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadImpressions(real); err != nil {
		t.Fatal(err)
	}
	got, err = ReadImpressions(real)
	if err != nil || len(got.Rows) != 2 {
		t.Fatalf("real store = %+v %v (file %q)", got, err, b)
	}
}
