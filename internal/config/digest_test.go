package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDigestSectionsDefaultAndOff(t *testing.T) {
	c := valid()
	if c.DigestTopOpen() != DefaultDigestTopOpen {
		t.Fatalf("top open default %d", c.DigestTopOpen())
	}
	if b, d := c.DigestExplore(); b != DefaultExploreBelowFold || d != DefaultExploreDropped {
		t.Fatalf("explore default %d/%d", b, d)
	}
	// An explicit zero is "off", not "default".
	var off Config
	if err := json.Unmarshal([]byte(`{"digest":{"top_open":0,"explore":{"below_fold":0,"dropped":0}}}`), &off); err != nil {
		t.Fatal(err)
	}
	if off.DigestTopOpen() != 0 {
		t.Fatalf("top_open 0 read as %d", off.DigestTopOpen())
	}
	if b, d := off.DigestExplore(); b != 0 || d != 0 {
		t.Fatalf("explore 0/0 read as %d/%d", b, d)
	}
}

func TestDigestSectionBounds(t *testing.T) {
	for _, raw := range []string{
		`{"top_open":-1}`,
		`{"top_open":51}`,
		`{"explore":{"below_fold":-1,"dropped":1}}`,
		`{"explore":{"below_fold":1,"dropped":11}}`,
	} {
		c := valid()
		if err := json.Unmarshal([]byte(raw), &c.Digest); err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "digest.") {
			t.Errorf("%s: %v", raw, err)
		}
	}
	c := valid()
	if err := json.Unmarshal([]byte(`{"top_open":50,"explore":{"below_fold":10,"dropped":10}}`), &c.Digest); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("maximums refused: %v", err)
	}
}
