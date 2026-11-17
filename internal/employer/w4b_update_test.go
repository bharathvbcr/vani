package employer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/indexutil"
)

const w4bPerChild = 12

// w4bPartial is the import child c's i-th round makes: one source with its own
// hash and name, so that nothing but a lost update can drop it.
func w4bPartial(child, i int) Partial {
	return Partial{
		Source: SourceMeta{Name: fmt.Sprintf("c%d-%d.csv", child, i), Kind: KindIRSBMF, SHA256: fmt.Sprintf("%d-%d", child, i), RowsRead: 1, RowsCounted: 1},
		Names:  map[string]attr{fmt.Sprintf("W4B C%d R%d", child, i): bitNonprofit},
	}
}

// TestW4bUpdateChild is the body of one importing process; the parent re-execs
// the test binary with the role in the environment.
func TestW4bUpdateChild(t *testing.T) {
	if os.Getenv("W4B_ROLE") != "child" {
		t.Skip("child role only")
	}
	path := os.Getenv("W4B_PATH")
	child, _ := strconv.Atoi(os.Getenv("W4B_CHILD"))
	for i := 0; i < w4bPerChild; i++ {
		_, err := Update(path, w4bPartial(child, i))
		switch {
		case err == nil:
			fmt.Printf("ok %d-%d\n", child, i)
		case errors.Is(err, ErrBusy):
			fmt.Println("busy")
		default:
			fmt.Printf("fail %v\n", err)
			os.Exit(2)
		}
	}
}

func TestW4bUpdateAcrossProcesses(t *testing.T) {
	if os.Getenv("W4B_ROLE") != "" {
		t.Skip("child process")
	}
	path := filepath.Join(t.TempDir(), "employer", "index.json")
	var outs [2]bytes.Buffer
	cmds := make([]*exec.Cmd, 2)
	for c := range cmds {
		cmds[c] = exec.Command(os.Args[0], "-test.run=^TestW4bUpdateChild$", "-test.count=1")
		cmds[c].Env = append(os.Environ(), "W4B_ROLE=child", "W4B_PATH="+path, "W4B_CHILD="+strconv.Itoa(c))
		cmds[c].Stdout = &outs[c]
		cmds[c].Stderr = &outs[c]
		if err := cmds[c].Start(); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{}
	for c, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", c, err, outs[c].String())
		}
		for _, ln := range strings.Split(outs[c].String(), "\n") {
			if id, ok := strings.CutPrefix(ln, "ok "); ok {
				want[id] = true
			}
		}
	}
	if len(want) == 0 {
		t.Fatalf("no import succeeded:\n%s%s", outs[0].String(), outs[1].String())
	}
	x, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range x.Sources() {
		got[s.SHA256] = true
	}
	for id := range want {
		if !got[id] {
			t.Errorf("import %s reported success but its names are not in the index (%d of %d survive)", id, len(got), len(want))
		}
	}
	if len(got) != len(want) {
		t.Errorf("index holds %d sources, %d imports succeeded", len(got), len(want))
	}
}

// A lock held past the bound is a clean ErrBusy and the index is untouched.
func TestW4bUpdateBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	if _, err := Update(path, w4bPartial(0, 0)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	old := indexutil.LockWait
	indexutil.LockWait = 100 * time.Millisecond
	defer func() { indexutil.LockWait = old }()
	_, release, err := indexutil.Lock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Update(path, w4bPartial(1, 0)); !errors.Is(err, ErrBusy) {
		t.Fatalf("Update under a held lock = %v, want ErrBusy", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("a busy refusal changed the index")
	}
}
