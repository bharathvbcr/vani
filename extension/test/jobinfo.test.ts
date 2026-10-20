import { describe, expect, test } from "bun:test";
import { formatJobDetails, readJobInfo } from "../src/jobinfo";

interface MEl {
  tag: string;
  attrs: Record<string, string>;
  classes: string[];
  textContent: string;
  children: MEl[];
  getAttribute(name: string): string | null;
  querySelector(sel: string): MEl | null;
  querySelectorAll(sel: string): MEl[];
}

function el(tag: string, attrs: Record<string, string> = {}, rawText = "", children: MEl[] = []): MEl {
  const classes = (attrs["class"] || "").split(/\s+/).filter(Boolean);
  return {
    tag: tag.toLowerCase(),
    attrs,
    classes,
    get textContent() {
      return (rawText || this.children.map(c => c.textContent).join(" ")).trim();
    },
    children,
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
  };
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
    if (!tagMatch && !rest.startsWith(".") && !rest.startsWith("[")) continue;
    while (rest.length > 0 && ok) {
      if (rest.startsWith(".")) {
        const clsMatch = rest.match(/^\.([a-zA-Z0-9_-]+)/);
        if (clsMatch) {
          if (!node.classes.includes(clsMatch[1]!)) ok = false;
          rest = rest.slice(clsMatch[0].length);
        } else ok = false;
      } else if (rest.startsWith("[")) {
        const attrMatch = rest.match(/^\[([a-zA-Z0-9_-]+)([*~|^$]?=)?['"]?([^'"]*)['"]?\]/);
        if (attrMatch) {
          const attrVal = node.getAttribute(attrMatch[1]!);
          if (attrVal === null) ok = false;
          else if (attrMatch[2] === "*=" && !attrVal.includes(attrMatch[3]!)) ok = false;
          else if (attrMatch[2] === "=" && attrVal !== attrMatch[3]!) ok = false;
          rest = rest.slice(attrMatch[0].length);
        } else ok = false;
      } else ok = false;
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
    createElement: (tag: string) => {
      let innerHTML = "";
      return {
        tag,
        get innerHTML() { return innerHTML; },
        set innerHTML(val: string) { innerHTML = val; },
        get textContent() { return innerHTML.replace(/<[^>]+>/g, " "); },
      };
    },
    querySelector: (sel: string) => root.querySelector(sel),
    querySelectorAll: (sel: string) => root.querySelectorAll(sel),
  } as unknown as Document;
}

describe("readJobInfo", () => {
  test("extracts job info from JSON-LD when present", () => {
    const jsonLd = JSON.stringify({
      "@type": "JobPosting",
      title: "Staff Go Engineer",
      hiringOrganization: { name: "CloudTech" },
      description: "<p>Building resilient distributed systems in Go.</p>",
    });
    const doc = makeDoc([
      el("script", { type: "application/ld+json" }, jsonLd),
    ], "CloudTech Openings");

    const info = readJobInfo(doc);
    expect(info.title).toBe("Staff Go Engineer");
    expect(info.company).toBe("CloudTech");
    expect(info.description).toContain("Building resilient distributed systems");
  });

  test("extracts job info from modern Greenhouse layout without JSON-LD", () => {
    const doc = makeDoc([
      el("meta", { property: "og:title", content: "Anthropic Fellows Program, AI Safety & Security" }),
      el("meta", { property: "og:description", content: "London, UK; San Francisco, CA" }),
      el("div", { class: "image-container" }, "", [
        el("img", { alt: "Anthropic Logo", src: "https://example.com/logo.png" }),
      ]),
      el("main", { class: "main job-post" }, "", [
        el("div", { class: "job__description body" }, "Anthropic’s mission is to create reliable, interpretable, and steerable AI systems. We are seeking research fellows to work on scalable oversight."),
      ]),
    ], "Job Application for Anthropic Fellows Program, AI Safety & Security at Anthropic");

    const info = readJobInfo(doc);
    expect(info.title).toBe("Anthropic Fellows Program, AI Safety & Security");
    expect(info.company).toBe("Anthropic");
    // Description should be the full job text, NOT just the location string from og:description
    expect(info.description).toContain("Anthropic’s mission is to create reliable");
  });
});

describe("formatJobDetails", () => {
  test("formats title, company, url and description cleanly", () => {
    const job = {
      title: "Staff Go Engineer",
      company: "CloudTech",
      description: "Building resilient distributed systems in Go.",
    };
    const formatted = formatJobDetails(job, "https://cloudtech.example/jobs/123");
    expect(formatted).toBe(
      "Staff Go Engineer at CloudTech\nhttps://cloudtech.example/jobs/123\n\nBuilding resilient distributed systems in Go."
    );
  });

  test("handles title and description without company or url", () => {
    const job = {
      title: "Senior AI Researcher",
      company: "",
      description: "Research on alignment and safety.",
    };
    const formatted = formatJobDetails(job);
    expect(formatted).toBe("Senior AI Researcher\n\nResearch on alignment and safety.");
  });

  test("handles empty or missing fields gracefully", () => {
    expect(formatJobDetails({})).toBe("");
    expect(formatJobDetails({ title: "Lead Architect" })).toBe("Lead Architect");
    expect(formatJobDetails({ description: "Only description provided" })).toBe("Only description provided");
  });
});
