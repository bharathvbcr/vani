package track

import (
	"fmt"
	"strings"
	"testing"
)

// Every line ending (CRLF, CR, LF) separates lines: a CR-only file must not lose
// terms behind a comment nor join two terms into one.
func TestAttackW6CROnlyLineEndings(t *testing.T) {
	for name, sep := range map[string]string{"cr": "\r", "crlf": "\r\n", "lf": "\n", "mixed": "\r\n"} {
		body := "# my list" + sep + "zorblax" + sep + "quuxcorp" + sep
		if name == "mixed" {
			body = "# my list\rzorblax\r\nquuxcorp\n"
		}
		w, err := w6load(t, body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(w.owner) != 2 {
			t.Errorf("%s: want 2 terms, got %q", name, w.owner)
		}
		if got := w.Found("Zorblax and Quuxcorp"); len(got) != 2 {
			t.Errorf("%s: both terms must screen, got %q", name, got)
		}
	}
}

func TestAttackW6CRNoLeadingComment(t *testing.T) {
	w, err := w6load(t, "zorblax\rquuxcorp\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(w.owner) != 2 || len(w.Found("I work at Zorblax")) == 0 {
		t.Errorf("CR inside a line: terms %q do not screen zorblax", w.owner)
	}
}

// A refusal names the line by its number under any ending.
func TestWordsLineNumberCountsCR(t *testing.T) {
	_, err := w6load(t, "# a\r# b\r\x01\r")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("line %d", 3)) {
		t.Errorf("want a refusal at line 3, got %v", err)
	}
}
