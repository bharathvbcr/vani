// Command rkb builds and verifies resumes from one evidence-backed bullet bank.
// Standard library only; external tools: Chrome (PDF), poppler (pdftotext,
// pdfinfo), optional LibreOffice (Word page count), optional pdflatex.
package main

import (
	"fmt"
	"os"
	"strings"
)

// version is stamped at build time: -ldflags "-X main.version=<sha>".
var version = "dev"

const usage = `rkb — resume knowledge base

Content (single source)
  rkb bank import-tex [--force]           one-time: LaTeX CV sources -> bank.json + resumes/*.json
  rkb bank check [--strict] [--json f]    bank + every resume: compose errors, missing/moved sources,
                                          unregistered tokens, bad reviews; exit 1 on errors
  rkb bank set <id> [--text t] [--need n] [--sources a,b] [--by who]   edit/create one bullet safely
  rkb compose <resume.json> [--out f]     write the composed resume (for machines without the repos)

Claims (run where the repos are)
  rkb claims vm [--out vm_report.txt] [--only ids|--skip ids] [--append] [--force]
                                          run the CV validator; a crashed run never replaces a good report
  rkb claims export --used-in <resume> --vm-report vm_report.txt [--out claims.json]
  rkb claims status <resume> --vm-report vm_report.txt [--quiet]
  rkb claims worksheet <resume> [--vm-report r] [--out blind.json] [--all] [--split N]
  rkb claims ingest <answers.json> [more.json ...]   pass 2: blind verifier answers (quotes are checked)
  rkb claims add --claim clmX --found "v" --source-path p [--by who] [--evidence e]   (manual pass 1)
  rkb eco [--json eco.json]               verify ecosystem links in knowledge.json
  rkb kbcheck [--strict]                  knowledge.json figures vs Claims.tex

Words
  rkb lint <resume> --claims claims.json [--final --eco eco.json]
  rkb lint <resume> --claims claims.json --sheet reviews_rN.json --round N
  rkb review <resume> <reviews.json> [--claims claims.json]   record reader verdicts (stale ones refused)

Outputs
  rkb render <resume> --claims c.json [--out-dir d] [--final --eco e]   txt + ascii txt + html
  rkb fit <resume.html> --pages N [--strict] [--final] [--out f.pdf] [--report fit.json]
  rkb ats <resume.pdf> <resume.txt>
  rkb docx <resume> --claims c.json [--pages N] [--font-pt 10] [--out f.docx]
  rkb latex <resume> [--out-dir d] [--compile]
  rkb build <resume> --claims c.json [--out-dir d]            all drafts, ungated
  rkb finalize <resume> --claims c.json [--eco e] [--out-dir d]   fail-closed release (staged, then published)

Environment
  rkb doctor [--strict]                   check config, repos, Claims.tex, bank, log, tools, binary
  rkb setup                               Go toolchain + golang.org mirror for the device shell; tool check
  rkb version
Common flags: --code-root <dir> (or RKB_CODE_ROOT), RKB_CHROME=<chrome binary>
Unknown or repeated flags are refused. Shared files (bank.json, Claims.verify.json) are
written atomically under a lock and never over an edit made by another tool.
`

// flagTable: the flags each command reads. Bool flags are listed in boolFlags.
var flagTable = map[string][]string{
	"bank import-tex":  {"force", "bank", "resumes"},
	"bank check":       {"bank", "resumes", "strict", "json"},
	"bank set":         {"bank", "text", "need", "sources", "by"},
	"compose":          {"out"},
	"claims export":    {"used-in", "vm-report", "out"},
	"claims status":    {"vm-report", "quiet"},
	"claims worksheet": {"vm-report", "out", "all", "split"},
	"claims ingest":    {},
	"claims add":       {"claim", "found", "source-path", "by", "evidence", "pass"},
	"claims vm":        {"out", "only", "skip", "append", "python", "force"},
	"eco":              {"json", "knowledge"},
	"kbcheck":          {"strict", "knowledge"},
	"lint":             {"claims", "final", "eco", "sheet", "round"},
	"review":           {"claims"},
	"render":           {"claims", "out-dir", "final", "eco"},
	"fit":              {"pages", "strict", "final", "out", "report"},
	"ats":              {},
	"docx":             {"claims", "pages", "font-pt", "out", "out-dir"},
	"latex":            {"out-dir", "compile"},
	"build":            {"claims", "pages", "out-dir"},
	"finalize":         {"claims", "eco", "pages", "out-dir"},
	"setup":            {"force", "go-version"},
	"doctor":           {"resumes", "strict"},
}

// posTable: how many positional arguments each command takes (max -1: any).
var posTable = map[string][2]int{
	"bank import-tex": {0, 0}, "bank check": {0, 0}, "bank set": {1, 1}, "compose": {1, 1},
	"claims export": {0, 0}, "claims status": {1, 1}, "claims worksheet": {1, 1}, "claims ingest": {1, -1},
	"claims add": {0, 0}, "claims vm": {0, 0}, "eco": {0, 0}, "kbcheck": {0, 0}, "lint": {1, 1},
	"review": {2, 2}, "render": {1, 1}, "fit": {1, 1}, "ats": {2, 2}, "docx": {1, 1}, "latex": {1, 1},
	"build": {1, 1}, "finalize": {1, 1}, "setup": {0, 0}, "doctor": {0, 0},
}

var boolFlags = []string{"final", "strict", "quiet", "all", "force", "compile", "append"}

// argsFor parses and validates the flags of one command.
func argsFor(cmd string, in []string) *args {
	allowed, ok := flagTable[cmd]
	if !ok {
		die("internal: no flag table for %q", cmd)
	}
	// names first, so a misspelt flag is reported as unknown rather than
	// "needs a value" (or worse, swallowing the next argument)
	pre := &args{}
	for _, s := range in {
		if s == "--" {
			break
		}
		if strings.HasPrefix(s, "--") && len(s) > 2 {
			pre.seen = append(pre.seen, strings.SplitN(s[2:], "=", 2)[0])
		}
	}
	if err := pre.check("rkb "+cmd, allowed); err != nil {
		die("%v", err)
	}
	a := parseArgs(in, boolFlags...)
	if n, ok := posTable[cmd]; ok {
		a.npos(n[0], n[1], usageLine(cmd))
	}
	return a
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Print(usage)
		return
	}
	if os.Args[1] == "version" || os.Args[1] == "--version" {
		fmt.Println("rkb " + version)
		return
	}
	watchSignals()
	loadConfig()
	cmd, rest := os.Args[1], os.Args[2:]
	sub := func(names string) (string, []string) {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			die("%s subcommands: %s", cmd, names)
		}
		k := cmd + " " + rest[0]
		if _, ok := flagTable[k]; !ok {
			die("%s subcommands: %s (got %q)", cmd, names, rest[0])
		}
		return rest[0], rest[1:]
	}
	switch cmd {
	case "bank":
		s, r := sub("import-tex | check | set")
		a := argsFor("bank "+s, r)
		switch s {
		case "import-tex":
			cmdImportTex(a)
		case "check":
			cmdBankCheck(a)
		case "set":
			cmdBankSet(a)
		}
	case "claims":
		s, r := sub("export | status | worksheet | ingest | add | vm")
		cmdClaims(s, argsFor("claims "+s, r))
	case "compose":
		cmdCompose(argsFor(cmd, rest))
	case "eco":
		cmdEco(argsFor(cmd, rest))
	case "kbcheck":
		cmdKBCheck(argsFor(cmd, rest))
	case "lint":
		cmdLint(argsFor(cmd, rest))
	case "review":
		cmdReviewIngest(argsFor(cmd, rest))
	case "render":
		cmdRender(argsFor(cmd, rest))
	case "fit":
		cmdFit(argsFor(cmd, rest))
	case "ats":
		cmdATS(argsFor(cmd, rest))
	case "docx":
		cmdDocx(argsFor(cmd, rest))
	case "latex":
		cmdLatex(argsFor(cmd, rest))
	case "build":
		cmdBuild(argsFor(cmd, rest))
	case "finalize":
		cmdFinalize(argsFor(cmd, rest))
	case "setup":
		cmdSetup(argsFor(cmd, rest))
	case "doctor":
		cmdDoctor(argsFor(cmd, rest))
	default:
		fmt.Fprintf(os.Stderr, "rkb: unknown command %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// usageLine returns the help line for cmd.
func usageLine(cmd string) string {
	for _, l := range strings.Split(usage, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "rkb "+cmd+" ") || t == "rkb "+cmd {
			if i := strings.Index(t, "   "); i > 0 {
				t = t[:i]
			}
			return t
		}
	}
	return "rkb " + cmd + " (see rkb help)"
}
