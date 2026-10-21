package fieldtriage

import (
	"strings"
	"testing"
	"time"
)

// The guard reads a form field's label, not a job description: "criminal
// justice" in a role title never reaches it. A label that names a sensitive
// subject is pinned to a manual category, and the pin errs toward manual.
func TestGuardPinsEverySensitivePhrase(t *testing.T) {
	cases := []struct{ label, want string }{
		{"Are you a U.S. citizen?", WorkAuth},
		{"Citizenship status", WorkAuth},
		{"Are you eligible to work in the United States?", WorkAuth},
		{"Are you legally entitled to work in Canada?", WorkAuth},
		{"Are you a U.S. person under export-control rules?", WorkAuth},
		{"Are you a US Person?", WorkAuth},
		{"Do you hold employment authorization?", WorkAuth},
		{"Employment authorisation document", WorkAuth},
		{"What is your age?", SelfID},
		{"Are you over the age of 18?", SelfID},
		{"Religion", SelfID},
		{"Religious affiliation (optional)", SelfID},
		{"Preferred pronouns", SelfID},
		{"What are your pronouns?", SelfID},
		{"Ethnic origin", SelfID},
		{"Racial or ethnic background", SelfID},
		{"Have you ever been convicted of a criminal offense?", SelfID},
		{"Criminal history", SelfID},
		{"Marital status", SelfID},
		// Mixed case and punctuation.
		{"ARE YOU A U.S. CITIZEN", WorkAuth},
		{"mArItAl StAtUs", SelfID},
		// Lookalikes: fullwidth dots and letters, NBSP and other spaces, zero-width joiners.
		{"Are you a U．S． person?", WorkAuth},
		{"Ｕ.Ｓ. citizen", WorkAuth},
		{"Are you eligible to work?", WorkAuth},
		{"Are you eligible to​work?", WorkAuth},
		{"Ci​tizen", WorkAuth},
		{"Ｒｅｌｉｇｉｏｎ", SelfID},
		// HTML entities copied from page source.
		{"Are you a U.S.&nbsp;person?", WorkAuth},
		{"Marital&#32;status", SelfID},
		{"Race &amp; ethnicity", SelfID},
		{"Are you eligible to work&nbsp;in the US&#63;", WorkAuth},
	}
	for _, c := range cases {
		if got := Guard(c.label); got != c.want {
			t.Errorf("Guard(%q) = %q, want %q", c.label, got, c.want)
		}
	}
}

// Words that merely contain a guard word, or a role title, stay unpinned.
func TestGuardLeavesNonSensitiveLabelsToTheModel(t *testing.T) {
	for _, label := range []string{
		"Describe how you manage a team",
		"Tell us about an agent you built",
		"Link to an image of your work",
		"Portfolio page",
		"Which stage of the process are you in?",
		"How do you engage stakeholders?",
		"Estimated mileage",
		"Percentage of time spent coding",
		"Why do you want to work here?",
		"Additional Information",
		"How do you pronounce your name?",
		"Do you have a citizenry of friends here?",
		"Which language do you code in?",
		"Message to the hiring manager",
		"Cover letter",
		"Tell us about a time you managed engagement",
		"Why criminal-justice software?",
	} {
		want := ""
		if strings.Contains(label, "criminal") {
			// A label that names criminal matters stays pinned: the cost of a
			// wrong pin is one manual answer, of a wrong draft, a fabricated one.
			want = SelfID
		}
		if got := Guard(label); got != want {
			t.Errorf("Guard(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestGuardIsLinearOnLongLabels(t *testing.T) {
	long := strings.Repeat("Describe the stage you managed. ", 2<<10) // ~64 KB, far over the 1000-rune field cap
	start := time.Now()
	if got := Guard(long); got != "" {
		t.Errorf("Guard(long) = %q", got)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Guard took %v on a 64 KB label", d)
	}
	if got := Guard(long + " Are you a citizen?"); got != WorkAuth {
		t.Errorf("a sensitive phrase after 64 KB was missed: %q", got)
	}
	if got := Guard(""); got != "" {
		t.Errorf("Guard(empty) = %q", got)
	}
}

func TestSensitiveCoversGuardAndBroaderPattern(t *testing.T) {
	for label, want := range map[string]string{
		"Are you a citizen?":              WorkAuth,
		"Please state your date of birth": SelfID,
		"National origin":                 SelfID,
		"Are you pregnant?":               SelfID,
		"Social security number":          SelfID,
		"Passport number":                 SelfID,
		"Why Anthropic?":                  "",
		"Additional Information":          "",
	} {
		if got := Sensitive(label); got != want {
			t.Errorf("Sensitive(%q) = %q, want %q", label, got, want)
		}
	}
}
