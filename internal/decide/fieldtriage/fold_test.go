package fieldtriage

import (
	"strings"
	"testing"
	"time"
)

func TestLatinTablesAreComplete(t *testing.T) {
	if len(latin1Upper) != 32 || len(latin1Lower) != 32 || len(latinExtA) != 128 {
		t.Fatalf("table lengths %d %d %d", len(latin1Upper), len(latin1Lower), len(latinExtA))
	}
	for in, want := range map[string]string{
		"é": "e", "É": "E", "ñ": "n", "ü": "u", "ø": "o", "ß": "ss", "æ": "ae", "Þ": "TH",
		"Ā": "A", "ş": "s", "Ž": "Z", "ſ": "s", "ŉ": "n", "Ŀ": "L", "ŵ": "w", "Ÿ": "Y", "œ": "oe",
		"×": "×", "÷": "÷", "Ⓐ": "A", "ⓩ": "z", "𝐜": "c", "𝓩": "Z", "𝟏": "1", "ａ": "a",
	} {
		if got := baseLetter([]rune(in)[0]); got != want {
			t.Errorf("baseLetter(%q) = %q, want %q", in, got, want)
		}
	}
}

// Each phrasing is one a real application form uses; the category is where it
// is pinned.
func TestGuardPinsRealisticPhrasings(t *testing.T) {
	cases := []struct{ label, want string }{
		{"Are you 18 or older?", SelfID},
		{"Are you over 18?", SelfID},
		{"Age 18", SelfID},
		{"Are you 21+?", SelfID},
		{"Armed forces service", SelfID},
		{"Military service", SelfID},
		{"Do you identify as LGBTQ+?", SelfID},
		{"Black or African American", SelfID},
		{"White", SelfID},
		{"Asian (Not Hispanic or Latino)", SelfID},
		{"Native Hawaiian or Other Pacific Islander", SelfID},
		{"What is your sex?", SelfID},
		{"Handicap", SelfID},
		{"Medical condition", SelfID},
		{"Nationality", WorkAuth},
		{"Are you a national of the US?", WorkAuth},
		{"Do you require a H-1B transfer?", WorkAuth},
		{"H1B status", WorkAuth},
		{"Are you on OPT or CPT?", WorkAuth},
		{"Are you a permanent resident?", WorkAuth},
		{"Do you hold a green card?", WorkAuth},
		{"Are you legally permitted to work?", WorkAuth},
		{"Are you legally allowed to work in the US?", WorkAuth},
		{"Do you have unrestricted work rights?", WorkAuth},
		{"workauthorization", WorkAuth},
		{"Work Authorization", WorkAuth},
		// Structural evasion.
		{"Are you a citi‪zen‬?", WorkAuth},
		{"Are you a citi؜zen?", WorkAuth},
		{"Are you a citi<b>zen</b>?", WorkAuth},
		{"Are you a citi&lt;b&gt;zen&lt;/b&gt;?", WorkAuth},
		{"Are you a citi&amp;lt;i&amp;gt;zen?", WorkAuth},
		{"Are you a citízen?", WorkAuth},
		{"Are you a citizén?", WorkAuth},
		{"Are you a Ⓒitizen?", WorkAuth},
		{"Are you a \U0001d41cit\U0001d422zen?", WorkAuth},
		{"Are you a<br>citizen?", WorkAuth},
		{"Are you a <citizen>?", WorkAuth},
		{"U.S.person", WorkAuth},
		{"Are you authorisеd to work", SelfID}, // Cyrillic e inside a Latin word: fail closed
	}
	for _, c := range cases {
		if got := Guard(c.label); got != c.want {
			t.Errorf("Guard(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}

// Lookalike evasion is unbounded without a confusables table, so any word
// mixing Latin with Cyrillic/Greek/etc. is pinned; whole-script words and
// natural mixes are not.
func TestMixedScriptWordsFailClosed(t *testing.T) {
	for _, l := range []string{"Gеnder", "Vеteran", "rаce", "visа", "Genderα", "Racե"} {
		if got := Sensitive(l); got != SelfID {
			t.Errorf("Sensitive(%q) = %q, want self_identification", l, got)
		}
	}
	for _, l := range []string{
		"Почему вы хотите здесь работать?", // entirely Cyrillic
		"Γιατί θέλετε αυτή τη δουλειά;",    // entirely Greek
		"为什么申请 Python 职位",                  // CJK next to Latin words
		"Cyrillic а and Latin a apart",     // separate words
	} {
		if got := Sensitive(l); got != "" {
			t.Errorf("Sensitive(%q) = %q, want unpinned", l, got)
		}
	}
}

// Negative cases for every new pattern: these stay with the model.
func TestNewPatternsKeepNearMissesUnpinned(t *testing.T) {
	for _, l := range []string{
		"Do you have 10 years of experience?",
		"Opt in to receive job alerts",
		"Link to a white paper you wrote",
		"Describe a blackbox testing project",
		"Link to your whiteboard design",
		"National conference talks you gave",
		"Why do you want to work here?",
		"Describe your experience with a typical stage",
		"Résumé summary",
		"Tell us about your Ångström-scale simulation work",
		"Tell us about the H-2 pressure sensor you built",
	} {
		if got := Guard(l); got != "" {
			t.Errorf("Guard(%q) = %q, want unpinned", l, got)
		}
	}
}

func TestFoldIsBoundedAndLinear(t *testing.T) {
	long := strings.Repeat("&amp;amp;amp;amp;<b>x</b>​", 1<<12)
	start := time.Now()
	_ = Sensitive(long)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Sensitive took %v", d)
	}
	// Entity decoding stops at the bound: a fourth layer is not decoded.
	if got := Guard("citi&amp;amp;amp;amp;lt;b&amp;amp;amp;amp;gt;zen"); got != "" {
		t.Errorf("over-deep entity nesting decoded: %q", got)
	}
}
