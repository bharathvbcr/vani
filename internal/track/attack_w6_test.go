package track

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func w6utf16(bom string, le bool, s string) string {
	var b []byte
	b = append(b, bom...)
	for _, u := range utf16.Encode([]rune(s)) {
		if le {
			b = append(b, byte(u), byte(u>>8))
		} else {
			b = append(b, byte(u>>8), byte(u))
		}
	}
	return string(b)
}

func w6load(t *testing.T, body string) (Words, error) {
	t.Helper()
	dir := t.TempDir()
	writeTrack(t, dir, WordsFile, body)
	return LoadWords(dir)
}

// Held: UTF-16 (both byte orders, surrogate pairs) loads the same terms as UTF-8,
// and the terms screen text.
func TestAttackW6UTF16Forms(t *testing.T) {
	const content = "# list\nzorblax\nz\U0001D41Aq corp\n"
	for name, body := range map[string]string{
		"le":      w6utf16("\xff\xfe", true, content),
		"be":      w6utf16("\xfe\xff", false, content),
		"le crlf": w6utf16("\xff\xfe", true, strings.ReplaceAll(content, "\n", "\r\n")),
	} {
		w, err := w6load(t, body)
		if err != nil || !w.Personal || len(w.owner) != 2 {
			t.Fatalf("%s: err=%v personal=%v owner=%q", name, err, w.Personal, w.owner)
		}
		if got := w.Found("I built Zorblax last year"); len(got) == 0 {
			t.Errorf("%s: zorblax not screened", name)
		}
	}
}

// Held: odd length, bare BOM, lone surrogate and cut pair fail closed or load
// as empty, and no error carries file content.
func TestAttackW6UTF16Malformed(t *testing.T) {
	for name, body := range map[string]string{
		"odd":         "\xff\xfez\x00o",
		"be odd":      "\xfe\xff\x00z\x00",
		"cut pair":    "\xff\xfe\x3d\xd8",
		"lone low":    "\xff\xfe\x00\xdc\n\x00",
		"lone high+n": "\xff\xfe\x3d\xd8\n\x00zorblax\n\x00",
	} {
		w, err := w6load(t, body)
		if err != nil {
			if strings.Contains(err.Error(), "zorblax") {
				t.Errorf("%s: error carries content: %v", name, err)
			}
			continue
		}
		t.Logf("%s: loaded personal=%v owner=%q", name, w.Personal, w.owner)
	}
	// A bare BOM is an empty file, not a configured list.
	for _, body := range []string{"\xff\xfe", "\xfe\xff", "\xef\xbb\xbf"} {
		w, err := w6load(t, body)
		if err != nil || w.Personal {
			t.Errorf("bare BOM %q: err=%v personal=%v", body, err, w.Personal)
		}
	}
}

// Held: comment-only and blank files in several shapes never report Personal.
func TestAttackW6CommentOnlyNeverPersonal(t *testing.T) {
	for name, body := range map[string]string{
		"utf16 le comment": w6utf16("\xff\xfe", true, "# a\n# b\r\n\n  # c\n"),
		"utf16 be comment": w6utf16("\xfe\xff", false, "# a\n# b\n"),
		"bom then zwsp #":  "\ufeff\u200b# a\n\u2060# b\n",
		"shy before #":     "\u00ad# a\n",
		"alm before #":     "\u061c# a\n",
		"bom mid file":     "# a\n\ufeff# b\n",
		"tabs":             "\t# a\n\t\t\n",
	} {
		w, err := w6load(t, body)
		if err != nil || w.Personal || len(w.owner) != 0 {
			t.Errorf("%s: err=%v personal=%v owner=%q", name, err, w.Personal, w.owner)
		}
	}
}

// Held: a Cf inside a term, in the term or in the text, matches the same text.
func TestAttackW6FormatCharsDoNotChangeMatching(t *testing.T) {
	for _, term := range []string{"zorblax", "zor\u200bblax", "zor\u00adblax", "\u061czorblax\u200e", "z\u2060o\u200dr\u200cblax"} {
		w, err := w6load(t, term+"\n")
		if err != nil || len(w.owner) != 1 {
			t.Fatalf("%q: %v %q", term, err, w.owner)
		}
		for _, text := range []string{"zorblax", "Zorblax Inc", "zor\u00adblax", "zor\u200bblax", "zor-blax", "z\u061co\u202arblax"} {
			if len(w.Found(text)) == 0 {
				t.Errorf("term %q misses text %q", term, text)
			}
		}
		for _, text := range []string{"zorbla", "zor"} {
			if len(w.Found(text)) != 0 {
				t.Errorf("term %q wrongly matches %q", term, text)
			}
		}
	}
	// A short term with a soft hyphen behaves exactly as the same term without.
	a, _ := w6load(t, "co\u00adop\n")
	b, _ := w6load(t, "coop\n")
	for _, text := range []string{"coop", "co-op", "co\u00adop", "co op", "cooperative", "coops"} {
		if len(a.Found(text)) != len(b.Found(text)) {
			t.Errorf("shy term differs from plain on %q", text)
		}
	}
}

// Held: errors name a line number and never the term text, including the
// invisible-only line error and the too-long line error.
func TestAttackW6ErrorsNameNoContent(t *testing.T) {
	for _, body := range []string{
		"zorblaxsecret\n\u200b\n",
		"zorblaxsecret\n" + strings.Repeat("y", 5000) + "\n",
		w6utf16("\xff\xfe", true, "zorblaxsecret\n\u200b\u200d\n"),
		"zorblaxsecret\n\ufeff\n",
	} {
		_, err := w6load(t, body)
		if err == nil {
			t.Errorf("accepted %q", body[:8])
			continue
		}
		if strings.Contains(err.Error(), "zorblaxsecret") || strings.Contains(err.Error(), "yyyy") {
			t.Errorf("error carries content: %v", err)
		}
	}
}

// Held: the size limit applies to the file bytes; a UTF-16 file that decodes
// larger than the limit, or to one huge line, is an error naming no content.
func TestAttackW6SizeLimitUTF16(t *testing.T) {
	big := w6utf16("\xff\xfe", true, strings.Repeat("\u4e2d", 32000))
	if len(big) > maxWordsBytes {
		t.Fatal("fixture over the limit")
	}
	if w, err := w6load(t, big); err == nil {
		t.Errorf("one 96000-byte line accepted: personal=%v", w.Personal)
	}
	if _, err := w6load(t, "\xff\xfe"+strings.Repeat("a\x00", maxWordsBytes/2)); err == nil {
		t.Error("UTF-16 file over the byte limit accepted")
	}
	// many short CJK lines: decoded size exceeds the file size but each line is fine
	var sb strings.Builder
	for range 1500 {
		sb.WriteString("\u4e2d\u6587\u8bcd\n")
	}
	if _, err := w6load(t, w6utf16("\xff\xfe", true, sb.String())); err != nil {
		t.Errorf("1500 short UTF-16 terms: %v", err)
	}
}

// Held: UTF-32 BOMs do not load as a clean, silent, word list of real terms.
func TestAttackW6UTF32(t *testing.T) {
	le := "\xff\xfe\x00\x00" + "z\x00\x00\x00o\x00\x00\x00r\x00\x00\x00b\x00\x00\x00l\x00\x00\x00a\x00\x00\x00x\x00\x00\x00\n\x00\x00\x00"
	w, err := w6load(t, le)
	if err == nil && w.Personal && len(w.Found("Zorblax")) == 0 {
		t.Errorf("UTF-32LE file reports Personal but its term zorblax is not screened: owner=%q", w.owner)
	}
	be := "\x00\x00\xfe\xff" + "\x00\x00\x00z\x00\x00\x00o\x00\x00\x00r\n"
	w, err = w6load(t, be)
	t.Logf("utf32be err=%v personal=%v owner=%q", err, w.Personal, w.owner)
}

// UTF-16 without a BOM: NULs between characters; a comment-only file must not
// report as configured, and a real term must be screened.
func TestAttackW6UTF16NoBOM(t *testing.T) {
	w, err := w6load(t, w6utf16("", true, "# a\n# b\n"))
	if err == nil && w.Personal {
		t.Errorf("BOM-less UTF-16 comment-only file reports Personal; owner=%q", w.owner)
	}
	w, err = w6load(t, w6utf16("", true, "zorblax\n"))
	if err == nil && w.Personal && len(w.Found("Zorblax")) == 0 {
		t.Errorf("BOM-less UTF-16 term not screened, owner=%q", w.owner)
	}
}

// Built-in term collapse: terms of only separators or with leading separators.
func TestAttackW6BuiltinSeparatorOnlyTerm(t *testing.T) {
	for _, raw := range []string{"- -", " - ", "--", "-"} {
		tm := newBuiltinTerm(raw)
		if tm == nil {
			continue
		}
		c := compile([]string{raw}, nil)
		f := foldString("a - b, c-d, x")
		if terms, _ := c.found(&f); len(terms) > 0 {
			t.Errorf("separator-only builtin %q (items %q) matches ordinary punctuation", raw, string(tm.items))
		}
	}
}
