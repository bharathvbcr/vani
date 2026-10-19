package marks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A last line that is whole JSON but not a valid event is a finished line
// (a newer writer's, say), not a torn write. It used to be skipped as torn and
// cut off by the next write, erasing a mark that was really made.
func TestCompleteInvalidLastLineIsCorrupt(t *testing.T) {
	for name, line := range map[string]string{
		"newer version": `{"v":2,"at":"2026-10-19T09:00:00Z","key":"gh:a:2","mark":"saved"}`,
		"unknown field": `{"v":1,"at":"2026-10-19T09:00:00Z","key":"gh:a:2","mark":"saved","extra":1}`,
		"unknown mark":  `{"v":1,"at":"2026-10-19T09:00:00Z","key":"gh:a:2","mark":"shortlisted"}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := Dir(t.TempDir())
			if _, err := SetMark(dir, k1, Saved, t0); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, fileName)
			fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fh.WriteString(line + "\n"); err != nil {
				t.Fatal(err)
			}
			fh.Close()
			before, _ := os.ReadFile(path)

			if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Read: %v, want ErrCorrupt", err)
			}
			if _, err := SetMark(dir, "gh:a:3", Saved, t0); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("SetMark: %v, want ErrCorrupt", err)
			}
			if after, _ := os.ReadFile(path); string(after) != string(before) {
				t.Fatal("the refused line was cut or altered")
			}
		})
	}
}
