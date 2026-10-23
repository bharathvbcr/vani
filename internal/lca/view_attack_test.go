//go:build unix

package lca

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// --- 1. parity across imports, non-contiguous years, odd queries -----------

// The window is the index's own last three covered years (nothing reads the
// wall clock), so it must follow the index across imports.
func TestAttackWindowFollowsIndexAcrossImports(t *testing.T) {
	dir := t.TempDir()
	steps := [][]map[int]int{
		{{2022: 1, 2023: 2}},
		{{2022: 1, 2023: 2}, {2024: 4}},
		{{2022: 1, 2023: 2}, {2024: 4}, {2025: 8}},            // 4 years: 2022 falls out
		{{2022: 1, 2023: 2}, {2024: 4}, {2025: 8}, {2031: 9}}, // a far-future year
		{{2019: 5}, {2021: 6}, {2025: 7}},                     // non-contiguous
		{{2019: 5}, {2021: 6}, {2025: 7}, {2026: 1}, {2027: 2}},
	}
	for i, st := range steps {
		var srcs []map[string]map[int]int
		for _, fyc := range st {
			m := map[string]map[int]int{"ALPHA": {}, "BETA GAMMA": {}}
			for fy, n := range fyc {
				m["ALPHA"][fy] = n
				m["BETA GAMMA"][fy] = n + 1
			}
			srcs = append(srcs, m)
		}
		path := saveIndex(t, dir, srcs...)
		x, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		v, info, err := LoadView(path)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && !info.Rebuilt {
			t.Fatalf("step %d: changed index but view not rebuilt", i)
		}
		checkParity(t, x, v, []string{"Alpha", "Beta Gamma", "Beta", "x"})
		if !reflect.DeepEqual(v.Coverage(), lookupWindow(x.Coverage())) {
			t.Fatalf("step %d: view window %v, index window %v", i, v.Coverage(), lookupWindow(x.Coverage()))
		}
		v2, info, _ := LoadView(path)
		if info.Rebuilt {
			t.Fatalf("step %d: second load rebuilt", i)
		}
		checkParity(t, x, v2, []string{"Alpha", "Beta Gamma", "Beta", "x"})
	}
}

func TestAttackNoWallClockInLCA(t *testing.T) {
	ents, _ := os.ReadDir(".")
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, "_test.go") || !strings.HasSuffix(n, ".go") {
			continue
		}
		b, _ := os.ReadFile(n)
		if bytes.Contains(b, []byte("time.Now")) {
			t.Errorf("%s reads the wall clock: view window would depend on when it was built", n)
		}
	}
}

func attackQueries() []string {
	nfc := "Café Zürich"
	nfd := "Café Zürich"
	qs := []string{
		"", " ", "\t\n", "\x00", "A\x00B", "\x00\x00 \x00", "Alpha\x00", "ALPHA\x00 BETA",
		"\xff\xfe", "Alpha\xff", "Alpha \xc3", "\xed\xa0\x80 Alpha", // lone surrogate encoding
		nfc, nfd, "CAFÉ ZÜRICH", "café zürich", "Cafe Zurich",
		"İstanbul Holdings", "istanbul holdings", "ISTANBUL HOLDINGS", "İSTANBUL HOLDINGS", "ıstanbul holdings",
		"Straße Werke", "STRASSE WERKE", "strasse werke", "ǅ Holdings", "ﬁrst ﬂight", "FIRST FLIGHT",
		"Alpha Inc.", "Alpha, Inc", "Alpha LLC", "Alpha L.L.C.", "Alpha Co", "Alpha Corp.", "Alpha Inc Inc", "Alpha LLC Inc",
		"A.L.P.H.A.", "Alpha-Beta", "Alpha & Beta", "Alpha and Beta", "ALPHA   BETA", "The Alpha", "The The Alpha",
		"Alpha Beta Gamma Delta", "Alpha Beta Gamma", "Alpha Beta", "Alpha", "Alp", "Beta", "Beta Gamma",
		"O'Brien Co", "O’Brien Co", "OBRIEN",
		"日本", "日本 電気", "日本　電気",
		strings.Repeat("A", 1<<20), strings.Repeat("A ", 1<<19), strings.Repeat("\x00", 1<<20),
		strings.Repeat("\xff", 1<<20), "Alpha " + strings.Repeat("Inc ", 1<<18),
	}
	return qs
}

func attackSources() []map[string]map[int]int {
	names := []string{
		"ALPHA", "ALPHA BETA", "ALPHA BETA GAMMA", "ALPHA BETA DELTA", "BETA", "BETA GAMMA",
		"ALPHA AND BETA", "ALPHA BETA INC", "ALPHABETA", "OBRIEN", "OBRIEN HOLDINGS",
		"日本 電気", "日本 電気 HOLDINGS", "日本",
		"CAFÉ ZÜRICH", "CAFÉ ZÜRICH", "CAFE ZURICH", "CAFÉ ZÜRICH", // NFC and NFD as distinct keys
		"İSTANBUL HOLDINGS", "ISTANBUL HOLDINGS", "ıSTANBUL HOLDINGS", "İSTANBUL HOLDINGS",
		"STRASSE WERKE", "STRASSE WERKE GMBH", "STRAßE WERKE", "FIRST FLIGHT", "ﬁRST FLIGHT",
		"ǅ HOLDINGS", "Ǆ HOLDINGS",
	}
	m := map[string]map[int]int{}
	for i, n := range names {
		m[n] = map[int]int{2023: i + 1, 2024: 2*i + 1, 2025: 3*i + 1}
		if k := Normalize(n); k != "" { // plus the normalised key, as an import would store
			m[k] = map[int]int{2025: 100 + i}
		}
	}
	return []map[string]map[int]int{m}
}

func TestAttackLookupParityAdversarialQueries(t *testing.T) {
	path := saveIndex(t, t.TempDir(), attackSources()...)
	x, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := LoadView(path)
	if err != nil {
		t.Fatal(err)
	}
	v2, info, _ := LoadView(path) // from the file
	if info.Rebuilt {
		t.Fatal("second load rebuilt")
	}
	qs := attackQueries()
	// also every key's Normalize round trip and each key as raw query
	for k := range x.merged {
		qs = append(qs, k, k+" Inc", strings.ToLower(k))
	}
	for _, vv := range []*View{v, v2} {
		for _, q := range qs {
			if !reflect.DeepEqual(x.Lookup(q), vv.Lookup(q)) {
				t.Errorf("Lookup(%.40q) differs: index %+v view %+v", q, x.Lookup(q), vv.Lookup(q))
			}
		}
	}
}

// --- 2. binding -------------------------------------------------------------

func TestAttackSameSizeDifferentContent(t *testing.T) {
	dir := t.TempDir()
	path := saveIndex(t, dir, map[string]map[int]int{"ALPHA": {2025: 10}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	size1 := mustSize(t, path)
	view1, _ := os.ReadFile(ViewPath(path))
	saveIndex(t, dir, map[string]map[int]int{"ALPHA": {2025: 20}})
	if mustSize(t, path) != size1 {
		t.Fatal("setup: sizes differ")
	}
	// preserve mtime of the old view too: nothing may depend on mtimes
	v, info, err := LoadView(path)
	if err != nil || !info.Rebuilt || v.Lookup("Alpha").Total != 20 {
		t.Fatalf("%+v %v total=%d", info, err, v.Lookup("Alpha").Total)
	}
	if bytes.Equal(view1, mustRead(t, ViewPath(path))) {
		t.Fatal("view file not rewritten")
	}
}

func mustSize(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An index that flips between two versions while LoadView runs: every returned
// view and every view file left on disk must be exactly one version's, bound to
// that version's own bytes.
func TestAttackIndexReplacedDuringLoadView(t *testing.T) {
	dir := t.TempDir()
	pa := saveIndex(t, filepath.Join(dir, "a"), map[string]map[int]int{"ALPHA": {2025: 111}, "ONLY A": {2025: 1}})
	pb := saveIndex(t, filepath.Join(dir, "b"), map[string]map[int]int{"ALPHA": {2025: 222}, "ONLY B": {2025: 1}})
	A, B := mustRead(t, pa), mustRead(t, pb)
	live := filepath.Join(dir, "live", "index.json")
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	sa, sb := sumOf(A), sumOf(B)
	xa, _ := Load(pa)
	xb, _ := Load(pb)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tmp := live + ".swap"
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			b := A
			if i%2 == 1 {
				b = B
			}
			if err := os.WriteFile(tmp, b, 0o600); err != nil {
				return
			}
			_ = os.Rename(tmp, live)
		}
	}()
	deadline := time.Now().Add(4 * time.Second)
	loads := 0
	for time.Now().Before(deadline) {
		if _, err := os.Stat(live); err != nil {
			continue
		}
		v, _, err := LoadView(live)
		if err != nil {
			t.Fatalf("LoadView: %v", err)
		}
		loads++
		switch v.Lookup("Alpha").Total {
		case 111:
			checkParity(t, xa, v, []string{"Alpha", "Only A", "Only B"})
		case 222:
			checkParity(t, xb, v, []string{"Alpha", "Only A", "Only B"})
		default:
			t.Fatalf("view answers %d: neither version", v.Lookup("Alpha").Total)
		}
		// the file on disk, if present and readable, is bound to a real version
		for _, c := range []struct {
			sum string
			x   *Index
			raw []byte
		}{{sa, xa, A}, {sb, xb, B}} {
			if vv, err := readView(ViewPath(live), c.sum, int64(len(c.raw))); err == nil {
				checkParity(t, c.x, vv, []string{"Alpha", "Only A", "Only B"})
			}
		}
		raw, err := os.ReadFile(ViewPath(live))
		if err != nil {
			continue
		}
		h, _, _ := splitViewBytes(raw)
		if h.IndexSHA256 != sa && h.IndexSHA256 != sb {
			t.Fatalf("view bound to a sha that is neither index version: %s", h.IndexSHA256)
		}
		// the binding must be honest: a view whose header names A holds A's data
		want := xa
		if h.IndexSHA256 == sb {
			want = xb
		}
		vv, err := readView(ViewPath(live), h.IndexSHA256, h.IndexBytes)
		if err == nil {
			checkParity(t, want, vv, []string{"Alpha", "Only A", "Only B"})
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("%d LoadViews under index flipping", loads)
}

func sumOf(b []byte) string { return sumHex(b) }

func splitViewBytes(data []byte) (viewHeader, []byte, error) {
	var h viewHeader
	line, body, _ := bytes.Cut(data, []byte{'\n'})
	err := json.Unmarshal(line, &h)
	return h, body, err
}

func TestAttackIndexIsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := saveIndex(t, filepath.Join(dir, "real"), map[string]map[int]int{"ALPHA": {2025: 5}})
	link := filepath.Join(dir, "state", "index.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	v, info, err := LoadView(link)
	if err != nil || v.Lookup("Alpha").Total != 5 {
		t.Fatalf("%+v %v", info, err)
	}
	// The view lands beside the link, not beside the target.
	if _, err := os.Stat(ViewPath(link)); err != nil {
		t.Errorf("no view beside the link: %v", err)
	}
	if _, err := os.Stat(ViewPath(real)); err == nil {
		t.Errorf("view written beside the symlink target")
	}
	// Retarget the link to a different index: the view must not follow along.
	real2 := saveIndex(t, filepath.Join(dir, "real2"), map[string]map[int]int{"ALPHA": {2025: 6}})
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real2, link); err != nil {
		t.Fatal(err)
	}
	if v, _, err = LoadView(link); err != nil || v.Lookup("Alpha").Total != 6 {
		t.Fatalf("after retarget: %v %v", v, err)
	}
	// A dangling link is an absent index.
	if err := os.Remove(real2); err != nil {
		t.Fatal(err)
	}
	if _, _, err = LoadView(link); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dangling link: %v", err)
	}
}

func TestAttackIndexMissingViewPresent(t *testing.T) {
	path := saveIndex(t, t.TempDir(), map[string]map[int]int{"ALPHA": {2025: 5}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if v, _, err := LoadView(path); !errors.Is(err, fs.ErrNotExist) || v != nil {
		t.Fatalf("served a view with no index: %v %v", v, err)
	}
}

func TestAttackViewNewerVersion(t *testing.T) {
	path := saveIndex(t, t.TempDir(), map[string]map[int]int{"ALPHA": {2025: 5}})
	if _, _, err := LoadView(path); err != nil {
		t.Fatal(err)
	}
	h, body, _ := splitViewBytes(mustRead(t, ViewPath(path)))
	for _, ver := range []int{ViewVersion + 1, 0, -1, 1 << 30} {
		h.Version = ver
		raw := joinView(t, h, body)
		if err := os.WriteFile(ViewPath(path), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		sum, size := mustSum(t, path)
		if _, err := readView(ViewPath(path), sum, size); err == nil {
			t.Fatalf("version %d accepted", ver)
		}
		v, info, err := LoadView(path)
		if err != nil || !info.Rebuilt || info.Routine() || v.Lookup("Alpha").Total != 5 {
			t.Fatalf("version %d: %+v %v", ver, info, err)
		}
	}
}

// --- 3. fuzz ----------------------------------------------------------------

var fuzzEnv struct {
	once      sync.Once
	dir       string
	sum       string
	size      int64
	seed      []byte
	header    viewHeader
	seedBody  []byte
	indexPath string
}

func fuzzSetup(tb testing.TB) {
	fuzzEnv.once.Do(func() {
		dir, err := os.MkdirTemp("", "lcafuzz")
		if err != nil {
			panic(err)
		}
		fuzzEnv.dir = dir
		src := fixtureSources()
		fuzzEnv.indexPath = saveIndex(tb, dir, src...)
		if _, _, err := LoadView(fuzzEnv.indexPath); err != nil {
			panic(err)
		}
		fuzzEnv.sum, fuzzEnv.size, _ = hashIndexFile(fuzzEnv.indexPath)
		fuzzEnv.seed, _ = os.ReadFile(ViewPath(fuzzEnv.indexPath))
		fuzzEnv.header, fuzzEnv.seedBody, _ = splitViewBytes(fuzzEnv.seed)
	})
}

func exercise(t *testing.T, v *View) {
	for _, q := range []string{"", "Google", "Acme Widgets", "Walt Disney", "zzz zzz", "A", "日本 電気", "Johnson And"} {
		b := v.Lookup(q)
		_ = b.String()
	}
	_ = v.Coverage()
	_ = v.Len()
}

func memGuard(t *testing.T, f func()) {
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	if d := b.TotalAlloc - a.TotalAlloc; d > 1<<30 {
		t.Fatalf("allocated %d bytes on a tiny input", d)
	}
}

// FuzzLoadView mutates the whole view file (header included) as readView sees
// it, with the true index checksum so inputs get past the stale check when
// their header survives.
func FuzzLoadView(f *testing.F) {
	fuzzSetup(f)
	f.Add(fuzzEnv.seed)
	f.Add(joinViewRaw(fuzzEnv.header, fuzzEnv.seedBody[:len(fuzzEnv.seedBody)/2]))
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(fuzzEnv.dir, fmt.Sprintf("v%d", os.Getpid()))
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Skip()
		}
		memGuard(t, func() {
			if v, err := readView(p, fuzzEnv.sum, fuzzEnv.size); err == nil {
				exercise(t, v)
			}
		})
	})
}

// FuzzViewBody mutates the body and the numeric header fields, then recomputes
// the body checksum, so the structural validation (offsets, ordering, counts)
// is what is under attack rather than the checksum.
func FuzzViewBody(f *testing.F) {
	fuzzSetup(f)
	f.Add(fuzzEnv.seedBody, fuzzEnv.header.Employers, 3, byte(0))
	f.Add([]byte{}, 0, 0, byte(0))
	f.Add(make([]byte, 64), 1<<31, 3, byte(1))
	f.Add(make([]byte, 64), 1<<62, 3, byte(1))
	f.Add(make([]byte, 64), 3, 1<<62, byte(1))
	f.Fuzz(func(t *testing.T, body []byte, employers, nfy int, flags byte) {
		h := fuzzEnv.header
		h.Employers = employers
		fys := []int{2023, 2024, 2025}
		if nfy < 0 || nfy > 3 {
			nfy = nfy & 3
		}
		h.FYs = fys[:nfy]
		h.BodyBytes = int64(len(body))
		h.BodySHA256 = sumHex(body)
		if flags&1 == 1 {
			h.BodyBytes += int64(flags) // lie about length
		}
		p := filepath.Join(fuzzEnv.dir, fmt.Sprintf("b%d", os.Getpid()))
		if err := os.WriteFile(p, joinViewRaw(h, body), 0o600); err != nil {
			t.Skip()
		}
		memGuard(t, func() {
			if v, err := readView(p, fuzzEnv.sum, fuzzEnv.size); err == nil {
				exercise(t, v)
			}
		})
	})
}

func joinViewRaw(h viewHeader, body []byte) []byte {
	line, _ := json.Marshal(h)
	return append(append(line, '\n'), body...)
}

// --- 4. write failures ------------------------------------------------------

func leftovers(t *testing.T, dir string) []string {
	var out []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() != "index.json" && e.Name() != "index.view" {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestAttackReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	dir := t.TempDir()
	path := saveIndex(t, dir, fixtureSources()...)
	x, _ := Load(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	for i := 0; i < 2; i++ {
		v, info, err := LoadView(path)
		if err != nil || !info.Rebuilt || info.WriteErr == nil || info.Routine() {
			t.Fatalf("round %d: %+v %v", i, info, err)
		}
		checkParity(t, x, v, fixtureQueries)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("leftover files: %v", l)
	}
	if _, err := os.Stat(ViewPath(path)); err == nil {
		t.Error("view file appeared in a read-only dir")
	}
}

func TestAttackViewPathIsADirectory(t *testing.T) {
	dir := t.TempDir()
	path := saveIndex(t, dir, fixtureSources()...)
	if err := os.Mkdir(ViewPath(path), 0o700); err != nil {
		t.Fatal(err)
	}
	x, _ := Load(path)
	v, info, err := LoadView(path)
	if err != nil || info.WriteErr == nil {
		t.Fatalf("%+v %v", info, err)
	}
	checkParity(t, x, v, fixtureQueries)
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("leftover files: %v", l)
	}
}

// Disk full: a child process with RLIMIT_FSIZE below the view size makes the
// temp-file write fail with EFBIG (the nearest stand-in for ENOSPC the stdlib
// allows without a seam; atomicfile has no writer seam).
func TestAttackDiskFullChild(t *testing.T) {
	if p := os.Getenv("VANI_ATTACK_FSIZE_CHILD"); p != "" {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 2048, Max: 2048}); err != nil {
			fmt.Println("SETRLIMIT", err)
			os.Exit(3)
		}
		v, info, err := LoadView(p)
		if err != nil {
			fmt.Println("ERR", err)
			os.Exit(4)
		}
		fmt.Printf("OK total=%d rebuilt=%v writeErr=%v\n", v.Lookup("Employer 0000001 Holdings").Total, info.Rebuilt, info.WriteErr != nil)
		return
	}
	dir := t.TempDir()
	path := synthLCA(t, dir, 500)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAttackDiskFullChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "VANI_ATTACK_FSIZE_CHILD="+path)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK total=") || !strings.Contains(string(out), "writeErr=true") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("partial files left after a failed write: %v", l)
	}
	if _, err := os.Stat(ViewPath(path)); err == nil {
		t.Error("a view exists though the write failed")
	}
}

// --- 5. cross-process -------------------------------------------------------

func TestAttackCrossProcess(t *testing.T) {
	if role := os.Getenv("VANI_ATTACK_ROLE"); role != "" {
		path := os.Getenv("VANI_ATTACK_INDEX")
		deadline := time.Now().Add(6 * time.Second)
		n := 0
		for time.Now().Before(deadline) {
			if role == "rebuild" {
				_ = os.Remove(ViewPath(path))
			}
			v, info, err := LoadView(path)
			if err != nil {
				fmt.Println("FAIL LoadView:", err)
				os.Exit(5)
			}
			if info.Rebuilt && !errors.Is(info.Reason, fs.ErrNotExist) && !errors.Is(info.Reason, ErrStaleView) {
				fmt.Printf("FAIL reader saw a refused view: %v\n", info.Reason)
				os.Exit(6)
			}
			if info.WriteErr != nil {
				fmt.Printf("FAIL write: %v\n", info.WriteErr)
				os.Exit(7)
			}
			if v.Len() != 20000 || v.Lookup("Employer 0000007 Holdings").Total == 0 {
				fmt.Println("FAIL wrong data")
				os.Exit(8)
			}
			n++
		}
		fmt.Println("OK", role, n)
		return
	}
	dir := t.TempDir()
	path := synthLCA(t, dir, 20000)
	var wg sync.WaitGroup
	roles := []string{"rebuild", "rebuild", "rebuild", "read", "read"}
	outs := make([][]byte, len(roles))
	errs := make([]error, len(roles))
	for i, r := range roles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestAttackCrossProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "VANI_ATTACK_ROLE="+r, "VANI_ATTACK_INDEX="+path)
			outs[i], errs[i] = cmd.CombinedOutput()
		}()
	}
	wg.Wait()
	for i := range roles {
		if errs[i] != nil || !bytes.Contains(outs[i], []byte("OK ")) {
			t.Errorf("%s #%d: %v\n%s", roles[i], i, errs[i], outs[i])
		} else {
			t.Logf("%s #%d: %s", roles[i], i, strings.TrimSpace(string(outs[i])))
		}
	}
	if l := leftovers(t, dir); len(l) != 0 {
		t.Errorf("temp files left: %v", l)
	}
}

// --- 6. cost ----------------------------------------------------------------

// BenchmarkHashIndex is the share of LoadView that is hashing the index file:
// compare with BenchmarkLoadView on the same 200k-employer index.
func BenchmarkHashIndex(b *testing.B) {
	path := synthLCA(b, b.TempDir(), 200_000)
	st, _ := os.Stat(path)
	b.SetBytes(st.Size())
	for b.Loop() {
		if _, _, err := hashIndexFile(path); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReadViewOnly is LoadView minus the index hash.
func BenchmarkReadViewOnly(b *testing.B) {
	path := synthLCA(b, b.TempDir(), 200_000)
	if _, _, err := LoadView(path); err != nil {
		b.Fatal(err)
	}
	sum, size, _ := hashIndexFile(path)
	for b.Loop() {
		if _, err := readView(ViewPath(path), sum, size); err != nil {
			b.Fatal(err)
		}
	}
}
