package rkbcoverage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

func line(key string, terms ...string) Line {
	return Line{At: "2026-10-25T10:00:00Z", Key: key, Company: "Acme", Title: "Engineer", Terms: terms}
}

func readLines(t *testing.T, path string) []Line {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Line
	for _, ln := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var l Line
		if err := json.Unmarshal(ln, &l); err != nil {
			t.Fatalf("not a line: %q", ln)
		}
		out = append(out, l)
	}
	return out
}

func TestHandoffAppendsOneLinePerCallAs0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "resumes", "gaps.jsonl")
	for i, k := range []string{"gh:a:1", "gh:b:2"} {
		if unchanged, err := AppendHandoff(context.Background(), p, line(k, "Rust", fmt.Sprint(i))); err != nil || unchanged {
			t.Fatalf("append %d: %v %v", i, unchanged, err)
		}
	}
	ls := readLines(t, p)
	if len(ls) != 2 || ls[0].Key != "gh:a:1" || ls[1].Key != "gh:b:2" || ls[0].Company != "Acme" {
		t.Errorf("lines = %+v", ls)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", st.Mode(), err)
	}
}

func TestHandoffARetriedCallAddsNothingButNewTermsAreAppended(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gaps.jsonl")
	ctx := context.Background()
	if _, err := AppendHandoff(ctx, p, line("k", "Rust", "CUDA")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	if unchanged, err := AppendHandoff(ctx, p, line("k", "cuda", "RUST")); err != nil || !unchanged {
		t.Fatalf("retry: %v %v", unchanged, err)
	}
	if after, _ := os.ReadFile(p); !bytes.Equal(before, after) {
		t.Error("a retry changed the file")
	}
	if unchanged, err := AppendHandoff(ctx, p, line("k", "Rust")); err != nil || unchanged {
		t.Fatalf("changed terms: %v %v", unchanged, err)
	}
	if n := len(readLines(t, p)); n != 2 {
		t.Errorf("%d lines", n)
	}
}

func TestHandoffRotatesTheOldestAtTheLineCap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gaps.jsonl")
	var seed bytes.Buffer
	for i := 0; i < MaxHandoffLines; i++ { // a full file, as 500 earlier calls left it
		b, _ := json.Marshal(line(fmt.Sprintf("k%d", i), "Rust"))
		seed.Write(append(b, '\n'))
	}
	if err := os.WriteFile(p, seed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := MaxHandoffLines; i < MaxHandoffLines+25; i++ {
		if _, err := AppendHandoff(context.Background(), p, line(fmt.Sprintf("k%d", i), "Rust")); err != nil {
			t.Fatal(err)
		}
	}
	ls := readLines(t, p)
	if len(ls) != MaxHandoffLines || ls[0].Key != "k25" || ls[len(ls)-1].Key != fmt.Sprintf("k%d", MaxHandoffLines+24) {
		t.Errorf("%d lines, first %s last %s", len(ls), ls[0].Key, ls[len(ls)-1].Key)
	}
}

func TestHandoffRotatesTheOldestAtTheByteCapAndKeepsTheNewest(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gaps.jsonl")
	fat := strings.Repeat("x", 70)
	var terms []string
	for i := 0; i < 100; i++ {
		terms = append(terms, fmt.Sprintf("%s%d", fat, i))
	}
	for i := 0; i < 80; i++ { // each line is about 7.5 KB: 80 of them are over 256 KiB
		if _, err := AppendHandoff(context.Background(), p, line(fmt.Sprintf("k%d", i), terms...)); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := os.Stat(p)
	ls := readLines(t, p)
	if st.Size() > MaxHandoffBytes || len(ls) >= 80 || ls[len(ls)-1].Key != "k79" {
		t.Errorf("size %d, %d lines, last %s", st.Size(), len(ls), ls[len(ls)-1].Key)
	}
}

func TestHandoffDropsTermsNotTheLineWhenOneLineIsTooBig(t *testing.T) {
	var terms []string
	for i := 0; i < 400; i++ {
		terms = append(terms, fmt.Sprintf("%s%d", strings.Repeat("y", 100), i))
	}
	l, raw, dropped, err := line("k", terms...).Fit()
	if err != nil || !dropped || len(raw) > MaxLineBytes || len(l.Terms) == 0 || len(l.Terms) >= 400 {
		t.Errorf("fit: %d terms, %d bytes, dropped %v, %v", len(l.Terms), len(raw), dropped, err)
	}
	huge := line("k")
	huge.Title = strings.Repeat("t", MaxLineBytes)
	if _, _, _, err := huge.Fit(); err == nil {
		t.Error("a line that is too big with no terms was accepted")
	}
}

func TestHandoffReplacesAFileItCannotReadWhole(t *testing.T) {
	dir := t.TempDir()
	// Garbage and over-bound: replaced, and the new line is there.
	p := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(p, bytes.Repeat([]byte("z"), 4*MaxHandoffBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendHandoff(context.Background(), p, line("k", "Rust")); err != nil {
		t.Fatal(err)
	}
	if ls := readLines(t, p); len(ls) != 1 {
		t.Errorf("lines = %+v", ls)
	}
	// Torn and foreign lines are dropped, good ones kept.
	q := filepath.Join(dir, "b.jsonl")
	good, _ := json.Marshal(line("old", "Go"))
	if err := os.WriteFile(q, append(append([]byte("not json\n{\"at\":\n"), good...), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendHandoff(context.Background(), q, line("k", "Rust")); err != nil {
		t.Fatal(err)
	}
	if ls := readLines(t, q); len(ls) != 2 || ls[0].Key != "old" {
		t.Errorf("lines = %+v", ls)
	}
	if st, _ := os.Stat(q); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode())
	}
}

func TestHandoffNeverWritesThroughALinkOrIntoAFolder(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "gaps.jsonl")
	if err := os.Symlink(target, p); err != nil {
		t.Skip("no symlinks here")
	}
	if _, err := AppendHandoff(context.Background(), p, line("k", "Rust")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep\n" {
		t.Errorf("the link's target was written: %q", b)
	}
	d := filepath.Join(dir, "folder.jsonl")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendHandoff(context.Background(), d, line("k", "Rust")); err == nil {
		t.Error("a folder was written over")
	}
}

func TestHandoffWaitsForTheLockAndThenGivesUp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gaps.jsonl")
	held, err := filelock.TryAcquire(p + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := AppendHandoff(ctx, p, line("k", "Rust")); !errors.Is(err, ErrHandoffBusy) {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(p); serr == nil {
		t.Error("written while the lock was held")
	}
	_ = held.Release()
	if _, err := AppendHandoff(context.Background(), p, line("k", "Rust")); err != nil {
		t.Fatal(err)
	}
}

func TestHandoffConcurrentWritersLoseNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "gaps.jsonl")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := AppendHandoff(context.Background(), p, line(fmt.Sprintf("k%d", i), "Rust")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(readLines(t, p)); n != 12 {
		t.Errorf("%d lines, want 12", n)
	}
}
