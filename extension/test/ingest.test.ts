// Scraped jobs to Vani (docs/EXTENSION_INGEST.md): the message bounds, the page URL scrub, the batch, the words.
import { describe, expect, test } from "bun:test";
import { checkToBackground } from "../src/messages";
import { MAX_INGEST_JOBS, buildIngestBatch, describeIngest, ingestJobsProblem, prepareIngest } from "../src/ingest";
import { scrapePageJobs, scrubPageUrl } from "../src/scraper";

const job = (over: Record<string, unknown> = {}) => ({
  title: "Staff Engineer", company: "Acme", location: "Remote", url: "https://careers.acme.com/jobs/1", snippet: "Build things", ...over,
});
const msg = (over: Record<string, unknown> = {}) => ({
  type: "ingestJobs", page: { url: "https://careers.acme.com/search?q=engineer", title: "Jobs at Acme" }, saveSearch: true, jobs: [job()], ...over,
});
const refused = { error: "malformed ingestJobs message" };

describe("ingestJobs message bounds", () => {
  test("a good batch is accepted", () => {
    expect(checkToBackground(msg())).toEqual({ msg: msg() as never });
    expect(checkToBackground(msg({ jobs: [job({ posted: "2026-10-01T00:00:00Z" })], saveSearch: false }))).toHaveProperty("msg");
    expect(checkToBackground(msg({ jobs: [job({ company: "", location: "", snippet: "" })] }))).toHaveProperty("msg");
  });

  test("each bound is refused, never cut", () => {
    const cases: Array<[string, Record<string, unknown>]> = [
      ["no jobs", msg({ jobs: [] })],
      ["201 jobs", msg({ jobs: Array.from({ length: MAX_INGEST_JOBS + 1 }, () => job()) })],
      ["jobs not a list", msg({ jobs: "x" })],
      ["empty title", msg({ jobs: [job({ title: "" })] })],
      ["301-byte title", msg({ jobs: [job({ title: "a".repeat(301) })] })],
      ["201-byte company", msg({ jobs: [job({ company: "a".repeat(201) })] })],
      ["201-byte location", msg({ jobs: [job({ location: "a".repeat(201) })] })],
      ["4001-byte snippet", msg({ jobs: [job({ snippet: "a".repeat(4001) })] })],
      ["65-byte posted", msg({ jobs: [job({ posted: "a".repeat(65) })] })],
      ["javascript: job url", msg({ jobs: [job({ url: "javascript:alert(1)" })] })],
      ["relative job url", msg({ jobs: [job({ url: "/jobs/1" })] })],
      ["empty job url", msg({ jobs: [job({ url: "" })] })],
      ["oversized job url", msg({ jobs: [job({ url: `https://a.com/${"a".repeat(2048)}` })] })],
      ["non-object job", msg({ jobs: ["x"] })],
      ["http page with saveSearch", msg({ page: { url: "http://careers.acme.com/s", title: "t" }, saveSearch: true })],
      ["javascript: page url", msg({ page: { url: "javascript:1", title: "t" }, saveSearch: false })],
      ["301-byte page title", msg({ page: { url: "https://a.com/", title: "a".repeat(301) } })],
      ["saveSearch not a boolean", msg({ saveSearch: "yes" })],
      ["no page", msg({ page: undefined })],
    ];
    for (const [name, m] of cases) expect([name, checkToBackground(m)]).toEqual([name, refused]);
  });

  test("bounds are bytes, not characters", () => {
    // 151 two-byte characters are 302 bytes but 151 characters.
    expect(checkToBackground(msg({ jobs: [job({ title: "é".repeat(151) })] }))).toEqual(refused);
    expect(checkToBackground(msg({ jobs: [job({ title: "é".repeat(150) })] }))).toHaveProperty("msg");
  });

  test("an http page is fine when no search is saved, and the reason is named", () => {
    const m = msg({ page: { url: "http://careers.acme.com/s", title: "t" }, saveSearch: false });
    expect(checkToBackground(m)).toHaveProperty("msg");
    expect(ingestJobsProblem(msg({ page: { url: "http://careers.acme.com/s", title: "t" } }) as Record<string, unknown>)).toBe("page.url must be https to save a search");
  });

  test("the old track message is gone", () => {
    expect(checkToBackground({ type: ["track", "Job"].join(""), job: { title: "x", company: "y", url: "https://a.com" } })).toEqual({ error: "unknown message type" });
  });
});

describe("pageUrl", () => {
  test("drops the fragment and tracking parameters, keeps the search", () => {
    expect(scrubPageUrl("https://careers.acme.com/search?q=engineer&utm_source=x&utm_medium=y&gclid=1&fbclid=2&msclkid=3&ref=4&refid=5&trk=6&trackingId=7&location=NYC#results"))
      .toBe("https://careers.acme.com/search?q=engineer&location=NYC");
    expect(scrubPageUrl("https://a.com/jobs?TRACKINGID=1&department=eng")).toBe("https://a.com/jobs?department=eng");
    expect(scrubPageUrl("https://u:p@a.com/jobs#x")).toBe("https://a.com/jobs");
  });

  test("a page that is not http(s) or not a URL has none", () => {
    expect(scrubPageUrl("javascript:alert(1)")).toBe("");
    expect(scrubPageUrl("chrome://extensions")).toBe("");
    expect(scrubPageUrl("not a url")).toBe("");
    expect(scrubPageUrl("")).toBe("");
  });

  test("scrapePageJobs records the scrubbed listing URL it was given", () => {
    const doc = { title: "Jobs", querySelector: () => null, querySelectorAll: () => [] } as unknown as Document;
    const r = scrapePageJobs(doc, "careers.acme.com", undefined, "https://careers.acme.com/s?q=a&utm_source=b#f");
    expect(r.pageUrl).toBe("https://careers.acme.com/s?q=a");
    expect(r.pageTitle).toBe("Jobs");
    expect(scrapePageJobs(undefined, "x", undefined, "https://x.com/?gclid=1").pageUrl).toBe("https://x.com/");
  });
});

describe("batch and reply", () => {
  test("a job that breaks a bound is left out whole, and counted", () => {
    const b = buildIngestBatch([job(), job({ url: "" }), job({ title: "a".repeat(301) }), job({ snippet: "a".repeat(4001) }), job({ url: "javascript:1" })]);
    expect(b.jobs.length).toBe(1);
    expect(b.skipped).toBe(4);
    expect(b.jobs[0]!.snippet).toBe("Build things");
  });

  test("a job's role excerpt is sent as its snippet; without one the listing snippet is sent", () => {
    const b = buildIngestBatch([job({ description: "The whole role text" }), job({ url: "https://a.com/2", description: "" }), job({ url: "https://a.com/3", description: undefined })]);
    expect(b.jobs.map(j => j.snippet)).toEqual(["The whole role text", "Build things", "Build things"]);
    expect(b.jobs[0]).not.toHaveProperty("description");
  });

  test("an excerpt over the bound still skips the job whole, never cut", () => {
    const b = buildIngestBatch([job({ description: "a".repeat(4001) })]);
    expect(b.jobs.length).toBe(0);
    expect(b.skipped).toBe(1);
  });

  test("past 200 jobs the rest are counted, not silently dropped", () => {
    const b = buildIngestBatch(Array.from({ length: 205 }, (_, i) => job({ url: `https://a.com/${i}` })));
    expect(b.jobs.length).toBe(200);
    expect(b.overLimit).toBe(5);
  });

  test("prepareIngest refuses to save a search from a non-https page and builds a valid message otherwise", () => {
    const scrape = { pageUrl: "http://a.com/s", pageTitle: "t", jobs: [job()] };
    expect(prepareIngest(scrape, true)).toHaveProperty("error");
    expect(prepareIngest({ ...scrape, pageUrl: "" }, true)).toHaveProperty("error");
    const ok = prepareIngest({ ...scrape, pageUrl: "https://a.com/s", pageTitle: "a".repeat(400) }, true);
    expect("req" in ok && ok.req.page).toEqual({ url: "https://a.com/s", title: "" });
    expect(prepareIngest({ pageUrl: "", pageTitle: "", jobs: [job({ url: "" })] }, false)).toHaveProperty("error");
    const one = prepareIngest({ pageUrl: "", pageTitle: "", jobs: [job()] }, false);
    expect("req" in one && one.req.page.url).toBe("https://careers.acme.com/jobs/1");
  });

  test("the host's answer is reported as it is", () => {
    expect(describeIngest({ queued: 12, duplicates: 1, rejected: [{ index: 3, reason: "url is not http(s)" }], search: { status: "added" }, analysis: "daemon" }))
      .toEqual({ ok: true, text: "12 queued, 1 duplicate, 1 rejected (url is not http(s)), search saved, analysing now" });
    expect(describeIngest({ queued: 2, search: { status: "exists" }, analysis: "next_run" }).text).toBe("2 queued, search already saved, analysed at next run");
    expect(describeIngest({ queued: 2, search: { status: "unsupported", reason: "linkedin.com needs a signed-in browser" } }).text)
      .toBe("2 queued, search not saved: linkedin.com needs a signed-in browser");
    expect(describeIngest({ queued: 2, search: { status: "not_requested" } }).text).toBe("2 queued");
  });

  test("an error, an unknown op and a non-reply are failures, never 'queued'", () => {
    expect(describeIngest({ error: "unknown op jobsIngest" })).toEqual({ ok: false, text: "This version of the Vani app cannot take scraped jobs. Update the Vani app." });
    expect(describeIngest({ error: "x", code: "unsupported" }).ok).toBe(false);
    expect(describeIngest({ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" }).text).toContain("not reachable");
    expect(describeIngest({ error: "too many", code: "bad_request" })).toEqual({ ok: false, text: "Vani refused the jobs: too many" });
    for (const r of [undefined, null, "ok", {}, { queued: "3" }]) expect(describeIngest(r).ok).toBe(false);
  });
});
