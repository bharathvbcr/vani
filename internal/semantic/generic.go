package semantic

import (
	"regexp"
	"strings"
)

// A posting repeats lines that say what its employer asks of any candidate (a
// degree, years of experience, communication skills, work authorization), not
// what the job is. Matched against a knowledge base they are free points: the
// degree line pairs with the degree entry and the communication line with any
// bullet about working with others, whatever the job, and a posting made of
// little else reads as close to the owner's work as one about it (measured
// against the real bank and model: a bare "Software Engineer" with four such
// lines scored 0.80 beside 0.81 for an LLM-inference posting). genericLine finds
// them so ChunkPosting can leave them out of the match. They stay in the
// description, so keywords, triage and the reader still see them.

var (
	// A credential word together with what makes it a requirement.
	credentialRe = regexp.MustCompile(`(?i)\b(?:bachelor'?s?|master'?s|masters|ph\.?\s?d|doctorate|doctoral|undergraduate|graduate)\b`)
	// Written in capitals so a lowercase "ms" (milliseconds) never counts.
	credentialAbbrRe = regexp.MustCompile(`\b(?:BS|MS|BA|B\.S\.|M\.S\.)\b`)
	requirementRe    = regexp.MustCompile(`(?i)\b(?:degrees?|equivalent|related (?:technical )?(?:field|discipline)s?|(?:technical|quantitative|relevant|stem|scientific) (?:field|discipline)|disciplines?|in (?:computer science|engineering|mathematics|math|statistics|electrical engineering|a (?:technical|quantitative|relevant|stem|related) field))\b`)
	equivalentRe     = regexp.MustCompile(`(?i)\bequivalent (?:practical |work |industry |relevant )?experience\b`)
	softSkillRe      = regexp.MustCompile(`(?i)\b(?:communication skills|written and verbal|verbal and written|interpersonal skills|team player)\b`)
	authorizationRe  = regexp.MustCompile(`(?i)\b(?:(?:authori[sz]ed|eligible|legally) to work|work authori[sz]ation|(?:without|no|not require|not need) (?:visa )?sponsorship|security clearance|background check|drug (?:screen|test)(?:ing)?)\b`)
	yearsRe          = regexp.MustCompile(`(?i)^\W*(?:at least |a minimum of |minimum of |minimum )?\d{1,2}\+?\s*(?:or more )?years?\b(.{0,80}?)\bexperience\W*$`)
)

// yearsFiller are the words between "N years" and "experience" that name no
// subject: with any other word the line says what experience (CUDA kernels), and
// is about the work.
var yearsFiller = map[string]bool{
	"of": true, "professional": true, "relevant": true, "industry": true, "work": true, "working": true,
	"software": true, "development": true, "engineering": true, "non-internship": true, "full-time": true,
	"hands-on": true, "practical": true, "related": true, "proven": true, "direct": true, "prior": true,
	"post-graduate": true, "postgraduate": true, "or": true, "more": true, "of,": true,
}

// genericLine reports whether s asks something of every candidate rather than
// describing the work. It reads at most two chunks' worth of s.
func genericLine(s string) bool {
	s = strings.TrimSpace(truncBytes(s, 2*MaxPostingChunkBytes))
	if s == "" {
		return false
	}
	if equivalentRe.MatchString(s) || softSkillRe.MatchString(s) || authorizationRe.MatchString(s) {
		return true
	}
	if (credentialRe.MatchString(s) || credentialAbbrRe.MatchString(s)) && requirementRe.MatchString(s) {
		return true
	}
	if m := yearsRe.FindStringSubmatch(s); m != nil {
		for _, w := range strings.Fields(strings.ToLower(m[1])) {
			if !yearsFiller[strings.Trim(w, ",;:.()")] && !yearsFiller[w] {
				return false
			}
		}
		return true
	}
	return false
}
