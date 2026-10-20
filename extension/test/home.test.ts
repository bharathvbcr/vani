import { describe, expect, test } from "bun:test";
import {
  basicsCompleteness, bankSummary, compactCount, friendlyError, homeHeadline, homeState, looksLikeJob, meterModel, nextStep, viewSections,
} from "../src/home";
import { BASICS, basicsFields } from "../src/basics";
import { BASICS_HOST, mergeCapture } from "../src/bank";
import { emptyBank } from "../src/types";
import type { Bank } from "../src/types";

const longDesc = "We are hiring a backend engineer to build distributed systems in Go and Rust.";

function bankWith(values: Record<string, string>): Bank {
  return mergeCapture(emptyBank(), basicsFields(values), BASICS_HOST, new Date("2026-10-19T12:00:00Z")).bank;
}

describe("homeState", () => {
  test("a page with fields is a form, whatever else it carries", () => {
    expect(homeState({ found: 3, job: { title: "SWE", description: longDesc }, portal: { isATS: true } })).toBe("form");
  });
  test("no fields but a posting on an ATS or job board is a job", () => {
    expect(homeState({ found: 0, job: { title: "SWE", description: "" }, portal: { isATS: true } })).toBe("job");
    expect(homeState({ found: 0, job: { title: "", description: longDesc }, portal: { isJobBoard: true } })).toBe("job");
  });
  test("an unknown site needs a positive score and a real description to count as a job", () => {
    expect(homeState({ found: 0, job: { title: "x", description: longDesc }, relevance: { score: 55 } })).toBe("job");
    expect(homeState({ found: 0, job: { title: "x", description: longDesc }, relevance: { score: 0 } })).toBe("idle");
    expect(homeState({ found: 0, job: { title: "x", description: "short" }, relevance: { score: 90 } })).toBe("idle");
  });
  test("an ATS page with nothing readable is idle, not an empty job card", () => {
    expect(homeState({ found: 0, job: { title: "  ", description: "" }, portal: { isATS: true } })).toBe("idle");
    expect(homeState({ found: 0, job: undefined, portal: { isATS: true } })).toBe("idle");
    expect(homeState({ found: 0, job: null, portal: null, relevance: null })).toBe("idle");
  });
  test("error and disabled win over everything", () => {
    expect(homeState({ error: true, found: 5, disabled: true })).toBe("error");
    expect(homeState({ disabled: true, found: 5 })).toBe("disabled");
  });
  test("malformed input never throws and falls to idle", () => {
    expect(homeState({ found: NaN })).toBe("idle");
    expect(homeState({ found: -4 })).toBe("idle");
    expect(homeState({ found: 0, job: { title: 7, description: {} }, relevance: { score: "high" }, portal: { isATS: true } })).toBe("idle");
    expect(looksLikeJob({ job: { title: "t", description: null }, relevance: { score: Infinity } })).toBe(false);
    expect(homeState({ found: Number.MAX_SAFE_INTEGER })).toBe("form");
  });
  test("sections follow the view", () => {
    expect(viewSections("form")).toEqual({ form: true, job: true, dashboard: false });
    expect(viewSections("job")).toEqual({ form: false, job: true, dashboard: false });
    for (const v of ["idle", "disabled", "error"] as const) expect(viewSections(v)).toEqual({ form: false, job: false, dashboard: true });
  });
  test("headlines name the host when disabled and never show an empty one", () => {
    expect(homeHeadline("disabled", "x.com").desc).toContain("x.com");
    expect(homeHeadline("disabled", "").desc).toContain("this site");
    expect(homeHeadline("error", "").title.length).toBeGreaterThan(0);
    expect(homeHeadline("idle", "").title).toBe("No application form here");
  });
});

describe("meterModel", () => {
  const base = { found: 0, fillable: 0, manual: 0, unknown: 0, capped: false };
  test("no fields: zero everything", () => {
    const m = meterModel(base);
    expect([m.okPct, m.revPct, m.unkPct, m.readyPct]).toEqual([0, 0, 0, 0]);
    expect(m.tone).toBe("muted");
    expect(m.subtitle).toBe("No form fields detected on this tab");
  });
  test("a mixed form", () => {
    const m = meterModel({ ...base, found: 10, fillable: 6, manual: 2, unknown: 2 });
    expect([m.okPct, m.revPct, m.unkPct, m.readyPct]).toEqual([60, 20, 20, 80]);
    expect(m.badge).toBe("80%");
    expect(m.tone).toBe("ok");
    expect(m.subtitle).toBe("6 of 10 fields ready to auto-fill");
  });
  test("under 70 percent is amber", () => {
    expect(meterModel({ ...base, found: 10, fillable: 2, manual: 1, unknown: 7 }).tone).toBe("rev");
  });
  test("the bars never exceed 100 even if the counts overlap or are inconsistent", () => {
    const m = meterModel({ ...base, found: 3, fillable: 3, manual: 3, unknown: 3 });
    expect(m.okPct + m.revPct + m.unkPct).toBeLessThanOrEqual(100);
    expect(m.readyPct).toBeLessThanOrEqual(100);
  });
  test("NaN, negative and huge counts are tamed", () => {
    const m = meterModel({ found: NaN, fillable: -1, manual: Infinity, unknown: 5, capped: false });
    expect(m.okPct + m.revPct + m.unkPct + m.readyPct).toBe(0);
    const huge = meterModel({ ...base, found: 2_000_000, fillable: 1_000_000, manual: 0, unknown: 1_000_000 });
    expect(huge.subtitle).toBe("1m+ of 1m+ fields ready to auto-fill");
    expect(huge.okPct).toBe(50);
  });
  test("a capped scan says it only saw the first fields", () => {
    expect(meterModel({ ...base, found: 40, fillable: 40, capped: true }).subtitle).toContain("first fields only");
  });
});

describe("compactCount", () => {
  test("small, thousands, and absurd", () => {
    expect([0, 7, 999, 1000, 1250, 9999, 10_000, 123_456, 1_000_000, 5e12].map(compactCount)).toEqual(["0", "7", "999", "1k", "1.3k", "10k", "10k", "123k", "1m+", "1m+"]);
  });
  test("non-numbers read as 0", () => {
    for (const x of [NaN, -3, Infinity, "9", null, undefined, {}]) expect(compactCount(x)).toBe("0");
  });
});

describe("basicsCompleteness / bankSummary / nextStep", () => {
  test("empty, null and malformed banks are 0% with every contact basic missing", () => {
    for (const b of [null, undefined, emptyBank(), { answers: null } as unknown as Bank, { answers: [] } as unknown as Bank, "x" as unknown as Bank]) {
      const c = basicsCompleteness(b);
      expect(c.filled).toBe(0);
      expect(c.pct).toBe(0);
      expect(c.total).toBeGreaterThan(0);
      expect(c.missingContact).toContain("Email");
    }
  });
  test("self-ID basics are not counted while self-ID capture is off", () => {
    const off = basicsCompleteness(emptyBank());
    const on = basicsCompleteness({ ...emptyBank(), settings: { captureSelfId: true, disabledHosts: [] } });
    expect(on.total).toBeGreaterThan(off.total);
    expect(off.total).toBe(BASICS.filter(b => b.group !== "self_id").length);
  });
  test("saved basics raise the percentage and leave the missing list", () => {
    const c = basicsCompleteness(bankWith({ first: "Ada", last: "Lovelace", email: "ada@example.com" }));
    expect(c.filled).toBe(3);
    expect(c.pct).toBe(Math.round((3 / c.total) * 100));
    expect(c.missingContact).not.toContain("Email");
    expect(c.missingContact).toContain("Phone");
  });
  test("a whitespace-only value is not an answer", () => {
    const b = bankWith({ first: "Ada" });
    b.answers["first name"]!.value = ["   "];
    expect(basicsCompleteness(b).filled).toBe(0);
  });
  test("answers under non-basic keys do not count toward the profile", () => {
    const b = emptyBank();
    b.answers["favourite colour"] = { key: "favourite colour", labels: ["Favourite colour"], kind: "text", value: ["green"], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01T00:00:00Z", review: false };
    expect(basicsCompleteness(b).filled).toBe(0);
    expect(bankSummary(b)).toEqual({ answers: 1, review: 0 });
  });
  test("bankSummary counts review answers and survives junk", () => {
    const b = emptyBank();
    for (let i = 0; i < 2000; i++) b.answers[`q${i}`] = { key: `q${i}`, labels: [], kind: "text", value: ["v"], sensitivity: "standard", signatures: [], uses: 0, fills: 0, updatedAt: "", review: i % 2 === 0 };
    expect(bankSummary(b)).toEqual({ answers: 2000, review: 1000 });
    expect(bankSummary(null)).toEqual({ answers: 0, review: 0 });
    expect(bankSummary({ answers: { a: null } } as unknown as Bank)).toEqual({ answers: 1, review: 0 });
  });
  test("next step: finish basics first, then review, else nothing", () => {
    const empty = nextStep(basicsCompleteness(null), bankSummary(null))!;
    expect(empty.tab).toBe("basics");
    expect(empty.text).toContain("Missing: First name, Last name, Email and 6 more.");
    const one = nextStep({ filled: 8, total: 9, pct: 89, missingContact: ["LinkedIn"] }, { answers: 3, review: 0 })!;
    expect(one.text).toBe("Missing: LinkedIn. Fill uses them on every application.");
    const review = nextStep({ filled: 9, total: 9, pct: 100, missingContact: [] }, { answers: 5, review: 1 })!;
    expect(review.tab).toBe("answers");
    expect(review.text).toContain("1 saved answer is");
    expect(nextStep({ filled: 9, total: 9, pct: 100, missingContact: [] }, { answers: 5, review: 0 })).toBeNull();
  });
});

describe("friendlyError", () => {
  test("the screenshot's exception reads as a plain line and keeps the raw text", () => {
    const f = friendlyError(new TypeError("Cannot read properties of undefined (reading 'query')"));
    expect(f.line).toBe("Vani can't reach the current tab. Open it from the toolbar on a normal web page.");
    expect(f.detail).toBe("Cannot read properties of undefined (reading 'query')");
  });
  test("known chrome errors", () => {
    expect(friendlyError(new Error("Could not establish connection. Receiving end does not exist.")).line).toContain("Reload the tab");
    expect(friendlyError("Extension context invalidated.").line).toContain("updated");
    expect(friendlyError(new Error("Cannot access a chrome:// URL")).line).toContain("doesn't let extensions");
    expect(friendlyError(new Error("scan timed out")).line).toContain("too long");
  });
  test("unknown, empty and non-error values get the fallback", () => {
    for (const x of [undefined, null, "", {}, 42, new Error("")]) {
      const f = friendlyError(x);
      expect(f.line).toBe("Something went wrong reading this page.");
    }
    expect(friendlyError(undefined).detail).toBe("");
    expect(friendlyError({ message: "boom" }).detail).toBe("boom");
  });
  test("the fallback can be named by the caller", () => {
    expect(friendlyError("odd", "Could not save.").line).toBe("Could not save.");
  });
  test("a huge message is bounded and a hostile toString cannot throw", () => {
    expect(friendlyError("x".repeat(100_000)).detail.length).toBe(300);
    const hostile = { toString() { throw new Error("no"); } };
    expect(() => friendlyError(hostile)).not.toThrow();
    expect(friendlyError(hostile).line).toBe("Something went wrong reading this page.");
  });
});
