import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { decideSubmit, isNotASubmit, looksConfirmed } from "../src/submit";
import { isFinalSubmit } from "../src/applied";

const ready = {
  armed: true,
  missed: 0,
  finals: ["Submit application"],
  writes: [{ label: "City", held: true }],
  cover: "absent" as const,
  unconfirmed: false,
};

describe("submit this page", () => {
  test("is off, and a miss, several controls, a mismatch, or a missing letter hold it", () => {
    expect(decideSubmit({ ...ready, armed: false }).code).toBe("off");
    expect(decideSubmit({ ...ready, missed: null }).code).toBe("audit");
    expect(decideSubmit({ ...ready, missed: 2 }).code).toBe("audit");
    expect(decideSubmit({ ...ready, finals: [] }).detail).toContain("none");
    expect(decideSubmit({ ...ready, finals: ["Submit application", "Send application"] }).detail).toContain("Submit application, Send application");
    expect(decideSubmit({ ...ready, writes: [{ label: "City", held: false }] }).code).toBe("readback");
    expect(decideSubmit({ ...ready, cover: "missing" }).code).toBe("cover");
    expect(decideSubmit(ready)).toEqual({ click: true, label: "Submit application" });
  });

  test("an unconfirmed submit is not clicked again", () => {
    const again = decideSubmit({ ...ready, unconfirmed: true });
    expect(again.click).toBe(false);
    if (!again.click) expect(again.code).toBe("unconfirmed");
  });

  test("Next, Continue, Save and Review are not submit controls", () => {
    for (const label of ["Next", "Continue", "Save", "Save and continue", "Review", "Review and submit", "Apply"]) {
      expect(isFinalSubmit(label)).toBe(false);
      expect(isNotASubmit(label) || !isFinalSubmit(label)).toBe(true);
    }
    expect(isFinalSubmit("Submit application")).toBe(true);
    expect(isFinalSubmit("Send application")).toBe(true);
  });

  test("a confirmation is visible text, and silence is not a confirmation", () => {
    expect(looksConfirmed("Thank you for your application")).toBe(true);
    expect(looksConfirmed("Your application has been submitted")).toBe(true);
    expect(looksConfirmed("")).toBe(false);
    expect(looksConfirmed("Still on the form")).toBe(false);
  });

  test("Fill, Draft and scan do not call submit", () => {
    const content = readFileSync(join(import.meta.dir, "../src/content.ts"), "utf8");
    const fill = content.slice(content.indexOf("async function performFill"), content.indexOf("function syncUndo"));
    const draft = content.slice(content.indexOf("async function startDraft"), content.indexOf("async function draftJob"));
    const scan = content.slice(content.indexOf("async function scan"), content.indexOf("function observeDom"));
    for (const block of [fill, draft, scan]) {
      expect(block).not.toContain("submitThisPage");
      expect(block).not.toContain("requestSubmit");
      expect(block).not.toContain('type: "submitPage"');
    }
    const popup = readFileSync(join(import.meta.dir, "../src/popup.ts"), "utf8");
    expect(popup).toContain('type: "submitPage"');
    expect(popup).toContain("armed: true");
    const html = readFileSync(join(import.meta.dir, "../static/popup.html"), "utf8");
    expect(html).toContain('id="submit-arm"');
    expect(html).toContain('id="submit-page"');
    expect(html).toMatch(/id="submit-page"[^>]*disabled/);
  });
});
