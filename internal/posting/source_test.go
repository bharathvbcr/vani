package posting

import (
	"reflect"
	"testing"
)

func TestSourceOf(t *testing.T) {
	cases := map[Key]string{
		"hm:himalayas:acme:engineer":                   "Himalayas",
		"ro:remoteok:1":                                "Remote OK",
		"zr:ziprecruiter:30182699":                     "ZipRecruiter",
		"dc:dice:46f0d513-ff59-4049-b4ab-9b45d8002fbe": "Dice",
		"ad:us:5012345678":                             "Adzuna",
		"pg:www-indeed-com-scrape:abc":                 "Indeed",
		"pg:careers-acme-com-scrape:abc":               "",
		"pg:acme:abc":                                  "",
		"gh:acme:1":                                    "",
		"":                                             "",
		"zz:x:1":                                       "",
	}
	for k, want := range cases {
		if got := SourceOf(k).Name; got != want {
			t.Errorf("SourceOf(%q).Name = %q, want %q", k, got, want)
		}
	}
	if SourceOf("dc:dice:x").Note == "" || SourceOf("ad:us:1").Note == "" || SourceOf("hm:himalayas:a:b").Note != "" {
		t.Fatal("notes are not where the terms ask for them")
	}
}

func TestSourceNotesAreDistinctAndOrdered(t *testing.T) {
	keys := []Key{"ad:us:1", "gh:acme:1", "dc:dice:a", "ad:gb:2", "dc:dice:b"}
	got := SourceNotes(keys)
	want := []string{SourceOf("dc:dice:a").Note, SourceOf("ad:us:1").Note}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notes = %q", got)
	}
	if SourceNotes(nil) != nil || SourceNotes([]Key{"gh:a:1"}) != nil {
		t.Fatal("notes for no job-board rows")
	}
}
