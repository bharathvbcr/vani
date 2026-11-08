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

Vani reads career pages and listing APIs that employers publish for applicants. It does not log into LinkedIn, Indeed, Glassdoor, or other aggregator accounts, and it does not solve CAPTCHAs.

Requests identify themselves as `Vani/0.1 (+personal job-listing reader)` (`internal/egress/client.go`). Career-page fetches honor `robots.txt` for that agent, including `Crawl-delay`. A career page whose `robots.txt` cannot be read, or whose `Crawl-delay` is above 10 seconds, fails that board with a warning instead of being fetched anyway. A 4xx from `robots.txt` means the host published none. Listing APIs are paced per host.

Each third-party site keeps its own terms. freehire.me, used only to suggest boards and never as a posting source, allows keyless personal noncommercial use of its documented API. The client stays inside that limit.

People who run Vani are responsible for the sources they choose and for the law that applies to them.

## No warranty

As far as the law allows, the software comes as is, without any warranty, and the author is not liable for damages from these terms or from using the software. The license text controls.
