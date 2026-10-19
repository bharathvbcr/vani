import { describe, expect, test } from "bun:test";
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
    if (s.startsWith(".")) {
      if (node.classes.includes(s.slice(1))) return true;
    } else if (s.includes("[") && s.includes("*=")) {
      const m = s.match(/\[([a-zA-Z0-9_-]+)\*=['"]?([^'"]*)['"]?\]/);
      if (m && (node.getAttribute(m[1]!) ?? "").includes(m[2]!)) return true;
    } else if (s.includes("[") && s.includes("=")) {
      const m = s.match(/\[([a-zA-Z0-9_-]+)=['"]?([^'"]*)['"]?\]/);
      if (m && node.getAttribute(m[1]!) === m[2]) return true;
    } else if (s.includes("[")) {
      const m = s.match(/\[([a-zA-Z0-9_-]+)\]/);
      if (m && node.getAttribute(m[1]!) !== null) return true;
    } else if (node.tag === s.toLowerCase()) {
      return true;
    }
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
    expect(csv).toContain("Title,Company,Relevance,Fit,Location,Salary,URL");
    expect(csv).toContain('"Senior Backend Engineer"');
    expect(csv).toContain('"Acme, Inc."');
    expect(csv).toContain("88%");

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
});
