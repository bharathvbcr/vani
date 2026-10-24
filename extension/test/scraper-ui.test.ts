import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// The Jobs tab: markup the script binds, and the empty states kept outside the list
// so a render cannot throw them away (they used to live inside #scraper-list).
const html = readFileSync(join(import.meta.dir, "../static/popup.html"), "utf-8");
const popup = readFileSync(join(import.meta.dir, "../src/popup.ts"), "utf-8");
const content = readFileSync(join(import.meta.dir, "../src/content.ts"), "utf-8");
const start = html.indexOf('<section id="tab-scraper"');
const end = html.indexOf('<section id="tab-basics"');
const pane = html.slice(start, end);

describe("Jobs tab", () => {
  test("keeps the controls the script binds, with the scrape label in a span busy() can swap", () => {
    for (const id of [
      "scrape-page-btn", "scraper-save", "scraper-save-search", "export-csv", "export-json",
      "scraper-save-result", "scraper-scope", "scraper-filter", "scraper-remote-only", "scraper-count",
      "scraper-list", "scraper-empty", "scraper-nomatch", "scraper-clear-filters",
    ]) {
      expect(pane).toContain(`id="${id}"`);
    }
    expect(pane).toContain('<span class="lbl">Scrape this page</span>');
    expect(pane).toContain('<span class="lbl">Save to Vani</span>');
    for (const min of ["0", "50", "70", "85"]) expect(pane).toContain(`data-min="${min}"`);
  });

  test("empty states are siblings of the list, and a filtered miss starts hidden", () => {
    expect(pane).toMatch(/id="scraper-list"[^>]*><\/div>\s*<div id="scraper-empty"/);
    expect(pane).toContain('id="scraper-nomatch" class="empty-state" hidden');
    expect(pane).not.toContain("scraper-score-filter");
  });

  test("uses the icon set and no inline style or emoji", () => {
    expect(pane).not.toContain('style="');
    expect(pane.match(/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/gu) ?? []).toEqual([]);
    for (const m of pane.matchAll(/<svg class="ico[^"]*"([^>]*)>/g)) expect(m[1]).toContain('aria-hidden="true"');
  });

  test("save and export start disabled, and the score pills come from joblist", () => {
    expect(pane).toMatch(/id="scraper-save"[^>]*disabled/);
    expect(pane).toMatch(/id="export-csv"[^>]*disabled/);
    expect(pane).toMatch(/id="export-json"[^>]*disabled/);
    expect(popup).toContain("selectJobs");
    expect(popup).toContain("jobsToActOn");
    expect(popup).toContain("scorePillText");
    expect(popup).toContain("companyLine(j.company)");
    expect(popup).toContain("copyActionTitle(isHttpUrl(j.url), j.snippet.trim() !== \"\")");
    expect(popup).toContain("saveSendNote");
    expect(popup).toContain("(u, t, f) => window.open(u, t, f)");
    expect(popup).not.toContain("scraper-score-filter");
    expect(popup).not.toContain("% Match");
    expect(popup).toContain('out.textContent = ""');
  });

  test("dealbreakers are not clamped, and only one toolbar button is primary once jobs exist", () => {
    expect(html).not.toMatch(/\.job-fit, \.job-snippet, \.job-flag/);
    expect(html).toContain(".job-flag");
    expect(popup).toContain('scrape.classList.toggle("primary", total === 0)');
  });

  test("the in-page drawer scrolls the preview and keeps Save on screen", () => {
    expect(content).toContain('list.className = "sjobs"');
    expect(content).toContain('saveRow.className = "ssave"');
    expect(content).toContain('drawer.style.display = "flex"');
    expect(content).toContain("#scrape-drawer{max-height:320px;display:flex;flex-direction:column;overflow:hidden}");
    expect(content).toContain(".sjobs{");
    expect(content).toContain(".pill.act{cursor:pointer}");
    expect(content).not.toMatch(/\.pill\{[^}]*cursor:pointer/);
    expect(content).toContain('className = `pill act ${scoreTone(rel.score)}`');
    expect(content).toContain("drawerSummary(res.jobs.length, preview.length");
    expect(content).not.toContain("Math.max(res.totalFound");
    expect(content).toContain("companyLine(j.company)");
    expect(content).toContain('result.textContent = "Sending..."');
    expect(content).toContain(".shead,.sjob,.ssave{user-select:text}");
    const drawerTag = content.slice(content.indexOf('id="scrape-drawer"'), content.indexOf('id="scrape-drawer"') + 280);
    expect(drawerTag).not.toContain("overflow");
  });
});
