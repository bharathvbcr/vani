package track

import "testing"

// An editor's "UTF-8 with BOM" puts U+FEFF before the first line. It must not
// hide a leading '#': a comment-only file has zero terms, exactly as an empty
// one does, and a real first term loses the mark.
func TestLoadWordsByteOrderMark(t *testing.T) {
	const bom = "\ufeff"
	for name, tc := range map[string]struct {
		body  string
		owner []string
	}{
		"empty":                {"", nil},
		"bom only":             {bom, nil},
		"bom comment":          {bom + "# my list\n", nil},
		"bom comments":         {bom + "# one\n# two\r\n\n  # three\n", nil},
		"bom one term":         {bom + "zorblax\n", []string{"zorblax"}},
		"bom comment and term": {bom + "# list\nzorblax\n", []string{"zorblax"}},
		"bom spaced term":      {bom + "  Example   Corp \n", []string{"example corp"}},
		"no bom comment":       {"# my list\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeTrack(t, dir, WordsFile, tc.body)
			w, err := LoadWords(dir)
			if err != nil && name == "bom only" {
				t.Fatalf("a file of only a BOM is an empty file, not an error: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.Personal != (len(tc.owner) > 0) || w.Missing {
				t.Fatalf("personal=%v missing=%v, want personal=%v", w.Personal, w.Missing, len(tc.owner) > 0)
			}
			if len(w.owner) != len(tc.owner) {
				t.Fatalf("owner terms %q, want %q", w.owner, tc.owner)
			}
			for i := range tc.owner {
				if w.owner[i] != tc.owner[i] {
					t.Errorf("term %d = %q, want %q", i, w.owner[i], tc.owner[i])
				}
			}
		})
	}
}
