import { describe, expect, test } from "bun:test";
import { runCrawlToVani } from "../src/crawlrun";
import { JOB_SITES, MAX_SEARCH_CHARS, jobSiteOf, searchDefaults, searchUrl, unreadSite } from "../src/jobsites";
import { detectPortal } from "../src/portal";
import { DEFAULT_PROFILE } from "../src/relevance";
import { scrapePageJobs } from "../src/scraper";

/** A document that fails the test the moment anything reads it. */
const untouchable = new Proxy({}, {
  get(_t, prop) {
    throw new Error(`the page was read: ${String(prop)}`);
  },
}) as unknown as Document;

describe("jobSiteOf", () => {
  test("names each site on its own domain and subdomains, any case, trailing dot", () => {
    for (const [host, id] of [
      ["www.linkedin.com", "linkedin"], ["linkedin.com", "linkedin"], ["IN.LinkedIn.com", "linkedin"],
      ["www.glassdoor.com", "glassdoor"], ["www.glassdoor.co.in", "glassdoor"], ["www.glassdoor.co.uk", "glassdoor"],
      ["www.glassdoor.ca", "glassdoor"], ["www.glassdoor.com.au", "glassdoor"], ["glassdoor.sg", "glassdoor"],
      ["www.naukri.com", "naukri"], ["naukri.com.", "naukri"],
    ] as const) {
      expect(jobSiteOf(host)?.id).toBe(id);
    }
  });

  test("never a lookalike, a suffix trick or another site", () => {
    for (const host of [
      "", "  ", "notlinkedin.com", "linkedin.com.evil.io", "linkedin.co", "glassdoor-jobs.com",
      "glassdoor.com.evil.io", "myglassdoor.com", "naukri.co", "naukrigulf.com", "evilnaukri.com",
      "boards.greenhouse.io", "indeed.com",
    ]) {
      expect(jobSiteOf(host)).toBeNull();
    }
  });

  test("only Glassdoor and Naukri are unread; LinkedIn's reader stays at the owner's choice", () => {
    expect(unreadSite("www.glassdoor.com")?.id).toBe("glassdoor");
    expect(unreadSite("www.naukri.com")?.id).toBe("naukri");
    expect(unreadSite("www.linkedin.com")).toBeNull();
    expect(unreadSite("jobs.lever.co")).toBeNull();
  });
});

describe("searchUrl", () => {
  test("each site's own search, with the terms encoded", () => {
    expect(searchUrl("linkedin", "ML engineer", "United States")).toBe("https://www.linkedin.com/jobs/search/?keywords=ML+engineer&location=United+States");
    expect(searchUrl("linkedin", "ML engineer")).toBe("https://www.linkedin.com/jobs/search/?keywords=ML+engineer");
    // Glassdoor takes no plain-text location, so none is sent.
    expect(searchUrl("glassdoor", "ML engineer", "Austin")).toBe("https://www.glassdoor.com/Job/jobs.htm?sc.keyword=ML+engineer");
    expect(searchUrl("naukri", "Data Engineer", "Hyderabad")).toBe("https://www.naukri.com/data-engineer-jobs-in-hyderabad?k=Data+Engineer");
    expect(searchUrl("naukri", "C++ / Go developer")).toBe("https://www.naukri.com/c-go-developer-jobs?k=C%2B%2B+%2F+Go+developer");
  });

  test("nothing to search for is no URL", () => {
    for (const site of JOB_SITES) {
      expect(searchUrl(site.id, "")).toBeNull();
      expect(searchUrl(site.id, " \t\n ")).toBeNull();
    }
    // Naukri's search is its ASCII path; a query with no ASCII letter or digit has none.
    expect(searchUrl("naukri", "डेटा इंजीनियर")).toBeNull();
    expect(searchUrl("linkedin", "डेटा इंजीनियर")).toContain("keywords=");
  });

  test("hostile input stays inside the query: no path, host or extra parameter", () => {
    const evil = "x&location=evil#frag/../../admin?\r\nSet-Cookie: a=b";
    for (const site of JOB_SITES) {
      const u = new URL(searchUrl(site.id, evil, "y&k=z#q")!);
      expect(u.hash).toBe("");
      expect(u.username + u.password).toBe("");
      expect(["www.linkedin.com", "www.glassdoor.com", "www.naukri.com"]).toContain(u.host);
      const keys = Array.from(u.searchParams.keys());
      expect(new Set(keys).size).toBe(keys.length);
      expect(u.toString()).not.toMatch(/[\r\n]/);
    }
    const n = new URL(searchUrl("naukri", evil, "../../etc")!);
    expect(n.pathname).toMatch(/^\/[a-z0-9-]+$/);
  });

  test("oversized terms are cut, not refused", () => {
    const u = new URL(searchUrl("linkedin", "a".repeat(10_000), "b".repeat(10_000))!);
    expect(u.searchParams.get("keywords")!.length).toBe(MAX_SEARCH_CHARS);
    expect(u.searchParams.get("location")!.length).toBeLessThanOrEqual(100);
    expect(new URL(searchUrl("naukri", "a".repeat(10_000))!).pathname.length).toBeLessThan(120);
  });
});

describe("searchDefaults", () => {
  test("the first title the user saved, never a built-in default, and their location", () => {
    expect(searchDefaults({ ...DEFAULT_PROFILE })).toEqual({ terms: "", location: "" });
    expect(searchDefaults({ ...DEFAULT_PROFILE, targetTitles: [...DEFAULT_PROFILE.targetTitles, "ml engineer"], preferredLocation: " Bengaluru " }))
      .toEqual({ terms: "ml engineer", location: "Bengaluru" });
  });
});

describe("unread sites are not read", () => {
  test("Scrape on Glassdoor or Naukri returns why, without touching the page", () => {
    for (const host of ["www.glassdoor.com", "www.glassdoor.co.in", "www.naukri.com"]) {
      const r = scrapePageJobs(untouchable, host, undefined, `https://${host}/Job/jobs.htm?sc.keyword=go`);
      expect(r.jobs).toEqual([]);
      expect(r.refused).toContain("terms forbid automated reading");
    }
  });

  test("Crawl on Glassdoor or Naukri fetches, clicks and sends nothing", async () => {
    for (const pageUrl of ["https://www.glassdoor.com/Job/jobs.htm?sc.keyword=go", "https://www.naukri.com/go-jobs"]) {
      const calls: string[] = [];
      const sent: unknown[] = [];
      const r = await runCrawlToVani(
        {
          doc: untouchable, pageUrl, pageTitle: "",
          http: { sleep: async () => {}, fetch: async (u: string) => { calls.push(u); return new Response(""); } },
          send: async m => { sent.push(m); return {}; },
        },
        true, new AbortController().signal, () => {},
      );
      expect(calls).toEqual([]);
      expect(sent).toEqual([]);
      expect(r.text).toContain("Nothing was sent");
      expect(r.report.jobs).toEqual([]);
    }
  });

  test("the portal is told by host alone and offers no Scrape", () => {
    for (const [href, id] of [["https://www.glassdoor.com/Job/index.htm", "glassdoor"], ["https://www.naukri.com/job-listings-x", "naukri"]] as const) {
      const p = detectPortal(untouchable, href);
      expect(p.id).toBe(id);
      expect(p.isJobBoard).toBe(true);
      expect(p.isATS).toBe(false);
      expect(p.tips.join(" ")).not.toContain("Click 'Scrape Jobs'");
    }
  });

  test("LinkedIn keeps its existing reader and portal", () => {
    expect(detectPortal({ querySelector: () => null, querySelectorAll: () => [] }, "https://www.linkedin.com/jobs/search/").id).toBe("linkedin");
  });
});
