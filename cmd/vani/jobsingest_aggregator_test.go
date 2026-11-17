package main

import "testing"

// A page on a job site Vani does not search on its own never becomes a saved board: LinkedIn and
// the like need a signed-in browser, and Naukri's and Glassdoor's terms forbid automated reading
// (extension/src/jobsites.ts names the same sites).
func TestIsAggregator(t *testing.T) {
	for _, host := range []string{
		"www.linkedin.com", "linkedin.com", "www.glassdoor.com", "www.glassdoor.co.in", "www.glassdoor.co.uk",
		"www.glassdoor.com.au", "www.naukri.com", "naukri.com", "www.indeed.com",
	} {
		if !isAggregator(host) {
			t.Errorf("%s: not an aggregator", host)
		}
	}
	for _, host := range []string{
		"", "notnaukri.com", "naukri.com.evil.io", "naukrigulf.com", "myglassdoor.com", "boards.greenhouse.io", "jobs.lever.co",
	} {
		if isAggregator(host) {
			t.Errorf("%s: an aggregator", host)
		}
	}
}
