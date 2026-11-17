package ibm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

func w1aStorableLocation(t *testing.T, loc string) {
	t.Helper()
	p := posting.Posting{
		Key: posting.Key("ib:ibm:1"), ATS: posting.IBM, Company: "C", Title: "T",
		Location: loc, URL: "https://h.test/j/1",
	}
	if _, err := srcutil.Storable(sources.Board{}, p); err != nil {
		t.Errorf("a %d-byte location the adapter produces is refused: %v", len(loc), err)
	}
}

// w1a: the adapter's largest legitimate location (maxCities places, each
// "city, country" as long as srcutil.RealisticPlaceBytes) passes Storable.
func TestW1aMaxCitiesAtTheRealisticPlaceSizePassStorable(t *testing.T) {
	const country = "United States"
	var cities []string
	for i := 0; i < maxCities+10; i++ {
		head := string(rune('A'+i%26)) + string(rune('a'+i/26))
		n := (srcutil.RealisticPlaceBytes - len(head) - len(", "+country)) / 3
		cities = append(cities, head+strings.Repeat("界", n))
	}
	loc := (job{Country: country, Cities: cities}).location()
	if n := strings.Count(loc, "; ") + 1; n != maxCities {
		t.Fatalf("%d cities kept, want %d", n, maxCities)
	}
	w1aStorableLocation(t, loc)
}

// w1a: every location in the recorded live fixture passes Storable. One of
// them is 583 bytes, which is over a 512-byte cap.
func TestW1aRecordedFixtureLocationsPassStorable(t *testing.T) {
	longest := 0
	for _, hit := range fixtureHits(t) {
		raw, err := json.Marshal(hit["_source"])
		if err != nil {
			t.Fatal(err)
		}
		var j job
		if err := json.Unmarshal(raw, &j); err != nil {
			t.Fatal(err)
		}
		loc := j.location()
		longest = max(longest, len(loc))
		w1aStorableLocation(t, loc)
	}
	t.Logf("longest recorded fixture location: %d bytes", longest)
}
