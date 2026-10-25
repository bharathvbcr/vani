package resumestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// class names what an error means to a caller, so a walk and a probe can be
// compared by meaning rather than by message.
func class(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrDamaged):
		return "damaged"
	case errors.Is(err, ErrSchema):
		return "schema"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	}
	return "other: " + err.Error()
}

// The walk answers every job exactly as the per-job Summary does: the same
// numbers for a healthy job, the zero Summary for a job with no folder or no
// versions, and an error of the same kind for each way a job can be damaged.
func TestSummariesAnswerLikeSummaryForEveryKindOfFolder(t *testing.T) {
	s, state := newStore(t)
	ctx := context.Background()
	root := filepath.Join(state, "resumes")
	folder := func(k string) string { return filepath.Join(root, jobFolder(k)) }

	mustSave(t, s, "healthy", goodMeta(), pdfBytes("1"))
	m := goodMeta()
	m.Parent, m.Source, m.Pages = 1, "edited", 2
	mustSave(t, s, "healthy", m, pdfBytes("2"))
	if _, err := s.Use(ctx, "healthy", 1); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "no-pdf", noPDFMeta(), nil)
	if err := s.AppendAttempt(ctx, "attempts-only", Attempt{Code: "timeout"}); err != nil {
		t.Fatal(err)
	}
	// Damage, one way each.
	mustSave(t, s, "pdf-gone", goodMeta(), pdfBytes("x"))
	if err := os.Remove(filepath.Join(folder("pdf-gone"), "v1.pdf")); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "other-key", goodMeta(), pdfBytes("x"))
	if err := os.WriteFile(filepath.Join(folder("other-key"), keyFile), []byte("someone-else"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "no-keyfile", goodMeta(), pdfBytes("x"))
	if err := os.Remove(filepath.Join(folder("no-keyfile"), keyFile)); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "bad-pointer", goodMeta(), pdfBytes("x"))
	if err := os.WriteFile(filepath.Join(folder("bad-pointer"), currentFile), []byte("7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "bad-schema", goodMeta(), pdfBytes("x"))
	if raw, err := os.ReadFile(versionFile(folder("bad-schema"), 1, "json")); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(versionFile(folder("bad-schema"), 1, "json"), []byte(string(raw[:len(`{"schema":`)])+"2"+string(raw[len(`{"schema":1`):])), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "record-other-key", goodMeta(), pdfBytes("x"))
	if raw, err := os.ReadFile(versionFile(folder("record-other-key"), 1, "json")); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(versionFile(folder("record-other-key"), 1, "json"), bytesReplace(raw, `"key":"record-other-key"`, `"key":"z"`), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unfinished create (a folder with nothing in it) and a folder that is a file.
	if err := os.Mkdir(folder("unfinished"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(folder("a-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	x, err := s.Summaries()
	if err != nil {
		t.Fatalf("Summaries: %v", err)
	}
	keys := []string{"healthy", "no-pdf", "attempts-only", "pdf-gone", "other-key", "no-keyfile", "bad-pointer", "bad-schema", "record-other-key", "unfinished", "a-file", "never-saved"}
	for _, k := range keys {
		want, werr := s.Summary(k)
		got, gerr := x.Of(k)
		if class(gerr) != class(werr) {
			t.Errorf("%s: walk says %s (%v), probe says %s (%v)", k, class(gerr), gerr, class(werr), werr)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: walk %+v, probe %+v", k, got, want)
		}
	}
	// The kinds that matter, pinned so the comparison above cannot agree on nothing.
	for k, want := range map[string]string{
		"healthy": "ok", "no-pdf": "ok", "attempts-only": "ok", "unfinished": "ok", "never-saved": "ok",
		"pdf-gone": "damaged", "other-key": "damaged", "no-keyfile": "damaged", "bad-pointer": "damaged",
		"bad-schema": "schema", "record-other-key": "damaged", "a-file": "damaged",
	} {
		if _, err := x.Of(k); class(err) != want {
			t.Errorf("%s: %s, want %s (%v)", k, class(err), want, err)
		}
	}
	if sum, _ := x.Of("healthy"); sum.Versions != 2 || sum.Current == nil || sum.Current.Version != 2 || sum.Used == nil || sum.Used.Version != 1 || sum.Current.Selection != nil {
		t.Fatalf("healthy = %+v", sum)
	}
	if _, err := x.Of(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty key: %v", err)
	}
}

func bytesReplace(b []byte, old, new string) []byte {
	return []byte(replaceOnce(string(b), old, new))
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func TestSummariesSkipDotEntriesAndStrays(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	root := filepath.Join(state, "resumes")
	for _, d := range []string{".tmp", "not-a-hash", hexOf('a')[:63]} {
		if err := os.Mkdir(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A file named like a job folder is not a stray: it is some job's damage.
	if err := os.WriteFile(filepath.Join(root, hexOf('b')), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	x, err := s.Summaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(x.byFolder) != 2 {
		t.Fatalf("the walk holds %d entries, want the job's folder and the damaged hash-named file only", len(x.byFolder))
	}
	if r := x.byFolder[hexOf('b')]; !errors.Is(r.err, ErrDamaged) {
		t.Fatalf("a hash-named file = %v, want ErrDamaged", r.err)
	}
	if sum, err := x.Of("k"); err != nil || sum.Versions != 1 {
		t.Fatalf("k = %+v %v", sum, err)
	}
}

func TestSummariesOfAStoreNeverWrittenAndOfOneThatCannotBeListed(t *testing.T) {
	s, state := newStore(t)
	x, err := s.Summaries()
	if err != nil {
		t.Fatalf("a store never written: %v", err)
	}
	if sum, err := x.Of("k"); err != nil || sum.Versions != 0 {
		t.Fatalf("k = %+v %v", sum, err)
	}
	if _, err := os.Stat(filepath.Join(state, "resumes")); err == nil {
		t.Fatal("the walk created the store")
	}
	var nilX *Summaries
	if sum, err := nilX.Of("k"); err != nil || sum.Versions != 0 {
		t.Fatalf("no store: %+v %v", sum, err)
	}
	// The store's root is a file: nothing can be listed, and every job says so
	// rather than reading as "no resume".
	if err := os.WriteFile(filepath.Join(state, "resumes"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	x, err = s.Summaries()
	if err == nil || x == nil {
		t.Fatalf("an unlistable store: %v %v", x, err)
	}
	if _, oerr := x.Of("k"); oerr == nil {
		t.Fatal("an unlistable store answered a job with no error")
	}
}

// More folders than the store ever keeps: the walk reads MaxJobs of them and
// says so, and the jobs of the folders it left unread are errors, not "no
// resume".
func TestSummariesCapAtMaxJobsAndSayWhatWasNotRead(t *testing.T) {
	s, state := newStore(t)
	root := filepath.Join(state, "resumes")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const extra = 3
	keys := make([]string, MaxJobs+extra)
	for i := range keys {
		keys[i] = fmt.Sprintf("job-%04d", i)
		if err := os.Mkdir(filepath.Join(root, jobFolder(keys[i])), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	x, err := s.Summaries()
	if !errors.Is(err, ErrDamaged) || x == nil {
		t.Fatalf("err = %v, want ErrDamaged with a usable Summaries", err)
	}
	unread, read := 0, 0
	for _, k := range keys {
		if _, err := x.Of(k); err != nil {
			unread++
		} else {
			read++
		}
	}
	if unread != extra || read != MaxJobs {
		t.Fatalf("read %d, unread %d; want %d and %d", read, unread, MaxJobs, extra)
	}
}

// fixture saves a resume for each of the first withResume of jobs keys and
// returns all the keys.
func summariesFixture(tb testing.TB, jobs, withResume int) (*Store, []string) {
	tb.Helper()
	state := tb.TempDir()
	s, err := Open(state)
	if err != nil {
		tb.Fatal(err)
	}
	keys := make([]string, jobs)
	for i := range keys {
		keys[i] = fmt.Sprintf("gh:board%d:%d", i%7, i)
		if i < withResume {
			if _, _, err := s.Save(context.Background(), keys[i], goodMeta(), pdfBytes(keys[i])); err != nil {
				tb.Fatal(err)
			}
		}
	}
	return s, keys
}

// The numbers behind the walk: 500 jobs, 50 of which have a resume folder.
// PerJob is what a listing did (one Summary call per job); Walk is one
// Summaries call and a lookup per job.
func BenchmarkSummaryPerJob500Jobs50Folders(b *testing.B) {
	s, keys := summariesFixture(b, 500, 50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		for _, k := range keys {
			if sum, err := s.Summary(k); err == nil && sum.Versions > 0 {
				n++
			}
		}
		if n != 50 {
			b.Fatalf("%d summaries", n)
		}
	}
}

func BenchmarkSummariesWalk500Jobs50Folders(b *testing.B) {
	s, keys := summariesFixture(b, 500, 50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		x, err := s.Summaries()
		if err != nil {
			b.Fatal(err)
		}
		n := 0
		for _, k := range keys {
			if sum, err := x.Of(k); err == nil && sum.Versions > 0 {
				n++
			}
		}
		if n != 50 {
			b.Fatalf("%d summaries", n)
		}
	}
}
