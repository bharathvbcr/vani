import { describe, expect, test } from "bun:test";
import { WORKDAY_PAGE, WORKDAY_WINDOW, clipBytes, crawlWorkday, excerpt, parseWorkdayBoard, plainText, type Http } from "../src/crawler";

// Adversarial tests for the Workday mode of crawler.ts. Tests named "BUG:" encode a promise the crawler
// makes (never loop, stay in bounds, never `complete` when jobs were missed, never an empty description,
// never leave the board) and fail against the current code. Tests named "pin:" hold today.

const ORIGIN = "https://acme.wd5.myworkdayjobs.com";
const LIST = `${ORIGIN}/wday/cxs/acme/External/jobs`;
const DETAIL_PREFIX = "/wday/cxs/acme/External/job/";
const hex = (n: number) => n.toString(16).padStart(32, "0");
const bytes = (s: string) => new TextEncoder().encode(s).length;
const board = parseWorkdayBoard(`${ORIGIN}/en-US/External`)!;

interface J { path: string; title: string; attrs?: Record<string, string[]> }
interface ListBody { appliedFacets: Record<string, string[]>; offset: number; limit: number; searchText: string }

interface Srv {
  jobs: J[];
  wrapAt?: number;
  /** Replace the answer's `total` (default: honest, capped at the window). `undefined` omits the field. */
  total?: (honest: number) => unknown;
  /** Edit the facets the honest answer carries. */
  facetEdit?: (facets: Array<{ facetParameter: string; values: Array<{ id: string; count: number }> }>) => unknown;
  /** Replace the page of rows (after the wrap). */
  page?: (offset: number, rows: J[]) => J[];
  /** Replace the whole listing answer. */
  respond?: (body: ListBody, n: number) => unknown;
  detail?: (j: J | undefined, url: string) => unknown;
  hook?: (url: string, init: RequestInit) => Response | Promise<Response> | undefined;
}

function server(s: Srv): Http & { calls: string[]; bodies: ListBody[] } {
  const calls: string[] = [];
  const bodies: ListBody[] = [];
  const params = [...new Set(s.jobs.flatMap(j => Object.keys(j.attrs ?? {})))];
  return {
    calls,
    bodies,
    sleep: async () => {},
    async fetch(url, init) {
      calls.push(url);
      const h = s.hook?.(url, init);
      if (h) return h;
      if (url === LIST) {
        const body = JSON.parse(String(init.body)) as ListBody;
        bodies.push(body);
        if (s.respond) return new Response(JSON.stringify(s.respond(body, bodies.length)));
        const match = s.jobs.filter(j => Object.entries(body.appliedFacets).every(([p, ids]) => (j.attrs?.[p] ?? []).some(v => ids.includes(v))));
        const at = body.offset >= (s.wrapAt ?? WORKDAY_WINDOW) ? 0 : body.offset;
        let rows = match.slice(at, at + body.limit);
        if (s.page) rows = s.page(body.offset, rows);
        const facets = params.map(p => {
          const ids = [...new Set(s.jobs.flatMap(j => j.attrs?.[p] ?? []))];
          return { facetParameter: p, values: ids.map(id => ({ id, count: match.filter(j => j.attrs?.[p]?.includes(id)).length })) };
        });
        const honest = Math.min(match.length, WORKDAY_WINDOW);
        const out: Record<string, unknown> = {
          jobPostings: rows.map(j => ({ title: j.title, externalPath: j.path, locationsText: "US", postedOn: "Today" })),
          facets: s.facetEdit ? s.facetEdit(facets) : facets,
        };
        const t = s.total ? s.total(honest) : honest;
        if (t !== undefined) out.total = t;
        return new Response(JSON.stringify(out));
      }
      const j = s.jobs.find(x => url.endsWith(x.path));
      const info = s.detail ? s.detail(j, url) : { jobPostingInfo: { title: j?.title ?? "t", jobDescription: "<p>You will build things.</p>" } };
      return new Response(JSON.stringify(info));
    },
  };
}

const mk = (n: number, attrs?: (i: number) => Record<string, string[]>): J[] =>
  Array.from({ length: n }, (_, i) => ({ path: `/job/US/Role_${i}`, title: `Engineer ${i}`, attrs: attrs?.(i) }));

const ALL = { maxDetails: 1_000_000, maxRequests: 1_000_000, delayMs: 0 };
const listCalls = (h: { calls: string[] }) => h.calls.filter(c => c === LIST).length;

// ---------------------------------------------------------------- completeness lies

describe("complete must not be claimed when the facet evidence is partial", () => {
  test("pin: a facet with more than 200 values is read whole, and one facet alone never proves the count", async () => {
    // 200 buckets of 10 plus a 201st of 5 (was: truncated at 200, 2000 of 2005 listed and "complete").
    const jobs = mk(2005, i => ({ fam: [hex(1 + Math.min(Math.floor(i / 10), 200))] }));
    const r = await crawlWorkday(board, ALL, server({ jobs }));
    expect(r.listed).toBe(2005);
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toContain("rests on one facet");
  });

  test("BUG: a facet value whose id cannot be sent back is dropped silently and lowers the expected count", async () => {
    // buckets of 1000 / 1000 / 500; the third id is not 32 hex, so it is dropped and 500 jobs are never asked for.
    const ids = [hex(1), hex(2), "tenant-specific-id"];
    const jobs = mk(2500, i => ({ fam: [ids[i < 1000 ? 0 : i < 2000 ? 1 : 2]!] }));
    const r = await crawlWorkday(board, ALL, server({ jobs }));
    expect(r.listed).toBe(2000);
    expect(r.complete).toBe(false);
  });

  test("BUG: the only facet is one some jobs lack: the split facet's own sum is taken as the board's count", async () => {
    // 2500 jobs, 500 have no `sub` at all. Nothing else reports a count.
    const jobs = mk(2500, i => (i < 2000 ? { sub: [hex(1 + (i % 2))] } : {}));
    const r = await crawlWorkday(board, ALL, server({ jobs }));
    expect(r.listed).toBe(2000);
    expect(r.complete).toBe(false);
  });

  test("pin: with a second facet that covers every job, the split uses it and lists the whole board", async () => {
    const jobs = mk(2500, i => ({ ...(i < 2000 ? { sub: [hex(1 + (i % 2))] } : {}), tt: [hex(100 + (i % 2))] }));
    const http = server({ jobs });
    const r = await crawlWorkday(board, ALL, http);
    expect(r.expected).toBe(2500);
    expect(r.listed).toBe(2500);
    // Only `tt` reaches 2500, so nothing confirms there are not more: listed whole, not provably complete.
    expect(r.complete).toBe(false);
    expect(r.notes).toEqual(["the board's count rests on one facet, so jobs that facet leaves out cannot be ruled out"]);
    expect(http.bodies.slice(1).every(b => "tt" in b.appliedFacets)).toBe(true);
  });

  test("pin: two facets that agree on the count make a whole listing complete", async () => {
    const jobs = mk(2500, i => ({ tt: [hex(100 + (i % 2))], ws: [hex(200 + (i % 3))] }));
    const r = await crawlWorkday(board, ALL, server({ jobs }));
    expect(r.listed).toBe(2500);
    expect(r.complete).toBe(true);
    expect(r.notes).toEqual([]);
  });

  test("pin: a multi-valued facet (overlapping buckets) never makes a short listing complete", async () => {
    // every job in two of three buckets: bucket sums are 2x the board, listing deduplicates, 2800 jobs real.
    const jobs = mk(2800, i => ({ fam: [hex(1 + (i % 3)), hex(1 + ((i + 1) % 3))] }));
    const r = await crawlWorkday(board, { ...ALL, maxDetails: 0 }, server({ jobs }));
    expect(r.listed).toBe(2800);
    // listed everything, but the board cannot prove it: it must not be worse than "incomplete".
    expect(r.complete).toBe(false);
  });

  test("pin: a facet whose buckets sum below the real total is passed over for the one that covers every job", async () => {
    const jobs = mk(2600, i => ({ fam: i < 2300 ? [hex(1 + (i % 2))] : [], tt: [hex(100 + (i % 2))] }));
    const http = server({ jobs });
    const r = await crawlWorkday(board, ALL, http);
    expect(r.listed).toBe(2600);
    expect(http.bodies.slice(1).every(b => "tt" in b.appliedFacets)).toBe(true);
  });

});

describe("a missing or lying total", () => {
  test("BUG: no `total` and a board that serves page 1 again: the repeat ends the walk silently and complete stays true", async () => {
    const r = await crawlWorkday(board, ALL, server({ jobs: mk(300), wrapAt: 100, total: () => undefined }));
    expect(r.listed).toBe(100);
    expect(r.complete).toBe(false);
  });

  test("BUG: no `total` and the walk ends at the window: 2000 of 3000 listed reads as complete", async () => {
    const r = await crawlWorkday(board, ALL, server({ jobs: mk(3000), total: () => undefined }));
    expect(r.listed).toBe(WORKDAY_WINDOW);
    expect(r.complete).toBe(false);
  });

  test("BUG: a row that was dropped as unreadable does not stop complete when the total cannot catch it", async () => {
    const respond = () => ({
      jobPostings: [
        { title: "A", externalPath: "/job/US/A_1" },
        { title: "B", externalPath: "/job/US/B_2" },
        { title: "C", externalPath: "https://evil.example/c" },
      ],
    });
    const r = await crawlWorkday(board, ALL, server({ jobs: [], respond }));
    expect(r.listed).toBe(2);
    expect(r.complete).toBe(false);
  });

  test("pin: totals of every wrong kind never loop or fetch more than the three real pages", async () => {
    const lies: unknown[] = [0, -5, "500", null, 1e9, 2000, 1e999 as number, [], {}, true, 2999.9];
    for (const lie of lies) {
      const http = server({ jobs: mk(45), total: () => lie });
      const r = await crawlWorkday(board, { ...ALL, maxDetails: 0 }, http);
      expect(r.listed).toBe(45);
      expect(listCalls(http)).toBeLessThanOrEqual(WORKDAY_WINDOW / WORKDAY_PAGE);
      expect(listCalls(http)).toBeLessThanOrEqual(3);
    }
  });

  test("pin: a total below what the board then serves does not cut the walk short", async () => {
    // 45 jobs, total says 25 (was: offset 40 never read, 40 listed). Paging runs to the short page.
    const r = await crawlWorkday(board, ALL, server({ jobs: mk(45), total: () => 25 }));
    expect(r.listed).toBe(45);
  });

  test("pin: a total that is too high never reads as complete", async () => {
    const r = await crawlWorkday(board, ALL, server({ jobs: mk(45), total: () => 500 }));
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toContain("of the 500 jobs");
  });
});

// ---------------------------------------------------------------- loops and request bounds

describe("loops and bounds", () => {
  test("BUG: a board that serves the same later page again (not page 1) is read through the whole window", async () => {
    const http = server({ jobs: mk(1500), page: (offset, rows) => (offset >= 40 ? mk(1500).slice(20, 40) : rows) });
    const r = await crawlWorkday(board, { ...ALL, maxDetails: 0 }, http);
    expect(r.listed).toBe(40);
    expect(listCalls(http)).toBeLessThanOrEqual(10);
  });

  test("BUG: page 1 served again in another order slips past the first-row check and burns the whole window", async () => {
    const jobs = mk(1500);
    const http = server({ jobs, wrapAt: 100, page: (offset, rows) => (offset >= 100 ? rows.slice().reverse() : rows) });
    const r = await crawlWorkday(board, { ...ALL, maxDetails: 0 }, http);
    expect(r.listed).toBe(100);
    expect(listCalls(http)).toBeLessThanOrEqual(10);
  });

  test("BUG: a facet that lists one id twice has that bucket walked twice", async () => {
    const jobs = mk(2500, i => ({ fam: [hex(1 + (i % 2))] }));
    const http = server({
      jobs,
      facetEdit: f => f.map(x => (x.facetParameter === "fam" ? { ...x, values: [...x.values, x.values[0]!] } : x)),
    });
    await crawlWorkday(board, { ...ALL, maxDetails: 0 }, http);
    const keys = http.bodies.map(b => JSON.stringify([b.appliedFacets, b.offset]));
    expect(new Set(keys).size).toBe(keys.length);
  });

  test("pin: liar facets (200 values x 3000 jobs, on every query) cannot exceed the request budget", async () => {
    const liar = (applied: Record<string, string[]>) => ["a", "b"].filter(p => !(p in applied)).map(p => ({
      facetParameter: p, values: Array.from({ length: 200 }, (_, i) => ({ id: hex(i + (p === "a" ? 1 : 1000)), count: 3000 })),
    }));
    const respond = (b: ListBody, n: number) => ({
      total: WORKDAY_WINDOW,
      jobPostings: Array.from({ length: WORKDAY_PAGE }, (_, k) => ({ title: `t${n}-${k}`, externalPath: `/job/x/${n}_${k}` })),
      facets: liar(b.appliedFacets),
    });
    const http = server({ jobs: [], respond });
    const r = await crawlWorkday(board, { maxDetails: 0, delayMs: 0 }, http);
    expect(http.calls.length).toBeLessThanOrEqual(600);
    expect(r.requests).toBeLessThanOrEqual(600);
    expect(r.complete).toBe(false);
    expect(r.listed).toBeLessThanOrEqual(10_000);
  });

  test("BUG: the same 'could not be split' note is repeated for every bucket", async () => {
    const respond = (b: ListBody, n: number) => ({
      total: WORKDAY_WINDOW,
      jobPostings: Array.from({ length: 1 }, (_, k) => ({ title: `t${n}-${k}`, externalPath: `/job/x/${n}_${k}` })),
      facets: ["a", "b"].filter(p => !(p in b.appliedFacets)).map(p => ({
        facetParameter: p, values: [{ id: hex(1), count: 3000 }, { id: hex(2), count: 3000 }, { id: hex(3), count: 3000 }],
      })),
    });
    const r = await crawlWorkday(board, { maxDetails: 0, delayMs: 0 }, server({ jobs: [], respond }));
    expect(new Set(r.notes).size).toBe(r.notes.length);
  });

  test("pin: a duplicate row across pages, or the first row legitimately repeated at a later offset, ends the walk incomplete", async () => {
    const jobs = mk(60);
    const http = server({ jobs, page: (offset, rows) => (offset === 20 ? [jobs[0]!, ...rows.slice(1)] : rows) });
    const r = await crawlWorkday(board, { ...ALL, maxDetails: 0 }, http);
    expect(r.complete).toBe(false);
    expect(r.listed).toBe(20);
    expect(listCalls(http)).toBe(2);
  });

  test("pin: a board that serves page 1 again at the window is not read past it", async () => {
    const http = server({ jobs: mk(2400, () => ({})) });
    const r = await crawlWorkday(board, { ...ALL, maxDetails: 0 }, http);
    expect(http.bodies.every(b => b.offset < WORKDAY_WINDOW)).toBe(true);
    expect(r.complete).toBe(false);
  });

  test("pin: nested depth is bounded: no request carries more than three applied facets", async () => {
    const respond = (b: ListBody, n: number) => ({
      total: WORKDAY_WINDOW,
      jobPostings: [{ title: `t${n}`, externalPath: `/job/x/${n}` }],
      facets: ["a", "b", "c", "d", "e"].filter(p => !(p in b.appliedFacets)).map(p => ({
        facetParameter: p, values: [{ id: hex(1), count: 3000 }, { id: hex(2), count: 3000 }],
      })),
    });
    const http = server({ jobs: [], respond });
    await crawlWorkday(board, { maxDetails: 0, delayMs: 0, maxRequests: 5000 }, http);
    expect(Math.max(...http.bodies.map(b => Object.keys(b.appliedFacets).length))).toBeLessThanOrEqual(3);
  });
});

// ---------------------------------------------------------------- request targets

describe("requests stay on the board's detail endpoint", () => {
  const evil = ["/job/../../../../../logout", "/job/%2e%2e/%2e%2e/admin", "/job/..\\..\\admin", "/job/a/../../../../x"];

  test("BUG: an externalPath with dot segments makes the crawler GET another path on the board's origin", async () => {
    const respond = () => ({
      total: 5,
      jobPostings: [{ title: "ok", externalPath: "/job/US/ok_1" }, ...evil.map((p, i) => ({ title: `e${i}`, externalPath: p }))],
    });
    const http = server({ jobs: [], respond });
    const r = await crawlWorkday(board, ALL, http);
    for (const c of http.calls.filter(x => x !== LIST)) {
      expect(new URL(c).origin).toBe(ORIGIN);
      expect(new URL(c).pathname.startsWith(DETAIL_PREFIX)).toBe(true);
    }
    for (const j of r.jobs) expect(new URL(j.url).pathname.startsWith("/External/job/")).toBe(true);
  });

  test("BUG: a same-origin externalUrl outside the job pages, or with credentials, is sent as the job's link", async () => {
    const urls = [`${ORIGIN}/wday/cxs/acme/External/jobs`, `${ORIGIN}/logout`, `https://user:pw@acme.wd5.myworkdayjobs.com/External/job/x`];
    const jobs = mk(3);
    const http = server({ jobs, detail: (j) => ({ jobPostingInfo: { title: j!.title, jobDescription: "<p>You will build.</p>", externalUrl: urls[Number(j!.title.split(" ")[1])] } }) });
    const r = await crawlWorkday(board, ALL, http);
    expect(r.jobs.length).toBe(3);
    for (const j of r.jobs) {
      expect(j.url.startsWith(`${ORIGIN}/External/job/`)).toBe(true);
      expect(j.url).not.toContain("@");
    }
  });

  test("pin: an off-origin or lookalike externalUrl falls back to the listing's own link", async () => {
    const urls = ["https://evil.example/x", "https://acme.wd5.myworkdayjobs.com.evil.example/x", "http://acme.wd5.myworkdayjobs.com/External/job/x", "javascript:alert(1)", "//evil.example/x"];
    const jobs = mk(urls.length);
    const http = server({ jobs, detail: (j) => ({ jobPostingInfo: { title: j!.title, jobDescription: "<p>You will build.</p>", externalUrl: urls[Number(j!.title.split(" ")[1])] } }) });
    const r = await crawlWorkday(board, ALL, http);
    expect(r.jobs.length).toBe(urls.length);
    for (const j of r.jobs) expect(j.url.startsWith(`${ORIGIN}/External/job/US/Role_`)).toBe(true);
  });

  test("pin: external-looking and oversized externalPaths are not rows", async () => {
    const rows = ["https://evil.example/job/x", "//evil.example/job/x", "/job/" + "a".repeat(1001), "/job/a b", "/job/a?x=1", "/job/a#f", "/other/job/x", "job/x"].map((p, i) => ({ title: `t${i}`, externalPath: p }));
    const http = server({ jobs: [], respond: () => ({ total: rows.length, jobPostings: rows }) });
    const r = await crawlWorkday(board, ALL, http);
    expect(r.listed).toBe(0);
    expect(http.calls.every(c => c === LIST)).toBe(true);
  });

  test("pin: every request is same-origin, credentialed same-origin only, and refuses redirects", async () => {
    const inits: RequestInit[] = [];
    const http = server({ jobs: mk(25), hook: (_u, init) => { inits.push(init); return undefined; } });
    await crawlWorkday(board, ALL, http);
    expect(http.calls.every(c => new URL(c).origin === ORIGIN)).toBe(true);
    expect(inits.every(i => i.credentials === "same-origin" && i.redirect === "error")).toBe(true);
  });
});

// ---------------------------------------------------------------- bodies, timeouts, aborts

describe("bodies, timeouts and aborts", () => {
  test("BUG: a huge body is fully buffered before its size is checked", async () => {
    let pulls = 0;
    const chunk = new Uint8Array(1 << 20).fill(0x20);
    const hook = (url: string) => {
      if (url !== LIST) return undefined;
      const stream = new ReadableStream<Uint8Array>({
        pull(c) {
          if (pulls >= 64) { c.close(); return; }
          pulls++;
          c.enqueue(chunk);
        },
      });
      return new Response(stream);
    };
    const r = await crawlWorkday(board, ALL, server({ jobs: [], hook }));
    expect(r.notes.join(" ")).toContain("too large");
    // 4 MiB is the bound; it should stop reading near it, not after 64 MiB.
    expect(pulls).toBeLessThanOrEqual(8);
  });

  test("pin: a listing that never answers is tried three times, no more, with growing pauses", async () => {
    const sleeps: number[] = [];
    const http = server({ jobs: [], hook: (_u, init) => new Promise<Response>((_, rej) => init.signal?.addEventListener("abort", () => rej(new Error("aborted")))) });
    http.sleep = async ms => { sleeps.push(ms); };
    const r = await crawlWorkday(board, { timeoutMs: 10, delayMs: 100 }, http);
    expect(http.calls.length).toBe(3);
    expect(sleeps).toEqual([400, 800]);
    expect(r.notes[0]).toContain("timed out after 10 ms");
  });

  test("pin: the user's stop during a pause makes no further request", async () => {
    const ac = new AbortController();
    const http = server({ jobs: mk(200) });
    let n = 0;
    http.sleep = async () => { if (++n === 1) ac.abort(); };
    const r = await crawlWorkday(board, { signal: ac.signal, delayMs: 1 }, http);
    expect(http.calls.length).toBe(1);
    expect(r.notes).toContain("stopped");
    expect(r.complete).toBe(false);
  });

  test("pin: the user's stop during a request ends it at once, with no retry", async () => {
    const ac = new AbortController();
    const http = server({ jobs: [], hook: (_u, init) => new Promise<Response>((_, rej) => { init.signal?.addEventListener("abort", () => rej(new Error("aborted"))); setTimeout(() => ac.abort(), 5); }) });
    const r = await crawlWorkday(board, { signal: ac.signal, timeoutMs: 5000 }, http);
    expect(http.calls.length).toBe(1);
    expect(r.notes).toEqual(["stopped"]);
  });

  test("pin: a stop before the crawl starts makes no request", async () => {
    const ac = new AbortController();
    ac.abort();
    const http = server({ jobs: mk(5) });
    const r = await crawlWorkday(board, { signal: ac.signal }, http);
    expect(http.calls.length).toBe(0);
    expect(r.complete).toBe(false);
  });

  test("pin: a stop in the middle of the descriptions keeps what was read and fetches no more", async () => {
    const ac = new AbortController();
    const http = server({ jobs: mk(10) });
    const r = await crawlWorkday(board, { signal: ac.signal, onProgress: p => { if (p.phase === "details" && p.jobs === 3) ac.abort(); } }, http);
    expect(r.jobs.length).toBe(3);
    expect(r.complete).toBe(false);
    expect(http.calls.length).toBe(1 + 3);
  });

  test("BUG: a stop after the listing finished leaves complete false with no note saying why", async () => {
    const ac = new AbortController();
    const http = server({ jobs: mk(45) });
    const r = await crawlWorkday(board, { signal: ac.signal, onProgress: p => { if (p.phase === "listing" && p.jobs >= 45) ac.abort(); } }, http);
    expect(r.jobs).toEqual([]);
    expect(r.complete).toBe(false);
    expect(r.notes.join(" ")).toMatch(/stopped/);
  });

  test("BUG: a progress callback that throws while descriptions are read discards the whole crawl", async () => {
    const http = server({ jobs: mk(5) });
    const run = crawlWorkday(board, { onProgress: p => { if (p.phase === "details") throw new Error("port closed"); } }, http);
    await expect(run).resolves.toBeDefined();
  });

  test("pin: a progress callback that throws never changes what the crawl reads", async () => {
    const r = await crawlWorkday(board, { onProgress: () => { throw new Error("port closed"); } }, server({ jobs: mk(5) }));
    expect(r.listed).toBe(5);
    expect(r.jobs.length).toBe(5);
    expect(r.complete).toBe(true);
  });
});

// ---------------------------------------------------------------- descriptions

describe("an empty description is never sent", () => {
  const sendOne = async (jobDescription: unknown) => {
    const r = await crawlWorkday(board, ALL, server({ jobs: mk(1), detail: j => ({ jobPostingInfo: { title: j!.title, jobDescription } }) }));
    return r;
  };

  test("pin: nothing, whitespace, tags, comments, scripts and &nbsp; are all empty", async () => {
    for (const d of ["", "   ", "<p></p>", "<p><br></p>", "<!-- c -->", "<script>x()</script>", "<style>p{}</style>", "<p>&nbsp;&nbsp;</p>", " ", null, 42, {}, []]) {
      const r = await sendOne(d);
      expect(r.jobs).toEqual([]);
      expect(r.complete).toBe(false);
    }
  });

  test("BUG: numeric entities and zero-width characters count as a description", async () => {
    const sent: string[] = [];
    for (const d of ["<p>&#160;</p>", "<p>&#xA0;</p>", "<p>​</p>", "<p>&ensp;&emsp;</p>", "﻿", "<p>­</p>"]) {
      if ((await sendOne(d)).jobs.length > 0) sent.push(JSON.stringify(d));
    }
    expect(sent).toEqual([]);
  });

  test("BUG: an unclosed <script> leaves its code in the description", async () => {
    const r = await sendOne("<p>Build things.</p><script>steal(document.cookie)");
    expect(r.jobs[0]!.snippet).not.toContain("steal");
  });

  test("BUG: a description that is only an unclosed <style> or <script> is sent as if it said something", async () => {
    for (const d of ["<style>p{color:red}", "<script>var a=1"]) {
      const r = await sendOne(d);
      expect({ d, sent: r.jobs.length }).toEqual({ d, sent: 0 });
    }
  });
});

describe("text helpers", () => {
  test("BUG: plainText decodes &amp; first, so an escaped entity is decoded twice", () => {
    expect(plainText("&amp;lt;b&amp;gt; and &amp;amp;")).toBe("&lt;b&gt; and &amp;");
  });

  test("pin: plainText of split-tag tricks leaves no live script tag or executable body", () => {
    for (const h of ["<scr<script>ipt>alert(1)</script>", "<SCRIPT >alert(1)</ScRiPt >", "<script\n>alert(1)</script\n>", "<<script>alert(1)</script>b>"]) {
      const t = plainText(h);
      expect(t).not.toContain("alert(1)");
    }
    expect(plainText("<p>A</p>\n\n\n<p>B</p>")).toBe("A\nB");
  });

  // A detail body may be 4 Mi characters (MAX_BODY_BYTES); these inputs are 100-150 KB. Linear time is ms.
  const slow = (name: string, html: string) =>
    test(`BUG: plainText is quadratic on ${name}`, () => {
      const t0 = performance.now();
      plainText(html);
      expect(performance.now() - t0).toBeLessThan(200);
    }, 30_000);
  slow("100k unclosed '<'", "<".repeat(100_000));
  slow("50k unclosed '<p '", "<p ".repeat(50_000));
  slow("40k unclosed <script>", "<script>".repeat(40_000));
  slow("40k unclosed <style>", "<style>".repeat(40_000));

  test("BUG: clipBytes with a bound smaller than the ellipsis exceeds the bound", () => {
    for (const max of [0, 1, 2]) expect(bytes(clipBytes("abcdefghij", max))).toBeLessThanOrEqual(max);
  });

  test("pin: clipBytes holds its bound for every width, never splits a code point, and always marks a cut", () => {
    const samples = ["日本語のテキスト ".repeat(50), "😀😀😀 mixed é ü ".repeat(60), "a".repeat(500), "x ".repeat(300), "\u{1F468}‍\u{1F469}‍\u{1F467} family ".repeat(40)];
    for (const s of samples) {
      for (let max = 3; max < 400; max++) {
        const c = clipBytes(s, max);
        expect(bytes(c)).toBeLessThanOrEqual(max);
        expect(c).not.toContain("�");
        if (bytes(s) > max) expect(c.endsWith("…")).toBe(true);
      }
    }
  });

  test("pin: excerpt keeps text that starts at the substance, and does not cut from a late match", () => {
    const body = "Responsibilities: " + "build the thing well. ".repeat(300);
    const a = excerpt(body);
    expect(a.text.startsWith("Responsibilities")).toBe(true);
    expect(bytes(a.text)).toBeLessThanOrEqual(4000);
    const late = "Acme is a company. ".repeat(300) + "requirements: go. ";
    const b = excerpt(late);
    expect(b.text.startsWith("Acme is a company")).toBe(true);
    expect(excerpt(" ".repeat(5000)).text.length).toBeLessThan(10);
  });

  test("pin: excerpt of a multibyte description stays within the byte bound", () => {
    const s = "要件: " + "分散システムを設計する。".repeat(400);
    expect(bytes(excerpt(s).text)).toBeLessThanOrEqual(4000);
  });
});

describe("locations", () => {
  test("BUG: a long additional-locations list with no primary location starts with a space", async () => {
    const detail = (j: J | undefined) => ({
      jobPostingInfo: {
        title: j!.title, jobDescription: "<p>You will build.</p>", location: "",
        additionalLocations: Array.from({ length: 30 }, (_, i) => `Some Long City Name ${i}, Region, Country`),
      },
    });
    const r = await crawlWorkday(board, ALL, server({ jobs: mk(1), detail }));
    expect(r.jobs[0]!.location).toBe(r.jobs[0]!.location.trim());
    expect(bytes(r.jobs[0]!.location)).toBeLessThanOrEqual(200);
  });
});
