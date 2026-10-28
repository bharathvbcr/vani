package draft

import (
	"strings"
	"testing"
	"time"
)

// Tests for the spelled-number, credential and acronym reads of the grounding
// guard (ground_numwords.go, ground_claims.go). Look-alike characters are built
// from code points (the vars in ground_redteam_test.go), never typed.

var (
	fwFive  = string(rune(0xff15)) // full-width 5
	curly   = string(rune(0x2019)) // right single quotation mark
	fwPhD   = string([]rune{0xff30, 0xff48, 0xff24})
	narrow  = string(rune(0x202f)) // narrow no-break space
	allGood = func(t *testing.T, source, reply string) {
		t.Helper()
		if f, n := seekerOnly(source).ungrounded(reply); n != 0 {
			t.Errorf("%q refused against %q: %q (%d)", reply, source, f.String(), n)
		}
	}
	allBad = func(t *testing.T, source, reply string) {
		t.Helper()
		if _, n := seekerOnly(source).ungrounded(reply); n == 0 {
			t.Errorf("%q accepted against %q", reply, source)
		}
	}
)

// Characterization of the digit path the spelled path must match. These pass on
// the code before the extension and after it.
func TestCharacterizeBareDigitsAndEmptyDrafts(t *testing.T) {
	if _, n := seekerOnly().ungrounded("I managed 5."); n != 1 {
		t.Errorf("a bare digit with no source: %d refusals, want 1", n)
	}
	allGood(t, "Led 5 reports.", "I led a team of 5.")
	allGood(t, "Ranked 99 of them.", "Ranked 99th.")
	allBad(t, "Led 5 reports.", "I led a team of 6.")
	for _, empty := range []string{"", "   ", "\n"} {
		if f, n := seekerOnly("anything 5").ungrounded(empty); n != 0 {
			t.Errorf("an empty draft %q has nothing to refuse, got %q", empty, f.String())
		}
	}
}

// Spelled numbers are the digit figures, both ways.
func TestSpelledNumbersMatchDigitsBothWays(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"five years of Kubernetes", "5 years of Kubernetes"},
		{"I led twenty-five engineers", "I led 25 engineers"},
		{"I led twenty five engineers", "I led 25 engineers"},
		{"I ran a dozen services", "I ran 12 services"},
		{"I ran half a dozen services", "I ran 6 services"},
		{"a couple of years", "2 years"},
		{"two hundred and fifty users", "250 users"},
		{"two hundred fifty users", "250 users"},
		{"five thousand users", "5,000 users"},
		{"one thousand five hundred users", "1500 users"},
		{"a hundred users", "100 users"},
		{"a thousand users", "1,000 users"},
		{"one hundred percent", "100%"},
		{"five percent", "5%"},
		{"seven-year project", "7-year project"},
		{"Five YEARS", "5 years"},
		{"five" + nbsp + "years", "5 years"},
		{"five" + narrow + "years", "5 years"},
		{"one year", "1 year"},
		{"one hundred and five", "105"},
		{"twenty-one", "21"},
		{"nineteen", "19"},
		{"two dozen eggs", "2 dozen eggs"},
	} {
		allGood(t, tc.a, tc.b)
		allGood(t, tc.b, tc.a)
	}
}

func TestSpelledNumbersKeepTheirCount(t *testing.T) {
	for _, tc := range []struct{ source, reply string }{
		{"Worked 6 years.", "I worked five years."},
		{"Worked 5 days.", "I worked five years."},
		{"Cut it 5x.", "I worked five years."},
		{"Led 25 engineers.", "I led twenty engineers."},
		{"I know 20 and 5.", "I led twenty-five engineers."},
		{"Served 1000 users.", "I served a hundred users."},
		{"Served 10 users.", "I served a dozen users."},
		{"Cut it 5%.", "I led five engineers."},
		{"a five-year plan", "a six-year plan"},
		{"Led 12 reports.", "I led a dozen engineers."},
		{"Shipped 2 releases.", "I shipped two hundred releases."},
		{"Ran 3 teams.", "I ran thirty teams."},
	} {
		allBad(t, tc.source, tc.reply)
	}
}

// A spelled number with no counted noun is a bare figure, as a bare digit is.
func TestSpelledBareNumberFollowsTheDigitPath(t *testing.T) {
	if f, n := seekerOnly().ungrounded("I led a team of five."); n != 1 || f.String() != "5" {
		t.Errorf("a bare spelled number with no source: %q (%d), want the figure 5", f.String(), n)
	}
	allGood(t, "Led 5 reports.", "I led a team of five.")
	allGood(t, "Led five reports.", "I led a team of 5.")
	allBad(t, "Led 6 reports.", "I led a team of five.")
	allBad(t, "Led five reports.", "I led a team of 6.")
}

// The spelled and the digit figure read the same, to the character.
func TestSpelledFiguresReadAsDigits(t *testing.T) {
	for in, want := range map[string]string{
		"five six":                      "5 6",
		"twenty-five":                   "25",
		"twenty-twenty":                 "20 20",
		"one hundred and five":          "105",
		"one hundred and":               "100",
		"five hundred thousand users":   "500000",
		"a hundred":                     "100",
		"hundred":                       "",
		"a thousand thousand":           "1000",
		"a couple of":                   "2",
		"a married couple":              "",
		"half a dozen":                  "6",
		"half":                          "",
		"three.js and Two Sigma":        "3 2",
		"fifty-fifty":                   "50 50",
		"five2 and k8s and one's":       "2 8",
		"eight-year-old":                "8",
		"sixteen":                       "16",
		"a dozen":                       "12",
		"dozens of services":            "",
		"hundreds and thousands":        "",
		"twentyfive":                    "",
		"often tenacious someone bone":  "",
		"I have 2 and three":            "2 3",
		"Ten.":                          "10",
		"Five, six, and seven.":         "5 6 7",
		"nine nine nine nine":           "9 9 9 9",
		"ten hundred":                   "10",
		"seven hundred and one":         "701",
		"one thousand, two hundred":     "1000 200",
		"a hundred and one":             "101",
		"sixty":                         "60",
		"fourty":                        "",
		"a  hundred":                    "",
		"a" + zwsp + "hundred":          "",
		"half a dozen and a dozen more": "6 12",
	} {
		if got := figureList(in); got != want {
			t.Errorf("figures(%q) = %q, want %q", in, got, want)
		}
	}
}

// A spelled number never grounds a full-width digit, nor the reverse: full-width
// digits are other characters to the figure reader, as before.
func TestSpelledNumbersAndFullWidthDigitsStayApart(t *testing.T) {
	allBad(t, "Worked five years.", "Worked "+fwFive+" years.")
	allBad(t, "Worked "+fwFive+" years.", "Worked five years.")
}

// Words that are not a count are not figures, against no source at all.
func TestNonCountingNumberWordsAreNotFigures(t *testing.T) {
	g := seekerOnly()
	for _, ok := range []string{
		"One of the things I enjoyed was the team.",
		"No one told me, and someone else did, anyone could, everyone knew.",
		"I did it at once.",
		"The first step was the hardest; the second was easier, and the third-party code was worse.",
		"We talked one another through it.",
		"I held one-on-one meetings.",
		"At one point it broke.",
		"One day I would like to lead.",
		"One thing I learned is to measure.",
		"The one I loved most was the compiler.",
		"Which one is it?",
		"Capital One's data team.",
		"I want zero downtime.",
		"I use it often, with tenacity, and a bone to pick.",
		"Dozens of services and hundreds of users, several times, a few teams, twice.",
		"A married couple and the couple next door.",
		"I like the one-off scripts and one-time jobs.",
		"In one go.",
		"One more thing.",
		"One.",
	} {
		if f, n := g.ungrounded(ok); n != 0 {
			t.Errorf("%q has the figure %q, want none", ok, f.String())
		}
	}
	for _, bad := range []string{"I have one year.", "I led one team.", "Twenty-one people."} {
		if _, n := g.ungrounded(bad); n == 0 {
			t.Errorf("%q has a counted number and was accepted", bad)
		}
	}
}

// Credentials: grounded iff the same credential occurs in what was shown.
func TestCredentialsMustBeShown(t *testing.T) {
	for _, reply := range []string{
		"I hold a PhD in physics.",
		"I have a Ph.D. in physics.",
		"I earned a doctorate.",
		"I hold an MBA.",
		"I am a CPA.",
		"I am a PMP and a CFA.",
		"I have a Master" + curly + "s degree.",
		"I have a master's degree.",
		"I have a Master of Science.",
		"I have an MS in physics.",
		"I have an MSc.",
		"I have a BS in physics.",
		"I hold a BSc.",
		"I have a bachelor's degree.",
		"I am AWS Certified.",
		"I am certified in Kubernetes.",
		"I hold a certification.",
		"I am a licensed engineer.",
		"I passed AZ-900.",
		"I hold the Google Cloud Professional Cloud Architect.",
		"I hold a CISSP.",
		"I am a phd.",
		"I hold a PHD.",
		"I hold a Ph" + zwsp + "D.",
		"I hold a " + fwPhD + ".",
	} {
		allBad(t, "I wrote compilers and led a small team at Acme.", reply)
	}
}

func TestCredentialsMatchAcrossSpellings(t *testing.T) {
	for _, tc := range []struct{ source, reply string }{
		{"Ph.D. in physics", "I hold a PhD."},
		{"PhD", "I hold a Ph.D."},
		{"phd", "I hold a PHD."},
		{"Doctorate in physics", "I hold a PhD."},
		{"Master" + curly + "s in CS", "I have a Master's degree."},
		{"Master's in CS", "I have a Master" + curly + "s degree."},
		{"Master" + curly + "s" + nbsp + "degree", "I have a master's degree."},
		{"MS in CS", "I have a Master of Science."},
		{"Master of Science", "I have an MS."},
		{"M.S. in CS", "I have an MSc."},
		{"B.S. in CS", "I have a BSc."},
		{"Bachelor of Science", "I have a BS."},
		{"M.B.A.", "I hold an MBA."},
		{"CPA licensed", "I am a CPA, and a licensed one."},
		{"AWS Certified Solutions Architect", "I am AWS Certified, certified in AWS."},
		{"Certified in AWS", "I am AWS Certified."},
		{"Amazon Web Services Certified Developer", "I am Amazon Web Services certified."},
		{"Google Cloud Certified Professional Cloud Architect", "I hold the Google Cloud Professional Cloud Architect certification."},
		{"Azure Fundamentals (AZ-900) certification", "I passed AZ-900 and hold an Azure certification."},
		{"certification in data science", "I hold a certificate."},
		{"Licensure as a nurse", "I am licensed."},
		{"P" + zwsp + "hD", "I hold a PhD."},
		{fwPhD, "I hold a PhD."},
	} {
		allGood(t, tc.source, tc.reply)
	}
}

// Words that merely look like a credential are not one.
func TestCredentialLookAlikesAreNotCredentials(t *testing.T) {
	g := seekerOnly()
	for _, ok := range []string{
		"Latency fell a lot, measured in ms.",
		"I mastered Go and master the basics; the old masters knew.",
		"A bachelor party and a master key.",
		"The open source license and a licensing review.",
		"My certifiable plan.",
	} {
		if f, n := g.ungrounded(ok); n != 0 {
			t.Errorf("%q: %q is not a credential", ok, f.String())
		}
	}
	// A cloud certification needs its own provider, close to its certification word.
	allBad(t, "Google Cloud Certified", "I am AWS Certified.")
	allBad(t, "I use AWS. Our engineers are certified by nobody.", "I am AWS Certified.")
	allGood(t, "I use AWS in a professional setting.", "I use AWS in a professional setting.")
	allBad(t, "I use AWS in a professional setting.", "I am AWS Certified.")
}

// Acronyms: an all-capitals token of 2 to 6 letters must occur in what was shown.
func TestAcronymsMustBeShown(t *testing.T) {
	for _, reply := range []string{
		"I built it on AWS.", "We use CUDA.", "I know SQL.", "A TPU cluster.", "I used GCP and K8S.",
		"I shipped for ACME and for NASA.", "I did ML at scale.", "Our SRE team.", "The CEO agreed.", "I wrote RAG, HIPAA and GDPR code.",
		"I know LLMS.",
	} {
		allBad(t, "I wrote compilers and led a small team at Acme.", reply)
	}
	for _, tc := range []struct{ source, reply string }{
		{"Built on AWS.", "I built it on AWS."},
		{"built on aws-lambda", "I built it on AWS."},
		{"We use Cuda daily.", "We use CUDA."},
		{"Acme, a TPU company", "A TPU cluster at ACME."},
		{"Knows SQL/NoSQL", "I know SQL."},
		{"RAG, HIPAA and GDPR", "RAG, HIPAA and GDPR."},
		{"ＡＷＳ", "I built it on AWS."},
		{"AWS", "I built it on " + string([]rune{0xff21, 0xff37, 0xff33}) + "."},
	} {
		allGood(t, tc.source, tc.reply)
	}
	// The generic allowlist: no source needed.
	for _, ok := range []string{"OK, I will.", "A US-based AI API with a UI.", "In the UK and the EU."} {
		if f, n := seekerOnly().ungrounded(ok); n != 0 {
			t.Errorf("%q: %q is on the allowlist but was refused", ok, f.String())
		}
	}
	// Not acronyms: mixed case, one letter, longer than six, lowercase. A generic
	// acronym stays generic in the plural.
	for _, ok := range []string{"APIs and UIs.", "I use Rust and Go.", "I, A, B.", "I am VERYLOUD about it.", "aws gcp cuda", "Is it ok?"} {
		if f, n := seekerOnly().ungrounded(ok); n != 0 {
			t.Errorf("%q: %q is not an acronym", ok, f.String())
		}
	}
}

// A trailing lowercase "s" folds on both sides: the plural of an acronym is the
// acronym, so "GPUs" is as unbacked as "GPU", and "LLM" is backed by "LLMs".
func TestAcronymPluralsFoldBothWays(t *testing.T) {
	for _, tc := range []struct{ source, reply string }{
		{"Own AI/LLMs latency", "I built LLM serving."},
		{"Own LLM latency", "I built LLMs."},
		{"5000 GPUs", "I ran GPUs."},
		{"5000 GPU nodes", "I ran GPUs."},
		{"5000 GPUs", "I ran a GPU."},
		{"SDKs and CLIs", "I wrote an SDK and a CLI."},
	} {
		allGood(t, tc.source, tc.reply)
	}
	for _, tc := range []struct{ source, reply string }{
		{"I wrote compilers.", "I ran GPUs."},
		{"I wrote compilers.", "I wrote SDKs."},
		{"I wrote compilers.", "I ran a GPU."},
		{"Own LLM latency", "I ran GPUs."},
		{"I wrote compilers.", "I know LLMS."},
		// An all-capitals plural is a longer acronym, not a plural: "AW" is not "AWS".
		{"AW", "I built it on AWS."},
	} {
		allBad(t, tc.source, tc.reply)
	}
	if f, n := seekerOnly().ungrounded("I ran GPUs."); n != 1 || f.String() != "GPU" || f.what != whatAcronym {
		t.Errorf("unbacked GPUs: %q (%d), want the acronym GPU", f.String(), n)
	}
}

// "MS" and "BS" are a degree only as one: a product name or a verb phrase that
// starts with them is not a master's.
func TestMasterAbbreviationIsNotAProduct(t *testing.T) {
	for _, source := range []string{
		"Built reports in MS Excel and MS Word.",
		"Administered MS SQL Server, MS Teams and MS Office.",
		"Supported MS-Office, MS-SQL and MS Teams users.",
		"Used MS Project and MS Access daily.",
		"Ran MS Exchange and MS SharePoint.",
	} {
		if hits := credentialsIn(source); len(hits) != 0 {
			t.Errorf("%q reads as the credential %+v", source, hits)
		}
		allBad(t, source, "I have a master's degree.")
		allBad(t, source, "I have an MS.")
	}
	// A draft that names a product needs no master's behind it.
	if f, n := seekerOnly().ungrounded("I support MS Teams and MS Office users."); n != 0 {
		t.Errorf("a product name was read as a credential: %q", f.String())
	}
	// A master's written as MS still is one.
	for _, source := range []string{"MS in CS", "MS Computer Science, 2019", "I have an MS.", "MS, physics", "M.S. in CS and MS Office"} {
		allGood(t, source, "I have a master's degree.")
	}
	allGood(t, "BS in CS", "I have a BS.")
}

// A cloud certification is a provider and a certification word, not a provider
// and the word "associate".
func TestCloudCertificationNeedsCertificationContext(t *testing.T) {
	for _, source := range []string{
		"I joined AWS as an associate.",
		"Sales associate at AWS.",
		"Joined AWS as a developer associate.",
		"AWS and Azure specialty retail partners.",
		"I joined Google Cloud as a specialty account lead.",
		"At AWS as a professional engineer.",
	} {
		if hits := credentialsIn(source); len(hits) != 0 {
			t.Errorf("%q reads as the credential %+v", source, hits)
		}
		allBad(t, source, "I hold the AWS Solutions Architect Associate.")
	}
	for _, tc := range []struct{ source, reply string }{
		{"AWS Solutions Architect Associate", "I hold the AWS Developer Associate."},
		{"AWS Developer Associate", "I hold the AWS Machine Learning Specialty."},
		{"AWS Machine Learning Specialty", "I hold the AWS Security Specialty."},
		{"AWS Security Specialty", "I hold the AWS Database Specialty."},
		{"AWS Certified Cloud Practitioner", "I hold the AWS Solutions Architect Associate."},
		{"Google Cloud Professional Cloud Architect", "I hold the Google Cloud Professional Data Engineer."},
		{"AWS Certified", "I hold the AWS Solutions Architect Associate."},
	} {
		allGood(t, tc.source, tc.reply)
	}
}

// A token with a digit is a tagged figure, read once, by the figure reader.
func TestDigitAcronymsAreReadOnce(t *testing.T) {
	for _, reply := range []string{"I used S3.", "I hold SOC2.", "I ran H100 nodes.", "I used K8S."} {
		if f, n := seekerOnly("nothing relevant").ungrounded(reply); n != 1 {
			t.Errorf("%q: %d claims (first %q), want exactly 1", reply, n, f.String())
		}
	}
	allGood(t, "We run S3 and SOC2.", "I used S3 and SOC2.")
	allBad(t, "We run S3.", "I used Q3.")
}

// The first claim is named and the rest counted, across classes.
func TestUngroundedCountsAcrossClasses(t *testing.T) {
	f, n := seekerOnly("nothing").ungrounded("I hold a PhD, know AWS, and led 12 engineers; AWS again, 12 engineers again.")
	if n != 3 || f.String() != "12" {
		t.Errorf("first %q of %d, want the figure 12 first of 3 (figures, credentials, acronyms in that order)", f.String(), n)
	}
}

// Through Draft: the refusal quotes the token and says which kind it was.
func TestDraftRefusesUnbackedCredentialsAcronymsAndSpelledNumbers(t *testing.T) {
	for _, tc := range []struct{ reply, quoted, kind string }{
		{"I cut latency 42% and hold a PhD.", `"PhD"`, "a credential"},
		{"I cut latency 42% and I am AWS Certified.", `"AWS Certified"`, "a credential"},
		{"I cut latency 42% on AWS.", `"AWS"`, "an acronym"},
		{"I served five thousand users.", `"5000"`, "a figure"},
		{"I cut latency 73%.", `"73%"`, "a figure"},
		{"I cut latency for a dozen services.", `"12"`, "a figure"},
	} {
		res, err := draftReply(t, tc.reply, latencyRequest(), Config{})
		de := ungroundedErr(t, err)
		if res.Draft != "" {
			t.Errorf("%q: a refused draft carried text %q", tc.reply, res.Draft)
		}
		msg := de.Error()
		if !strings.Contains(msg, tc.quoted) || !strings.Contains(msg, tc.kind) || !strings.Contains(msg, "not in the evidence") {
			t.Errorf("%q: error = %v, want %s and %s", tc.reply, msg, tc.quoted, tc.kind)
		}
	}
	_, err := draftReply(t, "I hold a PhD, know AWS and led 12 engineers.", latencyRequest(), Config{})
	if msg := err.Error(); !strings.Contains(msg, `"12"`) || !strings.Contains(msg, "2 more") {
		t.Errorf("the refusal names the first claim and counts the rest: %v", err)
	}
}

// A question that mentions a credential does not ground it (ground_seeker_test.go),
// and the figures and acronyms it does ground are unaffected.
func TestDraftAcceptsBackedSpelledAndAcronymClaims(t *testing.T) {
	req := latencyRequest()
	req.Question = "Describe a project where you cut latency. Do you hold a PhD or any certification?"
	for _, reply := range []string{
		"I cut latency by 42 percent and served one thousand users on v2.1.",
		"I shipped a 3.1x kernel for LLM inference.",
		"I served a thousand users.",
	} {
		if _, err := draftReply(t, reply, req, Config{}); err != nil {
			t.Errorf("%q refused: %v", reply, err)
		}
	}
	// "I do hold a certification" was grounded by the question that asked for it.
	if _, err := draftReply(t, "I do hold a certification.", req, Config{}); err == nil {
		t.Error("a question that asks about a certification grounded the claim of one")
	} else {
		ungroundedErr(t, err)
	}
}

// The seeker's notes ground a motivation answer only, as for figures.
func TestNotesGroundCredentialsForMotivationQuestionsOnly(t *testing.T) {
	notes := Config{MotivationNotes: "I hold a PhD and I use AWS every day"}
	motive := Request{Question: "Why Modal?", Job: latencyRequest().Job}
	if _, err := draftReply(t, "I hold a PhD and use AWS.", motive, notes); err != nil {
		t.Errorf("notes sent with a motivation question must ground it: %v", err)
	}
	_, err := draftReply(t, "I hold a PhD.", latencyRequest(), notes)
	ungroundedErr(t, err)
}

// A draft at the reply cap: the guard reads the text that is returned.
func TestGuardAtTheReplyCap(t *testing.T) {
	filler := strings.Repeat("I cut latency. ", MaxReplyRunes/10)
	res, err := draftReply(t, "I hold a PhD. "+filler, latencyRequest(), Config{})
	ungroundedErr(t, err)
	if res.Draft != "" {
		t.Error("a refused draft carried text")
	}
	// A claim past the cap is clipped away with the rest of the reply, and the
	// text the person gets is the text the guard read.
	body := strings.Repeat("I cut latency. ", MaxReplyRunes)
	res, err = draftReply(t, body+"I hold a PhD on AWS for five years.", latencyRequest(), Config{})
	if err != nil {
		t.Fatalf("a claim past the cap was read: %v", err)
	}
	if runes(res.Draft) != MaxReplyRunes || strings.Contains(res.Draft, "PhD") {
		t.Errorf("draft is %d runes and holds the clipped claim: %v", runes(res.Draft), strings.Contains(res.Draft, "PhD"))
	}
	// A claim that ends exactly at the cap is read.
	tail := " PhD"
	exact := strings.Repeat("a", MaxReplyRunes-len(tail)) + tail
	_, err = draftReply(t, exact, latencyRequest(), Config{})
	ungroundedErr(t, err)
}

// Adversarial inputs stay linear and bounded.
func TestGuardIsBoundedOnAdversarialText(t *testing.T) {
	start := time.Now()
	g := seekerOnly(strings.Repeat("five ", 20000), strings.Repeat("PhD AWS ", 5000))
	for _, in := range []string{
		strings.Repeat("five ", 20000),
		strings.Repeat("thousand ", 20000),
		strings.Repeat("a hundred ", 10000),
		strings.Repeat("one hundred and ", 10000),
		strings.Repeat("twenty-", 20000),
		strings.Repeat("AWS ", 20000),
		strings.Repeat("Ph.D. ", 20000),
		strings.Repeat("aws ", 5000) + strings.Repeat("certified ", 5000),
		strings.Repeat("AWS as a ", 20000) + "associate",
		strings.Repeat("AWS as a developer associate. ", 5000),
		strings.Repeat("MS ", 20000) + strings.Repeat("MS Teams ", 20000),
		strings.Repeat("GPUs ", 20000),
		strings.Repeat("5+ years ", 20000),
	} {
		g.ungrounded(in)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("adversarial inputs took %s", d)
	}
	if f, n := seekerOnly().ungrounded(strings.Repeat("five ", 5000)); n != 1 || f.String() != "5" {
		t.Errorf("repeated number words: %q (%d), want the single bare figure 5", f.String(), n)
	}
}
