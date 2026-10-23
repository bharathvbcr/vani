import { describe, expect, test } from "bun:test";
import { FIT_LABELS } from "../src/jobrow";
import { DEFAULT_PROFILE, evaluateRelevance, rankListing } from "../src/relevance";

describe("the offline estimate speaks Vani's vocabulary", () => {
  const titles = [
    ["Senior Backend Engineer (Go / Rust)", "Go, Rust, Kubernetes and distributed systems on GCP. Remote."],
    ["Software Engineer", "Some services work."],
    ["Platform Engineer", "Kubernetes, Docker and Linux."],
    ["Product Manager", "Roadmaps."],
    ["Software Engineering Recruiter", "Hiring."],
  ] as const;

  test("fit is always one of Vani's five options, never good_fit", () => {
    for (const [title, description] of titles) {
      const r = evaluateRelevance({ title, description }, DEFAULT_PROFILE);
      expect(Object.keys(FIT_LABELS)).toContain(r.fit);
      expect(r.source).toBe("estimate");
    }
  });

  test("every label says it is an estimate and starts with Vani's own fit words", () => {
    for (const [title, description] of titles) {
      const r = evaluateRelevance({ title, description }, DEFAULT_PROFILE);
      expect(r.fitLabel.startsWith(FIT_LABELS[r.fit]!)).toBe(true);
      expect(r.fitLabel).toMatch(/\(estimate \d+%\)$/);
    }
  });
});

describe("rankListing orders a board's rows before any description exists", () => {
  const profile = { targetTitles: ["software engineer"], skills: ["kubernetes", "docker"] };

  test("a title that matches outranks one that does not", () => {
    expect(rankListing({ title: "Software Engineer", location: "Austin, TX" }, profile))
      .toBeGreaterThan(rankListing({ title: "Office Coordinator", location: "Austin, TX" }, profile));
  });

  test("words in the location are not read as the posting's skills", () => {
    const plain = rankListing({ title: "Office Coordinator", location: "Austin, TX" }, profile);
    const skillsy = rankListing({ title: "Office Coordinator", location: "Kubernetes Docker Linux Campus" }, profile);
    expect(skillsy).toBe(plain);
  });

  test("an empty or hostile row does not throw", () => {
    expect(Number.isFinite(rankListing({ title: "", location: "" }, profile))).toBe(true);
    expect(Number.isFinite(rankListing({ title: "x".repeat(100_000), location: "y".repeat(100_000) }, profile))).toBe(true);
  });
});
