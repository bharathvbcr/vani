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
import { MAX_PAGE_JOB_LINKS, scrapePageJobs, unreadText } from "../src/scraper";

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
const card = (inner: string, href = "/careers/role-1") => `<div><a href="${href}">${inner}</a></div>`;

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
      ["12 open roles", "Search for Jobs"],
      ["Explore opportunities", "Careers"],
    ]) {
      const d = docOf(`<html><head><title>${title}</title></head><body><h1>${h1}</h1>${card("<h3>Staff Engineer</h3>")}${card("<h3>Principal Engineer</h3>", "/careers/role-2")}</body></html>`);
      const r = scrapePageJobs(d, "careers.example.test", undefined, PAGE);
      expect(r.jobs.map(j => j.company)).toEqual(["", ""]);
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
  });

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

  test("unread words: nothing for zero, negative or not a number; one link is singular", () => {
    for (const n of [0, -3, Number.NaN, Number.POSITIVE_INFINITY]) expect(unreadText(n)).toBe("");
    expect(unreadText(1)).toBe(`1 more job link on this page not read (at most ${MAX_PAGE_JOB_LINKS} are read)`);
    expect(unreadText(2.9)).toStartWith("2 more job links");
  });
});
