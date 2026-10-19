# Greenhouse fixture

`board.json` is two real postings (the two with the shortest `content`) from
`GET https://boards-api.greenhouse.io/v1/boards/gitlab/jobs?content=true`,
fetched 2026-10-18. Nothing was edited except dropping the other postings and
replacing any e-mail address with `redacted@example.invalid`. A scan for
phone-number-shaped digit runs found none.

`pay.json` is four real single-job responses from
`GET https://boards-api.greenhouse.io/v1/boards/{token}/jobs/{id}?pay_transparency=true`,
fetched 2026-10-19, keyed `{token}/{id}`: `anthropic/4461450008` (one USD
range titled "Annual Salary:"), `anthropic/5391313008` (one EUR range),
`anthropic/5183044008` (an empty `pay_input_ranges`) and `robinhood/6669758`
(three USD ranges titled by location zone, with no period word). Each keeps
`id`, `title`, `absolute_url`, `updated_at`, `first_published` and
`pay_input_ranges` verbatim, including each range's `blurb`, which is HTML that
the adapter does not render. `content` was replaced with
`(description omitted)` and every other response field was dropped. A scan for
e-mail addresses and phone-number-shaped digit runs found none.
