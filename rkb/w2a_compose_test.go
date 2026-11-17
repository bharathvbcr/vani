package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hashPrinted = regexp.MustCompile(`hash [0-9a-f]{12}\)`)

// `rkb compose` prints the resume hash. With Claims.tex unreadable that hash is
// computed with no claim values, so it can never match a real export; printing
// it as if it were real hides the failure. Compose must stop and say why.
func TestW2AComposeFailsWhenClaimsTexIsUnreadable(t *testing.T) {
	root := sampleCopy(t)
	sel := filepath.Join(root, sampleSel)
	out, code := runSample(t, root, "compose", sel)
	if code != 0 || !hashPrinted.MatchString(out) {
		t.Fatalf("compose on the sample: exit %d: %s", code, out)
	}
	if err := os.Remove(filepath.Join(root, cfg.ClaimsTex[0])); err != nil {
		t.Fatal(err)
	}
	out, code = runSample(t, root, "compose", sel)
	if code == 0 {
		t.Errorf("compose with no Claims.tex exited 0: %s", out)
	}
	if hashPrinted.MatchString(out) {
		t.Errorf("compose printed a hash computed without claim values: %s", out)
	}
	if !strings.Contains(out, "Claims.tex") {
		t.Errorf("compose does not name the unreadable Claims.tex: %s", out)
	}
}
