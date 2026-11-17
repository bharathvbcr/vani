package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var banned = []string{
	`\bresponsible for\b`, `\bhelped\b`, `\bworked on\b`, `\bvarious\b`, `\butiliz(e|ed|es|ing)\b`,
	`\bleverag`, `\bsynerg`, `\bcutting[- ]edge\b`, `\bstate[- ]of[- ]the[- ]art\b`, `\bpassionate\b`,
	`\bresults[- ]driven\b`, `\bdetail[- ]oriented\b`, `\bteam player\b`, `\bhard[- ]working\b`, `\bvery\b`,
	`\breally\b`, `\bsuccessfully\b`, `\bworld[- ]class\b`, `\bblazing\b`, `\brevolutionary\b`, `\bgame[- ]chang`,
	`\bseamless`, `\brobust\b`, `\blines of code\b`, `\bLOC\b`, `\bduties included\b`, `\btasked with\b`,
	`\bgot\b`,
}

// The generic lists below are compiled in; bullet_rules in config.json adds a
// person's own entries (lead_jargon, tech_terms, extra_banned, dup_phrases,
// summary_banned), empty by default, so recurring wordings from one person's
// drafts live in their config rather than in this binary.
const (
	leadJargonBase = `ABI|epoll|GEMM|BPE|bf16|f32`
	dupWordBase    = `within\s+within|faster\s+faster|slower\s+slower|from\s+from|to\s+to|in\s+in|on\s+on|at\s+at|by\s+by|for\s+for|with\s+with|across\s+across|over\s+over|under\s+under|than\s+than|the\s+the|a\s+a|an\s+an`
)

var (
	extendMu    sync.Mutex
	extendCache = map[string]*regexp.Regexp{}
)

// extendRe compiles `(?i)\b(base|extra…)\b`, adding each non-blank extra after
// the compiled-in alternatives (quoted as literal text when quote is set,
// otherwise as a grouped regexp). A config pattern that does not compile stops
// rkb, so a typo cannot silently turn a check off.
func extendRe(base string, extra []string, quote bool) *regexp.Regexp {
	alts := []string{base}
	for _, e := range extra {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if quote {
			e = regexp.QuoteMeta(e)
		}
		alts = append(alts, `(?:`+e+`)`)
	}
	key := strings.Join(alts, "\x00")
	extendMu.Lock()
	defer extendMu.Unlock()
	if re, ok := extendCache[key]; ok {
		return re
	}
	re, err := regexp.Compile(`(?i)\b(` + strings.Join(alts, "|") + `)\b`)
	if err != nil {
		die("config.json: bullet_rules pattern does not compile: %v", err)
	}
	extendCache[key] = re
	return re
}

// leadJargonRe: low-level words that must not sit unglossed in a bold lead.
func leadJargonRe() *regexp.Regexp { return extendRe(leadJargonBase, cfg.Rules.LeadJargon, true) }

// parityWhyBase: wordings that state why matching an external baseline is
// still a result (bullet_rules.parity_differentiators adds to it).
const parityWhyBase = `without\s+Python|no\s+Python|in-process|bit-identical|bit-exact|zero-dependency`

func parityWhyRe() *regexp.Regexp {
	return extendRe(parityWhyBase, cfg.Rules.ParityDifferentiators, false)
}

// unjustifiedParityRe matches "matched <baseline>" (optionally with an
// N-thread modifier) for the external baselines in
// bullet_rules.parity_baselines. Which baselines a person's bullets measure
// against is theirs, so the list is config; empty turns the check off (nil).
func unjustifiedParityRe() *regexp.Regexp {
	var names []string
	for _, n := range cfg.Rules.ParityBaselines {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, regexp.QuoteMeta(n))
		}
	}
	if len(names) == 0 {
		return nil
	}
	pat := `(?i)\bmatch(?:ed|ing)\s+(?:\w+-thread(?:ed)?\s+)?(?:` + strings.Join(names, "|") + `)\b`
	extendMu.Lock()
	defer extendMu.Unlock()
	if re, ok := extendCache[pat]; ok {
		return re
	}
	re := regexp.MustCompile(pat) // quoted names cannot fail to compile
	extendCache[pat] = re
	return re
}

// unjustifiedParity returns the parity wording in text that states no
// differentiator, or "".
func unjustifiedParity(text string) string {
	re := unjustifiedParityRe()
	if re == nil {
		return ""
	}
	if m := re.FindString(text); m != "" && !parityWhyRe().MatchString(text) {
		return m
	}
	return ""
}

// dupWordRe: doubled words and garbled token expansions.
func dupWordRe() *regexp.Regexp { return extendRe(dupWordBase, cfg.Rules.DupPhrases, false) }

// bannedPatterns is the filler list plus bullet_rules.extra_banned.
func bannedPatterns() []string {
	out := append([]string{}, banned...)
	for _, p := range cfg.Rules.ExtraBanned {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// statNotationRe is a test statistic with its degrees of freedom, e.g. F(2,30):
// its parentheses are notation, not a nested parenthetical.
var statNotationRe = regexp.MustCompile(`\b[A-Za-z]\(\d+,\s*\d+\)`)
var (
	firstPerson       = regexp.MustCompile(`\b(I|me|my|mine|we|our)\b`)
	thirdPerson       = regexp.MustCompile(`(?i)\b(he|his|him|himself|she|her|hers|herself|the author|the candidate)\b`)
	stubColonRe       = regexp.MustCompile(`:\s*(?:\*\*)?\s*(?:[A-Za-z0-9\-]+\s+){0,3}[A-Za-z0-9\-]+;`)
	unitMismatchRe    = regexp.MustCompile(`(?i)\ballocations\s+from\s+\d+\s*bytes\b|\b\d+\s*(?:bytes|ms|µs|s|GB|MB|KB|GiB|MiB|KiB)\s+to\s+1\b(?:\s*\.(?:\s|$)|\s*,|\s*;|\s*$)`)
	boldLeadRe        = regexp.MustCompile(`^\s*\*\*(.+?)\*\*(.*)$`)
	hasDigitRe        = regexp.MustCompile(`\d`)
	leadSciNotationRe = regexp.MustCompile(`(?i)(10\^\{?-?\d+\}?|10[⁻⁺][⁰¹²³⁴⁵⁶⁷⁸⁹]+|\bRTF\b|\b0\.\d{4}\b)`)
	// Blocks internal schema/migration counters like "schema v12", "store schema 3", "database schema 8".
	schemaVersionRe = regexp.MustCompile(`(?i)\b(?:store\s+|database\s+|db\s+)?schema\s+(?:v?\d+|version\s+\d+)\b`)
	// Blocks test/line/branch coverage vanity claims like "at 92% line coverage" or "77.5% test coverage".
	coverageVanityRe = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?%\s+(?:line|branch|statement|code|test|unit-test)\s+coverage\b`)
	// Blocks raw test-suite count filler like "1,200 vitest tests passing" or "640 cargo tests".
	testCountFillerRe = regexp.MustCompile(`(?i)\b\d[\d,]*\+?\s+(?:unit\s+|integration\s+|vitest\s+|cargo\s+|go\s+|swift\s+|bun\s+|pytest\s+)?tests(?:\s+passing)?\b`)
)

// Finding is one prose-lint violation on a bullet.
type Finding struct {
	Rule    string
	Bullet  string
	Message string
}

// codenameRe matches the project codenames listed in bullet_rules.codenames
// (whole words, case-sensitive, as written). The list is per user, so it lives
// in config.json; an empty list matches nothing, which turns off only the
// Summary codename check.
func codenameRe(names []string) *regexp.Regexp {
	var alts []string
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			alts = append(alts, regexp.QuoteMeta(n))
		}
	}
	if len(alts) == 0 {
		return regexp.MustCompile(`[^\s\S]`) // matches no text, not even ""
	}
	return regexp.MustCompile(`\b(` + strings.Join(alts, "|") + `)\b`)
}

func lintAntiVanityGates(bulletID, text string) []Finding {
	var out []Finding
	if m := schemaVersionRe.FindString(text); m != "" {
		out = append(out, Finding{
			Rule:    "prose.no-schema-version",
			Bullet:  bulletID,
			Message: fmt.Sprintf("do not cite internal schema version numbers (%q) in resume bullets; state architectural capability instead", m),
		})
	}
	if m := coverageVanityRe.FindString(text); m != "" {
		out = append(out, Finding{
			Rule:    "prose.no-coverage-vanity",
			Bullet:  bulletID,
			Message: fmt.Sprintf("do not use line/test coverage (%q) as a resume claim; highlight security, latency, or reliability mechanisms instead", m),
		})
	}
	if m := testCountFillerRe.FindString(text); m != "" {
		out = append(out, Finding{
			Rule:    "prose.no-test-count-filler",
			Bullet:  bulletID,
			Message: fmt.Sprintf("avoid raw passing test counts (%q) in resume bullets; focus on production outcomes and benchmarks", m),
		})
	}
	return out
}

var verbs = map[string]bool{}

var weakVerbs = map[string]string{
	"got":       "use an outcome verb like 'achieved', 'unlocked', 'delivered', 'yielded', 'realized'",
	"wrote":     "use a technical action verb like 'engineered', 'authored', 'implemented', 'developed'",
	"connected": "use an architectural verb like 'integrated', 'unified', 'federated', 'interfaced'",
	"created":   "use an engineering verb like 'architected', 'engineered', 'built', 'designed', 'implemented'",
	"made":      "use an action verb like 'rendered', 'optimized', 'reduced', 'cut', 'accelerated'",
	"held":      "use an action verb like 'maintained', 'sustained', 'retained'",
	"kept":      "use an action verb like 'preserved', 'sustained', 'constrained'",
	"took":      "use an action verb like 'reduced', 'required', 'consumed'",
	"gave":      "use an action verb like 'yielded', 'produced', 'provided'",
}

var presentVerbs = map[string]string{
	"own":      "use a past-tense verb like 'Owned', 'Engineered', or 'Maintained'",
	"operate":  "use a past-tense verb like 'Operated' or 'Maintained'",
	"ship":     "use 'Shipped'",
	"build":    "use 'Built'",
	"design":   "use 'Designed'",
	"run":      "use 'Ran' or 'Executed'",
	"lead":     "use 'Led'",
	"train":    "use 'Trained'",
	"serve":    "use 'Served'",
	"maintain": "use 'Maintained'",
	"unify":    "use 'Unified'",
}

func init() {
	for _, v := range strings.Fields(`achieved added architected audited automated benchmarked built calibrated cut debugged
decreased defined delivered deployed derived designed developed diagnosed drove eliminated enabled engineered established
evaluated expanded found founded halved hardened identified implemented improved increased instrumented integrated introduced
launched led lifted lowered measured migrated modeled optimized orchestrated ported prevented profiled published raised ran
rebuilt reduced refactored replaced replicated reproduced rewrote scaled secured shipped showed shortened simplified sped
standardized streamlined taught tested traced trained tuned unified validated verified co-authored authored mentored
owned operated brought grew won accelerated attained bounded characterized coalesced compacted compiled compressed conducted
confined confirmed consolidated constructed constrained converted curated demonstrated formulated fused gated generated grounded
isolated linked mapped narrowed open-sourced outperformed packaged paired parallelized partitioned placed preserved quantified
ranked reached recorded registered remediated removed reorganized re-ran resolved restored routed sandboxed sealed segmented
shaped shifted shrank sized spearheaded split stabilized sustained synthesized tiered tightened tracked transformed tripled
uncovered wired wrapped yielded`) {
		verbs[v] = true
	}
}

var commonAcr = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`AI ML API APIs GPU GPUs CPU CPUs CI CD UI UX SQL AWS GCP iOS PDF PDFs HTTP JSON LLM LLMs
SDK SDKs URL CLI IDE OS US USA MS BS PhD MBA HR QA KPI SaaS B2B R&D MCP RAG NVIDIA`) {
		commonAcr[w] = true
	}
}

var techTerms = map[string]bool{}

func init() {
	// Generic systems vocabulary; a person's own field adds bullet_rules.tech_terms.
	for _, w := range strings.Fields(`gemm blas simd ffi abi ssd kv ast lsp zero-copy bootstrap prefill decode speculative
quantization fp16 wasm`) {
		techTerms[w] = true
	}
}

func jargon(plain string) []string {
	var hits []string
	seen := map[string]bool{}
	extra := map[string]bool{}
	for _, w := range cfg.Rules.TechTerms {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
			extra[w] = true
		}
	}
	for _, tok := range regexp.MustCompile(`[A-Za-z][A-Za-z0-9+\-/&.]*`).FindAllString(plain, -1) {
		t := strings.Trim(tok, ".-/")
		isAcr := regexp.MustCompile(`^[A-Z][A-Z0-9&]{1,5}s?$`).MatchString(t) && !commonAcr[t]
		if (isAcr || techTerms[strings.ToLower(t)] || extra[strings.ToLower(t)]) && !seen[t] {
			seen[t] = true
			hits = append(hits, t)
		}
	}
	return hits
}

// literalFigures finds numbers written straight into a bullet. A digit run
// glued to letters (A100, INT4, M5) or hyphenated after a word (Mamba-2,
// Llama-3.1-8B) is a name; anything else is a measurement and must come from
// Claims.tex or be listed in allow_literals.
var litRe = regexp.MustCompile(`[0-9][0-9,]*(?:\.[0-9]+)?(?:[%×x]|[-–][A-Za-z]+|\s?(?:ms|µs|s|h|GB|MB|KB|TB|k|K|M|B|tok/s)\b)?`)

func literalFigures(text string, allow []string) []string {
	s := tokenRe.ReplaceAllString(text, " ")
	s = linkRe.ReplaceAllString(s, "$1")
	var out []string
	for _, loc := range litRe.FindAllStringIndex(s, -1) {
		i := loc[0]
		if i > 0 {
			p := rune(s[i-1])
			if (p >= 'A' && p <= 'Z') || (p >= 'a' && p <= 'z') || p == '.' || p == '_' || p == '/' {
				continue // part of a name or path
			}
			if (p == '-' || p == '–') && i > 1 {
				q := rune(s[i-2])
				if (q >= 'A' && q <= 'Z') || (q >= 'a' && q <= 'z') || (q >= '0' && q <= '9') {
					continue // Mamba-2, FlashAttention-2, 3.5-2B
				}
			}
		}
		lit := strings.TrimSpace(s[loc[0]:loc[1]])
		// allowed only where an allow_literals phrase actually occurs around
		// this number ("batch 1" covers that 1, not every 1 in the bullet)
		allowed := false
		for _, a := range allow {
			if a == "" {
				continue
			}
			for _, sp := range regexp.MustCompile(regexp.QuoteMeta(a)).FindAllStringIndex(s, -1) {
				if sp[0] <= loc[0] && loc[0] < sp[1] {
					allowed = true
				}
			}
		}
		if !allowed {
			out = append(out, lit)
		}
	}
	return out
}

// compareRe finds a baseline or comparator in a bullet's resolved text: a
// before -> after, a multiple, a named alternative ("vs", "than"), a
// from-to change, or a change verb applied to a percentage.
var compareRe = regexp.MustCompile(`(?i)→|×|\bvs\.?\s|\bversus\b|\bthan\b|\bfrom\s+\S+.{0,40}?\bto\b|\bcompared (?:with|to)\b|\brelative to\b|\bbaseline\b|\binstead of\b|\b(?:down|up) from\b|\bover (?:the same|a |an |its |closed|naive|stock|plain|default|prior|previous)|\b(?:cut|cutting|reduc\w*|rais\w*|lift\w*|improv\w*|boost\w*|halv\w*|doubl\w*|lower\w*|shrank|shrunk|saved|sav\w+)\b[^.;]{0,60}?\d[\d.,–-]*\s?(?:%|(?:percentage )?points?\b)|\bpoints? (?:above|below|over|ahead of|behind)\b|\d[\d.,]*\s?%\s+(?:faster|slower|fewer|less|more|lower|higher|smaller|cheaper)\b`)

// hasComparison reports whether the resolved bullet text states what its
// result is better or worse than.
func hasComparison(plain string) bool { return compareRe.MatchString(plain) }

func testingCentric(raw, plain string, cv map[string]*ClaimView) string {
	pat := regexp.MustCompile(`(?i)\b(?:` + strings.Join(cfg.Rules.TestingTerms, "|") + `)\b`)
	if m := regexp.MustCompile(`^\s*\*\*(.+?)\*\*`).FindStringSubmatch(raw); m != nil {
		lead := tokenRe.ReplaceAllStringFunc(m[1], func(t string) string {
			if c := cv[tokenRe.FindStringSubmatch(t)[1]]; c != nil {
				return c.Value
			}
			return t
		})
		if h := pat.FindString(plainText(lead)); h != "" {
			return fmt.Sprintf("bold lead is about testing (%q)", h)
		}
	}
	toks := tokenRe.FindAllStringSubmatch(raw, -1)
	if len(toks) > 0 {
		all := true
		var vals []string
		for _, t := range toks {
			v := ""
			if c := cv[t[1]]; c != nil {
				v = c.Value
			}
			vals = append(vals, v)
			if !pat.MatchString(v) {
				all = false
			}
		}
		if all {
			return "its only figures are testing figures (" + strings.Join(vals, ", ") + ")"
		}
	}
	if hits := pat.FindAllString(plain, -1); len(hits) >= 2 {
		return "testing vocabulary dominates (" + strings.Join(hits, ", ") + ")"
	}
	return ""
}

func resolvePlain(text string, cv map[string]*ClaimView) string {
	s := tokenRe.ReplaceAllStringFunc(text, func(t string) string {
		if c := cv[tokenRe.FindStringSubmatch(t)[1]]; c != nil {
			return c.Value
		}
		return t
	})
	return plainText(s)
}

type lintResult struct{ Errors, Warns []string }

func (l *lintResult) err(f string, a ...any)  { l.Errors = append(l.Errors, fmt.Sprintf(f, a...)) }
func (l *lintResult) warn(f string, a ...any) { l.Warns = append(l.Warns, fmt.Sprintf(f, a...)) }

func lint(c *Composed, cf *ClaimsFile, final bool, ecoPath string) *lintResult {
	R := &lintResult{}
	expPages := expectedPages(c, c.Meta.Origin)
	if c.Meta.Pages != 0 && c.Meta.Pages != expPages {
		R.err("meta.pages is %d, expected %d (only master CV can be 4 pages; all other resumes must be 2 pages)", c.Meta.Pages, expPages)
	}
	rules := cfg.Rules
	n, quant, impact := 0, 0, 0
	figured, compared := 0, 0
	summaryN, summaryStrong := 0, 0
	var testing, uncompared []string
	ids := map[string]bool{}
	var allText []string
	numWord := regexp.MustCompile(`(?i)\b(` + strings.Join(rules.NumberWords, "|") + `)\b`)

	for _, ref := range c.allBullets() {
		b := ref.B
		n++
		if b.ID == "" {
			R.err("a bullet in %q has no id", ref.Sec.Title)
		} else if ids[b.ID] {
			R.err("%s: selected twice on this resume", b.ID)
		}
		ids[b.ID] = true
		bid := b.ID
		plain := resolvePlain(b.Text, cf.Claims)
		allText = append(allText, plain)
		for _, f := range lintAntiVanityGates(bid, plain) {
			R.err("%s: [%s] %s", bid, f.Rule, f.Message)
		}
		words := strings.Fields(plain)
		toks := tokenRe.FindAllStringSubmatch(b.Text, -1)

		if len(toks) > 0 {
			quant++
			hasImpact := false
			for _, loc := range tokenRe.FindAllStringSubmatchIndex(b.Text, -1) {
				id := b.Text[loc[2]:loc[3]]
				after := strings.TrimSpace(b.Text[loc[1]:min(len(b.Text), loc[1]+4)])
				if c := cf.Claims[id]; c != nil && (c.Kind != "count" || strings.HasPrefix(after, "×") || strings.HasPrefix(after, "%")) {
					hasImpact = true
				}
			}
			if !hasImpact && (strings.Contains(plain, "×") || strings.Contains(plain, "%") || strings.Contains(plain, "→") || strings.Contains(plain, "p=") || strings.Contains(plain, "p =")) {
				hasImpact = true
			}
			if hasImpact {
				impact++
			} else if (ref.Sec.Kind == "bullets" || ref.Ent != nil) && b.UnquantifiedOK == "" {
				R.warn("%s: only count figures; add a percentage, multiple or time if the work has one", bid)
			}
		} else if b.UnquantifiedOK == "" {
			R.err("%s: no figure. Add a {{clm}} quantity (%%, ×, time) or set unquantified_ok with the reason", bid)
		}
		if mLead := boldLeadRe.FindStringSubmatch(b.Text); mLead == nil {
			R.err("%s: missing bold outcome lead (**...:**); every bullet must open with a bold lead stating what was built/solved with plain domain keywords and the headline metric for non-technical readers", bid)
		} else {
			rawLead, rawTail := mLead[1], mLead[2]
			plainLead := resolvePlain(rawLead, cf.Claims)
			plainTail := resolvePlain(rawTail, cf.Claims)
			if len(toks) > 0 && !tokenRe.MatchString(rawLead) && !hasDigitRe.MatchString(plainLead) {
				R.err("%s: bold lead (%q) buries the metric in the tail; place the headline {{clm}} figure or improvement metric inside the bold lead so non-technical readers see the impact immediately", bid, plainLead)
			}
			var unglossedLJ []string
			for _, lj := range leadJargonRe().FindAllString(plainLead, -1) {
				if !contains(b.Glossed, lj) && !contains(b.Glossed, strings.ToLower(lj)) {
					unglossedLJ = append(unglossedLJ, lj)
				}
			}
			if len(unglossedLJ) > 0 {
				R.err("%s: bold lead contains low-level technical jargon (%s); keep the bold lead in plain domain keywords + headline metric for non-technical readers and move implementation jargon to the clause after the colon", bid, strings.Join(uniq(unglossedLJ), ", "))
			}
			if mSci := leadSciNotationRe.FindString(plainLead); mSci != "" {
				R.err("%s: bold lead contains raw scientific notation or unanchored metric (%q) that non-technical readers cannot interpret; state the plain-English significance in the bold lead and move raw error bounds/precision to the technical tail", bid, mSci)
			}
			lintBulletGuards(R, b, plainLead, plainTail)
			if tailW := len(strings.Fields(plainTail)); tailW < 6 {
				R.err("%s: technical tail after bold lead has only %d words (min 6); state the concrete methodology, architecture, or experimental scope after the bold lead for technical readers", bid, tailW)
			}
		}
		for _, lit := range literalFigures(b.Text, b.AllowLiterals) {
			R.err("%s: literal figure %q is not registered; add it to Claims.tex and use its token, or list it in allow_literals if it is a name or scope descriptor", bid, lit)
		}
		if m := numWord.FindAllString(tokenRe.ReplaceAllString(b.Text, ""), -1); len(m) > 0 {
			var unallowed []string
			for _, w := range m {
				if !contains(b.AllowLiterals, strings.ToLower(w)) && !contains(b.AllowLiterals, w) {
					unallowed = append(unallowed, w)
				}
			}
			if len(unallowed) > 0 {
				R.warn("%s: spelled-out count(s) %v carry a claim too; the statement check must confirm them (or register them)", bid, unallowed)
			}
		}
		if strings.Contains(b.Text, "\\") || strings.Contains(b.Text, "$\\") {
			R.err("%s: contains raw LaTeX backslash command; use Unicode symbols (≥, ≤, °C, ×, →, µs)", bid)
		}
		if regexp.MustCompile(`\d[^,;:.]*→\s*\d`).MatchString(plain) && !strings.Contains(plain, "%") && !strings.Contains(plain, "×") && !regexp.MustCompile(`(?i)\b(pp|points)\b`).MatchString(plain) {
			R.err("%s: states a raw before→after value transition without a percentage (%%) or multiplier (×) improvement/change", bid)
		}
		maxTrans := rules.MaxTransitions
		if maxTrans <= 0 {
			maxTrans = 4
		}
		if arrows := strings.Count(plain, "→"); arrows > maxTrans {
			R.err("%s: chains %d before→after transitions in one bullet (max %d); lead with the headline outcome and drop secondary sub-metrics", bid, arrows, maxTrans)
		}
		maxFigs := rules.MaxFigures
		if maxFigs <= 0 {
			maxFigs = 6
		}
		if len(toks) > maxFigs {
			R.err("%s: packs %d figure tokens into one bullet (max %d); focus on primary results so metrics stay scannable", bid, len(toks), maxFigs)
		}
		if m := stubColonRe.FindString(b.Text); m != "" {
			R.err("%s: broken stub clause between colon and semicolon (%q); integrate scope into the lead or main clause", bid, m)
		}
		if m := unitMismatchRe.FindString(plain); m != "" {
			R.err("%s: ambiguous unit or count-vs-byte mismatch (%q); keep units explicit and consistent across before/after comparisons", bid, m)
		}
		if m := dupWordRe().FindString(plain); m != "" {
			R.err("%s: duplicate word or awkward token expansion (%q) after resolving {{clm}} tokens", bid, m)
		}
		if m := unjustifiedParity(plain); m != "" {
			R.err("%s: claims parity with an external baseline (%q) without stating the structural differentiator (e.g., without a Python runtime, bit-identical determinism, or in-process Rust/Go embedding)", bid, m)
		}
		if strings.EqualFold(ref.Sec.Title, "Summary") {
			summaryN++
			if len(toks) > 0 && hasComparison(plain) {
				summaryStrong++
			}
			for _, p := range rules.SummaryBanned {
				if p = strings.ToLower(strings.TrimSpace(p)); p != "" && strings.Contains(strings.ToLower(plain), p) {
					R.err("%s: summary must state concrete capabilities and impact brought to the table, not generic coworker phrasing", bid)
					break
				}
			}
			if cns := codenameRe(rules.Codenames).FindAllString(plain, -1); len(cns) >= 2 {
				R.err("%s: Summary bullet names internal project codenames (%s) before they are defined; describe the capability and impact in Summary and introduce project names in their own sections", bid, strings.Join(uniq(cns), ", "))
			}
		}
		cleanParens := statNotationRe.ReplaceAllString(plain, "STAT")
		if regexp.MustCompile(`\([^()]*\([^()]*\)[^()]*\)`).MatchString(cleanParens) {
			R.err("%s: contains nested parentheses ((...)); flatten parentheticals for clean readability", bid)
		}
		if len(words) < rules.MinWords {
			R.warn("%s: %d words (target %d-%d)", bid, len(words), rules.MinWords, rules.MaxWords)
		} else if len(words) > rules.MaxWords {
			R.err("%s: %d words (max %d; two lines in the template)", bid, len(words), rules.MaxWords)
		}
		if ref.Sec.Kind != "bullets" && len(words) > 0 {
			first := strings.ToLower(regexp.MustCompile(`[^A-Za-z\-]`).ReplaceAllString(words[0], ""))
			if alt, isWeak := weakVerbs[first]; isWeak {
				R.err("%s: opens with weak verb %q; %s", bid, words[0], alt)
			} else if alt, isPres := presentVerbs[first]; isPres {
				R.err("%s: opens with present-tense verb %q; %s", bid, words[0], alt)
			} else if !verbs[first] && !strings.HasSuffix(first, "ed") {
				R.warn("%s: opens with %q, not an action verb", bid, words[0])
			}
		}
		for _, p := range bannedPatterns() {
			re, err := regexp.Compile(`(?i)` + p)
			if err != nil {
				die("config.json: bullet_rules.extra_banned pattern %q does not compile: %v", p, err)
			}
			if re.MatchString(plain) {
				R.err("%s: filler or self-assessment phrase /%s/", bid, p)
			}
		}
		if firstPerson.MatchString(plain) {
			R.err("%s: first person", bid)
		}
		if m := thirdPerson.FindString(plain); m != "" {
			R.err("%s: third-person pronoun or reference %q; use implied first-person voice", bid, m)
		}
		var jg []string
		for _, j := range jargon(plain) {
			if !contains(b.Glossed, j) {
				jg = append(jg, j)
			}
		}
		if len(jg) > rules.MaxJargon {
			R.warn("%s: %d terms an HR reader may not know (%s); lead with the plain outcome", bid, len(jg), strings.Join(jg, ", "))
		}
		work := ref.Ent != nil && !regexp.MustCompile(`(?i)^(education|publication|skill|certific)`).MatchString(ref.Sec.Title)
		if (work || ref.Sec.Kind == "bullets") && b.Need == "" {
			R.warn("%s: no 'need' recorded (why did this matter?)", bid)
		}
		if work && len(toks) > 0 && b.UnquantifiedOK == "" {
			figured++
			if hasComparison(plain) || strings.TrimSpace(b.ComparisonOK) != "" {
				compared++
			} else {
				uncompared = append(uncompared, bid)
				R.warn("%s: result has no baseline or comparator; say what it beats (before → after, × or %% vs a named alternative, or the same system without the change), or set comparison_ok with the reason", bid)
			}
		}
		for _, t := range toks {
			if cv := cf.Claims[t[1]]; cv != nil && cv.Caution != "" && strings.TrimSpace(b.CautionOK[t[1]]) == "" {
				msg := fmt.Sprintf("%s: %s is flagged in Claims.tex: %q. Drop it, or fix the wording and record why in caution_ok[%q]", bid, t[1], cv.Caution, t[1])
				if final {
					R.err("%s", msg)
				} else {
					R.warn("%s", msg)
				}
			}
		}
		// A wording a blind check already refuted cannot come back on any page,
		// draft or final, until it is reworded (which needs a new blind pass).
		for _, t := range toks {
			if cv := cf.Claims[t[1]]; cv != nil {
				if why := cv.RefutedSentences[sentenceHash(b.Text, t[1])]; why != "" {
					R.err("%s: a blind check refuted this wording for %s (%s); for SCOPE narrow the sentence to what the source measured, for MISMATCH fix the value in Claims.tex or have the figure re-checked, before reusing it", bid, t[1], truncate(why, 140))
				}
			}
		}
		if sv := cf.Statements[bid]; sv != nil && sv.Refuted != "" && sv.TextHash == textHash(b.Text) {
			R.err("%s: a blind check refuted this wording (%s); reword to what the source says before reusing it", bid, truncate(sv.Refuted, 140))
		}
		if why := testingCentric(b.Text, plain, cf.Claims); why != "" && b.TestingOK == "" {
			testing = append(testing, bid)
			if ref.Sec.Kind == "bullets" {
				R.err("%s: Summary bullet is about testing (%s); lead with what was built and its impact", bid, why)
			} else {
				R.warn("%s: reads as testing-centric (%s); lead with the capability or impact", bid, why)
			}
		}

		if final {
			th := textHash(plain)
			_ = th
			cur := sha1hex(plain)
			var rv = map[string]string{}
			for _, reader := range []string{"hr", "hm"} {
				// Twice = two independent passes. Round 2: the latest verdict on this
				// exact text must be a pass. The other pass is a round-1 pass, or an
				// earlier round-2 pass by a different reviewer (a bullet reworded
				// during round 2 has then been passed twice by that persona).
				var r1, r2 *Review
				for i, r := range b.Review[reader] {
					rr := b.Review[reader][i]
					if r.Round == 2 && r.TextHash == cur {
						if r.Verdict == "pass" {
							r2 = &rr
						} else {
							r2 = nil
						}
					}
				}
				for i, r := range b.Review[reader] {
					rr := b.Review[reader][i]
					if r.Verdict != "pass" {
						continue
					}
					if r.Round == 1 || (r2 != nil && r.Round == 2 && r.By != r2.By) {
						r1 = &rr
					}
				}
				if r1 == nil || r2 == nil {
					R.err("%s: %s reader has not passed this exact wording in both rounds (round1=%v, round2-on-current=%v)", bid, strings.ToUpper(reader), r1 != nil, r2 != nil)
					continue
				}
				if r2.By == "" || r2.By == b.Author || r2.By == "drafter" {
					R.err("%s: %s round-2 review has no independent reviewer (by=%q, author=%q)", bid, strings.ToUpper(reader), r2.By, b.Author)
				}
				rv[reader] = r2.By
			}
			if rv["hr"] != "" && rv["hr"] == rv["hm"] {
				R.err("%s: the same reviewer (%s) played both HR and hiring manager", bid, rv["hr"])
			}
			for _, t := range toks {
				cv := cf.Claims[t[1]]
				switch {
				case cv == nil:
					R.err("%s: %s is not in Claims.tex", bid, t[1])
				case !cv.DoubleVerified:
					R.err("%s: %s not double-verified (%s)", bid, t[1], strings.Join(cv.Problems, "; "))
				case !contains(cv.ConfirmedSentences, sentenceHash(b.Text, t[1])):
					R.err("%s: %s has not been blind-checked in THIS sentence", bid, t[1])
				}
			}
			sv := cf.Statements[bid]
			switch {
			case sv == nil:
				R.err("%s: no statement check in claims.json (export it with --used-in this resume)", bid)
			case sv.TextHash != textHash(b.Text):
				R.err("%s: statement check was for different wording", bid)
			case !sv.SourcesOK || !sv.Confirmed:
				R.err("%s: statement check incomplete: %s", bid, sv.Problem)
			case sv.Attested != "":
				R.warn("%s: attested by the person, no artifact (%s)", bid, truncate(sv.Attested, 80))
			}
		} else {
			for _, t := range toks {
				if cf.Claims[t[1]] == nil {
					R.err("%s: %s is not in Claims.tex", bid, t[1])
				}
			}
		}
	}

	minQ := rules.MinQuantShare
	if c.Meta.Pages == 4 && minQ > 0.65 {
		minQ = 0.65
	}
	if n > 0 && float64(quant)/float64(n) < minQ {
		R.err("only %d/%d bullets carry a figure (%.0f%%); target %.0f%%", quant, n, 100*float64(quant)/float64(n), 100*minQ)
	}
	if quant > 0 && float64(impact)/float64(quant) < rules.MinImpactShare {
		R.err("only %d/%d quantified bullets carry a percentage, multiple, time or rate (%.0f%%); target %.0f%%", impact, quant, 100*float64(impact)/float64(quant), 100*rules.MinImpactShare)
	}
	if figured > 0 && float64(compared)/float64(figured) < rules.MinCompareShare {
		msg := fmt.Sprintf("only %d/%d figured work bullets say what the result beats (%.0f%%; target %.0f%%). Add the baseline or named alternative to: %s", compared, figured, 100*float64(compared)/float64(figured), 100*rules.MinCompareShare, strings.Join(uncompared, ", "))
		if final {
			R.err("%s", msg)
		} else {
			R.warn("%s", msg)
		}
	}
	// The Summary is what a recruiter reads first; an identity line alone gives
	// them no result to remember. At least one Summary bullet must carry a
	// registered figure and say what it beats.
	if summaryN > 0 && summaryStrong == 0 {
		msg := fmt.Sprintf("Summary has %d bullet(s) and none carries a {{clm}} result with a comparison (before → after, × or %% vs a named alternative); lead the page with the strongest measured, compared achievement", summaryN)
		if final {
			R.err("%s", msg)
		} else {
			R.warn("%s", msg)
		}
	}
	capShare := rules.MaxTestingShare
	if c.Meta.MaxTestingShare != nil {
		capShare = *c.Meta.MaxTestingShare
	}
	if n > 0 && float64(len(testing))/float64(n) > capShare {
		R.err("%d/%d bullets are about testing, coverage or gates (cap %.0f%%): %s. Rewrite around what each thing does and its impact", len(testing), n, 100*capShare, strings.Join(testing, ", "))
	}
	blob := strings.ToLower(strings.Join(allText, " "))
	for _, s := range c.Sections {
		for _, l := range s.Lines {
			blob += " " + strings.ToLower(l.Label+" "+l.Text)
		}
		for _, e := range s.Entries {
			blob += " " + strings.ToLower(e.Title+" "+e.Stack+" "+strings.Join(e.Members, " "))
		}
	}
	var missing []string
	for _, k := range c.Meta.JDKeywords {
		if !strings.Contains(blob, strings.ToLower(k)) {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		R.warn("JD keywords not on the page: %s", strings.Join(missing, ", "))
	}
	checkRepeats(c, cf, R)
	checkPoints(c, cf, R)
	if cf.Generated != "" && cf.Guards == 0 {
		// A check that could not run must not look like one that passed.
		msg := "claims.json was exported by an older rkb and carries no refuted-wording record, so wordings a blind check refuted were not checked; re-run `rkb claims export`"
		if final {
			R.err("%s", msg)
		} else {
			R.warn("%s", msg)
		}
	}

	if final {
		for _, l := range c.leads() {
			sv := cf.Statements[l.Key]
			if sv == nil || !sv.Confirmed || sv.TextHash != textHash(l.Text) {
				p := "missing"
				if sv != nil {
					p = sv.Problem
				}
				R.err("%s: lead line not statement-checked (%s)", l.Key, p)
			}
		}
		R.Errors = append(R.Errors, finalPreconditions(c, cf, ecoPath)...)
	}
	return R
}

// finalPreconditions: the claims export and the ecosystem report must be for
// this exact resume, recent, and every declared integration link verified.
func finalPreconditions(c *Composed, cf *ClaimsFile, ecoPath string) []string {
	var errs []string
	if c.BankSHA != "" && c.BankPath != "" {
		if cur := sha256File(c.BankPath); cur != "" && cur != c.BankSHA {
			errs = append(errs, "the composed resume was made from an older bank.json (reviews or wording changed since); re-compose it")
		}
	}
	if cf.ResumeHash != c.resumeHash() {
		errs = append(errs, "claims.json was exported for different content; re-run `rkb claims export --used-in` on this resume")
	}
	errs = append(errs, renderResume(c, cf, true).R.coverage(cf)...)
	if h := ageHours(cf.Generated); h > cfg.FreshHours {
		errs = append(errs, fmt.Sprintf("claims.json is %.0f h old (max %.0f); re-export", h, cfg.FreshHours))
	}
	if t, ok := cf.VMReport["mtime"]; !ok || ageHours(t) > cfg.FreshHours {
		errs = append(errs, "validator report missing or stale; run `rkb claims vm` and export with --vm-report")
	}
	var links []string
	for _, ref := range c.allBullets() {
		links = append(links, ref.B.Links...)
	}
	for _, s := range c.Sections {
		for _, e := range s.Entries {
			links = append(links, e.Links...)
		}
	}
	if len(links) > 0 {
		if ecoPath == "" {
			errs = append(errs, "this resume declares integration links; pass --eco <ecosystem report json> from `rkb eco --json`")
		} else {
			var rep EcoReport
			if err := readJSON(ecoPath, &rep); err != nil {
				errs = append(errs, "eco report: "+err.Error())
			} else {
				if ageHours(rep.Generated) > cfg.FreshHours {
					errs = append(errs, "ecosystem report is stale; re-run `rkb eco --json`")
				}
				v := map[string]string{}
				for _, r := range rep.Links {
					v[r.From+"->"+r.To] = r.Verdict
				}
				for _, l := range links {
					k := normalizeLink(l)
					vv := v[k]
					// PARTIAL passes only with wording checked against what was
					// found (statement check); TOOLING only for a link the bullet
					// itself marks "(tooling)".
					if vv != "VERIFIED" && vv != "PARTIAL" && (vv != "TOOLING" || !strings.Contains(l, "tooling")) {
						errs = append(errs, fmt.Sprintf("integration link %s is %s in the ecosystem report", l, orDash(vv)))
					}
				}
			}
		}
	}
	return errs
}

func normalizeLink(l string) string {
	if i := strings.Index(l, "("); i > 0 { // "A->B (tooling)"
		l = l[:i]
	}
	p := strings.SplitN(strings.ReplaceAll(l, " ", ""), "->", 2)
	if len(p) != 2 {
		return l
	}
	return canonProject(p[0]) + "->" + canonProject(p[1])
}

func cmdLint(a *args) {
	sel := a.need(0, "resume")
	c := loadResume(sel)
	cf := loadClaimsFile(a.get("claims", "claims.json"))
	if p := a.get("sheet", ""); p != "" {
		writeSheet(c, cf, p, a.get("round", "1"))
		return
	}
	R := lint(c, cf, a.bools["final"], a.get("eco", ""))
	for _, w := range R.Warns {
		fmt.Println("WARN  " + w)
	}
	for _, e := range R.Errors {
		fmt.Println("ERROR " + e)
	}
	tag := ""
	if a.bools["final"] {
		tag = " [final gate]"
	}
	fmt.Printf("\n%d error(s), %d warning(s)%s\n", len(R.Errors), len(R.Warns), tag)
	if len(R.Errors) > 0 {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- reader review

type SheetItem struct {
	ID       string            `json:"id"`
	Section  string            `json:"section"`
	Entry    string            `json:"entry,omitempty"`
	Text     string            `json:"text"`
	Need     string            `json:"need"`
	TextHash string            `json:"text_hash"`
	HR       map[string]string `json:"hr"`
	HM       map[string]string `json:"hm"`
}

type Sheet struct {
	Round        int         `json:"round"`
	Target       string      `json:"target"`
	Reviewer     string      `json:"reviewer"`
	ResumeHash   string      `json:"resume_hash,omitempty"`
	Instructions string      `json:"instructions"`
	Page         string      `json:"page_text,omitempty"`
	Items        []SheetItem `json:"items"`
}

func writeSheet(c *Composed, cf *ClaimsFile, p, round string) {
	rnd, err := strconv.Atoi(round)
	if err != nil || rnd < 1 || rnd > 2 {
		die("--round %q: expected 1 or 2", round)
	}
	sh := Sheet{Round: rnd, Target: c.Meta.Target, ResumeHash: c.resumeHash(),
		Instructions: "See references/validation-protocol.md, 'Reader validation'. Set top-level reviewer to your name. Fill ONLY your persona's object (hr or hm) on every item, never both: verdict 'pass' or 'revise'; on revise a one-sentence note and a rewrite that keeps every figure exactly as written. Judge whether the text conveys the need, reading it in the context of the whole page (page_text). " + readerOwnerRules}
	// The whole page goes on every round: a reader judges a bullet in context
	// (repetition, what the section already said), not as an isolated line.
	sh.Page = renderResume(c, cf, false).Text
	for _, r := range c.allBullets() {
		plain := resolvePlain(r.B.Text, cf.Claims)
		ent := ""
		if r.Ent != nil {
			ent = r.Ent.Title
		}
		sh.Items = append(sh.Items, SheetItem{ID: r.B.ID, Section: r.Sec.Title, Entry: ent, Text: plain, Need: r.B.Need,
			TextHash: sha1hex(plain), HR: map[string]string{"verdict": "", "note": "", "rewrite": ""},
			HM: map[string]string{"verdict": "", "note": "", "rewrite": ""}})
	}
	writeJSON(p, sh)
	fmt.Printf("wrote reader worksheet %s (round %d, %d bullets)\n", p, rnd, len(sh.Items))
}

// normVerdict accepts pass/revise in any case; anything else is an error.
func normVerdict(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return "", nil
	case "pass", "passed", "ok":
		return "pass", nil
	case "revise", "fail", "rewrite":
		return "revise", nil
	}
	return "", fmt.Errorf("verdict %q is not pass or revise", v)
}

// reviewPlan is the validated content of one reader sheet, ready to apply.
type reviewPlan struct {
	persona string
	items   []reviewItem
	stale   []string
	skipped []string
	empty   []string
}

type reviewItem struct {
	id     string
	review Review
}

// planReview validates a filled reader sheet against the resume as it is now.
// current maps bullet id -> sha1 of the text a reader sees today; a verdict on
// any other text is stale and is not recorded.
func planReview(sh *Sheet, current map[string]string, authors map[string]string) (*reviewPlan, error) {
	if strings.TrimSpace(sh.Reviewer) == "" {
		return nil, fmt.Errorf("the sheet has no \"reviewer\"; each reader must name itself")
	}
	if sh.Round != 1 && sh.Round != 2 {
		return nil, fmt.Errorf("sheet round %d: expected 1 or 2", sh.Round)
	}
	rp := &reviewPlan{}
	seen := map[string]bool{}
	var problems []string
	for _, it := range sh.Items {
		if seen[it.ID] {
			problems = append(problems, it.ID+": listed twice")
			continue
		}
		seen[it.ID] = true
		vs := map[string]map[string]string{"hr": it.HR, "hm": it.HM}
		filled := 0
		for _, persona := range []string{"hr", "hm"} {
			v := vs[persona]
			verdict, err := normVerdict(v["verdict"])
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s (%s): %v", it.ID, persona, err))
				continue
			}
			if verdict == "" {
				continue
			}
			filled++
			if rp.persona == "" {
				rp.persona = persona
			} else if rp.persona != persona {
				problems = append(problems, fmt.Sprintf("%s: reviewer %q filled both hr and hm; one reader plays one persona per sheet", it.ID, sh.Reviewer))
				continue
			}
			cur, onPage := current[it.ID]
			switch {
			case !onPage:
				rp.skipped = append(rp.skipped, it.ID)
				continue
			case cur != it.TextHash:
				rp.stale = append(rp.stale, it.ID)
				continue
			}
			if a := authors[it.ID]; a != "" && strings.EqualFold(a, strings.TrimSpace(sh.Reviewer)) {
				problems = append(problems, fmt.Sprintf("%s: reviewer %q wrote this bullet; a bullet cannot be reviewed by its author", it.ID, sh.Reviewer))
				continue
			}
			if verdict == "revise" && strings.TrimSpace(v["note"]) == "" {
				problems = append(problems, fmt.Sprintf("%s (%s): revise without a note", it.ID, persona))
				continue
			}
			rp.items = append(rp.items, reviewItem{id: it.ID, review: Review{Round: sh.Round, Verdict: verdict,
				Note: strings.TrimSpace(v["note"]), Rewrite: strings.TrimSpace(v["rewrite"]), TextHash: it.TextHash,
				By: strings.TrimSpace(sh.Reviewer), At: nowISO()}})
		}
		if filled == 0 {
			rp.empty = append(rp.empty, it.ID)
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the sheet was not recorded:\n  %s", strings.Join(problems, "\n  "))
	}
	return rp, nil
}

// applyReview records verdicts on bank bullets. History is kept: a later
// revise must not erase an earlier pass. Only a re-ingest of the same sheet
// (same round, reviewer and text) replaces its earlier verdict.
func applyReview(b *Bank, persona string, items []reviewItem) (bool, error) {
	for _, it := range items {
		bl, ok := b.Bullets[it.id]
		if !ok {
			return false, fmt.Errorf("bullet %s is no longer in the bank", it.id)
		}
		if bl.Review == nil {
			bl.Review = map[string][]Review{}
		}
		var keep []Review
		for _, r := range bl.Review[persona] {
			if r.Round != it.review.Round || r.By != it.review.By || r.TextHash != it.review.TextHash {
				keep = append(keep, r)
			}
		}
		bl.Review[persona] = append(keep, it.review)
	}
	return len(items) > 0, nil
}

// cmdReviewIngest writes reader verdicts back into the bank.
func cmdReviewIngest(a *args) {
	a.npos(2, 2, "rkb review <resume> <reviewed_sheet.json> [--claims claims.json]")
	sel := a.pos[0]
	c := loadResume(sel)
	cfp := a.get("claims", "claims.json")
	if !fileExists(cfp) {
		die("review needs the claims export the sheet was written from (--claims %s not found): verdicts are checked against the text a reader saw", cfp)
	}
	cf := loadClaimsFile(cfp)
	var sh Sheet
	if err := readJSON(a.pos[1], &sh); err != nil {
		die("%v", err)
	}
	current, authors := map[string]string{}, map[string]string{}
	for _, r := range c.allBullets() {
		current[r.B.ID] = sha1hex(resolvePlain(r.B.Text, cf.Claims))
		authors[r.B.ID] = r.B.Author
	}
	rp, err := planReview(&sh, current, authors)
	if err != nil {
		die("%s: %v", a.pos[1], err)
	}
	for _, id := range rp.stale {
		fmt.Printf("STALE  %s: the sheet judged different wording than the resume has now; not recorded (write a new sheet)\n", id)
	}
	for _, id := range rp.skipped {
		fmt.Printf("skip   %s: not on this resume\n", id)
	}
	if len(rp.empty) > 0 {
		fmt.Printf("note: %d item(s) have no verdict: %s\n", len(rp.empty), humanList(rp.empty, 8))
	}
	if len(rp.items) == 0 {
		fmt.Println("nothing to record")
		if len(rp.stale) > 0 {
			os.Exit(1)
		}
		return
	}
	if err := updateBank(c.BankPath, func(b *Bank) (bool, error) { return applyReview(b, rp.persona, rp.items) }); err != nil {
		die("%v", err)
	}
	nrev := 0
	for _, it := range rp.items {
		if it.review.Verdict != "pass" {
			nrev++
			fmt.Printf("REVISE %s (%s): %s\n  -> %s\n", it.id, rp.persona, it.review.Note, it.review.Rewrite)
		}
	}
	fmt.Printf("round %d: %d %s verdict(s) from %s written to %s; %d revise request(s)\n", sh.Round, len(rp.items), strings.ToUpper(rp.persona), sh.Reviewer, c.BankPath, nrev)
	if len(rp.stale) > 0 {
		os.Exit(1)
	}
}
