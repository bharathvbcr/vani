package posting

import (
	"errors"
	"strings"
	"testing"
)

func TestKeysAreStableAndCaseFolded(t *testing.T) {
	a, err := GreenhouseKey("Anthropic", "4012345")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GreenhouseKey(" anthropic ", "4012345")
	if a != b || a != "gh:anthropic:4012345" {
		t.Fatalf("got %q and %q, want both gh:anthropic:4012345", a, b)
	}
	if a.ATS() != Greenhouse {
		t.Fatalf("ATS() = %q", a.ATS())
	}
}

func TestKeyRejectsEmptyAndSeparator(t *testing.T) {
	for _, tc := range []struct{ board, id string }{{"", "1"}, {"x", ""}, {"a:b", "1"}, {"a", "1:2"}, {"  ", "1"}} {
		if _, err := GreenhouseKey(tc.board, tc.id); !errors.Is(err, ErrKeyPart) {
			t.Errorf("GreenhouseKey(%q,%q) err = %v, want ErrKeyPart", tc.board, tc.id, err)
		}
	}
}

func TestFallbackKeyIgnoresPunctuationAndCase(t *testing.T) {
	a, err := FallbackKey("Acme, Inc.", "Senior  ML Engineer", "San Francisco, CA")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := FallbackKey("acme inc", "senior ml engineer", "san francisco ca")
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
	c, _ := FallbackKey("acme inc", "senior ml engineer", "")
	if a == c {
		t.Fatal("location must be part of the fallback key")
	}
	if _, err := FallbackKey("", "x", ""); !errors.Is(err, ErrKeyPart) {
		t.Fatalf("empty company: err = %v", err)
	}
	if a.ATS() != Other {
		t.Fatalf("ATS() = %q", a.ATS())
	}
}

func TestSearchSiteKeys(t *testing.T) {
	for _, tc := range []struct {
		mk   func(string, string) (Key, error)
		a, b string
		want Key
		ats  ATS
	}{
		{AmazonKey, "Amazon", "10578742", "az:amazon:10578742", Amazon},
		{GoogleKey, "google", "124910793232655046", "gg:google:124910793232655046", Google},
		{AppleKey, "apple", "200677962", "ap:apple:200677962", Apple},
		{EightfoldKey, "Microsoft.com", "1970393556991825", "ef:microsoft.com:1970393556991825", Eightfold},
		{JibeKey, "AMD", "93574", "jb:amd:93574", Jibe},
	} {
		k, err := tc.mk(tc.a, tc.b)
		if err != nil || k != tc.want || k.ATS() != tc.ats {
			t.Errorf("%s: %q %v (ATS %q), want %q", tc.want, k, err, k.ATS(), tc.want)
		}
		for _, bad := range [][2]string{{"", "1"}, {tc.a, ""}, {"a:b", "1"}, {tc.a, "1:2"}} {
			if _, err := tc.mk(bad[0], bad[1]); !errors.Is(err, ErrKeyPart) {
				t.Errorf("%s(%q,%q) err = %v, want ErrKeyPart", tc.ats, bad[0], bad[1], err)
			}
		}
	}
}

func TestMalformedKeyHasNoATS(t *testing.T) {
	for _, k := range []Key{"", "nocolon", "zz:a:b"} {
		if k.ATS() != "" {
			t.Errorf("%q.ATS() = %q, want empty", k, k.ATS())
		}
	}
}

func TestDescriptionHashIgnoresWhitespace(t *testing.T) {
	a := Posting{Description: "Build  things\n\nwell"}
	b := Posting{Description: " Build things well "}
	if a.DescriptionHash() != b.DescriptionHash() {
		t.Fatal("whitespace-only differences must not change the hash")
	}
	c := Posting{Description: "Build things better"}
	if a.DescriptionHash() == c.DescriptionHash() {
		t.Fatal("a content change must change the hash")
	}
}

func TestStatusTerminalAndValid(t *testing.T) {
	for _, s := range []Status{Dead, Duplicate, Dropped, Digested} {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range []Status{Discovered, Live, Triaged, Unsure, Scored, Explained} {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
	if Status("bogus").Valid() {
		t.Error("unknown status reported valid")
	}
}

func TestPageKeyCanonicalises(t *testing.T) {
	a, err := PageKey("acme", "https://Careers.Acme.com/jobs/123/?utm_source=x&id=7#apply")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := PageKey("ACME", "https://careers.acme.com/jobs/123?id=7")
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
	if a.ATS() != Page || !strings.HasPrefix(string(a), "pg:acme:") {
		t.Fatalf("key %q", a)
	}
	c, _ := PageKey("acme", "https://careers.acme.com/jobs/124?id=7")
	if a == c {
		t.Fatal("different postings must not collide")
	}
	for _, bad := range []string{"", "/jobs/1", "ftp://x/y", "javascript:alert(1)", "https:///nohost"} {
		if _, err := PageKey("acme", bad); !errors.Is(err, ErrKeyPart) {
			t.Errorf("PageKey(%q) err = %v", bad, err)
		}
	}
	if _, err := PageKey("a:b", "https://x.com/1"); !errors.Is(err, ErrKeyPart) {
		t.Error("board with separator accepted")
	}
}
