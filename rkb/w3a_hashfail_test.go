package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// w3a: a bank (or other input) that cannot be hashed must stop the release
// with the reason, never pass the check unrun or enter the manifest as "".

func w3aSparse(t *testing.T, p string, size int64) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil { // sparse: no disk, no read time
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestW3AFinalGateRefusesUnhashableBank(t *testing.T) {
	root, vals := w2aEnv(t)
	over := filepath.Join(root, "bank-over.json")
	w3aSparse(t, over, maxQuoteFile+1)
	dir := filepath.Join(root, "bank-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"over the cap": over, "a directory": dir, "missing": filepath.Join(root, "none.json")} {
		c := w2aResume()
		c.BankPath, c.BankSHA = p, "0123abcd"
		got := w2aJoin(finalPreconditions(c, w2aClaims(c, vals), ""))
		if !strings.Contains(got, "bank.json cannot be hashed") {
			t.Errorf("%s: an unhashable bank passed the final gate: %q", name, got)
		}
	}
}

func TestW3AFinalGateBankHashStillCompared(t *testing.T) {
	root, vals := w2aEnv(t)
	p := filepath.Join(root, "bank-ok.json")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256File(p)
	c := w2aResume()
	c.BankPath, c.BankSHA = p, sum
	if got := w2aJoin(finalPreconditions(c, w2aClaims(c, vals), "")); strings.Contains(got, "bank.json") {
		t.Errorf("a matching bank was flagged: %q", got)
	}
	c.BankSHA = "ffff"
	if got := w2aJoin(finalPreconditions(c, w2aClaims(c, vals), "")); !strings.Contains(got, "older bank.json") {
		t.Errorf("a changed bank was not flagged: %q", got)
	}
}

func TestW3AManifestInputsRefuseUnhashable(t *testing.T) {
	d := t.TempDir()
	good := filepath.Join(d, "good.json")
	if err := os.WriteFile(good, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	over := filepath.Join(d, "over.json")
	w3aSparse(t, over, maxQuoteFile+1)
	in, probs := manifestInputs([]string{good, "", filepath.Join(d, "absent"), over})
	if len(probs) != 1 || !strings.Contains(probs[0], "over.json") || !strings.Contains(probs[0], "limit") {
		t.Errorf("want one problem naming the oversize input and the limit, got %v", probs)
	}
	if h := in[good]; h == "" {
		t.Errorf("the good input lost its hash: %v", in)
	}
	if h, ok := in[over]; ok {
		t.Errorf("the unhashable input was recorded as %q", h)
	}
	if _, ok := in[filepath.Join(d, "absent")]; ok {
		t.Errorf("an absent optional input was recorded")
	}
}
