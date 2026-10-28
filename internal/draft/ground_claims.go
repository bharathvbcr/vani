package draft

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Credentials and acronyms, the two claims beside figures that the grounding
// guard (ground.go) refuses when nothing the model was shown holds them. The
// rule is the figures' rule: a draft is refused if it names one that occurs in
// none of the question, the company and title, the posting excerpt, the target
// summary, the seeker's notes (motivation questions) and the evidence sent. A
// posting that says "PhD preferred" therefore grounds a draft's "PhD", as its
// "5 years" grounds a draft's "5 years". That is a known soft spot (a posting
// describes the job, not the seeker); excluding the excerpt for credentials only
// would need a second source list through newGrounding, and is the owner's call.
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
//     MD.
//   - "certified", "certification", "certificate" are one claim, and "licensed"
//     and "licensure" another; neither says whose, so a source that uses the word
//     of anything (a posting, a company) grounds it.
//   - A cloud certification is the provider and a certification word within three
//     words ("AWS Certified", "AWS Solutions Architect Associate", "Google Cloud
//     Professional Cloud Architect", "Certified in Azure", "AZ-900"). It needs
//     that provider's phrase in a source, so an "AWS Certified" draft is not
//     grounded by "Google Cloud Certified" nor by "AWS" and "certified" far apart.
type credSpec struct {
	key string
	re  *regexp.Regexp
}

const cloudProviders = `aws|amazon web services|gcp|google cloud|azure`

var (
	credentialSpecs []credSpec
	credentialNames = []string{"phd", "mba", "cpa", "pmp", "cfa", "cissp", "cism", "ccna", "cka", "ckad", "cks"}
	cloudFwdRE      = regexp.MustCompile(`(?i)\b(` + cloudProviders + `)\b(?: [a-z0-9-]+){0,3} (?:certified|certification|certificate|practitioner|fundamentals|associate|specialty|professional (?:cloud|data|machine|devops|security|network|solutions|architect|engineer|developer))\b`)
	cloudRevRE      = regexp.MustCompile(`(?i)\b(?:certified|certification|certificate) (?:in |for |on )?(` + cloudProviders + `)\b`)
	azureExamRE     = regexp.MustCompile(`(?i)\baz-\d{3}\b`)
	claimWordRE     = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

func init() {
	for _, n := range credentialNames {
		letters := strings.Split(n, "")
		credentialSpecs = append(credentialSpecs, credSpec{key: n, re: regexp.MustCompile(`(?i)\b` + strings.Join(letters, `(?:\. ?)?`) + `\b`)})
	}
	// A cased pattern matches as written (capitals only); the rest ignore case.
	add := func(key, pattern string, cased bool) {
		if !cased {
			pattern = "(?i)" + pattern
		}
		credentialSpecs = append(credentialSpecs, credSpec{key: key, re: regexp.MustCompile(pattern)})
	}
	add("phd", `\bdoctorate\b`, false)
	add("masters", `\bmaster's\b|\bmasters? degree\b|\bmasters? of (?:science|arts|business|engineering|fine arts|education)\b`, false)
	add("masters", `\b(?:MS|MSc|MEng)\b|\bM\.(?:S|Sc|Eng|A)\.`, true)
	add("bachelors", `\bbachelor's\b|\bbachelors? degree\b|\bbachelors? of (?:science|arts|engineering|business|fine arts|technology)\b`, false)
	add("bachelors", `\b(?:BS|BSc|BEng)\b|\bB\.(?:S|Sc|Eng|A)\.`, true)
	add("certified", `\bcertif(?:ied|ication|ications|icate|icates)\b`, false)
	add("licensed", `\blicensed\b|\blicensure\b`, false)
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
			record(c.key, loc)
		}
	}
	for _, re := range []*regexp.Regexp{cloudFwdRE, cloudRevRE} {
		for _, m := range re.FindAllStringSubmatchIndex(folded, -1) {
			record("cert:"+cloudProvider(strings.ToLower(folded[m[2]:m[3]])), m[:2])
		}
	}
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
// read twice. Only capitals count: "APIs" and "Rust" are words, and a plural or a
// lowercase name is not an acronym.
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

// acronymsIn lists the acronyms s holds that are neither generic nor a
// credential name, in text order, once each.
func acronymsIn(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range claimWordRE.FindAllString(foldClaim(s), -1) {
		if !isAcronym(tok) || genericAcronyms[tok] || credentialAcronyms[tok] || seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// wordsIn is every letter-and-digit word of s, lowercased: what "occurs in" means
// for an acronym ("AWS" occurs in "built on aws-lambda").
func wordsIn(s string) []string {
	return claimWordRE.FindAllString(strings.ToLower(foldClaim(s)), -1)
}
