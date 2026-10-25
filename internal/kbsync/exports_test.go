package kbsync

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// LoadSelection records the sha256 of the bytes it parsed, so a build's
// fingerprint covers what eligibility was judged on.
func TestLoadSelectionRecordsTheHashOfTheBytesItParsed(t *testing.T) {
	raw := []byte(`{"sections":[{"title":"Summary","kind":"bullets","bullets":["s1"]}]}`)
	path := filepath.Join(t.TempDir(), "sel.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sel, err := LoadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if want := hex.EncodeToString(sum[:]); sel.SHA256 != want {
		t.Fatalf("SHA256 = %q, want %q", sel.SHA256, want)
	}
	parsed, err := ParseSelection(raw)
	if err != nil || parsed.SHA256 != "" {
		t.Fatalf("ParseSelection hashed bytes it did not read: %q, %v", parsed.SHA256, err)
	}
}

// TokenRe, RKBPlain and WordingHash are the one copy of rkb's token pattern,
// plain-text rule and 12-hex wording hash; rkbresume calls them.
func TestExportedRKBRulesAgreeWithTheEligibilityRule(t *testing.T) {
	if got := TokenRe.FindStringSubmatch("cut {{clmLat}} now"); len(got) != 2 || got[1] != "clmLat" {
		t.Fatalf("TokenRe = %v", got)
	}
	plain := RKBPlain("**Cut {{clmLat}}** with [docs](https://x.dev)", map[string]string{"clmLat": "40%"})
	if plain != "Cut 40% with docs" {
		t.Fatalf("RKBPlain = %q", plain)
	}
	h := WordingHash(plain)
	if len(h) != 12 {
		t.Fatalf("WordingHash = %q, want 12 hex characters", h)
	}
	if WordingHash(plain) != h || WordingHash(plain+".") == h {
		t.Fatal("WordingHash is not a stable function of the text")
	}
}
