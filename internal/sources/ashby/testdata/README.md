# Ashby fixture

`board.json` is two real listed postings (the two with the shortest
`descriptionPlain`) from
`GET https://api.ashbyhq.com/posting-api/job-board/ashby?includeCompensation=false`,
fetched 2026-10-18. Nothing was edited except dropping the other postings and
replacing any e-mail address with `redacted@example.invalid`. A scan for
phone-number-shaped digit runs found none. The live board held no unlisted
entry, so the `isListed: false` case is covered by an inline payload in
`ashby_test.go`.
