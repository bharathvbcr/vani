package pipeline

import (
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/joburl"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A requisition id is the company's own number for a job, the one thing that
// proves two records under different ATSes are one job when their links differ
// (the same company, title and place alone can be two requisitions: one role
// opened twice, or a title a company reuses). A posting carries one in two
// places:
//
//   - its key, for the ATSes whose key id is the requisition number: Workday
//     (wd:tenant:site:JR-123), Oracle HCM, Jibe (req_id) and IBM (jobId);
//   - its link, in a job-naming query parameter (req_id, job_id, jobid, jid) that
//     the joburl form keeps. gh_jid is left out: it is Greenhouse's own id, which
//     the Greenhouse record already carries as its key.
//
// Two ids match when their letters and digits do, ignoring case, so "JR-123" is
// "jr123". An id needs at least minReqChars characters and a digit: "1" and
// "new" name no job.

// minReqChars is the fewest letters and digits a requisition id has.
const minReqChars = 3

// reqKeyATS are the ATSes whose key ends in the requisition number.
var reqKeyATS = map[posting.ATS]bool{posting.Workday: true, posting.OracleHCM: true, posting.Jibe: true, posting.IBM: true}

// reqParams are the link parameters that hold a requisition id.
var reqParams = []string{"req_id", "job_id", "jobid", "jid"}

// requisitionIDs are the requisition ids a posting carries, folded and sorted,
// each once; nil when it has none. key is the record's key (p.Key may be empty
// on a posting read from elsewhere).
func requisitionIDs(key posting.Key, p posting.Posting) []string {
	var out []string
	add := func(raw string) {
		if id := foldReq(raw); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if reqKeyATS[key.ATS()] {
		if i := strings.LastIndexByte(string(key), ':'); i >= 0 {
			add(string(key)[i+1:])
		}
	}
	for _, raw := range []string{p.URL, p.ApplyURL} {
		n, err := joburl.Normalize(raw)
		if err != nil {
			continue
		}
		_, query, _ := strings.Cut(n, "?")
		vals, err := url.ParseQuery(query)
		if err != nil {
			continue
		}
		for _, name := range reqParams {
			for _, v := range vals[name] {
				add(v)
			}
		}
	}
	slices.Sort(out)
	return out
}

// foldReq is a requisition id's letters and digits in lower case, or "" when
// it is too short or holds no digit to be one.
func foldReq(s string) string {
	var b strings.Builder
	digit := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsDigit(r):
			digit = true
			b.WriteRune(r)
		case unicode.IsLetter(r):
			b.WriteRune(r)
		}
	}
	if !digit || b.Len() < minReqChars {
		return ""
	}
	return b.String()
}

// sharesRequisition reports whether two sorted id lists have an id in common.
func sharesRequisition(a, b []string) bool {
	for _, x := range a {
		if _, ok := slices.BinarySearch(b, x); ok {
			return true
		}
	}
	return false
}
