package feedback

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendAfterTornTailKeepsTheNewRow(t *testing.T) {
	for name, torn := range map[string]string{
		"partial row after a whole one": "",
		"file is only a partial line":   "only",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(Dir(dir), ImpressionsFile)
			if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
				t.Fatal(err)
			}
			frag := `{"v":1,"at":"2026-10-20T09:00:00Z","surface":"dig`
			if torn == "" {
				appendN(t, dir, imp("gh:a:1", 1))
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(frag); err != nil {
				t.Fatal(err)
			}
			f.Close()
			want := 1
			if torn == "" {
				want = 2
			}
			appendN(t, dir, imp("gh:a:2", 1))
			got, err := ReadImpressions(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rows) != want || got.Skipped != 1 {
				t.Fatalf("rows %d skipped %d, want %d and exactly 1 (only the fragment)", len(got.Rows), got.Skipped, want)
			}
			if got.Rows[len(got.Rows)-1].Key != "gh:a:2" {
				t.Fatalf("the appended row was lost: %+v", got.Rows)
			}
		})
	}
}

func TestAppendToEmptyFileAddsNoLeadingNewline(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(dir), ImpressionsFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	appendN(t, dir, imp("gh:a:1", 1))
	b, err := os.ReadFile(filepath.Join(Dir(dir), ImpressionsFile))
	if err != nil || len(b) == 0 || b[0] == '\n' {
		t.Fatalf("an empty log must not start with a blank line: %q %v", b, err)
	}
}
