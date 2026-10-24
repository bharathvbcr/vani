import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

function between(src: string, start: string, end: string): string {
  const a = src.indexOf(start);
  const b = src.indexOf(end, a + start.length);
  if (a < 0 || b < 0) throw new Error(`missing slice ${start} .. ${end}`);
  return src.slice(a, b);
}

describe("the draft panel stays in step with the page", () => {
  const content = readFileSync(join(import.meta.dir, "../src/content.ts"), "utf8");
  const popup = readFileSync(join(import.meta.dir, "../src/popup.ts"), "utf8");

  test("opening a draft shows the full panel, and minimizing does not abort it", () => {
    const start = between(content, "async function startDraft(", "async function draftJob(");
    expect(start).toContain('classList.remove("minimized")');
    const minAt = content.indexOf("minBtn.addEventListener");
    const min = content.slice(minAt, content.indexOf("minView.addEventListener", minAt));
    expect(min).toContain('classList.add("minimized")');
    expect(min).not.toContain("draftEpoch");
    expect(min).not.toContain("dismissPanel");
  });

  test("a tailored insert and a one-field save rescan, and the header Save stays page-wide", () => {
    const save = between(content, "async function saveOne(", "function beginDraft(");
    expect(save.indexOf("tracker.noteSent")).toBeGreaterThan(0);
    expect(save.indexOf("tracker.noteSent")).toBeLessThan(save.indexOf("void scheduleScan()"));
    expect(save).not.toContain("tracker.flush");
    const header = content.slice(content.indexOf('panel.querySelector(".s")'), content.indexOf('panel.querySelector(".c")'));
    expect(header).toContain('capture("saved")');
    expect(header).not.toContain("saveOne");

    const tailorAt = content.indexOf('btn("d", "Tailor to JD")');
    const tailor = content.slice(tailorAt, content.indexOf("row.append(tb)", tailorAt));
    const inserted = tailor.indexOf("Inserted tailored answer");
    const rescanned = tailor.indexOf("void scheduleScan()");
    expect(inserted).toBeGreaterThan(0);
    expect(rescanned).toBeGreaterThan(inserted);
    expect(tailor.indexOf("overwrite = true")).toBeLessThan(inserted);

    const handler = between(content, "insertTailored:", "resumeNoted:");
    const ok = handler.lastIndexOf("return { ok: true }");
    expect(handler.lastIndexOf("void scheduleScan()")).toBeGreaterThan(handler.indexOf("insertDraft"));
    expect(handler.lastIndexOf("void scheduleScan()")).toBeLessThan(ok);
  });

  test("the popup redraws coverage after a tailored insert and not after a refused replace", () => {
    const block = between(popup, 'type: "insertTailored"', "if (q.note !== UNDRAFTED_NOTE)");
    const success = block.indexOf("a.reply.ok === true");
    const redraw = block.indexOf("renderCoverage(scanned.tabCoverage.total)");
    const refused = block.indexOf('reply.reason === "has_value"');
    expect(success).toBeGreaterThan(0);
    expect(redraw).toBeGreaterThan(success);
    expect(refused).toBeGreaterThan(redraw);
  });
});
