package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// approvedReads is the title_signals.read the owner approved on 2026-10-19
// (measured against the live ledger: no strong_fit or partial_fit dropped).
var approvedReads = []TitleRead{
	{Slot: "seniority", Value: "staff_plus",
		Match:  []string{"Staff", "Principal", "Distinguished"},
		Unless: []string{"Member of Technical Staff", "Technical Staff", "Member of Design Staff", "Senior/Staff"}},
	{Slot: "seniority", Value: "manager",
		Match: []string{"Manager", "Director", "Head of", "VP", "Vice President", "Chief"}},
	{Slot: "role_fit", Value: "mismatch_domain",
		Match: []string{"Sales", "Account Executive", "Account Manager", "Recruiter", "Recruiting", "Legal", "Counsel",
			"Paralegal", "Attorney", "Marketing", "Finance", "Financial", "Accountant", "Accounting", "Tax",
			"Payroll", "Treasury", "Designer", "Communications", "Policy", "People", "Human Resources",
			"Business Development", "Partnerships", "Customer Success", "Executive Assistant", "Facilities",
			"Workplace", "Procurement", "Sourcing", "Brand", "Editor", "Writer", "Events", "Economist",
			"Program Manager", "Product Manager"},
		Unless: []string{"Engineer", "Engineering", "Developer", "Scientist", "Science", "Researcher", "Research",
			"Intern", "Internship"}},
}

func reads(s TitleSignals, title string) string {
	var parts []string
	for _, r := range s.Reads(title) {
		parts = append(parts, r.Slot+"="+r.Value+"("+r.Matched+")")
	}
	return strings.Join(parts, " ")
}

func TestTitleReadsApprovedLists(t *testing.T) {
	s := TitleSignals{Read: approvedReads}
	for title, want := range map[string]string{
		// "Staff" inside Member of Technical Staff is not a seniority.
		"Member of Technical Staff - Inference Runtime":      "",
		"Senior Member of Technical Staff, Synthetic Data":   "",
		"member   of technical\tSTAFF, Training Performance": "",
		"Member of Design Staff - Brand":                     "role_fit=mismatch_domain(Brand)",
		"Senior/Staff Software Engineer (Back-End)":          "",
		"Staff Software Engineer, Inference":                 "seniority=staff_plus(Staff)",
		"STAFF ENGINEER":                                     "seniority=staff_plus(Staff)",
		"Principal Software Engineer":                        "seniority=staff_plus(Principal)",
		"Staffing Coordinator":                               "", // whole words only
		"Chief of Staff":                                     "seniority=staff_plus(Staff)",
		// Engineering Manager reads manager; whether it drops is the drop rule's call.
		"Engineering Manager, Cloud Platform": "seniority=manager(Manager)",
		"Senior Director, Solutions":          "seniority=manager(Director)",
		"Head of EMEA Sales":                  "seniority=manager(Head of) role_fit=mismatch_domain(Sales)",
		"Headcount Planner":                   "",
		// The first entry that reads a slot wins it.
		"Staff Engineering Manager": "seniority=staff_plus(Staff)",
		// An engineering word keeps a function title for the model.
		"Software Engineer, GTM Growth Engineering": "",
		"Sales Engineer": "",
		"Software Engineer, Applied Emerging Talent (2027)":  "",
		"Applied Science Internship, PhD Science Recruiting": "",
		"Account Executive - Strategic":                      "role_fit=mismatch_domain(Account Executive)",
		"Technical Recruiter":                                "role_fit=mismatch_domain(Recruiter)",
		"Recruiters":                                         "", // "Recruiter" is not a whole word in it
		"Product Marketing Manager":                          "seniority=manager(Manager) role_fit=mismatch_domain(Marketing)",
		// Empty, whitespace and odd text read nothing, or read as words.
		"":                       "",
		"   \t\n":                "",
		"Ｓｔａｆｆ Engineer":         "", // fullwidth letters are other words: left for the model
		"Señor Staff Engineer":   "seniority=staff_plus(Staff)",
		"ÉStaff Engineer":        "", // a non-ASCII letter is no boundary
		"Staff\x00Engineer":      "seniority=staff_plus(Staff)",
		"\xffStaff Engineer\xfe": "seniority=staff_plus(Staff)",
		"Software Engineer":      "",
		"Inference Engineer":     "",
	} {
		if got := reads(s, title); got != want {
			t.Errorf("Reads(%q) = %q, want %q", title, got, want)
		}
	}
	if got := (TitleSignals{}).Reads("Staff Sales Manager"); got != nil {
		t.Errorf("no read entries: %v", got)
	}
	// An oversized title is read in linear passes, without error.
	huge := strings.Repeat("x ", 1<<19) + "Director"
	if got := reads(s, huge); got != "seniority=manager(Director)" {
		t.Errorf("huge title: %q", got)
	}
	// Duplicate entries for a slot: the first wins, the second never reads it.
	dup := TitleSignals{Read: []TitleRead{
		{Slot: "seniority", Value: "manager", Match: []string{"Lead"}},
		{Slot: "seniority", Value: "staff_plus", Match: []string{"Lead"}},
	}}
	if got := reads(dup, "Tech Lead"); got != "seniority=manager(Lead)" {
		t.Errorf("duplicate: %q", got)
	}
}

func TestTitleReadProblems(t *testing.T) {
	long := strings.Repeat("x", MaxTitleSignalLen+1)
	many := make([]TitleRead, MaxTitleSignals+1)
	for i := range many {
		many[i] = TitleRead{Slot: "seniority", Value: "manager", Match: []string{"x"}}
	}
	ok := TitleRead{Slot: "seniority", Value: "manager", Match: []string{"Manager"}}
	for name, tc := range map[string]struct {
		r    []TitleRead
		want string
	}{
		"sponsorship":   {[]TitleRead{{Slot: "sponsorship", Value: "no_sponsorship", Match: []string{"US citizen"}}}, "never read from a title"},
		"unknown slot":  {[]TitleRead{{Slot: "remote", Value: "onsite", Match: []string{"x"}}}, `slot "remote" is not a triage slot`},
		"bad value":     {[]TitleRead{{Slot: "seniority", Value: "lead", Match: []string{"x"}}}, `"lead" is not a seniority value`},
		"empty value":   {[]TitleRead{{Slot: "role_fit", Match: []string{"x"}}}, `"" is not a role_fit value`},
		"no match":      {[]TitleRead{{Slot: "seniority", Value: "manager"}}, "match is empty"},
		"blank phrase":  {[]TitleRead{{Slot: "seniority", Value: "manager", Match: []string{" "}}}, "read[0].match[0] is empty"},
		"long unless":   {[]TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"x"}, Unless: []string{long}}}, "read[0].unless[0] is over"},
		"invalid utf-8": {[]TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"\xff"}}}, "not valid UTF-8"},
		"too many":      {many, "title_signals.read has 65 entries"},
		"second bad":    {[]TitleRead{ok, {Slot: "seniority", Value: "manager"}}, "read[1]: match is empty"},
	} {
		got := TitleSignals{Read: tc.r}.ReadProblems()
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: %q, want one problem containing %q", name, got, tc.want)
		}
	}
	if p := (TitleSignals{Read: approvedReads}).ReadProblems(); len(p) != 0 {
		t.Errorf("approved lists refused: %q", p)
	}
}

func TestConfigTitleReadRoundTripAndRefusal(t *testing.T) {
	c := Default()
	c.TitleSignals.Read = approvedReads
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.TitleSignals.Read, approvedReads) {
		t.Fatalf("round trip: %+v", back.TitleSignals.Read)
	}
	// Validate refuses a bad entry, and a misspelt field never loads.
	c.TitleSignals.Read = []TitleRead{{Slot: "sponsorship", Value: "no_sponsorship", Match: []string{"x"}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "never read from a title") {
		t.Fatalf("validate: %v", err)
	}
	bad := strings.Replace(string(data), `"unless"`, `"unles"`, 1)
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("unknown field in a read entry loaded")
	}
}

func TestTitleSignalsCloneIsDeep(t *testing.T) {
	s := TitleSignals{Include: []string{"GPU"}, Read: []TitleRead{{Slot: "seniority", Value: "manager", Match: []string{"Manager"}, Unless: []string{"x"}}}}
	c := s.Clone()
	if !reflect.DeepEqual(c, s) {
		t.Fatalf("clone %+v", c)
	}
	c.Read[0].Match[0] = "changed"
	c.Read[0].Unless[0] = "changed"
	c.Include[0] = "changed"
	if s.Read[0].Match[0] != "Manager" || s.Read[0].Unless[0] != "x" || s.Include[0] != "GPU" {
		t.Fatal("clone shares slices with the original")
	}
	if z := (TitleSignals{}).Clone(); z.Read != nil || z.Include != nil {
		t.Fatalf("zero clone %+v", z)
	}
}

func TestTitleIsAReservedBackendName(t *testing.T) {
	if err := checkBackendName("title"); err == nil {
		t.Fatal("an OpenRouter model could answer as the title reader")
	}
}
