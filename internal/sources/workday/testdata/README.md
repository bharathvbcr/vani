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
