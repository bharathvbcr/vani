package sources

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestSearchTerms(t *testing.T) {
	for name, tc := range map[string]struct {
		b    Board
		want []string
	}{
		"none":            {Board{}, nil},
		"search only":     {Board{Search: " TensorRT "}, []string{"TensorRT"}},
		"union in order":  {Board{Search: "a", Searches: []string{"b", "c"}}, []string{"a", "b", "c"}},
		"case duplicates": {Board{Search: "LLM", Searches: []string{"llm", " Llm ", "CUDA"}}, []string{"LLM", "CUDA"}},
		"empty terms":     {Board{Searches: []string{"", "  ", "x"}}, []string{"x"}},
	} {
		if got := tc.b.SearchTerms(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestFilterKeysOnlyForSearchSites(t *testing.T) {
	for ats := range FilterKeys {
		switch ats {
		case posting.Google, posting.Amazon, posting.Apple, posting.Eightfold, posting.Jibe, posting.SmartRecruiters, posting.TikTok, posting.Himalayas, posting.ZipRecruiter, posting.Dice, posting.Adzuna:
		default:
			t.Errorf("filters listed for %q, which has no verified filters", ats)
		}
	}
}

func TestKeyPrefixForSearchSites(t *testing.T) {
	for _, tc := range []struct {
		b    Board
		want string
	}{
		{Board{ATS: posting.Amazon, Token: "Amazon"}, "az:amazon:"},
		{Board{ATS: posting.Eightfold, Token: "microsoft.com"}, "ef:microsoft.com:"},
		{Board{ATS: posting.Jibe, Token: "AMD"}, "jb:amd:"},
		{Board{ATS: posting.SmartRecruiters, Token: "ServiceNow"}, "sr:servicenow:"},
		{Board{ATS: posting.IBM, Token: "Confluent"}, "ib:confluent:"},
		{Board{ATS: posting.Avature, Token: "TwoSigma"}, "av:twosigma:"},
		{Board{ATS: posting.Rippling, Token: "Aalo"}, "rp:aalo:"},
		{Board{ATS: posting.Workable, Token: "HuggingFace"}, "wk:huggingface:"},
		{Board{ATS: posting.Gem, Token: "Groq"}, "gm:groq:"},
		{Board{ATS: posting.Recruitee, Token: "Syrve"}, "rc:syrve:"},
		{Board{ATS: posting.Pinpoint, Token: "WorkWithUs"}, "pp:workwithus:"},
		{Board{ATS: posting.BambooHR, Token: "Sortitt"}, "bh:sortitt:"},
		{Board{ATS: posting.Personio, Token: "Deskbird"}, "ps:deskbird:"},
		{Board{ATS: posting.Breezy, Token: "ThirdChannel-Inc"}, "bz:thirdchannel-inc:"},
		{Board{ATS: posting.Teamtailor, Token: "Polestar"}, "tm:polestar:"},
		{Board{ATS: posting.HireHive, Token: "AMCSGroup"}, "hv:amcsgroup:"},
		{Board{ATS: posting.HiBob, Token: "Corlytics"}, "hb:corlytics:"},
		{Board{ATS: posting.TikTok, Token: "TikTok"}, "tt:tiktok:"},
		{Board{ATS: posting.TalentBrew, Token: "Intuit"}, "tb:intuit:"},
		{Board{ATS: posting.Feed, Token: "Shopify"}, "fd:shopify:"},
		{Board{ATS: posting.Workday, Token: "nvidia/Site"}, "wd:nvidia:site:"},
		{Board{ATS: posting.OracleHCM, Token: "JPMC/CX_1001"}, "oc:jpmc:cx_1001:"},
	} {
		if got := tc.b.KeyPrefix(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.b, got, tc.want)
		}
	}
}

func TestOracleHCMHostTenant(t *testing.T) {
	for host, want := range map[string]string{
		"jpmc.fa.oraclecloud.com":          "jpmc",
		" JPMC.FA.ORACLECLOUD.COM ":        "jpmc",
		"eeho.fa.us2.oraclecloud.com":      "eeho",
		"acme-corp.fa.em2.oraclecloud.com": "acme-corp",
	} {
		if got, ok := OracleHCMHostTenant(host); !ok || got != want {
			t.Errorf("%q: %q, %v; want %q", host, got, ok, want)
		}
	}
	for _, host := range []string{
		"", "oraclecloud.com", "jpmc.oraclecloud.com", "fa.jpmc.oraclecloud.com", "jpmc.fa.example.com",
		"jpmc.fa.oraclecloud.com.evil.test", "evil.jpmc.fa.oraclecloud.com", "jpmc.fa.us-2.oraclecloud.com",
		"jpmc.fa.us2.em2.oraclecloud.com", "jpmc.fa.oraclecloud.com:443", "x@jpmc.fa.oraclecloud.com",
		"jpmc.fa.oraclecloud.com/x", "-jpmc.fa.oraclecloud.com", ".fa.oraclecloud.com", "jpmc.fa.oraclecloud.com.",
		"jpmc.wd5.myworkdayjobs.com", "jpmc.fa.u2.oraclecloud.com", "jpmc.fa.usa2.oraclecloud.com",
	} {
		if got, ok := OracleHCMHostTenant(host); ok {
			t.Errorf("%q accepted as tenant %q", host, got)
		}
	}
}

// An Oracle Recruiting site is served from {tenant}.fa[.{region}].oraclecloud.com
// or from the company's own domain in front of the same API (Dell,
// enterpriseplatform.dell.com, 2026-11-17); every other host is refused.
func TestOracleHCMHostProblem(t *testing.T) {
	for _, tc := range []struct{ host, tenant string }{
		{"jpmc.fa.oraclecloud.com", "jpmc"},
		{"eeho.fa.us2.oraclecloud.com", "EEHO"},
		{"enterpriseplatform.dell.com", "dell"},
		{" Enterpriseplatform.Dell.com ", "dell"},
		{"dell.com", "dell"},
		{"jobs.akamai.com", "akamai"},
	} {
		if why := OracleHCMHostProblem(tc.host, tc.tenant); why != "" {
			t.Errorf("%q for %q refused: %s", tc.host, tc.tenant, why)
		}
	}
	for _, tc := range []struct{ host, tenant string }{
		{"", "dell"},
		{"chase.fa.oraclecloud.com", "jpmc"},
		{"jpmc.fa.oraclecloud.com.evil.test", "jpmc"},
		{"evil.jpmc.fa.oraclecloud.com", "jpmc"},
		{"jpmc.oraclecloud.com", "jpmc"},
		{"oraclecloud.com", "oraclecloud"},
		{"jpmc.fa.example.com", "jpmc"},
		{"jpmc.wd5.myworkdayjobs.com", "jpmc"},
		{"dell.evil.com", "dell"},
		{"enterpriseplatform.dell.com.evil.test", "dell"},
		{"enterpriseplatform.dell.co.uk", "dell"},
		{"dell", "dell"},
		{"10.0.0.1", "10"},
		{"enterpriseplatform.dell.com:443", "dell"},
		{"x@enterpriseplatform.dell.com", "dell"},
		{"enterpriseplatform.dell.com/x", "dell"},
		{"enterpriseplatform.dell.com.", "dell"},
		{"-x.dell.com", "dell"},
		// a label over 63 bytes
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.dell.com", "dell"},
	} {
		if why := OracleHCMHostProblem(tc.host, tc.tenant); why == "" {
			t.Errorf("%q for %q accepted", tc.host, tc.tenant)
		}
	}
}
