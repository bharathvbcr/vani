package inbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func job(n int) Job {
	return Job{Title: fmt.Sprintf("Engineer %d", n), Company: "Acme", Location: "Remote",
		URL: fmt.Sprintf("https://careers.acme.com/jobs/%d", n)}
}

func batchOf(token string, jobs ...Job) Batch {
	return Batch{Token: token, Company: "Acme", Page: Page{URL: "https://careers.acme.com/search", Title: "Jobs"}, Jobs: jobs}
}

func TestParseRequestMalformed(t *testing.T) {
	big := `{"page":{"url":"https://a.example/"},"jobs":[{"title":"t","url":"https://a.example/1","snippet":"` + strings.Repeat("x", MaxStdinBytes) + `"}]}`
	manyJobs := `{"jobs":[` + strings.Repeat(`{"title":"t","url":"https://a.example/1"},`, MaxJobs) + `{"title":"t","url":"https://a.example/1"}]}`
	for name, in := range map[string]string{
		"empty":                "",
		"not json":             "hello",
		"truncated":            `{"jobs":[`,
		"wrong type":           `{"jobs":"x"}`,
		"trailing data":        `{"jobs":[{"title":"t","url":"https://a.example/1"}]} {}`,
		"no jobs":              `{"page":{"url":"https://a.example/"},"jobs":[]}`,
		"missing jobs":         `{"page":{"url":"https://a.example/"}}`,
		"too many jobs":        manyJobs,
		"over 1 MiB":           big,
		"page url too long":    `{"page":{"url":"https://a.example/` + strings.Repeat("a", MaxPageURL) + `"},"jobs":[{"title":"t","url":"https://a.example/1"}]}`,
		"page title too long":  `{"page":{"url":"https://a.example/","title":"` + strings.Repeat("t", MaxPageTitle+1) + `"},"jobs":[{"title":"t","url":"https://a.example/1"}]}`,
		"save over http":       `{"page":{"url":"http://a.example/"},"save_search":true,"jobs":[{"title":"t","url":"https://a.example/1"}]}`,
		"save with no page":    `{"save_search":true,"jobs":[{"title":"t","url":"https://a.example/1"}]}`,
		"save with javascript": `{"page":{"url":"javascript:alert(1)"},"save_search":true,"jobs":[{"title":"t","url":"https://a.example/1"}]}`,
	} {
		if _, err := ParseRequest([]byte(in)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestParseRequestAcceptsHTTPPageWithoutSave(t *testing.T) {
	r, err := ParseRequest([]byte(`{"page":{"url":"http://a.example/x"},"future_field":1,"jobs":[{"title":"t","url":"https://a.example/1"}]}`))
	if err != nil || len(r.Jobs) != 1 {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestMalformedErrorsDoNotQuoteInput(t *testing.T) {
	_, err := ParseRequest([]byte(`{"page":{"url":"SECRET-PAGE-URL"},"jobs":"SECRET-TITLE"}`))
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v, want one that quotes nothing", err)
	}
}

func TestValidateRejectsPerJobAndKeepsTheRest(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	jobs := []Job{
		job(1),                                  // 0 ok
		{Title: "", URL: "https://a.example/1"}, // 1
		{Title: long(MaxTitle + 1), URL: "https://a.example/2"},                   // 2
		{Title: "t", Company: long(MaxCompany + 1), URL: "https://a.example/3"},   // 3
		{Title: "t", Location: long(MaxLocation + 1), URL: "https://a.example/4"}, // 4
		{Title: "t", URL: ""},                                                   // 5
		{Title: "t", URL: "ftp://a.example/1"},                                  // 6
		{Title: "t", URL: "javascript:alert(1)"},                                // 7
		{Title: "t", URL: "https://a.example/" + long(MaxURL)},                  // 8
		{Title: "t", URL: "https://a.example/9", Snippet: long(MaxSnippet + 1)}, // 9
		{Title: "t", URL: "https://a.example/10", Posted: long(MaxPosted + 1)},  // 10
		{Title: "   ", URL: "https://a.example/11"},                             // 11
		job(2), // 12 ok
	}
	got, dups, rej := Request{Jobs: jobs}.Validate()
	if len(got) != 2 || dups != 0 {
		t.Fatalf("valid %d dups %d, want 2 and 0", len(got), dups)
	}
	if len(rej) != 11 {
		t.Fatalf("%d rejections, want 11: %+v", len(rej), rej)
	}
	for _, r := range rej {
		if r.Index == 0 || r.Index == 12 || r.Reason == "" {
			t.Errorf("bad rejection %+v", r)
		}
		if strings.Contains(r.Reason, "a.example") {
			t.Errorf("reason quotes the job: %q", r.Reason)
		}
	}
	if rej[6].Reason != "url is not http(s)" {
		t.Errorf("reason %q", rej[6].Reason)
	}
}

func TestValidateAtTheBounds(t *testing.T) {
	j := Job{Title: strings.Repeat("t", MaxTitle), Company: strings.Repeat("c", MaxCompany), Location: strings.Repeat("l", MaxLocation),
		URL: "https://a.example/" + strings.Repeat("u", MaxURL-len("https://a.example/")), Snippet: strings.Repeat("s", MaxSnippet), Posted: strings.Repeat("p", MaxPosted)}
	got, _, rej := Request{Jobs: []Job{j}}.Validate()
	if len(got) != 1 || len(rej) != 0 {
		t.Fatalf("job exactly at every bound: %d valid, %+v", len(got), rej)
	}
}

func TestValidateCountsCanonicalDuplicatesOnce(t *testing.T) {
	a := Job{Title: "A", URL: "https://Careers.Acme.com/jobs/1/?utm_source=x#frag"}
	b := Job{Title: "A again", URL: "https://careers.acme.com/jobs/1"}
	c := Job{Title: "C", URL: "https://careers.acme.com/jobs/2"}
	got, dups, rej := Request{Jobs: []Job{a, b, c}}.Validate()
	if len(got) != 2 || dups != 1 || len(rej) != 0 {
		t.Fatalf("valid %d dups %d rej %v", len(got), dups, rej)
	}
	if got[0].URL != "https://careers.acme.com/jobs/1" {
		t.Errorf("url %q is not canonical", got[0].URL)
	}
}

func TestPostingsKeyAndFields(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	b := batchOf("careers-acme-com", job(1),
		Job{Title: "Dated", URL: "https://careers.acme.com/jobs/2", Posted: "2026-10-01T00:00:00Z", Snippet: "text"},
		Job{Title: "Loose date", URL: "https://careers.acme.com/jobs/3", Posted: "3 days ago", Company: ""})
	b.ReceivedAt = at
	ps := b.Postings()
	if len(ps) != 3 {
		t.Fatalf("%d postings", len(ps))
	}
	want, _ := posting.PageKey("careers-acme-com", "https://careers.acme.com/jobs/1")
	if ps[0].Key != want || ps[0].ATS != posting.Page || !ps[0].SeenAt.Equal(at) {
		t.Errorf("posting %+v", ps[0])
	}
	if ps[1].PostedAt.IsZero() || ps[1].Description != "text" || ps[1].Company != "Acme" {
		t.Errorf("posting %+v", ps[1])
	}
	if !ps[2].PostedAt.IsZero() {
		t.Errorf("a non-RFC3339 posted date was parsed: %v", ps[2].PostedAt)
	}
}

func TestWriteRoundTripAndRefusals(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inbox")
	path, err := Write(dir, batchOf("careers-acme-com", job(1)))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	b, err := ParseBatch(data)
	if err != nil || b.V != Version || b.ReceivedAt.IsZero() || len(b.Jobs) != 1 {
		t.Fatalf("%+v, %v", b, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	for name, bad := range map[string]Batch{
		"no jobs":   batchOf("careers-acme-com"),
		"bad token": batchOf("Not A Slug", job(1)),
		"colon":     batchOf("a:b", job(1)),
		"bad job":   batchOf("careers-acme-com", Job{Title: "", URL: "https://a.example/1"}),
		"dup jobs":  batchOf("careers-acme-com", job(1), job(1)),
		"too many":  batchOf("careers-acme-com", manyJobs(MaxJobs+1)...),
	} {
		if _, err := Write(dir, bad); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("%d entries in the spool, want only the one good batch", len(es))
	}
}

func manyJobs(n int) []Job {
	out := make([]Job, n)
	for i := range out {
		out[i] = job(i)
	}
	return out
}

func collect(dir string, limit int) ([]Batch, DrainReport, error) {
	var got []Batch
	rep, err := Drain(context.Background(), dir, limit, func(b Batch) error { got = append(got, b); return nil })
	return got, rep, err
}

func TestDrainOldestFirstAndRemovesAfterFold(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		if _, err := Write(dir, batchOf("careers-acme-com", job(i))); err != nil {
			t.Fatal(err)
		}
	}
	got, rep, err := collect(dir, MaxDrainFiles)
	if err != nil || rep.Folded != 3 || rep.Jobs != 3 {
		t.Fatalf("%+v, %v", rep, err)
	}
	for i, b := range got {
		if b.Jobs[0].Title != fmt.Sprintf("Engineer %d", i+1) {
			t.Errorf("batch %d is %q: not oldest first", i, b.Jobs[0].Title)
		}
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 || es[0].Name() != lockName {
		t.Errorf("spool not empty after drain: %v", es)
	}
	if _, rep, err := collect(dir, 5); err != nil || rep.Folded != 0 {
		t.Errorf("second drain %+v, %v", rep, err)
	}
}

func TestDrainBoundsFilesPerDrain(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if _, err := Write(dir, batchOf("careers-acme-com", job(i))); err != nil {
			t.Fatal(err)
		}
	}
	_, rep, err := collect(dir, 2)
	if err != nil || rep.Folded != 2 || rep.Remaining != 3 {
		t.Fatalf("%+v, %v", rep, err)
	}
	if _, err := Drain(context.Background(), dir, 0, nil); err == nil {
		t.Error("a limit of 0 was accepted")
	}
}

func TestDrainKeepsAFileWhenFoldFails(t *testing.T) {
	dir := t.TempDir()
	path, _ := Write(dir, batchOf("careers-acme-com", job(1)))
	boom := errors.New("ledger down")
	rep, err := Drain(context.Background(), dir, 5, func(Batch) error { return boom })
	if !errors.Is(err, boom) || rep.Folded != 0 {
		t.Fatalf("%+v, %v", rep, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file lost after a failed fold: %v", err)
	}
	if _, rep, err := collect(dir, 5); err != nil || rep.Folded != 1 {
		t.Fatalf("retry %+v, %v", rep, err)
	}
}

func TestDrainMovesUnparseableFilesAsideAndNeverRetriesThem(t *testing.T) {
	dir := t.TempDir()
	good, _ := Write(dir, batchOf("careers-acme-com", job(1)))
	put := func(name, body string) { t.Helper(); mustWrite(t, filepath.Join(dir, name), body) }
	put("00000000000000000001-garbage.json", "not json at all")
	put("00000000000000000002-empty.json", "")
	put("00000000000000000003-badjob.json", `{"v":1,"received_at":"2026-10-19T00:00:00Z","token":"x","jobs":[{"title":"","url":"https://a.example/1"}]}`)
	put("00000000000000000004-version.json", `{"v":9,"received_at":"2026-10-19T00:00:00Z","token":"x","jobs":[{"title":"t","url":"https://a.example/1"}]}`)
	put("00000000000000000005-huge.json", strings.Repeat("x", MaxBatchBytes+1))
	put("00000000000000000006-note.txt", "ignored: not .json")
	put(".hidden.json", "ignored: a temp file")

	calls := 0
	rep, err := Drain(context.Background(), dir, MaxDrainFiles, func(Batch) error { calls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || rep.Folded != 1 || len(rep.Bad) != 5 {
		t.Fatalf("calls %d, report %+v", calls, rep)
	}
	for _, b := range rep.Bad {
		if _, err := os.Stat(filepath.Join(dir, BadDir, b.Name)); err != nil {
			t.Errorf("%s not in bad/: %v", b.Name, err)
		}
		if _, err := os.Stat(filepath.Join(dir, b.Name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still in the spool", b.Name)
		}
	}
	if _, err := os.Stat(good); !errors.Is(err, os.ErrNotExist) {
		t.Error("the good batch was not removed")
	}
	// Ignored files stay, and a second drain neither folds nor reports the bad ones.
	for _, n := range []string{"00000000000000000006-note.txt", ".hidden.json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was touched: %v", n, err)
		}
	}
	rep, err = Drain(context.Background(), dir, MaxDrainFiles, func(Batch) error { calls++; return nil })
	if err != nil || calls != 1 || len(rep.Bad) != 0 || rep.Folded != 0 {
		t.Fatalf("second drain: calls %d, %+v, %v", calls, rep, err)
	}
}

func TestDrainHonoursCancellation(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, batchOf("careers-acme-com", job(1))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := Drain(ctx, dir, 5, func(Batch) error { calls++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("err %v, %d calls", err, calls)
	}
	if es, _ := collectNames(dir); len(es) != 1 {
		t.Errorf("batch lost on cancel: %v", es)
	}
}

// Two drains of one directory never fold a file twice: one gets ErrBusy while
// the other holds the drain lock, and every file is folded exactly once.
func TestConcurrentDrainsFoldEachBatchOnce(t *testing.T) {
	dir := t.TempDir()
	const n = 12
	for i := 0; i < n; i++ {
		if _, err := Write(dir, batchOf("careers-acme-com", job(i))); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	folded := map[string]int{}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fn := func(b Batch) error {
		once.Do(func() { close(entered); <-release })
		mu.Lock()
		folded[b.Jobs[0].Title]++
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = Drain(context.Background(), dir, MaxDrainFiles, fn)
	}()
	<-entered
	if _, err := Drain(context.Background(), dir, MaxDrainFiles, fn); !errors.Is(err, ErrBusy) {
		t.Errorf("second drain err = %v, want ErrBusy", err)
	}
	close(release)
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	if len(folded) != n {
		t.Fatalf("%d distinct batches folded, want %d", len(folded), n)
	}
	for k, c := range folded {
		if c != 1 {
			t.Errorf("%s folded %d times", k, c)
		}
	}
	// And a drain that starts after the first finished runs normally.
	if _, rep, err := collect(dir, 5); err != nil || rep.Folded != 0 {
		t.Errorf("after: %+v, %v", rep, err)
	}
}

// A file another process removes between the listing and the read is skipped.
func TestDrainSkipsAVanishedFile(t *testing.T) {
	dir := t.TempDir()
	a, _ := Write(dir, batchOf("careers-acme-com", job(1)))
	if _, err := Write(dir, batchOf("careers-acme-com", job(2))); err != nil {
		t.Fatal(err)
	}
	first := true
	rep, err := Drain(context.Background(), dir, 5, func(Batch) error {
		if first { // the second file disappears while the first is being folded
			first = false
			es, _ := collectNames(dir)
			for _, n := range es {
				if p := filepath.Join(dir, n); p != a {
					_ = os.Remove(p)
				}
			}
		}
		return nil
	})
	if err != nil || rep.Folded != 1 {
		t.Fatalf("%+v, %v", rep, err)
	}
}

func collectNames(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, err
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
