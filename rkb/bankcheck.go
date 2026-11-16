package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- bank check

type bankFinding struct {
	Level string `json:"level"` // ERROR | WARN
	Where string `json:"where"`
	What  string `json:"what"`
}

type bankReport struct {
	Bullets  int           `json:"bullets"`
	Entries  int           `json:"entries"`
	Resumes  int           `json:"resumes"`
	Unused   int           `json:"unused"`
	Findings []bankFinding `json:"findings"`
}

func (r *bankReport) add(level, where, f string, a ...any) {
	r.Findings = append(r.Findings, bankFinding{level, where, fmt.Sprintf(f, a...)})
}

func resolveBankClaimText(text string, claims map[string]*Claim) string {
	if claims == nil {
		return text
	}
	return tokenRe.ReplaceAllStringFunc(text, func(t string) string {
		if c := claims[tokenRe.FindStringSubmatch(t)[1]]; c != nil {
			return c.Value
		}
		return t
	})
}

func (r *bankReport) errors() int {
	n := 0
	for _, f := range r.Findings {
		if f.Level == "ERROR" {
			n++
		}
	}
	return n
}

// checkBank validates the bank on its own and every selection in dir.
// claims may be nil (Claims.tex unavailable): token checks are then skipped.
func checkBank(bp, dir string, claims map[string]*Claim, root string) *bankReport {
	bank := loadBank(bp)
	r := &bankReport{Bullets: len(bank.Bullets), Entries: len(bank.Entries)}
	var idx *basenameIndex
	if root != "" {
		idx = newBasenameIndex(root)
	}
	for _, id := range sortedKeys(bank.Bullets) {
		b := bank.Bullets[id]
		w := "bullet " + id
		if strings.TrimSpace(b.Text) == "" {
			r.add("ERROR", w, "empty text")
		}
		vanitySeen := map[string]bool{}
		for _, text := range []string{b.Text, resolveBankClaimText(b.Text, claims)} {
			for _, f := range lintAntiVanityGates(id, text) {
				if vanitySeen[f.Rule] {
					continue
				}
				vanitySeen[f.Rule] = true
				r.add("ERROR", w, "[%s] %s", f.Rule, f.Message)
			}
		}
		if strings.Count(b.Text, "{{") != strings.Count(b.Text, "}}") {
			r.add("ERROR", w, "unbalanced {{ }} in text")
		}
		if strings.Contains(b.Text, "\\") || strings.Contains(b.Text, "$\\") {
			r.add("ERROR", w, "contains raw LaTeX backslash command (use Unicode like ≥, ≤, °C, ×, →, µs so HTML/DOCX/LaTeX all render cleanly)")
		}
		if m := thirdPerson.FindString(b.Text); m != "" {
			r.add("ERROR", w, "contains third-person pronoun or reference %q; use implied first-person voice", m)
		}
		if m := stubColonRe.FindString(b.Text); m != "" {
			r.add("ERROR", w, "contains broken stub clause between colon and semicolon (%q)", m)
		}
		if m := unitMismatchRe.FindString(b.Text); m != "" {
			r.add("ERROR", w, "contains ambiguous unit or count-vs-byte mismatch (%q)", m)
		}
		if m := unjustifiedParity(b.Text); m != "" {
			r.add("ERROR", w, "claims parity with an external baseline (%q) without stating the structural differentiator (e.g., without Python, bit-identical determinism, or in-process embedding)", m)
		}
		if mLead := boldLeadRe.FindStringSubmatch(b.Text); mLead == nil {
			r.add("ERROR", w, "missing bold outcome lead (**...:**); every bullet must open with a bold lead for non-technical readers")
		} else {
			rawLead, rawTail := mLead[1], mLead[2]
			if strings.Contains(b.Text, "{{") && !tokenRe.MatchString(rawLead) && !hasDigitRe.MatchString(rawLead) {
				r.add("ERROR", w, "bold lead (%q) buries the metric in the tail; place the headline {{clm}} figure or improvement metric inside the bold lead", plainText(rawLead))
			}
			var unglossedLJ []string
			for _, lj := range leadJargonRe().FindAllString(plainText(rawLead), -1) {
				if !contains(b.Glossed, lj) && !contains(b.Glossed, strings.ToLower(lj)) {
					unglossedLJ = append(unglossedLJ, lj)
				}
			}
			if len(unglossedLJ) > 0 {
				r.add("ERROR", w, "bold lead contains low-level technical jargon (%s); move implementation jargon to the clause after the colon", strings.Join(uniq(unglossedLJ), ", "))
			}
			plainLeadBC := plainText(rawLead)
			if claims != nil {
				plainLeadBC = plainText(tokenRe.ReplaceAllStringFunc(rawLead, func(t string) string {
					if c := claims[tokenRe.FindStringSubmatch(t)[1]]; c != nil {
						return c.Value
					}
					return t
				}))
			}
			if mSci := leadSciNotationRe.FindString(plainLeadBC); mSci != "" {
				r.add("ERROR", w, "bold lead contains raw scientific notation or unanchored metric (%q) that non-technical readers cannot interpret; move it to the technical tail", mSci)
			}
			if tailW := len(strings.Fields(plainText(rawTail))); tailW < 6 {
				r.add("ERROR", w, "technical tail after bold lead has only %d words (min 6); state methodology, architecture, or scope after the bold lead", tailW)
			}
		}
		if claims != nil {
			for _, m := range tokenRe.FindAllStringSubmatch(b.Text, -1) {
				if _, ok := claims[m[1]]; !ok {
					r.add("ERROR", w, "token {{%s}} is not defined in Claims.tex", m[1])
				}
			}
		}
		if b.Attested != "" && len(b.Sources) > 0 {
			r.add("WARN", w, "attested and also lists sources; attested is only for work with no artifact")
		}
		for _, src := range b.Sources {
			if isURL(src) || root == "" || isUnmountedSibling(root, src) {
				continue
			}
			if !fileExists(filepath.Join(root, src)) && !dirExists(filepath.Join(root, src)) {
				msg := fmt.Sprintf("source %s does not exist", src)
				if c := idx.find(src); len(c) > 0 {
					msg += "; moved to " + humanList(c, 3) + "?"
				}
				r.add("ERROR", w, "%s", msg)
			}
		}
		for persona, revs := range b.Review {
			if persona != "hr" && persona != "hm" {
				r.add("ERROR", w, "review persona %q is not hr or hm", persona)
			}
			for _, rv := range revs {
				if rv.Verdict != "pass" && rv.Verdict != "revise" {
					r.add("ERROR", w, "%s review by %s has verdict %q", persona, rv.By, rv.Verdict)
				}
			}
		}
		for cid := range b.CautionOK {
			if !strings.Contains(b.Text, "{{"+cid+"}}") {
				r.add("WARN", w, "caution_ok names %s, which the text does not use", cid)
			}
		}
	}
	for _, id := range sortedKeys(bank.Entries) {
		for _, bid := range bank.Entries[id].Bullets {
			if _, ok := bank.Bullets[bid]; !ok {
				r.add("ERROR", "entry "+id, "default bullet %q is not in the bank", bid)
			}
		}
	}
	used := map[string]int{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".composed.json") {
			continue
		}
		c, errs := loadResumeErr(f)
		base := filepath.Base(f)
		for _, e := range errs {
			r.add("ERROR", base, "%s", strings.TrimPrefix(e, f+": "))
		}
		if c == nil {
			continue
		}
		if c.BankPath != "" && filepath.Clean(c.BankPath) != filepath.Clean(bp) {
			r.add("WARN", base, "uses a different bank (%s)", c.BankPath)
		}
		r.Resumes++
		expP := expectedPages(c, base)
		if c.Meta.Pages != expP {
			r.add("ERROR", base, "meta.pages is %d, expected %d (only master CV can be 4 pages; all other resumes must be 2 pages)", c.Meta.Pages, expP)
		}
		seen := map[string]bool{}
		for _, br := range c.allBullets() {
			if seen[br.B.ID] {
				r.add("ERROR", base, "bullet %s selected twice", br.B.ID)
			}
			seen[br.B.ID] = true
			used[br.B.ID]++
		}
	}
	for id := range bank.Bullets {
		if used[id] == 0 {
			r.Unused++
		}
	}
	return r
}

func cmdBankCheck(a *args) {
	a.npos(0, 0, "rkb bank check [--bank b] [--resumes dir] [--strict] [--json out]")
	bp := a.get("bank", inRoot(cfg.Bank))
	dir := a.get("resumes", filepath.Join(filepath.Dir(bp), "resumes"))
	var claims map[string]*Claim
	if cl, errs, _ := parseClaimsFiles(cfg.ClaimsTex, inRoot); len(errs) == 0 {
		claims = cl
	} else {
		fmt.Printf("WARN  Claims.tex not checked: %s\n", errs[0])
	}
	r := checkBank(bp, dir, claims, codeRoot())
	for _, f := range r.Findings {
		fmt.Printf("%-5s %s: %s\n", f.Level, f.Where, f.What)
	}
	if p := a.get("json", ""); p != "" {
		writeJSON(p, r)
	}
	ne := r.errors()
	fmt.Printf("\nbank: %d bullets, %d entries; %d resume(s) compose; %d bullet(s) unused (the pool to draw from); %d error(s), %d warning(s)\n",
		r.Bullets, r.Entries, r.Resumes, r.Unused, ne, len(r.Findings)-ne)
	if ne > 0 || (a.bools["strict"] && len(r.Findings) > 0) {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- moved files

// basenameIndex finds where a missing file went after a reorganisation.
type basenameIndex struct {
	root  string
	built bool
	by    map[string][]string
}

func newBasenameIndex(root string) *basenameIndex { return &basenameIndex{root: root} }

var heavyDirs = map[string]bool{".git": true, "node_modules": true, "target": true, ".venv": true, "venv": true,
	"__pycache__": true, "dist": true, "build": true, ".next": true, ".cache": true, "vendor": true, ".gradle": true}

func (x *basenameIndex) find(rel string) []string {
	if x == nil {
		return nil
	}
	if !x.built {
		x.built, x.by = true, map[string][]string{}
		n := 0
		_ = filepath.WalkDir(x.root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if heavyDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && p != x.root) {
					return filepath.SkipDir
				}
				return nil
			}
			if n++; n > 400000 {
				return filepath.SkipAll
			}
			r, _ := filepath.Rel(x.root, p)
			x.by[d.Name()] = append(x.by[d.Name()], filepath.ToSlash(r))
			return nil
		})
	}
	cands := x.by[filepath.Base(rel)]
	// prefer candidates sharing the most trailing path components
	score := func(c string) int {
		a, b := strings.Split(c, "/"), strings.Split(filepath.ToSlash(rel), "/")
		k := 0
		for k < len(a) && k < len(b) && a[len(a)-1-k] == b[len(b)-1-k] {
			k++
		}
		return k
	}
	out := append([]string(nil), cands...)
	sort.SliceStable(out, func(i, j int) bool { return score(out[i]) > score(out[j]) })
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// ---------------------------------------------------------------- bank set

// cmdBankSet edits one bullet safely (locked compare-and-swap; a reworded
// bullet's old reviews stay in history and no longer match its text hash).
func cmdBankSet(a *args) {
	a.npos(1, 1, "rkb bank set <bullet-id> [--text t] [--need n] [--sources a,b] [--by who] [--bank b]")
	id := a.pos[0]
	bp := a.get("bank", inRoot(cfg.Bank))
	_, hasText := a.opts["text"]
	_, hasNeed := a.opts["need"]
	_, hasSrc := a.opts["sources"]
	if !hasText && !hasNeed && !hasSrc {
		die("nothing to set: give --text, --need and/or --sources")
	}
	text := strings.TrimSpace(a.get("text", ""))
	if hasText && text == "" {
		die("--text is empty")
	}
	if hasText {
		if strings.Count(text, "{{") != strings.Count(text, "}}") {
			die("--text has unbalanced {{ }}")
		}
		if claims, errs, _ := parseClaimsFiles(cfg.ClaimsTex, inRoot); len(errs) == 0 {
			for _, m := range tokenRe.FindAllStringSubmatch(text, -1) {
				if _, ok := claims[m[1]]; !ok {
					die("--text uses {{%s}}, which is not defined in Claims.tex", m[1])
				}
			}
		}
	}
	var srcs []string
	if hasSrc {
		for _, s := range strings.Split(a.get("sources", ""), ",") {
			if s = cleanRel(strings.TrimSpace(s)); s != "" {
				if !isURL(s) && !fileExists(inRoot(s)) && !dirExists(inRoot(s)) {
					die("source %s does not exist under %s", s, codeRoot())
				}
				srcs = append(srcs, s)
			}
		}
	}
	created := false
	err := updateBank(bp, func(b *Bank) (bool, error) {
		bl, ok := b.Bullets[id]
		if !ok {
			if !hasText {
				return false, fmt.Errorf("bullet %s is not in the bank (give --text to create it)", id)
			}
			bl = &Bullet{ID: id}
			b.Bullets[id] = bl
			created = true
		}
		changed := created
		if hasText && bl.Text != text {
			bl.Text, changed = text, true
			bl.Author = a.get("by", "drafter")
		}
		if hasNeed && bl.Need != a.get("need", "") {
			bl.Need, changed = strings.TrimSpace(a.get("need", "")), true
			if bl.Need == "" {
				b.allowDrop = append(b.allowDrop, "$.bullets."+id+".need")
			}
		}
		if hasSrc && strings.Join(bl.Sources, ",") != strings.Join(srcs, ",") {
			bl.Sources, changed = srcs, true
			if len(srcs) == 0 {
				b.allowDrop = append(b.allowDrop, "$.bullets."+id+".sources")
			}
		}
		return changed, nil
	})
	if err != nil {
		die("%v", err)
	}
	verb := "updated"
	if created {
		verb = "created"
	}
	fmt.Printf("%s %s in %s\n", verb, id, bp)
}
