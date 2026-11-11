package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobfacts"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// keepCounts is a readKeep's hits and misses, for "what did this step reuse".
type keepCounts struct{ factsHit, factsMiss, learnHit, learnMiss, manualHit, manualMiss int64 }

func countsOf(k *readKeep) keepCounts {
	return keepCounts{k.factsHits.Load(), k.factsMisses.Load(), k.learnHits.Load(), k.learnMisses.Load(),
		k.manualHits.Load(), k.manualMisses.Load()}
}

func (c keepCounts) minus(o keepCounts) keepCounts {
	return keepCounts{c.factsHit - o.factsHit, c.factsMiss - o.factsMiss, c.learnHit - o.learnHit,
		c.learnMiss - o.learnMiss, c.manualHit - o.manualHit, c.manualMiss - o.manualMiss}
}

// readKeepRig is a serve over sameFixture and the requests each step asks,
// every reply checked against a fresh command's.
type readKeepRig struct {
	t    *testing.T
	e    *env
	cfg  config.Config
	res  *jobsResident
	s    *jobsServer
	last string
}

func newReadKeepRig(t *testing.T) *readKeepRig {
	t.Helper()
	e, cfg := sameFixture(t)
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Close() })
	return &readKeepRig{t: t, e: e, cfg: cfg, res: res, s: newJobsServer(e, res, io.Discard)}
}

var readKeepAsks = []struct {
	op   string
	args []string
}{
	{"list", []string{"--view", "all"}},
	{"list", []string{"--view", "new", "--sort", "relevance"}},
	{"stats", []string{"--view", "new"}},
	{"gaps", []string{"--view", "all"}},
}

// check asks every request and fails unless serve answers as the command.
func (g *readKeepRig) check(step string) string {
	g.t.Helper()
	var all strings.Builder
	for _, a := range readKeepAsks {
		resp := g.s.runOp(step, serveOps[a.op], a.args)
		if !resp.OK {
			g.t.Fatalf("%s: %s %v: %+v", step, a.op, a.args, resp.Error)
		}
		if want := cliReply(g.t, g.e, serveOps[a.op], a.args...); string(resp.Result) != want {
			g.t.Fatalf("%s: %s %v: serve and the command differ: %s", step, a.op, a.args, firstDiff(string(resp.Result), want))
		}
		all.Write(resp.Result)
		all.WriteByte('\n')
	}
	return all.String()
}

// step checks and fails unless the replies changed from the last step's.
func (g *readKeepRig) changed(step string) {
	g.t.Helper()
	got := g.check(step)
	if got == g.last {
		g.t.Fatalf("%s: the replies did not change, so the step tested nothing", step)
	}
	g.last = got
}

// same checks and fails unless the replies are the last step's.
func (g *readKeepRig) same(step string) {
	g.t.Helper()
	if got := g.check(step); got != g.last {
		g.t.Fatalf("%s: an unchanged state answered differently: %s", step, firstDiff(got, g.last))
	}
}

func (g *readKeepRig) counted(step string, f func(string)) keepCounts {
	g.t.Helper()
	before := countsOf(&g.res.reads)
	f(step)
	return countsOf(&g.res.reads).minus(before)
}

// firstAsk is what the first request after a change reused: it is the one
// that must see the change (later ones may reuse what it rebuilt). f then
// checks every request.
func (g *readKeepRig) firstAsk(step string, f func(string)) keepCounts {
	g.t.Helper()
	before := countsOf(&g.res.reads)
	if resp := g.s.runOp(step, serveOps["list"], []string{"--view", "all"}); !resp.OK {
		g.t.Fatalf("%s: %+v", step, resp.Error)
	}
	c := countsOf(&g.res.reads).minus(before)
	f(step)
	return c
}

// residentEnv is an env over g's resident, as serve's requests run.
func (g *readKeepRig) residentEnv() *env {
	return &env{configPath: g.e.configPath, stateDir: g.e.stateDir, stdout: io.Discard, stderr: io.Discard, resident: g.res}
}

func (g *readKeepRig) saveCfg(c config.Config) {
	g.t.Helper()
	if err := config.Save(g.e.configPath, c); err != nil {
		g.t.Fatal(err)
	}
	g.cfg = c
}

// setManual records a current manual reading of key with slot set to value.
func (g *readKeepRig) setManual(key posting.Key, slot, value string) {
	g.t.Helper()
	snap, _, err := g.e.jobsState()
	if err != nil {
		g.t.Fatal(err)
	}
	r, ok := findRecord(snap, key)
	if !ok {
		g.t.Fatalf("setup: %s is not in the ledger", key)
	}
	slots := map[string]string{}
	for s, values := range jdtriage.SlotValues() {
		slots[s] = values[0]
	}
	slots[slot] = value
	if err := manualtriage.Set(manualtriage.Dir(g.e.stateDir), manualtriage.Reading{Key: r.Key, Slots: slots,
		Model: "test", At: jobsT0, Text: manualtriage.TextSHA256(r.Posting), Prompt: manualtriage.PromptSHA256("p")}); err != nil {
		g.t.Fatal(err)
	}
}

// twoValuesOfOneLength is a slot and two of its values of one length, so a
// reading can be rewritten in place at the same size.
func twoValuesOfOneLength(t *testing.T) (slot, a, b string) {
	t.Helper()
	for s, values := range jdtriage.SlotValues() {
		for i, x := range values {
			for _, y := range values[i+1:] {
				if len(x) == len(y) && x != values[0] && y != values[0] {
					return s, x, y
				}
			}
		}
	}
	t.Fatal("setup: no slot has two values of one length")
	return "", "", ""
}

// A warm serve reuses the facts map, what learn learned and the manual
// readings, and every reply is the command's; each change to what a kept part
// was built from rebuilds that part, and only that part.
func TestJobsServeKeepsTheReadAndAnswersAsTheCommand(t *testing.T) {
	g := newReadKeepRig(t)
	g.last = g.check("first") // derives facts: nothing kept yet
	g.same("second")          // needs no text: keeps the facts map
	c := g.counted("warm", g.same)
	if c.factsMiss != 0 || c.learnMiss != 0 || c.manualMiss != 0 || c.factsHit == 0 || c.learnHit == 0 || c.manualHit == 0 {
		t.Fatalf("warm, nothing changed: %+v", c)
	}

	// A mark on a live posting changes no text scope and no posting in view
	// "all": the facts map and what learn learned are reused, and the reply
	// still changes (the row shows its mark).
	mustCode(t, 0, "jobs", "mark", "gh:acme:06", "saved")
	if c := g.firstAsk("a live posting saved", g.changed); c.factsMiss != 0 || c.learnMiss != 0 || c.factsHit == 0 || c.learnHit == 0 {
		t.Fatalf("saving a live posting rebuilt the read: %+v", c)
	}
	mustCode(t, 0, "jobs", "mark", "gh:beta:07", "hidden")
	if c := g.firstAsk("a live posting hidden", g.changed); c.factsMiss != 0 || c.learnMiss != 0 {
		t.Fatalf("hiding a live posting rebuilt the read: %+v", c)
	}

	// A mark on a posting the text scope leaves out (a dropped one) brings it
	// into the scope: the facts map is built again, with its facts.
	appendPosting(t, g.e.stateDir, insightSeed{key: "gh:beta:90", company: "Beta", title: "Release Engineer",
		fit: "strong_fit", status: posting.Dropped, desc: "Ship Go releases. Location: Austin, TX.", seenAgo: 24 * time.Hour})
	g.changed("a dropped posting")
	g.same("the dropped posting again")
	mustCode(t, 0, "jobs", "mark", "gh:beta:90", "saved")
	if c := g.firstAsk("a mark brings it into scope", g.changed); c.factsHit != 0 || c.learnHit != 0 {
		t.Fatalf("a mark on a dropped posting reused what it changed: %+v", c)
	}
	g.same("the mark again")
	if c := g.counted("the mark, warm", g.same); c.factsMiss != 0 || c.learnMiss != 0 {
		t.Fatalf("the mark, warm: %+v", c)
	}
	// Cleared, the posting leaves the scope and the store forgets its facts;
	// marked again, the marks are as when the map was kept, but the store is
	// not: the read derives and files them again rather than reuse a map
	// whose facts the store no longer holds.
	// One request between the two: it prunes and keeps nothing (the store
	// changed under it), so the map kept under these marks is still there.
	held := g.res.store.Len()
	mustCode(t, 0, "jobs", "mark", "gh:beta:90", "clear")
	if resp := g.s.runOp("the mark cleared", serveOps["list"], []string{"--view", "all"}); !resp.OK {
		t.Fatalf("the mark cleared: %+v", resp.Error)
	}
	if g.res.store.Len() != held-1 {
		t.Fatalf("setup: the store holds %d facts after the clear, want %d", g.res.store.Len(), held-1)
	}
	mustCode(t, 0, "jobs", "mark", "gh:beta:90", "saved")
	g.same("marked again")
	if g.res.store.Len() != held {
		t.Fatalf("marked again: the store holds %d facts, want %d: a kept map stood in for a read", g.res.store.Len(), held)
	}

	// An exclusion at the same generation: learn reads the postings in view,
	// which the exclusion changes; one that reads descriptions reads every
	// text, so no facts map is reused while it is set.
	other := g.cfg
	other.Exclude.Companies = []string{"Gamma"}
	g.saveCfg(other)
	if c := g.firstAsk("an exclusion", g.changed); c.learnHit != 0 {
		t.Fatalf("an exclusion reused what learn learned before it: %+v", c)
	}
	g.same("the exclusion again")
	other.Exclude.Companies = nil
	other.Exclude.Descriptions = []string{"kubernetes"}
	g.saveCfg(other)
	if c := g.counted("an exclusion that reads descriptions", g.changed); c.factsHit != 0 {
		t.Fatalf("a facts map was reused while every text is read: %+v", c)
	}
	g.same("it again")
	other.Exclude.Descriptions = nil
	g.saveCfg(other)
	g.changed("no exclusion")
	g.same("no exclusion again")

	// The manual readings: appended, then rewritten in place at the same size
	// and modification time (only the change time tells), replaced by a
	// rename, deleted.
	slot, a, b := twoValuesOfOneLength(t)
	g.setManual("gh:gamma:05", slot, a)
	if c := g.counted("a reading appended", g.changed); c.manualMiss == 0 {
		t.Fatalf("an appended reading was not read: %+v", c)
	}
	if c := g.counted("the reading again", g.same); c.manualMiss != 0 || c.manualHit == 0 {
		t.Fatalf("the reading again: %+v", c)
	}
	file := manualtriage.File(manualtriage.Dir(g.e.stateDir))
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	quoted := func(s string) []byte { return []byte(fmt.Sprintf("%q:%q", slot, s)) }
	if bytes.Count(data, quoted(a)) != 1 {
		t.Fatalf("setup: %s is not in the readings once", quoted(a))
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Replace(data, quoted(a), quoted(b), 1)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if fi2, err := os.Stat(file); err != nil || fi2.Size() != fi.Size() || !fi2.ModTime().Equal(fi.ModTime()) {
		t.Fatalf("setup: the rewrite changed the size or time: %v", err)
	}
	if c := g.firstAsk("a reading rewritten in place", g.changed); c.manualMiss != 1 {
		t.Fatalf("a reading rewritten in place was not read again: %+v", c)
	}
	g.setManual("gh:gamma:05", slot, a)
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	g.changed("a reading appended again")
	tmp := file + ".new"
	if err := os.WriteFile(tmp, bytes.ReplaceAll(data, quoted(a), quoted(b)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, file); err != nil {
		t.Fatal(err)
	}
	g.changed("the readings replaced by a rename")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	g.changed("the readings deleted")
	g.same("no readings again")
}

// A facts map whose save failed (another writer held the store's lock) is not
// kept: the next request saves what the failed one could not, so the store on
// disk ends up with the new posting's facts.
func TestJobsServeKeepsNoFactsMapWithAnUnsavedStore(t *testing.T) {
	g := newReadKeepRig(t)
	g.check("first")
	g.check("second")
	lock, err := filelock.TryAcquire(g.e.jobsFactsPath() + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	appendPosting(t, g.e.stateDir, insightSeed{key: "gh:acme:91", company: "Acme", title: "Build Engineer",
		fit: "strong_fit", status: posting.Digested, desc: "Go builds. Location: Austin, TX.", seenAgo: 24 * time.Hour})
	g.check("a new posting, the store locked") // derives, cannot save
	g.check("again, the store still locked")   // derives nothing, cannot save: keeps nothing
	lock.Release()
	// Serve alone: a command run would save the store file itself.
	if resp := g.s.runOp("the lock released", serveOps["list"], []string{"--view", "all"}); !resp.OK {
		t.Fatalf("the lock released: %+v", resp.Error)
	}
	disk :=jobfacts.Open(g.e.jobsFactsPath(), jobfacts.Version(readKeepVocab(t)))
	if disk.Len() != g.res.store.Len() {
		t.Fatalf("the store on disk holds %d facts, the resident one %d: a kept map stood in for a save", disk.Len(), g.res.store.Len())
	}
}

// Readings that cannot be read are never reused: each request warns, as the
// command does, and readings that can be read again are read.
func TestJobsServeUnreadableReadingsWarnEveryRequest(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	g := newReadKeepRig(t)
	g.setManual("gh:gamma:05", "role_fit", "partial_fit")
	g.last = g.check("a reading")
	g.same("the reading again")
	file := manualtriage.File(manualtriage.Dir(g.e.stateDir))
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o600) })
	for i := range 2 {
		resp := g.s.runOp("unreadable", serveOps["list"], []string{"--view", "all"})
		if !resp.OK || !strings.Contains(string(resp.Result), "manual Sonnet readings not used") {
			t.Fatalf("request %d with unreadable readings: ok %v, no warning in %.300s", i, resp.OK, resp.Result)
		}
	}
	g.changed("unreadable readings")
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	g.changed("readable again")
}

// Concurrent requests share the kept parts while a writer changes marks and
// readings; afterwards serve still answers as the command. Run with -race.
func TestJobsServeReadKeepConcurrent(t *testing.T) {
	g := newReadKeepRig(t)
	g.check("warm")
	g.check("warm again")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan error, 8)
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				a := readKeepAsks[w%len(readKeepAsks)]
				if resp := g.s.runOp("race", serveOps[a.op], a.args); !resp.OK {
					errs <- fmt.Errorf("%s %v: %+v", a.op, a.args, resp.Error)
					return
				}
			}
		}()
	}
	for i := range 6 {
		key := posting.Key(fmt.Sprintf("gh:acme:%02d", i*3))
		if err := manualtriageSetFor(g, key); err != nil {
			t.Fatal(err)
		}
		mustCode(t, 0, "jobs", "mark", fmt.Sprintf("gh:beta:%02d", 1+i*3), "saved")
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	g.check("after the writers")
}

func readKeepVocab(t *testing.T) *jobinsight.Vocab {
	t.Helper()
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func manualtriageSetFor(g *readKeepRig, key posting.Key) error {
	snap, _, err := g.e.jobsState()
	if err != nil {
		return err
	}
	r, ok := findRecord(snap, key)
	if !ok {
		return fmt.Errorf("%s is not in the ledger", key)
	}
	slots := map[string]string{}
	for s, values := range jdtriage.SlotValues() {
		slots[s] = values[len(values)-1]
	}
	return manualtriage.Set(manualtriage.Dir(g.e.stateDir), manualtriage.Reading{Key: r.Key, Slots: slots,
		Model: "test", At: jobsT0, Text: manualtriage.TextSHA256(r.Posting), Prompt: manualtriage.PromptSHA256("p")})
}

// A stamp changes with every change a reader could miss: a rewrite in place at
// the same size and modification time, a rename over the file, a mode change,
// a delete; and not when nothing changed.
func TestFileStampSeesEveryChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if st, ok := stampOf(path); !ok || !st.missing {
		t.Fatalf("missing file: %+v ok %v", st, ok)
	}
	if err := os.WriteFile(path, []byte("aaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := func() fileStamp {
		t.Helper()
		st, ok := stampOf(path)
		if !ok {
			t.Fatal("no stamp on this platform")
		}
		return st
	}
	first := stamp()
	if again := stamp(); again != first {
		t.Fatal("an unchanged file stamped differently")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bbbb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	inPlace := stamp()
	if inPlace.size != first.size || inPlace.mtime != first.mtime || inPlace.ino != first.ino {
		t.Fatal("setup: the rewrite changed more than the change time")
	}
	if inPlace == first {
		t.Fatal("a rewrite at the same size and time stamped the same")
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte("cccc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	renamed := stamp()
	if renamed == inPlace {
		t.Fatal("a rename over the file stamped the same")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if stamp() == renamed {
		t.Fatal("a mode change stamped the same")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if st := stamp(); !st.missing {
		t.Fatal("a deleted file did not stamp missing")
	}
}

// learn keeps nothing a posting was noted missing in: the next request must
// note it again.
func TestLearnKeepsNothingNotedMissing(t *testing.T) {
	g := newReadKeepRig(t)
	g.check("first")
	g.check("second")
	rd, err := g.residentEnv().jobsListRead()
	if err != nil {
		t.Fatal(err)
	}
	if rd.facts.kept == nil {
		t.Fatal("setup: the facts map was not kept")
	}
	// A copy of the kept facts without one lean posting's, under the kept
	// identity, so learn looks it up and finds it missing.
	var lean ledger.Record
	for _, r := range rd.snap.Records {
		if r.BodyEvicted && inLearnView(r, rd.m) {
			lean = r
			break
		}
	}
	if lean.Key == "" {
		t.Fatal("setup: no lean posting in view")
	}
	m := make(map[posting.Key]jobfacts.Facts, len(rd.facts.m))
	for k, f := range rd.facts.m {
		if k != lean.Key {
			m[k] = f
		}
	}
	missing := &keptFacts{key: rd.facts.kept.key, m: m, served: rd.facts.kept.served}
	tf := &textFacts{m: m, kept: missing}
	in := &insight{res: g.res, gen: rd.gen, facts: tf, vocab: readKeepVocab(t)}
	before := countsOf(&g.res.reads)
	in.learn(rd.snap, rd.m, "")
	if tf.err() == nil {
		t.Fatal("setup: learn noted nothing missing")
	}
	again := &insight{res: g.res, gen: rd.gen, facts: &textFacts{m: m, kept: missing}, vocab: readKeepVocab(t)}
	again.learn(rd.snap, rd.m, "")
	if again.facts.err() == nil {
		t.Fatal("a second learn over the same facts reused what noted the posting missing")
	}
	if c := countsOf(&g.res.reads).minus(before); c.learnHit != 0 {
		t.Fatalf("learn reused a learn that noted a posting missing: %+v", c)
	}
}
