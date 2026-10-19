# Draft fixture

`anthropic-4981828008.json` is one real posting, "Software Engineer, Research Tools", from
`GET https://boards-api.greenhouse.io/v1/boards/anthropic/jobs/4981828008`, fetched 2026-10-19. Its HTML
`content` was flattened to text the way the browser extension does it (`extension/src/jobinfo.ts`:
a line break after each `</p>`, `</li>`, `</hN>` and `<br>`, tags dropped, entities decoded, blank lines
collapsed). Nothing else was edited. A scan found no e-mail address and no phone-number-shaped digit run.

The real postings from the other boards come from the source fixtures in `internal/sources/*/testdata`,
read through each source's own parser.
