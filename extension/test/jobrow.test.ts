import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { FIT_LABELS, JOB_ROW_VERSION, applyVaniRow, parseFindRow, parseJobRow, scoreWord, withRowError } from "../src/jobrow";
import { DEFAULT_PROFILE, evaluateRelevance } from "../src/relevance";

// Go's golden rows (internal/jobview/testdata/rows.golden.json, written by
// `go test ./internal/jobview -update`), replayed through the extension's reader.
const golden = JSON.parse(readFileSync(join(import.meta.dir, "../../internal/jobview/testdata/rows.golden.json"), "utf8")) as {
  version: number;
  fit_order: string[];
  fit_labels: Record<string, string>;
  rows: Array<Record<string, unknown>>;
};

const estimate = () => evaluateRelevance({ title: "Backend Engineer", description: "Go and Rust services", location: "Remote" }, DEFAULT_PROFILE);

describe("jobs row contract", () => {
  test("the golden file is at the version this build reads", () => {
    expect(golden.version).toBe(JOB_ROW_VERSION);
    expect(golden.rows.length).toBeGreaterThanOrEqual(5);
  });

  test("every golden row parses", () => {
    for (const r of golden.rows) {
      const p = parseJobRow(r);
      expect(p.ok).toBe(true);
    }
  });

  test("the offline fit vocabulary is Vani's, and good_fit is not in it", () => {
    expect(FIT_LABELS).toEqual(golden.fit_labels);
    expect(new Set(Object.keys(FIT_LABELS))).toEqual(new Set(golden.fit_order));
    expect("good_fit" in FIT_LABELS).toBe(false);
  });

  test("the offline estimate speaks only the five fit labels of Go's label table", () => {
    const labels = JSON.parse(readFileSync(join(import.meta.dir, "../../internal/jobview/testdata/labels.golden.json"), "utf8")) as {
      v: number;
      fit: Array<{ value: string; label: string }>;
    };
    expect(labels.v).toBe(1);
    expect(Object.fromEntries(labels.fit.map((e) => [e.value, e.label]))).toEqual(FIT_LABELS);
    // Every fit the estimate can assign is one of them (an assignment or a RoleFit member).
    const src = readFileSync(join(import.meta.dir, "../src/relevance.ts"), "utf8");
    const used = new Set([...src.matchAll(/\bfit = "([a-z_]+)"/g)].map((m) => m[1]));
    const typed = new Set([...src.slice(src.indexOf("type RoleFit"), src.indexOf(";", src.indexOf("type RoleFit"))).matchAll(/"([a-z_]+)"/g)].map((m) => m[1]));
    expect(typed).toEqual(new Set(labels.fit.map((e) => e.value)));
    for (const u of used) expect(typed.has(u!)).toBe(true);
    expect(used.size).toBeGreaterThan(0);
  });

  test("an unknown schema version is refused by name, never mis-rendered", () => {
    for (const v of [0, 2, 99, -1, "1", null, undefined]) {
      const p = parseJobRow({ ...golden.rows[0], v });
      expect(p.ok).toBe(false);
      if (!p.ok) expect(p.error).toContain("not supported");
    }
  });

  test("malformed rows are refused", () => {
    const base = golden.rows[0]!;
    for (const bad of [null, [], "x", 3, { ...base, key: "" }, { ...base, relevance: "82" }, { ...base, priority: NaN }, { ...base, fit_label: 5 }, { ...base, status_label: undefined }, { ...base, key: "k".repeat(601) }]) {
      expect(parseJobRow(bad).ok).toBe(false);
    }
  });

  test("a jobFind reply yields the row, none, or a loud error", () => {
    const row = golden.rows[0]!;
    expect(parseFindRow({ found: true, job: { key: "k", row } })).toEqual({ row });
    expect(parseFindRow({ found: false })).toEqual({ row: null });
    expect(parseFindRow({ found: true, job: { key: "k" } })).toEqual({ row: null }); // a vani older than the contract
    expect(parseFindRow(undefined)).toEqual({ row: null });
    const r = parseFindRow({ found: true, job: { key: "k", row: { ...row, v: 2 } } });
    expect("error" in r && r.error).toContain("not supported");
  });

  test("Vani's row replaces the estimate's score and label, and the screen says whose it is", () => {
    const row = parseJobRow(golden.rows[0]);
    if (!row.ok) throw new Error(row.error);
    const rel = estimate();
    expect(rel.source).toBe("estimate");
    expect(scoreWord(rel)).toBe("Estimate");
    const v = applyVaniRow(rel, row.row);
    expect(v.source).toBe("vani");
    expect(v.score).toBe(82);
    expect(v.fit).toBe("strong_fit");
    expect(v.fitLabel).toBe("Strong fit (priority 71)");
    expect(scoreWord(v)).toBe("Relevance");
    expect(v.matchedSkills).toEqual(rel.matchedSkills); // the page's own details stay
  });

  test("a row with no fit reading shows its status, not the estimate's fit", () => {
    const row = parseJobRow(golden.rows[2]); // dead, no fit
    if (!row.ok) throw new Error(row.error);
    const v = applyVaniRow(estimate(), row.row);
    expect(v.fitLabel).toBe("Closed (priority 6)");
  });

  test("an unreadable row leaves the estimate, labelled, with the reason", () => {
    const v = withRowError(estimate(), "schema version 2 is not supported");
    expect(v.source).toBe("estimate");
    expect(v.fitLabel).toContain("Vani row not shown: schema version 2 is not supported");
  });
});
