# Purpose, license, and source notices

This page is the author's policy for Vani. It is not legal advice, and it is not a claim that every fetch is lawful in every place.

## What Vani is

Vani is a job-listing reader. Employers publish openings on their own career sites and applicant-tracking systems, and those systems do not share one way to read them. Vani fetches those public listings into one place so a person can look for work without visiting each board by hand.

The author does not sell Vani, does not sell listings, does not sell access, and does not take a fee when someone applies. It is not intended to make money.

## License

The source code in this repository is under the [PolyForm Noncommercial License 1.0.0](../LICENSE). The required notice is in [NOTICE](../NOTICE).

That license allows use, changes, and sharing for a permitted purpose. Permitted purposes include any noncommercial purpose, personal use (research, study, hobby, and private experiment without an anticipated commercial application), and use by charities, schools, public research organizations, public safety or health organizations, environmental organizations, and government institutions. Using Vani yourself to look for a job is the use it is published for.

The license does not allow selling the software or running it as a paid service. PolyForm Noncommercial is not an OSI-approved open-source license. Do not describe Vani as open source.

Anyone who receives a copy of any part of the software must also receive the license, or the URL <https://polyformproject.org/licenses/noncommercial/1.0.0>, and the plain-text lines in this repository that begin with `Required Notice:`.

## What the license does not cover

- Job-posting text, employer names used as trademarks, and any other material fetched from a board. That material stays with the employer or the board operator.
- Dependencies and programs that are not this repository. Manvi, the decide module, Tessl, DevPrism, Lightpanda, and the modules named in `go.mod` keep the licenses they ship with.
- Lightpanda. It is AGPL-licensed. Vani runs it as a separate program and does not link it.

## Include or exclude a source

The author will include or exclude a job source on notice.

Send the notice by opening an issue at <https://github.com/bharathvbcr/vani/issues>. Say who you are, which employer or board you mean (the site, and the board token if you know it), and whether you want that source included or excluded. A board operator or the employer does not have to use a particular form of words.

On a notice to exclude, the author will remove that source from this repository: the reader for it, the examples, and the docs that tell people how to fetch it. On a notice to include, the author will add a reader when the board publishes listings a person can already open, and will say so on the issue if that cannot be done.

This promise covers what this repository ships. It does not reach copies other people have already downloaded, and it does not delete listings someone has already stored on their own computer. The author does not operate a hosted copy of other people's watchlists.

A notice about copied code is handled the same way. If a file here is a substantial copy of someone else's work, the author will mark it with that work's license or remove it.

## How a fetch behaves

Vani reads career pages and listing APIs that employers publish for applicants, and the search services of five job boards that invite this use (below). Vani itself does not log into LinkedIn, Indeed, Glassdoor, or any other job site or aggregator account, sends no account cookie or token to one, and does not solve CAPTCHAs. Indeed is reached only through Indeed's own Claude connector, in the owner's Claude session, signed in by the owner (below).

Requests identify themselves as `Vani/0.1 (+personal job-listing reader)` (`internal/egress/client.go`). Career-page fetches honor `robots.txt` for that agent, including `Crawl-delay`. A career page whose `robots.txt` cannot be read, or whose `Crawl-delay` is above 10 seconds, fails that board with a warning instead of being fetched anyway. A 4xx from `robots.txt` means the host published none. Listing APIs are paced per host.

Each third-party site keeps its own terms. freehire.me, used only to suggest boards and never as a posting source, allows keyless personal noncommercial use of its documented API. The client stays inside that limit.

Five job boards are read through the search services they publish for this use, as boards an owner adds (`hm`, `ro`, `zr`, `dc`, `ad`), never by default. Every posting from one names the board as its source in the digest, `vani jobs show`, the Jobs tab and the extension (`posting.SourceOf`). As read on 2026-11-17 (Himalayas, Remote OK) and 2026-11-24 (the rest):

- **Himalayas** (`himalayas.app/jobs/api/search`): free to use without a key. Show Himalayas as the source and link back, send apply actions to the row's `applicationLink`, and do not pass the jobs on to other job boards or aggregators. Its robots.txt does not disallow the API. Vani keeps each job's himalayas.app page as its link and `applicationLink` as its apply link.
- **Remote OK** (`remoteok.com/api`): link back to the job's Remote OK page and name Remote OK as the source. Do not use its logo. Its robots.txt allows `/api` to every agent not named (`Allow: /`, `Crawl-delay: 1`), disallows named AI-training and SEO crawlers, and states `Content-Signal: search=yes, ai-train=no, use=reference` as a condition of access. Vani trains no model. It does send a job's description to the owner's decision model to rank it, which the signal neither grants nor forbids (no `ai-input` value). Vani keeps each job's remoteok.com page as its link and apply link.
- **Dice** (`mcp.dice.com/mcp`, its job-search MCP server): no login or key; dice.com/about/mcp invites any MCP client and asks a client that is rate limited (HTTP 429) to back off and to cache results. Vani calls `search_jobs` and, for postings it has not stored, `get_job_details`, at most 120 a board a run, about one call a second, and reuses stored descriptions; it never calls `get_company`. The search tool asks a client presenting results to say they were retrieved with AI: Vani shows that note wherever Dice rows appear. Vani keeps each job's dice.com page as its link, without the tracking parameters.
- **ZipRecruiter** (`api.ziprecruiter.com/mcp`, its job-search MCP server): no login (its partner documentation: "does not require end-user OAuth"), for "compatible AI assistants (for example, ChatGPT / Claude, or other MCP clients)". This is the weakest footing of the five: the server is presented for assistants, and Vani is a program of the owner's that calls it on the owner's schedule. Vani calls only `search_jobs` (never `connect_account`), sends no conversation id, and keeps each job's stable ziprecruiter.com page (`/jobs//j?lvk=…`) as its link rather than the per-call redirect link, which carries a tracking token. ZipRecruiter's own terms (ziprecruiter.global/en/terms) keep applying; on notice, this reader is removed like any other.
- **Adzuna** (`api.adzuna.com`, its search API): read with the owner's own free app id and key. Its API terms (developer.adzuna.com/docs/terms_of_service) permit "personal job search", require Adzuna to be acknowledged as the source ("Jobs by Adzuna" is shown wherever Adzuna rows appear), and require the result's `redirect_url` to be used to send a user to the advertiser: Vani keeps it as the job's link and apply link (less `utm_*` parameters). The free tier's request limits are the owner's to keep: `max_postings` bounds a run's requests.
- **Indeed** (`mcp.indeed.com`, Indeed's MCP server): "available only for Claude Connector" (docs.indeed.com/mcp), signed in with the owner's Indeed account, in beta and under Indeed's Terms of Service. Vani has no Indeed board and never calls it. `skills/indeed-to-vani` is instructions for a Claude session the owner runs by hand: Claude searches through the connector and passes the results to `vani jobs ingest`, the inbox the browser extension also uses. The skill never asks for the owner's Indeed resume or company data, and it is not to be scheduled. Whether Indeed's terms allow keeping connector results outside the conversation is not stated there; the owner decides whether to run it.
- **LinkedIn**: no route. LinkedIn offers no job-search API (its Job Posting API is for posting partners and takes no new ones), and its User Agreement §8.2 forbids scraping tools. The extension's LinkedIn reader is the owner's choice and risk (extension/README.md).

Vani shows these jobs only to the person running it and passes them on to no one.

People who run Vani are responsible for the sources they choose and for the law that applies to them.

## No warranty

As far as the law allows, the software comes as is, without any warranty, and the author is not liable for damages from these terms or from using the software. The license text controls.
