import { describe, expect, test } from "bun:test";
import { writeFileSync } from "node:fs";
import { tokens } from "../src/normalize";
import vectors from "./vectors/tokens.json";

// tokens() is replayed in Go (internal/semantic/lexveto_test.go) to veto "likely"
// merges of labels one word apart. Change tokens() and this file fails; so does the Go test
// until the vectors are regenerated: UPDATE_VECTORS=1 bun test extension/test/tokens.test.ts

const sorted = (label: string) => [...tokens(label)].sort();

if (process.env.UPDATE_VECTORS === "1") {
  const next = { comment: vectors.comment, cases: vectors.cases.map(c => ({ label: c.label, tokens: sorted(c.label) })) };
  writeFileSync(new URL("./vectors/tokens.json", import.meta.url), JSON.stringify(next, null, 1) + "\n");
}

describe("tokens vectors (shared with Go internal/semantic)", () => {
  test("every vector matches", () => {
    expect(vectors.cases.length).toBeGreaterThanOrEqual(200);
    for (const c of vectors.cases) expect(sorted(c.label)).toEqual(c.tokens);
  });
  test("the file covers the awkward shapes", () => {
    expect(vectors.cases.some(c => c.tokens.length === 0)).toBe(true); // stopword-only and empty labels
    expect(vectors.cases.some(c => c.label.length > 200)).toBe(true);
    expect(vectors.cases.some(c => /[^\x00-\x7f]/.test(c.label))).toBe(true);
    expect(vectors.cases.some(c => c.tokens.includes("usa"))).toBe(true); // phrase folding
  });
});
