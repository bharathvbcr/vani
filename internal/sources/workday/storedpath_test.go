package workday

import "testing"

func TestStoredPathOnSiteHost(t *testing.T) {
	const prefix = "recruiting/snapchat/snap"
	for _, tc := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{"https://wd1.myworkdaysite.com/recruiting/snapchat/snap/job/Paris-France/ML_R1-1", "/job/Paris-France/ML_R1-1", true},
		{"https://wd1.myworkdaysite.com/en-US/recruiting/snapchat/snap/job/Paris-France/ML_R1-1", "/job/Paris-France/ML_R1-1", true},
		{"https://WD1.myworkdaysite.com/recruiting/Snapchat/SNAP/job/a/b", "/job/a/b", true},
		{"https://wd1.myworkdaysite.com/recruiting/othertenant/snap/job/a/b", "", false},
		{"https://wd1.myworkdaysite.com/recruiting/snapchat/other/job/a/b", "", false},
		{"https://wd1.myworkdaysite.com/snap/job/a/b", "", false}, // the myworkdayjobs shape is not this host's
		{"https://wd5.myworkdaysite.com/recruiting/snapchat/snap/job/a/b", "", false},
		{"https://wd1.myworkdaysite.com/recruiting/snapchat/snap", "", false},
		{"", "", false},
	} {
		got, ok := storedPath(tc.raw, siteHost, prefix)
		if got != tc.want || ok != tc.ok {
			t.Errorf("storedPath(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
	// The tenant-labelled form is unchanged.
	if got, ok := storedPath("https://nvidia.wd5.myworkdayjobs.com/en-US/NVIDIAExternalCareerSite/job/a/b", host, "NVIDIAExternalCareerSite"); !ok || got != "/job/a/b" {
		t.Errorf("myworkdayjobs storedPath = %q, %v", got, ok)
	}
}
