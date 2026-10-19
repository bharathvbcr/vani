# Ashby fixture

`board.json` is two real listed postings (the two with the shortest
`descriptionPlain`) from
`GET https://api.ashbyhq.com/posting-api/job-board/ashby?includeCompensation=false`,
fetched 2026-10-18. Nothing was edited except dropping the other postings and
replacing any e-mail address with `redacted@example.invalid`. A scan for
phone-number-shaped digit runs found none. The live board held no unlisted
entry, so the `isListed: false` case is covered by an inline payload in
`ashby_test.go`.

`compensation.json` is the same two postings from
`GET https://api.ashbyhq.com/posting-api/job-board/ashby?includeCompensation=true`,
fetched 2026-10-19, keeping each job's identity fields,
`shouldDisplayCompensationOnJobPostings` and `compensation` (tier summary,
salary summary, and the five documented fields of each summary component).
`descriptionPlain` was replaced with `(description omitted)` and the other
response fields (`address`, `secondaryLocations`, the per-tier breakdown) were
dropped. It shows `compensation` sits on each job, as the API's field table
says, not beside `jobs` as its example response does.
