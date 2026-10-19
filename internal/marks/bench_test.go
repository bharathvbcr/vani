package marks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// historyOf writes about size bytes of mark history (many postings, each
// marked and noted several times) straight into a fresh store.
func historyOf(b *testing.B, size int64) string {
	b.Helper()
	dir := Dir(b.TempDir())
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		b.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, fileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	note := "check the visa wording and the team size before applying"
	marks := []Mark{Saved, Applied, Hidden, None}
	var n int64
	for i := 0; n < size; i++ {
		k, _ := posting.GreenhouseKey("acme", fmt.Sprint(i%3000))
		e := event{V: Version, At: t0.Add(time.Duration(i) * time.Second), Key: k}
		if i%2 == 0 {
			m := marks[i%len(marks)]
			e.Mark = &m
		} else {
			e.Note = &note
		}
		line, _ := json.Marshal(e)
		line = append(line, '\n')
		f.Write(line)
		n += int64(len(line))
	}
	return dir
}

// One mark from the Jobs tab: a whole `vani jobs mark` writes once, so this
// is its store cost, including the replay that checks the store and folds
// the posting's entry, and the fsync.
func BenchmarkSetMark(b *testing.B) {
	for _, size := range []int64{1 << 20, 4 << 20, 7 << 20} {
		b.Run(fmt.Sprintf("history=%dMiB", size>>20), func(b *testing.B) {
			dir := historyOf(b, size)
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				i++
				if _, err := SetMark(dir, k1, []Mark{Saved, Applied}[i%2], t0.Add(time.Duration(i)*time.Hour)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Compact over the same histories.
func BenchmarkCompact(b *testing.B) {
	for _, size := range []int64{1 << 20, 7 << 20} {
		b.Run(fmt.Sprintf("history=%dMiB", size>>20), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				dir := historyOf(b, size)
				b.StartTimer()
				if err := Compact(dir); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
