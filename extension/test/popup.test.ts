import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { isHttpUrl, openButton, titleElement } from "../src/joblink";

describe("popup.html home page UI and UX structure", () => {
  const popupHtmlPath = join(import.meta.dir, "../static/popup.html");
  const html = readFileSync(popupHtmlPath, "utf-8");

  test("contains all required navigation tabs with Home & Fill as default active tab", () => {
    expect(html).toContain('data-tab="page"');
    expect(html).toContain('data-tab="scraper"');
    expect(html).toContain('data-tab="basics"');
    expect(html).toContain('data-tab="answers"');
    expect(html).toContain('data-tab="settings"');
    expect(html).toContain('Home &amp; Fill');
  });

  test("contains the Home tab pane with tab-page id", () => {
    expect(html).toContain('id="tab-page"');
  });

  test("contains all form readiness meter components and metric cards", () => {
    expect(html).toContain('id="meter-card"');
    expect(html).toContain('id="meter-percent"');
    expect(html).toContain('id="meter-subtitle"');
    expect(html).toContain('id="bar-ok"');
    expect(html).toContain('id="bar-rev"');
    expect(html).toContain('id="bar-unk"');
    expect(html).toContain('id="stat-ok"');
    expect(html).toContain('id="stat-rev"');
    expect(html).toContain('id="stat-unk"');
  });

  test("contains hero CTA button and secondary actions with required IDs", () => {
    expect(html).toContain('id="fill"');
    expect(html).toContain('id="save"');
    expect(html).toContain('id="clear-marks"');
    expect(html).toContain('id="overwrite"');
    expect(html).toContain('id="typeahead"');
    expect(html).toContain('⌥⇧F');
  });

  test("contains resume attachment card and controls", () => {
    expect(html).toContain('id="resume"');
    expect(html).toContain('id="resume-name"');
    expect(html).toContain('id="resume-pick"');
    expect(html).toContain('id="resume-view"');
    expect(html).toContain('id="resume-status"');
    expect(html).toContain('id="resume-remove"');
    expect(html).toContain('accept="application/pdf,.pdf"');
  });

  test("contains job details and portal cards", () => {
    expect(html).toContain('id="portal-card"');
    expect(html).toContain('id="portal-icon"');
    expect(html).toContain('id="portal-name"');
    expect(html).toContain('id="portal-step"');
    expect(html).toContain('id="portal-tips"');

    expect(html).toContain('id="job-details-card"');
    expect(html).toContain('id="job-title-text"');
    expect(html).toContain('id="job-company-text"');
    expect(html).toContain('id="copy-job-details"');
    expect(html).toContain('id="copy-job-title"');
    expect(html).toContain('id="copy-job-desc"');
    expect(html).toContain('id="job-desc-snippet"');
  });

  test("contains relevance and audit checklist cards", () => {
    expect(html).toContain('id="relevance-card"');
    expect(html).toContain('id="rel-score-badge"');
    expect(html).toContain('id="rel-score-bar-fill"');
    expect(html).toContain('id="rel-fit-label"');
    expect(html).toContain('id="rel-workplace"');
    expect(html).toContain('id="rel-skills-row"');
    expect(html).toContain('id="rel-dealbreaker"');

    expect(html).toContain('id="missed-card"');
    expect(html).toContain('id="missed-count-badge"');
    expect(html).toContain('id="missed-list"');
  });

  test("contains coverage, frames, empty guide, and notification elements", () => {
    expect(html).toContain('id="home-empty-card"');
    expect(html).toContain('id="btn-goto-scraper"');
    expect(html).toContain('id="btn-goto-basics"');
    expect(html).toContain('id="btn-empty-rescan"');
    expect(html).toContain('id="applied"');
    expect(html).toContain('id="applied-text"');
    expect(html).toContain('id="applied-yes"');
    expect(html).toContain('id="applied-no"');
    expect(html).toContain('id="status"');
    expect(html).toContain('id="framebox"');
    expect(html).toContain('id="frames"');
    expect(html).toContain('id="coverage"');
    expect(html).toContain('id="coverage-note"');
    expect(html).toContain("never what you typed");
    expect(html).toContain('id="unknown"');
    expect(html).toContain('id="toast"');
  });

  test("contains header re-scan button and header right container", () => {
    expect(html).toContain('id="btn-rescan"');
    expect(html).toContain('class="header-right"');
    expect(html).toContain('class="rescan-icon"');
    expect(html).toContain('Re-scan current tab (⌥⇧R)');
  });

  test("contains meter bar elements for ok, rev, and unk readiness segments", () => {
    expect(html).toContain('id="bar-ok" class="bar-ok"');
    expect(html).toContain('id="bar-rev" class="bar-rev"');
    expect(html).toContain('id="bar-unk" class="bar-unk"');
  });

  test("contains global [hidden] rule preventing element display leaks", () => {
    expect(html).toContain('[hidden] { display: none !important; }');
  });

  test("places status at top of tab-page, home-empty-card before meter-card, and fill-options in form view", () => {
    const statusPos = html.indexOf('id="status"');
    const emptyCardPos = html.indexOf('id="home-empty-card"');
    const meterCardPos = html.indexOf('id="meter-card"');
    const fillOptionsPos = html.indexOf('id="fill-options"');

    // Status banner is at the top of the Home tab
    expect(statusPos).toBeGreaterThan(-1);
    expect(emptyCardPos).toBeGreaterThan(statusPos);
    expect(meterCardPos).toBeGreaterThan(emptyCardPos);

    // Fill options is present in the form view
    expect(fillOptionsPos).toBeGreaterThan(meterCardPos);
  });
});

describe("a scraped job's link in the popup", () => {
  /** Just enough of a document to build the elements: plain objects that record what was set. */
  const doc = { createElement: (tag: string) => ({ tagName: tag.toUpperCase(), textContent: "", className: "", href: undefined, target: undefined, onclick: null }) } as unknown as Pick<Document, "createElement">;

  test("an http(s) link gets an anchor and an Open button that opens it", () => {
    const a = titleElement(doc, "Backend Engineer", "https://acme.com/jobs/1");
    expect(a.tagName).toBe("A");
    expect((a as HTMLAnchorElement).href).toBe("https://acme.com/jobs/1");
    expect((a as HTMLAnchorElement).rel).toBe("noopener noreferrer");
    const opened: unknown[][] = [];
    const b = openButton(doc, "https://acme.com/jobs/1", (...args) => opened.push(args))!;
    expect(b.textContent).toBe("Open role");
    b.onclick!(new Event("click") as MouseEvent);
    expect(opened).toEqual([["https://acme.com/jobs/1", "_blank", "noopener,noreferrer"]]);
  });

  test("an empty or non-http(s) url gets no anchor and no Open button", () => {
    for (const url of ["", "javascript:alert(1)", "  JaVaScRiPt:alert(1)", "data:text/html,x", "file:///etc/hosts", "/relative", "not a url", "   "]) {
      const t = titleElement(doc, "Backend Engineer", url);
      expect(t.tagName).toBe("SPAN");
      expect((t as unknown as { href?: string }).href).toBeUndefined();
      expect(t.textContent).toBe("Backend Engineer");
      expect(openButton(doc, url, () => { throw new Error("must not open"); })).toBeNull();
      expect(isHttpUrl(url)).toBe(false);
    }
    expect(isHttpUrl(undefined)).toBe(false);
    expect(isHttpUrl(7)).toBe(false);
  });
});
