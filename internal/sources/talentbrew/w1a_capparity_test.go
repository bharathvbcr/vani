package talentbrew

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// w1a: the adapter's largest legitimate location (maxPlaces places, each
// "locality, region, country" as long as srcutil.RealisticPlaceBytes) passes
// Storable. A talentbrew board fails whole on a row Storable refuses.
func TestW1aMaxPlacesAtTheRealisticPlaceSizePassStorable(t *testing.T) {
	var in []ldPlace
	for i := 0; i < maxPlaces+10; i++ {
		var p ldPlace
		head := string(rune('A'+i%26)) + string(rune('a'+i/26))
		p.Address.Locality = head + strings.Repeat("界", (srcutil.RealisticPlaceBytes-len(head)-len(", Ca, Us"))/3)
		p.Address.Region = "Ca"
		p.Address.Country = "Us"
		in = append(in, p)
	}
	loc, _ := places(in)
	if n := strings.Count(loc, "; ") + 1; n != maxPlaces {
		t.Fatalf("%d places joined, want %d", n, maxPlaces)
	}
	p := posting.Posting{
		Key: posting.Key("tb:t:1"), ATS: posting.TalentBrew, Company: "C", Title: "T",
		Location: loc, URL: "https://h.test/j/1",
	}
	if _, err := srcutil.Storable(sources.Board{}, p); err != nil {
		t.Errorf("a %d-byte location the adapter produces is refused: %v", len(loc), err)
	}
}
