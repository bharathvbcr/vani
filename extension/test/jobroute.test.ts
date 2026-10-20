// readJobInfo on single-page ATS boards (Workday): the JSON-LD and Open Graph tags in
// <head> describe the URL the document was loaded at, and the board's in-app navigation
// does not update them. Shapes taken from utaustin.wd1.myworkdayjobs.com, 2026-10-19.
import { describe, expect, test } from "bun:test";
import { isReadableJob, postingKey, readJobInfo, routeChanged } from "../src/jobinfo";
import { ratedFrame } from "../src/home";

// A small fake DOM: selector lists of compound selectors (tag, .class, #id, [a], [a="v"],
// [a*='v' i]) joined by the descendant combinator, which is all readJobInfo asks.
interface FEl {
  tag: string;
  attrs: Record<string, string>;
  children: FEl[];
  parent: FEl | null;
  html: string;
  readonly textContent: string;
  readonly innerHTML: string;
  getAttribute(n: string): string | null;
  querySelector(sel: string): FEl | null;
  querySelectorAll(sel: string): FEl[];
}

const strip = (html: string) => html.replace(/<[^>]*>/g, "").replace(/&amp;/g, "&");

function el(tag: string, attrs: Record<string, string> = {}, html = "", children: FEl[] = []): FEl {
  const me: FEl = {
    tag, attrs, children, parent: null, html,
    get textContent() { return html ? strip(html) : children.map(c => c.textContent).join(""); },
    get innerHTML() { return html; },
    getAttribute: n => attrs[n] ?? null,
    querySelector: sel => all(me, sel)[0] ?? null,
    querySelectorAll: sel => all(me, sel),
  };
  for (const c of children) c.parent = me;
  return me;
}

function compound(node: FEl, s: string): boolean {
  let rest = s;
  const tag = rest.match(/^[a-z][a-z0-9]*/i);
  if (tag) { if (node.tag !== tag[0].toLowerCase()) return false; rest = rest.slice(tag[0].length); }
  while (rest) {
    let m: RegExpMatchArray | null;
    if ((m = rest.match(/^\.([\w-]+)/))) {
      if (!(node.attrs.class ?? "").split(/\s+/).includes(m[1]!)) return false;
    } else if ((m = rest.match(/^#([\w-]+)/))) {
      if (node.attrs.id !== m[1]) return false;
    } else if ((m = rest.match(/^\[([\w:-]+)(?:(\*?=)(["'])(.*?)\3(\s+i)?)?\]/))) {
      const v = node.attrs[m[1]!];
      if (v === undefined) return false;
      if (m[2]) {
        const want = m[5] ? m[4]!.toLowerCase() : m[4]!, have = m[5] ? v.toLowerCase() : v;
        if (m[2] === "=" ? have !== want : !have.includes(want)) return false;
      }
    } else throw new Error(`fake DOM cannot parse selector ${s}`);
    rest = rest.slice(m[0].length);
  }
  return true;
}

function matches(node: FEl, sel: string): boolean {
  return sel.split(",").some(alt => {
    const parts = alt.trim().split(/\s+/);
    if (!compound(node, parts.at(-1)!)) return false;
    let i = parts.length - 2;
    for (let p = node.parent; p && i >= 0; p = p.parent) if (compound(p, parts[i]!)) i--;
    return i < 0;
  });
}

function all(root: FEl, sel: string): FEl[] {
  const out: FEl[] = [];
  const walk = (n: FEl) => { for (const c of n.children) { if (matches(c, sel)) out.push(c); walk(c); } };
  walk(root);
  return out;
}

function doc(title: string, head: FEl[], body: FEl[]): Document {
  const root = el("html", {}, "", [el("head", {}, "", head), el("body", {}, "", body)]);
  return {
    title,
    createElement: () => {
      let h = "";
      return { set innerHTML(v: string) { h = v; }, get textContent() { return strip(h); } };
    },
    querySelector: (s: string) => root.querySelector(s),
    querySelectorAll: (s: string) => root.querySelectorAll(s),
  } as unknown as Document;
}

const ld = (o: object) => el("script", { type: "application/ld+json" }, JSON.stringify(o));
const posting = (title: string, description: string) => ld({
  "@context": "http://schema.org", "@type": "JobPosting", title, description,
  hiringOrganization: { "@type": "Organization", name: "The University of Texas at Austin" },
  identifier: { "@type": "PropertyValue", name: title, value: "R_00049483" },
});
const og = (p: string, content: string) => el("meta", { property: p, content });

const FDE_DESC = "Job Posting Title: Forward Deployed Engineer ---- Hiring Department: ET - AI Platforms and Innovation. Build Python and TypeScript services on AWS.";
const AI_DESC_HTML = '<p><b>Job Posting Title:</b></p>AI Engineer<p><b>----</b></p><p><b>Hiring Department:</b></p>Dell Medical School. Ship machine learning systems in Python.';
const BOARD_BLURB = "Start Here, Change the World At The University of Texas at Austin, tradition meets innovation, and every member of our staff helps.";
const DIRECT = "https://utaustin.wd1.myworkdayjobs.com/UTstaff/job/AUSTIN-TX/Forward-Deployed-Engineer_R_00049483";
const SIMILAR = "https://utaustin.wd1.myworkdayjobs.com/en-US/UTstaff/job/AUSTIN-TX/AI-Engineer_R_00049413";
const SEARCH = "https://utaustin.wd1.myworkdayjobs.com/UTstaff?q=Forward%20Deployed%20Engineer";
const DETAILS = "https://utaustin.wd1.myworkdayjobs.com/en-US/UTstaff/details/Forward-Deployed-Engineer_R_00049483?q=Forward+Deployed+Engineer";

const rendered = (title: string, descHtml: string) => [
  el("div", { "data-automation-id": "jobPostingPage" }, "", [
    el("h2", { "data-automation-id": "jobPostingHeader" }, title),
    el("div", { "data-automation-id": "jobPostingDescription" }, descHtml),
  ]),
];
const fdeHead = () => [posting("Forward Deployed Engineer", FDE_DESC), og("og:title", "Forward Deployed Engineer"), og("og:description", FDE_DESC)];

describe("readJobInfo on Workday", () => {
  test("direct load: the JSON-LD posting agrees with the rendered header and is read", () => {
    const d = doc("Forward Deployed Engineer", fdeHead(), rendered("Forward Deployed Engineer", `<p>${FDE_DESC}</p>`));
    const j = readJobInfo(d, { href: DIRECT, loadedHref: DIRECT });
    expect(j.title).toBe("Forward Deployed Engineer");
    expect(j.company).toBe("The University of Texas at Austin");
    expect(j.description).toContain("ET - AI Platforms");
    expect(isReadableJob(j)).toBe(true);
  });

  test("in-app navigation to a similar job: the stale JSON-LD and og tags of the first job are not read", () => {
    const d = doc("AI Engineer", fdeHead(), rendered("AI Engineer", AI_DESC_HTML));
    const j = readJobInfo(d, { href: SIMILAR, loadedHref: DIRECT });
    expect(j.title).toBe("AI Engineer");
    expect(j.description).toContain("Dell Medical School");
    expect(j.description).not.toContain("Forward Deployed");
    // The rendered markup's block boundaries become separators, so words do not run together.
    expect(j.description).not.toContain("Title:AI");
    expect(isReadableJob(j)).toBe(true);
  });

  test("a rendered header that disagrees with the JSON-LD wins even without a known load URL", () => {
    const d = doc("AI Engineer", fdeHead(), rendered("AI Engineer", AI_DESC_HTML));
    const j = readJobInfo(d);
    expect(j.title).toBe("AI Engineer");
    expect(j.description).not.toContain("Forward Deployed");
  });

  test("opened from search results: the board's og tags are not read as the posting", () => {
    const head = [og("og:title", "The University of Texas at Austin Staff Jobs"), og("og:description", BOARD_BLURB), el("meta", { name: "description", content: BOARD_BLURB })];
    const d = doc("", head, rendered("Forward Deployed Engineer", `<p>${FDE_DESC}</p>`));
    const j = readJobInfo(d, { href: DETAILS, loadedHref: SEARCH });
    expect(j.title).toBe("Forward Deployed Engineer");
    expect(j.description).toContain("ET - AI Platforms");
    expect(j.description).not.toContain("Start Here");
  });

  test("half-rendered: the URL changed but the new posting is not in the page yet, so nothing is read", () => {
    const d = doc("The University of Texas at Austin Staff Jobs", fdeHead(), [el("div", { "data-automation-id": "jobPostingPage" })]);
    const j = readJobInfo(d, { href: SIMILAR, loadedHref: DIRECT });
    expect(j.title).toBe("");
    expect(j.description).toBe("");
    expect(isReadableJob(j)).toBe(false);
  });

  test("the URL changed but the old posting is still on screen: its id is in the loaded URL and not this one, so nothing is read", () => {
    // Live: ~150 ms after clicking a similar job, location is the new job's while header, body and tab title are still the old one's.
    const d = doc("Forward Deployed Engineer", fdeHead(), rendered("Forward Deployed Engineer", `<p>${FDE_DESC}</p>`));
    const j = readJobInfo(d, { href: SIMILAR, loadedHref: DIRECT });
    expect(j.title).toBe("");
    expect(j.description).toBe("");
    expect(isReadableJob(j)).toBe(false);
  });

  test("after navigating back to the loaded posting by another route, its JSON-LD is read again", () => {
    const d = doc("Forward Deployed Engineer", fdeHead(), rendered("Forward Deployed Engineer", `<p>${FDE_DESC}</p>`));
    const j = readJobInfo(d, { href: DIRECT.replace("/UTstaff/", "/en-US/UTstaff/"), loadedHref: DIRECT });
    expect(j.title).toBe("Forward Deployed Engineer");
    expect(j.company).toBe("The University of Texas at Austin");
  });

  test("two postings with one title: after navigating to the second, the second is read, not the first's head", () => {
    const first = "Research Associate in the Department of Neuroscience: run assays and maintain lab records for the first lab.";
    const second = "Research Associate in the Department of Chemistry: synthesize compounds and maintain instruments for the second lab.";
    const head = [ld({ "@type": "JobPosting", title: "Research Associate", description: first, hiringOrganization: { name: "UT" }, identifier: { value: "R_00011111" } }), og("og:title", "Research Associate"), og("og:description", first)];
    const loaded = "https://x.wd1.myworkdayjobs.com/S/job/A/Research-Associate_R_00011111";
    const now = "https://x.wd1.myworkdayjobs.com/en-US/S/job/A/Research-Associate_R_00022222";
    const j = readJobInfo(doc("Research Associate", head, rendered("Research Associate", `<p>${second}</p>`)), { href: now, loadedHref: loaded, portal: "workday" });
    expect(j.title).toBe("Research Associate");
    expect(j.description).toContain("Department of Chemistry");
    expect(j.description).not.toContain("Neuroscience");
    expect(isReadableJob(j)).toBe(true);
    // Without an id in the JSON-LD, the first posting's head is not trusted either.
    const noId = [ld({ "@type": "JobPosting", title: "Research Associate", description: first, hiringOrganization: { name: "UT" } }), og("og:title", "Research Associate"), og("og:description", first)];
    const k = readJobInfo(doc("Research Associate", noId, rendered("Research Associate", `<p>${second}</p>`)), { href: now, loadedHref: loaded, portal: "workday" });
    expect(k.description).toContain("Department of Chemistry");
    expect(k.description).not.toContain("Neuroscience");
    expect(k.company).toBe("");
  });

  test("a Workday board landing page loaded directly is not a posting (its og tags are the board's)", () => {
    const head = [og("og:title", "The University of Texas at Austin Staff Jobs"), og("og:description", BOARD_BLURB), el("meta", { name: "description", content: BOARD_BLURB })];
    const j = readJobInfo(doc("The University of Texas at Austin Staff Jobs", head, []), { href: SEARCH, loadedHref: SEARCH, portal: "workday" });
    expect(isReadableJob(j)).toBe(false);
    expect(j.title).toBe("");
    expect(j.company).toBe("");
  });

  test("a logo whose alt text is just 'Logo' is not a company name", () => {
    const d = doc("", [], [el("div", { class: "logo" }, "", [el("img", { alt: "Logo" })]), ...rendered("AI Engineer", AI_DESC_HTML)]);
    expect(readJobInfo(d).company).toBe("");
  });
});

describe("readJobInfo edges found by the break-it pass", () => {
  const loaded = "https://x.wd1.myworkdayjobs.com/S/job/A/Engineer_R1000";
  const now = "https://x.wd1.myworkdayjobs.com/S/job/A/Engineer_R10001";
  const same = "Builds data pipelines for the research computing group, with on-call duties and a shared roadmap.";
  // Live markup (utaustin, 2026-10-19): the label and the value are a <dt>/<dd> pair, adjacent in textContent.
  const reqId = (id: string) => el("div", { "data-automation-id": "requisitionId" }, "", [el("dl", {}, "", [el("dt", {}, "job requisition id"), el("dd", {}, id)])]);

  test("two postings with one title and one text: the requisition id on screen says which is shown", () => {
    const head = [ld({ "@type": "JobPosting", title: "Software Engineer", description: same, identifier: { value: "R_00011111" } })];
    const at = (id: string) => [el("div", {}, "", [el("h2", { "data-automation-id": "jobPostingHeader" }, "Software Engineer"), reqId(id), el("div", { "data-automation-id": "jobPostingDescription" }, `<p>${same}</p>`)])];
    const ctx = { href: "https://x.wd1.myworkdayjobs.com/S/job/A/Software-Engineer_R_00022222", loadedHref: "https://x.wd1.myworkdayjobs.com/S/job/A/Software-Engineer_R_00011111", portal: "workday" };
    expect(isReadableJob(readJobInfo(doc("Software Engineer", head, at("R_00011111")), ctx))).toBe(false); // the old one, still on screen
    const j = readJobInfo(doc("Software Engineer", head, at("R_00022222")), ctx); // the new one, same words
    expect(j.title).toBe("Software Engineer");
    expect(isReadableJob(j)).toBe(true);
  });

  test("a posting id is matched as a whole token: R1000 is not in .../R10001", () => {
    const head = [ld({ "@type": "JobPosting", title: "Engineer", description: "Old posting text that belongs to the first requisition only.", identifier: { value: "R1000" }, hiringOrganization: { name: "AcmeA" } })];
    const j = readJobInfo(doc("Careers", head, []), { href: now, loadedHref: loaded });
    expect(j.company).toBe("");
    expect(j.title).toBe("");
  });

  test("an id in both URLs vouches for the JSON-LD only while no rendered header names another title", () => {
    const head = [ld({ "@type": "JobPosting", title: "Engineer", description: "Old posting text that belongs to the first page only, not this one.", identifier: { value: "ORG42" } })];
    const ctx = { href: "https://jobs.example.com/ORG42/b", loadedHref: "https://jobs.example.com/ORG42/a" };
    const other = [el("h2", { "data-automation-id": "jobPostingHeader" }, "Designer")];
    expect(readJobInfo(doc("Careers", head, other), ctx).description).not.toContain("Old posting");
  });

  test("Workday apply steps: the id stays in every URL, the step's tab title is not the job's, and the posting still reads", () => {
    // Live: /apply/applyManually is a full load with the posting's JSON-LD and a jobTitleHeading; later steps may be in-app routes.
    const loadedApply = "https://utaustin.wd1.myworkdayjobs.com/en-US/UTstaff/job/AUSTIN%2C-TX/Forward-Deployed-Engineer_R_00049483/apply/applyManually";
    const step = loadedApply.replace("applyManually", "myInformation");
    const withHeading = [el("h2", { "data-automation-id": "jobTitleHeading" }, "Forward Deployed Engineer")];
    for (const body of [withHeading, []]) {
      const j = readJobInfo(doc("My Information", fdeHead(), body), { href: step, loadedHref: loadedApply, portal: "workday" });
      expect(j.title).toBe("Forward Deployed Engineer");
      expect(j.company).toBe("The University of Texas at Austin");
      expect(isReadableJob(j)).toBe(true);
    }
    // A full load of the apply page itself (not navigated) reads it too.
    expect(isReadableJob(readJobInfo(doc("Sign In", fdeHead(), withHeading), { href: loadedApply, loadedHref: loadedApply, portal: "workday" }))).toBe(true);
  });

  test("a one-word stale title does not agree with a longer tab title that contains it", () => {
    const head = [ld({ "@type": "JobPosting", title: "Engineer", description: "Old posting text that belongs to the first page only, not this one." })];
    expect(readJobInfo(doc("Senior Software Engineer | Acme", head, []), { href: "https://jobs.example.com/b", loadedHref: "https://jobs.example.com/a" }).title).toBe("");
    expect(readJobInfo(doc("Engineer | Acme", head, []), { href: "https://jobs.example.com/b", loadedHref: "https://jobs.example.com/a" }).title).toBe("Engineer");
  });

  test("titles that differ only by + or # do not agree; an entity-encoded title agrees with its rendering", () => {
    const stale = [ld({ "@type": "JobPosting", title: "C++ Developer", description: "Systems work in C++ on the trading engine, low latency.", hiringOrganization: { name: "StaleCo" } })];
    const j = readJobInfo(doc("C# Developer", stale, rendered("C# Developer", "<p>Services in C# and .NET for the billing platform team.</p>")));
    expect(j.company).toBe("");
    expect(j.description).toContain("billing platform");
    const enc = [ld({ "@type": "JobPosting", title: "R&amp;D Engineer", description: "Research and development of sensors.", hiringOrganization: { name: "RightCo" } })];
    expect(readJobInfo(doc("R&D Engineer", enc, rendered("R&D Engineer", "<p>Research and development of sensors and firmware.</p>"))).company).toBe("RightCo");
  });

  test("script, style and noscript text never enters a description; block boundaries separate words", () => {
    const html = '<div>Title:</div><div>AI Engineer</div><table><tr><td>Department</td><td>Dell Medical</td></tr></table><script>evil()</script><style>.x{color:red}</style><noscript>ENABLE JAVASCRIPT</noscript><p>Ship models.</p>';
    const j = readJobInfo(doc("AI Engineer", [], rendered("AI Engineer", html)));
    expect(j.description).not.toContain("evil");
    expect(j.description).not.toContain("color:red");
    expect(j.description).not.toContain("ENABLE JAVASCRIPT");
    expect(j.description).not.toContain("Title:AI");
    expect(j.description).not.toContain("DepartmentDell");
  });

  test("og-only posting after an in-app hop to /apply: a tab title naming it vouches; one that is only the og title does not", () => {
    const head = [og("og:title", "Research Fellow"), og("og:description", "London, UK")];
    const body = [el("div", { class: "job__description" }, "Work on interpretability research with a small team, publishing results openly.")];
    const ctx = { href: "https://job-boards.greenhouse.io/acme/jobs/123/apply", loadedHref: "https://job-boards.greenhouse.io/acme/jobs/123" };
    const j = readJobInfo(doc("Job Application for Research Fellow at Acme", head, body), ctx);
    expect(j.title).toBe("Research Fellow");
    expect(isReadableJob(j)).toBe(true);
    const board = [og("og:title", "Acme Staff Jobs"), og("og:description", BOARD_BLURB)];
    expect(readJobInfo(doc("Acme Staff Jobs", board, []), ctx).title).toBe("");
  });

  test("a #content wrapper holding a form does not replace a JSON-LD description", () => {
    const head = [ld({ "@type": "JobPosting", title: "Staff Engineer", description: "<p>Lead the storage team's roadmap and reviews.</p>" })];
    const form = el("div", { id: "content" }, "Staff Engineer First Name Last Name Email Phone Resume Cover Letter LinkedIn Profile Website ".repeat(5));
    expect(readJobInfo(doc("Staff Engineer", head, [form])).description).toContain("storage team");
  });
});

describe("readJobInfo bounds what it reads", () => {
  test("unclosed <script> tags in page content cost one pass, not quadratic time (was 23 s at 800 KB)", () => {
    const t = performance.now();
    const j = readJobInfo(doc("Engineer", [], rendered("Engineer", `<p>Real duties of the role here.</p>${"<script>".repeat(100_000)}`)));
    expect(performance.now() - t).toBeLessThan(500);
    expect(j.description).toContain("Real duties");
    expect(j.description).not.toContain("script");
  });

  test("a huge description body is capped", () => {
    const d = doc("Engineer", [og("og:title", "Engineer")], [el("div", { id: "content" }, "word ".repeat(400_000))]);
    expect(readJobInfo(d).description.length).toBeLessThanOrEqual(200_000);
  });
});

describe("readJobInfo elsewhere is unchanged", () => {
  test("a page without a rendered header still reads its JSON-LD when it was not navigated", () => {
    const d = doc("Careers", [posting("Staff Go Engineer", "<p>Building resilient distributed systems in Go.</p>")], []);
    const j = readJobInfo(d, { href: "https://jobs.example.com/a", loadedHref: "https://jobs.example.com/a" });
    expect(j.title).toBe("Staff Go Engineer");
    expect(j.description).toContain("distributed systems");
  });

  test("navigated, no rendered header: JSON-LD is read only when document.title names the same posting", () => {
    const head = [posting("Staff Go Engineer", "<p>Building resilient distributed systems in Go.</p>")];
    const same = readJobInfo(doc("Staff Go Engineer | Example", head, []), { href: "https://jobs.example.com/b", loadedHref: "https://jobs.example.com/a" });
    expect(same.title).toBe("Staff Go Engineer");
    const other = readJobInfo(doc("Product Designer | Example", head, []), { href: "https://jobs.example.com/b", loadedHref: "https://jobs.example.com/a" });
    expect(other.title).toBe("");
    expect(other.description).toBe("");
  });
});

describe("routeChanged", () => {
  test("compares origin and path, not the query or hash", () => {
    expect(routeChanged(DIRECT, DIRECT)).toBe(false);
    expect(routeChanged(`${DIRECT}?source=x#apply`, DIRECT)).toBe(false);
    expect(routeChanged(`${DIRECT}/`, DIRECT)).toBe(false);
    expect(routeChanged(SIMILAR, DIRECT)).toBe(true);
    expect(routeChanged(DETAILS, SEARCH)).toBe(true);
  });
  test("an unknown load URL is not a change; an unparseable one is (metadata is then not trusted)", () => {
    expect(routeChanged(DIRECT, undefined)).toBe(false);
    expect(routeChanged(DIRECT, "")).toBe(false);
    expect(routeChanged(DIRECT, "not a url")).toBe(true);
    expect(routeChanged("not a url", DIRECT)).toBe(true);
  });
  test("a frame that never moved (about:srcdoc, about:blank) is not a change", () => {
    expect(routeChanged("about:srcdoc", "about:srcdoc")).toBe(false);
    expect(routeChanged("about:blank", "about:blank")).toBe(false);
  });
  test("a job-id query parameter is part of the route; tracking parameters are not", () => {
    expect(routeChanged("https://acme.example/careers?gh_jid=2", "https://acme.example/careers?gh_jid=1")).toBe(true);
    expect(routeChanged("https://acme.example/careers?gh_jid=1&utm_source=x", "https://acme.example/careers?gh_jid=1")).toBe(false);
  });
});

describe("isReadableJob", () => {
  test("needs a title and a description long enough to be a posting", () => {
    expect(isReadableJob({ title: "", company: "", description: "x".repeat(400) })).toBe(false);
    expect(isReadableJob({ title: "Engineer", company: "", description: "short" })).toBe(false);
    expect(isReadableJob({ title: "  ", company: "", description: "x".repeat(400) })).toBe(false);
    expect(isReadableJob({ title: "Engineer", company: "", description: "x".repeat(40) })).toBe(true);
  });
});

describe("ratedFrame (popup)", () => {
  const rel = (score: number) => ({ score });
  test("job and rating come from one frame: the top frame's when it rated a posting", () => {
    const answered = [
      { frameId: 7, reply: { relevance: rel(90), job: { title: "Stray iframe" } } },
      { frameId: 0, reply: { relevance: rel(0), job: { title: "The posting" } } },
    ];
    expect(ratedFrame(answered)?.reply.job.title).toBe("The posting");
  });
  test("an unrated top frame (mid-navigation) yields to a frame that rated one, and none rated means none", () => {
    const answered = [
      { frameId: 0, reply: { relevance: undefined, job: { title: "" } } },
      { frameId: 3, reply: { relevance: rel(40), job: { title: "Embedded posting" } } },
    ];
    expect(ratedFrame(answered)?.frameId).toBe(3);
    expect(ratedFrame([{ frameId: 0, reply: { relevance: undefined } }])).toBeUndefined();
    expect(ratedFrame([])).toBeUndefined();
  });
});

describe("postingKey", () => {
  test("changes when the rendered posting arrives, so a half-rendered read is never reused", () => {
    const before = doc("The University of Texas at Austin Staff Jobs", fdeHead(), [el("div", { "data-automation-id": "jobPostingPage" })]);
    const after = doc("AI Engineer", fdeHead(), rendered("AI Engineer", AI_DESC_HTML));
    expect(postingKey(before, SIMILAR)).not.toBe(postingKey(after, SIMILAR));
  });
  test("og tags changing in place change the key", () => {
    const a = doc("Same", [og("og:title", "One"), og("og:description", "short")], []);
    const b = doc("Same", [og("og:title", "Two"), og("og:description", "a longer description")], []);
    expect(postingKey(a, SIMILAR)).not.toBe(postingKey(b, SIMILAR));
  });
  test("two bodies of equal length do not share a key", () => {
    const a = doc("Same", [], rendered("Same", "a".repeat(300)));
    const b = doc("Same", [], rendered("Same", "b".repeat(300)));
    expect(postingKey(a, SIMILAR)).not.toBe(postingKey(b, SIMILAR));
  });
  test("changes with the URL and is stable for the same page", () => {
    const d = doc("AI Engineer", fdeHead(), rendered("AI Engineer", AI_DESC_HTML));
    expect(postingKey(d, SIMILAR)).toBe(postingKey(d, SIMILAR));
    expect(postingKey(d, SIMILAR)).not.toBe(postingKey(d, DIRECT));
  });
});
