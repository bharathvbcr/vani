# Lever fixture

`board.json` is three real postings from
`GET https://api.lever.co/v0/postings/spotify?mode=json`, fetched 2026-10-18:
the two with the shortest non-empty `descriptionPlain`, plus the first
`workplaceType: "remote"` posting. Nothing was edited except dropping the other
postings and replacing any e-mail address with `redacted@example.invalid`. A
scan for phone-number-shaped digit runs found none.
