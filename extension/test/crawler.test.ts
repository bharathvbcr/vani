import { describe, expect, test } from "bun:test";
import {
  type CrawlReport,
  type Http,
  type PageDeps,
  type PageJob,
  MAX_BATCH_BYTES,
  WORKDAY_WINDOW,
  wireBytes,
  chooseSplit,
  clipBytes,
  crawlPages,
  crawlWorkday,
  describeCrawl,
  excerpt,
  findNext,
  parseWorkdayBoard,
  plainText,
  pool,
  readFacets,
  sameListing,
  sendCrawl,
} from "../src/crawler";

const ORIGIN = "https://acme.wd5.myworkdayjobs.com";
const bytes = (s: string) => new TextEncoder().encode(s).length;
const hex = (n: number) => n.toString(16).padStart(32, "0");

// ---------------------------------------------------------------- A fake Workday board

interface FakeJob { req: string; title: string; family: number; time: number; desc: string }

interface Board {
  jobs: FakeJob[];
  /** Offer the family facet (single-valued, covers every job). */
  families?: number;
  /** Serve page 1 again from this offset on (Workday does it at WORKDAY_WINDOW). */
  wrapAt?: number;
  /** Status codes to answer before the real answer, per URL kind. */
  failList?: number[];
  failDetail?: number[];
  detail?: (j: FakeJob) => unknown;
  listBody?: (offset: number) => string | undefined;
  hang?: boolean;
}

function makeJobs(n: number, families = 3, desc = (i: number) => `<p>Role ${i}</p><p>What you'll be doing: build things.</p>`): FakeJob[] {
  return Array.from({ length: n }, (_, i) => ({
    req: `JR${100000 + i}`,
    title: i % 7 === 0 ? `Senior Software Engineer ${i}` : `Sales Associate ${i}`,
    family: i % families,
    time: i % 50 === 0 ? 1 : 0,
    desc: desc(i),
  }));
}

const path = (j: FakeJob) => `/job/US-CA-Santa-Clara/Role_${j.req}`;

function fakeWorkday(board: Board): Http & { calls: string[]; bodies: Array<Record<string, unknown>> } {
  const calls: string[] = [];
  const bodies: Array<Record<string, unknown>> = [];
  const failList = [...(board.failList ?? [])];
  const failDetail = [...(board.failDetail ?? [])];
  const wrapAt = board.wrapAt ?? WORKDAY_WINDOW;
  const families = board.families ?? 0;
  const http = {
    calls,
    bodies,
    sleep: async () => {},
    async fetch(url: string, init: RequestInit): Promise<Response> {
      calls.push(url);
      if (board.hang) {
        return new Promise((_, reject) => init.signal?.addEventListener("abort", () => reject(new Error("aborted"))));
      }
      const listUrl = `${ORIGIN}/wday/cxs/acme/External/jobs`;
      if (url === listUrl) {
        const f = failList.shift();
        if (f) return new Response("", { status: f });
        const body = JSON.parse(String(init.body)) as { appliedFacets: Record<string, string[]>; offset: number; limit: number; searchText: string };
        bodies.push(body);
        const custom = board.listBody?.(body.offset);
        if (custom !== undefined) return new Response(custom, { status: 200 });
        const fam = body.appliedFacets.jobFamilyGroup;
        const tt = body.appliedFacets.timeType;
        const where = (useFam: boolean, useTt: boolean) => board.jobs.filter(j =>
          (!useFam || !fam || fam.includes(hex(j.family + 1))) &&
          (!useTt || !tt || tt.includes(hex(1000 + j.time))) &&
          (!body.searchText || j.title.toLowerCase().includes(body.searchText.toLowerCase())));
        const match = where(true, true);
        // As Workday does (seen live on NVIDIA): an applied facet's counts ignore that facet's own filter,
        // so its other options show their sizes; every other facet counts this query's jobs.
        const famBase = where(false, true);
        const ttBase = where(true, false);
        const offset = body.offset >= wrapAt ? 0 : body.offset;
        const rows = match.slice(offset, offset + body.limit).map(j => ({
          title: j.title, externalPath: path(j), locationsText: "US, CA, Santa Clara", postedOn: "Posted Today", bulletFields: [j.req],
        }));
        const facets: unknown[] = [{
          facetParameter: "timeType",
          values: [0, 1].map(t => ({ descriptor: `t${t}`, id: hex(1000 + t), count: ttBase.filter(j => j.time === t).length })),
        }];
        if (families > 0) {
          facets.unshift({
            facetParameter: "jobFamilyGroup",
            values: Array.from({ length: families }, (_, k) => ({ descriptor: `f${k}`, id: hex(k + 1), count: famBase.filter(j => j.family === k).length })),
          });
          facets.push({ facetParameter: "locationMainGroup", values: [{ facetParameter: "locations", values: [{ id: hex(9999), count: match.length * 2 }, { id: hex(9998), count: 5 }] }] });
        }
        return new Response(JSON.stringify({ total: Math.min(match.length, WORKDAY_WINDOW), jobPostings: rows, facets }), { status: 200 });
      }
      const prefix = `${ORIGIN}/wday/cxs/acme/External/job/`;
      if (url.startsWith(prefix)) {
        const f = failDetail.shift();
        if (f) return new Response("", { status: f });
        const j = board.jobs.find(x => url === `${ORIGIN}/wday/cxs/acme/External${path(x)}`);
        if (!j) return new Response("", { status: 404 });
        const info = board.detail ? board.detail(j) : {
          jobPostingInfo: {
            title: j.title, jobDescription: j.desc, location: "US, CA, Santa Clara", additionalLocations: ["US, Remote"],
            startDate: "2026-10-19", jobReqId: j.req, externalUrl: `${ORIGIN}/External${path(j)}`,
          },
        };
        return new Response(JSON.stringify(info), { status: 200 });
      }
      return new Response("", { status: 404 });
    },
  };
  return http;
}

const board = parseWorkdayBoard(`${ORIGIN}/en-US/External`)!;

describe("parseWorkdayBoard", () => {
  test("reads the NVIDIA board from its careers URL", () => {
    expect(parseWorkdayBoard("https://nvidia.wd5.myworkdayjobs.com/NVIDIAExternalCareerSite")).toEqual({
      origin: "https://nvidia.wd5.myworkdayjobs.com", tenant: "nvidia", site: "NVIDIAExternalCareerSite", searchText: "", facets: {},
    });
  });
  test("skips the locale and keeps the search and the applied facets", () => {
    const b = parseWorkdayBoard(`${ORIGIN}/en-US/External?q=gpu+compiler&jobFamilyGroup=${hex(1)}&jobFamilyGroup=${hex(2)}&utm_source=x&timeType=junk`);
    expect(b?.site).toBe("External");
    expect(b?.searchText).toBe("gpu compiler");
    expect(b?.facets).toEqual({ jobFamilyGroup: [hex(1), hex(2)] });
  });
  test("a job page belongs to its board", () => {
    expect(parseWorkdayBoard(`${ORIGIN}/External/job/US-CA/Role_JR1`)?.site).toBe("External");
  });
  test("refuses what is not a Workday board", () => {
    for (const u of [
      "http://acme.wd5.myworkdayjobs.com/External",
      "https://acme.wd5.myworkdayjobs.com:8443/External",
      "https://acme.myworkdayjobs.com/External",
      "https://evil.com/acme.wd5.myworkdayjobs.com/External",
      "https://acme.wd5.myworkdayjobs.com.evil.com/External",
      "https://acme.wd5.myworkdayjobs.com/",
      "https://acme.wd5.myworkdayjobs.com/en-US",
      "https://acme.wd5.myworkdayjobs.com/wday/cxs/acme/External/jobs",
      "https://acme.wd5.myworkdayjobs.com/Ext%2Fernal",
      "not a url",
    ]) expect(parseWorkdayBoard(u)).toBeNull();
  });
  test("bounds the facets a URL can carry", () => {
    const many = Array.from({ length: 40 }, (_, i) => `f${i}=${hex(i)}`).join("&");
    expect(Object.keys(parseWorkdayBoard(`${ORIGIN}/External?${many}`)!.facets).length).toBe(20);
  });
});

describe("crawlWorkday", () => {
  test("a small board: every page, every description, dated, in the board's own link form", async () => {
    const http = fakeWorkday({ jobs: makeJobs(45) });
    const r = await crawlWorkday(board, { company: "Acme" }, http);
    expect(r.listed).toBe(45);
    expect(r.jobs.length).toBe(45);
    expect(r.complete).toBe(true);
    expect(r.notes).toEqual([]);
    expect(r.requests).toBe(3 + 45);
    const j = r.jobs.find(x => x.title === "Senior Software Engineer 0")!;
    expect(j.url).toBe(`${ORIGIN}/External/job/US-CA-Santa-Clara/Role_JR100000`);
    expect(j.posted).toBe("2026-10-19T00:00:00Z");
    expect(j.location).toBe("US, CA, Santa Clara; US, Remote");
    expect(j.company).toBe("Acme");
    expect(j.snippet).toBe("Role 0\nWhat you'll be doing: build things.");
  });

  test("a board over the window is split by a facet and listed whole, with no page read twice", async () => {
    const jobs = makeJobs(2752, 3);
    const http = fakeWorkday({ jobs, families: 3 });
    const r = await crawlWorkday(board, { maxDetails: 0 }, http);
    expect(r.expected).toBe(2752);
    expect(r.listed).toBe(2752);
    // One split, never a re-split of a bucket the applied facet's whole-board counts make look big.
    expect(r.parts).toBe(4);
    expect(r.notes).toEqual(["2752 less relevant jobs were listed but not sent (descriptions fetched for the top 0)"]);
    const listBodies = http.bodies;
    const keys = listBodies.map(b => JSON.stringify([b.appliedFacets, b.offset]));
    expect(new Set(keys).size).toBe(keys.length);
    expect(listBodies.every(b => (b.offset as number) < WORKDAY_WINDOW)).toBe(true);
    expect(listBodies.slice(1).every(b => Object.keys(b.appliedFacets as object)[0] === "jobFamilyGroup")).toBe(true);
  });

  test("without a facet to split by, it reads the window, stops at the wrap and says what it missed", async () => {
    const jobs = makeJobs(2500, 1).map(j => ({ ...j, time: 0 }));
    const http = fakeWorkday({ jobs });
    const r = await crawlWorkday(board, { maxDetails: 0 }, http);
    expect(r.listed).toBe(WORKDAY_WINDOW);
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toContain("could not be split");
    expect(http.bodies.every(b => (b.offset as number) < WORKDAY_WINDOW)).toBe(true);
  });

  // Quiet run is about 2.7s (4300 synthetic jobs, two facet splits). Bun's default
  // 5s fails the test when a Go race suite and a Swift build share the machine.
  test("a bucket still over the window is split again by a second facet", async () => {
    const jobs = makeJobs(4300, 2).map((j, i) => ({ ...j, time: Math.floor(i / 2) % 2 }));
    const http = fakeWorkday({ jobs, families: 2 });
    const r = await crawlWorkday(board, { maxDetails: 0 }, http);
    expect(r.listed).toBe(4300);
    // Only "listed but not sent" (maxDetails 0) may be noted; no part of the listing was missed.
    expect(r.notes).toEqual(["4300 less relevant jobs were listed but not sent (descriptions fetched for the top 0)"]);
    expect(http.bodies.some(b => Object.keys(b.appliedFacets as object).length === 2)).toBe(true);
  }, 30_000);

  test("a board that serves page 1 again early is caught, not read in a loop", async () => {
    const http = fakeWorkday({ jobs: makeJobs(300), wrapAt: 100 });
    const r = await crawlWorkday(board, { maxDetails: 0 }, http);
    expect(r.listed).toBe(100);
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toContain("the board served its first page again after 100 jobs of 300");
    expect(http.bodies.length).toBe(6);
  });

  test("an answer that is not JSON, or JSON without jobPostings, fails the listing loudly", async () => {
    for (const body of ["<html>login</html>", JSON.stringify({ total: 3 }), "null", JSON.stringify({ jobPostings: "x" })]) {
      const r = await crawlWorkday(board, {}, fakeWorkday({ jobs: makeJobs(3), listBody: () => body }));
      expect(r.complete).toBe(false);
      expect(r.listed).toBe(0);
      expect(r.notes[0]).toMatch(/^listing failed: /);
    }
  });

  test("rows without a title or a /job/ link are skipped and counted", async () => {
    const rows = [
      { title: "Good", externalPath: "/job/US/Good_JR1" },
      { title: "", externalPath: "/job/US/Empty_JR2" },
      { title: "Off site", externalPath: "https://evil.example/job/x" },
      { title: "No path" },
      "junk",
    ];
    const http = fakeWorkday({ jobs: [], listBody: () => JSON.stringify({ total: 5, jobPostings: rows, facets: "nope" }) });
    const r = await crawlWorkday(board, { maxDetails: 0 }, http);
    expect(r.listed).toBe(1);
    expect(r.notes.join(" ")).toContain("4 listing rows at offset 0 had no title or usable job link");
    expect(r.complete).toBe(false);
  });

  test("429 and 5xx are retried a bounded number of times; 4xx is not", async () => {
    const ok = await crawlWorkday(board, {}, fakeWorkday({ jobs: makeJobs(2), failList: [503, 429] }));
    expect(ok.listed).toBe(2);
    const down = fakeWorkday({ jobs: makeJobs(2), failList: [503, 503, 503, 503] });
    const r = await crawlWorkday(board, {}, down);
    expect(r.listed).toBe(0);
    expect(r.notes[0]).toContain("HTTP 503");
    expect(down.calls.length).toBe(3);
    const gone = fakeWorkday({ jobs: makeJobs(2), failList: [403] });
    expect((await crawlWorkday(board, {}, gone)).notes[0]).toContain("HTTP 403");
    expect(gone.calls.length).toBe(1);
  });

  test("a board that never answers times out instead of hanging", async () => {
    const r = await crawlWorkday(board, { timeoutMs: 20 }, fakeWorkday({ jobs: [], hang: true }));
    expect(r.complete).toBe(false);
    expect(r.notes[0]).toContain("timed out after 20 ms");
  });

  test("the user's stop ends the crawl, keeps nothing half-made and fetches no descriptions", async () => {
    const ac = new AbortController();
    const http = fakeWorkday({ jobs: makeJobs(200) });
    const r = await crawlWorkday(board, { signal: ac.signal, onProgress: p => { if (p.jobs >= 60) ac.abort(); } }, http);
    expect(r.listed).toBe(60);
    expect(r.jobs).toEqual([]);
    expect(r.complete).toBe(false);
    expect(r.notes).toContain("stopped");
    expect(http.calls.every(c => c.endsWith("/jobs"))).toBe(true);
  });

  test("the request budget bounds the whole crawl", async () => {
    const http = fakeWorkday({ jobs: makeJobs(500) });
    const r = await crawlWorkday(board, { maxRequests: 10 }, http);
    expect(http.calls.length).toBe(10);
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toContain("limit of 10 requests");
  });

  test("descriptions go to the most relevant jobs first, and the rest are reported, not hidden", async () => {
    const http = fakeWorkday({ jobs: makeJobs(70) });
    const r = await crawlWorkday(board, { maxDetails: 10, profile: { targetTitles: ["software engineer"], skills: [] } }, http);
    expect(r.jobs.length).toBe(10);
    expect(r.jobs.every(j => j.title.startsWith("Senior Software Engineer"))).toBe(true);
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toContain("60 less relevant jobs were listed but not sent");
  });

  test("a job whose description cannot be read is not sent with an empty one", async () => {
    const http = fakeWorkday({
      jobs: makeJobs(4),
      detail: j => j.req === "JR100001" ? { jobPostingInfo: { title: j.title, jobDescription: "" } } : j.req === "JR100002" ? { nope: 1 } : {
        jobPostingInfo: { title: j.title, jobDescription: "<p>x</p>", externalUrl: "https://evil.example/steal" },
      },
    });
    const r = await crawlWorkday(board, {}, http);
    expect(r.jobs.map(j => j.title).sort()).toEqual(["Sales Associate 3", "Senior Software Engineer 0"]);
    expect(r.jobs.every(j => j.url.startsWith(`${ORIGIN}/External/job/`))).toBe(true);
    expect(r.notes.join(" ")).toContain("2 descriptions could not be read");
  });

  test("a long description is cut to the ingest bound, starting where the role is described", async () => {
    const boiler = "<p>" + "Acme has been transforming the world for decades. ".repeat(40) + "</p>";
    const role = "<h3>What you'll be doing:</h3><ul><li>" + "Design distributed storage in Go. ".repeat(120) + "</li></ul>";
    const http = fakeWorkday({ jobs: makeJobs(1, 1, () => boiler + role) });
    const r = await crawlWorkday(board, {}, http);
    const s = r.jobs[0]!.snippet;
    expect(bytes(s)).toBeLessThanOrEqual(4000);
    expect(s.startsWith("What you'll be doing")).toBe(true);
    expect(s.endsWith("…")).toBe(true);
    expect(r.shortened).toBe(1);
  });

  test("search text and the page's facets go to every listing request", async () => {
    const http = fakeWorkday({ jobs: makeJobs(30, 3), families: 3 });
    const b = parseWorkdayBoard(`${ORIGIN}/External?q=engineer&jobFamilyGroup=${hex(1)}`)!;
    const r = await crawlWorkday(b, { maxDetails: 0 }, http);
    expect(http.bodies.every(x => x.searchText === "engineer" && JSON.stringify(x.appliedFacets) === JSON.stringify({ jobFamilyGroup: [hex(1)] }))).toBe(true);
    expect(r.listed).toBe(makeJobs(30, 3).filter(j => j.family === 0 && j.title.toLowerCase().includes("engineer")).length);
  });
});

describe("concurrency", () => {
  test("pool keeps at most n in flight, starts nothing after a failure and awaits what started", async () => {
    let inFlight = 0, peak = 0, started = 0;
    const tick = () => new Promise(r => setTimeout(r, 1));
    await pool(Array.from({ length: 20 }, (_, i) => i), 3, async () => { started++; inFlight++; peak = Math.max(peak, inFlight); await tick(); inFlight--; });
    expect(peak).toBe(3);
    expect(started).toBe(20);
    started = 0;
    let running = 0;
    await expect(pool(Array.from({ length: 20 }, (_, i) => i), 3, async i => {
      started++; running++;
      await tick();
      running--;
      if (i === 1) throw new Error("boom");
    })).rejects.toThrow("boom");
    expect(running).toBe(0);
    expect(started).toBeLessThanOrEqual(4);
  });

  test("parallel workers never overshoot the request budget", async () => {
    for (const concurrency of [1, 3, 6]) {
      const http = fakeWorkday({ jobs: makeJobs(2752, 3), families: 3 });
      http.sleep = () => new Promise(r => setTimeout(r, 0));
      const r = await crawlWorkday(board, { maxRequests: 37, concurrency }, http);
      expect(http.calls.length).toBe(37);
      expect(r.requests).toBe(37);
    }
  });

  test("results come out in relevance order whatever order details finish in", async () => {
    const http = fakeWorkday({ jobs: makeJobs(30) });
    const fetch = http.fetch.bind(http);
    let n = 0;
    http.fetch = async (url, init) => { await new Promise(r => setTimeout(r, (n++ * 7) % 5)); return fetch(url, init); };
    const profile = { targetTitles: ["software engineer"], skills: [] };
    const a = await crawlWorkday(board, { profile, concurrency: 4 }, http);
    const b = await crawlWorkday(board, { profile, concurrency: 1 }, fakeWorkday({ jobs: makeJobs(30) }));
    expect(a.jobs.map(j => j.url)).toEqual(b.jobs.map(j => j.url));
  });
});

describe("facets", () => {
  const fam = { param: "jobFamilyGroup", values: [{ id: hex(1), count: 1758 }, { id: hex(2), count: 994 }] };
  const time = { param: "timeType", values: [{ id: hex(3), count: 2749 }, { id: hex(4), count: 3 }] };
  const loc = { param: "locations", values: [{ id: hex(5), count: 1500 }, { id: hex(6), count: 1500 }, { id: hex(7), count: 900 }] };
  const partial = { param: "workerSubType", values: [{ id: hex(8), count: 100 }, { id: hex(9), count: 50 }] };

  test("splits by a facet that covers every job with every bucket under the window", () => {
    expect(chooseSplit([time, loc, partial, fam], {})?.param).toBe("jobFamilyGroup");
  });
  test("never re-splits a facet the page already applied", () => {
    expect(chooseSplit([fam, time], { jobFamilyGroup: [hex(1)] })?.param).toBe("timeType");
  });
  test("a facet with one bucket cannot split", () => {
    expect(chooseSplit([{ param: "timeType", values: [{ id: hex(3), count: 2500 }, { id: hex(4), count: 0 }] }], {})).toBeNull();
  });
  test("reads nested groups and drops values it cannot send back", () => {
    const f = readFacets([
      { facetParameter: "jobFamilyGroup", values: [{ id: hex(1), count: 3 }, { id: "x'); drop", count: 4 }, { id: hex(2), count: -1 }, { id: hex(3), count: "9" }] },
      { facetParameter: "locationMainGroup", values: [{ facetParameter: "locations", values: [{ id: hex(5), count: 2 }] }] },
      { facetParameter: "bad name!", values: [{ id: hex(6), count: 1 }] },
      null,
    ]);
    expect(f).toEqual([
      { param: "jobFamilyGroup", values: [{ id: hex(1), count: 3 }], partial: true },
      { param: "locations", values: [{ id: hex(5), count: 2 }] },
    ]);
  });
});

describe("text", () => {
  test("clipBytes never splits a character and stays within the bound", () => {
    const s = "日本語のテキスト ".repeat(400);
    for (const max of [10, 101, 4000]) {
      const c = clipBytes(s, max);
      expect(bytes(c)).toBeLessThanOrEqual(max);
      expect(c).not.toContain("�");
    }
    expect(clipBytes("short", 4000)).toBe("short");
  });
  test("plainText drops scripts and markup and keeps line breaks", () => {
    expect(plainText("<p>A&amp;B</p><script>alert(1)</script><ul><li>one</li><li>two</li></ul>")).toBe("A&B\none\ntwo");
  });
  test("excerpt leaves a description that fits alone", () => {
    expect(excerpt("short role")).toEqual({ text: "short role", shortened: false });
  });
  test("excerpt starts at the start of the line the role is on, never mid-sentence", () => {
    const text = "Intro. ".repeat(300) + "\nAs a senior engineer, you will build storage.\n" + "Detail. ".repeat(600);
    expect(excerpt(text).text.startsWith("As a senior engineer, you will build storage.")).toBe(true);
  });
});

// ---------------------------------------------------------------- Any other listing

interface FakeEl {
  localName: string;
  attrs: Record<string, string>;
  textContent: string;
  clicked: number;
  onClick?: () => void;
  hasAttribute(n: string): boolean;
  getAttribute(n: string): string | null;
  click(): void;
}

function fel(localName: string, attrs: Record<string, string>, text = "", onClick?: () => void): FakeEl {
  return {
    localName, attrs, textContent: text, clicked: 0, onClick,
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

describe("crawlPages", () => {
  const page = "https://careers.acme.com/search?q=go";

  test("clicks through an in-page pager until next is disabled", async () => {
    let p = 0;
    const next = fel("button", { "aria-label": "Next page" }, "", () => { p++; if (p === 2) next.attrs.disabled = ""; });
    const doc = fakeDoc(() => [fel("button", {}, "Previous"), next]);
    const deps: PageDeps = { scrape: () => [job(p * 2), job(p * 2 + 1)], sleep: async () => {} };
    const r = await crawlPages(doc, page, deps);
    expect(r.jobs.map(j => j.title)).toEqual(["Engineer 0", "Engineer 1", "Engineer 2", "Engineer 3", "Engineer 4", "Engineer 5"]);
    expect(r.complete).toBe(true);
    expect(r.parts).toBe(3);
  });

  test("a load-more button that grows the list is followed", async () => {
    let n = 2;
    const more = fel("button", {}, "Show more jobs", () => { n += 2; if (n >= 6) more.attrs["aria-disabled"] = "true"; });
    const doc = fakeDoc(() => [more]);
    const r = await crawlPages(doc, page, { scrape: () => Array.from({ length: n }, (_, i) => job(i)), sleep: async () => {} });
    expect(r.jobs.length).toBe(6);
    expect(r.complete).toBe(true);
  });

  test("a click that changes nothing ends the walk with the reason", async () => {
    const doc = fakeDoc(() => [fel("a", { href: "#" }, "Next")]);
    const r = await crawlPages(doc, page, { scrape: () => [job(1)], sleep: async () => {} }, { pageWaitMs: 1000 });
    expect(r.complete).toBe(false);
    expect(r.notes).toEqual(["page 2 did not load within 1 s"]);
  });

  test("a rel=next link on the same listing is fetched, never navigated", async () => {
    const pages = [1, 2, 3].map(i => fakeDoc(() => i < 3 ? [fel("a", { rel: "next", href: `/search?q=go&page=${i + 1}` })] : []));
    const loaded: string[] = [];
    const deps: PageDeps = {
      scrape: d => [job(pages.indexOf(d) + 1)],
      load: async url => { loaded.push(url); return pages[loaded.length]!; },
      sleep: async () => {},
    };
    const r = await crawlPages(pages[0]!, page, deps);
    expect(loaded).toEqual(["https://careers.acme.com/search?q=go&page=2", "https://careers.acme.com/search?q=go&page=3"]);
    expect(r.jobs.length).toBe(3);
    expect(r.complete).toBe(true);
  });

  test("a next link that leaves the listing is not followed", async () => {
    for (const href of ["https://evil.example/search?page=2", "/careers/elsewhere", "javascript:alert(1)//next"]) {
      const doc = fakeDoc(() => [fel("a", { rel: "next", href })]);
      let loads = 0;
      const r = await crawlPages(doc, page, { scrape: () => [job(1)], load: async () => { loads++; return doc; }, sleep: async () => {} });
      expect(loads).toBe(0);
      expect(r.complete).toBe(false);
      expect(r.notes[0]).toContain("not followed");
    }
  });

  test("a page with nothing new ends the walk; the page bound ends an endless pager", async () => {
    const doc = fakeDoc(() => [fel("button", {}, "Next")]);
    let k = 0;
    const same = await crawlPages(doc, page, { scrape: () => [job(k++ < 1 ? 1 : 1)], sleep: async () => {} }, { pageWaitMs: 0 });
    expect(same.notes.length).toBe(1);
    let p = 0;
    const endless = fel("button", {}, "Next", () => p++);
    const r = await crawlPages(fakeDoc(() => [endless]), page, { scrape: () => [job(p)], sleep: async () => {} }, { maxPages: 5 });
    expect(r.parts).toBe(5);
    expect(r.notes).toEqual(["stopped at the limit of 5 pages"]);
  });

  test("a failed fetch of the next page is reported, with the jobs read so far kept", async () => {
    const doc = fakeDoc(() => [fel("a", { rel: "next", href: "/search?q=go&page=2" })]);
    const r = await crawlPages(doc, page, { scrape: () => [job(1)], load: async () => { throw new Error("HTTP 500"); }, sleep: async () => {} });
    expect(r.jobs.length).toBe(1);
    expect(r.notes).toEqual(["page 2 could not be loaded: HTTP 500"]);
  });
});

describe("findNext and sameListing", () => {
  test("pages of one listing", () => {
    expect(sameListing("https://a.com/jobs?q=x", "https://a.com/jobs?q=x&page=2")).toBe(true);
    expect(sameListing("https://a.com/jobs/page/1", "https://a.com/jobs/page/2")).toBe(true);
    // A bare trailing number is as often a job id as a page; only /page/N and paging parameters page.
    expect(sameListing("https://a.com/jobs", "https://a.com/jobs/2")).toBe(false);
    expect(sameListing("https://a.com/jobs?q=go", "https://a.com/jobs?q=sales&page=2")).toBe(false);
    expect(sameListing("https://a.com/jobs?q=go", "https://a.com/jobs?page=2&q=go")).toBe(true);
    expect(sameListing("https://a.com/jobs", "https://b.com/jobs")).toBe(false);
    expect(sameListing("https://a.com/jobs", "https://a.com/blog")).toBe(false);
    expect(sameListing("https://a.com/jobs", "javascript:alert(1)")).toBe(false);
  });
  test("prefers the page's own rel=next and skips disabled controls", () => {
    const doc = fakeDoc(() => [fel("button", { disabled: "" }, "Next"), fel("button", { class: "pager-next is-disabled" }, "Next"), fel("a", { "aria-label": "next" }, "")]);
    const s = findNext(doc, "https://a.com/jobs");
    expect(s.kind).toBe("click");
  });
  test("Workday's own next control", () => {
    const doc = fakeDoc(() => [fel("button", { "data-uxi-element-id": "next", "aria-label": "next" })]);
    expect(findNext(doc, "https://a.com/jobs").kind).toBe("click");
  });
  test("nothing to take is 'none' without a reason, which is the listing's end", () => {
    expect(findNext(fakeDoc(() => [fel("a", { href: "/jobs/1" }, "Engineer")]), "https://a.com/jobs")).toEqual({ kind: "none" });
  });
});

// ---------------------------------------------------------------- Sending

const ingestJob = (i: number) => ({ title: `Role ${i}`, company: "Acme", location: "", url: `https://careers.acme.com/jobs/${i}`, snippet: "x" });
const pageRef = { url: "https://careers.acme.com/search", title: "Jobs" };

describe("sendCrawl", () => {
  test("sends batches of at most 200, saving the search once", async () => {
    const sent: Array<{ saveSearch: boolean; jobs: unknown[] }> = [];
    const r = await sendCrawl({ jobs: Array.from({ length: 450 }, (_, i) => ingestJob(i)) }, pageRef, true, async m => {
      sent.push(m as { saveSearch: boolean; jobs: unknown[] });
      return { queued: (m as { jobs: unknown[] }).jobs.length, analysis: "daemon" };
    });
    expect(sent.map(s => s.jobs.length)).toEqual([200, 200, 50]);
    expect(sent.map(s => s.saveSearch)).toEqual([true, false, false]);
    expect(r.sent).toBe(450);
    expect(r.leftOut).toBe("");
  });

  test("long descriptions close a batch before the host's 1 MiB bound, counted as the host counts", async () => {
    // 200 jobs of ~4000-byte, slash-heavy descriptions: about 1.2 MB as the Mac host re-serialises it.
    const long = (i: number) => ({ ...ingestJob(i), url: `https://acme.wd5.myworkdayjobs.com/External/job/US-CA-Santa-Clara/Senior-Engineer_JR${i}`, snippet: "a/b ".repeat(999) });
    const jobs = Array.from({ length: 200 }, (_, i) => long(i));
    const batches: Array<{ jobs: unknown[]; page: unknown; saveSearch: boolean }> = [];
    const r = await sendCrawl({ jobs }, pageRef, true, async m => {
      batches.push(m as { jobs: unknown[]; page: unknown; saveSearch: boolean });
      return { queued: (m as { jobs: unknown[] }).jobs.length };
    });
    expect(batches.length).toBeGreaterThan(1);
    for (const b of batches) {
      const wire = { op: "jobsIngest", page: b.page, save_search: b.saveSearch, jobs: b.jobs };
      expect(wireBytes(wire)).toBeLessThanOrEqual(MAX_BATCH_BYTES);
    }
    expect(batches.reduce((a, b) => a + b.jobs.length, 0)).toBe(200);
    expect(batches.map(b => b.saveSearch)).toEqual([true, ...Array(batches.length - 1).fill(false)]);
    expect(r.sent).toBe(200);
  });

  test("wireBytes counts each '/' twice, as JSONSerialization escapes it", () => {
    expect(wireBytes({ u: "a/b" })).toBe(new TextEncoder().encode(JSON.stringify({ u: "a/b" })).length + 1);
    expect(wireBytes({ t: "日本" })).toBe(new TextEncoder().encode('{"t":"日本"}').length);
  });

  test("a batch Vani cannot take ends the send and says how many were not sent", async () => {
    let n = 0;
    const r = await sendCrawl({ jobs: Array.from({ length: 450 }, (_, i) => ingestJob(i)) }, pageRef, false, async () => {
      n++;
      return n === 1 ? { queued: 200 } : { error: "not reachable", code: "unreachable" };
    });
    expect(n).toBe(2);
    expect(r.sent).toBe(200);
    expect(r.leftOut).toBe("250 not sent");
    expect(r.outcomes.at(-1)!.text).toContain("The Vani app is not reachable");
  });

  test("a thrown send is a failure, never a success", async () => {
    const r = await sendCrawl({ jobs: [ingestJob(1)] }, pageRef, false, async () => { throw new Error("port closed"); });
    expect(r.sent).toBe(0);
    expect(r.outcomes[0]!.ok).toBe(false);
  });

  test("jobs that break a bound are left out and counted, the rest still go", async () => {
    const jobs = [ingestJob(1), { ...ingestJob(2), url: "javascript:alert(1)" }, { ...ingestJob(3), title: "" }];
    const seen: unknown[][] = [];
    const r = await sendCrawl({ jobs }, pageRef, false, async m => { seen.push((m as { jobs: unknown[] }).jobs); return { queued: 1 }; });
    expect(seen[0]!.length).toBe(1);
    expect(r.leftOut).toContain("2 not sent");
  });

  test("nothing to send sends nothing", async () => {
    let calls = 0;
    const r = await sendCrawl({ jobs: [] }, pageRef, true, async () => { calls++; return {}; });
    expect(calls).toBe(0);
    expect(r.batches).toBe(0);
  });
});

describe("describeCrawl", () => {
  const base: CrawlReport = { mode: "workday", jobs: [], listed: 2752, expected: 2752, requests: 0, parts: 4, shortened: 0, complete: true, notes: [] };
  test("an incomplete crawl never reads as complete", () => {
    expect(describeCrawl({ ...base, listed: 2000, complete: false, notes: ["could not be split"] })).toBe(
      "Listed 2000 of 2752 jobs in 4 queries; 0 ready with descriptions (incomplete: could not be split).");
    expect(describeCrawl({ ...base, complete: false, notes: [] })).toContain("(incomplete: stopped early)");
  });
});
