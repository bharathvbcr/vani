package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// w3aRoot makes a synthetic code root (the CV folder exists, so codeRoot()
// resolves) and points rkb at it for the test.
func w3aRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, cfg.CVDir), 0o755); err != nil {
		t.Fatal(err)
	}
	old := codeRootFlag
	codeRootFlag = root
	t.Cleanup(func() { codeRootFlag = old })
	return root
}

func w3aWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// [94] A manual first pass is taken only while the source still hashes to what
// the reader saw.
func TestW3AManualPass1ChecksSourceSHA(t *testing.T) {
	root := w3aRoot(t)
	src := filepath.Join(root, "proj", "bench.txt")
	w3aWrite(t, src, "latency 5 ms\n")
	cl := &Claim{ID: "clmLat", Value: "5 ms"}
	entry := func(sha string) VerifyLog {
		return VerifyLog{"clmLat": {{Pass: 1, Verdict: "CONFIRMED", Method: "manual-read", Value: "5 ms", Found: "5 ms",
			SourcePath: "proj/bench.txt", SourceSHA: sha}}}
	}
	good := entry(sha256File(src))
	if r := evaluate(cl, good, nil, nil); !r.P1 || r.P1Verdict != "MANUAL" {
		t.Fatalf("an unchanged source must keep the manual pass: %+v", r)
	}
	w3aWrite(t, src, "latency 50 ms\n") // the source moved on after the reading
	r := evaluate(cl, good, nil, nil)
	if r.P1 {
		t.Fatalf("a source edited after the manual read must not satisfy pass 1: %+v", r)
	}
	joined := strings.Join(r.Problems, "; ")
	if !strings.Contains(joined, "clmLat") || !strings.Contains(joined, "bench.txt") {
		t.Errorf("the refusal must name the claim and the source: %q", joined)
	}
}

func TestW3AManualPass1RefusesUnboundedSources(t *testing.T) {
	root := w3aRoot(t)
	cl := &Claim{ID: "clmLat", Value: "5 ms"}
	mk := func(rel string) VerifyLog {
		return VerifyLog{"clmLat": {{Pass: 1, Verdict: "CONFIRMED", Value: "5 ms", Found: "5 ms", SourcePath: rel, SourceSHA: "00"}}}
	}
	// a directory is not a source
	if err := os.MkdirAll(filepath.Join(root, "proj", "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := evaluate(cl, mk("proj/adir"), nil, nil); r.P1 {
		t.Errorf("a directory satisfied manual pass 1: %+v", r)
	}
	// a file one byte over the read cap is refused without being read
	big := filepath.Join(root, "proj", "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxQuoteFile + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if r := evaluate(cl, mk("proj/big.bin"), nil, nil); r.P1 {
		t.Errorf("an over-cap source satisfied manual pass 1: %+v", r)
	}
	// a missing file and an empty recorded hash on a changed file
	if r := evaluate(cl, mk("proj/none.txt"), nil, nil); r.P1 {
		t.Errorf("a missing source satisfied manual pass 1: %+v", r)
	}
}

// [90] finalize must refuse a Claims.tex edited after claims.json was
// exported, whatever the manifest says about claims.json itself.
func TestW3AFinalizeRefusesEditedClaimsTex(t *testing.T) {
	root := sampleCopy(t)
	sel := filepath.Join(root, sampleSel)
	claimsPath := filepath.Join(root, "claims.json")
	if out, code := runSample(t, root, "claims", "export", "--used-in", sel, "--out", claimsPath); code != 0 {
		t.Fatalf("export: exit %d\n%s", code, out)
	}
	// control: unedited, finalize must not stop on Claims.tex
	out, code := runSample(t, root, "finalize", sel, "--claims", claimsPath)
	if code == 0 || strings.Contains(out, "Claims.tex") {
		t.Fatalf("an unedited Claims.tex must not be the reason finalize stops (exit %d):\n%s", code, out)
	}
	tex := filepath.Join(root, cfg.ClaimsTex[0])
	b, err := os.ReadFile(tex)
	if err != nil {
		t.Fatal(err)
	}
	w3aWrite(t, tex, string(b)+"\n% edited after verification\n")
	out, code = runSample(t, root, "finalize", sel, "--claims", claimsPath)
	if code == 0 || !strings.Contains(out, "FINALIZE STOPPED at claims export") || !strings.Contains(out, "Claims.tex changed since") {
		t.Fatalf("finalize must refuse an edited Claims.tex (exit %d):\n%s", code, out)
	}
}

// An older claims.json has no recorded hash: that is "re-verify required",
// stated, not a crash and not a pass.
func TestW3AFinalizeOldClaimsFileNeedsReexport(t *testing.T) {
	root := sampleCopy(t)
	sel := filepath.Join(root, sampleSel)
	claimsPath := filepath.Join(root, "claims.json")
	if out, code := runSample(t, root, "claims", "export", "--used-in", sel, "--out", claimsPath); code != 0 {
		t.Fatalf("export: exit %d\n%s", code, out)
	}
	raw, err := os.ReadFile(claimsPath)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, ln := range strings.Split(string(raw), "\n") {
		if !strings.Contains(ln, "claims_tex_sha256") {
			kept = append(kept, ln)
		}
	}
	w3aWrite(t, claimsPath, strings.Join(kept, "\n"))
	out, code := runSample(t, root, "finalize", sel, "--claims", claimsPath)
	if code != 1 || !strings.Contains(out, "records no Claims.tex hash") || !strings.Contains(out, "re-verify") {
		t.Fatalf("an old claims.json must be refused with a re-verify message (exit %d):\n%s", code, out)
	}
}
