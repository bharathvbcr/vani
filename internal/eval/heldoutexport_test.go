package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestNormalizeTextTable(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ascii runs", "a  b\t\nc", "a b c"},
		{"trim", "  \n a b \r\n", "a b"},
		{"nbsp", "a  b", "a b"},
		{"line separator", "a b c", "a b c"},
		{"ideographic space", "a　　b", "a b"},
		{"narrow nbsp and em space", "a  b", "a b"},
		{"next line", "a\u0085b", "a b"},
		{"python-only separators", "a\x1c\x1d\x1e\x1fb", "a b"},
		{"case kept", "Go  GO go", "Go GO go"},
		{"punctuation kept", "a, b.", "a, b."},
		{"invalid utf8 is dropped, as Python's errors=ignore", "a\xffb", "ab"},
	}
	for _, c := range cases {
		got, err := NormalizeText(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: NormalizeText(%q) = %q, %v; want %q", c.name, c.in, got, err, c.want)
		}
	}
}

func TestNormalizeTextEmptyRefused(t *testing.T) {
	for _, in := range []string{"", "   ", "  　\n"} {
		if _, err := NormalizeText(in); !errors.Is(err, ErrEmptyText) {
			t.Errorf("NormalizeText(%q) error = %v, want ErrEmptyText", in, err)
		}
		if _, err := NormalizedTextSHA256(in); !errors.Is(err, ErrEmptyText) {
			t.Errorf("NormalizedTextSHA256(%q) error = %v, want ErrEmptyText", in, err)
		}
	}
}

func TestNormalizeTextCutsOnRuneBoundary(t *testing.T) {
	// Exactly at the cap: kept whole.
	at := strings.Repeat("a", MaxNormalizedBytes)
	if got, _ := NormalizeText(at); len(got) != MaxNormalizedBytes {
		t.Fatalf("text at the cap: %d bytes, want %d", len(got), MaxNormalizedBytes)
	}
	// One byte over: cut to the cap.
	if got, _ := NormalizeText(at + "a"); len(got) != MaxNormalizedBytes {
		t.Fatalf("one over: %d bytes, want %d", len(got), MaxNormalizedBytes)
	}
	// A 3-byte rune straddling the cap is dropped whole, not split.
	for pad := 0; pad < 3; pad++ {
		in := strings.Repeat("a", MaxNormalizedBytes-3+pad) + "あtail"
		got, err := NormalizeText(in)
		if err != nil {
			t.Fatal(err)
		}
		// pad 0: the rune ends exactly at the cap and is kept; otherwise it
		// straddles the cap and is dropped whole.
		wantLen := MaxNormalizedBytes - 3 + pad
		if pad == 0 {
			wantLen = MaxNormalizedBytes
		}
		if len(got) != wantLen || strings.ContainsRune(got, '�') || (pad == 0) != strings.HasSuffix(got, "あ") {
			t.Errorf("pad %d: cut %d bytes (want %d), ends %q", pad, len(got), wantLen, got[len(got)-4:])
		}
	}
	// A trailing space left by the cut is trimmed.
	in := strings.Repeat("a", MaxNormalizedBytes-1) + " bbb"
	if got, _ := NormalizeText(in); strings.HasSuffix(got, " ") {
		t.Errorf("cut left trailing space")
	}
}

func TestNormalizedTextSHA256IsSha256OfNormalised(t *testing.T) {
	got, err := NormalizedTextSHA256("Hello   World\n")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("Hello World"))
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("got %s", got)
	}
}

func exportStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "labels"), filepath.Join(t.TempDir(), "text"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuildHeldoutExportContentAndPrivacy(t *testing.T) {
	s := exportStore(t)
	// A triage label, an audit-drawn would_apply label and a manual one.
	must := func(e Entry) {
		t.Helper()
		if err := s.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	must(Entry{Key: "gh:b", Labels: map[string]string{"role_fit": "strong_fit"}})
	must(Entry{Key: "gh:a", Labels: map[string]string{WouldApply: "yes"}, Source: "decile:10", PSelect: 0.5})
	must(Entry{Key: "gh:c", Labels: map[string]string{WouldApply: "no"}, Source: SourceManual})
	texts := map[posting.Key]string{
		"gh:a": "Build  things\nSECRET-PERSONAL jane@example.com",
		"gh:b": "Other text",
		"gh:c": "Third",
		"gh:z": "unlabelled posting, not exported",
	}
	x, err := BuildHeldoutExport(s, func(k posting.Key) (string, bool) { v, ok := texts[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if x.Rows != 3 || len(x.Missing) != 0 {
		t.Fatalf("rows %d missing %v", x.Rows, x.Missing)
	}
	lines := strings.Split(strings.TrimSpace(string(x.Data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	wantKeys := []string{"gh:a", "gh:b", "gh:c"}
	for i, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatal(err)
		}
		if len(m) != 3 || m["v"] != float64(1) || m["key"] != wantKeys[i] {
			t.Errorf("line %d = %s", i, ln)
		}
		h, _ := NormalizedTextSHA256(texts[posting.Key(wantKeys[i])])
		if m["text_sha256"] != h {
			t.Errorf("line %d hash %v want %s", i, m["text_sha256"], h)
		}
		if _, ok := m["sig"]; ok {
			t.Errorf("a sig field was written")
		}
	}
	for _, leak := range []string{"SECRET-PERSONAL", "jane@example.com", "Build", "decile", "strong_fit", "labels", "unlabelled"} {
		if strings.Contains(string(x.Data), leak) {
			t.Errorf("export contains %q", leak)
		}
	}
}

func TestBuildHeldoutExportReportsMissingText(t *testing.T) {
	s := exportStore(t)
	for _, k := range []posting.Key{"gh:have", "gh:gone", "gh:blank"} {
		if err := s.Put(Entry{Key: k, Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
			t.Fatal(err)
		}
	}
	x, err := BuildHeldoutExport(s, func(k posting.Key) (string, bool) {
		switch k {
		case "gh:have":
			return "text", true
		case "gh:blank":
			return "   ", true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if x.Rows != 1 || len(x.Missing) != 2 || x.Missing[0] != "gh:blank" || x.Missing[1] != "gh:gone" {
		t.Fatalf("rows %d missing %v", x.Rows, x.Missing)
	}
}

func TestBuildHeldoutExportFallsBackToCachedText(t *testing.T) {
	s := exportStore(t)
	h, err := s.PutText("cached  body")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:ev", Labels: map[string]string{"role_fit": "strong_fit"}, TextSHA256: h}); err != nil {
		t.Fatal(err)
	}
	x, err := BuildHeldoutExport(s, func(posting.Key) (string, bool) { return "", true }) // body evicted
	if err != nil || x.Rows != 1 || len(x.Missing) != 0 {
		t.Fatalf("rows %d missing %v err %v", x.Rows, x.Missing, err)
	}
	want, _ := NormalizedTextSHA256("cached body")
	if !strings.Contains(string(x.Data), want) {
		t.Fatalf("hash not from cached text: %s", x.Data)
	}
}

func TestBuildHeldoutExportRefusesMalformedLabels(t *testing.T) {
	s := exportStore(t)
	if err := s.Put(Entry{Key: "gh:a", Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(s.labelsPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{not json}\n")
	f.Close()
	if _, err := BuildHeldoutExport(s, nil); err == nil {
		t.Fatal("a malformed label line was exported around")
	}
}

func TestAfterWriteRunsAfterLabelWrite(t *testing.T) {
	s := exportStore(t)
	n := 0
	s.AfterWrite = func() { n++ }
	if err := s.Put(Entry{Key: "gh:a", Labels: map[string]string{"role_fit": "strong_fit"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:b", Labels: map[string]string{"role_fit": "bogus"}}); err == nil {
		t.Fatal("invalid label accepted")
	}
	if n != 1 {
		t.Fatalf("AfterWrite ran %d times, want 1 (not on a refused write)", n)
	}
}
