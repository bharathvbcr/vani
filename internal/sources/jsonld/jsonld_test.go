package jsonld

import (
	"strings"
	"testing"
	"time"
)

// talentbrew's jsonld_test.go covers Blocks, PayLine and the place shapes
// through that package's names; these pin what the shared package adds or
// what Meta's pages need.

func TestParseDateReadsAnOffsetTimeAsUTC(t *testing.T) {
	got, err := ParseDate("2026-10-01T17:10:09-07:00")
	if err != nil || !got.Equal(time.Date(2026, 10, 2, 0, 10, 9, 0, time.UTC)) || got.Location() != time.UTC {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"yesterday", "2026-13-01", "17:10"} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("%q read as a date", bad)
		}
	}
}

func TestDecodeReadsABlockWithARawTab(t *testing.T) {
	var v struct {
		Type        StringList `json:"@type"`
		Description string     `json:"description"`
	}
	if err := Decode("\n {\"@type\":\"JobPosting\",\"description\":\"a\tb\"} \n", &v); err != nil {
		t.Fatal(err)
	}
	if !IsJobPosting(v.Type) || v.Description != "a b" {
		t.Fatalf("%+v", v)
	}
	if err := Decode(`{"@type":"JobPosting",`, &v); err == nil {
		t.Fatal("a cut block decoded")
	}
}

func TestBlocksStopsAtMaxBlocks(t *testing.T) {
	doc := strings.Repeat(`<script type="application/ld+json">{}</script>`, MaxBlocks+3)
	if got := len(Blocks(doc)); got != MaxBlocks {
		t.Fatalf("%d blocks, want %d", got, MaxBlocks)
	}
}

func TestStringListTakesOneOrMany(t *testing.T) {
	var l StringList
	if err := l.UnmarshalJSON([]byte(`"JobPosting"`)); err != nil || len(l) != 1 {
		t.Fatalf("%v %v", l, err)
	}
	if err := l.UnmarshalJSON([]byte(`["Thing","JobPosting"]`)); err != nil || !IsJobPosting(l) {
		t.Fatalf("%v %v", l, err)
	}
	if err := l.UnmarshalJSON([]byte(`7`)); err == nil {
		t.Fatal("a number read as a string list")
	}
}
