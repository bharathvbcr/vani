package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cmdDoctor checks the whole toolchain in one pass and reports every problem
// it can find, instead of the first command that trips over one.
func cmdDoctor(a *args) {
	a.npos(0, 0, "rkb doctor [--resumes dir] [--strict]")
	var nErr, nWarn int
	say := func(level, area, f string, x ...any) {
		switch level {
		case "ERROR":
			nErr++
		case "WARN":
			nWarn++
		}
		fmt.Printf("%-5s %-10s %s\n", level, area, fmt.Sprintf(f, x...))
	}
	say("OK", "config", "%s", configSource)
	root := codeRoot()
	say("OK", "code-root", "%s", root)

	// binary
	if exe, err := os.Executable(); err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		if st, err := os.Stat(exe); err == nil {
			if sd := skillDir(); sd != "" {
				newest, f := newestSource(filepath.Join(sd, "rkb"))
				switch {
				case newest.IsZero():
					// The usual case: rkb is built from its own module, and the
					// skill holds only binaries, config and template.
					say("OK", "binary", "%s (version %s); no Go sources beside the skill, so freshness is not checked here", filepath.Base(exe), version)
				case newest.After(st.ModTime().Add(2 * time.Second)):
					say("WARN", "binary", "%s is older than %s (%s); rebuild: cd %s/rkb && go build -o ../bin/rkb-$(go env GOOS)-$(go env GOARCH) .",
						filepath.Base(exe), filepath.Base(f), newest.Format(time.RFC3339), sd)
				default:
					say("OK", "binary", "%s (version %s) is current with its sources", filepath.Base(exe), version)
				}
			}
		}
	}

	// claims
	claims, cerrs, cwarns := parseClaimsFiles(cfg.ClaimsTex, inRoot)
	for _, e := range cerrs {
		say("ERROR", "claims", "%s", e)
	}
	if len(cwarns) > 0 {
		say("INFO", "claims", "%d claim(s) have no [check] comment (pass 1 by manual read), e.g. %s", len(cwarns), cwarns[0])
	}
	caution := 0
	for _, c := range claims {
		if c.Caution != "" {
			caution++
		}
	}
	say("OK", "claims", "%d claims in %d file(s), %d flagged CAUTION/WITHDRAWN", len(claims), len(cfg.ClaimsTex), caution)

	// verify log
	if b, err := os.ReadFile(logPath()); errors.Is(err, os.ErrNotExist) {
		say("WARN", "log", "%s does not exist yet", logPath())
	} else if err != nil {
		say("ERROR", "log", "%v", err)
	} else {
		var l VerifyLog
		if err := decodeJSONStrict(logPath(), b, &l); err != nil {
			say("ERROR", "log", "%v", err)
		} else {
			n := 0
			for _, es := range l {
				n += len(es)
			}
			say("OK", "log", "%d entries for %d keys", n, len(l))
		}
	}

	// knowledge.json
	kp := inRoot(cfg.KnowledgeJSON)
	if !fileExists(kp) {
		say("WARN", "knowledge", "%s not found (entries with a group cannot compose)", kp)
	} else {
		var kb map[string]any
		if err := readJSON(kp, &kb); err != nil {
			say("ERROR", "knowledge", "%v", err)
		} else {
			say("OK", "knowledge", "%s parses", filepath.Base(kp))
		}
	}

	// repos
	var names []string
	for k := range cfg.Repos {
		names = append(names, k)
	}
	sort.Strings(names)
	missing := 0
	for _, k := range names {
		d, why := repoDirWhy(k)
		switch {
		case d == "":
			missing++
			say("WARN", "repo", "%s: %s", k, why)
		case why != "":
			say("WARN", "repo", "%s: %s", k, why)
		}
	}
	say("OK", "repo", "%d/%d configured projects found", len(names)-missing, len(names))

	// bank and every selection
	bp := inRoot(cfg.Bank)
	dir := a.get("resumes", filepath.Join(filepath.Dir(bp), "resumes"))
	if !fileExists(bp) {
		say("ERROR", "bank", "%s not found", bp)
	} else {
		var cl map[string]*Claim
		if len(cerrs) == 0 {
			cl = claims
		}
		r := checkBank(bp, dir, cl, root)
		for _, f := range r.Findings {
			say(f.Level, "bank", "%s: %s", f.Where, f.What)
		}
		say("OK", "bank", "%d bullets, %d resume(s) compose", r.Bullets, r.Resumes)
		// CAUTION figures in use
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		sort.Strings(files)
		for _, f := range files {
			if strings.HasSuffix(f, ".composed.json") {
				continue
			}
			c, errs := loadResumeErr(f)
			if len(errs) > 0 || c == nil {
				continue
			}
			for _, br := range c.allBullets() {
				for _, m := range tokenRe.FindAllStringSubmatch(br.B.Text, -1) {
					if x := claims[m[1]]; x != nil && x.Caution != "" && br.B.CautionOK[m[1]] == "" {
						say("WARN", "caution", "%s uses %s in %s: %s", filepath.Base(f), m[1], br.B.ID, truncate(x.Caution, 90))
					}
				}
			}
		}
	}

	// tools
	if p, err := chromePath(); err != nil {
		say("WARN", "tools", "%v (PDF rendering needs it)", err)
	} else {
		say("OK", "tools", "chrome: %s", p)
	}
	for _, t := range []struct{ name, why string }{
		{"pdftotext", "PDF fit and ATS checks"}, {"pdfinfo", "PDF page size"},
		{"soffice", "Word page count (optional)"}, {"pdflatex", "LaTeX PDF (optional)"},
	} {
		if p, err := exec.LookPath(t.name); err != nil {
			lvl := "WARN"
			if strings.Contains(t.why, "optional") {
				lvl = "INFO"
			}
			say(lvl, "tools", "%s missing — %s", t.name, t.why)
		} else {
			say("OK", "tools", "%s: %s", t.name, p)
		}
	}
	if g, err := exec.LookPath(goBinary()); err != nil {
		say("WARN", "tools", "go missing — `rkb claims vm` needs it; run `rkb setup`")
	} else {
		say("OK", "tools", "go: %s", g)
	}
	fmt.Printf("\ndoctor: %d error(s), %d warning(s)\n", nErr, nWarn)
	if nErr > 0 || (a.bools["strict"] && nWarn > 0) {
		os.Exit(1)
	}
}

// newestSource returns the newest non-test .go file under dir. The skill's
// assets (config.json, resume_template.html) are read at run time, so a newer
// one never calls for a rebuild.
func newestSource(dir string) (time.Time, string) {
	var t time.Time
	var f string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if st, err := d.Info(); err == nil && st.ModTime().After(t) {
			t, f = st.ModTime(), p
		}
		return nil
	})
	return t, f
}
