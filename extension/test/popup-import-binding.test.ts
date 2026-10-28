// MV3 CSP drops inline handlers. The import control is a hidden file input (#import);
// #import-btn only opens it from script. These checks read the files on disk — no browser.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const html = readFileSync(join(import.meta.dir, "../static/popup.html"), "utf8");
const ts = readFileSync(join(import.meta.dir, "../src/popup.ts"), "utf8");

const INLINE = /onclick|onload|onerror|javascript:/i;

/** Case-insensitive hits. A case-sensitive `includes("onclick")` misses ONCLICK. */
function inlineHits(markup: string): string[] {
  return [...markup.matchAll(/onclick|onload|onerror|javascript:/gi)].map(m => m[0]);
}

function startTag(markup: string, id: string): string | null {
  const m = markup.match(new RegExp(`<(button|input)\\b[^>]*\\bid="${id}"[^>]*>`, "i"));
  return m ? m[0] : null;
}

/** The label element that contains `id`, if any. Labels in this popup are not nested. */
function wrappingLabel(markup: string, id: string): string | null {
  for (const m of markup.matchAll(/<label\b[^>]*>[\s\S]*?<\/label>/gi)) {
    if (m[0].includes(`id="${id}"`)) return m[0];
  }
  return null;
}

describe("import picker binding", () => {
  test("popup.html has no onclick, onload, onerror, or javascript: (any case)", () => {
    expect(inlineHits(html)).toEqual([]);
    expect(INLINE.test(html)).toBe(false);
  });

  test("an uppercase ONCLICK fixture fails the case-insensitive check; the real file passes", () => {
    const hostile = [
      '<button type="button" id="import-btn" ONCLICK="document.getElementById(\'import\').click()">Import</button>',
      '<body ONLOAD="steal()">',
      '<img ONERROR="steal()" src="x">',
      '<a href="JAVASCRIPT:alert(1)">x</a>',
    ].join("\n");
    // The pre-existing scan is case-sensitive and would miss every one of these.
    expect(hostile.includes("onclick")).toBe(false);
    expect(hostile.includes("onload")).toBe(false);
    expect(hostile.includes("onerror")).toBe(false);
    expect(hostile.includes("javascript:")).toBe(false);
    expect(inlineHits(hostile).map(s => s.toLowerCase())).toEqual([
      "onclick",
      "onload",
      "onerror",
      "javascript:",
    ]);
    expect(inlineHits(html)).toEqual([]);
  });

  test("#import-btn is type=button and its onclick clicks #import", () => {
    const btn = startTag(html, "import-btn");
    expect(btn).not.toBeNull();
    expect(btn).toMatch(/^<button\b/i);
    expect(btn).toMatch(/\btype="button"/);
    expect(ts.split('$("#import-btn").onclick').length - 1).toBe(1);
    expect(ts).toContain('$("#import-btn").onclick = () => { $("#import").click(); };');
  });

  test("the hidden file input is still #import and its onchange clears input.value", () => {
    const input = startTag(html, "import");
    expect(input).not.toBeNull();
    expect(input).toMatch(/^<input\b/i);
    expect(input).toMatch(/\btype="file"/);
    expect(input).toMatch(/\bstyle="[^"]*display:\s*none/);
    expect(html.match(/\bid="import"/g)).toEqual(['id="import"']);
    expect(html.match(/\bid="import-btn"/g)).toEqual(['id="import-btn"']);

    const start = ts.indexOf('$("#import").onchange = async ev => {');
    expect(start).toBeGreaterThan(-1);
    expect(ts.indexOf('$("#import").onchange', start + 1)).toBe(-1);
    const handler = ts.slice(start, ts.indexOf("buildBasics();", start));
    const clearAt = handler.indexOf('input.value = ""');
    expect(clearAt).toBeGreaterThan(handler.indexOf("input.files"));
    expect(clearAt).toBeLessThan(handler.indexOf("if (!file)"));
    expect(clearAt).toBeLessThan(handler.indexOf("confirm("));
    expect(handler).toContain("replaceBank");
  });

  test("a label around the button is not the only opener, and #export onclick stays assigned", () => {
    const label = wrappingLabel(html, "import-btn");
    expect(label).not.toBeNull();
    expect(label).toContain('id="import"');
    expect(label).toContain("<button");
    // Interactive descendants do not activate the label, so this script line is the opener.
    expect(ts).toContain('$("#import-btn").onclick = () => { $("#import").click(); };');

    const onlyLabel = '<label class="button"><input type="file" id="import" style="display:none"><button type="button" id="import-btn">Import</button></label>';
    const noBinding = '$("#export").onclick = () => { exportBank(current); };\n';
    expect(wrappingLabel(onlyLabel, "import-btn")).not.toBeNull();
    expect(noBinding).not.toContain('$("#import-btn").onclick');
    expect(ts).toContain('$("#import-btn").onclick');

    const exportAt = ts.indexOf('$("#export").onclick = () => {');
    expect(exportAt).toBeGreaterThan(-1);
    expect(ts.indexOf('$("#export").onclick', exportAt + 1)).toBe(-1);
    const exportBody = ts.slice(exportAt, ts.indexOf('$("#import-btn").onclick', exportAt));
    expect(exportBody).toContain("exportBank(current)");
    expect(exportBody).not.toContain('$("#import").click()');
  });
});
