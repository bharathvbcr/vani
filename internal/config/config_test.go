package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/page"
)

func valid() Config {
	c := Default()
	c.Watchlist = []sources.Board{
		{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"},
		{ATS: posting.Lever, Token: "globex", Company: "Globex", Region: "eu"},
		{ATS: posting.Ashby, Token: "initech", Company: "Initech"},
	}
	c.AllowedHosts = []string{"boards-api.greenhouse.io", "api.eu.lever.co", "api.ashbyhq.com", "openrouter.ai"}
	return c
}

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default invalid: %v", err)
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid invalid: %v", err)
	}
}

func TestValidateRules(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"version", func(c *Config) { c.Version = 2 }, "version is 2"},
		{"bad ats", func(c *Config) { c.Watchlist[0].ATS = "zz" }, "not one of gh, lv, ab, wd"},
		{"empty token", func(c *Config) { c.Watchlist[0].Token = "  " }, "empty token"},
		{"slash token", func(c *Config) { c.Watchlist[0].Token = "a/b" }, "must not contain"},
		{"bad region", func(c *Config) { c.Watchlist[0].Region = "eu" }, "lever only"},
		{"duplicate", func(c *Config) { c.Watchlist = append(c.Watchlist, c.Watchlist[0]) }, "duplicate board gh/acme"},
		{"bad time", func(c *Config) { c.Schedule.DailyAt = "25:00" }, "not HH:MM"},
		{"bad time format", func(c *Config) { c.Schedule.DailyAt = "8:30" }, "not HH:MM"},
		{"bad zone", func(c *Config) { c.Schedule.Timezone = "Mars/Base" }, "timezone"},
		{"local zone", func(c *Config) { c.Schedule.Timezone = "Local" }, "explicit IANA"},
		{"negative usd", func(c *Config) { c.Decide.DailyUSD.OpenRouter = -1 }, "daily_usd.openrouter"},
		{"huge usd", func(c *Config) { c.Decide.DailyUSD.LLM = 20.01 }, "daily_usd.llm"},
		{"bad provider", func(c *Config) { c.LLM.Provider = "openai" }, "llm.provider"},
		{"workday without host", func(c *Config) { c.Watchlist = []sources.Board{{ATS: posting.Workday, Token: "acme/Ext"}} }, "needs host"},
		{"workday host of another tenant", func(c *Config) {
			c.Watchlist = []sources.Board{{ATS: posting.Workday, Token: "acme/Ext", Host: "globex.wd5.myworkdayjobs.com"}}
		}, "does not belong to tenant"},
		{"workday host not workday", func(c *Config) {
			c.Watchlist = []sources.Board{{ATS: posting.Workday, Token: "acme/Ext", Host: "acme.evil.com"}}
		}, "needs host"},
		{"workday host missing from allowlist", func(c *Config) {
			c.Watchlist = []sources.Board{{ATS: posting.Workday, Token: "acme/Ext", Host: "acme.wd5.myworkdayjobs.com"}}
		}, "allowed_hosts is missing: acme.wd5.myworkdayjobs.com"},
		{"search on non-workday", func(c *Config) { c.Watchlist[0].Search = "ml" }, "search applies to wd, az, gg, ap, ef and jb only"},
		{"page http url", func(c *Config) {
			c.Watchlist = []sources.Board{{ATS: posting.Page, Token: "acme", URL: "http://acme.example/jobs", LinkPattern: "/jobs/"}}
		}, "needs an https url"},
		{"page bad regexp", func(c *Config) {
			c.Watchlist = []sources.Board{{ATS: posting.Page, Token: "acme", URL: "https://acme.example/jobs", LinkPattern: "("}}
		}, "link_pattern"},
		{"page missing pattern", func(c *Config) {
			c.Watchlist = []sources.Board{{ATS: posting.Page, Token: "acme", URL: "https://acme.example/jobs"}}
		}, "needs a link_pattern"},
		{"url on ats board", func(c *Config) { c.Watchlist[0].URL = "https://x.example" }, "page boards only"},
		{"bad shadow", func(c *Config) { c.Decide.Shadow = "lappi" }, "decide.shadow"},
		{"shadow llm without llm", func(c *Config) { c.LLM.Provider = ""; c.LLM.Model = ""; c.Decide.Shadow = "llm" }, "needs llm.provider"},
		{"zero openrouter budget with openrouter", func(c *Config) { c.Decide.OpenRouterModel = "typesafe/jev-1.13"; c.Decide.DailyUSD.OpenRouter = 0 }, "daily_usd.openrouter must be above 0"},
		{"zero llm budget with llm", func(c *Config) { c.LLM = LLM{Provider: "gemini", Model: "m"}; c.Decide.DailyUSD.LLM = 0 }, "daily_usd.llm must be above 0"},
		{"anthropic without prices", func(c *Config) { c.LLM = LLM{Provider: "anthropic", Model: "m"} }, "needs input_nano_usd_per_token"},
		{"absurd price", func(c *Config) { c.LLM.InputNanoUSDPerToken = 2_000_000 }, "per-token price above"},
		{"negative price", func(c *Config) { c.LLM.OutputNanoUSDPerToken = -1 }, "needs input_nano_usd_per_token"},
		{"drop rule", func(c *Config) { c.DropRules = []DropRule{{Slot: "", MinOpenRouterProb: 2}} }, "drop_rules[0]"},
		{"drop rule slot typo", func(c *Config) {
			c.DropRules = []DropRule{{Slot: "seniority_level", Values: []string{"manager"}, MinOpenRouterProb: 0.9}}
		}, `drop_rules[0]: slot "seniority_level" is not a triage slot (want one of `},
		{"drop rule unknown value", func(c *Config) {
			c.DropRules = []DropRule{{Slot: "seniority", Values: []string{"manager", "boss"}, MinOpenRouterProb: 0.9}}
		}, `drop_rules[0]: "boss" is not a seniority value`},
		{"rank penalty empty slot", func(c *Config) { c.RankPenalties = []RankPenalty{{Values: []string{"senior"}}} }, "rank_penalties[0]: slot is empty"},
		{"rank penalty slot typo", func(c *Config) {
			c.RankPenalties = []RankPenalty{{Slot: "seniority_level", Values: []string{"senior"}}}
		}, `rank_penalties[0]: slot "seniority_level" is not a triage slot`},
		{"rank penalty unknown value", func(c *Config) {
			c.RankPenalties = []RankPenalty{{Slot: "seniority", Values: []string{"senior", "boss"}}}
		}, `rank_penalties[0]: "boss" is not a seniority value`},
		{"rank penalty no values", func(c *Config) { c.RankPenalties = []RankPenalty{{Slot: "seniority"}} }, "rank_penalties[0]: values is empty"},
		{"too many rank penalties", func(c *Config) {
			c.RankPenalties = make([]RankPenalty, MaxRankPenalties+1)
			for i := range c.RankPenalties {
				c.RankPenalties[i] = RankPenalty{Slot: "seniority", Values: []string{"senior"}}
			}
		}, "rank_penalties has 33 entries, max 32"},
		{"negative page interval", func(c *Config) { c.PageIntervalMS = -1 }, "page_interval_ms -1"},
		{"page interval over max", func(c *Config) { c.PageIntervalMS = 10_001 }, "page_interval_ms 10001 must be within 0..10000"},
		{"huge page interval", func(c *Config) { c.PageIntervalMS = math.MaxInt }, "page_interval_ms"},
		{"negative advance per run", func(c *Config) { c.AdvancePerRun = -1 }, "advance_per_run -1 must be within 0..5000"},
		{"advance per run over max", func(c *Config) { c.AdvancePerRun = 5001 }, "advance_per_run 5001"},
		{"huge advance per run", func(c *Config) { c.AdvancePerRun = math.MaxInt }, "advance_per_run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestUSDBoundsInclusive(t *testing.T) {
	c := valid()
	c.Decide.OpenRouterModel = "" // a zero cap is only valid with Jev switched off
	c.Decide.DailyUSD = DailyUSD{OpenRouter: 0, LLM: 20}
	if err := c.Validate(); err != nil {
		t.Fatalf("0 and 20 must be valid: %v", err)
	}
}

func TestMissingHostsListed(t *testing.T) {
	c := valid()
	c.AllowedHosts = []string{"api.ashbyhq.com"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, h := range []string{"boards-api.greenhouse.io", "api.eu.lever.co", "openrouter.ai"} {
		if !strings.Contains(err.Error(), h) {
			t.Errorf("error %q does not list %s", err, h)
		}
	}
	if strings.Contains(err.Error(), "missing: api.ashbyhq.com") {
		t.Errorf("present host reported missing: %v", err)
	}
}

func TestBoardDomain(t *testing.T) {
	for _, bad := range []string{"https://www.acme.com", "www.acme.com/", "www.acme.com:443", "WWW.Acme.com",
		" www.acme.com", "acme", "..acme.com", "acme..com", "www.acme.com.", "1.2.3.4", "../logos", "acme com"} {
		b := sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme", Domain: bad}
		if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), "domain") {
			t.Errorf("domain %q: got %v, want a domain error", bad, err)
		}
	}
	c := valid()
	c.Watchlist[0].Domain = "www.acme.com"
	c.Watchlist[1].Domain = "www.acme.com" // two boards may share one site
	if got := c.RequiredHosts(); !slices.Contains(got, "www.acme.com") || len(got) != 5 {
		t.Fatalf("RequiredHosts = %v, want the domain once", got)
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "allowed_hosts is missing: www.acme.com") {
		t.Fatalf("an unallowed domain: got %v", err)
	}
	c.AllowedHosts = append(c.AllowedHosts, "www.acme.com")
	if err := c.Validate(); err != nil {
		t.Fatalf("an allowed domain: %v", err)
	}
	c.Watchlist[0].Domain, c.Watchlist[1].Domain = "", ""
	if err := c.Validate(); err != nil {
		t.Fatalf("a board without a domain stays valid: %v", err)
	}
}

func TestLeverUSHost(t *testing.T) {
	c := valid()
	c.Watchlist = []sources.Board{{ATS: posting.Lever, Token: "g", Company: "G"}}
	c.AllowedHosts = []string{"api.eu.lever.co", "openrouter.ai"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "api.lever.co") {
		t.Fatalf("us lever host should be required, got %v", err)
	}
}

func TestNoJevNoOpenRouterRequirement(t *testing.T) {
	c := valid()
	c.Decide.OpenRouterModel = ""
	c.AllowedHosts = []string{"boards-api.greenhouse.io", "api.eu.lever.co", "api.ashbyhq.com"}
	if err := c.Validate(); err != nil {
		t.Fatalf("openrouter must not be required without jev: %v", err)
	}
}

func TestLoadRejectsUnknownFieldAndTrailing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.json")
	if err := Save(p, valid()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	bad := strings.Replace(string(data), `"version": 1,`, `"version": 1, "surprise": true,`, 1)
	if err := os.WriteFile(p, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("unknown field accepted: %v", err)
	}
	if err := os.WriteFile(p, append(data, []byte(`{}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("trailing data accepted")
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
}

// rank_penalties accepts legal slot values, is omitted when unset, and
// survives a save and load.
func TestRankPenaltiesRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := Save(p, valid()); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); strings.Contains(string(data), "rank_penalties") {
		t.Fatal("unset rank_penalties written")
	}
	c := valid()
	c.RankPenalties = []RankPenalty{{Slot: "seniority", Values: []string{"senior", "staff_plus"}}}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RankPenalties) != 1 || got.RankPenalties[0].Slot != "seniority" || !slices.Equal(got.RankPenalties[0].Values, []string{"senior", "staff_plus"}) {
		t.Fatalf("rank_penalties = %+v", got.RankPenalties)
	}
}

// page_interval_ms is off by default, accepts 1..page.MaxInterval in ms, and
// survives a save and load.
func TestPageInterval(t *testing.T) {
	c := valid()
	if c.PageIntervalMS != 0 || c.PageInterval() != 0 {
		t.Fatalf("default page interval %d", c.PageIntervalMS)
	}
	for _, ms := range []int{0, 1, 5000, int(page.MaxInterval / time.Millisecond)} {
		c.PageIntervalMS = ms
		if err := c.Validate(); err != nil {
			t.Errorf("page_interval_ms %d: %v", ms, err)
		}
	}
	c.PageIntervalMS = 5000
	if c.PageInterval() != 5*time.Second {
		t.Fatalf("PageInterval %s", c.PageInterval())
	}
	p := filepath.Join(t.TempDir(), "c.json")
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), `"page_interval_ms": 5000`) {
		t.Fatalf("saved config lacks page_interval_ms:\n%s", data)
	}
	got, err := Load(p)
	if err != nil || got.PageIntervalMS != 5000 {
		t.Fatalf("loaded %d, %v", got.PageIntervalMS, err)
	}
	over := strings.Replace(string(data), `"page_interval_ms": 5000`, `"page_interval_ms": 60000`, 1)
	if err := os.WriteFile(p, []byte(over), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "page_interval_ms") {
		t.Fatalf("over-max interval loaded: %v", err)
	}
}

func TestSaveAtomicAndMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "config.json")
	if err := Save(p, valid()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Watchlist) != 3 {
		t.Errorf("round trip lost boards: %+v", got.Watchlist)
	}

	// An invalid save must leave the existing file intact and no temp files behind.
	bad := valid()
	bad.Version = 9
	if err := Save(p, bad); err == nil {
		t.Fatal("invalid save accepted")
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("existing config damaged: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("stray files after save: %v", entries)
	}
}

func TestPathsEnvOverride(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	c, s := pathsFor("darwin", get, "/home/u")
	if c != "/home/u/Library/Application Support/Vani/config.json" || s != "/home/u/Library/Application Support/Vani/state" {
		t.Fatalf("defaults wrong: %s %s", c, s)
	}
	env["VANI_CONFIG"] = "/x/c.json"
	env["VANI_STATE_DIR"] = "/x/state"
	c, s = Paths(get, "/home/u")
	if c != "/x/c.json" || s != "/x/state" {
		t.Fatalf("overrides ignored: %s %s", c, s)
	}
}

// Linux used to get the macOS layout (~/Library/Application Support/Vani).
// It follows the XDG base directories, and the VANI_* overrides still win.
func TestLinuxPathsFollowXDG(t *testing.T) {
	for name, tc := range map[string]struct {
		env          map[string]string
		config, stat string
	}{
		"spec defaults": {map[string]string{}, "/home/u/.config/vani/config.json", "/home/u/.local/state/vani"},
		"xdg set":       {map[string]string{"XDG_CONFIG_HOME": "/c", "XDG_STATE_HOME": "/s"}, "/c/vani/config.json", "/s/vani"},
		// The spec: a relative value is invalid and is ignored.
		"relative xdg ignored": {map[string]string{"XDG_CONFIG_HOME": "rel", "XDG_STATE_HOME": "./s"}, "/home/u/.config/vani/config.json", "/home/u/.local/state/vani"},
		"vani overrides win": {map[string]string{"XDG_CONFIG_HOME": "/c", "VANI_CONFIG": "/x/c.json", "VANI_STATE_DIR": "/x/state"},
			"/x/c.json", "/x/state"},
	} {
		get := func(k string) string { return tc.env[k] }
		c, s := pathsFor("linux", get, "/home/u")
		if c != tc.config || s != tc.stat {
			t.Errorf("%s: %s %s, want %s %s", name, c, s, tc.config, tc.stat)
		}
	}
	// Windows: %AppData%\Vani, or the profile's Roaming folder without it;
	// the VANI_* overrides still win.
	for name, tc := range map[string]struct {
		env          map[string]string
		config, stat string
	}{
		"appdata": {map[string]string{"APPDATA": `C:\Users\u\AppData\Roaming`},
			filepath.Join(`C:\Users\u\AppData\Roaming`, "Vani", "config.json"), filepath.Join(`C:\Users\u\AppData\Roaming`, "Vani", "state")},
		"no appdata": {map[string]string{},
			filepath.Join(`C:\Users\u`, "AppData", "Roaming", "Vani", "config.json"), filepath.Join(`C:\Users\u`, "AppData", "Roaming", "Vani", "state")},
		"vani overrides win": {map[string]string{"APPDATA": `C:\A`, "VANI_CONFIG": `D:\c.json`, "VANI_STATE_DIR": `D:\s`}, `D:\c.json`, `D:\s`},
	} {
		get := func(k string) string { return tc.env[k] }
		c, s := pathsFor("windows", get, `C:\Users\u`)
		if c != tc.config || s != tc.stat {
			t.Errorf("windows %s: %s %s, want %s %s", name, c, s, tc.config, tc.stat)
		}
	}
	// XDG variables do not move the macOS layout.
	c, _ := pathsFor("darwin", func(k string) string { return map[string]string{"XDG_CONFIG_HOME": "/c"}[k] }, "/Users/u")
	if c != "/Users/u/Library/Application Support/Vani/config.json" {
		t.Errorf("darwin config %s", c)
	}
}

func TestGeminiPriceDefaults(t *testing.T) {
	in, out, err := LLM{Provider: "gemini", Model: "m"}.Prices()
	if err != nil || in != GeminiInputNanoUSD || out != GeminiOutputNanoUSD {
		t.Fatalf("got %d %d %v", in, out, err)
	}
	in, out, err = LLM{Provider: "anthropic", Model: "m", InputNanoUSDPerToken: 3000, OutputNanoUSDPerToken: 15000}.Prices()
	if err != nil || in != 3000 || out != 15000 {
		t.Fatalf("explicit prices: got %d %d %v", in, out, err)
	}
}

func TestLappiConfigRules(t *testing.T) {
	ok := func(c Config) {
		t.Helper()
		if err := c.Validate(); err != nil {
			t.Fatalf("rejected: %v", err)
		}
	}
	bad := func(c Config, frag string) {
		t.Helper()
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), frag) {
			t.Fatalf("err = %v, want %q", err, frag)
		}
	}
	c := valid()
	c.Decide.Shadow = "lappi"
	bad(c, "decide.shadow lappi needs decide.lappi")
	c.Decide.Lappi = &Lappi{}
	ok(c)
	c.Decide.Lappi = &Lappi{Socket: "qd.sock"}
	bad(c, "decide.lappi.socket")
	c.Decide.Lappi = &Lappi{TimeoutMS: 10_001}
	bad(c, "timeout_ms")
	c.Decide.Lappi = &Lappi{Primary: true}
	bad(c, "exclusive")
	c.Decide.Shadow = ""
	ok(c)
	c.Decide.Lappi = &Lappi{Socket: "/tmp/qd.sock", TimeoutMS: 2000}
	bad(c, "configured but unused")
}

func TestLocalConfigRules(t *testing.T) {
	check := func(c Config, frag string) {
		t.Helper()
		err := c.Validate()
		if frag == "" && err != nil {
			t.Fatalf("rejected: %v", err)
		}
		if frag != "" && (err == nil || !strings.Contains(err.Error(), frag)) {
			t.Fatalf("err = %v, want %q", err, frag)
		}
	}
	good := &Local{Endpoint: "http://127.0.0.1:8791/v1/systemone", Model: "LiquidAI/d1-3B-GGUF:Q8_0"}
	c := valid()
	c.Decide.Shadow = "local"
	check(c, "decide.shadow local needs decide.local")
	c.Decide.Local = good
	check(c, "")
	for _, ep := range []string{"http://localhost:8791/v1/systemone", "http://[::1]:8791/v1/systemone", "https://127.0.0.1/x"} {
		c.Decide.Local = &Local{Endpoint: ep, Model: "m"}
		check(c, "")
	}
	// Postings never leave this Mac through the local backend.
	for _, ep := range []string{"https://openrouter.ai/api/alpha/decisions", "http://10.0.0.2:8791/v1/systemone",
		"http://127.0.0.1.example.com/x", "http://localhost.example.com/x"} {
		c.Decide.Local = &Local{Endpoint: ep, Model: "m"}
		check(c, "loopback host")
	}
	for ep, frag := range map[string]string{"": "absolute URL", "127.0.0.1:8791": "", "ftp://127.0.0.1/x": "http or https", "http://u:p@127.0.0.1/x": "credentials"} {
		c.Decide.Local = &Local{Endpoint: ep, Model: "m"}
		if frag == "" {
			if c.Validate() == nil {
				t.Errorf("endpoint %q accepted", ep)
			}
			continue
		}
		check(c, frag)
	}
	c.Decide.Local = &Local{Endpoint: good.Endpoint}
	check(c, "decide.local.model is required")
	c.Decide.Local = &Local{Endpoint: good.Endpoint, Model: "m", TimeoutMS: MaxLocalTimeoutMS + 1}
	check(c, "decide.local.timeout_ms")
	for _, q := range []string{"", "combined", "per_slot"} {
		c.Decide.Local = &Local{Endpoint: good.Endpoint, Model: "m", Questions: q}
		check(c, "")
	}
	c.Decide.Local = &Local{Endpoint: good.Endpoint, Model: "m", Questions: "perslot"}
	check(c, "decide.local.questions")

	srv := &LocalServer{Binary: "/opt/homebrew/bin/llama-server", ModelPath: "/m/d1.gguf"}
	c.Decide.Local = &Local{Model: "m", Server: srv}
	check(c, "")
	c.Decide.Local = &Local{Model: "m", Server: srv, Endpoint: good.Endpoint}
	check(c, "not both")
	for frag, s := range map[string]*LocalServer{
		"server.binary":           {Binary: "llama-server", ModelPath: "/m/d1.gguf"},
		"server.model_path":       {Binary: "/b", ModelPath: "d1.gguf"},
		"server.context_size":     {Binary: "/b", ModelPath: "/m", ContextSize: 100},
		"server.start_timeout_ms": {Binary: "/b", ModelPath: "/m", StartTimeoutMS: 300_001},
		"server.batch_size":       {Binary: "/b", ModelPath: "/m", BatchSize: 16},
		"within 32..8192":         {Binary: "/b", ModelPath: "/m", BatchSize: 8193},
		"within 32..4096":         {Binary: "/b", ModelPath: "/m", ContextSize: 4096, BatchSize: 8192},
		"server.parallel":         {Binary: "/b", ModelPath: "/m", Parallel: 17},
		"parallel -1":             {Binary: "/b", ModelPath: "/m", Parallel: -1},
	} {
		c.Decide.Local = &Local{Model: "m", Server: s}
		check(c, frag)
	}
	// vllm-d3 as the shadow: the whole prompt in one 8192-token batch, one slot.
	c.Decide.Local = &Local{Model: "m", Server: &LocalServer{Binary: "/b", ModelPath: "/m/d3.gguf", BatchSize: 8192, Parallel: 1}}
	check(c, "")
	c.Decide.Local = good
	c.Decide.Shadow = ""
	check(c, "decide.local is configured but unused")
}

func TestToolPathsMustBeAbsolute(t *testing.T) {
	for name, set := range map[string]func(*Config){
		"kb.bank":           func(c *Config) { c.KB.Bank = "bank.json" },
		"kb.rkb":            func(c *Config) { c.KB.RKB = "bin/rkb" },
		"kb.code_root":      func(c *Config) { c.KB.CodeRoot = "Code" },
		"lightpanda_binary": func(c *Config) { c.LightpandaBinary = "lightpanda" },
		"devprism.binary":   func(c *Config) { c.DevPrism.Binary = "devprism" },
	} {
		c := valid()
		set(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s relative: err = %v", name, err)
		}
	}
	c := valid()
	c.KB = KB{Bank: "/a/bank.json", RKB: "/a/rkb", CodeRoot: "/a"}
	c.LightpandaBinary = "/a/lightpanda"
	if err := c.Validate(); err != nil {
		t.Fatalf("absolute paths rejected: %v", err)
	}
}

func TestSearchSiteBoards(t *testing.T) {
	good := []sources.Board{
		{ATS: posting.Amazon, Token: "amazon", Company: "Amazon", Searches: []string{"LLM inference", "machine learning"},
			Filters: map[string][]string{"normalized_country_code[]": {"USA"}, "category[]": {"machine-learning-science"}}},
		{ATS: posting.Google, Token: "google", Company: "Google", Search: "LLM", Filters: map[string][]string{"target_level": {"EARLY", "MID"}}},
		{ATS: posting.Apple, Token: "apple", Company: "Apple", Search: `"LLM inference"`, Filters: map[string][]string{"location": {"united-states-USA"}}},
		{ATS: posting.Eightfold, Token: "qualcomm.com", Company: "Qualcomm", Host: "careers.qualcomm.com", Search: "machine learning"},
		{ATS: posting.Workday, Token: "nvidia/NVIDIAExternalCareerSite", Company: "NVIDIA", Host: "nvidia.wd5.myworkdayjobs.com",
			Searches: []string{"TensorRT", "new college grad"}},
		{ATS: posting.Jibe, Token: "amd", Company: "AMD", Host: "careers.amd.com", Search: "machine learning",
			Filters: map[string][]string{"location": {"United States", "Canada"}}},
		// Jibe, like Workday, lists the whole site without a search.
		{ATS: posting.Jibe, Token: "acme", Company: "Acme", Host: "careers.acme.example"},
		{ATS: posting.Workday, Token: "intel/External", Company: "Intel", Host: "intel.wd1.myworkdayjobs.com",
			Search: "LLM", MaxPostings: sources.MaxPostingsCeiling},
	}
	c := valid()
	c.Watchlist = append(c.Watchlist, good...)
	c.AllowedHosts = append(c.AllowedHosts, "www.amazon.jobs", "www.google.com", "jobs.apple.com", "careers.qualcomm.com", "nvidia.wd5.myworkdayjobs.com",
		"careers.amd.com", "careers.acme.example", "intel.wd1.myworkdayjobs.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"www.amazon.jobs", "www.google.com", "jobs.apple.com", "careers.qualcomm.com", "careers.amd.com"} {
		if !slices.Contains(c.RequiredHosts(), h) {
			t.Errorf("RequiredHosts lacks %s", h)
		}
	}

	long := strings.Repeat("x", sources.MaxSearchLen+1)
	many := make([]string, sources.MaxSearches+1)
	for i := range many {
		many[i] = fmt.Sprint("term", i)
	}
	mod := func(i int, f func(*sources.Board)) sources.Board { b := good[i]; f(&b); return b }
	for name, tc := range map[string]struct {
		b    sources.Board
		want string
	}{
		"amazon without search":     {mod(0, func(b *sources.Board) { b.Searches = nil }), "needs at least one search"},
		"blank searches only":       {mod(1, func(b *sources.Board) { b.Search, b.Searches = " ", []string{""} }), "needs at least one search"},
		"too many searches":         {mod(1, func(b *sources.Board) { b.Searches = many }), "searches, more than"},
		"search too long":           {mod(1, func(b *sources.Board) { b.Search = long }), "at most"},
		"control char in search":    {mod(1, func(b *sources.Board) { b.Search = "a\nb" }), "control"},
		"unverified filter":         {mod(1, func(b *sources.Board) { b.Filters = map[string][]string{"degree": {"MASTERS"}} }), "is not one of"},
		"unverified eightfold":      {mod(3, func(b *sources.Board) { b.Filters = map[string][]string{"department": {"x"}} }), "is not one of"},
		"filter on workday":         {mod(4, func(b *sources.Board) { b.Filters = map[string][]string{"x": {"y"}} }), "not supported"},
		"empty filter values":       {mod(1, func(b *sources.Board) { b.Filters = map[string][]string{"target_level": {}} }), "values"},
		"blank filter value":        {mod(1, func(b *sources.Board) { b.Filters = map[string][]string{"target_level": {" "}} }), "value"},
		"search on greenhouse":      {sources.Board{ATS: posting.Greenhouse, Token: "acme", Search: "x"}, "search applies"},
		"host on amazon":            {mod(0, func(b *sources.Board) { b.Host = "www.amazon.jobs" }), "host applies"},
		"eightfold without host":    {mod(3, func(b *sources.Board) { b.Host = "" }), "needs host"},
		"eightfold ip host":         {mod(3, func(b *sources.Board) { b.Host = "10.0.0.1" }), "needs host"},
		"eightfold token not dns":   {mod(3, func(b *sources.Board) { b.Token = "qualcomm" }), "domain"},
		"jibe without host":         {mod(5, func(b *sources.Board) { b.Host = "" }), "needs host"},
		"jibe ip host":              {mod(5, func(b *sources.Board) { b.Host = "10.0.0.1" }), "needs host"},
		"jibe host with path":       {mod(5, func(b *sources.Board) { b.Host = "careers.amd.com/x" }), "needs host"},
		"jibe token not a code":     {mod(5, func(b *sources.Board) { b.Token = "amd.com" }), "client code"},
		"unverified jibe filter":    {mod(5, func(b *sources.Board) { b.Filters = map[string][]string{"country": {"Canada"}} }), "is not one of"},
		"url on jibe":               {mod(5, func(b *sources.Board) { b.URL = "https://careers.amd.com/careers-home/jobs" }), "page boards only"},
		"unknown ats":               {sources.Board{ATS: "zz", Token: "x"}, "is not one of"},
		"max_postings on amazon":    {mod(0, func(b *sources.Board) { b.MaxPostings = 300 }), "applies to wd only"},
		"max_postings on eightfold": {mod(3, func(b *sources.Board) { b.MaxPostings = 300 }), "applies to wd only"},
		"max_postings on jibe":      {mod(5, func(b *sources.Board) { b.MaxPostings = 300 }), "applies to wd only"},
		"max_postings negative":     {mod(4, func(b *sources.Board) { b.MaxPostings = -1 }), "must be within"},
		"max_postings over ceiling": {mod(4, func(b *sources.Board) { b.MaxPostings = sources.MaxPostingsCeiling + 1 }), "must be within"},
	} {
		if err := ValidBoard(tc.b); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}

	// One board per single-tenant site: a second would key postings twice.
	c.Watchlist = append(c.Watchlist, sources.Board{ATS: posting.Google, Token: "google-2", Company: "Google", Search: "TPU"})
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "second gg board") {
		t.Fatalf("second google board: %v", err)
	}
}

// Unsaved browser scrapes fold under "<host-slug>-scrape" (vani jobs ingest); a
// board with that token end would age those records out. A hand-edited
// config.json must be refused as well as `watchlist add`.
func TestBoardTokenCannotEndInTheScrapeSuffix(t *testing.T) {
	for _, tok := range []string{"careers-acme-com-scrape", "Acme-Scrape", "-scrape"} {
		b := sources.Board{ATS: posting.Page, Token: tok, URL: "https://careers.acme.com/jobs", LinkPattern: `^https://careers\.acme\.com/jobs/`}
		if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("ValidBoard(%q) = %v, want the reserved-suffix refusal", tok, err)
		}
		c := valid()
		c.Watchlist = append(c.Watchlist, b)
		c.AllowedHosts = append(c.AllowedHosts, "careers.acme.com")
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("Validate with token %q = %v, want the reserved-suffix refusal", tok, err)
		}
	}
	// Only the end is reserved.
	for _, tok := range []string{"scrape", "scrape-acme", "acme-scraper"} {
		b := sources.Board{ATS: posting.Greenhouse, Token: tok}
		if err := ValidBoard(b); err != nil {
			t.Errorf("ValidBoard(%q) = %v", tok, err)
		}
	}
}

func TestWorkdayAndPageBoardsValid(t *testing.T) {
	c := valid()
	c.Watchlist = append(c.Watchlist,
		sources.Board{ATS: posting.Workday, Token: "acme/External", Host: "acme.wd5.myworkdayjobs.com", Search: "machine learning"},
		sources.Board{ATS: posting.Page, Token: "initech-careers", URL: "https://careers.initech.example/jobs", LinkPattern: `^https://careers\.initech\.example/jobs/\d+$`},
	)
	c.AllowedHosts = append(c.AllowedHosts, "acme.wd5.myworkdayjobs.com", "careers.initech.example")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

// advance_per_run: zero reads as the default, the bounds themselves are valid.
func TestAdvanceLimit(t *testing.T) {
	for _, tc := range []struct{ set, want int }{{0, DefaultAdvancePerRun}, {1, 1}, {MaxAdvancePerRun, MaxAdvancePerRun}} {
		c := valid()
		c.AdvancePerRun = tc.set
		if err := c.Validate(); err != nil {
			t.Fatalf("advance_per_run %d: %v", tc.set, err)
		}
		if got := c.AdvanceLimit(); got != tc.want {
			t.Fatalf("advance_per_run %d: limit %d, want %d", tc.set, got, tc.want)
		}
	}
}

// decide.shadow_every: zero reads as the default, the bounds themselves are
// valid, anything outside them is refused.
func TestShadowSampleEvery(t *testing.T) {
	for _, tc := range []struct{ set, want int }{{0, DefaultShadowEvery}, {1, 1}, {MaxShadowEvery, MaxShadowEvery}} {
		c := valid()
		c.Decide.ShadowEvery = tc.set
		if err := c.Validate(); err != nil {
			t.Fatalf("shadow_every %d: %v", tc.set, err)
		}
		if got := c.Decide.ShadowSampleEvery(); got != tc.want {
			t.Fatalf("shadow_every %d: every %d, want %d", tc.set, got, tc.want)
		}
	}
	for _, bad := range []int{-1, MaxShadowEvery + 1, math.MaxInt} {
		c := valid()
		c.Decide.ShadowEvery = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "decide.shadow_every") {
			t.Fatalf("shadow_every %d: err %v", bad, err)
		}
	}
}

// advance_concurrency: zero reads as the default, the bounds themselves are
// valid, anything outside them is refused.
func TestAdvanceWorkers(t *testing.T) {
	for _, tc := range []struct{ set, want int }{{0, DefaultAdvanceConcurrency}, {1, 1}, {MaxAdvanceConcurrency, MaxAdvanceConcurrency}} {
		c := valid()
		c.AdvanceConcurrency = tc.set
		if err := c.Validate(); err != nil {
			t.Fatalf("advance_concurrency %d: %v", tc.set, err)
		}
		if got := c.AdvanceWorkers(); got != tc.want {
			t.Fatalf("advance_concurrency %d: workers %d, want %d", tc.set, got, tc.want)
		}
	}
	for _, bad := range []int{-1, MaxAdvanceConcurrency + 1, math.MaxInt} {
		c := valid()
		c.AdvanceConcurrency = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "advance_concurrency") {
			t.Fatalf("advance_concurrency %d: err %v", bad, err)
		}
	}
}
