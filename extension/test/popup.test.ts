import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

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
