package sources

import (
	"net/url"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestBoardFromURL(t *testing.T) {
	for name, tc := range map[string]struct {
		raw    string
		known  bool
		err    string // a substring of the reason, "" for a board
		ats    posting.ATS
		token  string
		host   string
		region string
		search string
		board  string
	}{
		"greenhouse posting":     {raw: "https://boards.greenhouse.io/acme/jobs/1", known: true, ats: posting.Greenhouse, token: "acme"},
		"greenhouse new host":    {raw: "https://job-boards.greenhouse.io/acme/jobs/1?utm_source=x", known: true, ats: posting.Greenhouse, token: "acme"},
		"greenhouse no board":    {raw: "https://boards.greenhouse.io/", known: true, err: "names no Greenhouse board"},
		"greenhouse mixed case":  {raw: "https://Boards.Greenhouse.io/Acme/jobs/1", known: true, ats: posting.Greenhouse, token: "Acme"},
		"greenhouse EU host":     {raw: "https://job-boards.eu.greenhouse.io/acme/jobs/1"},
		"greenhouse custom page": {raw: "https://careers.acme.com/?gh_jid=1"},
		"lever":                  {raw: "https://jobs.lever.co/acme/uuid", known: true, ats: posting.Lever, token: "acme"},
		"lever eu":               {raw: "https://jobs.eu.lever.co/acme", known: true, ats: posting.Lever, token: "acme", region: "eu"},
		"lever no board":         {raw: "https://jobs.lever.co", known: true, err: "names no Lever board"},
		"gem":                    {raw: "https://jobs.gem.com/acme/abc", known: true, ats: posting.Gem, token: "acme"},
		"gem no board":           {raw: "https://jobs.gem.com/", known: true, err: "names no Gem board"},
		"smartrecruiters":        {raw: "https://careers.smartrecruiters.com/Acme/1-x", known: true, ats: posting.SmartRecruiters, token: "Acme"},
		"smartrecruiters jobs":   {raw: "https://jobs.smartrecruiters.com/Acme", known: true, ats: posting.SmartRecruiters, token: "Acme"},
		"smartrecruiters app":    {raw: "https://jobs.smartrecruiters.com/oneclick-ui/company/Acme", known: true, err: "names no SmartRecruiters company"},
		"smartrecruiters ext":    {raw: "https://jobs.smartrecruiters.com/External-Referrals/x", known: true, err: "names no SmartRecruiters company"},
		"ashby":                  {raw: "https://jobs.ashbyhq.com/acme/uuid", known: true, ats: posting.Ashby, token: "acme"},
		"ashby space":            {raw: "https://jobs.ashbyhq.com/Hippocratic%20AI/uuid", known: true, ats: posting.Ashby, token: "hippocratic-ai", board: "Hippocratic AI"},
		"ashby dotted":           {raw: "https://jobs.ashbyhq.com/careers.azx.io/uuid", known: true, ats: posting.Ashby, token: "careers.azx.io"},
		"ashby refused name":     {raw: "https://jobs.ashbyhq.com/a%20%2Fb", known: true, ats: posting.Ashby, token: "a "},
		"ashby no board":         {raw: "https://jobs.ashbyhq.com/", known: true, err: "names no Ashby board"},
		"workable":               {raw: "https://apply.workable.com/acme/j/ABC123/", known: true, ats: posting.Workable, token: "acme"},
		"workable account page":  {raw: "https://apply.workable.com/acme/", known: true, ats: posting.Workable, token: "acme"},
		"workable shortlink":     {raw: "https://apply.workable.com/j/ABC123", known: true, err: "names no Workable account"},
		"workable no account":    {raw: "https://apply.workable.com/", known: true, err: "names no Workable account"},
		"rippling":               {raw: "https://ats.rippling.com/acme/jobs/uuid", known: true, ats: posting.Rippling, token: "acme"},
		"rippling locale":        {raw: "https://ats.rippling.com/en-GB/acme/jobs/uuid", known: true, ats: posting.Rippling, token: "acme"},
		"rippling bare job":      {raw: "https://ats.rippling.com/jobs/uuid", known: true, err: "names no Rippling board"},
		"rippling locale only":   {raw: "https://ats.rippling.com/en-GB", known: true, err: "names no Rippling board"},
		"workday":                {raw: "https://r1rcm.wd1.myworkdayjobs.com/R1RCM/job/Remote-USA/x_R1", known: true, ats: posting.Workday, token: "r1rcm/R1RCM", host: "r1rcm.wd1.myworkdayjobs.com"},
		"workday locale":         {raw: "https://netflix.wd108.myworkdayjobs.com/en-US/Netflix/job/x", known: true, ats: posting.Workday, token: "netflix/Netflix", host: "netflix.wd108.myworkdayjobs.com"},
		"workday search":         {raw: "https://acme.wd5.myworkdayjobs.com/Careers?q=ml%20engineer", known: true, ats: posting.Workday, token: "acme/Careers", host: "acme.wd5.myworkdayjobs.com", search: "ml engineer"},
		"workday site is apply":  {raw: "https://ebay.wd5.myworkdayjobs.com/apply/job/San-Jose/x", known: true, ats: posting.Workday, token: "ebay/apply", host: "ebay.wd5.myworkdayjobs.com"},
		"workday no site":        {raw: "https://acme.wd5.myworkdayjobs.com/", known: true, err: "names no Workday site"},
		"workday only a locale":  {raw: "https://acme.wd5.myworkdayjobs.com/en-US", known: true, err: "names no Workday site"},
		"workday shared host":    {raw: "https://wd3.myworkdaysite.com/en-US/recruiting/acme/Careers/job/1", known: true, ats: posting.Workday, token: "acme/Careers", host: "wd3.myworkdaysite.com"},
		"workday shared, no tnt": {raw: "https://wd3.myworkdaysite.com/recruiting/acme", known: true, err: "names no Workday site"},
		"oracle":                 {raw: "https://eeho.fa.us2.oraclecloud.com/hcmUI/CandidateExperience/en/sites/CX_1/jobs?keyword=ml", known: true, ats: posting.OracleHCM, token: "eeho/CX_1", host: "eeho.fa.us2.oraclecloud.com", search: "ml"},
		"oracle other page":      {raw: "https://eeho.fa.us2.oraclecloud.com/other/path", known: true, err: "names no Oracle HCM site"},
		"recruitee":              {raw: "https://syrve.recruitee.com/o/backend-engineer", known: true, ats: posting.Recruitee, token: "syrve"},
		"pinpoint":               {raw: "https://workwithus.pinpointhq.com/postings/abc", known: true, ats: posting.Pinpoint, token: "workwithus"},
		"bamboohr":               {raw: "https://sortitt.bamboohr.com/careers/76", known: true, ats: posting.BambooHR, token: "sortitt"},
		"personio":               {raw: "https://deskbird.jobs.personio.com/job/2759612?language=en", known: true, ats: posting.Personio, token: "deskbird"},
		"tenant mixed case":      {raw: "https://Syrve.Recruitee.com/o/x", known: true, ats: posting.Recruitee, token: "syrve"},
		"tenant two labels":      {raw: "https://a.b.recruitee.com/o/x", known: true, err: "names no rc board"},
		"recruitee api host":     {raw: "https://api.recruitee.com/c/1/offers"},
		"bamboohr www":           {raw: "https://www.bamboohr.com/careers"},
		"pinpoint app":           {raw: "https://app.pinpointhq.com/"},
		"personio bare jobs":     {raw: "https://jobs.personio.com/"},
		"personio de not read":   {raw: "https://deskbird.jobs.personio.de/job/1"},
		"tenant lookalike":       {raw: "https://acme.recruitee.com.evil.test/o/x"},
		"tenant suffix only":     {raw: "https://evilrecruitee.com/o/x"},
		"not an ATS host":        {raw: "https://www.example.com/careers"},
		"lookalike host":         {raw: "https://jobs.lever.co.evil.test/acme"},
		"suffix lookalike":       {raw: "https://notjobs.lever.co/acme"},
		"workday lookalike":      {raw: "https://acme.wd5.myworkdayjobs.com.evil.test/Careers"},
	} {
		t.Run(name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			b, known, err := BoardFromURL(u)
			if known != tc.known {
				t.Fatalf("known = %v, want %v (board %+v, err %v)", known, tc.known, b, err)
			}
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one holding %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tc.known {
				if b.ATS != "" || b.Token != "" {
					t.Errorf("a page that is no ATS's gave %+v", b)
				}
				return
			}
			if b.ATS != tc.ats || b.Token != tc.token || b.Host != tc.host || b.Region != tc.region || b.Search != tc.search || b.BoardName != tc.board || b.Company != "" {
				t.Errorf("board %+v, want %s/%s host %q region %q search %q board name %q", b, tc.ats, tc.token, tc.host, tc.region, tc.search, tc.board)
			}
		})
	}
}
