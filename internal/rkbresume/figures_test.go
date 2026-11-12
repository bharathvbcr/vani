package rkbresume

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// rkbSentenceHashForTest mirrors rkb/claims.go sentenceHash (`sha1hex(squash(plainText(masked)))`)
// to generate realistic `confirmed_sentences` fixtures.
func rkbSentenceHashForTest(rawText, claimID string) string {
	s := strings.ReplaceAll(rawText, "{{"+claimID+"}}", "[MASKED]")
	s = kbsync.TokenRe.ReplaceAllString(s, "<other figure>")
	s = kbsync.RKBPlain(s, nil)
	s = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// rkbShareLintForTest enforces rkb/lint.go:545-550 (`>= 85%` figured, `>= 75%` impact)
// on the composed selection JSON handed to `rkb lint`.
func rkbShareLintForTest(t *testing.T, bankPath string, claims map[string]string, sel string) (int, string) {
	t.Helper()
	rawBank, err := os.ReadFile(bankPath)
	if err != nil {
		t.Fatal(err)
	}
	bank, err := kbsync.Parse(rawBank)
	if err != nil {
		t.Fatal(err)
	}
	ids := selBullets(t, sel)
	n, quant, impact := len(ids), 0, 0
	var errs []string
	for _, id := range ids {
		bl, ok := bank.Bullets[id]
		if !ok {
			t.Fatalf("unknown bullet %q on selection", id)
		}
		toks := tokens(bl.Text)
		if len(toks) > 0 {
			quant++
			imp := false
			for _, cid := range toks {
				v := claims[cid]
				if strings.Contains(v, "%") || strings.Contains(v, "x") || strings.Contains(v, "×") || strings.Contains(v, "/s") || strings.Contains(v, "ms") {
					imp = true
				}
			}
			if imp {
				impact++
			}
		}
	}
	if n > 0 && float64(quant)/float64(n) < 0.85 {
		errs = append(errs, fmt.Sprintf("ERROR only %d/%d bullets carry a figure (%.0f%%); target 85%%", quant, n, 100*float64(quant)/float64(n)))
	}
	if quant > 0 && float64(impact)/float64(quant) < 0.75 {
		errs = append(errs, fmt.Sprintf("ERROR only %d/%d quantified bullets carry a percentage, multiple, time or rate (%.0f%%); target 75%%", impact, quant, 100*float64(impact)/float64(quant)))
	}
	if len(errs) > 0 {
		return 1, strings.Join(errs, "\n") + "\n"
	}
	return 0, "\n0 error(s), 0 warning(s)\n"
}

func TestValidatorReportValidationRefusesBrokenAndQuietReports(t *testing.T) {
	badCases := []struct {
		name string
		body string
	}{
		{"empty file", ""},
		{"whitespace only", "   \n\t\n"},
		{"go panic", "# rkb-vm-report generated=2026-10-01T00:00:00Z\n[PRIMARY ] clmLat ok\npanic: runtime error: index out of range\n"},
		{"python traceback", "Traceback (most recent call last):\n  File \"validate.py\", line 10, in <module>\nValueError: boom\n"},
		{"truncated run missing RESULT", "# rkb-vm-report generated=2026-10-01T00:00:00Z\n[PRIMARY ] clmLat 40%\n  PRIMARY      : 1\n"},
		{
			"quiet problems-only report matching live vm_report_full.txt",
			"# rkb-vm-report generated=2026-10-01T00:00:00Z\n" +
				"[FAIL    ] clmBad expected 4 got 3\n" +
				"  PRIMARY      : 102\n" +
				"  LEDGER       : 29\n" +
				"  FAIL         : 1\n" +
				"RESULT: FAILED (1 problem)\n" +
				"exit status 1\n",
		},
		{
			"parenthesized validate_metrics.py quiet report matching exact vm_report_full.txt lines",
			"====================================================================================================\n" +
				"CV METRIC VALIDATION\n" +
				"====================================================================================================\n" +
				"[FAIL   ] toolchain.composes     broken edge\n" +
				"[SKIP   ] scholarlm.rag          missing flag list\n" +
				"----------------------------------------------------------------------------------------------------\n" +
				"  PRIMARY (recomputed from artifacts) : 102\n" +
				"  LEDGER  (CV agrees with results doc): 29\n" +
				"  SKIP    (could not run — NOT a pass): 2\n" +
				"  UNBACKED(no source found — FIX THE CV): 0\n" +
				"  FAIL                                : 1\n" +
				"RESULT: PROBLEMS FOUND\n" +
				"exit status 1\n",
		},
		{
			"quiet base report masked by appended partial run",
			"# rkb-vm-report generated=2026-10-01T00:00:00Z\n" +
				"  PRIMARY      : 50\n" +
				"  LEDGER       : 10\n" +
				"RESULT: PASSED\n" +
				"\n# --- merged partial run (2026-10-02T00:00:00Z) ---\n" +
				"# rkb-vm-report generated=2026-10-02T00:00:00Z\n" +
				"[PRIMARY ] c1 ok\n" +
				"[PRIMARY ] c2 ok\n" +
				"  PRIMARY      : 2\n" +
				"RESULT: PASSED\n",
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.cfg.Provider, f.cfg.NoModel = nil, "off"
			rep := filepath.Join(t.TempDir(), "vm_report.txt")
			if err := os.WriteFile(rep, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			f.cfg.VMReport = rep
			_, err := f.build()
			wantCode(t, err, CodeConfig)
			_, perr := f.pool()
			wantCode(t, perr, CodeConfig)
		})
	}

	goodCases := []struct {
		name string
		body string
	}{
		{
			"complete single run",
			"# rkb-vm-report generated=2026-10-01T00:00:00Z\n" +
				"[PRIMARY ] c1 40%\n" +
				"[LEDGER  ] c2 2.4x\n" +
				"[FAIL    ] c3 mismatch\n" +
				"  PRIMARY      : 1\n" +
				"  LEDGER       : 1\n" +
				"  FAIL         : 1\n" +
				"RESULT: FAILED (1 problem)\n",
		},
		{
			"merged --append run where mergeVM stripped re-run check from earlier segment",
			"# rkb-vm-report generated=2026-10-01T00:00:00Z\n" +
				"[PRIMARY ] c1 40%\n" +
				"[LEDGER  ] c3 18k/s\n" +
				"  PRIMARY      : 2\n" +
				"  LEDGER       : 1\n" +
				"RESULT: PASSED\n" +
				"\n# --- merged partial run (2026-10-02T00:00:00Z) ---\n" +
				"# rkb-vm-report generated=2026-10-02T00:00:00Z\n" +
				"[PRIMARY ] c2 2.4x\n" +
				"  PRIMARY      : 1\n" +
				"RESULT: PASSED\n",
		},
		{
			"parenthesized validate_metrics.py tallies with merged partial run",
			"# rkb-vm-report generated=2026-10-01T00:00:00Z\n" +
				"[PRIMARY] c1 40%\n" +
				"[LEDGER ] c3 18k/s\n" +
				"  PRIMARY (recomputed from artifacts) : 2\n" +
				"  LEDGER  (CV agrees with results doc): 1\n" +
				"  SKIP    (could not run — NOT a pass): 0\n" +
				"  UNBACKED(no source found — FIX THE CV): 0\n" +
				"  FAIL                                : 0\n" +
				"RESULT: PROBLEMS FOUND\n" +
				"\n# --- merged partial run (2026-10-02T00:00:00Z) ---\n" +
				"# rkb-vm-report generated=2026-10-02T00:00:00Z\n" +
				"[PRIMARY] c2 2.4x\n" +
				"  PRIMARY (recomputed from artifacts) : 1\n" +
				"  LEDGER  (CV agrees with results doc): 0\n" +
				"  SKIP    (could not run — NOT a pass): 0\n" +
				"  UNBACKED(no source found — FIX THE CV): 0\n" +
				"  FAIL                                : 0\n" +
				"RESULT: PROBLEMS FOUND\n",
		},
	}
	for _, tc := range goodCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.cfg.Provider, f.cfg.NoModel = nil, "off"
			rep := filepath.Join(t.TempDir(), "vm_report.txt")
			if err := os.WriteFile(rep, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			f.cfg.VMReport = rep
			res := mustBuild(t, f)
			if res.Fingerprints.VMReport == "" {
				t.Fatal("missing VMReport fingerprint")
			}
		})
	}
}

func TestRebalanceSharesFixesExactLint7Of10And4Of7BugAcrossPoolBaseBuildAndRender(t *testing.T) {
	f := newFixture(t)
	f.cfg.Provider, f.cfg.NoModel = nil, "off"
	f.cfg.Selections = []string{f.base}

	// Register clmCnt="12" in fakeRKB claims alongside clmLat="40%" and unverified clmUnv="3.1x"
	// BEFORE calling reviewFor so plainOf resolves {{clmCnt}} -> "12".
	origClaimCnt, hadCnt := claimValues["clmCnt"]
	claimValues["clmCnt"] = "12"
	t.Cleanup(func() {
		if hadCnt {
			claimValues["clmCnt"] = origClaimCnt
		} else {
			delete(claimValues, "clmCnt")
		}
	})

	// Exact 28 -> 17 -> 10 failure shape:
	// - 11 ineligible impact bullets (filtered by baseChoice)
	// - 14 eligible figured bullets:
	//   - 4 verified impact bullets (imp1..imp3 + cntFollowImp which follows cntLead!)
	//   - 3 verified count-only bullets (cntLead, cnt2, cnt3 using clmCnt="12")
	//   - 7 unverified impact bullets (unv1..unv7 using clmUnv="3.1x"): when 7 drop,
	//     7 figured remain (4 impact + 3 count = 57% impact < 75%) + 3 unquantified_ok -> 7/10 = 70% figured < 85%!
	// - 3 eligible unquantified_ok bullets (uq1, uq2, uq3)
	extraClaims := map[string]string{
		"clmLat": "40%",
		"clmCnt": "12",
		"clmUnv": "3.1x",
	}
	var expB, expC []string
	for i := 1; i <= 7; i++ {
		expB = append(expB, fmt.Sprintf("unv%d", i))
	}
	expB = append(expB, "uq2", "uq3")
	for i := 1; i <= 11; i++ {
		expC = append(expC, fmt.Sprintf("inel%d", i))
	}
	expA := []string{"imp2", "imp3", "cntLead", "cntFollowImp", "cnt2", "cnt3"}

	editBank := func(m map[string]any) {
		bullets := map[string]any{}
		tImp1 := "Cut serving latency {{clmLat}} by redesigning request batching across GPU workers"
		bullets["imp1"] = map[string]any{"text": tImp1, "review": reviewFor(tImp1)}
		tUq1 := "Architected deterministic execution harness across multi-tenant workers"
		bullets["uq1"] = map[string]any{"text": tUq1, "unquantified_ok": "architecture scope", "review": reviewFor(tUq1)}

		for i := 2; i <= 3; i++ {
			id := fmt.Sprintf("imp%d", i)
			txt := fmt.Sprintf("Cut tail latency {{clmLat}} on path %d across request queues", i)
			bullets[id] = map[string]any{"text": txt, "review": reviewFor(txt)}
		}
		tCntLead := "Built {{clmCnt}} evaluation pipelines for model releases"
		bullets["cntLead"] = map[string]any{"text": tCntLead, "review": reviewFor(tCntLead)}
		tCntFollowImp := "In those pipelines, cut verification latency {{clmLat}} across distributed runners"
		bullets["cntFollowImp"] = map[string]any{"text": tCntFollowImp, "review": reviewFor(tCntFollowImp)}
		for i := 2; i <= 3; i++ {
			id := fmt.Sprintf("cnt%d", i)
			txt := fmt.Sprintf("Shipped {{clmCnt}} production connectors %d across ingestion workers", i)
			bullets[id] = map[string]any{"text": txt, "review": reviewFor(txt)}
		}

		for i := 1; i <= 7; i++ {
			id := fmt.Sprintf("unv%d", i)
			txt := fmt.Sprintf("Accelerated throughput {{clmUnv}} on tier %d scheduling loops", i)
			bullets[id] = map[string]any{"text": txt, "review": reviewFor(txt)}
		}
		for i := 2; i <= 3; i++ {
			id := fmt.Sprintf("uq%d", i)
			txt := fmt.Sprintf("Designed fault-tolerant coordinator %d for worker restarts", i)
			bullets[id] = map[string]any{"text": txt, "unquantified_ok": "design scope", "review": reviewFor(txt)}
		}

		for i := 1; i <= 11; i++ {
			id := fmt.Sprintf("inel%d", i)
			txt := fmt.Sprintf("Cut memory {{clmLat}} in module %d across GPU nodes", i)
			bullets[id] = map[string]any{"text": txt}
		}
		m["bullets"] = bullets
		ents := m["entries"].(map[string]any)
		ents["exp-a"].(map[string]any)["bullets"] = expA
		ents["exp-b"].(map[string]any)["bullets"] = expB
		ents["exp-c"].(map[string]any)["bullets"] = expC
	}
	editJSON(t, f.bank, editBank)

	editJSON(t, f.base, func(m map[string]any) {
		secs := m["sections"].([]any)
		secs[0].(map[string]any)["bullets"] = []string{"imp1", "uq1"}
		secs[1].(map[string]any)["entries"] = []any{
			map[string]any{"entry": "exp-a", "bullets": expA},
			map[string]any{"entry": "exp-b", "bullets": expB},
			map[string]any{"entry": "exp-c", "bullets": expC},
		}
	})

	f.rkb.unverified = map[string][]string{"clmUnv": nil}
	f.rkb.lint = func(sel string) (int, string) {
		return rkbShareLintForTest(t, f.bank, extraClaims, sel)
	}

	// 1. PoolView: exposes figure_kind & unquantified_ok, and PoolView.Base already rebalances 14/17 -> 14/16 (>= 85%).
	pv, perr := f.pool()
	if perr != nil {
		t.Fatalf("Pool failed: %v", perr)
	}
	var rawPool map[string]any
	rawBytes, _ := json.Marshal(pv)
	_ = json.Unmarshal(rawBytes, &rawPool)
	sum0 := rawPool["summary"].([]any)[0].(map[string]any)
	sum1 := rawPool["summary"].([]any)[1].(map[string]any)
	if sum0["figure_kind"] != "impact" || sum1["figure_kind"] != "none" || sum1["unquantified_ok"] != true {
		t.Fatalf("PoolBullet figure metadata missing or wrong: sum0=%v sum1=%v", sum0, sum1)
	}

	// 2. Resume Studio Preview & Fit on PoolView.Base: drops unv1..unv7, rebalances count & unquantified_ok
	// without dropping cntLead (because cntFollowImp is an impact continuation!), and passes rkb lint!
	req := Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD}
	prevRes, rerr := Render(context.Background(), f.cfg, req, pv.Base, false)
	if rerr != nil {
		t.Fatalf("Preview Render on PoolView.Base failed: %v", rerr)
	}
	defer os.RemoveAll(prevRes.BuildDir)
	fitRes, ferr := Render(context.Background(), f.cfg, req, pv.Base, true)
	if ferr != nil {
		t.Fatalf("Fit Render on PoolView.Base failed: %v", ferr)
	}
	wantDropped := "unv1,unv2,unv3,unv4,unv5,unv6,unv7"
	if got := strings.Join(fitRes.UnverifiedDropped, ","); got != wantDropped {
		t.Errorf("UnverifiedDropped = %q, want %q (must list only unverified figures, not rebalanced bullets)", got, wantDropped)
	}
	var finalIDs []string
	for _, b := range fitRes.Bullets {
		finalIDs = append(finalIDs, b.ID)
	}
	if !has(finalIDs, "cntLead") || !has(finalIDs, "cntFollowImp") {
		t.Errorf("rebalance dropped cntLead and cascaded impact continuation cntFollowImp: %v", finalIDs)
	}
	if len(fitRes.Notes) == 0 {
		t.Errorf("expected Notes to record bullets trimmed to preserve figure shares, got none")
	}

	// 3. Build base fallback: passes rkb lint with identical verified precision.
	buildRes := mustBuild(t, f)
	if buildRes.Source != SourceBase || strings.Join(buildRes.UnverifiedDropped, ",") != wantDropped {
		t.Fatalf("Build base result = %+v", buildRes)
	}
}

func TestSharedClaimDropsOnlyUnconfirmedSentenceNotConfirmedSibling(t *testing.T) {
	f := newFixture(t)
	rawBank, err := os.ReadFile(f.bank)
	if err != nil {
		t.Fatal(err)
	}
	bank, err := kbsync.Parse(rawBank)
	if err != nil {
		t.Fatal(err)
	}
	// s1 (in Summary) and a1 (in Experience) both use {{clmLat}}.
	// s1 IS confirmed in rkb's Pass 2; a1 is NOT confirmed in Pass 2; Pass 1 succeeded!
	s1Hash := rkbSentenceHashForTest(bank.Bullets["s1"].Text, "clmLat")

	// Custom runner wrapping f.rkb that writes realistic ConfirmedSentences for clmLat.
	f.cfg.Runner = runnerFunc(func(ctx context.Context, c Command, timeout time.Duration) (Output, error) {
		out, err := f.rkb.Run(ctx, c, timeout)
		if err != nil || len(c.Args) == 0 || c.Args[0] != "claims" {
			return out, err
		}
		usedIn := flag(c.Args, "--used-in")
		if usedIn == "" {
			return out, nil
		}
		selBytes, _ := os.ReadFile(usedIn)
		sel := string(selBytes)
		outPath := flag(c.Args, "--out")
		claimsBytes, _ := os.ReadFile(outPath)
		var doc map[string]any
		_ = json.Unmarshal(claimsBytes, &doc)
		cm := doc["claims"].(map[string]any)
		hasS1 := strings.Contains(sel, `"s1"`)
		hasA1 := strings.Contains(sel, `"a1"`)
		switch {
		case hasS1 && hasA1:
			cm["clmLat"] = map[string]any{
				"value":               claimValues["clmLat"],
				"double_verified":     false,
				"confirmed_sentences": []string{s1Hash},
				"problems":            []string{"pass 2 missing for: a1 sentence"},
			}
		case hasS1 && !hasA1:
			cm["clmLat"] = map[string]any{
				"value":               claimValues["clmLat"],
				"double_verified":     true,
				"confirmed_sentences": []string{s1Hash},
			}
		case !hasS1 && hasA1:
			cm["clmLat"] = map[string]any{
				"value":               claimValues["clmLat"],
				"double_verified":     false,
				"confirmed_sentences": []string{},
				"problems":            []string{"pass 2 missing for: a1 sentence"},
			}
		}
		rewritten, _ := json.Marshal(doc)
		_ = os.WriteFile(outPath, rewritten, 0o600)
		return out, nil
	})

	// 1. PoolAndVerify on an edit containing both s1 and a1 flags ONLY a1, keeping confirmed s1.
	editBoth := []byte(`{"sections":[{"title":"Summary","bullets":["s1","s3"]},{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a1","a2"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["c1"]}]}],"jd_keywords":["Rust","inference"]}`)
	_, verifs, verr := PoolAndVerify(context.Background(), f.cfg, [][]byte{editBoth})
	if verr != nil {
		t.Fatalf("PoolAndVerify error: %v", verr)
	}
	if got := strings.Join(verifs[0].Unverified, ","); got != "a1" {
		t.Fatalf("PoolAndVerify Unverified = %q, want only \"a1\" (confirmed sibling s1 must not be flagged)", got)
	}

	// 2. Render drops ONLY a1 on round 0 and ships s1 on round 1!
	req := Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD}
	rres, rerr := Render(context.Background(), f.cfg, req, editBoth, true)
	if rerr != nil {
		t.Fatalf("Render error: %v", rerr)
	}
	if got := strings.Join(rres.UnverifiedDropped, ","); got != "a1" {
		t.Errorf("Render UnverifiedDropped = %q, want \"a1\"", got)
	}
	var renderedIDs []string
	for _, b := range rres.Bullets {
		renderedIDs = append(renderedIDs, b.ID)
	}
	if !has(renderedIDs, "s1") || has(renderedIDs, "a1") {
		t.Errorf("Render bullets = %v, want s1 kept and a1 dropped", renderedIDs)
	}

	// 3. Build probe offers confirmed s1 to Opus while hiding unconfirmed a1!
	replyUsingS1 := `{"sections":[{"title":"Summary","bullets":["s1","s3"]},{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a2"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["c1"]}]}],"jd_keywords":` + mustJSON(testKeywords) + `}`
	f.provider.replies = []string{replyUsingS1}
	bres := mustBuild(t, f)
	userPrompt := f.provider.reqs[0].Messages[0].Text()
	if !strings.Contains(userPrompt, "s1:") {
		t.Errorf("probe hid confirmed s1 from Opus prompt")
	}
	if strings.Contains(userPrompt, "a1:") {
		t.Errorf("probe offered unconfirmed a1 in Opus prompt")
	}
	if bres.Source != SourceTailored {
		t.Errorf("Build source = %q, want tailored (reason: %s)", bres.Source, bres.Reason)
	}
}

func TestModelPromptAndSteeringEnforceFigureAndImpactShares(t *testing.T) {
	f := newFixture(t)
	origCnt, hadCnt := claimValues["clmCnt"]
	claimValues["clmCnt"] = "12"
	t.Cleanup(func() {
		if hadCnt {
			claimValues["clmCnt"] = origCnt
		} else {
			delete(claimValues, "clmCnt")
		}
	})

	// Upgrade the bank so all unquantified bullets carry `unquantified_ok` (real rkb bank shape)
	// and add count-only bullets + enough impact bullets for >= 85% figured and >= 75% impact.
	editJSON(t, f.bank, func(m map[string]any) {
		bl := m["bullets"].(map[string]any)
		bl["s2"].(map[string]any)["unquantified_ok"] = "summary scope"
		bl["s3"].(map[string]any)["unquantified_ok"] = "batching scope"
		bl["a3"].(map[string]any)["unquantified_ok"] = "tracing scope"
		bl["b1"].(map[string]any)["unquantified_ok"] = "dispatcher scope"
		tC1 := "Cut node provisioning time {{clmLat}} with a Kubernetes operator"
		bl["c1"] = map[string]any{"text": tC1, "review": reviewFor(tC1)}
		tB2 := "Cut kernel launch overhead {{clmLat}} across stream queues"
		bl["b2"] = map[string]any{"text": tB2, "review": reviewFor(tB2)}
		tCnt1 := "Built {{clmCnt}} benchmark harnesses across GPU clusters"
		bl["cnt1"] = map[string]any{"text": tCnt1, "review": reviewFor(tCnt1)}
		tCnt2 := "Maintained {{clmCnt}} serving regions across production"
		bl["cnt2"] = map[string]any{"text": tCnt2, "review": reviewFor(tCnt2)}
		ents := m["entries"].(map[string]any)
		ents["exp-a"].(map[string]any)["bullets"] = []string{"a1", "a2", "a3", "a4", "cnt1", "cnt2"}
		ents["exp-b"].(map[string]any)["bullets"] = []string{"b1", "b2"}
	})
	editJSON(t, f.other, func(m map[string]any) {
		secs := m["sections"].([]any)
		secs[1].(map[string]any)["entries"] = []any{
			map[string]any{"entry": "exp-a", "bullets": []string{"a3", "a4", "cnt1", "cnt2"}},
			map[string]any{"entry": "exp-b", "bullets": []string{"b2"}},
			map[string]any{"entry": "exp-c", "bullets": []string{"c1"}},
		}
	})

	// Round 0 picks 2 unquantified bullets out of 6 (4/6 = 67% < 85%) AND 2 count bullets out of 4 figured (2/4 = 50% < 75%).
	badShareReply := `{"sections":[{"title":"Summary","bullets":["s2","s1"]},{"title":"Experience","entries":[` +
		`{"entry":"exp-a","bullets":["cnt1","cnt2"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["c1"]}]}],"jd_keywords":` + mustJSON(testKeywords) + `}`
	// Round 1 fixes shares: 7 bullets total, 6 figured (86% >= 85%), 5 impact + 1 count a4 ("2 GB") (83% >= 75%).
	goodShareReply := `{"sections":[{"title":"Summary","bullets":["s1"]},{"title":"Experience","entries":[` +
		`{"entry":"exp-a","bullets":["a1","a2","a4"]},{"entry":"exp-b","bullets":["b2","b1"]},{"entry":"exp-c","bullets":["c1"]}]}],"jd_keywords":` + mustJSON(testKeywords) + `}`
	f.provider.replies = []string{badShareReply, goodShareReply}

	res := mustBuild(t, f)
	if res.Source != SourceTailored {
		t.Fatalf("expected tailored build after retry, got %+v", res)
	}
	firstPrompt := f.provider.reqs[0].Messages[0].Text()
	for _, want := range []string{"85%", "75%", "[no figure]", "[count figure]"} {
		if !strings.Contains(firstPrompt, want) {
			t.Errorf("first prompt missing %q:\n%s", want, firstPrompt)
		}
	}
	if len(f.provider.reqs) != 2 {
		t.Fatalf("provider turns = %d, want 2 (round 0 refused by checkSteering)", len(f.provider.reqs))
	}
	// Round 0 must have been caught in checkSteering BEFORE running rkb lint/render/fit!
	if got := f.rkb.count("lint"); got != 1 {
		t.Errorf("rkb lint ran %d times, want 1 (checkSteering must refuse bad shares before invoking rkb)", got)
	}
	retryMsg := f.provider.reqs[1].Messages[2].Text()
	if !strings.Contains(retryMsg, "85%") {
		t.Errorf("retry feedback did not explain figure-share violation: %q", retryMsg)
	}
}

func TestRenderErrorNamesDroppedUnverifiedBullets(t *testing.T) {
	f := newFixture(t)
	f.rkb.unverified = map[string][]string{"clmUnv": {"u1"}}
	f.rkb.lint = func(string) (int, string) {
		return 1, "ERROR a1: synthetic prose error\n"
	}
	edit := []byte(`{"sections":[{"title":"Summary","bullets":["s1"]},{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a1"]},{"entry":"exp-c","bullets":["c1","u1"]}]}],"jd_keywords":["Rust","inference"]}`)
	_, err := Render(context.Background(), f.cfg, Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD}, edit, false)
	e := wantCode(t, err, CodeLint)
	if !strings.Contains(e.Msg, "left off as unverified: u1") {
		t.Errorf("Render error message = %q, want it to include '; left off as unverified: u1'", e.Msg)
	}
}
