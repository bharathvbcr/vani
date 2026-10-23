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

`spaced_board.json` is two real listed postings from
`GET https://api.ashbyhq.com/posting-api/job-board/Hippocratic%20AI?includeCompensation=false`
(HTTP 200, 50 jobs live; `hippocraticai`, `HippocraticAI`, `hippocratic` and `hippocratic-ai`
all answer 404), fetched 2026-10-22: the two with the shortest `descriptionPlain`, keeping the
identity, location, date and link fields. `descriptionPlain` was replaced with
`(description omitted)`; `descriptionHtml`, `address`, `secondaryLocations` and `compensation`
were dropped. The `jobUrl`s carry the board name as `Hippocratic%20AI`. It is the board whose
token is the slug `hippocratic-ai` with `board_name` `Hippocratic AI`.
