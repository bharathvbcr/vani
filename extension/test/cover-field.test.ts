import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { isCoverField, isCoverText, parseCoverGet } from "../src/cover";
import { isResumeField } from "../src/resume";
import type { FieldInfo } from "../src/types";

const field = (label: string, name = "", automationId = "", kind: FieldInfo["kind"] = "file"): Pick<FieldInfo, "kind" | "label" | "name" | "automationId"> =>
  ({ kind, label, name, automationId });

describe("cover letter fields", () => {
  test("a cover letter file is not a resume, and a combined box is neither", () => {
    expect(isCoverField(field("Cover letter"))).toBe(true);
    expect(isResumeField(field("Cover letter"))).toBe(false);
    expect(isCoverField(field("Resume"))).toBe(false);
    expect(isResumeField(field("Resume"))).toBe(true);
    expect(isCoverField(field("Resume or cover letter"))).toBe(false);
    expect(isResumeField(field("Resume or cover letter"))).toBe(false);
    expect(isCoverField(field("Cover letter or transcript"))).toBe(false);
    expect(isCoverField(field("Other cover letter"))).toBe(false);
    expect(isCoverField(field("CV"))).toBe(false);
    expect(isCoverText(field("Cover letter", "", "", "textarea"))).toBe(true);
    expect(isCoverText(field("Cover letter", "", "", "file"))).toBe(false);
    expect(isCoverField(field(""))).toBe(false);
  });

  test("a missing or damaged letter is no_cover or unavailable, never a resume", () => {
    const missing = parseCoverGet({ found: false });
    const down = parseCoverGet({ error: "down", code: "unreachable" });
    const blank = parseCoverGet({ found: true, body: "  " });
    const none = parseCoverGet(null);
    expect(missing.ok).toBe(false);
    expect(down.ok).toBe(false);
    expect(blank.ok).toBe(false);
    expect(none.ok).toBe(false);
    if (!missing.ok) expect(missing.reason).toBe("no_cover");
    if (!down.ok) expect(down.reason).toBe("cover_unavailable");
    if (!blank.ok) expect(blank.reason).toBe("no_cover");
    if (!none.ok) expect(none.reason).toBe("cover_unavailable");
  });

  test("fill asks for the cover letter before the resume and does not give a cover field the resume", () => {
    const fill = readFileSync(join(import.meta.dir, "../src/fill.ts"), "utf8");
    const one = fill.slice(fill.indexOf("const one = async"), fill.indexOf("for (const wave"));
    const coverStart = one.indexOf('kind === "file" && isCoverField');
    const resumeStart = one.indexOf('kind === "file" && getResume && isResumeField');
    expect(coverStart).toBeGreaterThanOrEqual(0);
    expect(resumeStart).toBeGreaterThan(coverStart);
    const coverBranch = one.slice(coverStart, resumeStart);
    expect(coverBranch).not.toContain("getResume");
    expect(coverBranch).toContain("attachCover");
    expect(coverBranch).toContain('reason: "no_cover"');
    const textStart = one.indexOf("isCoverText");
    const textEnd = one.indexOf('kind === "file" ||', textStart);
    const textBranch = one.slice(textStart, textEnd);
    expect(textBranch).toContain("insertCoverBody");
    expect(textBranch).not.toContain("getResume");
  });
});
