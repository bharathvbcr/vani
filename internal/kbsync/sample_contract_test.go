package kbsync

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSampleKBContract is the contract between Vani and the rkb that ships in
// this repository: it builds ../../rkb, then runs Vani's real path (Load, the
// rkb binary's claims export through RKB.Claims, LoadSelections, Eligible) on a
// copy of rkb/examples, the sample a new user starts from. If rkb's export,
// bank format or review records drift from what Vani reads, this fails. A
// missing sample, a missing Go toolchain or a failed build is a failure, never
// a skip: a contract check that cannot run must not read as a pass.
func TestSampleKBContract(t *testing.T) {
	rkbDir, err := filepath.Abs(filepath.Join("..", "..", "rkb"))
	if err != nil {
		t.Fatal(err)
	}
	sample := filepath.Join(rkbDir, "examples")
	sel := filepath.Join("career", "CurriculumVitae", "resume", "resumes", "sample.json")
	if _, err := os.Stat(filepath.Join(sample, sel)); err != nil {
		t.Fatalf("the sample knowledge base is missing: %v", err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH, so rkb cannot be built for the contract test: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "rkb")
	build := exec.Command(goBin, "build", "-o", bin, ".")
	build.Dir = rkbDir
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building rkb from %s: %v\n%s", rkbDir, err, out)
	}

	// Work on a copy: rkb writes lock files next to bank.json.
	root := t.TempDir()
	err = filepath.WalkDir(sample, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sample, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(root, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, rel), b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy sample: %v", err)
	}
	// rkb runs with Vani's minimal environment; an empty HOME keeps a
	// developer's ~/.config/rkb/config.json out of the result.
	t.Setenv("HOME", t.TempDir())

	b, err := Load(filepath.Join(root, "career", "CurriculumVitae", "resume", "bank.json"))
	if err != nil {
		t.Fatalf("Load the sample bank: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	claims, err := RKB{Binary: bin, CodeRoot: root}.Claims(ctx)
	if err != nil {
		t.Fatalf("rkb claims export on the sample: %v", err)
	}
	if got, want := claims.Values["clmShelfSearch"], "470 → 94 ms (-80%)"; got != want {
		t.Errorf("clmShelfSearch = %q, want %q", got, want)
	}
	sels, err := LoadSelections([]string{filepath.Join(root, sel)})
	if err != nil {
		t.Fatalf("LoadSelections: %v", err)
	}
	el := Eligible(b, claims, sels)

	for _, id := range []string{"shelf-title-index", "sky-batched-calls"} {
		if !el.Bullets[id] {
			t.Errorf("%s must be eligible (reviewed by HR and HM in both rounds, figure resolves); why: %q", id, el.Why[id])
		}
	}
	if !el.Allowed("shelfwise", "shelf-title-index") || !el.Allowed("skycast", "sky-batched-calls") {
		t.Errorf("eligible bullets must be allowed under their entries: %v", el.EntryBullets)
	}
	why, withheld := el.Why["shelf-loan-reminders"]
	if el.Bullets["shelf-loan-reminders"] || !withheld {
		t.Fatalf("the unreviewed bullet must be withheld: eligible=%v why=%q", el.Bullets["shelf-loan-reminders"], why)
	}
	if !strings.Contains(why, "HR reader has not passed this wording in both rounds") {
		t.Errorf("withheld for the wrong reason: %q", why)
	}
	if got := el.Ineligible(); len(got) != 1 || got[0] != "shelf-loan-reminders" {
		t.Errorf("Ineligible() = %v, want only shelf-loan-reminders", got)
	}
	// The resolved wording Vani hashes must be the wording rkb's readers saw.
	bl := b.Bullets["shelf-title-index"]
	cur := WordingHash(RKBPlain(bl.Text, claims.Values))
	if rs := bl.Review["hr"]; len(rs) == 0 || rs[len(rs)-1].TextHash != cur {
		t.Errorf("Vani's wording hash %s does not match the hash rkb recorded on the HR review: %+v", cur, rs)
	}
}
