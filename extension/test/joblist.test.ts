import { describe, expect, test } from "bun:test";
import {
  actionTitle,
  countLabel,
  drawerSummary,
  filtering,
  jobsToActOn,
  saveLabel,
  scopeNote,
  scorePillText,
  saveSendNote,
  scoreTone,
  selectJobs,
  workplaceLabel,
  companyLine,
  copyActionTitle,
} from "../src/joblist";
import type { ScrapedJob } from "../src/scraper";
import type { RelevanceResult } from "../src/relevance";

function rel(over: Partial<RelevanceResult> = {}): RelevanceResult {
  return {
    source: "estimate",
    score: 50,
    fit: "partial_fit",
    fitLabel: "Partial fit",
    matchedSkills: [],
    missingSkills: [],
    dealbreakers: [],
    highlights: [],
    ...over,
  };
}

function job(over: Partial<ScrapedJob> & { score?: number } = {}): ScrapedJob {
  const { score, ...rest } = over;
  return {
    id: rest.id ?? "j",
    title: rest.title ?? "Engineer",
    company: rest.company ?? "Acme",
    location: rest.location ?? "Austin, TX",
    url: rest.url ?? "https://acme.example/jobs/1",
    snippet: rest.snippet ?? "",
    relevance: rest.relevance ?? rel(score === undefined ? {} : { score }),
    ...rest,
    relevance: rest.relevance ?? rel(score === undefined ? {} : { score }),
  };
}

const none = { query: "", minScore: 0, remoteOnly: false };

describe("selectJobs", () => {
  test("an empty scrape stays empty", () => {
    expect(selectJobs([], none)).toEqual([]);
  });

  test("best score first, and a tie keeps scrape order", () => {
    const jobs = [job({ id: "a", title: "A", score: 40 }), job({ id: "b", title: "B", score: 90 }), job({ id: "c", title: "C", score: 90 })];
    expect(selectJobs(jobs, none).map(j => j.id)).toEqual(["b", "c", "a"]);
  });

  test("the minimum is inclusive, and a non-finite minimum is no minimum", () => {
    const jobs = [job({ id: "low", score: 49 }), job({ id: "edge", score: 50 }), job({ id: "high", score: 85 })];
    expect(selectJobs(jobs, { ...none, minScore: 50 }).map(j => j.id)).toEqual(["high", "edge"]);
    expect(selectJobs(jobs, { ...none, minScore: Number.NaN }).map(j => j.id)).toEqual(["high", "edge", "low"]);
  });

  test("a non-finite or negative score stays on All, last, and drops out of a positive minimum", () => {
    const bad = job({ id: "bad", relevance: rel({ score: Number.NaN }) });
    const neg = job({ id: "neg", relevance: rel({ score: -5 }) });
    const good = job({ id: "good", score: 80 });
    expect(selectJobs([bad, neg, good], none).map(j => j.id)).toEqual(["good", "neg", "bad"]);
    expect(selectJobs([bad, neg, good], { ...none, minScore: 50 }).map(j => j.id)).toEqual(["good"]);
  });

  test("the query matches title, company, location, snippet and skills, ignoring case and surrounding space", () => {
    const j = job({ title: "Rust Engineer", company: "Northwind", location: "Berlin", snippet: "Build the ledger", relevance: rel({ matchedSkills: ["Kafka"] }) });
    for (const q of ["  rust ", "NORTH", "berlin", "LEDGER", "kafka"]) {
      expect(selectJobs([j], { ...none, query: q })).toHaveLength(1);
    }
    expect(selectJobs([j], { ...none, query: "   " })).toHaveLength(1);
    expect(selectJobs([j], { ...none, query: "python" })).toHaveLength(0);
  });

  test("remote follows the workplace type, and an untyped location uses the same negation rules as the scorer", () => {
    const typed = job({ id: "t", location: "United States", relevance: rel({ workplaceType: "Remote" }) });
    const word = job({ id: "w", location: "REMOTE, USA", relevance: rel({ workplaceType: "Unknown" }) });
    const office = job({ id: "o", location: "Austin, TX", relevance: rel({ workplaceType: "On-site" }) });
    const ruledOut = job({ id: "n", location: "Remote work is not available", relevance: rel({ workplaceType: "Unknown" }) });
    const hybrid = job({ id: "h", location: "Remote", relevance: rel({ workplaceType: "Hybrid" }) });
    const fully = job({ id: "f", location: "Fully remote (no hybrid)", relevance: rel({ workplaceType: "Unknown" }) });
    expect(selectJobs([typed, word, office, ruledOut, hybrid, fully], { ...none, remoteOnly: true }).map(j => j.id)).toEqual(["t", "w", "f"]);
  });

  test("a duplicate listing is kept, not collapsed", () => {
    const a = job({ id: "1", title: "Same", score: 70 });
    const b = job({ id: "2", title: "Same", score: 70 });
    expect(selectJobs([a, b], none)).toHaveLength(2);
  });
});

describe("labels", () => {
  test("the count distinguishes nothing scraped, an unfiltered list, and a filter", () => {
    expect(countLabel(0, 0, false)).toBe("Nothing scraped yet");
    expect(countLabel(1, 1, false)).toBe("1 job");
    expect(countLabel(4, 4, false)).toBe("4 jobs");
    expect(countLabel(1, 4, true)).toBe("1 of 4");
    expect(countLabel(0, 4, true)).toBe("0 of 4");
  });

  test("a filter is active only when a control narrows the list", () => {
    expect(filtering(none)).toBe(false);
    expect(filtering({ ...none, query: "  " })).toBe(false);
    expect(filtering({ ...none, query: "go" })).toBe(true);
    expect(filtering({ ...none, minScore: 50 })).toBe(true);
    expect(filtering({ ...none, minScore: Number.NaN })).toBe(false);
    expect(filtering({ ...none, remoteOnly: true })).toBe(true);
  });

  test("the score pill names the source and clamps the number", () => {
    expect(scorePillText(rel({ score: 82 }))).toBe("82% estimate");
    expect(scorePillText(rel({ source: "vani", score: 61.4 }))).toBe("61% relevance");
    expect(scorePillText(rel({ score: 140 }))).toBe("100% estimate");
    expect(scorePillText(rel({ score: Number.NaN }))).toBe("0% estimate");
    expect(scoreTone(80)).toBe("ok");
    expect(scoreTone(60)).toBe("rev");
    expect(scoreTone(59)).toBe("muted");
    expect(scoreTone(Number.NaN)).toBe("muted");
  });

  test("the workplace pill is omitted when the location already states that type", () => {
    expect(workplaceLabel(job({ location: "Remote - US", relevance: rel({ workplaceType: "Remote" }) }))).toBe("");
    expect(workplaceLabel(job({ location: "Austin", relevance: rel({ workplaceType: "Hybrid" }) }))).toBe("Hybrid");
    expect(workplaceLabel(job({ location: "", relevance: rel({ workplaceType: "Unknown" }) }))).toBe("");
    expect(workplaceLabel(job({ location: "remote work is not available", relevance: rel({ workplaceType: "Remote" }) }))).toBe("Remote");
    expect(workplaceLabel(job({ location: "non-remote", relevance: rel({ workplaceType: "Remote" }) }))).toBe("Remote");
    expect(workplaceLabel(job({ location: "Hybrid", relevance: rel({ workplaceType: "Hybrid" }) }))).toBe("");
    expect(workplaceLabel(job({ location: "Austin, TX", relevance: rel({ workplaceType: "On-site" }) }))).toBe("On-site");
  });

  test("a blank company is named, and copy names only the fields it will include", () => {
    expect(companyLine("")).toBe("Company not listed");
    expect(companyLine("   ")).toBe("Company not listed");
    expect(companyLine("  Acme  ")).toBe("Acme");
    expect(copyActionTitle(true, true)).toBe("Copy the title, company, link and snippet");
    expect(copyActionTitle(false, true)).toBe("Copy the title, company and snippet");
    expect(copyActionTitle(true, false)).toBe("Copy the title, company and link");
    expect(copyActionTitle(false, false)).toBe("Copy the title and company");
  });

  test("save and export follow the jobs on screen", () => {
    const low = job({ id: "low", score: 10 });
    const high = job({ id: "high", score: 90 });
    const jobs = [low, high];
    expect(jobsToActOn(jobs, none).map(j => j.id)).toEqual(["low", "high"]);
    expect(jobsToActOn(jobs, { ...none, minScore: 50 }).map(j => j.id)).toEqual(["high"]);
    expect(jobsToActOn(jobs, { ...none, query: "missing" })).toEqual([]);
    expect(saveLabel(2, 10, true)).toBe("Save 2 shown");
    expect(saveLabel(1, 10, true)).toBe("Save 1 shown");
    expect(saveLabel(10, 10, false)).toBe("Save to Vani");
    expect(actionTitle("save", 2, 10, true)).toBe("Send the 2 shown jobs to Vani");
    expect(actionTitle("csv", 10, 10, false)).toBe("Export all 10 scraped jobs as CSV");
    expect(actionTitle("json", 1, 1, false)).toBe("Export the 1 scraped job as JSON");
    expect(scopeNote(2, 10, true)).toBe("Save and export use the 2 shown jobs.");
    expect(scopeNote(1, 10, true)).toBe("Save and export use the 1 shown job.");
    expect(scopeNote(10, 10, false)).toBe("");
    expect(scopeNote(10, 10, true)).toBe("");
    expect(scopeNote(0, 10, true)).toBe("");
    expect(actionTitle("save", 0, 0, false)).toBe("Scrape a page first");
    expect(actionTitle("csv", 0, 10, true)).toBe("Nothing matches these filters");
  });

  test("the drawer says when the preview is short of the page", () => {
    expect(drawerSummary(0, 0)).toBe("No jobs on this page.");
    expect(drawerSummary(1, 1)).toBe("1 job on this page.");
    expect(drawerSummary(3, 3)).toBe("3 jobs on this page.");
    expect(drawerSummary(12, 8)).toBe("Showing 8 of 12. Save below sends all 12.");
    const why = "2 not sent (no usable link or title, or a field too long)";
    expect(drawerSummary(12, 8, 10, why)).toBe(
      "Showing 8 of 12. Save below sends 10 of these 12. 2 not sent (no usable link or title, or a field too long).",
    );
    expect(drawerSummary(12, 12, 10, why)).toBe(
      "12 jobs on this page. Save below sends 10 of these 12. 2 not sent (no usable link or title, or a field too long).",
    );
    expect(drawerSummary(1, 1, 0, "1 not sent (no usable link or title, or a field too long)")).toBe(
      "1 job on this page. Save below cannot send these jobs: 1 not sent (no usable link or title, or a field too long).",
    );
    expect(saveSendNote(3, 3, why)).toBe("");
    expect(saveSendNote(3, 2, "1 not sent (no usable link or title, or a field too long)")).toBe(
      "Save sends 2 of these 3. 1 not sent (no usable link or title, or a field too long).",
    );
    expect(saveSendNote(3, 1, "")).toBe("Save sends 1 of these 3.");
    expect(saveSendNote(0, 0, "")).toBe("");
    expect(saveSendNote(Number.NaN, 1, "x")).toBe("");
  });
});
