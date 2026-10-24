import { describe, expect, test } from "bun:test";
import { evaluateRelevance } from "../src/relevance";
import { prepareIngest } from "../src/ingest";
import { type ScrapedJob, exportJobsAsCSV, exportJobsAsJSON, scrapePageJobs } from "../src/scraper";

interface MEl {
  tag: string;
  localName: string;
  attrs: Record<string, string>;
  classes: string[];
  textContent: string;
  children: MEl[];
  parent: MEl | null;
  getAttribute(name: string): string | null;
  querySelector(sel: string): MEl | null;
  querySelectorAll(sel: string): MEl[];
  closest(sel: string): MEl | null;
  parentElement: MEl | null;
}

function el(tag: string, attrs: Record<string, string> = {}, rawText = "", children: MEl[] = []): MEl {
  const classes = (attrs["class"] || "").split(/\s+/).filter(Boolean);
  const me: MEl = {
    tag: tag.toLowerCase(),
    localName: tag.toLowerCase(),
    attrs,
    classes,
    get textContent() {
      return (rawText || this.children.map(c => c.textContent).join(" ")).trim();
    },
    children,
    parent: null,
    get parentElement() { return this.parent; },
    getAttribute(n: string) { return attrs[n.toLowerCase()] ?? null; },
    querySelector(sel: string) {
      for (const c of this.querySelectorAll(sel)) return c;
      return null;
    },
    querySelectorAll(sel: string) {
      const res: MEl[] = [];
      function walk(node: MEl) {
        for (const child of node.children) {
          if (matches(child, sel)) res.push(child);
          walk(child);
        }
      }
      walk(this);
      return res;
    },
    closest(sel: string) {
      let p: MEl | null = this;
      while (p) {
        if (matches(p, sel)) return p;
        p = p.parent;
      }
      return null;
    },
  };
  for (const c of children) c.parent = me;
  return me;
}

function matches(node: MEl, sel: string): boolean {
  for (const part of sel.split(",")) {
    const s = part.trim();
    if (!s) continue;
    let ok = true;
    let rest = s;
    const tagMatch = rest.match(/^[a-zA-Z0-9_-]+/);
    if (tagMatch) {
      if (node.tag !== tagMatch[0].toLowerCase()) continue;
      rest = rest.slice(tagMatch[0].length);
    }
    if (!tagMatch && !rest.startsWith(".") && !rest.startsWith("[")) {
      continue;
    }
    while (rest.length > 0 && ok) {
      if (rest.startsWith(".")) {
        const clsMatch = rest.match(/^\.([a-zA-Z0-9_-]+)/);
        if (clsMatch) {
          if (!node.classes.includes(clsMatch[1])) ok = false;
          rest = rest.slice(clsMatch[0].length);
        } else {
          ok = false;
        }
      } else if (rest.startsWith("[")) {
        const attrMatch = rest.match(/^\[([a-zA-Z0-9_-]+)([*~|^$]?=)?['"]?([^'"]*)['"]?\]/);
        if (attrMatch) {
          const attrVal = node.getAttribute(attrMatch[1]);
          if (attrVal === null) {
            ok = false;
          } else if (attrMatch[2] === "*=") {
            if (!attrVal.includes(attrMatch[3])) ok = false;
          } else if (attrMatch[2] === "=") {
            if (attrVal !== attrMatch[3]) ok = false;
          }
          rest = rest.slice(attrMatch[0].length);
        } else {
          ok = false;
        }
      } else {
        ok = false;
      }
    }
    if (ok) return true;
  }
  return false;
}

function makeDoc(children: MEl[], title = "Careers"): Document {
  const root = el("html", {}, "", [
    el("head", {}, "", [el("title", {}, title)]),
    el("body", {}, "", children),
  ]);
  return {
    title,
    // htmlToText reads a JSON-LD description through an inert template element.
    createElement: () => {
      let html = "";
      return {
        get innerHTML() { return html; },
        set innerHTML(v: string) { html = v; },
        get textContent() { return html.replace(/<[^>]+>/g, " "); },
      };
    },
    querySelector: (sel: string) => root.querySelector(sel),
    querySelectorAll: (sel: string) => root.querySelectorAll(sel),
  } as unknown as Document;
}

describe("job scraper & live relevance checks", () => {
  test("scrapes structured JSON-LD JobPosting data", () => {
    const jsonLd = JSON.stringify({
      "@context": "https://schema.org",
      "@type": "JobPosting",
      title: "Senior Backend Engineer - Go & Kubernetes",
      description: "Building scalable distributed cloud services using Go, Rust, and Kubernetes on GCP.",
      hiringOrganization: {
        "@type": "Organization",
        name: "Acme Corp",
      },
      jobLocation: {
        "@type": "Place",
        name: "Remote - US",
      },
      baseSalary: "$160,000 - $190,000 per year",
      url: "https://acme.com/jobs/101",
    });

    const doc = makeDoc([
      el("script", { type: "application/ld+json" }, jsonLd),
    ], "Acme Jobs");

    const result = scrapePageJobs(doc, "acme.com");
    expect(result.totalFound).toBe(1);
    const job = result.jobs[0]!;
    expect(job.title).toBe("Senior Backend Engineer - Go & Kubernetes");
    expect(job.company).toBe("Acme Corp");
    expect(job.location).toBe("Remote - US");
    expect(job.salary).toContain("160,000");
    expect(job.relevance.score).toBeGreaterThanOrEqual(75);
    expect(job.relevance.matchedSkills).toContain("go");
    expect(job.relevance.matchedSkills).toContain("kubernetes");
  });

  test("scrapes Greenhouse ATS board listings", () => {
    const doc = makeDoc([
      el("h1", {}, "Greenhouse Careers"),
      el("div", { class: "opening" }, "", [
        el("a", { href: "/acme/jobs/501" }, "Distributed Systems Engineer"),
        el("span", { class: "location" }, "Remote - Global"),
      ]),
      el("div", { class: "opening" }, "", [
        el("a", { href: "/acme/jobs/502" }, "Junior Marketing Specialist"),
        el("span", { class: "location" }, "New York, NY"),
      ]),
    ], "Acme Greenhouse");

    const result = scrapePageJobs(doc, "boards.greenhouse.io");
    expect(result.totalFound).toBe(2);
    // Highest relevance score should be first
    expect(result.jobs[0]!.title).toBe("Distributed Systems Engineer");
    expect(result.jobs[0]!.relevance.score).toBeGreaterThan(result.jobs[1]!.relevance.score);
  });

  test("scrapes Lever ATS board listings", () => {
    const doc = makeDoc([
      el("h1", {}, "Lever Careers"),
      el("div", { class: "posting" }, "", [
        el("a", { class: "posting-title", href: "https://jobs.lever.co/company/abc" }, "", [
          el("h5", {}, "Staff Software Engineer"),
        ]),
        el("div", { class: "posting-categories" }, "", [
          el("span", { class: "workplaceTypes" }, "Remote"),
        ]),
      ]),
    ], "Lever Jobs");

    const result = scrapePageJobs(doc, "jobs.lever.co");
    expect(result.totalFound).toBe(1);
    expect(result.jobs[0]!.title).toBe("Staff Software Engineer");
    expect(result.jobs[0]!.location).toBe("Remote");
  });

  test("scrapes Ashby ATS board listings", () => {
    const doc = makeDoc([
      el("h1", {}, "Ashby Careers"),
      el("div", { class: "ashby-job-posting" }, "", [
        el("a", { href: "/ashby/jobs/xyz" }, "", [
          el("h3", {}, "Platform & Cloud Engineer"),
        ]),
        el("span", { class: "location" }, "Remote"),
      ]),
    ], "Ashby Jobs");

    const result = scrapePageJobs(doc, "jobs.ashbyhq.com");
    expect(result.totalFound).toBe(1);
    expect(result.jobs[0]!.title).toBe("Platform & Cloud Engineer");
  });

  test("scrapes generic career page links with rankings", () => {
    const doc = makeDoc([
      el("h1", {}, "Company Openings"),
      el("div", { class: "job-card" }, "Remote $150k - $180k per year", [
        el("a", { href: "/careers/lead-golang-engineer" }, "Lead Golang Engineer"),
      ]),
      el("div", { class: "job-card" }, "On-site office role", [
        el("a", { href: "/careers/office-manager" }, "Office Administrative Manager"),
      ]),
    ], "Open Positions");

    const result = scrapePageJobs(doc, "mycompany.io");
    expect(result.totalFound).toBeGreaterThanOrEqual(2);
    // Golang Engineer should rank higher than Office Manager
    expect(result.jobs[0]!.title).toBe("Lead Golang Engineer");
    expect(result.jobs[0]!.relevance.score).toBeGreaterThan(result.jobs[1]!.relevance.score);
  });

  test("exportJobsAsCSV and exportJobsAsJSON produce valid export formats", () => {
    const mockJobs: ScrapedJob[] = [
      {
        id: "job-1",
        title: "Senior Backend Engineer",
        company: "Acme, Inc.",
        location: "Remote",
        url: "https://example.com/jobs/1",
        salary: "$150k - $180k",
        snippet: "Go, Kubernetes",
        relevance: {
          source: "estimate",
          score: 88,
          fit: "strong_fit",
          fitLabel: "Strong Match",
          matchedSkills: ["go", "kubernetes"],
          missingSkills: [],
          dealbreakers: [],
          highlights: [],
        },
      },
    ];

    const csv = exportJobsAsCSV(mockJobs);
    expect(csv).toContain("Title,Company,Score,Fit,Location,Salary,URL");
    expect(csv).toContain('"Senior Backend Engineer"');
    expect(csv).toContain('"Acme, Inc."');
    expect(csv).toContain("88% estimate");
    const vani = exportJobsAsCSV([{
      ...mockJobs[0]!,
      relevance: { ...mockJobs[0]!.relevance, source: "vani", score: 61 },
    }]);
    expect(vani).toContain("61% relevance");
    expect(vani).not.toContain("61% estimate");

    const jsonStr = exportJobsAsJSON(mockJobs);
    const parsed = JSON.parse(jsonStr);
    expect(Array.isArray(parsed)).toBe(true);
    expect(parsed[0].title).toBe("Senior Backend Engineer");
    expect(parsed[0].relevance.score).toBe(88);
  });

  test("scrapes Workday career listings using data-automation-id", () => {
    const doc = makeDoc([
      el("div", { "data-automation-id": "jobResults" }, "", [
        el("li", { "data-automation-id": "compositeHeader" }, "", [
          el("a", { "data-automation-id": "jobTitle", href: "/en-US/careers/job/R-12345" }, "Staff Systems Engineer"),
          el("span", { "data-automation-id": "locations" }, "Remote - USA"),
          el("span", { "data-automation-id": "postedOn" }, "Posted 2 Days Ago"),
        ]),
      ]),
    ], "Workday Careers");

    const result = scrapePageJobs(doc, "company.wd1.myworkdayjobs.com");
    expect(result.totalFound).toBe(1);
    expect(result.jobs[0]!.title).toBe("Staff Systems Engineer");
    expect(result.jobs[0]!.location).toBe("Remote - USA");
    expect(result.jobs[0]!.relevance.score).toBeGreaterThanOrEqual(55);
  });

  test("scrapes generic career links extracting nested heading when anchor text is Apply", () => {
    const doc = makeDoc([
      el("div", { class: "job-item" }, "", [
        el("h3", {}, "Cloud Infrastructure Architect"),
        el("p", {}, "Remote position building distributed systems with Go & Kubernetes."),
        el("a", { href: "/apply/role-99" }, "Apply Now"),
      ]),
    ], "Career Board");

    const result = scrapePageJobs(doc, "careers.techcorp.com");
    expect(result.totalFound).toBe(1);
    expect(result.jobs[0]!.title).toBe("Cloud Infrastructure Architect");
    expect(result.jobs[0]!.relevance.matchedSkills).toContain("go");
    expect(result.jobs[0]!.relevance.matchedSkills).toContain("kubernetes");
  });

  test("scrapes modern Greenhouse job-boards.greenhouse.io listings (Anthropic layout)", () => {
    const doc = makeDoc([
      el("meta", { property: "og:title", content: "Anthropic" }),
      el("h1", { class: "page-header" }, "Open Roles"),
      el("div", { class: "job-posts" }, "", [
        el("div", { class: "job-posts--table--department" }, "", [
          el("h3", { class: "section-header" }, "AI Research & Engineering"),
          el("div", { class: "job-posts--table" }, "", [
            el("table", {}, "", [
              el("tbody", {}, "", [
                el("tr", { class: "job-post" }, "", [
                  el("td", { class: "cell" }, "", [
                    el("a", { href: "https://job-boards.greenhouse.io/anthropic/jobs/5183044008" }, "", [
                      el("p", { class: "body body--medium" }, "Anthropic Fellows Program, AI Safety & Security"),
                      el("p", { class: "body body__secondary body--metadata" }, "London, UK; Ontario, CAN; Remote-Friendly, United States; San Francisco, CA"),
                    ]),
                  ]),
                ]),
                el("tr", { class: "job-post" }, "", [
                  el("td", { class: "cell" }, "", [
                    el("a", { href: "https://job-boards.greenhouse.io/anthropic/jobs/5183053008" }, "", [
                      el("p", { class: "body body--medium" }, "Anthropic Fellows Program, The Anthropic Institute (Economics & Policy)"),
                      el("p", { class: "body body__secondary body--metadata" }, "San Francisco, CA; Remote-Friendly, United States"),
                    ]),
                  ]),
                ]),
              ]),
            ]),
          ]),
        ]),
      ]),
    ], "Jobs at Anthropic");

    const result = scrapePageJobs(doc, "job-boards.greenhouse.io");
    expect(result.totalFound).toBe(2);
    // Company should be Anthropic, not "Open Roles"
    expect(result.jobs[0]!.company).toBe("Anthropic");
    // Title should be clean, not merged with location
    expect(result.jobs[0]!.title).toBe("Anthropic Fellows Program, AI Safety & Security");
    expect(result.jobs[0]!.location).toContain("London, UK");
    expect(result.jobs[0]!.url).toBe("https://job-boards.greenhouse.io/anthropic/jobs/5183044008");
    // Second job contains "Policy" in title and must not be rejected by non-job keywords
    const policyJob = result.jobs.find(j => j.title.includes("Economics & Policy"));
    expect(policyJob).toBeDefined();
    expect(policyJob!.title).toBe("Anthropic Fellows Program, The Anthropic Institute (Economics & Policy)");
    expect(policyJob!.location).toContain("San Francisco, CA");
  });

  test("scrapes modern Greenhouse listings from window.__remixContext in script", () => {
    const remixState = {
      state: {
        loaderData: {
          root: {
            boardConfiguration: { name: "Anthropic" },
          },
          "routes/$url_token": {
            board: { name: "Anthropic" },
            jobPosts: {
              data: [
                {
                  id: 5183044008,
                  title: "Anthropic Fellows Program, AI Safety & Security",
                  location: "London, UK; San Francisco, CA",
                  absolute_url: "https://job-boards.greenhouse.io/anthropic/jobs/5183044008",
                  department: { name: "AI Research & Engineering" },
                },
                {
                  id: 5183053008,
                  title: "Research Scientist, Interpretability",
                  location: "San Francisco, CA; Remote",
                  absolute_url: "https://job-boards.greenhouse.io/anthropic/jobs/5183053008",
                  department: { name: "AI Research & Engineering" },
                },
              ],
            },
          },
        },
      },
    };

    const doc = makeDoc([
      el("script", {}, `window.__remixContext = ${JSON.stringify(remixState)};`),
      el("h1", {}, "Open Roles"),
    ], "Jobs at Anthropic");

    const result = scrapePageJobs(doc, "job-boards.greenhouse.io");
    expect(result.totalFound).toBe(2);
    expect(result.jobs[0]!.company).toBe("Anthropic");
    const safetyJob = result.jobs.find(j => j.title.includes("AI Safety & Security"));
    expect(safetyJob).toBeDefined();
    expect(safetyJob!.title).toBe("Anthropic Fellows Program, AI Safety & Security");
    expect(safetyJob!.location).toBe("London, UK; San Francisco, CA");
    expect(safetyJob!.url).toBe("https://job-boards.greenhouse.io/anthropic/jobs/5183044008");
    const interpJob = result.jobs.find(j => j.title.includes("Interpretability"));
    expect(interpJob).toBeDefined();
    expect(interpJob!.title).toBe("Research Scientist, Interpretability");
  });

  test("scrapes modern Greenhouse single job posting page", () => {
    const doc = makeDoc([
      el("meta", { property: "og:title", content: "Anthropic Fellows Program, AI Safety & Security" }),
      el("meta", { property: "og:description", content: "London, UK; San Francisco, CA" }),
      el("div", { class: "image-container" }, "", [
        el("img", { alt: "Anthropic Logo", src: "https://example.com/logo.png" }),
      ]),
      el("main", { class: "main job-post" }, "", [
        el("div", { class: "job-post-container" }, "", [
          el("div", { class: "job__header" }, "", [
            el("h1", { class: "section-header" }, "Anthropic Fellows Program, AI Safety & Security"),
            el("div", { class: "job__location" }, "London, UK; San Francisco, CA"),
          ]),
          el("div", { class: "job__description body" }, "Anthropic's mission is to create reliable AI systems. Building large language models using Go, Python, Kubernetes."),
        ]),
      ]),
    ], "Job Application for Anthropic Fellows Program, AI Safety & Security at Anthropic");

    const result = scrapePageJobs(doc, "job-boards.greenhouse.io");
    expect(result.totalFound).toBe(1);
    expect(result.jobs[0]!.title).toBe("Anthropic Fellows Program, AI Safety & Security");
    expect(result.jobs[0]!.company).toBe("Anthropic");
    expect(result.jobs[0]!.location).toBe("London, UK; San Francisco, CA");
    expect(result.jobs[0]!.snippet).toContain("Anthropic's mission");
  });

  test("exportJobsAsCSV sanitizes formula characters (=, +, -, @, \\t, \\r)", () => {
    const maliciousJobs: any[] = [
      {
        id: "job-1",
        title: "=cmd|' /C calc'!A0",
        company: "+EvilCorp",
        location: "@Remote",
        url: "https://example.com/job/1",
        salary: "$150k",
        snippet: "test",
        relevance: { score: 85, fitLabel: "-High Risk" },
      },
    ];

    const csv = exportJobsAsCSV(maliciousJobs);
    expect(csv).toContain("\"'=cmd|' /C calc'!A0\"");
    expect(csv).toContain("\"'+EvilCorp\"");
    expect(csv).toContain("\"'@Remote\"");
    expect(csv).toContain("\"'-High Risk\"");
  });
});

// Workday is a single-page app: <head>'s JSON-LD describes the URL the document loaded at, however
// far in-app navigation has moved on. A JSON-LD posting with no url of its own is listed only
// when it belongs to the route the page is on (jobinfo.ts's postingIsCurrent).
describe("Workday: stale head metadata and card selection", () => {
  const HOST = "utaustin.wd1.myworkdayjobs.com";
  const A_JOB = `https://${HOST}/UTstaff/job/AUSTIN-TX/Forward-Deployed-Engineer_R_00049483`;
  const SEARCH = `https://${HOST}/en-US/UTstaff?q=engineer`;
  const B_DETAILS = `https://${HOST}/en-US/UTstaff/details/Staff-Accountant_R_00011111?q=engineer`;
  const ldA = (extra: Record<string, unknown> = {}) => JSON.stringify({
    "@context": "https://schema.org",
    "@type": "JobPosting",
    title: "Forward Deployed Engineer",
    identifier: { "@type": "PropertyValue", value: "R_00049483" },
    hiringOrganization: { name: "UT Austin" },
    description: "<p>Build Go services.</p>",
    ...extra,
  });
  const ldScript = (raw: string) => el("script", { type: "application/ld+json" }, raw);
  const card = (title: string, href: string) =>
    el("li", { class: "css-1q2dra3" }, "", [
      el("div", {}, "", [
        el("h3", {}, "", [el("a", { "data-automation-id": "jobTitle", href }, title)]),
        el("dd", { "data-automation-id": "locations" }, "AUSTIN, TX"),
      ]),
    ]);
  const results = (cards: MEl[]) => el("section", { "data-automation-id": "jobResults" }, "", [el("ul", { role: "list" }, "", cards)]);
  const searchBody = () => [results([
    card("Software Engineer", "/en-US/UTstaff/details/Software-Engineer_R_1?q=engineer"),
    card("Staff Accountant", "/en-US/UTstaff/details/Staff-Accountant_R_2?q=engineer"),
    card("Forward Deployed Engineer", "/en-US/UTstaff/details/Forward-Deployed-Engineer_R_00049483?q=engineer"),
  ])];
  const postingBody = (title: string, desc: string) => [
    el("div", { "data-automation-id": "jobPostingPage" }, "", [
      el("h2", { "data-automation-id": "jobPostingHeader" }, title),
      el("div", { "data-automation-id": "jobPostingDescription" }, "", [
        el("p", {}, desc),
        el("a", { href: "https://hr.utexas.edu/prospective/benefits" }, "benefits and total rewards information"),
      ]),
    ]),
  ];

  test("a head posting from the page the document loaded at is not listed under the search URL after in-app navigation", () => {
    const doc = makeDoc([ldScript(ldA()), ...searchBody()]);
    const r = scrapePageJobs(doc, HOST, undefined, SEARCH, A_JOB);
    expect(r.jobs.filter(j => j.url.split("?")[0] === SEARCH.split("?")[0])).toEqual([]);
    expect(r.jobs.map(j => j.title).sort()).toEqual(["Forward Deployed Engineer", "Software Engineer", "Staff Accountant"]);
    expect(r.jobs.every(j => j.url.includes("/details/"))).toBe(true);
  });

  test("a head posting is not listed under another posting's URL after in-app navigation", () => {
    const doc = makeDoc([ldScript(ldA()), ...postingBody("Staff Accountant", "Keep the books.")]);
    const r = scrapePageJobs(doc, HOST, undefined, B_DETAILS, A_JOB);
    expect(r.jobs.filter(j => j.title === "Forward Deployed Engineer")).toEqual([]);
  });

  test("a head posting whose requisition id is in the current URL is listed there", () => {
    const here = `https://${HOST}/en-US/UTstaff/details/Forward-Deployed-Engineer_R_00049483`;
    const doc = makeDoc([ldScript(ldA()), ...postingBody("Forward Deployed Engineer", "Build Go services.")]);
    const r = scrapePageJobs(doc, HOST, undefined, here, A_JOB);
    expect(r.jobs.filter(j => j.title === "Forward Deployed Engineer").map(j => j.url)).toEqual([here]);
  });

  test("a head posting on the page the document loaded at is listed once, under that URL", () => {
    const doc = makeDoc([ldScript(ldA()), ...postingBody("Forward Deployed Engineer", "Build Go services.")]);
    const r = scrapePageJobs(doc, HOST, undefined, A_JOB, A_JOB);
    expect(r.jobs.filter(j => j.title === "Forward Deployed Engineer").map(j => j.url)).toEqual([A_JOB]);
  });

  test("a head posting that carries its own url keeps it after navigation", () => {
    const doc = makeDoc([ldScript(ldA({ url: A_JOB })), ...postingBody("Staff Accountant", "Keep the books.")]);
    const r = scrapePageJobs(doc, HOST, undefined, B_DETAILS, A_JOB);
    expect(r.jobs.filter(j => j.title === "Forward Deployed Engineer").map(j => j.url)).toEqual([A_JOB]);
  });

  test("without a known load URL a head posting is read as before", () => {
    const doc = makeDoc([ldScript(ldA())]);
    const r = scrapePageJobs(doc, HOST, undefined, A_JOB);
    expect(r.jobs.map(j => j.title)).toEqual(["Forward Deployed Engineer"]);
  });

  test("a posting is rated on its whole description; only the stored snippet is clipped", () => {
    const filler = "We value collaboration and clear writing across the whole team every day. ".repeat(8).trim();
    const description = `${filler} Day to day you will build services in Go and Kubernetes with Rust.`;
    expect(description.indexOf("Kubernetes")).toBeGreaterThan(300);
    const ld = JSON.stringify({ "@type": "JobPosting", title: "Backend Engineer", description, hiringOrganization: { name: "Acme" }, url: "https://acme.com/jobs/1" });
    const job = scrapePageJobs(makeDoc([ldScript(ld)]), "acme.com").jobs[0]!;
    expect(job.snippet.length).toBeLessThanOrEqual(300);
    expect(job.relevance.matchedSkills).toContain("kubernetes");
    expect(job.relevance.score).toBe(evaluateRelevance({ title: "Backend Engineer", company: "Acme", description, location: "" }).score);
  });

  test("a Greenhouse posting page is rated on its whole description", () => {
    const filler = "We value collaboration and clear writing across the whole team every day. ".repeat(8).trim();
    const description = `${filler} Day to day you will build services in Go and Kubernetes with Rust.`;
    const doc = makeDoc([
      el("main", { class: "job-post" }, "", [
        el("h1", { class: "section-header" }, "Backend Engineer"),
        el("div", { class: "job__description" }, description),
      ]),
    ]);
    const job = scrapePageJobs(doc, "job-boards.greenhouse.io", undefined, "https://job-boards.greenhouse.io/acme/jobs/1").jobs[0]!;
    expect(job.snippet.length).toBeLessThanOrEqual(300);
    expect(job.relevance.matchedSkills).toContain("kubernetes");
  });

  test("one posting reached by its /job/ route and listed by a /details/ card is one listing", () => {
    const here = `https://${HOST}/en-US/UTstaff/job/AUSTIN-TX/Forward-Deployed-Engineer_R_00049483`;
    const doc = makeDoc([
      ldScript(ldA()),
      ...postingBody("Forward Deployed Engineer", "Build Go services."),
      results([card("Forward Deployed Engineer", "/en-US/UTstaff/details/Forward-Deployed-Engineer_R_00049483?q=engineer")]),
    ]);
    const r = scrapePageJobs(doc, HOST, undefined, here, here);
    expect(r.jobs.filter(j => j.title === "Forward Deployed Engineer").length).toBe(1);
  });

  test("two different Workday postings with one title stay two listings", () => {
    const doc = makeDoc([results([
      card("Software Engineer", "/en-US/UTstaff/details/Software-Engineer_R_1"),
      card("Software Engineer", "/en-US/UTstaff/details/Software-Engineer_R_2"),
    ])]);
    expect(scrapePageJobs(doc, HOST, undefined, SEARCH).jobs.length).toBe(2);
  });

  test("a Workday card is not credited to the page heading", () => {
    const doc = makeDoc([el("h1", {}, "Search for Jobs"), ...searchBody()]);
    const r = scrapePageJobs(doc, HOST, undefined, SEARCH);
    expect(r.jobs.length).toBe(3);
    expect(r.jobs.map(j => j.company)).toEqual(["", "", ""]);
  });

  test("a Workday card is credited to the company the page names", () => {
    const named = makeDoc([el("span", { "data-automation-id": "companyName" }, "UT Austin"), ...searchBody()]);
    expect(new Set(scrapePageJobs(named, HOST, undefined, SEARCH).jobs.map(j => j.company))).toEqual(new Set(["UT Austin"]));
    const careersAt = makeDoc([el("meta", { property: "og:title", content: "CAREERS AT NVIDIA" }), ...searchBody()]);
    expect(new Set(scrapePageJobs(careersAt, HOST, undefined, SEARCH).jobs.map(j => j.company))).toEqual(new Set(["NVIDIA"]));
  });

  test("a link inside the description is not a listing", () => {
    const doc = makeDoc(postingBody("Forward Deployed Engineer", "Build Go services."));
    const r = scrapePageJobs(doc, HOST, undefined, A_JOB);
    expect(r.jobs.filter(j => j.url.includes("hr.utexas.edu"))).toEqual([]);
  });

  test("only same-origin jobTitle anchors to a /job/ or /details/ path are listed", () => {
    const doc = makeDoc([results([
      card("Software Engineer", "/en-US/UTstaff/details/Software-Engineer_R_1"),
      card("Data Engineer", "/en-US/UTstaff/job/AUSTIN-TX/Data-Engineer_R_2"),
      card("Benefits Overview", "/en-US/UTstaff/benefits"),
      card("Elsewhere Engineer", "https://evil.example.com/job/1"),
      card("Careers Home", "https://hr.utexas.edu/job/7"),
    ])]);
    const r = scrapePageJobs(doc, HOST, undefined, SEARCH);
    expect(r.jobs.map(j => j.title).sort()).toEqual(["Data Engineer", "Software Engineer"]);
    const se = r.jobs.find(j => j.title === "Software Engineer")!;
    expect(se.url).toBe(`https://${HOST}/en-US/UTstaff/details/Software-Engineer_R_1`);
    expect(se.location).toBe("AUSTIN, TX");
  });
});

describe("the role excerpt Vani is sent is longer than the listing snippet", () => {
  const bytes = (s: string): number => new TextEncoder().encode(s).length;
  const sentence = "You will design and run backend services in Go and Kubernetes for customers. ";
  const body = sentence.repeat(30).trim(); // about 2.3 KB, well past the 300-character snippet
  const ldDoc = (description: string) =>
    makeDoc([el("script", { type: "application/ld+json" }, JSON.stringify({ "@type": "JobPosting", title: "Backend Engineer", description, hiringOrganization: { name: "Acme" }, url: "https://acme.com/jobs/1" }))]);
  const sent = (r: ReturnType<typeof scrapePageJobs>) => prepareIngest({ pageUrl: "", pageTitle: "", jobs: r.jobs }, false);

  test("a JSON-LD posting is sent whole while its listing snippet stays at 300 characters", () => {
    const r = scrapePageJobs(ldDoc(`${body} Closing note: references are checked.`), "acme.com");
    const job = r.jobs[0]!;
    expect(job.snippet.length).toBeLessThanOrEqual(300);
    const p = sent(r);
    if ("error" in p) throw new Error(p.error);
    const snippet = p.req.jobs[0]!.snippet;
    expect(snippet.length).toBeGreaterThan(2000);
    expect(snippet).toContain("references are checked");
  });

  test("an excerpt over the ingest bound is cut to it, in bytes, and the job is still sent", () => {
    const multibyte = "Développez des services fiables et évolutifs pour nos clients. ".repeat(200); // ~13 KB, 2-byte letters
    const r = scrapePageJobs(ldDoc(multibyte), "acme.com");
    const description = r.jobs[0]!.description!;
    expect(bytes(description)).toBeLessThanOrEqual(4000);
    expect(bytes(description)).toBeGreaterThan(3500);
    const p = sent(r);
    expect("error" in p).toBe(false);
  });

  test("a Greenhouse posting page is sent whole", () => {
    const doc = makeDoc([
      el("link", { rel: "canonical", href: "https://job-boards.greenhouse.io/acme/jobs/1" }),
      el("main", { class: "job-post" }, "", [
        el("h1", { class: "section-header" }, "Backend Engineer"),
        el("div", { class: "job__description" }, `${body} Closing note: references are checked.`),
      ]),
    ]);
    const r = scrapePageJobs(doc, "job-boards.greenhouse.io", undefined, "https://job-boards.greenhouse.io/acme/jobs/1");
    expect(r.jobs[0]!.snippet.length).toBeLessThanOrEqual(300);
    const p = sent(r);
    if ("error" in p) throw new Error(p.error);
    expect(p.req.jobs[0]!.snippet).toContain("references are checked");
  });

  test("a Greenhouse Remix posting is sent whole", () => {
    const state = { state: { loaderData: { root: { boardConfiguration: { name: "Acme" } }, "routes/$url_token": {
      board: { name: "Acme" },
      jobPost: { title: "Backend Engineer", company_name: "Acme", location: "Remote", public_url: "https://job-boards.greenhouse.io/acme/jobs/9", content: `${body} Closing note: references are checked.` },
    } } } };
    const doc = makeDoc([el("script", {}, `window.__remixContext = ${JSON.stringify(state)};`)]);
    const r = scrapePageJobs(doc, "job-boards.greenhouse.io");
    expect(r.jobs[0]!.snippet.length).toBeLessThanOrEqual(300);
    const p = sent(r);
    if ("error" in p) throw new Error(p.error);
    expect(p.req.jobs[0]!.snippet).toContain("references are checked");
  });

  test("a list card has no excerpt and is sent as its snippet", () => {
    const doc = makeDoc([el("div", { class: "opening" }, "", [el("a", { href: "/jobs/7" }, "Data Engineer"), el("span", { class: "location" }, "Remote")])]);
    const r = scrapePageJobs(doc, "boards.greenhouse.io", undefined, "https://boards.greenhouse.io/acme");
    expect(r.jobs[0]!.description).toBeUndefined();
    const p = sent(r);
    if ("error" in p) throw new Error(p.error);
    expect(p.req.jobs[0]!.snippet).toBe(r.jobs[0]!.snippet);
  });

  test("the JSON export is not made longer by the excerpt", () => {
    const r = scrapePageJobs(ldDoc(body), "acme.com");
    expect(r.jobs[0]!.description).toBeDefined();
    expect(exportJobsAsJSON(r.jobs)).not.toContain("description");
  });
});

describe("scraped links are http(s) or empty", () => {
  const posting = (url: unknown) => makeDoc([
    el("script", { type: "application/ld+json" }, JSON.stringify({ "@type": "JobPosting", title: "Backend Engineer", description: "Go services", hiringOrganization: { name: "Acme" }, url })),
  ]);
  const urlOf = (url: unknown) => scrapePageJobs(posting(url), "acme.com").jobs[0]!.url;

  test("javascript:, data:, file: and other schemes yield an empty url, whatever the case or leading whitespace", () => {
    for (const bad of [
      "javascript:alert(1)", "  JaVaScRiPt:alert(1)", "\tjavascript:alert(1)", "\njavascript:void(0)", "JAVASCRIPT:alert(1)",
      "data:text/html,<b>x</b>", " DATA:text/html;base64,AAAA", "file:///etc/hosts", "  FiLe:///etc/hosts",
      "ftp://acme.com/jobs/1", "chrome-extension://abc/page.html", "blob:https://acme.com/1", "mailto:jobs@acme.com", "vbscript:x",
    ]) {
      expect(urlOf(bad)).toBe("");
    }
  });

  test("http(s) links, absolute or relative to the page, are kept", () => {
    expect(urlOf("https://acme.com/jobs/101")).toBe("https://acme.com/jobs/101");
    expect(urlOf("  HTTP://acme.com/jobs/102")).toBe("http://acme.com/jobs/102");
    expect(urlOf("/jobs/103")).toMatch(/^https?:\/\/[^/]+\/jobs\/103$/);
  });

  test("a posting with a hostile link is still listed, with no link", () => {
    const r = scrapePageJobs(posting("javascript:alert(1)"), "acme.com");
    expect(r.totalFound).toBe(1);
    expect(r.jobs[0]!.title).toBe("Backend Engineer");
    expect(exportJobsAsJSON(r.jobs)).not.toContain("javascript");
  });
});

describe("adversarial: scraped link shapes", () => {
  const posting = (url: unknown) => makeDoc([
    el("script", { type: "application/ld+json" }, JSON.stringify({ "@type": "JobPosting", title: "Backend Engineer", description: "Go services", hiringOrganization: { name: "Acme" }, url })),
  ]);
  const jobs = (url: unknown) => scrapePageJobs(posting(url), "acme.com").jobs;
  const urlOf = (url: unknown) => jobs(url)[0]!.url;
  const isHttp = (u: string) => u === "" || /^https?:\/\//.test(u);

  test("scheme tricks: no host, protocol-relative, case, tab/newline smuggling, blob, about, chrome-extension, vbscript", () => {
    for (const bad of ["javascript:alert(1)", "java\nscript:alert(1)", "java\tscript:alert(1)", "java\r\nscript:alert(1)", "\u0000javascript:alert(1)",
      "vbscript:msgbox(1)", "blob:https://acme.com/1", "about:blank", "about:srcdoc", "chrome-extension://abc/popup.html", "moz-extension://abc/x", "safari-web-extension://abc/x", "chrome://settings", "view-source:https://acme.com", "ws://acme.com/x", "wss://acme.com/x", "tel:+15555550100"]) {
      expect([bad, urlOf(bad)]).toEqual([bad, ""]);
    }
    // These parse as http(s) in the URL parser (the scheme is folded, whitespace dropped, a missing host after a special scheme is read from what follows).
    for (const odd of ["https:", "jav&#x61;script:alert(1)", "javascript\u200b:alert(1)", "HTTPS://acme.com/jobs/1", "\thttps://acme.com/jobs/1", " https://acme.com/jobs/1", "https:acme.com/jobs/1", "http:/acme.com/jobs/1", "https:///acme.com/jobs/1", "//acme.com/jobs/1"]) {
      const u = urlOf(odd);
      expect([odd, isHttp(u)]).toEqual([odd, true]);
    }
    // No usable host at all: refused.
    for (const nohost of ["http:", "https://", "http://:80", "http://?x", "https://#frag", "http://[::1"]) {
      expect([nohost, urlOf(nohost)]).toEqual([nohost, ""]);
    }
  });

  test("a protocol-relative '//evil' link resolves against the page's scheme: it is http(s), on whatever host it names (the page's own listing)", () => {
    const u = urlOf("//evil.example/jobs/1");
    expect(u).toMatch(/^https?:\/\/evil\.example\/jobs\/1$/);
  });

  test("a relative link resolved against a hostile base (javascript:, data:, about:blank) is empty, never that base", () => {
    const saved = (globalThis as { location?: unknown }).location;
    try {
      for (const href of ["javascript:alert(1)", "data:text/html,x", "about:blank", "blob:https://acme.com/1", "chrome-extension://abc/popup.html"]) {
        (globalThis as { location?: unknown }).location = { href, hostname: "acme.com" };
        for (const rel of ["/jobs/1", "jobs/1", "?id=1", "#x", ""]) {
          const u = urlOf(rel === "" ? undefined : rel);
          expect([href, rel, isHttp(u)]).toEqual([href, rel, true]);
        }
      }
    } finally {
      (globalThis as { location?: unknown }).location = saved;
    }
  });

  test("a JSON-LD url that is an array, object, number, boolean or null never yields a non-http(s) link", () => {
    for (const v of [["https://acme.com/a", "javascript:alert(1)"], ["javascript:alert(1)"], { "@id": "javascript:alert(1)" }, { url: "javascript:alert(1)" }, 42, true, null, [], {}, [[]], "[object Object]"]) {
      const j = jobs(v);
      expect(j.length).toBe(1);
      expect([JSON.stringify(v), isHttp(j[0]!.url)]).toEqual([JSON.stringify(v), true]);
      expect(j[0]!.url).not.toMatch(/javascript/i);
    }
  });

  test("an extremely long url (1 MB) neither throws nor produces a non-http(s) link, and its size is bounded for the ingest message", () => {
    for (const long of [`https://acme.com/${"a".repeat(1_000_000)}`, `javascript:${"a".repeat(1_000_000)}`, `/${"a".repeat(1_000_000)}`]) {
      const j = jobs(long);
      expect(j.length).toBe(1);
      expect(isHttp(j[0]!.url)).toBe(true);
    }
    // the ingest bound (2048 bytes) refuses what is too long rather than truncating it into another link
    const long = jobs(`https://acme.com/${"a".repeat(1_000_000)}`)[0]!;
    const r = prepareIngest({ pageUrl: "https://acme.com/careers", pageTitle: "Jobs", jobs: [long] }, false);
    expect(JSON.stringify(r)).not.toContain("a".repeat(4096));
  });
});

// Workday's shared host wd{N}.myworkdaysite.com (Snap's board): the site is /recruiting/{tenant}/{site},
// and one posting is reachable by /job/ and /details/ routes with or without a locale, as on myworkdayjobs.com.
describe("Workday on the shared host", () => {
  const HOST = "wd1.myworkdaysite.com";
  const SITE = "/recruiting/snapchat/snap";
  const SEARCH = `https://${HOST}${SITE}?q=data`;
  const card = (title: string, href: string) =>
    el("li", {}, "", [
      el("a", { "data-automation-id": "jobTitle", href }, title),
      el("dd", { "data-automation-id": "locations" }, "Los Angeles, California"),
    ]);
  const listing = (cards: MEl[]) => [el("section", { "data-automation-id": "jobResults" }, "", [el("ul", {}, "", cards)])];
  const scrape = (cards: MEl[], host = HOST, page = SEARCH) => scrapePageJobs(makeDoc(listing(cards)), host, undefined, page).jobs;

  test("lists the posting cards of the site, with their own links", () => {
    const jobs = scrape([
      card("Data Scientist, Level 4", `${SITE}/job/Los-Angeles-California/Data-Scientist--Level-4_Q426DSA4-1`),
      card("Machine Learning Engineer", `/en-US${SITE}/job/Paris-France/Machine-Learning-Engineer_R2-1`),
    ]);
    expect(jobs.map(j => j.url).sort()).toEqual([
      `https://${HOST}${SITE}/job/Los-Angeles-California/Data-Scientist--Level-4_Q426DSA4-1`,
      `https://${HOST}/en-US${SITE}/job/Paris-France/Machine-Learning-Engineer_R2-1`,
    ].sort());
  });

  test("one posting reached by two routes is one listing; two postings stay two", () => {
    const one = scrape([
      card("Data Scientist, Level 4", `${SITE}/job/Los-Angeles-California/Data-Scientist--Level-4_Q426DSA4-1`),
      card("Data Scientist, Level 4", `/en-US${SITE}/details/Data-Scientist--Level-4_Q426DSA4-1?q=data`),
      card("Data Scientist, Level 4", `/en-US${SITE}/job/Los-Angeles-California/Data-Scientist--Level-4_Q426DSA4-1/apply`),
    ]);
    expect(one.length).toBe(1);
    const two = scrape([
      card("Data Scientist, Level 4", `${SITE}/details/Data-Scientist--Level-4_Q426DSA4-1`),
      card("Data Scientist, Level 4", `${SITE}/details/Data-Scientist--Level-4_Q426DSA4-2`),
    ]);
    expect(two.length).toBe(2);
  });

  test("the same slug under another tenant or site is another posting", () => {
    const jobs = scrape([
      card("Data Scientist", "/recruiting/snapchat/snap/details/Data-Scientist_R1"),
      card("Data Scientist", "/recruiting/snapchat/other/details/Data-Scientist_R1"),
      card("Data Scientist", "/recruiting/acme/snap/details/Data-Scientist_R1"),
    ]);
    expect(jobs.length).toBe(3);
  });

  test("the host is told whatever its case", () => {
    const jobs = scrape([card("Data Scientist", `/en-US${SITE}/details/Data-Scientist_R1`), card("Data Scientist", `${SITE}/job/LA/Data-Scientist_R1`)], "WD1.MyWorkdaySite.com");
    expect(jobs.length).toBe(1);
  });

  test("a lookalike host is not folded", () => {
    const evil = "wd1.myworkdaysite.com.evil.com";
    const twice = scrape([card("Data Scientist", `${SITE}/details/Data-Scientist_R1`), card("Data Scientist", `/en-US${SITE}/job/LA/Data-Scientist_R1`)], evil, `https://${evil}${SITE}`);
    expect(twice.length).toBe(2);
  });
});
