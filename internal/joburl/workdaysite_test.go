package joburl

import "testing"

// On the shared host wd{N}.myworkdaysite.com the site is
// recruiting/{tenant}/{site}; the routes fold like a myworkdayjobs board's.
func TestWorkdayPostingFoldsSharedHostRoutes(t *testing.T) {
	const host = "https://wd1.myworkdaysite.com"
	const want = host + "/recruiting/snapchat/snap/job/Machine-Learning-Engineer_R0047006-1"
	for _, in := range []string{
		host + "/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R0047006-1",
		host + "/en-US/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R0047006-1",
		host + "/recruiting/snapchat/snap/job/Machine-Learning-Engineer_R0047006-1",
		host + "/fr-FR/recruiting/snapchat/snap/details/Machine-Learning-Engineer_R0047006-1",
		host + "/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R0047006-1/apply",
		host + "/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R0047006-1/APPLY/applyManually",
		host + "/recruiting/snapchat/snap/job/Paris-France/Machine-Learning-Engineer_R0047006-1?jid=7",
	} {
		if got, ok := WorkdayPosting(in); !ok || got != want {
			t.Errorf("WorkdayPosting(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// Two tenants on the shared host never fold together.
	a, _ := WorkdayPosting(host + "/recruiting/snapchat/snap/job/x/R1")
	b, _ := WorkdayPosting(host + "/recruiting/othertenant/snap/job/x/R1")
	if a == "" || a == b {
		t.Errorf("tenants fold together: %q %q", a, b)
	}
}

func TestWorkdayPostingSharedHostLeavesEverythingElseAlone(t *testing.T) {
	const host = "https://wd1.myworkdaysite.com"
	for _, in := range []string{
		host,
		host + "/recruiting",
		host + "/recruiting/snapchat",
		host + "/recruiting/snapchat/snap",
		host + "/en-US/recruiting/snapchat/snap",
		host + "/recruiting/snapchat/snap/login",
		host + "/recruiting/snapchat/snap/job",
		host + "/recruiting/snapchat/snap/job/apply",
		host + "/recruiting/snapchat/snap/jobs/x/R1",      // not /job or /details
		host + "/snap/job/x/R1",                           // the myworkdayjobs shape is not this host's
		host + "/careers/snapchat/snap/job/x/R1",          // not /recruiting
		host + "/en-us/recruiting/snapchat/snap/job/x/R1", // a locale is xx-XX
		"https://wd.myworkdaysite.com/recruiting/a/b/job/x/R1",
		"https://myworkdaysite.com/recruiting/a/b/job/x/R1",
		"https://snapchat.wd1.myworkdaysite.com/recruiting/a/b/job/x/R1",
		"https://wd1.myworkdaysite.com.evil.example/recruiting/a/b/job/x/R1",
		"https://evil-wd1.myworkdaysite.com/recruiting/a/b/job/x/R1",
	} {
		if got, ok := WorkdayPosting(in); ok {
			t.Errorf("WorkdayPosting(%q) = %q; want no fold", in, got)
		}
	}
}
