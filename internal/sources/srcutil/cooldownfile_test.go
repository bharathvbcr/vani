package srcutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

func cooldownPath(t *testing.T) string { return filepath.Join(t.TempDir(), "hosts", "cooldown.json") }

func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCooldownsRoundTrip(t *testing.T) {
	p := cooldownPath(t)
	in := map[string]HostCooldown{
		"apply.careers.microsoft.com": {Runs: 3, Last: t0.Add(-time.Hour)},
		"[::1]":                       {Runs: 1, Last: t0}, // not a Hostname(): refused
		"2001:db8::1":                 {Runs: 2, Last: t0},
	}
	if err := SaveCooldowns(p, in, t0); err != nil {
		t.Fatal(err)
	}
	got, warn := LoadCooldowns(p, t0)
	if warn != "" {
		t.Errorf("warning %q", warn)
	}
	want := map[string]HostCooldown{
		"apply.careers.microsoft.com": {Runs: 3, Last: t0.Add(-time.Hour)},
		"2001:db8::1":                 {Runs: 2, Last: t0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for h, c := range want {
		if g := got[h]; g.Runs != c.Runs || !g.Last.Equal(c.Last) {
			t.Errorf("%s = %+v, want %+v", h, g, c)
		}
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("file %v mode %v, want 0600", err, st)
	}
}

func TestLoadCooldownsMissingFileIsEmptyAndQuiet(t *testing.T) {
	got, warn := LoadCooldowns(cooldownPath(t), t0)
	if len(got) != 0 || warn != "" {
		t.Errorf("%v %q", got, warn)
	}
}

// Whatever is in the file, loading never fails the run: it yields no cooldowns
// (or only the good entries) and says what it did.
func TestLoadCooldownsToleratesCorruption(t *testing.T) {
	good := `"ok.test":{"runs":2,"last":"2026-10-22T11:00:00Z"}`
	for name, c := range map[string]struct {
		body     string
		wantHost bool // ok.test survives
		warns    string
	}{
		"empty file":      {"", false, "not a version 1"},
		"binary garbage":  {"\x00\x01\x02{{{", false, "not a version 1"},
		"truncated json":  {`{"version":1,"hosts":{` + good, false, "not a version 1"},
		"wrong version":   {`{"version":2,"hosts":{` + good + `}}`, false, "not a version 1"},
		"array":           {`[1,2,3]`, false, "not a version 1"},
		"null hosts":      {`{"version":1,"hosts":null}`, false, ""},
		"hosts is string": {`{"version":1,"hosts":"x"}`, false, "not a version 1"},
		"bad entries dropped": {`{"version":1,"hosts":{` + good + `,
			"zero.test":{"runs":0,"last":"2026-10-22T11:00:00Z"},
			"neg.test":{"runs":-5,"last":"2026-10-22T11:00:00Z"},
			"nolast.test":{"runs":1},
			"badlast.test":{"runs":1,"last":"yesterday"},
			"str.test":{"runs":"many","last":"2026-10-22T11:00:00Z"},
			"UPPER.test":{"runs":1,"last":"2026-10-22T11:00:00Z"},
			"sp ace.test":{"runs":1,"last":"2026-10-22T11:00:00Z"},
			"":{"runs":1,"last":"2026-10-22T11:00:00Z"},
			"null.test":null}}`, true, "unusable or surplus entries dropped"},
	} {
		t.Run(name, func(t *testing.T) {
			p := cooldownPath(t)
			writeRaw(t, p, c.body)
			got, warn := LoadCooldowns(p, t0)
			if _, ok := got["ok.test"]; ok != c.wantHost {
				t.Errorf("ok.test present = %v, want %v (got %v)", ok, c.wantHost, got)
			}
			if c.wantHost && len(got) != 1 {
				t.Errorf("kept %v, want only ok.test", got)
			}
			if !strings.Contains(warn, c.warns) || (c.warns != "" && warn == "") {
				t.Errorf("warning %q, want it to contain %q", warn, c.warns)
			}
		})
	}
}

func TestLoadCooldownsOversizedFileIsIgnored(t *testing.T) {
	p := cooldownPath(t)
	var b strings.Builder
	b.WriteString(`{"version":1,"hosts":{`)
	for i := 0; b.Len() < maxCooldownFile+100; i++ {
		fmt.Fprintf(&b, `"h%d.test":{"runs":1,"last":"2026-10-22T11:00:00Z"},`, i)
	}
	b.WriteString(`"z.test":{"runs":1,"last":"2026-10-22T11:00:00Z"}}}`)
	writeRaw(t, p, b.String())
	got, warn := LoadCooldowns(p, t0)
	if len(got) != 0 || !strings.Contains(warn, "is over") {
		t.Errorf("%d entries, warning %q; want none and an over-size warning", len(got), warn)
	}
	// Saving afterwards replaces the unusable file with a usable one.
	if err := SaveCooldowns(p, map[string]HostCooldown{"a.test": {Runs: 1, Last: t0}}, t0); err != nil {
		t.Fatal(err)
	}
	if got, warn := LoadCooldowns(p, t0); len(got) != 1 || warn != "" {
		t.Errorf("after repair: %v %q", got, warn)
	}
}

// Many hosts on file: only the most recent maxThrottleHosts are kept, the same
// ones on every load.
func TestCooldownsManyHostsKeepMostRecent(t *testing.T) {
	p := cooldownPath(t)
	in := map[string]HostCooldown{}
	for i := 0; i < 300; i++ {
		in[fmt.Sprintf("h%03d.test", i)] = HostCooldown{Runs: 1, Last: t0.Add(-time.Duration(i) * time.Minute)}
	}
	if err := SaveCooldowns(p, in, t0); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadCooldowns(p, t0)
	if len(got) != maxThrottleHosts {
		t.Fatalf("%d entries, want %d", len(got), maxThrottleHosts)
	}
	for i := 0; i < maxThrottleHosts; i++ {
		if _, ok := got[fmt.Sprintf("h%03d.test", i)]; !ok {
			t.Fatalf("h%03d.test (recent) was dropped", i)
		}
	}
	st, _ := os.Stat(p)
	if st.Size() > maxCooldownFile {
		t.Errorf("file is %d bytes, over the %d it must be readable under", st.Size(), maxCooldownFile)
	}
}

func TestCooldownsAgeAndSkew(t *testing.T) {
	p := cooldownPath(t)
	writeRaw(t, p, `{"version":1,"hosts":{
		"old.test":{"runs":4,"last":"2026-09-01T00:00:00Z"},
		"fresh.test":{"runs":4,"last":"2026-10-22T11:50:00Z"},
		"future.test":{"runs":4,"last":"2027-01-01T00:00:00Z"},
		"huge.test":{"runs":1000000,"last":"2026-10-22T11:50:00Z"}}}`)
	got, warn := LoadCooldowns(p, t0)
	if _, ok := got["old.test"]; ok || !strings.Contains(warn, "1 unusable") {
		t.Errorf("a cooldown older than %v was kept: %v (%q)", maxCooldownAge, got, warn)
	}
	if c := got["fresh.test"]; c.Runs != 4 {
		t.Errorf("fresh.test = %+v", c)
	}
	if c := got["huge.test"]; c.Runs != MaxCooldownRuns {
		t.Errorf("huge.test runs = %d, want the cap %d", c.Runs, MaxCooldownRuns)
	}
	// A stamp from the future is read as a window just over: the host is probed.
	c, ok := got["future.test"]
	if !ok || c.Last.After(t0) || t0.Sub(c.Last) != probeGap(4) {
		t.Fatalf("future.test = %+v (present %v), want its window read as just passed", c, ok)
	}
	f := newFakeHosts(always429)
	noWait(t)
	th := NewHostThrottle(got, (&fakeClock{t: t0}).now)
	wantWhy(t, getFrom(WithHostThrottle(t.Context(), th), f, "future.test"), "probed once")
	if f.count("future.test") != 1 {
		t.Errorf("a future-stamped cooldown was not probed: %d requests", f.count("future.test"))
	}
}

func TestSaveCooldownsDropsWhatItCannotKeep(t *testing.T) {
	p := cooldownPath(t)
	err := SaveCooldowns(p, map[string]HostCooldown{
		"ok.test":  {Runs: 2, Last: t0},
		"zero.t":   {Runs: 0, Last: t0},
		"old.test": {Runs: 2, Last: t0.Add(-2 * maxCooldownAge)},
		"Bad Host": {Runs: 2, Last: t0},
		"big.test": {Runs: 5000, Last: t0},
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	got, warn := LoadCooldowns(p, t0)
	if warn != "" || len(got) != 2 || got["ok.test"].Runs != 2 || got["big.test"].Runs != MaxCooldownRuns {
		t.Errorf("%v %q", got, warn)
	}
}

// An empty map still writes a file (so a corrupt one is repaired) and a
// directory in the way is an error, not a crash.
func TestSaveCooldownsEmptyAndUnwritable(t *testing.T) {
	p := cooldownPath(t)
	writeRaw(t, p, "garbage")
	if err := SaveCooldowns(p, nil, t0); err != nil {
		t.Fatal(err)
	}
	if got, warn := LoadCooldowns(p, t0); len(got) != 0 || warn != "" {
		t.Errorf("after saving nothing over garbage: %v %q", got, warn)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "cooldown.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveCooldowns(filepath.Join(dir, "cooldown.json"), map[string]HostCooldown{"a.test": {Runs: 1, Last: t0}}, t0); err == nil {
		t.Error("saving over a directory succeeded")
	}
	if _, warn := LoadCooldowns(filepath.Join(dir, "cooldown.json"), t0); warn == "" {
		t.Error("loading a directory raised no warning")
	}
}

// Two runs in a row, through the file: the second sends one probe, and when the
// host has recovered the file no longer names it.
func TestCooldownsAcrossRunsThroughTheFile(t *testing.T) {
	noWait(t)
	noJitter(t)
	p := cooldownPath(t)
	clk := &fakeClock{t: t0}
	run := func(h func(string, int) srctest.Reply) (*fakeHosts, *HostThrottle) {
		prior, warn := LoadCooldowns(p, clk.now())
		if warn != "" {
			t.Fatal(warn)
		}
		th := NewHostThrottle(prior, clk.now)
		f := newFakeHosts(h)
		_ = getFrom(WithHostThrottle(t.Context(), th), f, "ms.test")
		if err := SaveCooldowns(p, th.Cooldowns(), clk.now()); err != nil {
			t.Fatal(err)
		}
		return f, th
	}
	// Day 1: down. Five attempts, then a cooldown of one run.
	f, _ := run(always429)
	if f.count("ms.test") != 5 {
		t.Fatalf("day 1: %d requests", f.count("ms.test"))
	}
	// Days 2 and 3: still down. One probe each, and the count climbs.
	for day, want := range []int{2, 3} {
		clk.advance(24 * time.Hour)
		f, th := run(always429)
		if f.count("ms.test") != 1 || th.Cooldowns()["ms.test"].Runs != want {
			t.Fatalf("day %d: %d requests, cooldowns %v; want one probe and %d runs", day+2, f.count("ms.test"), th.Cooldowns(), want)
		}
	}
	// Day 4: back. One request, answered; the file forgets the host.
	clk.advance(24 * time.Hour)
	f, _ = run(func(string, int) srctest.Reply { return ok200 })
	got, _ := LoadCooldowns(p, clk.now())
	if f.count("ms.test") != 1 || len(got) != 0 {
		t.Errorf("day 4: %d requests, cooldowns %v", f.count("ms.test"), got)
	}
}
