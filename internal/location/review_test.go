package location

import (
	"strings"
	"testing"
)

// The verdicts below are the review of 9f1db69 (the one-owner US change),
// finding by finding. Each row is a board location string; the want is what a
// person reads it as. Under the exclusions' cost model a US option read as
// foreign is the costly miss, so an unresolvable string must be ambiguous
// (AmbiguousPlace: kept unjudged), never foreign.

type usWant int

const (
	wantUS usWant = iota
	wantNotUS
	wantAmbiguous // not US, and only_locations must keep it unjudged
	wantUnread    // no place at all: Parse reads nothing
)

func (w usWant) String() string {
	return [...]string{"US", "not US", "ambiguous", "unread"}[w]
}

// gotUS reads l the way the exclusions do: US when InUS; ambiguous when no
// place is US and one is ambiguous; unread when there is no place and no
// remote scope.
func gotUS(l Location) usWant {
	if InUS(l) {
		return wantUS
	}
	if len(l.Places) == 0 && l.RemoteScope == "" {
		return wantUnread
	}
	for _, p := range l.Places {
		if AmbiguousPlace(p) {
			return wantAmbiguous
		}
	}
	return wantNotUS
}

func checkUS(t *testing.T, rows []struct {
	loc    string
	remote bool
	want   usWant
}) {
	t.Helper()
	for _, r := range rows {
		l := Parse(r.loc, r.remote)
		if got := gotUS(l); got != r.want {
			t.Errorf("%q (remote %v): read %v, want %v (parsed %q, places %+v, scope %q)", r.loc, r.remote, got, r.want, l.String(), l.Places, l.RemoteScope)
		}
	}
}

// Finding 1: a placeholder naming a region that takes in the US ("Global
// locations") is not a place. 9f1db69 made it an "Anywhere" place, which
// only_locations dropped.
func TestReviewGlobalPlaceholdersAreNoPlace(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Global locations", false, wantUnread},
		{"Multiple Global Locations", false, wantUnread},
		{"Various locations worldwide", false, wantUnread},
		{"Multiple Locations Worldwide", false, wantUnread},
		{"Multiple Americas locations", false, wantUnread},
		{"Multiple Locations", false, wantUnread},
		// A placeholder naming one country still names it.
		{"US locations", false, wantUS},
		{"Multiple U.S. Data Center Locations", false, wantUS},
		{"Europe locations", false, wantNotUS},
	})
}

// Finding 3: South, Central and Latin America are not the US, and a negated
// country is not that country.
func TestReviewAmericasAndNegations(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"South America", false, wantNotUS},
		{"Central America", false, wantNotUS},
		{"Latin America", false, wantNotUS},
		{"LATAM", false, wantNotUS},
		{"Remote - South America", true, wantNotUS},
		{"Sao Paulo, South America", false, wantNotUS},
		{"Americas - Remote (excluding US)", true, wantNotUS},
		{"Remote - Anywhere except US", true, wantNotUS},
		{"Remote - Anywhere outside the US", true, wantNotUS},
		{"Remote - outside US", true, wantNotUS},
		{"Remote - Not US", true, wantNotUS},
		{"Remote - Non-US", true, wantNotUS},
		{"Remote (Americas, non-US)", true, wantNotUS},
		// The negation applies to Canada, not the US.
		{"Remote - US (no Canada)", true, wantUS},
		{"Remote - US, excluding Hawaii", true, wantUS},
		// "Non-remote" still negates the work mode, not a place.
		{"Non-remote - Austin, TX", false, wantUS},
		// An unresolvable negation is not judged.
		{"Remote - excluding sanctioned regions", true, wantAmbiguous},
		// North America and the Americas still take in the US.
		{"Remote - North America", true, wantUS},
		{"America", false, wantUS},
	})
}

// Finding 4: a US city name followed by a foreign country or region is that
// foreign place; a US city name inside a longer foreign name is not the city;
// a US city followed by a qualifier that is neither a US state nor a US form
// is not judged.
func TestReviewCityNamesAbroad(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Santa Clara, Cuba", false, wantNotUS},
		{"Santa Clara, Peru", false, wantNotUS},
		{"San Jose del Cabo", false, wantNotUS},
		{"Phoenix Aquila, Hyderabad", false, wantNotUS},
		{"SF Express, Shenzhen", false, wantNotUS},
		{"Bay Area, Ontario", false, wantNotUS},
		{"Houston, Scotland", false, wantNotUS},
		{"Dallas, Scotland", false, wantNotUS},
		{"Andover, Hampshire", false, wantNotUS},
		{"Newark, Nottinghamshire", false, wantNotUS},
		{"Stamford, Lincolnshire", false, wantNotUS},
		{"Henderson, Auckland", false, wantNotUS},
		{"Austin, Victoria", false, wantNotUS},
		{"Phoenix, Mauritius", false, wantNotUS},
		{"Lima, Peru", false, wantNotUS},
		{"Washington, England", false, wantNotUS},
		// A qualifier nobody can place: not judged.
		{"Santa Clara, Villa Clara", false, wantAmbiguous},
		{"Washington, Tyne and Wear", false, wantNotUS},
		// Bare San Jose is as likely San José, Costa Rica.
		{"San Jose", false, wantAmbiguous},
		{"San Jose, CA", false, wantUS},
		{"San Jose, California", false, wantUS},
		{"San Jose HQ", false, wantAmbiguous},
		// US forms still read as US.
		{"San Francisco HQ", false, wantUS},
		{"Union Square, New York City", false, wantUS},
		{"Seattle, Washington State", false, wantUS},
		{"Columbus, Georgia", false, wantUS},
		{"Athens, Georgia", false, wantUS},
		{"Melbourne, FL", false, wantUS},
	})
}

// Finding 6: the pronoun "us" in prose is not the country.
func TestReviewPronounUs(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Contact us for locations", false, wantUnread},
		{"Join us at multiple locations", false, wantUnread},
		{"Multiple Locations - US", false, wantUS},
		{"US", false, wantUS},
		{"U.S.", false, wantUS},
		{"Remote US", true, wantUS},
		{"Remote (US)", true, wantUS},
		{"US-CA-San Francisco", false, wantUS},
	})
}

// Finding 7: an ambiguous name keeps its ambiguity when the board adds an
// office or area word.
func TestReviewAmbiguousWithAffixes(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Cambridge HQ", false, wantAmbiguous},
		{"Greater Portland Area", false, wantAmbiguous},
		{"Portland Metro", false, wantAmbiguous},
		{"Richmond Area", false, wantAmbiguous},
		{"Portland Office", false, wantAmbiguous},
		{"Cambridge, UK", false, wantNotUS},
		{"Cambridge, MA", false, wantUS},
	})
}

// Finding 8: US regional names, a US city with "North America", worldwide
// words without a remote flag, and a slash between a foreign city and New York.
func TestReviewUSRegionsAndSlashes(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"New England", false, wantUS},
		{"Greater Boston", false, wantUS},
		{"Greater Boston Area", false, wantUS},
		{"Greater Philadelphia", false, wantUS},
		{"Pacific Northwest", false, wantUS},
		{"Midwest", false, wantUS},
		{"East Coast", false, wantUS},
		{"West Coast", false, wantUS},
		{"Remote - East Coast", true, wantUS},
		{"DMV Area", false, wantUS},
		{"Tri-State Area", false, wantUS},
		{"Bay Area", false, wantUS},
		{"Silicon Valley", false, wantUS},
		{"San Francisco, North America", false, wantUS},
		{"Remote - NA", true, wantAmbiguous},
		{"Anywhere", false, wantAmbiguous},
		{"Worldwide", false, wantAmbiguous},
		{"Global", false, wantAmbiguous},
	})
	l := Parse("Toronto / New York", false)
	if s := l.String(); strings.Contains(s, "Toronto, NY") {
		t.Errorf(`"Toronto / New York" parsed as %q: Toronto is not in New York`, s)
	}
	if !InUS(l) {
		t.Errorf(`"Toronto / New York" not read as US`)
	}
	// "Dallas/Fort Worth, TX" still shares its state.
	if s := Parse("Dallas/Fort Worth, TX", false).String(); s != "Dallas, TX; Fort Worth, TX" {
		t.Errorf(`"Dallas/Fort Worth, TX" parsed as %q`, s)
	}
}

// Finding 9: "(X only)" narrows a wider remote scope to X, and LATAM beside
// the Americas is LATAM.
func TestReviewOnlyQualifierNarrowsScope(t *testing.T) {
	checkUS(t, []struct {
		loc    string
		remote bool
		want   usWant
	}{
		{"Remote - Americas (LATAM only)", true, wantNotUS},
		{"Remote - North America (Canada only)", true, wantNotUS},
		{"Remote - LATAM / Americas", true, wantNotUS},
		// No narrowing without a wider scope: the list stays as written.
		{"Remote (Continental US + Hawaii + Canada Only)", true, wantUS},
		{"Remote - North America (US only)", true, wantUS},
		{"Remote (EMEA or Americas)", true, wantUS},
	})
	if s := Parse("Remote (Continental US + Hawaii + Canada Only)", true).RemoteScope; s != "US, Canada" {
		t.Errorf("scope %q, want US, Canada", s)
	}
}
