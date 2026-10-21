package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
)

func shortLock(t *testing.T, d time.Duration) {
	t.Helper()
	old := lockWait
	lockWait = d
	t.Cleanup(func() { lockWait = old })
}

// holdLock holds Update's lock on path until the returned func is called.
func holdLock(t *testing.T, path string) (release func()) {
	t.Helper()
	holding, rel, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- Update(path, func(*Config) error { close(holding); <-rel; return nil })
	}()
	<-holding
	return func() {
		close(rel)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

// Two spellings of one config must share one lock, or two writers each think
// they are alone and the last rename wins.
func TestAdvLockSharedAcrossSpellings(t *testing.T) {
	shortLock(t, 150*time.Millisecond)
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(realDir, "config.json")
	if err := Save(real, valid()); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(dir, "link.json")
	dirLink := filepath.Join(dir, "dlink")
	if err := os.Symlink(real, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, dirLink); err != nil {
		t.Fatal(err)
	}
	spellings := map[string]string{
		"dotdot":      filepath.Join(realDir, "..", "real", "config.json"),
		"dir symlink": filepath.Join(dirLink, "config.json"),
	}
	_ = fileLink // a file symlink takes a different lock: see adv_failing_test.go
	release := holdLock(t, real)
	defer release()
	for name, p := range spellings {
		ran := false
		err := Update(p, func(*Config) error { ran = true; return nil })
		if !errors.Is(err, ErrBusy) || ran {
			t.Errorf("%s (%s): err %v, fn ran %v; want ErrBusy: a second spelling of the same file took a different lock", name, p, err, ran)
		}
	}
}

func TestAdvLockFileMode0600(t *testing.T) {
	path := savedConfig(t)
	st, err := os.Stat(path + lockSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode %v, want 0600", st.Mode().Perm())
	}
}

func TestAdvUnwritableDirIsAClearErrorWithNoPartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	path := savedConfig(t)
	dir := filepath.Dir(path)
	before, _ := os.ReadFile(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	err := Update(path, func(c *Config) error { c.MotivationNotes = "x"; return nil })
	if err == nil {
		t.Fatal("Update in a read-only dir succeeded")
	}
	if !strings.Contains(err.Error(), "config:") || errors.Is(err, ErrBusy) {
		t.Errorf("unclear error: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("config changed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	// And with no lock file yet (fresh dir): the failure names the lock.
	fresh := filepath.Join(t.TempDir(), "config.json")
	if err := Save(fresh, valid()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fresh + lockSuffix); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(fresh), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(fresh), 0o700) })
	err = Update(fresh, func(*Config) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Errorf("no-lock-file case: %v, want an error naming the lock", err)
	}
}

// A process killed with SIGKILL while holding the lock leaves the file but not
// the lock: the next Update proceeds at once.
func TestAdvKilledHolderDoesNotWedgeTheNextUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals")
	}
	if p := os.Getenv("VANI_ADV_HOLD_PATH"); p != "" {
		_ = Update(p, func(*Config) error {
			_ = os.WriteFile(p+".held", []byte("1"), 0o600)
			time.Sleep(time.Minute)
			return nil
		})
		return
	}
	path := savedConfig(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdvKilledHolderDoesNotWedgeTheNextUpdate$")
	cmd.Env = append(os.Environ(), "VANI_ADV_HOLD_PATH="+path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		if _, err := os.Stat(path + ".held"); err == nil {
			break
		}
		if i > 500 {
			_ = cmd.Process.Kill()
			t.Fatal("holder never took the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	shortLock(t, 200*time.Millisecond)
	if err := Update(path, func(*Config) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("while held: %v, want ErrBusy", err)
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
	shortLock(t, 2*time.Second)
	if err := Update(path, func(c *Config) error { c.MotivationNotes = "after kill"; return nil }); err != nil {
		t.Fatalf("after SIGKILL: %v", err)
	}
	if got := mustLoad(t, path).MotivationNotes; got != "after kill" {
		t.Errorf("notes %q", got)
	}
}

// Update from inside fn takes the same flock on a second descriptor and cannot
// get it: bounded by lockWait, never a hang, and nothing is half-written.
func TestAdvReentrantUpdateIsBoundedBusy(t *testing.T) {
	shortLock(t, 200*time.Millisecond)
	path := savedConfig(t)
	before, _ := os.ReadFile(path)
	var inner error
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- Update(path, func(c *Config) error {
			inner = Update(path, func(c *Config) error { c.MotivationNotes = "inner"; return nil })
			return inner
		})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBusy) || !errors.Is(inner, ErrBusy) {
			t.Errorf("outer %v inner %v, want ErrBusy", err, inner)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-entrant Update hung past lockWait")
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %v with lockWait 200ms", time.Since(start))
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("config changed")
	}
}

// The daemon reads without the lock. Every read during a storm of updates sees
// a whole, valid file.
func TestAdvUnlockedReaderNeverSeesAHalfWrittenConfig(t *testing.T) {
	path := savedConfig(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = Update(path, func(c *Config) error {
				c.MotivationNotes = strings.Repeat("n", 1+i%4000)
				return nil
			})
		}
	}()
	deadline := time.Now().Add(1500 * time.Millisecond)
	reads := 0
	for time.Now().Before(deadline) {
		if _, err := Load(path); err != nil {
			t.Errorf("unlocked read failed: %v", err)
			break
		}
		reads++
	}
	close(stop)
	wg.Wait()
	if reads < 50 {
		t.Errorf("only %d reads", reads)
	}
}

// Converse of the builder's property: whatever egress.New takes as an entry,
// Validate takes too (the allowlist is also required-host checked).
func TestAdvEgressAcceptsImpliesValidateAccepts(t *testing.T) {
	cands := []string{
		"example.com.", " example.com ", "EXAMPLE.com", "*.example.com", "*.example.com.", "*example.com",
		"localhost", "[::1]", "::1", "example.com:443", "bücher.de", "xn--bcher-kva.de", "例え.jp",
		"a_b.example.com", "foo.example.com\t", "foo.example.com\n", "x.-a.com", "a.com..",
	}
	for _, cand := range cands {
		_, nerr := egress.New(egress.Config{AllowedHosts: []string{cand}})
		c := valid()
		c.AllowedHosts = append(c.AllowedHosts, cand)
		verr := c.Validate()
		if nerr == nil && verr != nil {
			t.Errorf("egress.New accepts %q but Validate refuses: %v", cand, verr)
		}
		if nerr != nil && verr == nil {
			t.Errorf("Validate accepts %q but egress.New refuses: %v", cand, nerr)
		}
	}
}
