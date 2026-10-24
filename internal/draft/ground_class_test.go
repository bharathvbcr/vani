package draft

import "testing"

// A figure is grounded by the same number counting the same thing (ground.go):
// these are the honest drafts the class check must keep accepting, and the
// look-alike counts it must refuse.
func TestGroundingAcceptsHonestCounts(t *testing.T) {
	for name, tc := range map[string]struct{ source, reply string }{
		"marked figure with its own words":     {"Shipped a 3.1x faster decode path.", "I shipped a 3.1x faster path."},
		"a count in the question":              {"Answer in 200 words.", "In 200 words: I build tools."},
		"plural folds":                         {"Led 12 engineers.", "I led 12 engineer"},
		"singular source, plural draft":        {"Worked 3 year at Acme.", "I worked 3 years at Acme."},
		"hyphenated source, spaced draft":      {"A 3-year project.", "A project of 3 years."},
		"attached unit":                        {"Latency 40ms at p99.", "Latency was 40 ms."},
		"function word after the number":       {"Cut it 42% in 2023.", "In 2023 I cut it, and 42 more things followed."},
		"a year before a noun":                 {"Joined 2023-01 to present.", "In 2023 Acme grew."},
		"a version before a verb":              {"Ran on v2.1", "I ran on v2.1 starting then."},
		"a bare figure is read as before":      {"Own 5000 GPUs.", "I think of 5000."},
		"a code name matches the same code":    {"We run S3 and k8s and H100 nodes at p99.", "I use S3, k8s, H100 and p99 daily."},
		"a slash figure matches itself":        {"On call 24/7.", "I was on call 24/7."},
		"a vulgar fraction matches itself":     {"Cut cost by " + vulgar + ".", "I cut cost by " + vulgar + "."},
		"percentage points":                    {"Up 42%.", "Up 42 percentage points."},
		"ordinal suffix is not a counted noun": {"Ranked 99th.", "My rank was 99th."},
		"thin space grouping":                  {"1,000 users", "1 000 users"},
	} {
		t.Run(name, func(t *testing.T) {
			if f, n := newGrounding(tc.source).ungrounded(tc.reply); n != 0 {
				t.Errorf("%q refused against %q: first figure %v (%d)", tc.reply, tc.source, f, n)
			}
		})
	}
}

func TestGroundingRefusesTheWrongCount(t *testing.T) {
	for name, tc := range map[string]struct{ source, reply string }{
		"words not read, so digits are new":     {"Worked five years at Acme.", "I worked 5 years at Acme."},
		"another unit for the same number":      {"Led 12 reports.", "I led 12 engineers."},
		"a version number as a count":           {"Ran on v2.1", "I ran 2.1 million jobs."},
		"a code name is not a plain figure":     {"We run H100 nodes.", "I run 100 nodes."},
		"a plain figure is not a code name":     {"We run 100 nodes.", "I run H100 nodes."},
		"a slash figure does not split":         {"On call 24/7.", "I ran 24 clusters."},
		"a slash date does not split":           {"Joined 12/05/2023.", "I managed 12 people."},
		"a different tag, same number":          {"We run S3.", "We run Q3."},
		"percent grounds no people":             {"Up 40%.", "I mentored 40 people."},
		"a multiple grounds no years":           {"A 3.1x speedup.", "Over 3.1 years."},
		"a fraction for another fraction":       {"Cut cost by " + vulgar + ".", "Cut cost by ¼."},
		"the other way: a count grounds no pct": {"I led 40 people.", "I cut it 40%."},
	} {
		t.Run(name, func(t *testing.T) {
			if _, n := newGrounding(tc.source).ungrounded(tc.reply); n == 0 {
				t.Errorf("%q accepted against %q", tc.reply, tc.source)
			}
		})
	}
}

// What the reader makes of a figure: tags, words and the cases that have none.
func TestFigureTagsAndWords(t *testing.T) {
	for in, want := range map[string]figure{
		"7 years":               {num: "7", word: "year"},
		"7 Years":               {num: "7", word: "year"},
		"3-year":                {num: "3", word: "year"},
		"40ms":                  {num: "40", word: "ms"},
		"companies 5 companies": {num: "5", word: "company"},
		"S3":                    {num: "3", tag: "s"},
		"k8s":                   {num: "8", tag: "k"},
		"v2.1 now":              {num: "2.1"},
		"in 2023 Acme":          {num: "2023"},
		"5 and":                 {num: "5"},
		"42 %":                  {num: "42", mark: "%"},
		"3.1x faster":           {num: "3.1", mark: "x"},
		"9\nlines":              {num: "9"},
	} {
		var got figure
		fs := figures(in)
		if len(fs) == 0 {
			t.Errorf("figures(%q) read nothing", in)
			continue
		}
		got = fs[len(fs)-1]
		if got != want {
			t.Errorf("figures(%q) last = %+v, want %+v", in, got, want)
		}
	}
	if fs := figures("24/7"); len(fs) != 2 || fs[0].tag != "/" || fs[1].tag != "/" {
		t.Errorf("24/7 = %+v, want two slash-tagged figures", fs)
	}
	if fs := figures(vulgar + " and ⅓"); len(fs) != 2 || fs[0].num != vulgar || fs[1].num != "⅓" {
		t.Errorf("fractions = %+v", fs)
	}
	if fs := figures("² and 4³"); len(fs) != 1 || fs[0].num != "4" {
		t.Errorf("superscripts are not fractions: %+v", fs)
	}
}
