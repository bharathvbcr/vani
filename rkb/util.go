package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed assets/config.json assets/resume_template.html
var assets embed.FS

// ---------------------------------------------------------------- config

type Config struct {
	CVDir         string              `json:"cv_dir"`
	ClaimsTex     []string            `json:"claims_tex"`
	VerifyLog     string              `json:"verify_log"`
	Validator     string              `json:"validator"`
	KnowledgeJSON string              `json:"knowledge_json"`
	DossiersDir   string              `json:"dossiers_dir"`
	HTMLOutDir    string              `json:"html_out_dir"`
	Bank          string              `json:"bank"`
	ResumesDir    string              `json:"resumes_dir"`
	LatexOutDir   string              `json:"latex_out_dir"`
	FreshHours    float64             `json:"freshness_hours"`
	ChromePaths   []string            `json:"chrome_paths"`
	Repos         map[string][]string `json:"repos"`
	DepAliases    map[string][]string `json:"dependency_aliases"`
	ManifestFiles []string            `json:"manifest_files"`
	Groups        []Group             `json:"groups,omitempty"`
	Layout        struct {
		Paper         string                    `json:"paper"`
		Margin        map[string]float64        `json:"margin_in"`
		MinLastFill   float64                   `json:"min_last_page_fill"`
		MinInnerFill  float64                   `json:"min_inner_page_fill"`
		DensityRanges map[string][3]interface{} `json:"density_range"`
	} `json:"layout"`
	Rules struct {
		MinWords        int      `json:"min_words"`
		MaxWords        int      `json:"max_words"`
		MaxLines        int      `json:"max_rendered_lines"`
		PreferredLines  int      `json:"preferred_rendered_lines"`
		OrphanRatio     float64  `json:"orphan_last_line_ratio"`
		MinQuantShare   float64  `json:"min_quantified_share"`
		MinImpactShare  float64  `json:"min_impact_share"`
		MinCompareShare float64  `json:"min_comparison_share"`
		MaxJargon       int      `json:"max_unglossed_jargon"`
		MaxFigures      int      `json:"max_figures"`
		MaxTransitions  int      `json:"max_transitions"`
		MaxTestingShare float64  `json:"max_testing_share"`
		TestingTerms    []string `json:"testing_terms"`
		NumberWords     []string `json:"number_words"`
		Codenames       []string `json:"codenames"`
		// Per-user additions to compiled-in generic lists. Empty adds nothing.
		LeadJargon    []string `json:"lead_jargon"`    // words that must not sit in a bold lead unglossed (whole word, any case)
		TechTerms     []string `json:"tech_terms"`     // words counted as jargon in a bullet (any case)
		ExtraBanned   []string `json:"extra_banned"`   // regexps (case-insensitive) refused as filler, after the built-in list
		DupPhrases    []string `json:"dup_phrases"`    // regexps (case-insensitive) refused as duplicate or garbled wording
		SummaryBanned []string `json:"summary_banned"` // phrases (any case) refused in a Summary bullet
		// Points the person's bullets keep making in different words; two
		// bullets on one page that make the same point are refused.
		Mechanisms []MechanismSpec `json:"mechanisms"`
		// External baselines whose "matched X" claim needs a stated
		// differentiator (names, any case); empty turns that check off.
		ParityBaselines []string `json:"parity_baselines"`
		// Regexps (case-insensitive) added to the generic differentiator wordings.
		ParityDifferentiators []string `json:"parity_differentiators"`
	} `json:"bullet_rules"`
	// TexLabels maps a LaTeX source stem to the output label `bank import-tex`
	// gives its selection (default: the stem without a "Resume_" prefix).
	TexLabels map[string]string `json:"tex_labels"`
	// SiblingRoots names the top-level folders under code_root that hold
	// project repos, which a sandbox may leave unmounted (a source there is
	// then skipped, not failed). Empty: every missing source is an error.
	SiblingRoots []string `json:"sibling_roots"`
}

var cfg Config
var codeRootFlag string

// skillDir is the folder holding SKILL.md (the binary lives in <skill>/bin).
func skillDir() string {
	if d := os.Getenv("RKB_SKILL_DIR"); d != "" {
		return d
	}
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		for d := filepath.Dir(exe); d != filepath.Dir(d); d = filepath.Dir(d) {
			if fileExists(filepath.Join(d, "SKILL.md")) {
				return d
			}
		}
	}
	return ""
}

// loadConfig prefers <skill>/rkb/assets/config.json (editable without a
// rebuild) and falls back to the copy embedded in the binary.
func loadConfig() {
	src, err := loadConfigFrom(userConfigPath())
	if err != nil {
		die("%v", err)
	}
	configSource = src
}

// configSource names the file layered over the embedded default, or
// "embedded default" when there is none (rkb doctor prints it).
var configSource string

// userConfigPath is the one user config file rkb reads, the first that exists:
// <skill>/rkb/assets/config.json (RKB_SKILL_DIR, or the nearest SKILL.md above
// the binary), then ~/.config/rkb/config.json. The second works when rkb runs
// under a caller that passes only PATH, HOME and TMPDIR (Vani does). "" means
// none exists.
func userConfigPath() string {
	if sd := skillDir(); sd != "" {
		if p := filepath.Join(sd, "rkb", "assets", "config.json"); pathExists(p) {
			return p
		}
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if p := filepath.Join(h, ".config", "rkb", "config.json"); pathExists(p) {
			return p
		}
	}
	return ""
}

func pathExists(p string) bool { _, err := os.Lstat(p); return err == nil }

// loadConfigFrom resets cfg to the embedded default and layers the file at
// path (if any) over it: keys the file sets replace the default's, lists
// whole, and keys it leaves out keep the default. A file that exists but
// cannot be read or parsed is an error, never a silent fall back to the
// default.
func loadConfigFrom(path string) (string, error) {
	b, err := assets.ReadFile("assets/config.json")
	if err != nil {
		return "", fmt.Errorf("embedded config.json: %v", err)
	}
	cfg = Config{}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("embedded config.json: %v", err)
	}
	if path == "" {
		if err := cfg.Validate(); err != nil {
			return "", fmt.Errorf("embedded config.json: %v", err)
		}
		return "embedded default", nil
	}
	b, err = os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("config %s: %v", path, err)
	}
	// A key the file spells wrongly is refused, not ignored: the setting it
	// meant would silently stay at the default.
	if err := checkKnownKeys(b, reflect.TypeOf(cfg)); err != nil {
		return "", fmt.Errorf("config %s: %v", path, err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("config %s: %v", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("config %s: %v", path, err)
	}
	return path + " (over the embedded default)", nil
}

// termsRe is `(?i)\b(?:t1|t2|…)\b` over regexp terms, the one constructor for
// the testing_terms and number_words checks, so Validate and lint compile the
// same pattern.
func termsRe(terms []string) (*regexp.Regexp, error) {
	return regexp.Compile(`(?i)\b(?:` + strings.Join(terms, "|") + `)\b`)
}

// configTermsRe is termsRe for a list Validate has already accepted; a list
// that was set without validating stops rkb with the compile error instead of
// panicking.
func configTermsRe(key string, terms []string) *regexp.Regexp {
	re, err := termsRe(terms)
	if err != nil {
		die("config.json: bullet_rules.%s does not compile: %v", key, err)
	}
	return re
}

// Validate refuses a config value that would fail or panic later: a pattern
// that does not compile, or a density range that is not [number, number,
// unit-string]. Every regexp the config feeds is compiled here, once, so no
// check can meet a bad one at run time. Unknown keys are refused earlier, at
// decode.
func (c *Config) Validate() error {
	r := &c.Rules
	for _, l := range []struct {
		key   string
		terms []string
	}{{"testing_terms", r.TestingTerms}, {"number_words", r.NumberWords}} {
		if _, err := termsRe(l.terms); err != nil {
			return fmt.Errorf("bullet_rules.%s does not compile: %v", l.key, err)
		}
		for i, t := range l.terms {
			if _, err := regexp.Compile(t); err != nil {
				return fmt.Errorf("bullet_rules.%s[%d] %q does not compile: %v", l.key, i, t, err)
			}
		}
	}
	for _, l := range []struct {
		key  string
		pats []string
	}{{"extra_banned", r.ExtraBanned}, {"dup_phrases", r.DupPhrases}, {"parity_differentiators", r.ParityDifferentiators}} {
		for i, p := range l.pats {
			if _, err := regexp.Compile(`(?i)\b(?:` + p + `)\b`); err != nil {
				return fmt.Errorf("bullet_rules.%s[%d] %q does not compile: %v", l.key, i, p, err)
			}
		}
	}
	if _, err := compileMechanisms(r.Mechanisms); err != nil {
		return err
	}
	for _, k := range sortedKeys(c.Layout.DensityRanges) {
		rg := c.Layout.DensityRanges[k]
		_, loOK := rg[0].(float64)
		_, hiOK := rg[1].(float64)
		unit, hasUnit := rg[2], rg[2] != nil
		if _, isStr := unit.(string); !loOK || !hiOK || (hasUnit && !isStr) {
			return fmt.Errorf("layout.density_range.%s must be [number, number, \"unit\"], got %v", k, rg)
		}
	}
	return nil
}

func templateHTML() string {
	if sd := skillDir(); sd != "" {
		if b, err := os.ReadFile(filepath.Join(sd, "rkb", "assets", "resume_template.html")); err == nil {
			return string(b)
		}
	}
	b, _ := assets.ReadFile("assets/resume_template.html")
	return string(b)
}

// codeRoot finds the folder that contains cfg.CVDir (normally ~/Code).
func codeRoot() string {
	var cands []string
	if codeRootFlag != "" {
		cands = append(cands, expand(codeRootFlag))
	}
	for _, e := range []string{"RKB_CODE_ROOT", "RESUME_CODE_ROOT"} {
		if v := os.Getenv(e); v != "" {
			cands = append(cands, expand(v))
		}
	}
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			cands = append(cands, d)
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	home, _ := os.UserHomeDir()
	cands = append(cands, filepath.Join(home, "Code"), filepath.Join(home, "mnt", "Code"), "/mnt/user-data/uploads/Code")
	for _, c := range cands {
		if dirExists(filepath.Join(c, cfg.CVDir)) {
			return c
		}
	}
	die("could not find a folder containing %s; pass --code-root or set RKB_CODE_ROOT", cfg.CVDir)
	return ""
}

func inRoot(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(codeRoot(), rel)
}

// ---------------------------------------------------------------- io

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
func dirExists(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }

// isUnmountedSibling returns true when running against the career tree in a
// sandbox where one of the sibling top-level folders listed in
// sibling_roots (the folders holding project repos) is not mounted.
func isUnmountedSibling(root, rel string) bool {
	if root == "" || !fileExists(filepath.Join(root, cfg.Bank)) {
		return false
	}
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
	top := strings.SplitN(rel, "/", 2)[0]
	for _, s := range cfg.SiblingRoots {
		if s != "" && top == s {
			return !dirExists(filepath.Join(root, top)) && !fileExists(filepath.Join(root, top))
		}
	}
	return false
}

func readJSON(p string, v interface{}) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return decodeJSONStrict(p, b, v)
}

// decodeJSONStrict refuses duplicate object keys (decoders silently keep the
// last one) before decoding.
func decodeJSONStrict(p string, b []byte, v interface{}) error {
	if d, err := dupKeys(b); err != nil {
		return fmt.Errorf("%s: invalid JSON: %w", p, err)
	} else if len(d) > 0 {
		return fmt.Errorf("%s: duplicate key(s) %s — one copy would silently win; merge or rename them", p, humanList(d, 5))
	}
	if err := checkKeyCase(b, reflect.TypeOf(v)); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	return nil
}

func writeJSON(p string, v interface{}) {
	b, err := encodeJSON(v)
	if err != nil {
		die("encode %s: %v", p, err)
	}
	if err := writeAtomic(p, b); err != nil {
		die("write %s: %v", p, err)
	}
}

func writeFile(p string, s string) {
	if err := writeAtomic(p, []byte(s)); err != nil {
		die("write %s: %v", p, err)
	}
}

func sha1hex(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:12]
}

// sha256File hashes a regular file of at most maxQuoteFile bytes, streaming
// it. "" means unreadable, not a regular file (a directory, a device, a FIFO)
// or over the cap: callers treat that as a source that cannot be vouched for.
func sha256File(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }() // read-only handle
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxQuoteFile {
		return ""
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxQuoteFile+1)) // the file may grow after Stat
	if err != nil || n > maxQuoteFile {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func nowISO() string { return time.Now().Format(time.RFC3339) }

func ageHours(iso string) float64 {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 1e9
	}
	return time.Since(t).Hours()
}

func die(f string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "rkb: "+f+"\n", a...)
	exitNow(2)
}

var (
	exitMu       sync.Mutex
	exitCleanups []func()
)

// exitNow ends the process after running the cleanups scratchDir registered:
// os.Exit skips deferred calls, so a folder made with scratchDir would
// otherwise survive every die() and stop.
func exitNow(code int) {
	exitMu.Lock()
	fns := exitCleanups
	exitCleanups = nil
	exitMu.Unlock()
	for i := len(fns) - 1; i >= 0; i-- {
		fns[i]()
	}
	os.Exit(code)
}

// scratchDir makes a private temp folder and returns it with its cleanup. The
// cleanup is idempotent and also runs from exitNow, so the folder is removed
// on a normal return (defer cleanup()), a panic, a die and a stop alike. Use
// it for any folder that holds copies of the person's documents.
func scratchDir(prefix string) (string, func(), error) {
	watchSignals() // a signal must remove the folder too
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", func() {}, err
	}
	var once sync.Once
	cleanup := func() { once.Do(func() { _ = os.RemoveAll(dir) }) } // best-effort: the folder holds only copies
	exitMu.Lock()
	exitCleanups = append(exitCleanups, cleanup)
	exitMu.Unlock()
	return dir, cleanup, nil
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

var wsRe = regexp.MustCompile(`\s+`)

func squash(s string) string { return strings.TrimSpace(wsRe.ReplaceAllString(s, " ")) }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------- flags

// args is a tiny flag parser: --name value, --name=value, --bool; the rest
// are positionals. Keeps the binary free of anything but the standard library.
// Every command declares the flags it reads (main.go: flagTable) and anything
// else is refused: a misspelt --final or --vm-report must not silently turn a
// release gate into a draft run.
type args struct {
	pos   []string
	opts  map[string]string
	bools map[string]bool
	seen  []string // every flag name given, in order
}

func parseArgs(in []string, boolFlags ...string) *args {
	a, err := parseArgsErr(in, boolFlags...)
	if err != nil {
		die("%v", err)
	}
	if v, ok := a.opts["code-root"]; ok {
		codeRootFlag = v
	}
	return a
}

func parseArgsErr(in []string, boolFlags ...string) (*args, error) {
	a := &args{opts: map[string]string{}, bools: map[string]bool{}}
	given := map[string]bool{}
	onlyPos := false
	for i := 0; i < len(in); i++ {
		s := in[i]
		if onlyPos || s == "-" || !strings.HasPrefix(s, "-") {
			a.pos = append(a.pos, s)
			continue
		}
		if s == "--" {
			onlyPos = true
			continue
		}
		if !strings.HasPrefix(s, "--") {
			if len(s) > 1 && (s[1] < '0' || s[1] > '9') {
				return nil, fmt.Errorf("%s: flags take two dashes (--%s)", s, strings.TrimLeft(s, "-"))
			}
			a.pos = append(a.pos, s) // a negative number
			continue
		}
		name, val, hasVal := s[2:], "", false
		if k := strings.Index(name, "="); k >= 0 {
			name, val, hasVal = name[:k], name[k+1:], true
		}
		if name == "" {
			return nil, fmt.Errorf("%q is not a flag", s)
		}
		if given[name] {
			return nil, fmt.Errorf("--%s given twice", name)
		}
		given[name] = true
		a.seen = append(a.seen, name)
		if contains(boolFlags, name) {
			if hasVal {
				switch strings.ToLower(val) {
				case "1", "t", "true", "yes", "on":
					a.bools[name] = true
				case "0", "f", "false", "no", "off":
				default:
					return nil, fmt.Errorf("--%s=%s: expected true or false", name, val)
				}
			} else {
				a.bools[name] = true
			}
			continue
		}
		if hasVal {
			a.opts[name] = val
			continue
		}
		if i+1 < len(in) && !strings.HasPrefix(in[i+1], "--") {
			a.opts[name] = in[i+1]
			i++
			continue
		}
		return nil, fmt.Errorf("--%s needs a value", name)
	}
	return a, nil
}

// check refuses flags the command does not read, suggesting the closest one.
func (a *args) check(cmd string, allowed []string) error {
	allowed = append(allowed, "code-root")
	for _, f := range a.seen {
		if contains(allowed, f) {
			continue
		}
		best, bd := "", 1<<30
		for _, g := range allowed {
			if d := editDistance(f, g); d < bd {
				best, bd = g, d
			}
		}
		hint := ""
		if bd <= 3 || strings.HasPrefix(best, f) || strings.HasPrefix(f, best) {
			hint = fmt.Sprintf(" — did you mean --%s?", best)
		}
		sorted := append([]string(nil), allowed...)
		sort.Strings(sorted)
		return fmt.Errorf("%s: unknown flag --%s%s (accepted: --%s)", cmd, f, hint, strings.Join(sorted, " --"))
	}
	return nil
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func (a *args) get(name, def string) string {
	if v, ok := a.opts[name]; ok {
		return v
	}
	return def
}

// intFlag reads an integer flag in [lo, hi]; a malformed value is fatal
// (strconv.Atoi's zero would otherwise mean "0 pages").
func (a *args) intFlag(name string, def, lo, hi int) int {
	v, ok := a.opts[name]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < lo || n > hi {
		die("--%s %q: expected a whole number from %d to %d", name, v, lo, hi)
	}
	return n
}

func (a *args) floatFlag(name string, def, lo, hi float64) float64 {
	v, ok := a.opts[name]
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "pt"), 64)
	if err != nil || f < lo || f > hi || f != f {
		die("--%s %q: expected a number from %g to %g", name, v, lo, hi)
	}
	return f
}

// npos enforces the number of positionals (max < 0: unlimited).
func (a *args) npos(lo, hi int, usage string) {
	if len(a.pos) < lo || (hi >= 0 && len(a.pos) > hi) {
		die("usage: %s (got %d argument(s): %s)", usage, len(a.pos), strings.Join(a.pos, " "))
	}
}

func (a *args) need(i int, what string) string {
	if i >= len(a.pos) {
		die("missing %s", what)
	}
	return a.pos[i]
}
