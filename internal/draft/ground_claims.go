package draft

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Credentials and acronyms, the two claims beside figures that the grounding
// guard (ground.go) refuses when nothing the model was shown holds them. The
// rule differs by claim. An acronym is grounded by any source, like a figure: a
// draft is asked to echo the tools the posting names, so an acronym that occurs
// in the question, the company and title, the posting excerpt, the target summary,
// the seeker's notes (motivation questions) or the evidence sent is grounded. A
// credential is a claim about the seeker, so it is grounded only by text the
// seeker owns: the evidence, the target summary and the notes. A posting that says
// "PhD preferred", a question "Do you have a PhD?" or a title "PhD Research Intern"
// describes the job or asks; none of them says the seeker holds one. The target
// summary names roles the seeker wants, so "PhD-level roles" in it grounds a PhD:
// a residual, kept because the text is the seeker's.
//
// Text is folded before either is read, on both sides: format characters (zero
// width space, soft hyphen) are dropped, any Unicode space is a space, curly
// apostrophes are "'", the dash family is "-" and full-width ASCII is ASCII, so
// "Master’s", "Ph.D." and "PhD" with a no-break space in it read as themselves.

// A credential is a degree, a licence or a certification. The list is closed:
// anything not on it is not read.
//
//   - The names are matched case-insensitively with optional dots between the
//     letters ("PhD", "Ph.D.", "phd"): phd and doctorate, mba, cpa, pmp, cfa,
//     cissp, cism, ccna, cka, ckad, cks.
//   - A master's and a bachelor's are one credential each however written:
//     "master's", "masters degree", "master of science", and the cased
//     abbreviations MS, MSc, MEng, M.S., M.A. (and BS, BSc, BEng, B.S., B.A.).
//     The abbreviations are matched only as written in capitals: "ms" is a
//     millisecond, "bs" a typo. "MA" and "BA" without dots are left out, as they
//     are states and airlines; so are JD (a job description in this domain) and
//     MD. An undotted "MS" followed by a Microsoft product word ("MS SQL", "MS
//     Teams", "MS-Office") is the company's name, not a degree.
//   - "certified", "certification", "certificate" are one claim, and "licensed"
//     and "licensure" another; neither says whose, so any seeker text that uses
//     the word of anything grounds it.
//   - A cloud certification is the provider and a certification word within three
//     words ("AWS Certified", "AWS Solutions Architect Associate", "Google Cloud
//     Professional Cloud Architect", "Certified in Azure", "AZ-900"). "Associate"
//     and "specialty" count only after a track noun ("Developer Associate",
//     "Security Specialty"), and a gap that holds a function word ("AWS as an
//     associate", "Google Cloud as a professional engineer") is a job, not a
//     certification. It needs that provider's phrase in a source, so an "AWS
//     Certified" draft is not grounded by "Google Cloud Certified" nor by "AWS" and
//     "certified" far apart.
type credSpec struct {
	key string
	re  *regexp.Regexp
	// unless, when set, drops a match that the text after it matches.
	unless *regexp.Regexp
}

const cloudProviders = `aws|amazon web services|gcp|google cloud|azure`

// cloudCertWords end a cloud certification: a certification word, a track noun
// then "associate" or "specialty", or "professional" and a track.
const cloudCertWords = `certified|certification|certificate|practitioner|fundamentals|` +
	`(?:architect|developer|administrator|engineer|analyst|security|networking|database|learning|analytics) (?:associate|specialty)|` +
	`professional (?:cloud|data|machine|devops|security|network|solutions|architect|engineer|developer)`

// cloudGapStops are the words that, between a provider and a certification word,
// make the phrase a sentence about a job or a place ("AWS as an associate").
var cloudGapStops = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the as at for in with of by to on from is was my our i`) {
		cloudGapStops[w] = true
	}
}

var (
	credentialSpecs []credSpec
	credentialNames = []string{"phd", "mba", "cpa", "pmp", "cfa", "cissp", "cism", "ccna", "cka", "ckad", "cks"}
	// cloudFwdRE captures the provider (2:4) and the words between it and the certification word (4:6).
	cloudFwdRE  = regexp.MustCompile(`(?i)\b(` + cloudProviders + `)\b((?: [a-z0-9-]+){0,3}) (?:` + cloudCertWords + `)\b`)
	cloudRevRE  = regexp.MustCompile(`(?i)\b(?:certified|certification|certificate) (?:in |for |on )?(` + cloudProviders + `)\b`)
	azureExamRE = regexp.MustCompile(`(?i)\baz-\d{3}\b`)
	claimWordRE = regexp.MustCompile(`[\p{L}\p{N}]+`)
	// microsoftProductRE follows an "MS" that names Microsoft, not a master's.
	microsoftProductRE = regexp.MustCompile(`(?i)^[ -]+(?:sql|server|teams|office|excel|word|access|outlook|project|powerpoint|power|windows|dos|dynamics|exchange|sharepoint|onedrive|visio|azure|visual|365)\b`)
)

func init() {
	for _, n := range credentialNames {
		letters := strings.Split(n, "")
		credentialSpecs = append(credentialSpecs, credSpec{key: n, re: regexp.MustCompile(`(?i)\b` + strings.Join(letters, `(?:\. ?)?`) + `\b`)})
	}
	// A cased pattern matches as written (capitals only); the rest ignore case.
	add := func(key, pattern string, cased bool, unless *regexp.Regexp) {
		if !cased {
			pattern = "(?i)" + pattern
		}
		credentialSpecs = append(credentialSpecs, credSpec{key: key, re: regexp.MustCompile(pattern), unless: unless})
	}
	add("phd", `\bdoctorate\b`, false, nil)
	add("masters", `\bmaster's\b|\bmasters? degree\b|\bmasters? of (?:science|arts|business|engineering|fine arts|education)\b`, false, nil)
	add("masters", `\b(?:MS|MSc|MEng)\b`, true, microsoftProductRE)
	add("masters", `\bM\.(?:S|Sc|Eng|A)\.`, true, nil)
	add("bachelors", `\bbachelor's\b|\bbachelors? degree\b|\bbachelors? of (?:science|arts|engineering|business|fine arts|technology)\b`, false, nil)
	add("bachelors", `\b(?:BS|BSc|BEng)\b|\bB\.(?:S|Sc|Eng|A)\.`, true, nil)
	add("certified", `\bcertif(?:ied|ication|ications|icate|icates)\b`, false, nil)
	add("licensed", `\blicensed\b|\blicensure\b`, false, nil)
}

// credentialAcronyms are the credential names that are also all-capitals words;
// the credential class owns them, so the acronym class does not read them again.
var credentialAcronyms = map[string]bool{
	"PHD": true, "MBA": true, "CPA": true, "PMP": true, "CFA": true, "CISSP": true, "CISM": true, "CCNA": true,
	"CKA": true, "CKAD": true, "CKS": true, "MS": true, "MSC": true, "MENG": true, "BS": true, "BSC": true, "BENG": true,
}

// foldClaim folds text for the credential and acronym readers (see above).
func foldClaim(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsSpace(r):
			return ' '
		case r == '’' || r == '‘' || r == 'ʼ' || r == '′':
			return '\''
		case r >= 0x2010 && r <= 0x2015 || r == 0x2212 || r == 0xff0d:
			return '-'
		case r >= 0xff01 && r <= 0xff5e:
			return r - 0xfee0
		}
		return r
	}, s)
}

type credentialHit struct{ key, surface string }

// credentialsIn lists the credentials s names, in text order, once per key.
func credentialsIn(s string) []credentialHit {
	folded := foldClaim(s)
	type at struct {
		pos int
		credentialHit
	}
	var found []at
	record := func(key string, loc []int) {
		found = append(found, at{loc[0], credentialHit{key, folded[loc[0]:loc[1]]}})
	}
	for _, c := range credentialSpecs {
		for _, loc := range c.re.FindAllStringIndex(folded, -1) {
			if c.unless != nil && c.unless.MatchString(folded[loc[1]:]) {
				continue
			}
			record(c.key, loc)
		}
	}
	for _, m := range cloudRevRE.FindAllStringSubmatchIndex(folded, -1) {
		record("cert:"+cloudProvider(strings.ToLower(folded[m[2]:m[3]])), m[:2])
	}
	forEachCloudCert(folded, func(provider string, loc []int) {
		record("cert:"+cloudProvider(provider), loc)
	})
	for _, loc := range azureExamRE.FindAllStringIndex(folded, -1) {
		record("cert:azure", loc)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].pos < found[j].pos })
	seen := map[string]bool{}
	var out []credentialHit
	for _, f := range found {
		if !seen[f.key] {
			seen[f.key] = true
			out = append(out, f.credentialHit)
		}
	}
	return out
}

// forEachCloudCert calls fn for each provider-first cloud certification in s (a
// lowercase provider and the span of the phrase). A phrase whose gap holds a
// function word is not one; the scan then resumes after its provider, so a real
// certification later in the same sentence ("AWS as a lead, Azure certified") is
// still found.
func forEachCloudCert(s string, fn func(provider string, loc []int)) {
	for pos := 0; pos < len(s); {
		m := cloudFwdRE.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			return
		}
		for i := range m {
			m[i] += pos
		}
		gapOK := true
		for _, w := range strings.Fields(strings.ToLower(s[m[4]:m[5]])) {
			if cloudGapStops[w] {
				gapOK = false
				break
			}
		}
		if !gapOK {
			pos = m[3]
			continue
		}
		fn(strings.ToLower(s[m[2]:m[3]]), m[:2])
		pos = m[1]
	}
}

// cloudProvider names a provider the way the cloud rules key it.
func cloudProvider(p string) string {
	switch p {
	case "amazon web services":
		return "aws"
	case "google cloud":
		return "gcp"
	}
	return p
}

// An acronym is a run of 2 to 6 capital letters ("AWS", "SOC", "CUDA", "TPU"). A
// token with a digit in it ("S3", "SOC2", "H100") is a tagged figure and is read
// by the figure reader, which refuses it with the same strictness, so it is not
// read twice. Only capitals count: "Rust" is a word, and a lowercase name is not an
// acronym. A trailing lowercase "s" is folded, on both sides: "GPUs" is the
// acronym GPU, so an unbacked plural is as refused as the singular, and a source's
// "LLMs" backs a draft's "LLM". An all-capitals "LLMS" is a longer acronym, not a plural.
const (
	minAcronymLen = 2
	maxAcronymLen = 6
)

// genericAcronyms are the acronyms a draft may use unbacked, because they name no
// employer, product, credential or skill the seeker could be claiming:
//   - OK: an interjection.
//   - US, UK, EU: a place a sentence may be about ("a US-based team"); the guard
//     is about what the seeker did, and location is kept out of drafts elsewhere.
//   - AI, API, UI: the generic nouns of the field itself. Anything narrower (ML,
//     LLM, SQL, GPU, CUDA, SRE) is a skill and must be in the material shown.
//
// CEO, CTO, PM, HR and IT are deliberately not here: "the CEO" and "a PM" are
// roles, and "I worked with the CEO" is a claim.
var genericAcronyms = map[string]bool{
	"OK": true, "US": true, "UK": true, "EU": true, "AI": true, "API": true, "UI": true,
}

func isAcronym(tok string) bool {
	if len(tok) < minAcronymLen || len(tok) > maxAcronymLen {
		return false
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < 'A' || tok[i] > 'Z' {
			return false
		}
	}
	return true
}

// acronymOf reads tok as an acronym or the plural of one ("GPUs" is GPU).
func acronymOf(tok string) (string, bool) {
	if isAcronym(tok) {
		return tok, true
	}
	if n := len(tok); n > 0 && tok[n-1] == 's' && isAcronym(tok[:n-1]) {
		return tok[:n-1], true
	}
	return "", false
}

// acronymsIn lists the acronyms s holds that are neither generic nor a
// credential name, in text order, once each; a plural is listed as its singular.
func acronymsIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range claimWordRE.FindAllString(foldClaim(s), -1) {
		a, ok := acronymOf(tok)
		if !ok || genericAcronyms[a] || credentialAcronyms[a] || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// wordsIn is every letter-and-digit word of s, lowercased: what "occurs in" means
// for an acronym ("AWS" occurs in "built on aws-lambda"). A plural acronym is also
// its singular ("LLMs" holds "llm").
func wordsIn(s string) []string {
	folded := foldClaim(s)
	out := claimWordRE.FindAllString(strings.ToLower(folded), -1)
	for _, tok := range claimWordRE.FindAllString(folded, -1) {
		if a, ok := acronymOf(tok); ok && a != tok {
			out = append(out, strings.ToLower(a))
		}
	}
	return out
}
