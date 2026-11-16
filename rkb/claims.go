package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Claims.tex stays the only place a figure's value lives. Claims.verify.json
// records verdicts, never values of its own; a logged pass whose value no
// longer matches Claims.tex is void.
//
//	figure pass 1  validate_metrics.py verdict (PRIMARY/LEDGER), or a logged
//	               manual read with the value found and an existing source file
//	figure pass 2  blind re-derivation by a separate agent, recorded PER
//	               SENTENCE: a figure reused in new wording is checked again
//	statement p1   the bullet names its sources (files that exist)
//	statement p2   blind check that every non-numeric assertion is supported
//
// A figure is usable on a final resume only when pass 1 holds and every
// sentence on the page that uses it has a CONFIRMED pass 2 whose source file
// is unchanged (sha256; a deleted source is stale too).

type Claim struct {
	ID     string `json:"id"`
	TeX    string `json:"value_tex"`
	Value  string `json:"value"`
	Source string `json:"source"`
	Check  string `json:"check"`
	Notes  string `json:"notes"`
	// Caution: the notes say not to use the figure as written (CAUTION, DO
	// NOT USE, WITHDRAWN). Draft resumes warn; the final gate refuses it.
	Caution string `json:"caution,omitempty"`
	File    string `json:"file"`
}

var (
	claimComment = regexp.MustCompile(`^%\s*(.*?)\s*\[([A-Za-z0-9_.\-]+)\]\s*$`)
	claimDef     = regexp.MustCompile(`^\\newcommand\{\\(clm[A-Za-z]+)\}\{(.*)\}\s*$`)
)

func parseClaims() map[string]*Claim {
	out, errs, _ := parseClaimsFiles(cfg.ClaimsTex, inRoot)
	if len(errs) > 0 {
		die("Claims.tex problems (fix them before anything is verified or rendered):\n  %s", strings.Join(errs, "\n  "))
	}
	if len(out) == 0 {
		die("no claims found in %v", cfg.ClaimsTex)
	}
	return out
}

// cautionRe flags a claim whose notes say it must not be used as written.
var cautionRe = regexp.MustCompile(`\b(CAUTION|DO NOT USE|WITHDRAWN|RETRACTED)\b`)

// parseClaimsFiles reads every configured Claims file. A configured file that
// is missing, a macro defined twice (LaTeX would refuse, or the second file
// would silently win here) and a \newcommand line that does not parse are
// errors; a claim with no "% source [check]" comment is a warning (it can
// still pass figure pass 1 by a logged manual read).
func parseClaimsFiles(files []string, resolve func(string) string) (map[string]*Claim, []string, []string) {
	out := map[string]*Claim{}
	var errs, warns []string
	looseDef := regexp.MustCompile(`^\s*\\(re)?newcommand\s*\{?\\(clm[A-Za-z0-9_]*)`)
	for _, rel := range files {
		p := resolve(rel)
		b, err := os.ReadFile(p)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		var pendSrc, pendChk string
		var notes []string
		for n, line := range strings.Split(string(b), "\n") {
			line = strings.TrimRight(line, " \t\r")
			if m := claimComment.FindStringSubmatch(line); m != nil {
				pendSrc, pendChk = m[1], m[2]
				continue
			}
			if strings.HasPrefix(line, "%") {
				t := strings.TrimSpace(strings.TrimLeft(line, "%"))
				if strings.HasPrefix(t, "----") {
					notes = nil
				} else if t != "" {
					notes = append(notes, t)
				}
				continue
			}
			m := claimDef.FindStringSubmatch(line)
			if m == nil {
				if lm := looseDef.FindStringSubmatch(line); lm != nil {
					errs = append(errs, fmt.Sprintf("%s:%d: cannot parse the definition of \\%s (expected \\newcommand{\\clmName}{value} on one line, letters only in the name)", rel, n+1, lm[2]))
				}
				continue
			}
			nt := strings.Join(notes, " ")
			caution := ""
			if loc := cautionRe.FindStringIndex(nt); loc != nil {
				caution = truncate(strings.TrimSpace(nt[loc[0]:]), 200)
			}
			if len(nt) > 600 {
				nt = nt[len(nt)-600:]
			}
			if prev, dup := out[m[1]]; dup {
				errs = append(errs, fmt.Sprintf("%s:%d: \\%s is already defined in %s; one value per figure", rel, n+1, m[1], prev.File))
			} else {
				if pendChk == "" {
					warns = append(warns, fmt.Sprintf("%s:%d: \\%s has no \"%% source [check.id]\" comment (pass 1 needs a manual read)", rel, n+1, m[1]))
				}
				if strings.TrimSpace(detex(m[2])) == "" {
					errs = append(errs, fmt.Sprintf("%s:%d: \\%s has an empty value", rel, n+1, m[1]))
				}
				out[m[1]] = &Claim{ID: m[1], TeX: m[2], Value: detex(m[2]), Source: pendSrc, Check: pendChk, Notes: nt, Caution: caution, File: rel}
			}
			pendSrc, pendChk, notes = "", "", nil
		}
	}
	return out, errs, warns
}

// ---------------------------------------------------------------- log

type LogEntry struct {
	Pass         int    `json:"pass"`
	Kind         string `json:"kind,omitempty"` // figure | statement
	Verdict      string `json:"verdict"`
	Method       string `json:"method"`
	By           string `json:"by"`
	At           string `json:"at"`
	Value        string `json:"value,omitempty"`
	Found        string `json:"found,omitempty"`
	Evidence     string `json:"evidence,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	Line         any    `json:"line,omitempty"`
	SourceSHA    string `json:"source_sha256,omitempty"`
	SentenceHash string `json:"sentence_hash,omitempty"`
	Quote        string `json:"quote,omitempty"`
	TextHash     string `json:"text_hash,omitempty"`
	Sources      []struct {
		Path string `json:"path"`
		SHA  string `json:"sha256"`
	} `json:"sources,omitempty"`
}

type VerifyLog map[string][]LogEntry

func logPath() string { return inRoot(cfg.VerifyLog) }

// loadLog reads the verification log. A missing log is empty; a log that
// exists but does not parse is fatal — treating it as empty would let the next
// write erase the whole verification history.
func loadLog() VerifyLog {
	l := VerifyLog{}
	b, err := os.ReadFile(logPath())
	if errors.Is(err, os.ErrNotExist) {
		return l
	}
	if err != nil {
		die("verify log: %v", err)
	}
	if err := decodeJSONStrict(logPath(), b, &l); err != nil {
		die("verify log is unreadable, refusing to continue (restore it from a backup or git; never delete it): %v", err)
	}
	return l
}

// appendLog adds entries to the verification log safely: the log is
// append-only, so a concurrent writer is merged, never overwritten.
func appendLog(add VerifyLog) {
	if len(add) == 0 {
		return
	}
	err := guardedUpdate(logPath(), 6, func(old []byte, exists bool) ([]byte, error) {
		nb, _, err := mergeLog(old, exists, add)
		return nb, err
	})
	if err != nil {
		die("verify log: %v", err)
	}
}

// mergeLog appends entries to the raw log. Existing entries are carried as raw
// JSON, so fields written by other tools survive byte for byte; an entry that
// repeats an existing verdict (the same sheet ingested twice) is skipped.
// Returns nil bytes when nothing is new.
func mergeLog(old []byte, exists bool, add VerifyLog) ([]byte, int, error) {
	cur := map[string][]json.RawMessage{}
	if exists && len(bytes.TrimSpace(old)) > 0 {
		if err := decodeJSONStrict(logPath(), old, &cur); err != nil {
			return nil, 0, fmt.Errorf("verify log is unreadable; refusing to overwrite it: %v", err)
		}
		if cur == nil {
			return nil, 0, fmt.Errorf("verify log holds null, not an object; refusing to overwrite it")
		}
	}
	added := 0
	for _, k := range sortedKeys(add) {
		have := map[string]bool{}
		for _, r := range cur[k] {
			var e LogEntry
			if json.Unmarshal(r, &e) == nil {
				have[e.dedupeKey()] = true
			}
		}
		for _, e := range add[k] {
			if have[e.dedupeKey()] {
				continue
			}
			have[e.dedupeKey()] = true
			b, err := json.Marshal(e)
			if err != nil {
				return nil, 0, err
			}
			cur[k] = append(cur[k], b)
			added++
		}
	}
	if added == 0 {
		return nil, 0, nil
	}
	nb, err := encodeJSON(cur)
	if err != nil {
		return nil, 0, err
	}
	if exists {
		if d := droppedFields(old, nb); len(d) > 0 {
			return nil, 0, fmt.Errorf("refusing to write the verify log: it would drop %s", humanList(d, 5))
		}
	}
	return nb, added, nil
}

// dedupeKey identifies a verdict regardless of when it was recorded.
func (e LogEntry) dedupeKey() string {
	srcs := ""
	for _, s := range e.Sources {
		srcs += s.Path + "@" + s.SHA + ";"
	}
	return strings.Join([]string{fmt.Sprint(e.Pass), e.Kind, e.Verdict, e.Method, e.By, e.Value, e.Found,
		e.SourcePath, e.SourceSHA, e.SentenceHash, e.TextHash, e.Quote, srcs}, "\x00")
}

// ---------------------------------------------------------------- validator report

var vmLine = regexp.MustCompile(`^\[(PRIMARY|LEDGER|SKIP|FAIL|UNBACKED)\s*\]\s+(\S+)\s+(.*)$`)
var verdictRank = map[string]int{"PRIMARY": 0, "LEDGER": 1, "SKIP": 2, "UNBACKED": 3, "FAIL": 4}

type vmResult struct{ Verdict, Detail string }

// parseVM keeps the WORST verdict per check id, so a FAIL can never be hidden
// by a later PASS line for the same check.
func parseVM(p string) (map[string]vmResult, time.Time) {
	out := map[string]vmResult{}
	if p == "" {
		return out, time.Time{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		die("validator report: %v", err)
	}
	st, _ := os.Stat(p)
	when := st.ModTime()
	// rkb stamps every report it writes; the oldest stamp is the report's age
	// (a merged partial run only refreshes the checks it re-ran). The stamp
	// survives copies, unlike the file time.
	stamped := time.Time{}
	for _, m := range vmStamp.FindAllStringSubmatch(string(b), -1) {
		if t, err := time.Parse(time.RFC3339, m[1]); err == nil && (stamped.IsZero() || t.Before(stamped)) {
			stamped = t
		}
	}
	if !stamped.IsZero() {
		if strings.HasPrefix(string(b), "# rkb-vm-report ") {
			when = stamped
		} else if stamped.Before(when) { // an unstamped (older-format) base: trust neither alone
			when = stamped
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		m := vmLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		old, ok := out[m[2]]
		if !ok || verdictRank[m[1]] > verdictRank[old.Verdict] {
			out[m[2]] = vmResult{m[1], truncate(m[3], 300)}
		}
	}
	return out, when
}

var vmStamp = regexp.MustCompile(`(?m)^# rkb-vm-report generated=(\S+)`)

// ---------------------------------------------------------------- sentences

// maskedSentence is what the blind verifier sees: the sentence with this
// figure hidden and every other figure marked.
func maskedSentence(text, id string) string {
	s := strings.ReplaceAll(text, "{{"+id+"}}", "[MASKED]")
	s = tokenRe.ReplaceAllString(s, "<other figure>")
	return squash(plainText(s))
}

func sentenceHash(text, id string) string { return sha1hex(maskedSentence(text, id)) }

func statementText(text string) string {
	return squash(plainText(tokenRe.ReplaceAllString(text, "[figure]")))
}

func textHash(text string) string { return sha1hex(squash(text)) }

// usage: claim id -> sentences on this resume that use it
func claimUsage(c *Composed) map[string][]string {
	u := map[string][]string{}
	add := func(t string) {
		for _, m := range tokenRe.FindAllStringSubmatch(t, -1) {
			if !contains(u[m[1]], t) {
				u[m[1]] = append(u[m[1]], t)
			}
		}
	}
	for _, r := range c.allBullets() {
		add(r.B.Text)
	}
	for _, l := range c.leads() {
		add(l.Text)
	}
	return u
}

// ---------------------------------------------------------------- evaluate

type evalResult struct {
	P1, P2OK  bool
	P1Verdict string
	Confirmed []string
	Problems  []string
	Refuted   map[string]string // sentence hash -> verdict: evidence
}

// refutingFigure: blind-pass verdicts that say the sentence's wording or value
// is wrong (as opposed to a pass that is missing, stale or badly filled in).
var refutingFigure = map[string]bool{"SCOPE": true, "MISMATCH": true}

func evaluate(cl *Claim, log VerifyLog, vm map[string]vmResult, sentences []string) evalResult {
	var r evalResult
	entries := log[cl.ID]
	var current []LogEntry
	voided := 0
	for _, e := range entries {
		if e.Value == cl.Value {
			current = append(current, e)
		} else {
			voided++
		}
	}
	vmr, hasVM := vm[cl.Check]
	if hasVM && (vmr.Verdict == "PRIMARY" || vmr.Verdict == "LEDGER") {
		r.P1, r.P1Verdict = true, vmr.Verdict
	}
	if !r.P1 {
		for _, e := range current {
			if e.Pass == 1 && e.Verdict == "CONFIRMED" && e.Found != "" && e.SourcePath != "" && (fileExists(inRoot(e.SourcePath)) || isUnmountedSibling(codeRoot(), e.SourcePath)) && !circularSource(e.SourcePath) {
				r.P1, r.P1Verdict = true, "MANUAL"
			}
		}
	}
	if hasVM && (vmr.Verdict == "FAIL" || vmr.Verdict == "UNBACKED") {
		r.P1 = false
		r.Problems = append(r.Problems, fmt.Sprintf("validator %s: %s", vmr.Verdict, truncate(vmr.Detail, 120)))
	}
	if !r.P1 {
		msg := "pass 1 missing"
		if hasVM {
			msg += " (validator " + vmr.Verdict + ")"
		}
		r.Problems = append(r.Problems, msg)
	}
	r.P2OK = len(sentences) > 0
	for _, s := range sentences {
		h := sentenceHash(s, cl.ID)
		var latest *LogEntry
		for i := range current {
			e := &current[i]
			if e.Pass == 2 && e.SentenceHash == h {
				latest = e
			}
		}
		switch {
		case latest == nil:
			r.P2OK = false
			r.Problems = append(r.Problems, "pass 2 missing for: "+truncate(maskedSentence(s, cl.ID), 70))
		case latest.Verdict != "CONFIRMED":
			r.P2OK = false
			r.Problems = append(r.Problems, fmt.Sprintf("latest blind pass %s: %s", latest.Verdict, truncate(latest.Evidence, 110)))
			if refutingFigure[latest.Verdict] {
				if r.Refuted == nil {
					r.Refuted = map[string]string{}
				}
				r.Refuted[h] = latest.Verdict + ": " + truncate(latest.Evidence, 200)
			}
		case circularSource(latest.SourcePath):
			r.P2OK = false
			r.Problems = append(r.Problems, "pass 2 cited a claims/resume/KB file, not an artifact: "+latest.SourcePath)
		default:
			sha := sha256File(inRoot(latest.SourcePath))
			if sha == "" && isUnmountedSibling(codeRoot(), latest.SourcePath) && latest.SourceSHA != "" {
				sha = latest.SourceSHA
			}
			if sha == "" || (latest.SourceSHA != "" && sha != latest.SourceSHA) {
				r.P2OK = false
				r.Problems = append(r.Problems, "pass 2 source changed or deleted since it was read: "+latest.SourcePath)
			} else {
				r.Confirmed = append(r.Confirmed, h)
			}
		}
	}
	if voided > 0 {
		r.Problems = append(r.Problems, fmt.Sprintf("%d logged pass(es) void: value changed since", voided))
	}
	return r
}

func evalStatement(key, text string, sources []string, attested string, log VerifyLog) *StatementView {
	sv := &StatementView{TextHash: textHash(text)}
	if attested != "" {
		sv.SourcesOK, sv.Confirmed, sv.Attested = true, true, attested
		return sv
	}
	sv.SourcesOK = len(sources) > 0
	for _, s := range sources {
		if isURL(s) || isUnmountedSibling(codeRoot(), s) { // a published paper or unmounted sibling repo
			continue
		}
		if !fileExists(inRoot(s)) && !dirExists(inRoot(s)) {
			sv.SourcesOK = false
			sv.Problem = "source not found: " + s
		} else if circularSource(s) {
			sv.SourcesOK = false
			sv.Problem = "source restates claims (CV folder, knowledge.json, dossiers): " + s
		}
	}
	if len(sources) == 0 {
		sv.Problem = "no sources listed (statement pass 1)"
	}
	var latest *LogEntry
	for i, e := range log["stmt:"+key] {
		if e.TextHash == sv.TextHash {
			latest = &log["stmt:"+key][i]
		}
	}
	switch {
	case latest == nil:
		if sv.Problem == "" {
			sv.Problem = "statement pass 2 missing"
		}
	case latest.Verdict != "CONFIRMED":
		sv.Problem = "statement pass 2 " + latest.Verdict + ": " + truncate(latest.Evidence, 120)
		if latest.Verdict == "UNSUPPORTED" {
			sv.Refuted = latest.Verdict + ": " + truncate(latest.Evidence, 200)
		}
	default:
		fresh := true
		for _, s := range latest.Sources {
			if isURL(s.Path) {
				continue
			}
			sha := sha256File(inRoot(s.Path))
			if sha == "" && isUnmountedSibling(codeRoot(), s.Path) && s.SHA != "" {
				sha = s.SHA
			}
			if sha == "" || sha != s.SHA {
				fresh = false
				sv.Problem = "statement source changed since it was checked: " + s.Path
			}
		}
		sv.Confirmed = fresh
	}
	return sv
}

// ---------------------------------------------------------------- commands

func cmdClaims(sub string, a *args) {
	switch sub {
	case "export":
		claimsExport(a)
	case "status":
		claimsStatus(a)
	case "worksheet":
		claimsWorksheet(a)
	case "ingest":
		claimsIngest(a)
	case "add":
		claimsAdd(a)
	case "vm":
		claimsVM(a)
	default:
		die("claims subcommands: export | status | worksheet | ingest | add | vm")
	}
}

func claimsExport(a *args) {
	claims := parseClaims()
	log := loadLog()
	vm, vmTime := parseVM(a.get("vm-report", ""))
	cf := ClaimsFile{Generated: nowISO(), Claims: map[string]*ClaimView{}, Statements: map[string]*StatementView{}, Guards: 1}
	usage := map[string][]string{}
	if u := a.get("used-in", ""); u != "" {
		c := loadResume(u)
		cf.ResumeHash, cf.Selection = c.resumeHash(), u
		usage = claimUsage(c)
		for _, r := range c.allBullets() {
			cf.Statements[r.B.ID] = evalStatement(r.B.ID, r.B.Text, r.B.Sources, r.B.Attested, log)
		}
		for _, l := range c.leads() {
			cf.Statements[l.Key] = evalStatement(l.Key, l.Text, nil, "", log)
		}
	}
	if !vmTime.IsZero() {
		cf.VMReport = map[string]string{"path": a.get("vm-report", ""), "mtime": vmTime.Format(time.RFC3339)}
	}
	ok := 0
	for id, cl := range claims {
		r := evaluate(cl, log, vm, usage[id])
		dv := r.P1 && r.P2OK
		if dv {
			ok++
		}
		cf.Claims[id] = &ClaimView{Value: cl.Value, Kind: figureKind(cl.Value), Check: cl.Check, Source: cl.Source,
			DoubleVerified: dv, ConfirmedSentences: r.Confirmed, Problems: r.Problems, Caution: cl.Caution,
			RefutedSentences: r.Refuted}
	}
	out := a.get("out", "claims.json")
	writeJSON(out, cf)
	fmt.Printf("wrote %s: %d claims, %d double-verified for the sentences they are used in\n", out, len(claims), ok)
}

func claimsStatus(a *args) {
	claims := parseClaims()
	log := loadLog()
	vm, _ := parseVM(a.get("vm-report", ""))
	u := a.need(0, "resume (selection or composed)")
	c := loadResume(u)
	usage := claimUsage(c)
	bad := 0
	for _, id := range sortedKeys(usage) {
		cl, ok := claims[id]
		if !ok {
			fmt.Printf("[UNREGISTERED] %s: used but not defined in %v\n", id, cfg.ClaimsTex)
			bad++
			continue
		}
		r := evaluate(cl, log, vm, usage[id])
		tag := "OK  "
		if !r.P1 || !r.P2OK {
			tag = "TODO"
			bad++
		}
		line := fmt.Sprintf("[%s] %-26s p1=%-8s p2=%d/%d  %s", tag, id, orDash(r.P1Verdict), len(r.Confirmed), len(usage[id]), truncate(cl.Value, 40))
		if tag == "TODO" {
			line += "  <- " + strings.Join(r.Problems, "; ")
		}
		if tag == "TODO" || !a.bools["quiet"] {
			fmt.Println(line)
		}
	}
	sb := 0
	for _, r := range c.allBullets() {
		sv := evalStatement(r.B.ID, r.B.Text, r.B.Sources, r.B.Attested, log)
		if !sv.SourcesOK || !sv.Confirmed {
			sb++
			if !a.bools["quiet"] || true {
				fmt.Printf("[STMT] %-26s %s\n", r.B.ID, sv.Problem)
			}
		}
	}
	fmt.Printf("\n%d/%d figures double-verified; %d bullet statement checks outstanding\n", len(usage)-bad, len(usage), sb)
	if bad > 0 || sb > 0 {
		os.Exit(1)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type WorkItem struct {
	Kind         string            `json:"kind"`
	Claim        string            `json:"claim,omitempty"`
	Key          string            `json:"key,omitempty"`
	Sentence     string            `json:"sentence"`
	SentenceHash string            `json:"sentence_hash,omitempty"`
	TextHash     string            `json:"text_hash,omitempty"`
	SourceHint   string            `json:"source_hint,omitempty"`
	CheckID      string            `json:"check_id,omitempty"`
	ScopeNotes   string            `json:"scope_notes,omitempty"`
	ValueShape   string            `json:"value_shape,omitempty"`
	Sources      []string          `json:"sources_hint,omitempty"`
	Answer       map[string]any    `json:"answer"`
	Extra        map[string]string `json:"-"`
}

type Worksheet struct {
	Instructions string              `json:"instructions"`
	CodeRoot     string              `json:"code_root"`
	Repos        map[string][]string `json:"repos"`
	Verifier     string              `json:"verifier"`
	Items        []*WorkItem         `json:"items"`
}

const worksheetInstructions = `You are a blind verifier. You have NOT seen the resume's figures. Work only from project artifacts under code_root (never from Claims.tex, resumes, CVs, knowledge.json or the portfolio site).
Set "verifier" to your name (e.g. "blind-verifier-1").
kind=figure: find the [MASKED] figure yourself using source_hint/check_id ('#' in hints hides digits). Fill answer.found_value with ONLY the figure, in value_shape ('#' = digit; convert units or compute a ratio if the source states it differently and show that in scope_note), keeping both ends of a range or before/after pair. answer.quote = verbatim source line, answer.source_path = path relative to code_root, answer.line. answer.scope_ok=false with scope_note if the sentence claims more than the source supports (faster vs parity, pilot vs general, other hardware/baseline/subset, or a figure measured on one phase or part of something the sentence names as a whole: e.g. one stage of a pipeline timed and sold as the whole pipeline).
kind=statement: list every non-numeric factual assertion in the sentence ([figure] marks checked figures; ignore them). answer.supported=true only if EVERY assertion is supported by the code or docs; give answer.quotes=[{path, line, quote}] (paths relative to code_root, or {url, quote} for an https source listed in sources_hint, e.g. a published paper) covering them, and answer.unsupported=[...] for any that are not.
Employer or client work under NDA has no artifact here: mark it supported=false with "no artifact (employer work)" and never use side projects as evidence for it. A value rounded or truncated at its stated precision is the same value. Keep helper files out of the user's folders.
If you cannot find something, leave it empty and say where you looked. Never infer from the sentence.`

func claimsWorksheet(a *args) {
	claims := parseClaims()
	log := loadLog()
	vm, _ := parseVM(a.get("vm-report", ""))
	c := loadResume(a.need(0, "resume"))
	all := a.bools["all"]
	ws := Worksheet{Instructions: worksheetInstructions, CodeRoot: codeRoot(), Repos: cfg.Repos}
	usage := claimUsage(c)
	for _, id := range sortedKeys(usage) {
		cl, ok := claims[id]
		if !ok {
			continue
		}
		r := evaluate(cl, log, vm, usage[id])
		for _, s := range usage[id] {
			h := sentenceHash(s, id)
			if !all && contains(r.Confirmed, h) {
				continue
			}
			hint := cl.Source
			for _, m := range regexp.MustCompile(`\\(clm[A-Za-z]+)`).FindAllStringSubmatch(hint, -1) {
				if ref, ok := claims[m[1]]; ok && m[1] != id {
					hint += fmt.Sprintf("  [%s comes from: %s; check %s]", m[1], ref.Source, ref.Check)
				}
			}
			ws.Items = append(ws.Items, &WorkItem{Kind: "figure", Claim: id, Sentence: maskedSentence(s, id), SentenceHash: h,
				SourceHint: maskValueDigits(hint, cl.Value), CheckID: cl.Check, ScopeNotes: maskValueDigits(cl.Notes, cl.Value),
				ValueShape: regexp.MustCompile(`\d`).ReplaceAllString(cl.Value, "#"),
				Answer:     map[string]any{"found_value": "", "quote": "", "source_path": "", "line": nil, "scope_ok": nil, "scope_note": ""}})
		}
	}
	addStmt := func(key, text string, sources []string, attested string) {
		sv := evalStatement(key, text, sources, attested, log)
		if !all && sv.Confirmed {
			return
		}
		ws.Items = append(ws.Items, &WorkItem{Kind: "statement", Key: key, Sentence: statementText(text), TextHash: sv.TextHash,
			Sources: sources, Answer: map[string]any{"supported": nil, "unsupported": []string{}, "quotes": []any{}, "scope_note": ""}})
	}
	for _, r := range c.allBullets() {
		addStmt(r.B.ID, r.B.Text, r.B.Sources, r.B.Attested)
	}
	for _, l := range c.leads() {
		addStmt(l.Key, l.Text, nil, "")
	}
	out := a.get("out", "blind_worksheet.json")
	if n := a.intFlag("split", 1, 1, 50); n > 1 {
		parts := splitWorksheet(ws, n)
		if len(parts) < n {
			fmt.Printf("note: the items come from %d project group(s), so %d part(s) instead of %d\n", len(parts), len(parts), n)
		}
		for i, part := range parts {
			p := strings.TrimSuffix(out, ".json") + fmt.Sprintf("_%d.json", i+1)
			writeJSON(p, part)
			fmt.Printf("wrote %s: %d item(s)\n", p, len(part.Items))
		}
		fmt.Println("give each part to a different blind verifier, then: rkb claims ingest <all answered parts>")
		return
	}
	writeJSON(out, ws)
	nf, ns := 0, 0
	for _, it := range ws.Items {
		if it.Kind == "figure" {
			nf++
		} else {
			ns++
		}
	}
	fmt.Printf("wrote %s: %d figure sentence(s) and %d statement(s) need a blind pass\n", out, nf, ns)
}

func claimsIngest(a *args) {
	if len(a.pos) == 0 {
		die("missing answers file(s): rkb claims ingest <answers.json> [more.json ...]")
	}
	claims := parseClaims()
	add := VerifyLog{}
	nOK, nBad := 0, 0
	verifiers := map[string]string{}
	// read and check every file first: a bad third file must not leave the
	// first two half-applied
	sheets := make([]Worksheet, len(a.pos))
	for i, f := range a.pos {
		if err := readJSON(f, &sheets[i]); err != nil {
			die("%v (nothing was recorded)", err)
		}
	}
	for i, f := range a.pos {
		ws := sheets[i]
		by := strings.TrimSpace(ws.Verifier)
		if by == "" {
			die("%s has no \"verifier\"; the blind verifier must name itself", f)
		}
		switch strings.ToLower(by) {
		case "author", "drafter", "me", "self", "user":
			die("%s: verifier %q is not an independent blind verifier", f, by)
		}
		if len(ws.Items) == 0 {
			die("%s has no items", f)
		}
		if err := checkAnswerHashes(&ws); err != nil {
			die("%s: %v — nothing was recorded", f, err)
		}
		if prev, ok := verifiers[by]; ok && prev != f {
			fmt.Printf("note: %s and %s were filled by the same verifier %q\n", prev, f, by)
		}
		verifiers[by] = f
		ok, bad := ingestWorksheet(&ws, by, claims, add)
		nOK += ok
		nBad += bad
	}
	appendLog(add)
	fmt.Printf("\n%d confirmed, %d need attention (log: %s)\n", nOK, nBad, logPath())
	if nBad > 0 {
		os.Exit(1)
	}
}

// checkAnswerHashes: a verdict without the hash of the sentence it judged
// matches no sentence. Recorded, it reads as CONFIRMED in the log while the
// gate stays stale. Verifiers that re-save a slimmed copy drop these fields.
func checkAnswerHashes(ws *Worksheet) error {
	for _, it := range ws.Items {
		if it.Kind == "figure" && strings.TrimSpace(it.SentenceHash) == "" {
			return fmt.Errorf("figure item %s has no sentence_hash (keep the worksheet's fields and fill answers in place)", it.Claim)
		}
		if it.Kind == "statement" && strings.TrimSpace(it.TextHash) == "" {
			return fmt.Errorf("statement item %s has no text_hash (keep the worksheet's fields and fill answers in place)", it.Key)
		}
	}
	return nil
}

// ingestWorksheet turns one blind verifier's answers into log entries.
// A quote must actually occur in the file it cites (whitespace, case and
// markdown markup ignored): an invented or misattributed quote is refused.
func ingestWorksheet(ws *Worksheet, by string, claims map[string]*Claim, add VerifyLog) (nOK, nBad int) {
	for _, it := range ws.Items {
		ans := it.Answer
		if ans == nil {
			fmt.Printf("[SKIP       ] %s%s: no answer object\n", it.Claim, it.Key)
			continue
		}
		str := func(k string) string { s, _ := ans[k].(string); return strings.TrimSpace(s) }
		if it.Kind == "statement" {
			sup, supAnswered := answerBool(ans["supported"])
			e := LogEntry{Pass: 2, Kind: "statement", Method: "blind-statement", By: by, At: nowISO(), TextHash: it.TextHash}
			quotes, _ := ans["quotes"].([]any)
			valid, unfound, unusable := 0, 0, 0
			var firstQuote string
			for _, q := range quotes {
				m, _ := q.(map[string]any)
				pth, _ := m["path"].(string)
				qt, _ := m["quote"].(string)
				if u, _ := m["url"].(string); pth == "" && isURL(u) {
					pth = u
				}
				if isURL(pth) {
					valid++
					e.Sources = append(e.Sources, struct {
						Path string `json:"path"`
						SHA  string `json:"sha256"`
					}{pth, "url"})
					continue
				}
				pth = cleanRel(pth)
				// evidence is a quote that really occurs in an existing artifact
				// file: no quote, a folder, or a claims/resume file proves nothing
				if pth == "" || circularSource(pth) || !fileExists(inRoot(pth)) || strings.TrimSpace(qt) == "" {
					unusable++
					continue
				}
				if !quoteInFile(inRoot(pth), qt) {
					unfound++
					continue
				}
				valid++
				if firstQuote == "" {
					firstQuote = qt
				}
				e.Sources = append(e.Sources, struct {
					Path string `json:"path"`
					SHA  string `json:"sha256"`
				}{pth, sha256File(inRoot(pth))})
			}
			uns := answerList(ans["unsupported"])
			switch {
			case !supAnswered:
				e.Verdict, e.Evidence = "NO_ANSWER", "supported was not answered (true or false)"
			case sup && len(uns) > 0:
				e.Verdict, e.Evidence = "UNSUPPORTED", fmt.Sprintf("supported=true but unsupported lists %v", uns)
			case sup && valid > 0 && unfound > 0:
				// one invented quote taints the answer: re-verify rather than
				// keep the quotes that happened to match
				e.Verdict, e.Evidence = "QUOTE_NOT_FOUND", fmt.Sprintf("%d of %d quote(s) do not occur in the files they cite", unfound, valid+unfound)
			case sup && valid > 0:
				e.Verdict, e.Evidence = "CONFIRMED", fmt.Sprintf("%d quote(s)", valid)
				e.Quote = truncate(firstQuote, 300)
				if unusable > 0 {
					e.Evidence += fmt.Sprintf("; %d citation(s) without a quote or file ignored", unusable)
				}
			case sup && unfound > 0:
				e.Verdict, e.Evidence = "QUOTE_NOT_FOUND", fmt.Sprintf("%d quote(s) do not occur in the files they cite", unfound)
			case sup:
				e.Verdict, e.Evidence = "NO_SOURCE", "supported=true but no verbatim quote from an existing artifact file"
			default:
				e.Verdict, e.Evidence = "UNSUPPORTED", fmt.Sprintf("%v %s", uns, str("scope_note"))
			}
			add["stmt:"+it.Key] = append(add["stmt:"+it.Key], e)
			report(e.Verdict, it.Key, e.Evidence, &nOK, &nBad)
			continue
		}
		cl, ok := claims[it.Claim]
		if !ok {
			fmt.Printf("[SKIP       ] %s: not in Claims.tex\n", it.Claim)
			continue
		}
		found, sp, qt := str("found_value"), cleanRel(str("source_path")), str("quote")
		e := LogEntry{Pass: 2, Kind: "figure", Method: "blind-rederive", By: by, At: nowISO(), Value: cl.Value,
			Found: found, SourcePath: sp, Line: ans["line"], SentenceHash: it.SentenceHash, Quote: truncate(qt, 300)}
		scopeOK, hasScope := answerBool(ans["scope_ok"])
		switch {
		case found == "":
			e.Verdict, e.Evidence = "NOT_FOUND", "verifier could not locate the figure: "+str("scope_note")
		case !sameNumbers(cl.Value, found):
			e.Verdict, e.Evidence = "MISMATCH", fmt.Sprintf("verifier found %q, Claims.tex has %q", found, cl.Value)
		case hasScope && !scopeOK:
			e.Verdict, e.Evidence = "SCOPE", str("scope_note")
		case !hasScope:
			e.Verdict, e.Evidence = "NO_SCOPE", "scope_ok was not answered"
		case sp == "" || !fileExists(inRoot(sp)):
			e.Verdict, e.Evidence = "NO_SOURCE", "source_path does not exist: "+sp
		case circularSource(sp):
			e.Verdict, e.Evidence = "NO_SOURCE", "source_path is a claims/resume/KB file, not an artifact: "+sp
		case qt == "":
			e.Verdict, e.Evidence = "NO_QUOTE", "no quote given from "+sp
		case !quoteInFile(inRoot(sp), qt):
			e.Verdict, e.Evidence = "QUOTE_NOT_FOUND", fmt.Sprintf("the quote does not occur in %s: %q", sp, truncate(qt, 120))
		default:
			e.Verdict, e.Evidence = "CONFIRMED", truncate(qt, 200)
			e.SourceSHA = sha256File(inRoot(sp))
		}
		add[cl.ID] = append(add[cl.ID], e)
		report(e.Verdict, cl.ID, e.Evidence, &nOK, &nBad)
	}
	return nOK, nBad
}

// cleanRel normalises a verifier's path: strips a leading "./" or code_root.
func cleanRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if root := codeRoot(); filepath.IsAbs(p) && strings.HasPrefix(p, root+string(filepath.Separator)) {
		p = strings.TrimPrefix(p, root+string(filepath.Separator))
	}
	for _, pre := range []string{"$HOME/mnt/Code/", "~/Code/", "./"} {
		p = strings.TrimPrefix(p, pre)
	}
	return p
}

// circularSource: a file that restates claims rather than producing them —
// the CV folder (Claims.tex, resumes, bank, validator), knowledge.json and the
// dossiers. Citing one as evidence would verify a claim against itself.
func circularSource(p string) bool {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = inRoot(p)
	}
	abs = filepath.Clean(abs)
	for _, d := range []string{cfg.CVDir, cfg.DossiersDir} {
		root := filepath.Clean(inRoot(d))
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return abs == filepath.Clean(inRoot(cfg.KnowledgeJSON))
}

func report(verdict, id, ev string, ok, bad *int) {
	if verdict == "CONFIRMED" {
		*ok++
	} else {
		*bad++
	}
	fmt.Printf("[%-11s] %s: %s\n", verdict, id, truncate(ev, 140))
}

// claimsAdd logs a manual PASS 1 only. Pass 2 comes exclusively from a blind
// verifier's answers through `claims ingest`.
func claimsAdd(a *args) {
	claims := parseClaims()
	id, found, sp := a.get("claim", ""), a.get("found", ""), a.get("source-path", "")
	if a.get("pass", "1") != "1" {
		die("pass 2 can only be recorded by `rkb claims ingest` from a blind verifier's answers")
	}
	cl, ok := claims[id]
	if !ok {
		die("%s is not in %v", id, cfg.ClaimsTex)
	}
	if found == "" || sp == "" {
		die("a manual pass 1 needs --found \"<value as the source states it>\" and --source-path <file under code_root>")
	}
	if !fileExists(inRoot(sp)) {
		die("source file not found: %s", sp)
	}
	if circularSource(sp) {
		die("%s restates claims (CV folder, knowledge.json, dossiers); cite the artifact that produced the figure", sp)
	}
	if !sameNumbers(cl.Value, found) {
		die("refusing: found %q but Claims.tex has %q", found, cl.Value)
	}
	appendLog(VerifyLog{id: {LogEntry{Pass: 1, Kind: "figure", Verdict: "CONFIRMED", Method: "manual-read",
		By: a.get("by", "author"), At: nowISO(), Value: cl.Value, Found: found, SourcePath: sp,
		Evidence: a.get("evidence", ""), SourceSHA: sha256File(inRoot(sp))}}})
	fmt.Printf("logged manual pass 1 for %s\n", id)
}

// claimsVM runs the CV repo's validator and saves its report. The validator
// is the Go port in career/CurriculumVitae/validate (built once into the user
// cache); a validate_metrics.py path is still accepted.
func claimsVM(a *args) {
	v := inRoot(cfg.Validator)
	cv := inRoot(cfg.CVDir)
	var cmd *exec.Cmd
	switch {
	case dirExists(v):
		cache, _ := os.UserCacheDir()
		bin := filepath.Join(cache, "rkb", "validate_metrics")
		b := exec.Command(goBinary(), "build", "-o", bin, ".")
		b.Dir = v
		b.Env = append(os.Environ(), goEnv()...)
		if o, err := b.CombinedOutput(); err != nil {
			die("building the validator failed (run `rkb setup`?): %v\n%s", err, o)
		}
		var flags []string
		for _, f := range []string{"only", "skip"} {
			if x := a.get(f, ""); x != "" {
				flags = append(flags, "-"+f+"="+x)
			}
		}
		cmd = exec.Command(bin, flags...)
	case strings.HasSuffix(v, ".py"):
		cmd = exec.Command(a.get("python", "python3"), v)
	default:
		cmd = exec.Command(v)
	}
	cmd.Dir = cv
	cmd.Env = append(append(os.Environ(), goEnv()...), "CV_DIR="+cv)
	out, runErr := cmd.CombinedOutput()
	p := a.get("out", "vm_report.txt")
	report := string(out)
	if why := vmAbnormal(report, runErr); why != "" {
		// A crashed or truncated run must never replace a good report: every
		// check it did not print would read as "not run" and the claims export
		// would quietly lose pass 1 for them.
		failed := p + ".failed"
		writeFile(failed, report)
		if !a.bools["force"] {
			die("the validator run looks broken (%s); kept %s unchanged and saved this output to %s. Fix it, or pass --force to write it anyway", why, p, failed)
		}
		fmt.Printf("WARNING: validator run looks broken (%s); writing it because of --force\n", why)
	}
	report = "# rkb-vm-report generated=" + nowISO() + "\n" + report
	if a.bools["append"] && fileExists(p) {
		prev, err := os.ReadFile(p)
		if err != nil {
			die("--append: %v", err)
		}
		report = mergeVM(string(prev), report)
	}
	writeFile(p, report)
	vm, _ := parseVM(p)
	t := map[string]int{}
	for _, r := range vm {
		t[r.Verdict]++
	}
	b, _ := json.Marshal(t)
	fmt.Printf("wrote %s: %s\n", p, b)
}

// vmAbnormal says why a validator run cannot be trusted as a report, or "".
// A healthy run exits 0 (clean) or 1 (problems found), prints at least one
// verdict line, the tally and a RESULT line.
func vmAbnormal(out string, runErr error) string {
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			return "did not start: " + runErr.Error()
		}
		code = ee.ExitCode()
	}
	if code != 0 && code != 1 {
		return fmt.Sprintf("exit status %d", code)
	}
	if strings.Contains(out, "\npanic: ") || strings.HasPrefix(out, "panic: ") || strings.Contains(out, "fatal error: ") {
		return "it panicked"
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if vmLine.MatchString(line) {
			n++
		}
	}
	if n == 0 {
		return "no check results in the output"
	}
	if !strings.Contains(out, "RESULT: ") {
		return "no RESULT line (cut short?)"
	}
	return ""
}

// mergeVM: a partial run (--only/--skip, e.g. a slow suite run on its own
// because the device shell limits one command's time) replaces those checks'
// blocks in the previous report and keeps every other check's result.
func mergeVM(prev, cur string) string {
	ran := map[string]bool{}
	for _, line := range strings.Split(cur, "\n") {
		if m := vmLine.FindStringSubmatch(line); m != nil {
			ran[m[2]] = true
		}
	}
	var kept []string
	drop := false
	for _, line := range strings.Split(prev, "\n") {
		if m := vmLine.FindStringSubmatch(line); m != nil {
			drop = ran[m[2]]
		} else if line == "" || (line[0] != ' ' && line[0] != '\t') {
			drop = false
		}
		if !drop {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n") + "\n# --- merged partial run (" + nowISO() + ") ---\n" + cur
}

// isURL: a public https source (a published paper, a release page). Statement
// checks accept it; the blind verifier fetches it. Figures still need a file.
func isURL(s string) bool { return strings.HasPrefix(s, "https://") }

// workGroup keys an item by the project its evidence lives in, so one
// verifier reads one repo instead of every verifier reading all of them.
func workGroup(it *WorkItem) string {
	var hints []string
	if it.Kind == "figure" {
		hints = append(hints, it.SourceHint)
	}
	hints = append(hints, it.Sources...)
	for _, h := range hints {
		for _, f := range strings.Fields(h) {
			f = strings.Trim(f, "()[],;`'\"")
			if strings.HasPrefix(f, "https://") {
				return "web"
			}
			if strings.Count(f, "/") >= 1 {
				parts := strings.Split(cleanRel(f), "/")
				if len(parts) >= 2 {
					return parts[0] + "/" + parts[1]
				}
			}
		}
	}
	if it.CheckID != "" {
		return "check:" + strings.SplitN(it.CheckID, ".", 2)[0]
	}
	return "misc"
}

// splitWorksheet bin-packs groups (largest first) into n parts of similar size.
// Items of one group never span two parts. Every part keeps the instructions.
func splitWorksheet(ws Worksheet, n int) []Worksheet {
	groups := map[string][]*WorkItem{}
	var order []string
	for _, it := range ws.Items {
		g := workGroup(it)
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], it)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if len(groups[order[i]]) != len(groups[order[j]]) {
			return len(groups[order[i]]) > len(groups[order[j]])
		}
		return order[i] < order[j]
	})
	if n > len(order) {
		n = max(len(order), 1)
	}
	parts := make([]Worksheet, n)
	for i := range parts {
		parts[i] = Worksheet{Instructions: ws.Instructions, CodeRoot: ws.CodeRoot, Repos: ws.Repos}
	}
	for _, g := range order {
		k := 0
		for i := range parts {
			if len(parts[i].Items) < len(parts[k].Items) {
				k = i
			}
		}
		parts[k].Items = append(parts[k].Items, groups[g]...)
	}
	return parts
}

// answerBool accepts true/false as JSON booleans or the strings verifiers
// sometimes write ("true", "yes"); anything else is unanswered.
func answerBool(v any) (val, ok bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "y":
			return true, true
		case "false", "no", "n":
			return false, true
		}
	}
	return false, false
}

// answerList reads a list of non-empty strings (or one string).
func answerList(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if s := strings.TrimSpace(fmt.Sprint(x)); s != "" && x != nil {
				out = append(out, s)
			}
		}
	case string:
		if s := strings.TrimSpace(t); s != "" {
			out = append(out, s)
		}
	}
	return out
}
