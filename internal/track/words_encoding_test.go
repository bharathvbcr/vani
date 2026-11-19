package track

import (
	"strings"
	"testing"
)

const notTextMsg = "not valid UTF-8 or UTF-16"

// A Latin-1 file that begins FF FE (y-diaeresis, thorn) is not UTF-16: loading
// it as garbage terms would leave the real term unscreened with no error.
func TestAttackW6AccidentalFFFE(t *testing.T) {
	_, err := w6load(t, "\xff\xfezorblax\n")
	if err == nil || !strings.Contains(err.Error(), notTextMsg) {
		t.Errorf("want a refusal, got %v", err)
	}
}

// Anything that is not clean UTF-8, or clean UTF-16 behind a BOM, is refused
// with a message that carries no content.
func TestWordsEncodingRefusals(t *testing.T) {
	for name, body := range map[string]string{
		"latin1":            "caf\xe9\nzorblax\n",
		"lone continuation": "zorblax\x80\n",
		"utf32 le bom":      "\xff\xfe\x00\x00z\x00\x00\x00",
		"utf32 be bom":      "\x00\x00\xfe\xffzorblax",
		"utf16 odd":         "\xff\xfez\x00o",
		"utf16 be odd":      "\xfe\xff\x00z\x00",
		"utf16 lone high":   "\xff\xfe\x3d\xd8z\x00",
		"utf16 lone low":    "\xff\xfe\x00\xdcz\x00",
		"utf16 nul":         "\xff\xfez\x00\x00\x00o\x00",
		"utf16 fffd":        "\xff\xfe\xfd\xffz\x00",
		"utf16 cut pair":    "\xff\xfez\x00\x3d\xd8",
	} {
		w, err := w6load(t, body)
		if err == nil {
			t.Errorf("%s: loaded, owner=%q", name, w.owner)
			continue
		}
		if !strings.Contains(err.Error(), notTextMsg) {
			t.Errorf("%s: message %q lacks %q", name, err, notTextMsg)
		}
		if strings.Contains(err.Error(), "zorblax") {
			t.Errorf("%s: message leaks content: %q", name, err)
		}
		if w.Personal || len(w.owner) != 0 {
			t.Errorf("%s: refusal returned terms", name)
		}
	}
}

// Clean text still loads: UTF-8 with and without a BOM, UTF-16 in both orders,
// and a UTF-16 BOM with nothing after it.
func TestWordsEncodingAccepted(t *testing.T) {
	for name, body := range map[string]string{
		"utf8":       "zorblax\n",
		"utf8 bom":   "\xef\xbb\xbfzorblax\n",
		"accented":   "caf\u00e9\n",
		"utf16 le":   w6utf16("\xff\xfe", true, "zorblax\n"),
		"utf16 be":   w6utf16("\xfe\xff", false, "zorblax\n"),
		"utf16 pair": w6utf16("\xff\xfe", true, "z\U0001D41Aq\n"),
	} {
		w, err := w6load(t, body)
		if err != nil || !w.Personal {
			t.Errorf("%s: err=%v personal=%v", name, err, w.Personal)
		}
	}
	if _, err := w6load(t, "\xff\xfe"); err != nil {
		t.Errorf("bare UTF-16 BOM refused: %v", err)
	}
}
