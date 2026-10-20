package indexutil

import (
	"reflect"
	"testing"
)

func TestCanonHeader(t *testing.T) {
	for in, want := range map[string]string{
		"Case Status":   "CASE_STATUS",
		"case-status":   "CASE_STATUS",
		" CASE_STATUS ": "CASE_STATUS",
		"a - b":         "A_B",
		"":              "",
		"   ":           "",
	} {
		if got := CanonHeader(in); got != want {
			t.Errorf("CanonHeader(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSumHex(t *testing.T) {
	// sha256 of the empty input and of "abc" (FIPS 180 test vectors).
	if got := SumHex(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("SumHex(nil) = %s", got)
	}
	if got := SumHex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("SumHex(abc) = %s", got)
	}
}

func TestSortedBySHA(t *testing.T) {
	type src struct{ sha string }
	m := map[string]src{"x": {"c"}, "y": {"a"}, "z": {"b"}}
	got := SortedBySHA(m, func(s src) string { return s.sha })
	if want := []src{{"a"}, {"b"}, {"c"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := SortedBySHA(map[string]src(nil), func(s src) string { return s.sha }); len(got) != 0 {
		t.Errorf("nil map gave %v", got)
	}
}
