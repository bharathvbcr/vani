// Workday's shared host, wd{N}.myworkdaysite.com. Unlike {tenant}.wd{N}.myworkdayjobs.com it has no tenant
// label: the tenant and site are the path's, /recruiting/{tenant}/{site}[/job/...]. Go's twins are
// cmd/vani TestDeriveBoardWorkdaySiteHost (the board a listing page derives), internal/joburl
// (WorkdayPosting) and internal/sources/workday (pagePrefix, storedPath). The scraper's cards are in
// scraper.test.ts; the real Snap board is the shape taken here (2026-10, not visited by this suite).
import { describe, expect, test } from "bun:test";
import { postingKey } from "../src/applied";
import { type Http, crawlWorkday, parseWorkdayBoard } from "../src/crawler";
import { parseFindRow } from "../src/jobrow";
import { detectPortal } from "../src/portal";
import { scrubPageUrl } from "../src/scraper";

const SHARED = "https://wd1.myworkdaysite.com";
const SNAP = `${SHARED}/recruiting/snapchat/snap`;
const SNAP_JOB = `${SNAP}/job/Los-Angeles-California/Data-Scientist--Level-4_Q426DSA4-1`;
const hex = (n: number) => n.toString(16).padStart(32, "0");

describe("parseWorkdayBoard on the shared host", () => {
  const snap = { origin: SHARED, tenant: "snapchat", site: "snap", searchText: "", facets: {} };

  test("reads the board from its listing page and from a posting page of it", () => {
    expect(parseWorkdayBoard(SNAP)).toEqual(snap);
    expect(parseWorkdayBoard(SNAP_JOB)).toEqual(snap);
  });

  test("skips the locale, an /apply tail and the host's case; keeps the search; drops the fragment", () => {
    expect(parseWorkdayBoard(`${SHARED}/en-US/recruiting/snapchat/snap?q=machine+learning#x`)).toEqual({ ...snap, searchText: "machine learning" });
    expect(parseWorkdayBoard(`${SNAP_JOB}/apply`)).toEqual(snap);
    expect(parseWorkdayBoard(`${SHARED}/fr-CA/recruiting/snapchat/snap/job/Paris-France/ML_R1-1/apply/applyManually?utm_source=x`)).toEqual(snap);
    expect(parseWorkdayBoard("https://WD1.MyWorkdaySite.COM/recruiting/snapchat/snap")).toEqual(snap);
  });

  test("keeps the applied facets of the listing page", () => {
    expect(parseWorkdayBoard(`${SNAP}?jobFamilyGroup=${hex(1)}&timeType=junk`)?.facets).toEqual({ jobFamilyGroup: [hex(1)] });
  });

  test("refuses lookalike hosts, other shapes of the host and paths that name no tenant or site", () => {
    for (const u of [
      "https://wd1.myworkdaysite.com.evil.com/recruiting/snapchat/snap",
      "https://evilmyworkdaysite.com/recruiting/snapchat/snap",
      "https://evil-wd1.myworkdaysite.com/recruiting/snapchat/snap",
      "https://snapchat.wd1.myworkdaysite.com/recruiting/snapchat/snap",
      "https://wd.myworkdaysite.com/recruiting/snapchat/snap",
      "https://myworkdaysite.com/recruiting/snapchat/snap",
      "https://evil.com/wd1.myworkdaysite.com/recruiting/snapchat/snap",
      "http://wd1.myworkdaysite.com/recruiting/snapchat/snap",
      "https://wd1.myworkdaysite.com:8443/recruiting/snapchat/snap",
      `${SHARED}/`,
      `${SHARED}/recruiting`,
      `${SHARED}/recruiting/snapchat`,
      `${SHARED}/en-US/recruiting/snapchat`,
      `${SHARED}/snap/job/x/R1`, // the myworkdayjobs shape names no tenant here
      `${SHARED}/careers/snapchat/snap`,
      `${SHARED}/recruiting/snapchat/job/x/R1`,
      `${SHARED}/recruiting/snapchat/wday`,
      `${SHARED}/recruiting/snap%2Fchat/snap`,
      `${SHARED}/recruiting/snapchat/sn%2Fap`,
      `${SHARED}/wday/cxs/snapchat/snap/jobs`,
    ]) expect([u, parseWorkdayBoard(u)]).toEqual([u, null]);
  });
});

describe("crawlWorkday on the shared host", () => {
  const snap = parseWorkdayBoard(`${SHARED}/en-US/recruiting/snapchat/snap`)!;
  const rows = [
    { title: "Data Scientist, Level 4", externalPath: "/job/Los-Angeles-California/Data-Scientist--Level-4_Q426DSA4-1", locationsText: "Los Angeles, California", postedOn: "Posted Today" },
    { title: "Machine Learning Engineer", externalPath: "/job/Paris-France/Machine-Learning-Engineer_R2-1", locationsText: "Paris, France", postedOn: "Posted Today" },
  ];
  const links = (prefix: string) => rows.map(r => `${SHARED}/${prefix}${r.externalPath}`).sort();

  function board(externalUrl: (p: string) => string): Http & { calls: string[] } {
    const calls: string[] = [];
    return {
      calls,
      sleep: async () => {},
      async fetch(url: string): Promise<Response> {
        calls.push(url);
        if (url === `${SHARED}/wday/cxs/snapchat/snap/jobs`) return new Response(JSON.stringify({ total: rows.length, jobPostings: rows, facets: [] }), { status: 200 });
        const row = rows.find(r => url === `${SHARED}/wday/cxs/snapchat/snap${r.externalPath}`);
        if (!row) return new Response("", { status: 404 });
        return new Response(JSON.stringify({ jobPostingInfo: { title: row.title, jobDescription: "<p>Build models.</p>", location: row.locationsText, externalUrl: externalUrl(row.externalPath) } }), { status: 200 });
      },
    };
  }

  test("asks the board's own API at the shared host, with the tenant and site from the path", async () => {
    const h = board(p => `${SHARED}/en-US/recruiting/snapchat/snap${p}`);
    const r = await crawlWorkday(snap, { company: "Snap" }, h);
    expect(r.complete).toBe(true);
    expect(r.listed).toBe(2);
    expect(r.jobs.length).toBe(2);
    expect(h.calls[0]).toBe(`${SHARED}/wday/cxs/snapchat/snap/jobs`);
    expect(h.calls).toContain(`${SHARED}/wday/cxs/snapchat/snap${rows[0]!.externalPath}`);
  });

  test("a posting's link is the board's own, the locale form included, without a fragment", async () => {
    const r = await crawlWorkday(snap, {}, board(p => `${SHARED}/en-US/recruiting/snapchat/snap${p}#frag`));
    expect(r.jobs.map(j => j.url).sort()).toEqual(links("en-US/recruiting/snapchat/snap"));
  });

  test("with no usable externalUrl a link is built under /recruiting/{tenant}/{site}, never /{site}", async () => {
    for (const bad of [
      () => "",
      (p: string) => `${SHARED}/recruiting/other/snap${p}`,
      (p: string) => `${SHARED}/snap${p}`,
      (p: string) => `https://evil.example/recruiting/snapchat/snap${p}`,
    ]) {
      const r = await crawlWorkday(snap, {}, board(bad));
      expect(r.jobs.map(j => j.url).sort()).toEqual(links("recruiting/snapchat/snap"));
    }
  });

  test("a link folds to the key Go folds it to (internal/joburl.WorkdayPosting)", async () => {
    const r = await crawlWorkday(snap, {}, board(() => ""));
    const urls = r.jobs.map(j => j.url);
    expect(urls).toContain(SNAP_JOB);
    expect(postingKey(SNAP_JOB)).toBe(`${SNAP}/job/Data-Scientist--Level-4_Q426DSA4-1`);
  });
});

describe("the page URL the ingest sends names the board Go derives", () => {
  test("keeps the whole path and the search of a shared-host page, and drops tracking and the fragment", () => {
    expect(scrubPageUrl(`${SHARED}/en-US/recruiting/snapchat/snap?q=machine+learning&utm_source=x#top`)).toBe(`${SHARED}/en-US/recruiting/snapchat/snap?q=machine+learning`);
    expect(scrubPageUrl(SNAP)).toBe(SNAP);
  });
});

describe("detectPortal on the shared host", () => {
  const empty = { querySelector: () => null, querySelectorAll: () => [] };
  const at = (href: string) => detectPortal(empty, href);

  test("a posting, its apply flow and its locale form are Workday and not a board", () => {
    for (const u of [SNAP_JOB, `${SNAP_JOB}/apply`, `${SNAP_JOB}/apply/applyManually`, `${SHARED}/en-US/recruiting/snapchat/snap/job/Paris-France/ML_R1-1`, `${SNAP_JOB}?source=x#frag`]) {
      const p = at(u);
      expect([u, p.id, p.isATS, p.isMultiStep, p.isJobBoard]).toEqual([u, "workday", true, true, false]);
    }
  });

  test("the site's own page is a board, with or without a locale, a search or a trailing slash", () => {
    for (const u of [SNAP, `${SNAP}/`, `${SHARED}/en-US/recruiting/snapchat/snap`, `${SNAP}?q=machine+learning`, "https://WD1.MYWORKDAYSITE.COM/recruiting/snapchat/snap"]) {
      const p = at(u);
      expect([u, p.id, p.isJobBoard]).toEqual([u, "workday", true]);
    }
  });

  test("a path that names no tenant or site, a login or a details page is not a board", () => {
    for (const u of [`${SHARED}/`, `${SHARED}/recruiting`, `${SHARED}/recruiting/snapchat`, `${SNAP}/login`, `${SNAP}/details/ML_R1-1`]) {
      const p = at(u);
      expect([u, p.id, p.isJobBoard]).toEqual([u, "workday", false]);
    }
  });

  test("rendered jobTitle anchors mark a board anywhere on the host", () => {
    const doc = { querySelector: () => null, querySelectorAll: (sel: string) => (sel.includes("jobTitle") ? [{}] : []) };
    expect(detectPortal(doc, SNAP_JOB).isJobBoard).toBe(true);
  });

  test("lookalike hosts are not Workday", () => {
    for (const u of [
      `https://wd1.myworkdaysite.com.evil.com/recruiting/snapchat/snap`,
      `https://evilmyworkdaysite.com/recruiting/snapchat/snap`,
      `https://evil-wd1.myworkdaysite.com/recruiting/snapchat/snap`,
      `https://evil.com/wd1.myworkdaysite.com/recruiting/snapchat/snap/job/x/R1`,
    ]) expect([u, at(u).id]).not.toEqual([u, "workday"]);
  });
});

describe("postingKey on the shared host", () => {
  test("every route of the Snap posting folds to one key", () => {
    const key = `${SNAP}/job/Data-Scientist--Level-4_Q426DSA4-1`;
    for (const u of [SNAP_JOB, `${SNAP_JOB}/apply`, `${SHARED}/en-US/recruiting/snapchat/snap/job/Los-Angeles-California/Data-Scientist--Level-4_Q426DSA4-1/apply/applyManually`, `${SNAP_JOB}?source=x#frag`, "https://WD1.MYWORKDAYSITE.COM/recruiting/snapchat/snap/job/X/Data-Scientist--Level-4_Q426DSA4-1"]) {
      expect([u, postingKey(u)]).toEqual([u, key]);
    }
  });
});

// jobFind's reply carries `job.row` only for a posting triage has scored; an untriaged posting is found
// (the Applied offer still works) with no row, and the page keeps its page-text estimate (content.ts
// vaniRelevance: `r.row ? applyVaniRow(estimate, r.row) : estimate`).
describe("parseFindRow on a reply with no row", () => {
  test("an untriaged posting is found and has no row", () => {
    const found = { found: true, job: { key: "wd:snapchat:snap:q426dsa4-1", company: "Snap", title: "Data Scientist, Level 4", status: "digested" } };
    expect(parseFindRow(found)).toEqual({ row: null });
    expect(parseFindRow({ ...found, job: { ...found.job, row: null } })).toEqual({ row: null });
    expect(parseFindRow({ found: true, job: { ...found.job, row: undefined } })).toEqual({ row: null });
  });

  test("a reply with no job, a non-object job or found:false is no row, and never an error", () => {
    for (const reply of [{ found: true }, { found: true, job: null }, { found: true, job: "x" }, { found: false, job: { row: {} } }, {}, null, "x", 3, []]) {
      expect([JSON.stringify(reply), parseFindRow(reply)]).toEqual([JSON.stringify(reply), { row: null }]);
    }
  });

  test("a row that is there but unreadable is an error, not the estimate passed off as Vani's", () => {
    const r = parseFindRow({ found: true, job: { key: "k", row: {} } });
    expect("error" in r).toBe(true);
  });
});
