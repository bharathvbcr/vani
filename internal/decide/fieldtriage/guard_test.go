package fieldtriage

import (
	"runtime"
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

// bestOf3 is the fastest of three runs of fn: the minimum filters scheduler
// noise (a loaded machine or -race only ever adds time).
func bestOf3(fn func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < 3; i++ {
		start := time.Now()
		fn()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// allocated returns the heap bytes fn allocates. Unlike wall-clock time it does
// not move with machine speed, -race or load, so a ratio of two of them is a
// stable measure of how the work grows with the input.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// assertLinear fails when fn on 8x the input allocates more than 12x what it
// allocates on the small input. Linear work gives at most 8 (fixed overhead
// only lowers it); repeated concatenation or per-rune re-copying gives 64 or
// more. The regexp engine is linear by construction (RE2, no backtracking), so
// the allocation count is what a string-handling regression would move.
func assertLinear(t *testing.T, name string, small, big string, fn func(string)) {
	t.Helper()
	fn(small) // warm regexp machines and allocator pools
	as := allocated(func() { fn(small) })
	ab := allocated(func() { fn(big) })
	const maxRatio = 12
	if as == 0 || float64(ab)/float64(as) > maxRatio {
		t.Errorf("%s is super-linear: %d bytes allocated %d B, %d bytes allocated %d B (max ratio %d for an 8x input)",
			name, len(small), as, len(big), ab, maxRatio)
	}
}

func TestGuardIsLinearOnLongLabels(t *testing.T) {
	const unit = "Describe the stage you managed. "
	long := strings.Repeat(unit, 2<<10) // 64 KB, far over the 1000-rune field cap
	if got := Guard(long); got != "" {
		t.Errorf("Guard(long) = %q", got)
	}
	assertLinear(t, "Guard", strings.Repeat(unit, 256), long, func(s string) { Guard(s) })
	// A label at the field cap is the production size: it must stay far below a
	// second even under -race on a loaded machine.
	capped := strings.Repeat(unit, MaxLabelRunes/len(unit)+1)[:MaxLabelRunes]
	if d := bestOf3(func() { Guard(capped) }); d > 2*time.Second {
		t.Errorf("Guard took %v on a %d-rune label", d, MaxLabelRunes)
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
