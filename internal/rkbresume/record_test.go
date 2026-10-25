package rkbresume

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func pageIDs(t *testing.T, res Result) []string {
	t.Helper()
	var ids []string
	for _, b := range res.Bullets {
		ids = append(ids, b.ID)
	}
	return ids
}

// A tailored build reports itself: the source, the checks, the keywords, the
// bullets on the page in page order with the hash of their wording, and the
// choice in the owner-edit shape.
func TestResultRecordsATailoredBuild(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{pages: 2, pdf: testPDF, lines: map[string]int{"a3": 2, "a1": 1, "s3": 3}}
	}
	res := mustBuild(t, f)
	if res.Source != SourceTailored || res.Reason != "" || res.Pages != 2 {
		t.Fatalf("result = %+v", res)
	}
	if want := (Checks{Eligibility: "current", Lint: "passed", Fit: "passed", Figures: "verified"}); res.Checks != want {
		t.Errorf("checks = %+v, want %+v", res.Checks, want)
	}
	if strings.Join(res.Keywords, ",") != strings.Join(testKeywords, ",") {
		t.Errorf("keywords = %v", res.Keywords)
	}
	// The bullets are the page's, in the order the final selection lists them.
	if got, want := pageIDs(t, res), selBullets(t, f.rkb.sels[len(f.rkb.sels)-1]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("bullets = %v, the rendered selection has %v", got, want)
	}
	for _, br := range res.Bullets {
		plain := kbsync.RKBPlain(bulletTexts[br.ID], claimValues)
		if want := kbsync.WordingHash(plain); br.TextHash != want || len(br.TextHash) != 12 {
			t.Errorf("%s: text hash %q, want %q", br.ID, br.TextHash, want)
		}
	}
	// FitLines: the ids and counts rkb reported.
	if want := map[string]int{"a3": 2, "a1": 1, "s3": 3}; len(res.FitLines) != len(want) || res.FitLines["a3"] != 2 || res.FitLines["s3"] != 3 || res.FitLines["a1"] != 1 {
		t.Errorf("fit lines = %v", res.FitLines)
	}
}

// The recorded selection is exactly what Render takes: it decodes as an owner's
// edit and builds the selection rkb was given.
func TestResultSelectionJSONIsTheOwnerEditOfWhatWasBuilt(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	res := mustBuild(t, f)
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := b.buildPool(func(string) bool { return true })
	ch, err := b.ownerChoice(res.SelectionJSON, p, keywordText(Request{Description: testJD}))
	if err != nil {
		t.Fatalf("ownerChoice(res.SelectionJSON) = %v\n%s", err, res.SelectionJSON)
	}
	// rkb read the bank snapshot, so only the bank path differs between the two.
	want := f.rkb.sels[len(f.rkb.sels)-1]
	got, err := b.base.selectionJSON(b.cfg.Bank, ch)
	if err != nil {
		t.Fatal(err)
	}
	if selBulletsKey(t, want) != selBulletsKey(t, string(got)) {
		t.Errorf("the recorded choice builds %s, rkb rendered %s", selBulletsKey(t, string(got)), selBulletsKey(t, want))
	}
	for _, bad := range []string{ownerName, ownerContact, `"bank"`, `"header"`, `"meta"`, f.dir} {
		if strings.Contains(string(res.SelectionJSON), bad) {
			t.Errorf("selection record holds %q: %s", bad, res.SelectionJSON)
		}
	}
}

func selBulletsKey(t *testing.T, sel string) string {
	t.Helper()
	return strings.Join(selBullets(t, sel), ",") + "|" + strings.Join(selTitles(t, sel), ",")
}

// The base build says so, with its reason, and carries no keywords of its own.
func TestResultRecordsABaseBuild(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "no llm provider is configured"
	res := mustBuild(t, f)
	if res.Source != SourceBase || res.Reason != "no llm provider is configured" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Keywords) != 0 {
		t.Errorf("keywords = %v: the base selection's own are not job keywords", res.Keywords)
	}
	if len(res.Bullets) == 0 || len(res.SelectionJSON) == 0 {
		t.Errorf("no record of the base page: %+v", res)
	}
}

// A bullet dropped for an unverified figure is not on the page, so not in the
// record, and the figures check says it dropped something.
func TestResultFiguresCheckAndBulletsFollowTheDrops(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{strings.Replace(validReply(), `{"entry":"exp-c","bullets":["c1"]}`, `{"entry":"exp-c","bullets":["c1","u1"]}`, 1)}
	f.rkb.unverified = map[string][]string{"clmUnv": {"u1"}}
	res := mustBuild(t, f)
	if res.Checks.Figures != "dropped" || strings.Join(res.UnverifiedDropped, ",") != "u1" {
		t.Fatalf("result = %+v", res)
	}
	if has(pageIDs(t, res), "u1") || strings.Contains(string(res.SelectionJSON), "u1") {
		t.Errorf("u1 is in the record of a page it is not on: %v %s", pageIDs(t, res), res.SelectionJSON)
	}
}

// A soft layout flaw is a pass with a note, and says "soft", never "passed".
func TestResultFitIsSoftWhenALayoutNoteWasAccepted(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{status: 5, advice: "OK: 2 page(s). STRICT: 1 orphan last line(s)", pages: 2, pdf: testPDF, orphans: []string{"a1"}}
	}
	res := mustBuild(t, f)
	if res.Checks.Fit != "soft" || len(res.Notes) != 1 {
		t.Fatalf("checks = %+v notes = %v", res.Checks, res.Notes)
	}
}

// Only ids and counts leave rkb's fit report: its text (the owner's header here)
// is never decoded, an id that is not on the page and a count that is no
// measurement are dropped.
func TestResultFitLinesKeepOnlyIDsAndCountsOfBulletsOnThePage(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{pages: 2, pdf: testPDF, lines: map[string]int{"a1": 2, "a2": 1, "s1": 0, "s2": 99, "zz": 2},
			extra: []map[string]any{{"id": "a1", "text": ownerName, "lines": 5}, {"id": "../etc", "text": "x", "lines": 1}}}
	}
	res := mustBuild(t, f)
	if len(res.FitLines) != 2 || res.FitLines["a1"] != 2 || res.FitLines["a2"] != 1 {
		t.Fatalf("fit lines = %v, want a1 and a2 only (a duplicate id keeps its first count)", res.FitLines)
	}
	raw, _ := json.Marshal(res)
	for _, bad := range []string{ownerName, ownerContact, "zz", "etc"} {
		if bytes.Contains(raw, []byte(bad)) {
			t.Errorf("the record holds %q: %s", bad, raw)
		}
	}
}

// Chrome measuring nothing is no lines, not an error.
func TestResultHasNoFitLinesWhenRkbReportedNone(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	res := mustBuild(t, f)
	if res.FitLines != nil {
		t.Fatalf("fit lines = %v", res.FitLines)
	}
}

// A failed build still returns what ran: lint passed, fit could not run, and the
// inputs it read; there is no PDF.
func TestAFailedBuildReturnsWhatRanAndNoPDF(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	f.rkb.fit = func(string) fitSpec { return fitSpec{noReport: true, stderr: "rkb: no Chrome/Chromium found"} }
	res, err := f.build()
	wantCode(t, err, CodeRender)
	if len(res.PDF) != 0 || res.Source != "" {
		t.Errorf("a failed build returned a page: %+v", res)
	}
	if want := (Checks{Eligibility: "current", Lint: "passed", Fit: "could_not_run", Figures: "verified"}); res.Checks != want {
		t.Errorf("checks = %+v, want %+v", res.Checks, want)
	}
	if res.Fingerprints.Bank == "" || res.Fingerprints.Claims == "" {
		t.Errorf("fingerprints = %+v", res.Fingerprints)
	}
}

// A build that fails before it read anything says nothing ran.
func TestABuildThatNeverLoadedSaysNothingRan(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	f.rkb.runErr = map[string]error{"claims-plain": os.ErrPermission}
	res, err := f.build()
	wantCode(t, err, CodeKB)
	if want := (Checks{Eligibility: "unknown", Lint: "not_run", Fit: "not_run", Figures: "not_run"}); res.Checks != want {
		t.Errorf("checks = %+v, want %+v", res.Checks, want)
	}
}

// ---- fingerprints

func TestFingerprintsAreHashesOfTheBytesActuallyRead(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	rkbBin := filepath.Join(f.dir, "bin", "rkb")
	if err := os.WriteFile(rkbBin, []byte("#!/bin/sh\nrkb\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.cfg.RKB = rkbBin
	vm := filepath.Join(f.dir, "vm_report.txt")
	if err := os.WriteFile(vm, []byte("validator says\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.cfg.VMReport = vm
	liveBank, _ := os.ReadFile(f.bank)
	// The owner edits the live bank while the build runs: the fingerprint is of the
	// snapshot rkb was given, not of the file on disk at the end.
	edited := false
	f.rkb.onStep = func(step string, c Command) {
		if step == "claims-used" && !edited {
			edited = true
			if err := os.WriteFile(f.bank, append(liveBank, []byte("\n")...), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	res := mustBuild(t, f)
	fp := res.Fingerprints
	baseRaw := mustRead(t, f.base)
	otherRaw := mustRead(t, f.other)
	if fp.Bank != hexSHA(liveBank) {
		t.Errorf("bank = %s, want the snapshot's %s", fp.Bank, hexSHA(liveBank))
	}
	if fp.BaseSelection != hexSHA(baseRaw) {
		t.Errorf("base selection = %s, want %s", fp.BaseSelection, hexSHA(baseRaw))
	}
	pairs, _ := json.Marshal([][2]string{{f.base, hexSHA(baseRaw)}, {f.other, hexSHA(otherRaw)}})
	if fp.Selections != hexSHA(pairs) {
		t.Errorf("selections = %s, want %s", fp.Selections, hexSHA(pairs))
	}
	if fp.VMReport != hexSHA([]byte("validator says\n")) {
		t.Errorf("vm report = %s", fp.VMReport)
	}
	if want := hexSHA(mustRead(t, rkbBin)); fp.RKB != want {
		t.Errorf("rkb = %s, want %s", fp.RKB, want)
	}
	ids := []string{"clmLat", "clmMem", "clmThr", "clmUnv"}
	var cp [][2]string
	for _, id := range ids {
		cp = append(cp, [2]string{id, claimValues[id]})
	}
	craw, _ := json.Marshal(cp)
	if fp.Claims != hexSHA(craw) {
		t.Errorf("claims = %s, want %s", fp.Claims, hexSHA(craw))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Each fingerprint moves when its input does, and only that one.
func TestEachFingerprintMovesWithItsOwnInput(t *testing.T) {
	cases := map[string]struct {
		mutate func(f *fixture)
		moved  func(a, b Fingerprints) bool
	}{
		"a claim value": {func(f *fixture) {
			f.rkb.claimValue = func(id, v, sel string) string {
				if id == "clmThr" {
					return "4x"
				}
				return v
			}
		}, func(a, b Fingerprints) bool {
			return a.Claims != b.Claims && a.Bank == b.Bank && a.Selections == b.Selections
		}},
		"the other selection": {func(f *fixture) {
			editJSON(t, f.other, func(m map[string]any) { m["note"] = "x" })
		}, func(a, b Fingerprints) bool {
			return a.Selections != b.Selections && a.BaseSelection == b.BaseSelection && a.Claims == b.Claims
		}},
		"the base selection": {func(f *fixture) {
			editJSON(t, f.base, func(m map[string]any) { m["note"] = "x" })
		}, func(a, b Fingerprints) bool {
			return a.BaseSelection != b.BaseSelection && a.Selections != b.Selections
		}},
		"the bank": {func(f *fixture) {
			editJSON(t, f.bank, func(m map[string]any) { m["note"] = "x" })
		}, func(a, b Fingerprints) bool { return a.Bank != b.Bank && a.Claims == b.Claims }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.cfg.Provider, f.cfg.NoModel = nil, "off"
			base := mustBuild(t, f).Fingerprints
			c.mutate(f)
			if got := mustBuild(t, f).Fingerprints; !c.moved(base, got) {
				t.Errorf("base %+v, changed %+v", base, got)
			}
		})
	}
}

func TestFingerprintsOfAFileThatIsNotThereAreNeverAHashOrEmpty(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	f.cfg.VMReport = filepath.Join(f.dir, "gone.txt")
	fp := mustBuild(t, f).Fingerprints
	if fp.VMReport != "unreadable" || fp.RKB != "unreadable" {
		t.Errorf("vm report %q, rkb %q: a named file that could not be read is not a hash and not 'none'", fp.VMReport, fp.RKB)
	}
	g := newFixture(t)
	g.cfg.Provider, g.cfg.NoModel = nil, "off"
	if fp := mustBuild(t, g).Fingerprints; fp.VMReport != "" {
		t.Errorf("no validator report configured: vm report = %q, want empty", fp.VMReport)
	}
}

func TestFingerprintClaimsAndSelectionsDoNotRunTogether(t *testing.T) {
	a := claimsDigest(kbsync.Claims{Values: map[string]string{"clmA": "bc", "clmB": "d"}})
	b := claimsDigest(kbsync.Claims{Values: map[string]string{"clmA": "b", "clmB": "cd"}})
	if a == b {
		t.Error("claims digest ignores where one value ends")
	}
	if claimsDigest(kbsync.Claims{Values: map[string]string{"x": "1", "y": "2"}}) != claimsDigest(kbsync.Claims{Values: map[string]string{"y": "2", "x": "1"}}) {
		t.Error("claims digest depends on map order")
	}
	s1 := selectionsDigest([]kbsync.Selection{{Path: "/a", SHA256: "bb"}, {Path: "/c", SHA256: "d"}})
	s2 := selectionsDigest([]kbsync.Selection{{Path: "/a", SHA256: "b"}, {Path: "/bc", SHA256: "d"}})
	if s1 == s2 {
		t.Error("selections digest runs a path into a hash")
	}
	if s1 != selectionsDigest([]kbsync.Selection{{Path: "/c", SHA256: "d"}, {Path: "/a", SHA256: "bb"}}) {
		t.Error("selections digest depends on the order the paths are listed in")
	}
}

func TestFileSHA256IsBoundedAndRefusesAFolder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fileSHA256(p, 3); err == nil {
		t.Error("a file over the bound was hashed")
	}
	if got, err := fileSHA256(p, 4); err != nil || got != hexSHA([]byte("abcd")) {
		t.Errorf("fileSHA256 = %q, %v", got, err)
	}
	if _, err := fileSHA256(dir, 100); err == nil {
		t.Error("a folder was hashed")
	}
}

// ---- contract parity with resumestore.Meta

// metaFields is the field set of resumestore.Meta as the wave 2 contract writes
// it, copied here so rkbresume need not import the store. A Result must marshal
// into it without a key Meta lacks, and survive the trip unchanged.
type metaFields struct {
	Schema    int             `json:"schema"`
	Key       string          `json:"key"`
	Version   int             `json:"version"`
	Parent    int             `json:"parent,omitempty"`
	Source    string          `json:"source"`
	Reason    string          `json:"reason,omitempty"`
	Model     string          `json:"model,omitempty"`
	Cost      metaCost        `json:"cost"`
	Checks    metaChecks      `json:"checks"`
	Pages     int             `json:"pages,omitempty"`
	Notes     []string        `json:"notes,omitempty"`
	Dropped   []string        `json:"unverified_dropped,omitempty"`
	Keywords  []string        `json:"jd_keywords,omitempty"`
	Selection json.RawMessage `json:"selection"`
	Bullets   []metaBullet    `json:"bullets"`
	FitLines  map[string]int  `json:"fit_lines,omitempty"`
	Prints    metaPrints      `json:"fingerprints"`
	PDFSHA256 string          `json:"pdf_sha256,omitempty"`
	Created   time.Time       `json:"created"`
}

type metaCost struct {
	USD      float64 `json:"usd"`
	Complete bool    `json:"complete"`
	Turns    int     `json:"turns"`
}

type metaChecks struct {
	Eligibility string `json:"eligibility"`
	Lint        string `json:"lint"`
	Fit         string `json:"fit"`
	Figures     string `json:"figures"`
}

type metaBullet struct {
	ID       string `json:"id"`
	TextHash string `json:"text_hash"`
}

type metaPrints struct {
	Bank          string `json:"bank"`
	BaseSelection string `json:"base_selection"`
	Selections    string `json:"selections"`
	VMReport      string `json:"vm_report"`
	Claims        string `json:"claims"`
	RKB           string `json:"rkb"`
}

func TestResultMarshalsLosslesslyIntoTheStoresMeta(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	f.rkb.fit = func(string) fitSpec {
		return fitSpec{status: 5, advice: "OK: 2 page(s). STRICT: 1 orphan last line(s)", pages: 2, pdf: testPDF, orphans: []string{"a1"}, lines: map[string]int{"a1": 2}}
	}
	res, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceTailored || res.Checks.Fit != "soft" || len(res.FitLines) == 0 || len(res.Notes) == 0 {
		t.Fatalf("the scenario did not fill the optional fields: %+v", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"PDF", "HTMLPath", "pdf", "html_path"} {
		if _, ok := top[k]; ok {
			t.Errorf("Result marshals %q, which no Meta field holds", k)
		}
	}
	// No key outside Meta's: DisallowUnknownFields refuses one.
	var meta metaFields
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&meta); err != nil {
		t.Fatalf("Result has a field Meta lacks, or one with a different type: %v\n%s", err, raw)
	}
	// Lossless: Meta's copy marshals to the same document.
	back, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal(raw, &a)
	_ = json.Unmarshal(back, &b)
	for _, k := range []string{"schema", "key", "version", "created"} { // the store's own
		delete(b, k)
	}
	delete(b, "pdf_sha256")
	if !equalJSON(a, b) {
		t.Errorf("round trip changed the record:\nresult: %s\nmeta:   %s", raw, back)
	}
	// Every field is carried, not just present: spot-check the typed ones.
	if meta.Source != res.Source || meta.Pages != res.Pages || meta.Cost != metaCost(res.Cost) || meta.Checks != metaChecks(res.Checks) ||
		meta.Prints != metaPrints(res.Fingerprints) || len(meta.Bullets) != len(res.Bullets) || string(meta.Selection) != string(res.SelectionJSON) ||
		strings.Join(meta.Keywords, ",") != strings.Join(res.Keywords, ",") || meta.FitLines["a1"] != 2 {
		t.Errorf("meta = %+v", meta)
	}
}

func equalJSON(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
