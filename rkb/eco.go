package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// An integration claim ("the dashboard runs the indexer in-process") gets two checks:
//   MANIFEST  B is a declared dependency of A (dependency sections only:
//             Cargo [*dependencies*]/features, go.mod require/replace,
//             package.json dependency maps, Package.swift .package, gradle deps)
//   SOURCE    A's code imports / links / spawns B (comments skipped)
// VERIFIED = both, PARTIAL = one (say exactly which), NONE = neither (keep it
// off the resume), SKIP = repo not on this machine (not a pass).

type EcoLink struct {
	Link     string   `json:"link"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Verdict  string   `json:"verdict"`
	Manifest []string `json:"manifest,omitempty"`
	Source   []string `json:"source,omitempty"`
	Kind     string   `json:"kind,omitempty"`
}

type EcoReport struct {
	Generated string    `json:"generated"`
	CodeRoot  string    `json:"code_root"`
	Links     []EcoLink `json:"links"`
}

var linkRe2 = regexp.MustCompile(`^(.+?) builds on (.+?):\s*(.*)$`)

func canonProject(name string) string {
	for _, part := range regexp.MustCompile(`\s*[·(/:,]\s*`).Split(name, -1) {
		p := strings.Trim(part, ") ")
		for k := range cfg.Repos {
			if strings.EqualFold(k, p) {
				return k
			}
		}
	}
	return strings.TrimSpace(name)
}

// repoDir picks the configured path that really holds the project: the first
// one with a manifest or .git. A directory left behind by a move (a few stray
// files, no manifest) is only used when nothing better exists.
func repoDir(name string) string {
	d, _ := repoDirWhy(name)
	return d
}

func repoDirWhy(name string) (string, string) {
	fallback := ""
	for _, rel := range cfg.Repos[name] {
		p := inRoot(rel)
		if !dirExists(p) {
			continue
		}
		if looksLikeProject(p) {
			return p, ""
		}
		if fallback == "" {
			fallback = p
		}
	}
	if fallback != "" {
		return fallback, fallback + " has no manifest or .git (left over from a move?)"
	}
	return "", "none of " + strings.Join(cfg.Repos[name], ", ") + " exists"
}

var projectMarkers = []string{".git", "go.mod", "Cargo.toml", "package.json", "pyproject.toml", "setup.py",
	"requirements.txt", "Package.swift", "build.gradle", "build.gradle.kts", "pom.xml", "CMakeLists.txt", "Makefile", "README.md"}

func looksLikeProject(p string) bool {
	for _, m := range append(projectMarkers, cfg.ManifestFiles...) {
		if _, err := os.Stat(filepath.Join(p, m)); err == nil {
			return true
		}
	}
	return false
}

func aliases(name string) []string {
	if a, ok := cfg.DepAliases[name]; ok {
		return a
	}
	return []string{strings.ToLower(name)}
}

var skipDirs = map[string]bool{"node_modules": true, "target": true, ".git": true, "dist": true, "build": true,
	".venv": true, "venv": true, "vendor": true, "tmp": true, "__pycache__": true, ".next": true, "out": true, "coverage": true}

func hasAlias(s string, al []string) bool {
	l := strings.ToLower(s)
	for _, a := range al {
		if strings.Contains(l, a) {
			return true
		}
	}
	return false
}

func scanManifests(repo string, al []string) []string {
	var hits []string
	_ = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(repo, p)
			// dot-dirs (.claude/worktrees, .cache, …) hold scratch copies, not the repo
			if skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && p != repo) || strings.Count(rel, string(os.PathSeparator)) > 4 {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		rel, _ := filepath.Rel(repo, p)
		switch name {
		case "package.json":
			var pj map[string]json.RawMessage
			b, _ := os.ReadFile(p)
			if json.Unmarshal(b, &pj) != nil {
				return nil
			}
			for _, k := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
				var deps map[string]string
				if json.Unmarshal(pj[k], &deps) == nil {
					for dep, ver := range deps {
						if hasAlias(dep, al) || hasAlias(ver, al) {
							hits = append(hits, fmt.Sprintf("%s: %s %q: %q", rel, k, dep, ver))
						}
					}
				}
			}
		case "Cargo.toml", "go.mod", "Package.swift", "build.gradle.kts", "build.gradle", "pyproject.toml":
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer func() { _ = f.Close() }() // read-only
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			section, inRequire, ln := "", false, 0
			for sc.Scan() {
				ln++
				line := strings.TrimSpace(sc.Text())
				if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
					continue
				}
				dep := false
				switch name {
				case "Cargo.toml":
					if strings.HasPrefix(line, "[") {
						section = line
						continue
					}
					dep = strings.Contains(section, "dependencies") || strings.Contains(section, "features") ||
						strings.Contains(line, "dep:")
				case "go.mod":
					if strings.HasPrefix(line, "require (") || strings.HasPrefix(line, "replace (") {
						inRequire = true
						continue
					}
					if line == ")" {
						inRequire = false
						continue
					}
					dep = inRequire || strings.HasPrefix(line, "require ") || strings.HasPrefix(line, "replace ")
				case "Package.swift":
					dep = strings.Contains(line, ".package(") || strings.Contains(line, ".product(")
				case "pyproject.toml":
					if strings.HasPrefix(line, "[") {
						section = line
						continue
					}
					dep = strings.Contains(section, "dependencies") || strings.HasPrefix(line, "dependencies")
				default:
					dep = regexp.MustCompile(`(implementation|api|compileOnly)\s*\(`).MatchString(line)
				}
				if dep && hasAlias(line, al) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, ln, truncate(line, 110)))
					break
				}
			}
		}
		return nil
	})
	return hits
}

var srcExt = map[string]bool{".rs": true, ".go": true, ".ts": true, ".tsx": true, ".js": true, ".mjs": true,
	".py": true, ".swift": true, ".kt": true, ".svelte": true, ".sh": true}

func scanSource(repo string, al []string) []string {
	var parts []string
	for _, a := range al {
		parts = append(parts, regexp.QuoteMeta(a))
	}
	pat := regexp.MustCompile(`(?i)` + strings.Join(parts, "|"))
	kw := regexp.MustCompile(`\b(use|import|require|from|extern|spawn|Command|exec|mod)\b`)
	goImp := regexp.MustCompile(`^(\w+\s+)?"[^"]+"$`)
	rsPath := regexp.MustCompile(`\b\w+::`)
	var hit []string
	n := 0
	stop := fmt.Errorf("stop")
	_ = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && p != repo) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(p)
		if !srcExt[ext] {
			return nil
		}
		n++
		if n > 8000 {
			return stop
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }() // read-only
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		ln := 0
		for sc.Scan() {
			ln++
			code := strings.TrimSpace(sc.Text())
			if code == "" || strings.HasPrefix(code, "//") || strings.HasPrefix(code, "#") || strings.HasPrefix(code, "*") ||
				strings.HasPrefix(code, "/*") || strings.HasPrefix(code, "--") || strings.HasPrefix(code, `"""`) {
				continue
			}
			if !pat.MatchString(code) {
				continue
			}
			if kw.MatchString(code) || (ext == ".go" && goImp.MatchString(code)) || (ext == ".rs" && rsPath.MatchString(code)) {
				rel, _ := filepath.Rel(repo, p)
				hit = []string{fmt.Sprintf("%s:%d: %s", rel, ln, truncate(code, 110))}
				return stop
			}
		}
		return nil
	})
	return hit
}

func checkLink(from, to string) EcoLink {
	a, b := canonProject(from), canonProject(to)
	r := EcoLink{Link: a + "->" + b, From: a, To: b}
	repo := repoDir(a)
	if repo == "" {
		r.Verdict = "SKIP"
		return r
	}
	al := aliases(b)
	r.Manifest = scanManifests(repo, al)
	r.Source = scanSource(repo, al)
	switch {
	case len(r.Manifest) > 0 && len(r.Source) > 0:
		r.Verdict = "VERIFIED"
	case len(r.Manifest) > 0:
		r.Verdict, r.Kind = "PARTIAL", "declared dependency, no code reference found"
	case len(r.Source) > 0:
		r.Verdict, r.Kind = "PARTIAL", "code reference (e.g. spawned binary), not a declared dependency"
	default:
		if t := scanTooling(repo, b, al); len(t) > 0 {
			r.Source = t
			r.Verdict, r.Kind = "TOOLING", "wired into the development workflow (config/MCP/dev docs), not the build: call it development tooling"
		} else {
			r.Verdict = "NONE"
		}
	}
	return r
}

// scanTooling: B used as development tooling of A — a root config dir named
// after B (.<alias>/), an MCP registration naming B, or A's own development
// docs/scripts naming B. Only consulted when there is no build or code link.
func scanTooling(repo, b string, all []string) []string {
	var hits, al []string
	for _, a := range all { // short aliases (e.g. "x-") match too much prose
		if len(a) >= 4 {
			al = append(al, a)
		}
	}
	if len(al) == 0 {
		return nil
	}
	for _, a := range al {
		a = strings.TrimSuffix(a, "-")
		if a != "" && dirExists(filepath.Join(repo, "."+a)) {
			hits = append(hits, "."+a+"/ (config dir)")
			break
		}
	}
	for _, f := range []string{".mcp.json", ".cursor/mcp.json", ".vscode/mcp.json", ".claude/settings.json", ".claude/settings.local.json"} {
		if body, err := os.ReadFile(filepath.Join(repo, f)); err == nil && hasAlias(string(body), al) {
			hits = append(hits, f+" (MCP/agent config names "+b+")")
			break
		}
	}
	for _, d := range []string{"docs", "Scripts", "scripts"} {
		matches, _ := filepath.Glob(filepath.Join(repo, d, "*"))
		for _, m := range append(matches, filepath.Join(repo, "README.md"), filepath.Join(repo, "CONTRIBUTING.md")) {
			ext := filepath.Ext(m)
			if ext != ".md" && ext != ".sh" {
				continue
			}
			f, err := os.Open(m)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			ln := 0
			for sc.Scan() {
				ln++
				if hasAlias(sc.Text(), al) {
					rel, _ := filepath.Rel(repo, m)
					hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, ln, truncate(strings.TrimSpace(sc.Text()), 110)))
					break
				}
			}
			_ = f.Close() // read-only
			if len(hits) > 0 && strings.Contains(hits[len(hits)-1], ":") {
				return hits
			}
		}
	}
	return hits
}

func cmdEco(a *args) {
	var kb struct {
		Ecosystem struct {
			Links  []string `json:"links"`
			Groups []Group  `json:"groups"`
		} `json:"ecosystem"`
	}
	kp := a.get("knowledge", inRoot(cfg.KnowledgeJSON))
	if err := readJSON(kp, &kb); err != nil {
		if len(cfg.Groups) > 0 {
			kb.Ecosystem.Groups = cfg.Groups
		} else {
			die("%v", err)
		}
	}
	pairs := map[string][2]string{}
	var order []string
	add := func(f, t string) {
		k := canonProject(f) + "->" + canonProject(t)
		if _, ok := pairs[k]; !ok {
			pairs[k] = [2]string{f, t}
			order = append(order, k)
		}
	}
	for _, s := range kb.Ecosystem.Links {
		if m := linkRe2.FindStringSubmatch(s); m != nil {
			add(m[1], m[2])
		}
	}
	for _, g := range kb.Ecosystem.Groups {
		for _, l := range g.Links {
			p := strings.SplitN(l, "->", 2)
			if len(p) == 2 {
				add(strings.TrimSpace(p[0]), strings.TrimSpace(p[1]))
			}
		}
	}
	rep := EcoReport{Generated: nowISO(), CodeRoot: codeRoot()}
	t := map[string]int{}
	for _, k := range order {
		p := pairs[k]
		r := checkLink(p[0], p[1])
		rep.Links = append(rep.Links, r)
		t[r.Verdict]++
		ev := append(append([]string{}, r.Manifest...), r.Source...)
		if len(ev) > 2 {
			ev = ev[:2]
		}
		why := strings.Join(ev, " | ")
		if r.Kind != "" {
			why = r.Kind + ": " + why
		}
		fmt.Printf("[%-8s] %s: %s\n", r.Verdict, r.Link, truncate(why, 200))
	}
	if j := a.get("json", ""); j != "" {
		writeJSON(j, rep)
	}
	b, _ := json.Marshal(t)
	fmt.Printf("\n%s\n", b)
	if t["NONE"] > 0 {
		os.Exit(1)
	}
}
