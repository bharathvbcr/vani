package location

import "testing"

// An option is Unrecognised when nothing in the tables places it: a US town
// the tables lack reads this way, a place the tables know abroad does not, and
// neither does a US place or an ambiguous one.
func TestOptionUnrecognisedIsANameNothingPlaces(t *testing.T) {
	for _, c := range []struct {
		loc          string
		unrecognised bool
		verdict      Verdict
	}{
		{"Zzyzxville", true, VerdictNotUS},
		{"Quillmont Heights", true, VerdictNotUS},
		{"Austin, TX", false, VerdictUS},
		{"Austin", false, VerdictUS},
		{"London", false, VerdictAmbiguous}, // a name as likely US as not is not judged
		{"Paris, France", false, VerdictNotUS},
		{"Toronto, Canada", false, VerdictNotUS},
		{"Zzyzxville, TX", false, VerdictUS},        // a state settles it
		{"Zzyzxville, France", false, VerdictNotUS}, // so does a country
	} {
		var got []Option
		for _, pl := range Parse(c.loc, false).Places {
			got = append(got, PlaceOptions(pl)...)
		}
		if len(got) == 0 {
			t.Errorf("%q: no option", c.loc)
			continue
		}
		for _, o := range got {
			if o.Unrecognised != c.unrecognised || (c.loc != "London" && o.Verdict != c.verdict) {
				t.Errorf("%q: option %+v, want unrecognised %v verdict %v", c.loc, o, c.unrecognised, c.verdict)
			}
		}
	}
}

// A list holds one verdict per option: "Paris, Zzyzxville" is a known place
// abroad and an unknown name.
func TestOptionUnrecognisedIsPerOption(t *testing.T) {
	var got []Option
	for _, pl := range Parse("Paris, France; Zzyzxville", false).Places {
		got = append(got, PlaceOptions(pl)...)
	}
	n := 0
	for _, o := range got {
		if o.Unrecognised {
			n++
		}
	}
	if len(got) < 2 || n != 1 {
		t.Errorf("options %+v: want exactly one unrecognised of several", got)
	}
}
