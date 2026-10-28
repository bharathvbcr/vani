package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// Every board kind the config accepts has an adapter, so a valid board is
// never skipped with "no adapter". Careers pages get theirs per run.
func TestEveryAcceptedBoardKindHasAnAdapter(t *testing.T) {
	srcs := apiSources(srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) { return srctest.Reply{}, false }))
	for _, a := range []posting.ATS{posting.Greenhouse, posting.Lever, posting.Ashby, posting.Workday,
		posting.Amazon, posting.Google, posting.Apple, posting.Eightfold, posting.Jibe,
		posting.Gem, posting.SmartRecruiters, posting.IBM, posting.Avature, posting.DEShaw, posting.Atlassian, posting.OracleHCM,
		posting.Rippling, posting.Workable, posting.Recruitee, posting.Pinpoint, posting.BambooHR, posting.Personio,
		posting.TikTok, posting.TalentBrew, posting.Feed, posting.Page, posting.Other, "zz"} {
		err := config.ValidBoard(sources.Board{ATS: a})
		accepted := err == nil || !strings.Contains(err.Error(), "is not one of")
		_, has := srcs[a]
		switch {
		case a == posting.Page:
			if !accepted || has {
				t.Errorf("pg: accepted %v, api adapter %v; want accepted with a per-run adapter", accepted, has)
			}
		case accepted != has:
			t.Errorf("%s: config accepts %v, adapter %v", a, accepted, has)
		}
	}
}
