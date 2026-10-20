package semantic

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hotVec(dim, hot int) []float32 {
	v := make([]float32, dim)
	v[hot%dim] = 1
	return v
}

func openVC(t *testing.T, dir string, max int) *VectorCache {
	t.Helper()
	c, err := OpenVectorCache(dir, max)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVectorCacheRoundTripAndKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "semantic")
	c := openVC(t, dir, 0)
	if _, ok := c.Get("m", KindSimilarity, "x"); ok {
		t.Fatal("hit in an empty cache")
	}
	if err := c.Put("m", KindSimilarity, "LinkedIn", []float32{3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2 := openVC(t, dir, 0)
	v, ok := c2.Get("m", KindSimilarity, "LinkedIn")
	if !ok || len(v) != 2 || math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("round trip: %v %v (want the normalized vector)", v, ok)
	}
	for name, miss := range map[string]func() bool{
		"other model": func() bool { _, ok := c2.Get("m2", KindSimilarity, "LinkedIn"); return ok },
		"other kind":  func() bool { _, ok := c2.Get("m", KindQuery, "LinkedIn"); return ok },
		"other text":  func() bool { _, ok := c2.Get("m", KindSimilarity, "LinkedIn "); return ok },
		"no model":    func() bool { _, ok := c2.Get("", KindSimilarity, "LinkedIn"); return ok },
	} {
		if miss() {
			t.Errorf("%s: hit", name)
		}
	}
	// Get returns a copy.
	v[0] = 9
	if w, _ := c2.Get("m", KindSimilarity, "LinkedIn"); w[0] == 9 {
		t.Error("Get exposed the cached slice")
	}
	if fi, err := os.Stat(filepath.Join(dir, vectorsFileName)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file: %v %v", fi, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".*tmp*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left: %v", leftovers)
	}
}

func TestVectorCacheRefusesBadVectors(t *testing.T) {
	c := openVC(t, t.TempDir(), 0)
	nan := float32(math.NaN())
	for name, v := range map[string][]float32{
		"nan": {1, nan}, "inf": {float32(math.Inf(1)), 0}, "zero": {0, 0}, "empty": nil,
		"too wide": make([]float32, maxVectorDim+1),
	} {
		if len(v) > 0 && name == "too wide" {
			v[0] = 1
		}
		if err := c.Put("m", KindSimilarity, "t", v); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
	if err := c.Put("", KindSimilarity, "t", []float32{1}); !errors.Is(err, ErrCache) {
		t.Errorf("empty model id: %v", err)
	}
	if err := c.Put("a|b", KindSimilarity, "t", []float32{1}); !errors.Is(err, ErrCache) {
		t.Errorf("model id with the key separator: %v", err)
	}
	if c.Len() != 0 {
		t.Errorf("%d vectors stored from bad input", c.Len())
	}
}

func TestVectorCacheEvictsOldestPastEntryCap(t *testing.T) {
	dir := t.TempDir()
	c := openVC(t, dir, 5)
	for i := 0; i < 8; i++ {
		if err := c.Put("m", KindSimilarity, fmt.Sprint("t", i), hotVec(4, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2 := openVC(t, dir, 5)
	if c2.Len() != 5 {
		t.Fatalf("%d entries, want 5", c2.Len())
	}
	for i := 0; i < 8; i++ {
		_, ok := c2.Get("m", KindSimilarity, fmt.Sprint("t", i))
		if want := i >= 3; ok != want {
			t.Errorf("t%d: present=%v, want %v (oldest go first)", i, ok, want)
		}
	}
	// A reopened cache keeps counting: new writes are newer than the survivors.
	if err := c2.Put("m", KindSimilarity, "t8", hotVec(4, 0)); err != nil {
		t.Fatal(err)
	}
	if err := c2.Save(); err != nil {
		t.Fatal(err)
	}
	c3 := openVC(t, dir, 5)
	if _, ok := c3.Get("m", KindSimilarity, "t3"); ok {
		t.Error("t3 (oldest) survived")
	}
	if _, ok := c3.Get("m", KindSimilarity, "t8"); !ok {
		t.Error("t8 (newest) was evicted")
	}
}

func TestVectorCacheByteCap(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a cache past 64 MiB")
	}
	dir := t.TempDir()
	c := openVC(t, dir, 1<<20)
	const dim = 2048
	n := MaxCacheBytes/(dim*4*4/3) + 2000 // base64 grows 4/3: well past the cap
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		v[i%dim], v[(i*7+1)%dim] = 1, float32(i%5)
		if err := c.Put("m", KindSimilarity, fmt.Sprint("t", i), v); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, vectorsFileName))
	if err != nil || fi.Size() > MaxCacheBytes {
		t.Fatalf("file %v %v, want at most %d bytes", fi, err, MaxCacheBytes)
	}
	c2 := openVC(t, dir, 1<<20)
	if c2.Len() == 0 || c2.Len() >= n {
		t.Fatalf("%d of %d entries kept", c2.Len(), n)
	}
	if _, ok := c2.Get("m", KindSimilarity, fmt.Sprint("t", n-1)); !ok {
		t.Error("the newest vector was evicted for size")
	}
	if _, ok := c2.Get("m", KindSimilarity, "t0"); ok {
		t.Error("the oldest vector survived a size eviction")
	}
}

func TestVectorCacheFailsClosed(t *testing.T) {
	good := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		c := openVC(t, dir, 0)
		if err := c.Put("m", KindSimilarity, "a", []float32{1, 0}); err != nil {
			t.Fatal(err)
		}
		if err := c.Save(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, vectorsFileName))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	raw := good(t)
	vec := raw[strings.Index(raw, `"v":"`)+5:]
	vec = vec[:strings.Index(vec, `"`)]
	key := raw[strings.Index(raw, `"m|`)+1:]
	key = key[:strings.Index(key, `"`)]
	nanB64 := encodeVec([]float32{float32(math.NaN()), 1})
	cases := map[string]string{
		"not json":      "{",
		"trailing":      raw + "{}",
		"version 0":     strings.Replace(raw, `"version":1`, `"version":0`, 1),
		"version 2":     strings.Replace(raw, `"version":1`, `"version":2`, 1),
		"bad key":       strings.Replace(raw, key, "nokey", 1),
		"bad base64":    strings.Replace(raw, vec, "!!!!", 1),
		"nan vector":    strings.Replace(raw, vec, nanB64, 1),
		"odd length":    strings.Replace(raw, vec, "AAAA", 1), // 3 bytes
		"empty vector":  strings.Replace(raw, vec, "", 1),
		"unknown shape": `[]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, vectorsFileName), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenVectorCache(dir, 0); !errors.Is(err, ErrCache) {
				t.Fatalf("got %v, want ErrCache", err)
			}
		})
	}
	t.Run("oversized file", func(t *testing.T) {
		dir := t.TempDir()
		big := make([]byte, MaxCacheBytes+1)
		for i := range big {
			big[i] = ' '
		}
		if err := os.WriteFile(filepath.Join(dir, vectorsFileName), big, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenVectorCache(dir, 0); !errors.Is(err, ErrCache) {
			t.Fatalf("got %v, want ErrCache", err)
		}
	})
	t.Run("reset then reopen", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, vectorsFileName), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ResetVectorCache(dir); err != nil {
			t.Fatal(err)
		}
		if c, err := OpenVectorCache(dir, 0); err != nil || c.Len() != 0 {
			t.Fatalf("%v %v", c, err)
		}
		if err := ResetVectorCache(dir); err != nil { // nothing to remove is not an error
			t.Fatal(err)
		}
	})
	t.Run("no directory", func(t *testing.T) {
		if _, err := OpenVectorCache("", 0); !errors.Is(err, ErrCache) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestVectorCacheSaveOnlyWhenChanged(t *testing.T) {
	dir := t.TempDir()
	c := openVC(t, dir, 0)
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, vectorsFileName)); err == nil {
		t.Fatal("wrote a file with nothing to save")
	}
	_ = c.Put("m", KindSimilarity, "a", []float32{1, 0})
	_ = c.Save()
	if err := os.Remove(filepath.Join(dir, vectorsFileName)); err != nil {
		t.Fatal(err)
	}
	_ = c.Put("m", KindSimilarity, "a", []float32{1, 0}) // identical: not a change
	_ = c.Save()
	if _, err := os.Stat(filepath.Join(dir, vectorsFileName)); err == nil {
		t.Fatal("rewrote the file for an identical Put")
	}
}

func TestVectorCacheConcurrent(t *testing.T) {
	c := openVC(t, t.TempDir(), 0)
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				_ = c.Put("m", KindSimilarity, fmt.Sprint("t", g, i), hotVec(4, i))
				c.Get("m", KindSimilarity, fmt.Sprint("t", g, i))
				if i%10 == 0 {
					_ = c.Save()
				}
			}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	if c.Len() != 400 {
		t.Fatalf("%d entries", c.Len())
	}
}
