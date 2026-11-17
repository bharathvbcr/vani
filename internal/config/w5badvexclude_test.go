package config

import (
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// Many writers read revision R, then each does the compare-and-replace the CLI
// does (inside Update's lock): exactly one may win, the rest get ErrStale.
func TestW5badvRevisionCheckIsAtomicWithTheWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Create(path, Default()); err != nil {
		t.Fatal(err)
	}
	c0, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rev := c0.Exclude.Revision()
	const n = 12
	var wins, stale atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := Update(path, func(c *Config) error {
				if c.Exclude.Revision() != rev {
					return ErrStale
				}
				c.Exclude = Exclude{Companies: []string{"co" + strconv.Itoa(i)}}
				return nil
			})
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrStale):
				stale.Add(1)
			default:
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || stale.Load() != n-1 {
		t.Fatalf("wins=%d stale=%d, want 1 and %d", wins.Load(), stale.Load(), n-1)
	}
}

// Field separators cannot make two different list sets collide.
func TestW5badvRevisionNoSeparatorAmbiguity(t *testing.T) {
	sets := []Exclude{
		{Titles: []string{"a,b"}},
		{Titles: []string{"a", "b"}},
		{Titles: []string{"a\x00b"}},
		{Titles: []string{"a"}, TitleUnless: []string{"b"}},
		{Titles: []string{"a"}, Companies: []string{"b"}},
		{Titles: []string{"ab"}},
		{Titles: []string{"", "ab"}},
		{Titles: []string{"ab", ""}},
		{},
	}
	seen := map[string]int{}
	for i, s := range sets {
		if j, dup := seen[s.Revision()]; dup {
			t.Errorf("sets %d and %d collide: %+v vs %+v", j, i, sets[j], s)
		}
		seen[s.Revision()] = i
	}
}
