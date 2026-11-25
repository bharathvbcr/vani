// A listing whose whole card is one <a> (title, "Featured" badge and "Office: City" row side by side)
// read by the generic scraper. The documents are parsed from real markup into a small DOM that keeps
// the DOM's own text rules (textContent joins text nodes with nothing between them), unlike the
// hand-built nodes of scraper.test.ts, whose textContent joins children with a space and so cannot
// show a title glued to its badge. Fixture: a trimmed capture of one careers page (tests/fixtures).
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { MAX_CARD_NODES, cardRest, readCard, withoutGluedLabel } from "../src/card";
import { prepareIngest } from "../src/ingest";
import { isPagerText } from "../src/crawler";
import { MAX_PAGE_JOB_LINKS, boardTotalText, namesPosting, scrapePageJobs, skippedText, unreadText } from "../src/scraper";

const ELEMENT = 1;
const TEXT = 3;

class N {
  parent: N | null = null;
  childNodes: N[] = [];
  constructor(
    readonly nodeType: number,
    readonly localName: string,
    readonly attrs: Record<string, string>,
    readonly data: string,
  ) {}
  get children(): N[] { return this.childNodes.filter(c => c.nodeType === ELEMENT); }
  get parentElement(): N | null { return this.parent; }
  get textContent(): string {
    return this.nodeType === TEXT ? this.data : this.childNodes.map(c => c.textContent).join("");
  }
  getAttribute(name: string): string | null { return this.attrs[name.toLowerCase()] ?? null; }
  querySelectorAll(sel: string): N[] {
    const out: N[] = [];
    const walk = (n: N): void => {
      for (const c of n.childNodes) {
        if (c.nodeType !== ELEMENT) continue;
        if (matches(c, sel)) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  querySelector(sel: string): N | null { return this.querySelectorAll(sel)[0] ?? null; }
  closest(sel: string): N | null {
    for (let p: N | null = this; p; p = p.parent) if (p.nodeType === ELEMENT && matches(p, sel)) return p;
    return null;
  }
}

/** Compound selectors only: tag, .class, [attr], [attr=v], [attr*=v], joined by commas. */
function matches(n: N, sel: string): boolean {
  return sel.split(",").some(part => {
    let rest = part.trim();
    const tag = /^[a-zA-Z][a-zA-Z0-9-]*/.exec(rest)?.[0];
    if (tag) {
      if (n.localName !== tag.toLowerCase()) return false;
      rest = rest.slice(tag.length);
    }
    if (!rest && !tag) return false;
    while (rest) {
      const cls = /^\.([\w-]+)/.exec(rest);
      const at = /^\[([\w-]+)(?:([*]?=)['"]?([^'"\]]*)['"]?)?\]/.exec(rest);
      if (cls) {
        if (!(n.attrs["class"] ?? "").split(/\s+/).includes(cls[1]!)) return false;
        rest = rest.slice(cls[0].length);
      } else if (at) {
        const v = n.getAttribute(at[1]!);
        if (v === null) return false;
        if (at[2] === "=" && v !== at[3]) return false;
        if (at[2] === "*=" && !v.includes(at[3]!)) return false;
        rest = rest.slice(at[0].length);
      } else {
        return false;
      }
    }
    return true;
  });
}

const VOID = new Set(["img", "br", "hr", "input", "meta", "link", "wbr", "source"]);
const ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };

function decode(s: string): string {
  return s.replace(/&(?:#x([0-9a-f]+)|#(\d+)|(\w+));/gi, (m, hex, dec, name) => {
    if (hex) return String.fromCodePoint(parseInt(hex, 16));
    if (dec) return String.fromCodePoint(parseInt(dec, 10));
    return ENTITIES[name as string] ?? m;
  });
}

function parse(html: string): N {
  const root = new N(ELEMENT, "#root", {}, "");
  const stack: N[] = [root];
  const top = (): N => stack[stack.length - 1]!;
  const add = (n: N): void => { n.parent = top(); top().childNodes.push(n); };
  const re = /<!--[\s\S]*?-->|<!doctype[^>]*>|<\/([a-zA-Z][\w-]*)\s*>|<([a-zA-Z][\w-]*)((?:"[^"]*"|'[^']*'|[^'">])*)>|([^<]+|<)/gi;
  let m: RegExpExecArray | null;
  while ((m = re.exec(html)) !== null) {
    if (m[1]) {
      const name = m[1].toLowerCase();
      const at = stack.map(s => s.localName).lastIndexOf(name);
      if (at > 0) stack.length = at;
    } else if (m[2]) {
      const name = m[2].toLowerCase();
      const attrs: Record<string, string> = {};
      for (const a of (m[3] ?? "").matchAll(/([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
        const k = a[1]!.toLowerCase();
        if (!(k in attrs)) attrs[k] = decode(a[2] ?? a[3] ?? a[4] ?? "");
      }
      const el = new N(ELEMENT, name, attrs, "");
      add(el);
      if (name === "script" || name === "style") {
        const close = html.toLowerCase().indexOf("</" + name, re.lastIndex);
        const end = close < 0 ? html.length : close;
        const body = html.slice(re.lastIndex, end);
        if (body) { const t = new N(TEXT, "#text", {}, body); t.parent = el; el.childNodes.push(t); }
        re.lastIndex = end;
      } else if (!VOID.has(name) && !(m[3] ?? "").endsWith("/")) {
        stack.push(el);
      }
    } else if (m[4]) {
      add(new N(TEXT, "#text", {}, decode(m[4])));
    }
  }
  return root;
}

function docOf(html: string): Document {
  const root = parse(html);
  const title = root.querySelector("title")?.textContent ?? "";
  return {
    title,
    querySelector: (s: string) => root.querySelector(s),
    querySelectorAll: (s: string) => root.querySelectorAll(s),
    createElement: () => ({ innerHTML: "", textContent: "" }),
  } as unknown as Document;
}

const PAGE = "https://careers.example.test/board";
const scrape = (body: string, host = "careers.example.test") =>
  scrapePageJobs(docOf(`<html><head><title>Board</title></head><body>${body}</body></html>`), host, undefined, PAGE);
/** A scrape of `body` as the page at `url` (the listing's own address matters to the link reader). */
const scrapeAt = (body: string, host: string, url: string) =>
  scrapePageJobs(docOf(`<html><head><title>Board</title></head><body>${body}</body></html>`), host, undefined, url);
const card = (inner: string, href = "/careers/role-1") =>`<div><a href="${href}">${inner}</a></div>`;

describe("a card that is one link around several elements", () => {
  const html = readFileSync(join(import.meta.dir, "fixtures", "revolutpeople-biorce-2026-10.html"), "utf8");
  const result = scrapePageJobs(docOf(html), "revolutpeople.com", undefined, "https://revolutpeople.com/biorce/public/careers");
  const byUrlTail = (tail: string) => result.jobs.find(j => j.url.endsWith(tail))!;

  test("the captured listing yields every card with its own title, company Biorce, and no badge or location text", () => {
    expect(result.totalFound).toBe(68);
    for (const j of result.jobs) {
      expect(j.title).not.toMatch(/Featured|Office\s*:/);
      expect(j.title).toBe(j.title.trim());
      expect(j.company).toBe("Biorce");
      expect(j.url).toStartWith("https://revolutpeople.com/biorce/public/careers/position/");
    }
  });

  test("titles and places match the cards (live ledger rows read 'Academy LeaderFeaturedOffice: New York')", () => {
    const academy = byUrlTail("academy-leader-01c1a712-6e3d-4c2f-a40d-f2664a77e39a");
    expect(academy.title).toBe("Academy Leader");
    expect(academy.location).toBe("New York");

    const lead = byUrlTail("lead-ai-engineer-9c8637a5-7138-4908-bb86-23a8c26b0c2f");
    expect(lead.title).toBe("Lead AI Engineer");
    expect(lead.location).toBe("Austin; Barcelona");

    // A "|" that is part of the title stays; only the location row's "|" separates places.
    const trainee = byUrlTail("trainee-biorce-graduate-rotation-program-6737a2b9-35e4-4c3c-a3a4-8e6883915ad1");
    expect(trainee.title).toBe("Trainee | Biorce Graduate Rotation Program");
    expect(trainee.location).toBe("Austin; Barcelona");

    const medical = byUrlTail("medical-writer-c68a22cc-834a-410f-84d5-7473598fbe5f");
    expect(medical.title).toBe("Medical Writer");
    expect(medical.location).toBe("Barcelona");
  });

  test("every card has a place, and none echoes its title as a description", () => {
    for (const j of result.jobs) {
      expect(j.location).not.toBe("");
      expect(j.location).not.toMatch(/\||Office|Featured/);
      expect(j.snippet).toBe("");
    }
  });

  test("the scrape is sendable as it is: titles and places reach the batch, the snippet stays empty", () => {
    const prepared = prepareIngest(result, true);
    if (!("req" in prepared)) throw new Error(prepared.error);
    expect(prepared.leftOut).toBe("");
    expect(prepared.req.jobs).toHaveLength(68);
    expect(prepared.req.jobs.every(j => j.snippet === "" && !/Featured|Office:/.test(j.title))).toBe(true);
  });
});

describe("card reading, adversarial", () => {
  test("a card whose link text is the title, with no nested markup, is read as before", () => {
    const r = scrape(`<ul><li class="item"><a href="/careers/staff-engineer">Staff Engineer</a></li></ul>`);
    expect(r.jobs.map(j => j.title)).toEqual(["Staff Engineer"]);
    expect(r.jobs[0]!.snippet).toBe("");
  });

  test("a title with inline emphasis keeps the whole title and drops the badge", () => {
    const r = scrape(card(`<span>Senior <em>AI</em> Engineer</span><span class="badge">New</span>`));
    expect(r.jobs.map(j => j.title)).toEqual(["Senior AI Engineer"]);
  });

  test("a headline element beats an earlier line of text", () => {
    const r = scrape(card(`<span>Acme Robotics</span><h3>Staff Software Engineer</h3>`));
    expect(r.jobs.map(j => j.title)).toEqual(["Staff Software Engineer"]);
  });

  test("what the card says beyond the title is the snippet; the badge and place are not in it", () => {
    const r = scrape(card(`<h3>Staff Engineer</h3><span data-variant="status">Featured</span><p>Builds distributed systems</p><div class="job-location">Remote, US</div>`));
    expect(r.jobs[0]!.title).toBe("Staff Engineer");
    expect(r.jobs[0]!.snippet).toBe("Builds distributed systems");
    expect(r.jobs[0]!.location).toBe("Remote, US");
  });

  test("a card with no title element: badge-only text yields no job", () => {
    expect(scrape(card(`<span data-variant="status">Featured</span>`)).jobs).toEqual([]);
    expect(scrape(card(`<span class="pill">Featured</span><span class="chip">New</span>`)).jobs).toEqual([]);
  });

  test("a card of a location row alone yields no job", () => {
    expect(scrape(card(`<div data-testid="location-row">Office: <div>Austin</div></div>`)).jobs).toEqual([]);
  });

  test("an empty card yields no job and does not throw", () => {
    expect(scrape(card(``)).jobs).toEqual([]);
    expect(scrape(card(`<span></span><div> </div>`)).jobs).toEqual([]);
  });

  test("duplicate links to one posting are one job", () => {
    const r = scrape(card(`<h3>Staff Engineer</h3>`, "/careers/same") + card(`<h3>Staff Engineer</h3><span class="badge">New</span>`, "/careers/same"));
    expect(r.jobs).toHaveLength(1);
  });

  test("an oversized card is read within its node budget, a title ahead of the bulk is found", () => {
    const bulk = "<div>filler text</div>".repeat(20_000);
    const r = scrape(card(`<h3>Staff Engineer</h3>${bulk}`));
    expect(r.jobs.map(j => j.title)).toEqual(["Staff Engineer"]);
    expect(r.jobs[0]!.snippet.length).toBeLessThanOrEqual(250);
    const rest = cardRest(parse(`<div><h3>Staff Engineer</h3>${bulk}</div>`).childNodes[0] as unknown as Element, "Staff Engineer");
    expect(rest.length).toBeLessThanOrEqual(2000);
  });

  test("an oversized card whose first text is past the node budget fails closed: no job, not a wrong one", () => {
    const bulk = "<div></div>".repeat(MAX_CARD_NODES + 100);
    const r = scrape(card(`${bulk}<h3>Staff Engineer</h3>`));
    expect(r.jobs).toEqual([]);
  });

  test("a title buried deeper than the depth bound is not read, and the walk does not overflow the stack", () => {
    const depth = 5_000;
    const r = scrape(card(`${"<div>".repeat(depth)}Staff Engineer${"</div>".repeat(depth)}`));
    expect(r.jobs).toEqual([]);
  });

  test("injected markup stays text: script and style content is not a title, escaped tags are literal", () => {
    const r = scrape(
      card(`<script>alert(1)</script><style>.x{}</style><h3>Staff Engineer &lt;img src=x onerror=alert(1)&gt;</h3>`),
    );
    expect(r.jobs[0]!.title).toBe("Staff Engineer <img src=x onerror=alert(1)>");
    expect(r.jobs[0]!.snippet).not.toContain("alert(1)</");
    const onlyScript = scrape(card(`<script>Staff Engineer</script>`));
    expect(onlyScript.jobs).toEqual([]);
  });

  test("a javascript: link is never a job, whatever its card says", () => {
    expect(scrape(card(`<h3>Staff Engineer</h3>`, "javascript:alert(1)")).jobs).toEqual([]);
  });

  test("a location over the ingest bound is dropped, not sent to be refused with its job", () => {
    const long = "Somewhere ".repeat(40);
    const r = scrape(card(`<h3>Staff Engineer</h3><div class="job-location">${long}</div>`));
    expect(r.jobs[0]!.location).toBe("");
    const prepared = prepareIngest(r, false);
    expect("req" in prepared).toBe(true);
  });

  test("text glued behind a title in one text node is cut at the label; a spaced label is left alone", () => {
    expect(withoutGluedLabel("Lead AI EngineerOffice: Austin|Barcelona")).toBe("Lead AI Engineer");
    expect(withoutGluedLabel("AI Orchestration Architect - AustinFeaturedOffice: Austin")).toBe("AI Orchestration Architect - Austin");
    expect(withoutGluedLabel("Front Office: Manager")).toBe("Front Office: Manager");
    expect(withoutGluedLabel("Office: Manager")).toBe("Office: Manager");
    expect(withoutGluedLabel("")).toBe("");
    const r = scrape(`<div><a href="/careers/glued">Lead AI EngineerOffice: Austin|Barcelona</a></div>`);
    expect(r.jobs.map(j => j.title)).toEqual(["Lead AI Engineer"]);
  });

  test("readCard on a bare element: no children, no throw", () => {
    const a = parse(`<a href="/careers/x"></a>`).childNodes[0] as unknown as Element;
    expect(readCard(a)).toEqual({ title: "", location: "" });
  });

  test("generic page headings and titles are never returned as the company name", () => {
    for (const [h1, title] of [
      ["We have 68 open positions", "Careers home"],
      ["Join our team", "Back to Careers"],
      ["Join our team!", "Careers Portal"],
      ["68 jobs available", "Current Opportunities"],
      ["12 open roles.", "Search for Jobs"],
      ["Explore opportunities", "Careers."],
    ]) {
      const d = docOf(`<html><head><title>${title}</title></head><body><h1>${h1}</h1>${card("<h3>Staff Engineer</h3>")}${card("<h3>Principal Engineer</h3>", "/careers/role-2")}</body></html>`);
      const r = scrapePageJobs(d, "careers.example.test", undefined, PAGE);
      expect(r.jobs.map(j => j.company)).toEqual(["", ""]);
    }
  });

  test("generic ATS and board labels in og:site_name are rejected and fall through to real company names", () => {
    for (const badSite of [
      "Greenhouse",
      "Lever",
      "Ashby",
      "Workday",
      "SmartRecruiters",
      "iCIMS",
      "Careers",
      "Jobs",
      "Job Board",
      "Careers Portal",
      "Join our team!",
      "68 jobs available",
      "Current Opportunities",
    ]) {
      const dNoCompany = docOf(
        `<html><head><meta property="og:site_name" content="${badSite}"><title>Careers Portal</title></head><body><h1>68 jobs available</h1>${card("<h3>Staff Engineer</h3>")}${card("<h3>Principal Engineer</h3>", "/careers/role-2")}</body></html>`,
      );
      expect(scrapePageJobs(dNoCompany, "careers.example.test", undefined, PAGE).jobs.map(j => j.company)).toEqual(["", ""]);

      const dWithTitleCompany = docOf(
        `<html><head><meta property="og:site_name" content="${badSite}"><title>Acme Robotics | Careers</title></head><body><h1>Join our team!</h1>${card("<h3>Staff Engineer</h3>")}${card("<h3>Principal Engineer</h3>", "/careers/role-2")}</body></html>`,
      );
      expect(scrapePageJobs(dWithTitleCompany, "careers.example.test", undefined, PAGE).jobs.map(j => j.company)).toEqual(["Acme Robotics", "Acme Robotics"]);
    }
  });

  test("separator-based doc.title forms extract company while rejecting generic/ATS segments", () => {
    for (const [title, want] of [
      ["Acme | Careers", "Acme"],
      ["Acme - Jobs", "Acme"],
      ["Careers | Acme", "Acme"],
      ["Jobs - Acme", "Acme"],
      ["Rolls-Royce | Careers", "Rolls-Royce"],
      ["Acme | Careers - Greenhouse", "Acme"],
      ["Careers | Greenhouse", ""],
      ["Jobs - Workday", ""],
      ["Careers Portal | Lever", ""],
      ["68 jobs available | Ashby", ""],
      ["Jobs at Greenhouse", ""],
      ["Greenhouse Jobs", ""],
    ] as const) {
      const d = docOf(`<html><head><title>${title}</title></head><body><h1>Current Opportunities</h1>${card("<h3>Staff Engineer</h3>")}${card("<h3>Principal Engineer</h3>", "/careers/role-2")}</body></html>`);
      expect(scrapePageJobs(d, "careers.example.test", undefined, PAGE).jobs.map(j => j.company)).toEqual([want, want]);
    }
  });
});

// Anthropic's careers page (www.anthropic.com/careers/jobs) lists every opening as one card link to
// its Greenhouse posting, all on one page, with no JSON-LD and no ATS markup, so the generic scraper
// reads it. It used to stop at the first 100 links in page order and say nothing (669 on the page).
const ROLES = ["Research Engineer", "Software Engineer", "Product Manager", "Recruiting Coordinator"];
const listingCard = (i: number, href = `https://job-boards.greenhouse.io/anthropic/jobs/${4_000_000_000 + i}`) =>
  `<a href="${href}" class="_8DFgsW_jobItem"><span class="title">${ROLES[i % ROLES.length]}, Team ${i}</span><span class="location">San Francisco, CA</span></a>`;
const listingPage = (cards: string[]) => `<div class="jobs">${cards.join("")}</div>`;

describe("a one-page listing with many job links", () => {
  test("a page of 669 job cards is read whole", () => {
    const r = scrape(listingPage(Array.from({ length: 669 }, (_, i) => listingCard(i))), "www.anthropic.com");
    expect(r.jobs).toHaveLength(669);
    expect(new Set(r.jobs.map(j => j.url)).size).toBe(669);
    // ~7 s on its own: the test DOM walks a subtree per query, so a loaded machine ran it past Bun's 5 s default.
  }, 30_000);

  test("a posting linked twice is one job, and the second link does not use up the bound", () => {
    const cards = Array.from({ length: 150 }, (_, i) => listingCard(i) + listingCard(i));
    const r = scrape(listingPage(cards), "www.anthropic.com");
    expect(r.jobs).toHaveLength(150);
    expect(r.unread).toBe(0);
  });

  test("past the bound, the rest are counted as unread, never dropped in silence; repeats past it count once", () => {
    const extra = 5;
    // Plain text links, each in its own <p>: this test's small DOM walks a whole subtree for every
    // querySelector, so cards sharing one big wrapper would time the test DOM, not the bound.
    const link = (i: number) => `<p><a href="https://job-boards.greenhouse.io/anthropic/jobs/${4_000_000_000 + i}">${ROLES[i % ROLES.length]}, Team ${i}</a></p>`;
    const cards = Array.from({ length: MAX_PAGE_JOB_LINKS + extra }, (_, i) => link(i));
    // The last unread posting linked again: still 5 unread, not 6.
    cards.push(link(MAX_PAGE_JOB_LINKS + extra - 1));
    const r = scrape(cards.join(""), "www.anthropic.com");
    expect(r.jobs).toHaveLength(MAX_PAGE_JOB_LINKS);
    expect(r.unread).toBe(extra);
    // The first links in page order are the ones read.
    expect(r.jobs.some(j => j.url.endsWith(`/jobs/${4_000_000_000 + MAX_PAGE_JOB_LINKS}`))).toBe(false);
    expect(unreadText(r.unread)).toBe(`5 more job links on this page not read (at most ${MAX_PAGE_JOB_LINKS} are read)`);
  });

  test("an empty page, a page of one job, and a page read from structured data leave nothing unread", () => {
    expect(scrape("").unread).toBe(0);
    expect(scrape(listingPage([listingCard(1)])).unread).toBe(0);
    const ld = `<script type="application/ld+json">{"@type":"JobPosting","title":"Staff Engineer","hiringOrganization":{"name":"Acme"},"url":"https://careers.example.test/jobs/1"}</script>`;
    expect(scrape(ld + listingPage(Array.from({ length: 3 }, (_, i) => listingCard(i)))).unread).toBe(0);
  });

  test("structured data for a few featured roles does not hide the page's other job cards", () => {
    // A careers page that marks up 3 featured roles as JSON-LD and lists 300 roles as cards.
    const posting = (i: number) => ({ "@type": "JobPosting", title: `Featured Engineer ${i}`, hiringOrganization: { name: "Acme" }, url: `https://careers.example.test/featured/${i}` });
    const ld = `<script type="application/ld+json">${JSON.stringify({ "@type": "ItemList", itemListElement: [0, 1, 2].map(posting) })}</script>`;
    const r = scrape(ld + Array.from({ length: 300 }, (_, i) => `<p>${listingCard(i)}</p>`).join(""), "careers.example.test");
    expect(r.jobs.filter(j => j.url.includes("/featured/"))).toHaveLength(3);
    expect(r.jobs.filter(j => j.url.includes("/anthropic/jobs/"))).toHaveLength(300);
  });

  test("a JSON-LD list of 300 postings is read whole", () => {
    const posting = (i: number) => ({ "@type": "JobPosting", title: `Engineer ${i}`, hiringOrganization: { name: "Acme" }, url: `https://careers.example.test/jobs/${i}` });
    const ld = `<script type="application/ld+json">${JSON.stringify({ "@type": "ItemList", itemListElement: Array.from({ length: 300 }, (_, i) => posting(i)) })}</script>`;
    const r = scrape(ld, "careers.example.test");
    expect(r.jobs).toHaveLength(300);
    expect(r.unread).toBe(0);
  });

  test("a paged Greenhouse board says how many jobs it lists beyond this page, and its paging is not unread", () => {
    // job-boards.greenhouse.io/anthropic, as saved: jobPosts {count 50, page 1, total 669, total_pages 14}.
    const post = (i: number) => ({ title: `Engineer ${i}`, location: "San Francisco, CA", absolute_url: `https://job-boards.greenhouse.io/anthropic/jobs/${4_000_000_000 + i}`, department: { name: "Engineering" } });
    const state = (total: unknown) => ({ state: { loaderData: { root: { boardConfiguration: { name: "Anthropic" } }, "routes/$url_token": { jobPosts: { count: 50, page: 1, total, total_pages: 14, data: Array.from({ length: 50 }, (_, i) => post(i)) } } } } });
    const page = (total: unknown) => `<script>window.__remixContext = ${JSON.stringify(state(total))};</script>`;
    const r = scrape(page(669), "job-boards.greenhouse.io");
    expect(r.jobs).toHaveLength(50);
    expect(r.boardTotal).toBe(669);
    expect(r.unread).toBe(0);
    expect(boardTotalText(r.boardTotal, r.jobs.length)).toBe("This board lists 669 jobs and this page shows 50; Crawl all pages reads the rest");
    // A total that is not a whole count, or past belief, is not carried.
    for (const bad of ["669", -1, 3.5, Number.MAX_SAFE_INTEGER, null]) expect(scrape(page(bad), "job-boards.greenhouse.io").boardTotal).toBeUndefined();
  });

  test("navigation links on a search page are not jobs: Citi's saved page, 15 postings and 11 site links", () => {
    // jobs.citi.com/search-jobs as saved 2026-11-12: postings at /job/{city}/{slug}/287/{id}; the rest
    // are site navigation, a talent-network form on another host, and the pager's Previous link.
    const posting = (i: number) => `<li><a href="/job/irving/senior-engineer-${i}/287/1032218337${10 + i}"><h2>Senior Engineer ${i}</h2><span class="job-location">Irving, Texas</span></a></li>`;
    const nav = [
      `<a href="/saved-jobs">Saved Jobs</a>`,
      `<a href="/early-career-programs">Early Career Overview</a>`,
      `<a href="/early-career-programs-pre-internships">Pre-Internships</a>`,
      `<a href="/early-career-programs-internships">Internships</a>`,
      `<a href="/early-career-programs-full-time">Full-Time Programs</a>`,
      `<a href="/early-career-programs-apprenticeships">Apprenticeships</a>`,
      `<a href="https://citi.eightfold.ai/careers/join?jtn_form_id=jtn-early-career">Early Career Talent Network (opens in new window)</a>`,
      `<a href="https://citi.eightfold.ai/careers/join?jtn_form_id=jtn-general">Career Professionals Talent Network (opens in new window)</a>`,
      `<a href="https://citi.eightfold.ai/careers">Get Matched (opens in new window)</a>`,
      `<a href="/search-jobs&p=1">Previous</a>`,
      `<a href="/early-careers">Learn About Early Careers</a>`,
    ].map(a => `<p>${a}</p>`);
    const r = scrapeAt(nav.slice(0, 6).join("") + `<ul>${Array.from({ length: 15 }, (_, i) => posting(i)).join("")}</ul>` + nav.slice(6).join(""), "jobs.citi.com", "https://jobs.citi.com/search-jobs");
    expect(r.jobs.map(j => j.title).sort()).toEqual(Array.from({ length: 15 }, (_, i) => `Senior Engineer ${i}`).sort());
    expect(r.skipped).toBe(10); // "Previous" is a pager control, not counted as a skipped link
    expect(skippedText(r.skipped)).toBe("10 links that name no posting left out as site navigation");
    expect(skippedText(1)).toBe("1 link that names no posting left out as site navigation");
    for (const n of [undefined, 0, -2, 1.5]) expect(skippedText(n)).toBe("");
  });

  test("a section's skip link and Show more jobs are not jobs: ServiceNow's SmartRecruiters page", () => {
    const posting = (i: number) => `<li><a href="https://jobs.smartrecruiters.com/ServiceNow/7440001609878${10 + i}-staff-engineer-${i}"><h4>Staff Engineer ${i}</h4></a></li>`;
    const body = `<a href="https://careers.smartrecruiters.com/ServiceNow#st-footer">Skip openings section</a><ul>${Array.from({ length: 5 }, (_, i) => posting(i)).join("")}</ul>` +
      `<a href="https://careers.smartrecruiters.com/ServiceNow?search=&page=0&location=Amsterdam%2C%20NL">Show more jobs</a>`;
    const r = scrapeAt(body, "careers.smartrecruiters.com", "https://careers.smartrecruiters.com/ServiceNow");
    expect(r.jobs.map(j => j.title).sort()).toEqual(["Staff Engineer 0", "Staff Engineer 1", "Staff Engineer 2", "Staff Engineer 3", "Staff Engineer 4"]);
  });

  test("controls inside an ATS section are not jobs either: ServiceNow's .opening sections as served", () => {
    // careers.smartrecruiters.com marks its sections "opening", which the .opening reader takes whole.
    const posting = (i: number) => `<li class="opening-job"><a href="https://jobs.smartrecruiters.com/ServiceNow/7440001609878${10 + i}-staff-engineer-${i}"><h4>Staff Engineer ${i}</h4></a></li>`;
    const body = `<section class="openings-section opening opening--grouped"><a href="#st-footer" class="skipping-link">Skip openings section</a><ul>${Array.from({ length: 5 }, (_, i) => posting(i)).join("")}` +
      `<li><a href="?search=&amp;page=0&amp;location=Amsterdam%2C%20NL" class="link details-desc js-more">Show more jobs</a></li></ul></section>`;
    const r = scrapeAt(body, "careers.smartrecruiters.com", "https://careers.smartrecruiters.com/ServiceNow");
    expect(r.jobs.map(j => j.title).sort()).toEqual(["Staff Engineer 0", "Staff Engineer 1", "Staff Engineer 2", "Staff Engineer 3", "Staff Engineer 4"]);
  });

  test("a single posting's page keeps its posting at the page's own address", () => {
    const ld = `<script type="application/ld+json">${JSON.stringify({ "@type": "JobPosting", title: "Staff Engineer", description: "You will build things.", hiringOrganization: { name: "Acme" }, url: "https://careers.example.test/careers/staff-engineer" })}</script>`;
    const r = scrapeAt(ld, "careers.example.test", "https://careers.example.test/careers/staff-engineer");
    expect(r.jobs.map(j => j.title)).toEqual(["Staff Engineer"]);
  });

  test("a narrow search with fewer postings than site links keeps the site links: nothing is dropped on a guess", () => {
    // Two postings and five unnumbered site links: no clear majority of numbered links, so all are kept.
    const posting = (i: number) => `<p><a href="/job/irving/engineer-${i}/287/10322183${i}0">Engineer ${i}</a></p>`;
    const nav = ["saved-jobs", "early-careers", "careers/students", "careers/veterans", "job-alerts"].map(p => `<p><a href="/${p}">${p.replace(/[-/]/g, " ")} page</a></p>`);
    const r = scrapeAt(nav.join("") + posting(1) + posting(2), "jobs.citi.com", "https://jobs.citi.com/search-jobs?q=engineer");
    expect(r.jobs).toHaveLength(7);
    expect(r.skipped).toBeUndefined();
  });

  test("a board whose postings carry no number keeps every link; short numeric ids and gh_jid count as ids", () => {
    const slug = (s: string) => `<p><a href="/careers/${s}">${s.replace(/-/g, " ")}</a></p>`;
    const slugOnly = scrapeAt(["staff-engineer", "product-designer", "data-scientist", "sales-lead"].map(slug).join(""), "careers.example.test", PAGE);
    expect(slugOnly.jobs).toHaveLength(4);
    // /jobs/57 is an id; the one unnumbered link is the odd one out among four numbered postings.
    const short = scrapeAt([57, 58, 112, 9].map(n => `<p><a href="/jobs/${n}">Software Engineer ${n}</a></p>`).join("") + `<p><a href="/careers">Back to Careers</a></p>`, "careers.example.test", PAGE);
    expect(short.jobs.map(j => j.url).sort()).toEqual([9, 112, 57, 58].map(n => `https://careers.example.test/jobs/${n}`).sort());
    expect(short.skipped).toBe(1);
    // A Greenhouse embed names the job in the query on the listing's own path.
    const embed = scrapeAt([11, 12, 13].map(n => `<p><a href="/careers?gh_jid=40000000${n}">Machine Learning Engineer ${n}</a></p>`).join(""), "careers.example.test", "https://careers.example.test/careers");
    expect(embed.jobs).toHaveLength(3);
  });

  test("what names a posting, and what is a pager control", () => {
    for (const u of [
      "https://jobs.citi.com/job/irving/senior-engineer/287/103221833776",
      "https://jobs.smartrecruiters.com/ServiceNow/744000160987819-staff-engineer",
      "https://job-boards.greenhouse.io/anthropic/jobs/4980436008",
      "https://jobs.lever.co/shieldai/a2627727-1714-46fd-8724-3557bc17c696",
      "https://acme.wd5.myworkdayjobs.com/External/job/Austin/Engineer_JR12345",
      "https://careers.example.test/jobs/57",
      "https://careers.example.test/careers?gh_jid=4000000011",
    ]) expect(namesPosting(u)).toBe(true);
    for (const u of [
      "https://jobs.citi.com/saved-jobs",
      "https://jobs.citi.com/early-career-programs-internships",
      "https://citi.eightfold.ai/careers/join?jtn_form_id=jtn-general",
      "https://www.anthropic.com/careers",
      "not a url",
    ]) expect(namesPosting(u)).toBe(false);
    // A year in a slug is a four-digit run, so it reads as named: the link is kept, the safe direction.
    expect(namesPosting("https://careers.example.test/careers/early-careers-2026")).toBe(true);
    for (const t of ["Next", "Next page", "Previous", "‹ Prev", "« Previous", "Show more jobs", "Load more", "›", "→"]) expect(isPagerText(t)).toBe(true);
    for (const t of ["Next Gen Platform Engineer", "Previous Experience Required Analyst", "Show more about Acme", "Staff Engineer", ""]) expect(isPagerText(t)).toBe(false);
  });

  test("a TalentBrew search says how many results it has: Citi's search-results section", () => {
    const section = (attrs: string) => `<section id="search-results" ${attrs}><ul>${Array.from({ length: 3 }, (_, i) => `<li><a href="/job/irving/engineer-${i}/287/1032218337${i}0"><h2>Engineer ${i}</h2></a></li>`).join("")}</ul></section>`;
    const r = scrapeAt(section(`data-total-results="3116" data-total-job-results="3116" data-total-pages="208" data-records-per-page="15"`), "jobs.citi.com", "https://jobs.citi.com/search-jobs");
    expect(r.boardTotal).toBe(3116);
    expect(boardTotalText(r.boardTotal, r.jobs.length)).toBe("This board lists 3116 jobs and this page shows 3; Crawl all pages reads the rest");
    // data-total-results stands in when the job count is absent; a count that is not a whole number is ignored.
    expect(scrapeAt(section(`data-total-results="40"`), "jobs.citi.com", "https://jobs.citi.com/search-jobs").boardTotal).toBe(40);
    for (const bad of [`data-total-job-results="many"`, `data-total-job-results="-3"`, `data-total-job-results="1e9"`, ``]) {
      expect(scrapeAt(section(bad), "jobs.citi.com", "https://jobs.citi.com/search-jobs").boardTotal).toBeUndefined();
    }
  });

  test("board total words: nothing when the board shows everything or gives no count", () => {
    expect(boardTotalText(undefined, 50)).toBe("");
    expect(boardTotalText(50, 50)).toBe("");
    expect(boardTotalText(40, 50)).toBe("");
    expect(boardTotalText(51.5, 50)).toBe("");
  });

  test("unread words: nothing for zero, negative or not a number; one link is singular", () => {
    for (const n of [0, -3, Number.NaN, Number.POSITIVE_INFINITY]) expect(unreadText(n)).toBe("");
    expect(unreadText(1)).toBe(`1 more job link on this page not read (at most ${MAX_PAGE_JOB_LINKS} are read)`);
    expect(unreadText(2.9)).toStartWith("2 more job links");
  });
});
