import { describe, expect, test } from "bun:test";
import { detectPortal } from "../src/portal";

interface MockNode {
  tag: string;
  attrs: Record<string, string>;
  classes: string[];
  textContent: string;
  src?: string;
  href?: string;
  getAttribute(name: string): string | null;
  classList: { contains(c: string): boolean };
}

function makeDoc(html: string) {
  const nodes: MockNode[] = [];
  const tagRegex = /<([a-zA-Z0-9]+)([^>]*)>/g;
  let match: RegExpExecArray | null;
  while ((match = tagRegex.exec(html)) !== null) {
    if (match[1]!.startsWith("/")) continue;
    const tag = match[1]!.toLowerCase();
    const attrString = match[2]!;
    const start = match.index + match[0].length;
    const closing = `</${tag}>`;
    const end = html.indexOf(closing, start);
    const textContent = end !== -1 ? html.slice(start, end).replace(/<[^>]+>/g, "").trim() : "";
    const attrs: Record<string, string> = {};
    const attrRegex = /([a-zA-Z0-9_-]+)(?:=(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g;
    let attrMatch: RegExpExecArray | null;
    while ((attrMatch = attrRegex.exec(attrString)) !== null) {
      const key = attrMatch[1]!.toLowerCase();
      const val = attrMatch[2] ?? attrMatch[3] ?? attrMatch[4] ?? "";
      attrs[key] = val;
    }
    const classList = (attrs["class"] || "").split(/\s+/).filter(Boolean);
    const node: MockNode = {
      tag,
      attrs,
      classes: classList,
      textContent,
      src: attrs["src"] || "",
      href: attrs["href"] || "",
      getAttribute(name: string) {
        return attrs[name.toLowerCase()] ?? null;
      },
      classList: {
        contains(c: string) {
          return classList.includes(c);
        },
      },
    };
    nodes.push(node);
  }

  function matchesOne(node: MockNode, sel: string): boolean {
    const s = sel.trim();
    if (!s) return false;
    if (s.includes(",")) {
      return s.split(",").some(part => matchesOne(node, part));
    }
    if (s.includes("#")) {
      const [t, id] = s.split("#");
      if (t && node.tag !== t.toLowerCase()) return false;
      return node.attrs["id"] === id;
    }
    if (s.startsWith(".")) {
      return node.classList.contains(s.slice(1));
    }
    if (s.includes("[")) {
      const attrMatch = s.match(/\[([a-zA-Z0-9_-]+)(?:(\*?=)['"]?([^'"]*)['"]?)?\]/);
      if (attrMatch) {
        const [, attrName, op, expectedVal] = attrMatch;
        const actualVal = node.getAttribute(attrName!);
        if (actualVal === null) return false;
        if (!op) return true;
        if (op === "=") return actualVal === expectedVal;
        if (op === "*=") return actualVal.includes(expectedVal ?? "");
      }
    }
    if (/^[a-zA-Z0-9]+$/.test(s)) {
      return node.tag === s.toLowerCase();
    }
    return false;
  }

  return {
    querySelector(sel: string) {
      for (const n of nodes) {
        if (matchesOne(n, sel)) return n;
      }
      return null;
    },
    querySelectorAll(sel: string) {
      return nodes.filter(n => matchesOne(n, sel));
    },
  };
}

describe("portal detection & adaptation", () => {
  test("detects Greenhouse portal", () => {
    const doc = makeDoc('<form id="application_form"><input type="text" name="first_name"></form>');
    const p1 = detectPortal(doc, "https://boards.greenhouse.io/acme/jobs/12345");
    expect(p1.id).toBe("greenhouse");
    expect(p1.name).toBe("Greenhouse");
    expect(p1.isATS).toBe(true);

    const docCustom = makeDoc('<div><form id="application_form"></form></div>');
    const p2 = detectPortal(docCustom, "https://careers.company.com/apply");
    expect(p2.id).toBe("greenhouse");

    const docBoard = makeDoc('<div data-qa="greenhouse-job-board"><table class="job-posts--table"><tbody><tr class="job-post"><td><a href="/jobs/1">Dev</a></td></tr></tbody></table></div>');
    const pBoard = detectPortal(docBoard, "https://job-boards.greenhouse.io/anthropic");
    expect(pBoard.id).toBe("greenhouse");
    expect(pBoard.isJobBoard).toBe(true);
  });

  test("detects Lever portal", () => {
    const doc = makeDoc('<form class="application-form"><input type="text" name="name"></form>');
    const p = detectPortal(doc, "https://jobs.lever.co/acme/abc-123");
    expect(p.id).toBe("lever");
    expect(p.name).toBe("Lever");
    expect(p.isATS).toBe(true);
  });

  test("detects Workday portal and extracts multi-step progress", () => {
    const doc = makeDoc(`
      <div data-automation-id="workdayApplication">
        <div data-automation-id="progressStep1" class="step">My Information</div>
        <div data-automation-id="progressStep2" aria-current="step" class="step active">My Experience</div>
        <div data-automation-id="progressStep3" class="step">Application Questions</div>
        <div data-automation-id="progressStep4" class="step">Review</div>
      </div>
    `);
    const p = detectPortal(doc, "https://acme.wd5.myworkdayjobs.com/en-US/Careers/job/Engineer_123");
    expect(p.id).toBe("workday");
    expect(p.isATS).toBe(true);
    expect(p.isMultiStep).toBe(true);
    expect(p.stepInfo?.currentStep).toBe(2);
    expect(p.stepInfo?.totalSteps).toBe(4);
    expect(p.stepInfo?.stepName).toBe("My Experience");
  });

  test("Workday search pages are job boards whatever the site is called; posting and apply pages are not", () => {
    const H = "https://utaustin.wd1.myworkdayjobs.com";
    const empty = makeDoc("");
    const board = (path: string) => detectPortal(empty, `${H}${path}`).isJobBoard;
    expect(board("/UTstaff?q=engineer")).toBe(true);
    expect(board("/en-US/UTstaff")).toBe(true);
    expect(board("/en-US/External_Careers")).toBe(true);
    expect(board("/en-US/UTstaff/job/AUSTIN-TX/Forward-Deployed-Engineer_R_1")).toBe(false);
    expect(board("/en-US/UTstaff/details/Forward-Deployed-Engineer_R_1?q=engineer")).toBe(false);
    expect(board("/en-US/UTstaff/job/AUSTIN-TX/Forward-Deployed-Engineer_R_1/apply")).toBe(false);
    expect(board("/en-US/UTstaff/login")).toBe(false);
    // A "search" in a posting's slug is not a search page.
    expect(board("/en-US/UTstaff/details/Search-Engine-Engineer_R_1")).toBe(false);
  });

  test("a Workday page that lists jobTitle anchors is a job board even on a /details/ path", () => {
    const doc = makeDoc('<a data-automation-id="jobTitle" href="/en-US/UTstaff/details/Other_R_2">Other</a>');
    expect(detectPortal(doc, "https://utaustin.wd1.myworkdayjobs.com/en-US/UTstaff/details/Forward-Deployed-Engineer_R_1").isJobBoard).toBe(true);
  });

  test("detects Ashby portal", () => {
    const doc = makeDoc('<div data-ashby-embed></div>');
    const p = detectPortal(doc, "https://jobs.ashbyhq.com/startup/456");
    expect(p.id).toBe("ashby");
    expect(p.name).toBe("Ashby");
    expect(p.isATS).toBe(true);
    expect(p.isJobBoard).toBe(false);

    const boardDoc = makeDoc('<h1 class="ashby-job-board-heading">Open Positions (131)</h1><div class="ashby-job-posting-brief-list"><div class="ashby-job-posting-brief"></div></div>');
    const board = detectPortal(boardDoc, "https://jobs.ashbyhq.com/perplexity");
    expect(board.id).toBe("ashby");
    expect(board.isJobBoard).toBe(true);

    const ssrBoard = detectPortal(makeDoc('<div id="root"></div>'), "https://jobs.ashbyhq.com/perplexity");
    expect(ssrBoard.id).toBe("ashby");
    expect(ssrBoard.isJobBoard).toBe(true);

    const appPage = detectPortal(makeDoc('<div class="ashby-job-posting-heading">Engineer</div>'), "https://jobs.ashbyhq.com/perplexity/5c561bd0/application");
    expect(appPage.id).toBe("ashby");
    expect(appPage.isJobBoard).toBe(false);
  });

  test("detects SmartRecruiters and Taleo", () => {
    const sr = detectPortal(makeDoc(""), "https://jobs.smartrecruiters.com/company/job1");
    expect(sr.id).toBe("smartrecruiters");

    const taleo = detectPortal(makeDoc(""), "https://acme.taleo.net/careersection/jobdetail.ftl");
    expect(taleo.id).toBe("taleo");
  });

  test("detects LinkedIn and Indeed job boards", () => {
    const linkedin = detectPortal(makeDoc(""), "https://www.linkedin.com/jobs/search/?keywords=golang");
    expect(linkedin.id).toBe("linkedin");
    expect(linkedin.isJobBoard).toBe(true);

    const indeed = detectPortal(makeDoc(""), "https://www.indeed.com/jobs?q=software+engineer");
    expect(indeed.id).toBe("indeed");
    expect(indeed.isJobBoard).toBe(true);
  });

  test("detects generic career job board from links", () => {
    const doc = makeDoc(`
      <div>
        <a href="/careers/job-1">Backend Engineer</a>
        <a href="/careers/job-2">Frontend Engineer</a>
        <a href="/careers/job-3">Product Designer</a>
      </div>
    `);
    const p = detectPortal(doc, "https://company.io/careers");
    expect(p.id).toBe("job_board");
    expect(p.isJobBoard).toBe(true);
  });
});
