# Workday fixtures

Captured live on 2026-10-18 from the public NVIDIA careers site (tenant `nvidia`,
site `NVIDIAExternalCareerSite`, host `nvidia.wd5.myworkdayjobs.com`), trimmed to two
postings. No emails or phone numbers appear in them (checked; the capture script
scrubbed both patterns anyway). Descriptions are cut at a paragraph boundary.

- `list.json`: `POST https://nvidia.wd5.myworkdayjobs.com/wday/cxs/nvidia/NVIDIAExternalCareerSite/jobs`
  with `{"appliedFacets":{},"limit":20,"offset":0,"searchText":""}`. `total` was 2000 live;
  trimmed to 2 here. Live, `offset=20` returned 20 rows with `total: 0`, and a no-match
  `searchText` returned `{"total":0,"jobPostings":[],...}`.
- `detail1.json`, `detail2.json`: `GET .../wday/cxs/nvidia/NVIDIAExternalCareerSite{externalPath}`
  for `/job/US-CA-Santa-Clara/NVIDIA-2027-Ignite-Internships--Hardware-Engineering_JR2027047` and
  `/job/India-Bengaluru/CAD-Engineer_JR2014480-1`. Only the fields the adapter reads, plus a few
  identifiers, are kept.
- `remoteType` was absent on both live postings; it is exercised by a test that injects it.

## Shared host `wd1.myworkdaysite.com` (Snap)

Captured live on 2026-10-22 from Snap's public board (tenant `snapchat`, site `snap`, host
`wd1.myworkdaysite.com`, which carries no tenant label). Same endpoints as above, same host
for the listing and the details; the details' `externalUrl` is the public page,
`https://wd1.myworkdaysite.com/recruiting/snapchat/snap/job/...` (that URL answers 200 with and
without a leading locale, `/en-US/`, and with a trailing `/apply`).

- `snap_list.json`: `POST https://wd1.myworkdaysite.com/wday/cxs/snapchat/snap/jobs` with
  `{"appliedFacets":{},"limit":20,"offset":0,"searchText":"machine learning"}`. `total` was 41
  live; trimmed to the first 2 rows and `total` set to 2. `facets` dropped.
- `snap_detail1.json`, `snap_detail2.json`: `GET .../wday/cxs/snapchat/snap{externalPath}` for
  `/job/Paris-France/Machine-Learning-Engineer_R0047006-1` and
  `/job/Palo-Alto-California/Machine-Learning-Engineer--Level-5_Q426SWEML5-1`. Only the fields the
  adapter reads plus identifiers are kept; `country`, `jobRequisitionLocation`, `questionnaireId`
  and `includeResumeParsing` are dropped, and `jobDescription` is a one-line synthesized
  paragraph, not Snap's text. No e-mail address or phone number appears in them.
