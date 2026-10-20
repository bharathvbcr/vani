import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// The popup's header, tab bar and Home tab as markup: what must hold for them to fit,
// to stay accessible, and to keep working with the ids popup.ts binds.
const html = readFileSync(join(import.meta.dir, "../static/popup.html"), "utf-8");
const ts = readFileSync(join(import.meta.dir, "../src/popup.ts"), "utf-8");
const home = html.slice(html.indexOf("<body>"), html.indexOf('<section id="tab-scraper"'));

describe("header and tab bar", () => {
  const tabs = [...html.matchAll(/<button class="tab-btn[^"]*" data-tab="(\w+)"[^>]*title="([^"]+)"[^>]*>([\s\S]*?)<\/button>/g)];

  test("five tabs keep their data-tab values, with short labels and the full name as a tooltip", () => {
    expect(tabs.map(t => t[1])).toEqual(["page", "scraper", "basics", "answers", "settings"]);
    expect(tabs.map(t => t[2])).toEqual(["Home &amp; Fill", "Job Scraper", "Your Basics", "Saved Answers", "Settings &amp; Data"]);
    const labels = tabs.map(t => /<span>([^<]+)<\/span>/.exec(t[3])![1]);
    expect(labels).toEqual(["Home", "Jobs", "Basics", "Answers", "Settings"]);
    // 494px wide, 8px padding each side, five equal tabs: 95px each; at 11px a label of 8 characters is about 50px.
    for (const l of labels) expect(l.length).toBeLessThanOrEqual(8);
  });

  test("tabs are exposed as a tablist, and the script keeps aria-selected in step", () => {
    expect(html).toContain('role="tablist"');
    expect(tabs.length).toBe(5);
    for (const t of tabs) expect(t[0]).toContain('role="tab"');
    expect(ts).toContain('setAttribute("aria-selected"');
  });

  test("no emoji in the header, tab bar or Home markup except the portal's own data-driven icon", () => {
    // The cards between the portal strip and the dashboard (portal, job, relevance, missed) belong to the job-card work; the rest is checked here.
    const mine = home.slice(0, home.indexOf('id="portal-card"')) + home.slice(home.indexOf('id="home-empty-card"'));
    expect(mine.match(/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/gu) ?? []).toEqual([]);
  });

  test("icon-only and icon-first buttons have accessible names, and icons are hidden from assistive tech", () => {
    expect(home).toContain('id="btn-rescan" class="icon-btn" title="Re-scan current tab (⌥⇧R)" aria-label="Re-scan page"');
    for (const m of home.matchAll(/<svg class="ico[^"]*"([^>]*)>/g)) expect(m[1]).toContain('aria-hidden="true"');
  });

  test("buttons are real buttons: no inline handlers (MV3 CSP) and no clickable divs", () => {
    expect(home).not.toMatch(/\son(click|change|input|load)=/i);
    expect(home).not.toMatch(/<(div|span)[^>]*role="button"/);
  });
});

describe("Home tab states", () => {
  test("the tab declares its view, and each state's block starts hidden", () => {
    expect(home).toContain('id="tab-page" class="tab-pane active" data-view="idle"');
    for (const id of ["form-view", "form-extra", "job-area", "home-empty-card"]) {
      expect(home).toMatch(new RegExp(`id="${id}"[^>]*hidden`));
    }
  });

  test("the dashboard carries the profile card, quick actions and shortcut hints", () => {
    const dash = home.slice(home.indexOf('id="home-empty-card"'), home.indexOf('id="form-view"'));
    for (const id of ["home-profile", "profile-pct", "profile-track", "profile-bar", "profile-text", "profile-meta", "btn-profile-next", "btn-empty-rescan", "btn-goto-scraper", "btn-goto-basics", "btn-goto-answers", "answers-label", "home-title", "home-desc"]) {
      expect(dash).toContain(`id="${id}"`);
    }
    expect(dash).toContain("<kbd>⌥⇧R</kbd>");
    expect(dash).toContain("<kbd>⌥⇧F</kbd>");
    expect(dash).toContain('role="progressbar"');
  });

  test("the dashboard and form blocks use classes, not inline style attributes", () => {
    const mine = home.slice(home.indexOf('id="home-empty-card"'));
    expect(mine).not.toContain('style="');
  });

  test("Fill options are a native disclosure holding both toggles, closed by default", () => {
    const start = home.indexOf('<details id="fill-options"');
    const d = home.slice(start, home.indexOf("</details>", start));
    expect(d).not.toMatch(/<details[^>]*\sopen/);
    expect(d).toContain("<summary>Fill options</summary>");
    expect(d).toContain('id="overwrite"');
    expect(d).toContain('id="typeahead"');
  });

  test("Fill is one prominent action with Save and Clear beside it", () => {
    const row = home.slice(home.indexOf('class="fill-row"'), home.indexOf('id="fill-options"'));
    expect(row.indexOf('id="fill"')).toBeLessThan(row.indexOf('id="save"'));
    expect(row.indexOf('id="save"')).toBeLessThan(row.indexOf('id="clear-marks"'));
    expect(row.match(/class="hero-cta"/g)?.length).toBe(1);
    for (const id of ["fill", "save"]) expect(row).toMatch(new RegExp(`id="${id}"[\\s\\S]*?<span class="lbl">`));
  });

  test("every id popup.ts binds at load exists, so no handler throws at start-up", () => {
    const bound = new Set([...ts.matchAll(/\$\(\s*["']#([\w-]+)["']\s*\)\s*\.(?:onclick|onchange|oninput)\s*=/g)].map(m => m[1]!));
    expect(bound.size).toBeGreaterThan(5);
    for (const id of bound) expect(html).toContain(`id="${id}"`);
    for (const id of ["home-title", "home-desc", "home-profile", "form-view", "form-extra", "job-area", "job-actions", "job-track", "missed-card"]) {
      expect(html).toContain(`id="${id}"`);
    }
  });

  test("the resume card is hidden on the dashboard views", () => {
    expect(html).toContain('[data-view="error"]) > #resume { display: none; }');
  });

  test("status is a polite live region with a tone attribute", () => {
    expect(html).toContain('id="status" role="status" aria-live="polite" data-tone="info"');
    for (const tone of ["ok", "warn", "error"]) expect(html).toContain(`#status[data-tone="${tone}"]`);
  });

  test("focus is visible and colour tokens give readable text on the accent", () => {
    expect(html).toContain("button:focus-visible, summary:focus-visible");
    expect(html).toContain("--accent: #047857;"); // white text on it: 5.5:1
  });
});
