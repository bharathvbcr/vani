// The template generator used to invent a biography. An empty profile does not
// contain experience, a date, a salary, or a work-authorization status, so none
// of those may come back as the applicant's words.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { generateTailoredDraft, matchTailorableQuestion, openAnswerFill, OPEN_ANSWER_HOLD, type TailoredKind } from "../src/tailor";
import type { JobInfo } from "../src/draft";

const KINDS: readonly TailoredKind[] = [
  "why_company",
  "why_role",
  "role_fit",
  "project_experience",
  "cover_letter",
  "salary_expectations",
  "start_date",
  "work_auth_explanation",
  "remote_collaboration",
  "diversity_perspective",
];

const job: JobInfo = {
  title: "Engineer",
  company: "Northwind",
  description: "Requirements: Kubernetes. Ship reliable services.",
};

const LABELS: Record<TailoredKind, string> = {
  why_company: "Why Northwind?",
  why_role: "Why this role?",
  role_fit: "What makes you a good fit?",
  project_experience: "Describe a project you built",
  cover_letter: "Cover letter",
  salary_expectations: "What are your salary expectations?",
  start_date: "When is your earliest start date?",
  work_auth_explanation: "Please specify your visa sponsorship status and work authorization",
  remote_collaboration: "How do you manage async communication and remote collaboration?",
  diversity_perspective: "How do you contribute to diversity in engineering?",
};

function submitControls(html: string): string[] {
  const out: string[] = [];
  const tagged = /<button\b[^>]*\btype=["']submit["'][^>]*>[^<]{0,80}<\/button>|<input\b[^>]*\btype=["']submit["'][^>]*>|<button\b[^>]*>[^<]{0,40}\b(?:submit|send)\b[^<]{0,40}<\/button>/gi;
  for (const m of html.matchAll(tagged)) out.push(m[0]);
  return out;
}

describe("invented open answers are not the applicant's words", () => {
  test("an empty profile does not become experience, work authorization, a date, or a salary", () => {
    for (const kind of KINDS) {
      const text = generateTailoredDraft(kind, job, {});
      expect(text, kind).not.toMatch(/experience|authorized|visa|sponsorship|notice period|salary|compensation|\$|architected|i have|i am|years|two-week|diverse viewpoint|asynchronous/i);
      expect(text, kind).toBe("");
      const match = matchTailorableQuestion(LABELS[kind], job.company);
      expect(match?.kind, kind).toBe(kind);
      expect(match?.templateSnippet, kind).toBe("");
    }
  });

  test("each open answer is an unfilled hold with a visible reason", () => {
    for (const kind of KINDS) {
      const offer = openAnswerFill({ label: LABELS[kind], companyName: job.company });
      expect(offer.filled, kind).toBe(false);
      expect(offer.text, kind).toBe("");
      expect(offer.reason, kind).toBe(OPEN_ANSWER_HOLD);
      expect(offer.reason ?? "", kind).not.toMatch(/\bI (have|am)\b/);
      expect(offer.reason ?? "", kind).not.toMatch(/authorized to work|visa sponsorship|\$\d/);
      expect(offer.reason ?? "").toContain("left this blank");
    }
  });

  test("a stored answer fills, a grounded draft fills, and the stored answer wins", () => {
    const storedOnly = openAnswerFill({ label: "Cover letter", stored: "  shipped the tracing pipeline  " });
    expect(storedOnly).toEqual({ filled: true, text: "shipped the tracing pipeline", source: "stored" });
    const groundedOnly = openAnswerFill({ label: "Cover letter", stored: "   ", groundedDraft: "Cut latency by 42%." });
    expect(groundedOnly).toEqual({ filled: true, text: "Cut latency by 42%.", source: "grounded_draft" });
    const storedWins = openAnswerFill({
      label: "Cover letter",
      stored: "saved line",
      groundedDraft: "grounded line",
    });
    expect(storedWins).toEqual({ filled: true, text: "saved line", source: "stored" });
  });

  test("submit and send controls on the form fixtures are not clicked or filled", () => {
    const dir = import.meta.dir;
    const greenhouse = readFileSync(join(dir, "fixtures/greenhouse-anthropic-2026-10.html"), "utf8");
    const revolut = readFileSync(join(dir, "fixtures/revolutpeople-biorce-2026-10.html"), "utf8");
    const greenhouseControls = submitControls(greenhouse);
    expect(greenhouseControls.length).toBeGreaterThan(0);
    expect(greenhouseControls.some(c => /submit application/i.test(c))).toBe(true);
    expect(submitControls(revolut)).toEqual([]);
    const content = readFileSync(join(dir, "../src/content.ts"), "utf8");
    const popup = readFileSync(join(dir, "../src/popup.ts"), "utf8");
    const fill = readFileSync(join(dir, "../src/fill.ts"), "utf8");
    const tailorAt = content.indexOf("openAnswerFill(");
    const tailor = content.slice(tailorAt, content.indexOf("row.append(tb)", tailorAt));
    expect(tailor.indexOf("toast(")).toBeLessThan(tailor.indexOf("insertDraft"));
    expect(tailor).not.toContain("requestSubmit");
    expect(tailor).not.toContain(".submit(");
    expect(tailor).not.toContain(".click(");
    expect(popup).not.toContain('type: "insertTailored"');
    expect(popup).not.toContain("generateTailoredDraft");
    expect(fill).not.toContain("requestSubmit");
    expect(fill).not.toContain('type="submit"');
    for (const control of greenhouseControls) {
      for (const kind of KINDS) {
        const offer = openAnswerFill({ label: LABELS[kind], companyName: "Northwind" });
        expect(offer.filled).toBe(false);
        expect(offer.text).toBe("");
        expect(control.includes("Submit")).toBe(true);
        expect(offer.text).not.toBe("Submit application");
      }
    }
  });
});
