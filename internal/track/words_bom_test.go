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

// A term keeps only its visible text, a UTF-16 file is read as text, and a UTF-16
// file cut mid-unit is refused by name only.
func TestLoadWordsInvisibleAndUTF16Forms(t *testing.T) {
	utf16be := "\xfe\xff\x00z\x00o\x00r\x00b\x00l\x00a\x00x\x00\n"
	for name, tc := range map[string]struct {
		body     string
		personal bool
		owner    string
		wantErr  bool
	}{
		"zwsp inside a term":  {"zor\u200bblax\n", true, "zorblax", false},
		"leading joiner term": {"\u2060zorblax\n", true, "zorblax", false},
		"utf16 be term":       {utf16be, true, "zorblax", false},
		"utf16 odd length":    {"\xff\xfe#\x00\x00", false, "", true},
		"invisible-only line": {"# c\n\u200b\u200d\n", false, "", true},
		"empty file":          {"", false, "", false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeTrack(t, dir, WordsFile, tc.body)
			w, err := LoadWords(dir)
			if tc.wantErr {
				if err == nil {
					t.Fatal("accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.Personal != tc.personal || (tc.owner != "" && (len(w.owner) != 1 || w.owner[0] != tc.owner)) {
				t.Fatalf("Personal=%v owner=%q", w.Personal, w.owner)
			}
		})
	}
}
