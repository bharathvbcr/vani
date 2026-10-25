package resumestore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// PDFPath names the file Save wrote, absolute, under the state dir's resumes
// folder, and never a path a key could steer.
func TestPDFPathIsTheFileSaveWrote(t *testing.T) {
	s, state := newStore(t)
	pdf := pdfBytes("path")
	got, _ := mustSave(t, s, "gh:acme:1", goodMeta(), pdf)
	p, err := s.PDFPath("gh:acme:1", got.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p) || filepath.Dir(filepath.Dir(p)) != Dir(state) || filepath.Base(p) != "v1.pdf" {
		t.Errorf("path = %q", p)
	}
	b, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(b, pdf) {
		t.Errorf("the path does not hold the saved PDF: %v", err)
	}
	if st, _ := os.Stat(p); st == nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st)
	}
	// A key that looks like a path steers nothing: the folder is the key's hash.
	evil := "../../etc/passwd"
	p2, err := s.PDFPath(evil, 1)
	if err != nil || filepath.Dir(filepath.Dir(p2)) != Dir(state) {
		t.Errorf("a path-like key escaped: %q %v", p2, err)
	}
}

func TestPDFPathRefusesABadKeyOrVersion(t *testing.T) {
	s, _ := newStore(t)
	for _, c := range []struct {
		key string
		n   int
	}{{"", 1}, {"k\x00", 1}, {"gh:a:1", 0}, {"gh:a:1", -1}, {"gh:a:1", MaxVersion + 1}} {
		if p, err := s.PDFPath(c.key, c.n); !errors.Is(err, ErrInvalid) || p != "" {
			t.Errorf("PDFPath(%q, %d) = %q, %v", c.key, c.n, p, err)
		}
	}
}
