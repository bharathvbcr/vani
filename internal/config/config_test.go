package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
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
		{"search on non-workday", func(c *Config) { c.Watchlist[0].Search = "ml" }, "search applies to wd, az, gg, ap and ef only"},
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
	c, s := Paths(get, "/home/u")
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
	} {
		c.Decide.Local = &Local{Model: "m", Server: s}
		check(c, frag)
	}
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
	}
	c := valid()
	c.Watchlist = append(c.Watchlist, good...)
	c.AllowedHosts = append(c.AllowedHosts, "www.amazon.jobs", "www.google.com", "jobs.apple.com", "careers.qualcomm.com", "nvidia.wd5.myworkdayjobs.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"www.amazon.jobs", "www.google.com", "jobs.apple.com", "careers.qualcomm.com"} {
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
		"amazon without search":   {mod(0, func(b *sources.Board) { b.Searches = nil }), "needs at least one search"},
		"blank searches only":     {mod(1, func(b *sources.Board) { b.Search, b.Searches = " ", []string{""} }), "needs at least one search"},
		"too many searches":       {mod(1, func(b *sources.Board) { b.Searches = many }), "searches, more than"},
		"search too long":         {mod(1, func(b *sources.Board) { b.Search = long }), "at most"},
		"control char in search":  {mod(1, func(b *sources.Board) { b.Search = "a\nb" }), "control"},
		"unverified filter":       {mod(1, func(b *sources.Board) { b.Filters = map[string][]string{"degree": {"MASTERS"}} }), "is not one of"},
		"unverified eightfold":    {mod(3, func(b *sources.Board) { b.Filters = map[string][]string{"department": {"x"}} }), "is not one of"},
		"filter on workday":       {mod(4, func(b *sources.Board) { b.Filters = map[string][]string{"x": {"y"}} }), "not supported"},
		"empty filter values":     {mod(1, func(b *sources.Board) { b.Filters = map[string][]string{"target_level": {}} }), "values"},
		"blank filter value":      {mod(1, func(b *sources.Board) { b.Filters = map[string][]string{"target_level": {" "}} }), "value"},
		"search on greenhouse":    {sources.Board{ATS: posting.Greenhouse, Token: "acme", Search: "x"}, "search applies"},
		"host on amazon":          {mod(0, func(b *sources.Board) { b.Host = "www.amazon.jobs" }), "host applies"},
		"eightfold without host":  {mod(3, func(b *sources.Board) { b.Host = "" }), "needs host"},
		"eightfold ip host":       {mod(3, func(b *sources.Board) { b.Host = "10.0.0.1" }), "needs host"},
		"eightfold token not dns": {mod(3, func(b *sources.Board) { b.Token = "qualcomm" }), "domain"},
		"unknown ats":             {sources.Board{ATS: "zz", Token: "x"}, "is not one of"},
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
