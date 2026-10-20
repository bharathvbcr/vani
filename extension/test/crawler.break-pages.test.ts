// Adversarial tests for the generic DOM pager and the send path of crawler.ts.
// Tests named "BUG:" fail against the code at the time of writing; the others pin invariants that hold.

import { describe, expect, test } from "bun:test";
import { type Http, type PageDeps, type PageJob, crawlListing, crawlPages, describeCrawl, findNext, sameListing, sendCrawl } from "../src/crawler";

// ---------------------------------------------------------------- fakes (copied from crawler.test.ts, plus parentElement)

interface FakeEl {
  localName: string;
  attrs: Record<string, string>;
  textContent: string;
  clicked: number;
  parentElement: FakeEl | null;
  onClick?: () => void;
  hasAttribute(n: string): boolean;
  getAttribute(n: string): string | null;
  click(): void;
}

function fel(localName: string, attrs: Record<string, string>, text = "", onClick?: () => void): FakeEl {
  return {
    localName, attrs, textContent: text, clicked: 0, onClick, parentElement: null,
    hasAttribute(n) { return n in this.attrs; },
    getAttribute(n) { return this.attrs[n] ?? null; },
    click() { this.clicked++; this.onClick?.(); },
  };
}

function fakeDoc(controls: () => FakeEl[]): Document {
  return {
    querySelector(sel: string) {
      if (sel.startsWith('a[rel~="next"]')) return controls().find(c => (c.attrs.rel ?? "").split(" ").includes("next") && c.attrs.href !== undefined) ?? null;
      return null;
    },
    querySelectorAll(sel: string) {
      if (sel === 'button, a, [role="button"]') return controls().filter(c => c.localName === "button" || c.localName === "a" || c.attrs.role === "button");
      return [];
    },
  } as unknown as Document;
}

const job = (n: number): PageJob => ({ title: `Engineer ${n}`, company: "Acme", location: "Remote", url: `https://careers.acme.com/jobs/${n}`, snippet: `role ${n}` });
const page = "https://careers.acme.com/search?q=go";
const noSleep = async () => {};

// ---------------------------------------------------------------- sameListing

describe("sameListing", () => {
  test("origin tricks never pass", () => {
    const base = "https://a.com/jobs";
    for (const u of [
      "https://a.com/jobs/../admin",
      "https://evil.com/jobs",
      "https://a.com@evil.com/jobs",
      "https://a.com.evil.com/jobs",
      "https://a.com./jobs",
      "https://a.com:8443/jobs",
      "http://a.com/jobs",
      "https://sub.a.com/jobs",
      "https://a.com.:443/jobs",
      "data:text/html,<a>",
      "javascript:alert(1)",
      "blob:https://a.com/uuid",
      "ftp://a.com/jobs",
      "file:///jobs",
    ]) expect([u, sameListing(base, u)]).toEqual([u, false]);
    // A protocol-relative or backslash link resolves against the page, so check what the resolver makes of them.
    for (const rel of ["//evil.com/jobs", "\\\\evil.com\\jobs", "/\\evil.com/jobs"]) {
      expect([rel, sameListing(base, new URL(rel, base).href)]).toEqual([rel, false]);
    }
  });

  test("host case and the default port are the same origin", () => {
    expect(sameListing("https://a.com/jobs", "https://A.COM:443/jobs?page=2")).toBe(true);
  });

  test("BUG: a job detail page with a short numeric id is not another page of the listing", () => {
    // /jobs/12345 is a job, not "page 12345": the \d{1,5} path-number rule makes every job with <= 5 digits a sibling.
    expect(sameListing("https://a.com/jobs", "https://a.com/jobs/12345")).toBe(false);
    expect(sameListing("https://a.com/jobs/1234", "https://a.com/jobs/5678")).toBe(false);
  });

  test("a real path page number still passes", () => {
    expect(sameListing("https://a.com/jobs/page/1", "https://a.com/jobs/page/2")).toBe(true);
    expect(sameListing("https://a.com/jobs/", "https://a.com/jobs/page/2")).toBe(true);
    // A bare trailing number is a job id as often as a page number, so only /page/N counts as paging.
    expect(sameListing("https://a.com/jobs", "https://a.com/jobs/99999")).toBe(false);
    expect(sameListing("https://a.com/jobs", "https://a.com/jobs/123456")).toBe(false);
  });

  test("BUG: a next link that swaps the search itself (not the page number) is not the same listing", () => {
    const doc = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=sales-promo&page=2" })]);
    expect(findNext(doc, page).kind).not.toBe("load");
  });
});

// ---------------------------------------------------------------- findNext

describe("findNext", () => {
  test("a next link that leaves the listing is refused and says why", () => {
    for (const href of ["//evil.com/search?page=2", "https://a.com@evil.com/", "/admin", "/search/../admin", "data:text/html,x", "javascript:void(0)", "JaVaScRiPt:void(0)", "vbscript:x"]) {
      const s = findNext(fakeDoc(() => [fel("a", { rel: "next", href })]), page);
      expect([href, s.kind]).not.toEqual([href, "load"]);
      const s2 = findNext(fakeDoc(() => [fel("a", { href }, "Next")]), page);
      expect([href, s2.kind]).not.toEqual([href, "load"]);
    }
  });

  test("a real next link is loaded, never clicked", () => {
    const s = findNext(fakeDoc(() => [fel("a", { href: "/search?q=go&page=2" }, "Next")]), page);
    expect(s).toEqual({ kind: "load", url: "https://careers.acme.com/search?q=go&page=2" });
  });

  test("BUG: a hash-only next link (SPA pager) is clicked, not fetched as if it were another page", () => {
    // Fetching `…?q=go#page-2` returns the same document, so the pager is never advanced.
    const s = findNext(fakeDoc(() => [fel("a", { href: "#page-2" }, "Next")]), page);
    expect(s.kind).toBe("click");
  });

  test("BUG: <a rel=next href=#> is a JS control: clicked, not fetched", () => {
    const s = findNext(fakeDoc(() => [fel("a", { rel: "next", href: "#" }, "Next")]), page);
    expect(s.kind).toBe("click");
  });

  test.each(["Next ›", "Next »", "Next →", "Next >", "Next Page ›", "NEXT", "Next page »"])("BUG: %p is a next control", text => {
    // NEXT_TEXT is anchored, so "Next ›" is not recognised, findNext answers a bare "none" and the crawl reports complete.
    const s = findNext(fakeDoc(() => [fel("button", {}, text)]), page);
    expect(s.kind).toBe("click");
  });

  test("BUG: a job link whose aria-label merely contains the word 'next' does not hide the real pager", () => {
    const real = fel("button", { "aria-label": "Next page" });
    const card = fel("a", { href: "/careers/next-gen-platform-engineer", "aria-label": "Next Gen Platform Engineer" }, "Next Gen Platform Engineer");
    const s = findNext(fakeDoc(() => [card, real]), page);
    expect(s.kind).toBe("click");
    if (s.kind === "click") expect(s.el).toBe(real as unknown as HTMLElement);
  });

  test("BUG: on a job page, 'Next job' (/jobs/12346) is not the listing's next page", () => {
    const s = findNext(fakeDoc(() => [fel("a", { href: "/jobs/12346", "aria-label": "Next job" }, "Next job")]), "https://careers.acme.com/jobs/12345");
    expect(s.kind).not.toBe("load");
  });

  test("BUG: more than 3000 controls before the pager do not make the listing look finished", () => {
    // A load-more list grows with every click; past 3000 links the cap silently drops the button and the crawl says complete.
    const links = Array.from({ length: 3500 }, (_, i) => fel("a", { href: `/jobs/slug-${i}` }, `Job ${i}`));
    const more = fel("button", {}, "Show more jobs");
    const s = findNext(fakeDoc(() => [...links, more]), page);
    expect(s).not.toEqual({ kind: "none" });
  });

  test("a disabled next, in any of the three spellings, is the end", () => {
    for (const attrs of [{ disabled: "" }, { "aria-disabled": "true" }, { class: "btn is-disabled" }]) {
      expect(findNext(fakeDoc(() => [fel("button", attrs, "Next")]), page)).toEqual({ kind: "none" });
    }
  });
});

// ---------------------------------------------------------------- crawlPages

describe("crawlPages: stopping", () => {
  test("BUG: an abort during the pause before the click does not click", async () => {
    const ac = new AbortController();
    const next = fel("button", {}, "Next", () => {});
    const deps: PageDeps = { scrape: () => [job(1)], sleep: async ms => { if (ms === 7) ac.abort(); } };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { signal: ac.signal, delayMs: 7 });
    expect(next.clicked).toBe(0);
    expect(r.complete).toBe(false);
  });

  test("BUG: an abort during the pause before a fetch does not fetch", async () => {
    const ac = new AbortController();
    let loads = 0;
    const doc = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    const deps: PageDeps = {
      scrape: () => [job(1)],
      load: async () => { loads++; return doc; },
      sleep: async ms => { if (ms === 7) ac.abort(); },
    };
    const r = await crawlPages(doc, page, deps, { signal: ac.signal, delayMs: 7 });
    expect(loads).toBe(0);
    expect(r.complete).toBe(false);
  });

  test("an abort during the click-wait stops within one poll", async () => {
    const ac = new AbortController();
    let polls = 0;
    let scrapes = 0;
    const next = fel("button", {}, "Next");
    const deps: PageDeps = {
      scrape: () => { scrapes++; return [job(1)]; },
      sleep: async ms => { if (ms === 250 && ++polls === 2) ac.abort(); },
    };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { signal: ac.signal, pageWaitMs: 60_000, delayMs: 0 });
    expect(polls).toBe(2);
    expect(scrapes).toBeLessThanOrEqual(3);
    expect(r.complete).toBe(false);
    expect(r.notes).toEqual(["stopped"]);
  });

  test("already aborted: nothing is scraped, nothing is clicked", async () => {
    const ac = new AbortController();
    ac.abort();
    let scrapes = 0;
    const next = fel("button", {}, "Next");
    const r = await crawlPages(fakeDoc(() => [next]), page, { scrape: () => { scrapes++; return [job(1)]; }, sleep: noSleep }, { signal: ac.signal });
    expect([scrapes, next.clicked, r.complete, r.jobs.length]).toEqual([0, 0, false, 0]);
  });

  test("a load that rejects because of the abort is 'stopped', not a load failure", async () => {
    const ac = new AbortController();
    const doc = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    const deps: PageDeps = {
      scrape: () => [job(1)],
      load: (_u, signal) => new Promise((_, reject) => { signal.addEventListener("abort", () => reject(new Error("aborted"))); queueMicrotask(() => ac.abort()); }),
      sleep: noSleep,
    };
    const r = await crawlPages(doc, page, deps, { signal: ac.signal, delayMs: 0 });
    expect(r.notes).toEqual(["stopped"]);
    expect(r.complete).toBe(false);
  });

  test("a load that never settles is cut by the per-load timeout signal", async () => {
    const doc = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    const deps: PageDeps = {
      scrape: () => [job(1)],
      load: (_u, signal) => new Promise((_, reject) => signal.addEventListener("abort", () => reject(new Error("timed out")))),
      sleep: noSleep,
    };
    const r = await crawlPages(doc, page, deps, { delayMs: 0, timeoutMs: 20 });
    expect(r.complete).toBe(false);
    expect(r.notes[0]).toContain("could not be loaded");
    expect(r.jobs.length).toBe(1);
  });

  test("an endless pager of fresh pages stops at the page bound with exactly maxPages-1 fetches", async () => {
    let n = 0;
    const mk = (): Document => fakeDoc(() => [fel("a", { rel: "next", href: `/search?q=go&page=${n + 2}` })]);
    const docs = new Map<Document, number>();
    const first = mk();
    docs.set(first, 0);
    let fetched = 0;
    const deps: PageDeps = {
      scrape: d => [job((docs.get(d) ?? 0) * 2), job((docs.get(d) ?? 0) * 2 + 1)],
      load: async () => { fetched++; n++; const d = mk(); docs.set(d, n); return d; },
      sleep: noSleep,
    };
    const r = await crawlPages(first, page, deps, { maxPages: 6, delayMs: 0 });
    expect(r.parts).toBe(6);
    expect(fetched).toBe(5);
    expect(r.requests).toBe(5);
    expect(r.complete).toBe(false);
    expect(r.notes).toEqual(["stopped at the limit of 6 pages"]);
  });

  test("a load that hands back the same document ends the walk (no new jobs), not a loop", async () => {
    const doc = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    let loads = 0;
    const r = await crawlPages(doc, page, { scrape: () => [job(1), job(2)], load: async () => { loads++; return doc; }, sleep: noSleep }, { delayMs: 0 });
    expect(loads).toBe(1);
    expect(r.complete).toBe(false);
    expect(r.jobs.length).toBe(2);
  });

  test("two pages whose rel=next point at each other end at the first repeat", async () => {
    const a: Document = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    const b: Document = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=1" })]);
    let loads = 0;
    const r = await crawlPages(a, page, { scrape: d => [job(d === a ? 1 : 2)], load: async () => { loads++; return loads % 2 ? b : a; }, sleep: noSleep }, { delayMs: 0, maxPages: 50 });
    expect(loads).toBeLessThanOrEqual(3);
    expect(r.complete).toBe(false);
  });

  test("maxJobs reached in the middle of a page keeps exactly maxJobs and is not complete", async () => {
    const next = fel("button", {}, "Next");
    const r = await crawlPages(fakeDoc(() => [next]), page, { scrape: () => [job(1), job(2), job(3), job(4)], sleep: noSleep }, { maxJobs: 3, delayMs: 0 });
    expect(r.jobs.length).toBe(3);
    expect(r.complete).toBe(false);
    expect(r.notes).toEqual(["stopped at 3 jobs"]);
    expect(next.clicked).toBe(0);
  });

  test("a listing of exactly maxJobs jobs with no next control is not complete when the bound was hit", async () => {
    const r = await crawlPages(fakeDoc(() => []), page, { scrape: () => [job(1), job(2)], sleep: noSleep }, { maxJobs: 2, delayMs: 0 });
    expect(r.jobs.length).toBe(2);
    // Stopping at the bound is reported as a stop; it never claims the listing ended.
    expect(r.complete).toBe(false);
  });

  test("a scrape that throws mid-walk keeps what was read and is never complete", async () => {
    let p = 0;
    const next = fel("button", {}, "Next", () => { p++; });
    const deps: PageDeps = { scrape: () => { if (p === 2) throw new Error("boom"); return [job(p)]; }, sleep: noSleep };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { delayMs: 0 });
    expect(r.jobs.length).toBe(2);
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toContain("the walk failed: boom");
  });

  test("a scrape that throws during the click-wait is the same", async () => {
    let clicked = false;
    const next = fel("button", {}, "Next", () => { clicked = true; });
    const deps: PageDeps = { scrape: () => { if (clicked) throw new Error("detached"); return [job(1)]; }, sleep: noSleep };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { delayMs: 0 });
    expect(r.jobs.length).toBe(1);
    expect(r.complete).toBe(false);
  });

  test("a pager whose clicks only reorder the same jobs ends with 'no new jobs', never complete", async () => {
    let flip = false;
    const next = fel("button", {}, "Next", () => { flip = !flip; });
    const deps: PageDeps = { scrape: () => (flip ? [job(2), job(1)] : [job(1), job(2)]), sleep: noSleep };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { delayMs: 0 });
    expect(r.jobs.length).toBe(2);
    expect(r.complete).toBe(false);
    expect(r.notes).toEqual(["page 2 showed no jobs not already seen"]);
  });

  test("a next that stays enabled and changes nothing is bounded by the wait, not clicked forever", async () => {
    const next = fel("button", {}, "Next");
    const r = await crawlPages(fakeDoc(() => [next]), page, { scrape: () => [job(1)], sleep: noSleep }, { delayMs: 0, pageWaitMs: 1000 });
    expect(next.clicked).toBe(1);
    expect(r.complete).toBe(false);
  });

  test("a fetched page's click-only pager is not clicked on the wrong document", async () => {
    const second = fakeDoc(() => [fel("button", {}, "Next")]);
    const first = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    const deps: PageDeps = { scrape: d => [job(d === first ? 1 : 2)], load: async () => second, sleep: noSleep };
    const r = await crawlPages(first, page, deps, { delayMs: 0 });
    expect(r.jobs.length).toBe(2);
    expect(r.complete).toBe(false);
    expect(r.notes).toEqual(["a fetched page's controls cannot be clicked"]);
  });

  test("a next link and no loader: reported, not complete", async () => {
    const doc = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    const r = await crawlPages(doc, page, { scrape: () => [job(1)], sleep: noSleep }, { delayMs: 0 });
    expect(r.complete).toBe(false);
    expect(r.notes[0]).toContain("cannot be read here");
  });
});

describe("crawlPages: what it reports as complete", () => {
  test("BUG: a 'Next ›' pager is walked, not reported as the whole listing after page 1", async () => {
    let p = 0;
    const next = fel("button", {}, "Next ›", () => { p++; if (p === 2) next.attrs.disabled = ""; });
    const r = await crawlPages(fakeDoc(() => [next]), page, { scrape: () => [job(p * 2), job(p * 2 + 1)], sleep: noSleep }, { delayMs: 0 });
    expect(r.jobs.length).toBe(6);
  });

  test("BUG: the last-page Bootstrap pager (<li class=disabled><a href=#>Next) ends the walk as complete, not as a 10 s timeout", async () => {
    let p = 0;
    const li = fel("li", { class: "page-item" });
    // A real disabled pager item does nothing when clicked.
    const a = fel("a", { href: "#", class: "page-link" }, "Next", () => { if (li.attrs.class.includes("disabled")) return; p++; if (p === 1) li.attrs.class = "page-item disabled"; });
    a.parentElement = li;
    const r = await crawlPages(fakeDoc(() => [a]), page, { scrape: () => [job(p * 2), job(p * 2 + 1)], sleep: noSleep }, { delayMs: 0, pageWaitMs: 5000 });
    expect(r.jobs.length).toBe(4);
    expect(a.clicked).toBe(1); // not clicked again on the last page
    expect(r.complete).toBe(true);
  });

  test("BUG: an enabled carousel arrow before the real pager does not end the crawl at page 1", async () => {
    let p = 0;
    const carousel = fel("button", { "aria-label": "Next slide" });
    const pager = fel("button", { "aria-label": "Next page" }, "", () => { p++; if (p === 1) pager.attrs.disabled = ""; });
    const r = await crawlPages(fakeDoc(() => [carousel, pager]), page, { scrape: () => [job(p * 2), job(p * 2 + 1)], sleep: noSleep }, { delayMs: 0, pageWaitMs: 1000 });
    expect(r.jobs.length).toBe(4);
  });

  test("BUG: a list that is emptied while the next page loads is not read as 'no new jobs'", async () => {
    let clock = 0;
    let ready = 0;
    let p = 0;
    const next = fel("button", {}, "Next", () => { p++; ready = clock + 2; if (p === 1) next.attrs.disabled = ""; });
    const deps: PageDeps = {
      scrape: () => (clock < ready ? [] : [job(p * 2), job(p * 2 + 1)]),
      sleep: async ms => { if (ms === 250) clock++; },
    };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { delayMs: 0, pageWaitMs: 5000 });
    expect(r.jobs.length).toBe(4);
    expect(r.complete).toBe(true);
  });

  test("BUG: a page that renders progressively is read whole before the next click (else jobs vanish and complete is true)", async () => {
    let clock = 0;
    let ready = 0;
    let p = 0;
    const next = fel("button", {}, "Next", () => { p++; ready = clock + 2; if (p === 2) next.attrs.disabled = ""; });
    const full = (n: number) => [job(n * 3), job(n * 3 + 1), job(n * 3 + 2)];
    const deps: PageDeps = {
      scrape: () => (p > 0 && clock < ready ? full(p).slice(0, 1) : full(p)),
      sleep: async ms => { if (ms === 250) clock++; },
    };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { delayMs: 0, pageWaitMs: 5000 });
    expect(r.jobs.length).toBe(9);
    expect(r.complete).toBe(true);
  });

  test("BUG: job links that differ only by letter case are different jobs", async () => {
    const a = { ...job(1), url: "https://careers.acme.com/jobs/AbC123" };
    const b = { ...job(2), url: "https://careers.acme.com/jobs/abc123" };
    const r = await crawlPages(fakeDoc(() => []), page, { scrape: () => [a, b], sleep: noSleep }, { delayMs: 0 });
    expect(r.jobs.length).toBe(2);
  });

  test("a trailing slash on a job link is the same job across pages", async () => {
    let p = 0;
    const next = fel("button", {}, "Next", () => { p++; if (p === 1) next.attrs.disabled = ""; });
    const deps: PageDeps = { scrape: () => (p === 0 ? [job(1)] : [job(1), { ...job(2) }, { ...job(1), url: "https://careers.acme.com/jobs/1/" }]), sleep: noSleep };
    const r = await crawlPages(fakeDoc(() => [next]), page, deps, { delayMs: 0 });
    expect(r.jobs.map(j => j.title)).toEqual(["Engineer 1", "Engineer 2"]);
  });

  test("a clean walk to a disabled next is complete and carries no notes", async () => {
    let p = 0;
    const next = fel("button", {}, "Next", () => { p++; if (p === 1) next.attrs.disabled = ""; });
    const r = await crawlPages(fakeDoc(() => [next]), page, { scrape: () => [job(p * 2), job(p * 2 + 1)], sleep: noSleep }, { delayMs: 0 });
    expect([r.jobs.length, r.complete, r.notes]).toEqual([4, true, []]);
    expect(describeCrawl(r)).toBe("Read 4 jobs from 2 pages.");
  });

  test("maxPages is the walk's page count, and never reads 'complete'", async () => {
    let p = 0;
    const next = fel("button", {}, "Next", () => { p++; });
    const r = await crawlPages(fakeDoc(() => [next]), page, { scrape: () => [job(p)], sleep: noSleep }, { maxPages: 1, delayMs: 0 });
    expect(r.parts).toBe(1);
    expect(r.complete).toBe(false);
    expect(next.clicked).toBe(0);
  });
});

describe("crawlListing", () => {
  const trap: Http & { calls: string[] } = { calls: [], sleep: noSleep, fetch: async url => { trap.calls.push(url); return new Response("{}"); } };

  test("a page that only looks like a Workday board is walked through the DOM, never through the board API", async () => {
    for (const u of ["https://acme.wd5.myworkdayjobs.com.evil.com/External", "https://acme.wd5.myworkdayjobs.com:8443/External", "http://acme.wd5.myworkdayjobs.com/External"]) {
      const r = await crawlListing(fakeDoc(() => []), u, { scrape: () => [job(1)], sleep: noSleep }, { delayMs: 0 }, trap);
      expect([u, r.mode]).toEqual([u, "pages"]);
    }
    expect(trap.calls).toEqual([]);
  });
});

// ---------------------------------------------------------------- sendCrawl

const ingestJob = (i: number) => ({ title: `Role ${i}`, company: "Acme", location: "", url: `https://careers.acme.com/jobs/${i}`, snippet: "x" });
const bad = (i: number) => ({ ...ingestJob(i), url: "javascript:alert(1)" });
const pageRef = { url: "https://careers.acme.com/search", title: "Jobs" };
const jobsOf = (n: number) => Array.from({ length: n }, (_, i) => ingestJob(i));
type Msg = { type: string; saveSearch: boolean; jobs: unknown[] };

describe("sendCrawl", () => {
  test.each([[0, []], [1, [1]], [199, [199]], [200, [200]], [201, [200, 1]], [400, [200, 200]], [401, [200, 200, 1]]] as Array<[number, number[]]>)(
    "%i jobs go as batches %p, saveSearch only on the first", async (n, sizes) => {
      const sent: Msg[] = [];
      const r = await sendCrawl({ jobs: jobsOf(n) }, pageRef, true, async m => { sent.push(m as Msg); return { queued: (m as Msg).jobs.length }; });
      expect(sent.map(s => s.jobs.length)).toEqual(sizes);
      expect(sent.map(s => s.saveSearch)).toEqual(sizes.map((_, i) => i === 0));
      expect(sent.every(s => s.type === "ingestJobs")).toBe(true);
      expect([r.sent, r.batches, r.leftOut]).toEqual([n, sizes.length, ""]);
    });

  test("saveSearch=false is never turned on", async () => {
    const sent: Msg[] = [];
    await sendCrawl({ jobs: jobsOf(450) }, pageRef, false, async m => { sent.push(m as Msg); return { queued: 1 }; });
    expect(sent.map(s => s.saveSearch)).toEqual([false, false, false]);
  });

  test("when the first chunk is all unusable, the search is saved by the first batch that goes", async () => {
    const jobs = [...Array.from({ length: 200 }, (_, i) => bad(i)), ...jobsOf(5)];
    const sent: Msg[] = [];
    const r = await sendCrawl({ jobs }, pageRef, true, async m => { sent.push(m as Msg); return { queued: 5 }; });
    expect(sent.length).toBe(1);
    expect(sent[0]!.saveSearch).toBe(true);
    expect(r.sent).toBe(5);
    expect(r.leftOut).toContain("200 not sent");
  });

  test("unusable jobs are counted once across batches and no batch exceeds 200", async () => {
    const jobs = jobsOf(450).map((j, i) => (i % 64 === 0 ? bad(i) : j));
    const badCount = jobs.filter(j => j.url.startsWith("javascript")).length;
    const sent: Msg[] = [];
    const r = await sendCrawl({ jobs }, pageRef, false, async m => { sent.push(m as Msg); return { queued: (m as Msg).jobs.length }; });
    expect(sent.every(s => s.jobs.length <= 200)).toBe(true);
    expect(r.sent).toBe(450 - badCount);
    expect(r.leftOut).toBe(`${badCount} not sent (no usable link or title, or a field too long)`);
  });

  test("a reply of {queued:0, duplicates:n} is not a failure: the send goes on", async () => {
    let calls = 0;
    const r = await sendCrawl({ jobs: jobsOf(450) }, pageRef, false, async m => { calls++; return { queued: 0, duplicates: (m as Msg).jobs.length }; });
    expect(calls).toBe(3);
    expect(r.outcomes.length).toBe(3);
  });

  test.each([
    ["error with a queued count", { queued: 0, error: "boom", code: "unreachable" }],
    ["error with a positive queued count", { queued: 5, error: "disk full" }],
    ["negative queued", { queued: -1 }],
    ["NaN queued", { queued: Number.NaN }],
    ["rejected that is not a list", { queued: 1, rejected: "all" }],
  ] as Array<[string, unknown]>)("BUG: %s is a failure and ends the send", async (_name, reply) => {
    // The stop test is `!ok && queued is a number`, so any malformed or error reply that carries a number slips through as a success.
    let calls = 0;
    const r = await sendCrawl({ jobs: jobsOf(450) }, pageRef, false, async () => { calls++; return reply; });
    expect(calls).toBe(1);
    expect(r.outcomes[0]!.ok).toBe(false);
    expect(r.sent).toBe(0);
  });

  test.each([
    ["undefined", undefined], ["null", null], ["a string", "ok"], ["an array", [{ queued: 1 }]], ["an empty object", {}], ["queued as text", { queued: "3" }],
  ] as Array<[string, unknown]>)("a reply that is %s ends the send as a failure", async (_name, reply) => {
    let calls = 0;
    const r = await sendCrawl({ jobs: jobsOf(450) }, pageRef, false, async () => { calls++; return reply; });
    expect(calls).toBe(1);
    expect(r.sent).toBe(0);
    expect(r.outcomes[0]!.ok).toBe(false);
    expect(r.leftOut).toContain("450 not sent");
  });

  test("a thrown send, Error or not, ends the send and nothing after it is sent", async () => {
    for (const thrown of [new Error("port closed"), "plain string", 42, null]) {
      let calls = 0;
      const r = await sendCrawl({ jobs: jobsOf(450) }, pageRef, false, async () => { calls++; throw thrown; });
      expect([calls, r.sent, r.outcomes[0]!.ok]).toEqual([1, 0, false]);
    }
  });

  test("a failure on a later batch keeps the earlier count and counts the rest", async () => {
    let n = 0;
    const r = await sendCrawl({ jobs: jobsOf(450) }, pageRef, true, async () => (++n === 2 ? { error: "refused" } : { queued: 200 }));
    expect(n).toBe(2);
    expect(r.sent).toBe(200);
    expect(r.leftOut).toContain("250 not sent");
  });

  test("BUG: the jobs a failed batch could not carry are not counted twice in the left-out words", async () => {
    // batch.skipped is added to `skipped` before the send, and unsent(i) counts the whole chunk again on failure.
    const r = await sendCrawl({ jobs: [ingestJob(1), bad(2), ingestJob(3)] }, pageRef, false, async () => ({ error: "nope" }));
    const counts = [...r.leftOut.matchAll(/(\d+) not sent/g)].map(m => Number(m[1]));
    expect(counts.reduce((a, b) => a + b, 0)).toBe(3);
  });

  test("an abort between batches stops before the next and counts every job that did not go", async () => {
    const ac = new AbortController();
    let calls = 0;
    const r = await sendCrawl({ jobs: jobsOf(450) }, pageRef, true, async () => { calls++; ac.abort(); return { queued: 200 }; }, ac.signal);
    expect(calls).toBe(1);
    expect(r.sent).toBe(200);
    expect(r.leftOut).toBe("250 not sent (stopped)");
  });

  test("an abort before the first batch sends nothing", async () => {
    const ac = new AbortController();
    ac.abort();
    let calls = 0;
    const r = await sendCrawl({ jobs: jobsOf(10) }, pageRef, true, async () => { calls++; return { queued: 10 }; }, ac.signal);
    expect([calls, r.sent, r.batches, r.leftOut]).toEqual([0, 0, 0, "10 not sent (stopped)"]);
  });

  test("BUG: jobs Vani rejected outright are not reported as sent", async () => {
    // {queued:0, rejected:[...]} is ok:false but numeric, so it continues and counts the whole batch in `sent`.
    const r = await sendCrawl({ jobs: jobsOf(3) }, pageRef, false, async () => ({ queued: 0, rejected: [{ index: 0, reason: "x" }, { index: 1, reason: "x" }, { index: 2, reason: "x" }] }));
    expect(r.sent).toBe(0);
  });
});
