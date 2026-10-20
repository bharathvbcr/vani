import { describe, expect, test } from "bun:test";
import type { Http } from "../src/crawler";
import { progressText, runCrawlToVani } from "../src/crawlrun";

const ORIGIN = "https://acme.wd1.myworkdayjobs.com";
const PAGE = `${ORIGIN}/en-US/Careers?q=engineer&utm_source=mail`;

/** A Workday board of `n` jobs, each with a description. */
function board(n: number, onList?: (offset: number) => void): Http & { calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    sleep: async () => {},
    async fetch(url: string, init: RequestInit) {
      calls.push(url);
      if (url === `${ORIGIN}/wday/cxs/acme/Careers/jobs`) {
        const { offset, limit } = JSON.parse(String(init.body)) as { offset: number; limit: number };
        onList?.(offset);
        const rows = Array.from({ length: n }, (_, i) => ({ title: `Software Engineer ${i}`, externalPath: `/job/Remote/SE_JR${i}` })).slice(offset, offset + limit);
        return new Response(JSON.stringify({ total: n, jobPostings: rows, facets: [] }), { status: 200 });
      }
      const m = /\/job\/Remote\/SE_JR(\d+)$/.exec(url);
      if (m) return new Response(JSON.stringify({ jobPostingInfo: { title: `Software Engineer ${m[1]}`, jobDescription: "<p>Build Go services.</p>", location: "Remote" } }), { status: 200 });
      return new Response("", { status: 404 });
    },
  };
}

const doc = (meta?: string, ogTitle?: string) => ({
  querySelector: (sel: string) =>
    meta && sel.includes("og:site_name") ? { getAttribute: () => meta }
      : ogTitle && sel.includes("og:title") ? { getAttribute: () => ogTitle }
        : null,
  querySelectorAll: () => [],
} as unknown as Document);

describe("runCrawlToVani", () => {
  test("crawls the board and sends it to Vani in bounded batches, from the scrubbed page address", async () => {
    const sent: Array<Record<string, unknown>> = [];
    const statuses: string[] = [];
    const r = await runCrawlToVani(
      { doc: doc("Acme"), pageUrl: PAGE, pageTitle: "Careers at Acme", http: board(250), send: async m => { sent.push(m as Record<string, unknown>); return { queued: (m as { jobs: unknown[] }).jobs.length, analysis: "daemon" }; } },
      true, new AbortController().signal, t => statuses.push(t),
    );
    // maxDetails defaults to one ingest batch (200), so 200 of the 250 are sent and 50 are reported as left.
    expect(sent.map(m => (m.jobs as unknown[]).length)).toEqual([200]);
    expect(sent[0]!.type).toBe("ingestJobs");
    // 250 jobs is within what the daemon's Workday adapter lists (5,000), so the search is saved.
    expect(sent[0]!.saveSearch).toBe(true);
    expect(r.text).not.toContain("not saved");
    expect(sent[0]!.page).toEqual({ url: `${ORIGIN}/en-US/Careers?q=engineer`, title: "Careers at Acme" });
    expect((sent[0]!.jobs as Array<{ company: string }>)[0]!.company).toBe("Acme");
    expect(r.text).toContain("Listed 250 jobs");
    expect(r.text).toContain("Vani: 200 queued, analysing now.");
    expect(r.text).toContain("50 less relevant jobs were listed but not sent");
    expect(statuses[0]).toBe("Starting the crawl…");
    expect(statuses.some(s => s.startsWith("Reading descriptions"))).toBe(true);
    expect(statuses.at(-1)).toBe("Sending 200 jobs to Vani…");
  });

  test("a Workday board the daemon can list is saved as a daily search", async () => {
    const sent: Array<Record<string, unknown>> = [];
    const r = await runCrawlToVani(
      { doc: doc(), pageUrl: PAGE, pageTitle: "", http: board(150), send: async m => { sent.push(m as Record<string, unknown>); return { queued: 150, search: { status: "added" } }; } },
      true, new AbortController().signal, () => {},
    );
    expect(sent[0]!.saveSearch).toBe(true);
    expect(r.text).toContain("search saved");
    expect(r.text).not.toContain("not saved");
  });

  test("a Workday board over the daemon's listing limit is not saved, and the status says so", async () => {
    const sent: Array<Record<string, unknown>> = [];
    // The board counts 5,001 jobs: over DAEMON_WORKDAY_MAX (workday.MaxPostings, 5,000).
    const r = await runCrawlToVani(
      { doc: doc(), pageUrl: PAGE, pageTitle: "", http: board(5001), send: async m => { sent.push(m as Record<string, unknown>); return { queued: (m as { jobs: unknown[] }).jobs.length }; } },
      true, new AbortController().signal, () => {},
    );
    expect(sent.length).toBeGreaterThan(0);
    expect(sent.every(m => m.saveSearch === false)).toBe(true);
    expect(r.text).toContain("The search was not saved: this board has over 5000 jobs");
  });

  test("Stop ends the crawl and sends nothing", async () => {
    const ac = new AbortController();
    let sends = 0;
    const r = await runCrawlToVani(
      { doc: doc(), pageUrl: PAGE, pageTitle: "", http: board(100, o => { if (o >= 40) ac.abort(); }), send: async () => { sends++; return { queued: 1 }; } },
      false, ac.signal, () => {},
    );
    expect(sends).toBe(0);
    expect(r.text.startsWith("Stopped.")).toBe(true);
    expect(r.text).toContain("Nothing was sent.");
  });

  test("an unreachable Vani is reported as a failure, never as queued", async () => {
    const r = await runCrawlToVani(
      { doc: doc(), pageUrl: PAGE, pageTitle: "", http: board(3), send: async () => ({ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" }) },
      false, new AbortController().signal, () => {},
    );
    expect(r.text).toContain("Vani: The Vani app is not reachable");
    expect(r.text).not.toContain("queued");
    expect(r.text).toContain("3 not sent");
    // Found live: the reason was said twice and the sentence ended in "..".
    expect(r.text.split("not reachable").length).toBe(2);
    expect(r.text).not.toContain("..");
  });

  test("a non-Workday page that lists nothing sends nothing and says so", async () => {
    const sent: Array<Record<string, unknown>> = [];
    // Not a Workday board (http), so the page pager runs; one job on the page, no next control.
    const page = {
      querySelector: () => null,
      querySelectorAll: (sel: string) => (sel.includes("job") || sel.includes("a[href]") ? [] : []),
      title: "Jobs",
    } as unknown as Document;
    const r = await runCrawlToVani(
      { doc: page, pageUrl: "http://careers.acme.com/jobs", pageTitle: "Jobs", send: async m => { sent.push(m as Record<string, unknown>); return { queued: 1 }; } },
      true, new AbortController().signal, () => {},
    );
    // The fake page lists nothing, so nothing goes; the run still finishes and says why.
    expect(sent.length).toBe(0);
    expect(r.text).toContain("Nothing to send.");
  });

  test("a page that holds a whole posting sends its role text, not the 300-character listing snippet", async () => {
    const sent: Array<Record<string, unknown>> = [];
    const description = `${"You will design and run backend services in Go and Kubernetes for customers. ".repeat(30).trim()} Closing note: references are checked.`;
    const ld = JSON.stringify({ "@type": "JobPosting", title: "Backend Engineer", description, hiringOrganization: { name: "Acme" }, url: "https://careers.acme.com/jobs/1" });
    const page = {
      title: "Jobs",
      createElement: () => {
        let html = "";
        return { get innerHTML() { return html; }, set innerHTML(v: string) { html = v; }, get textContent() { return html.replace(/<[^>]+>/g, " "); } };
      },
      querySelector: () => null,
      querySelectorAll: (sel: string) => (sel.includes("ld+json") ? [{ textContent: ld }] : []),
    } as unknown as Document;
    await runCrawlToVani(
      { doc: page, pageUrl: "https://careers.acme.com/jobs", pageTitle: "Jobs", send: async m => { sent.push(m as Record<string, unknown>); return { queued: 1 }; } },
      false, new AbortController().signal, () => {},
    );
    const jobs = sent[0]!.jobs as Array<{ snippet: string }>;
    expect(jobs.length).toBe(1);
    expect(jobs[0]!.snippet).toContain("references are checked");
  });

  test("an over-long page title goes as empty rather than breaking the batch", async () => {
    const sent: Array<Record<string, unknown>> = [];
    await runCrawlToVani(
      { doc: doc(), pageUrl: PAGE, pageTitle: "x".repeat(301), http: board(2), send: async m => { sent.push(m as Record<string, unknown>); return { queued: 2 }; } },
      false, new AbortController().signal, () => {},
    );
    expect((sent[0]!.page as { title: string }).title).toBe("");
  });

  test("a Workday board's 'CAREERS AT NVIDIA' title names the company", async () => {
    const sent: Array<Record<string, unknown>> = [];
    await runCrawlToVani(
      { doc: doc(undefined, "CAREERS AT NVIDIA"), pageUrl: PAGE, pageTitle: "Search for Jobs", http: board(1), send: async m => { sent.push(m as Record<string, unknown>); return { queued: 1 }; } },
      false, new AbortController().signal, () => {},
    );
    expect((sent[0]!.jobs as Array<{ company: string }>)[0]!.company).toBe("NVIDIA");
  });

  test("without a company on the page, the board's tenant names it", async () => {
    const sent: Array<Record<string, unknown>> = [];
    await runCrawlToVani(
      { doc: doc(), pageUrl: PAGE, pageTitle: "", http: board(1), send: async m => { sent.push(m as Record<string, unknown>); return { queued: 1 }; } },
      false, new AbortController().signal, () => {},
    );
    expect((sent[0]!.jobs as Array<{ company: string }>)[0]!.company).toBe("acme");
  });
});

describe("progressText", () => {
  test("says what phase the crawl is in", () => {
    expect(progressText({ phase: "listing", done: 3, of: 138, jobs: 60 })).toBe("Listing the board… 60 jobs found (about 138 pages)");
    expect(progressText({ phase: "details", done: 4, of: 200, jobs: 4 })).toBe("Reading descriptions 4/200…");
    expect(progressText({ phase: "pages", done: 2, jobs: 40 })).toBe("Page 2: 40 jobs so far…");
  });
});
