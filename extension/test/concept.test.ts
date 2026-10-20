import { describe, expect, test } from "bun:test";
import { type Answer, type Bank, type FieldInfo, emptyBank } from "../src/types";
import { MAX_ANSWERS, MAX_LABELS, conceptKey, findOwner, mergeCapture, ownerIndex } from "../src/bank";
import { pendingDiff } from "../src/capture";
import vectors from "./vectors/concept.json";

const T0 = new Date("2026-01-01T00:00:00Z");
const T1 = new Date("2026-02-01T00:00:00Z");

function field(label: string, value: string, over: Partial<FieldInfo> = {}): FieldInfo {
  return { kind: "text", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value: [value], ...over };
}

function capture(bank: Bank, fields: FieldInfo[], host = "boards.example.com", now = T0): Bank {
  return mergeCapture(bank, fields, host, now).bank;
}

function row(key: string, over: Partial<Answer> = {}): Answer {
  return { key, labels: [key], kind: "text", value: ["v"], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01T00:00:00.000Z", review: false, ...over };
}

describe("conceptKey vectors (shared with Swift ConceptKeyTests)", () => {
  test("every vector matches", () => {
    expect(vectors.cases.length).toBeGreaterThanOrEqual(60);
    for (const c of vectors.cases) expect(conceptKey(c.label, c.autocomplete)).toBe(c.want);
  });
  test("the file covers the awkward shapes", () => {
    const wants = vectors.cases.map(c => c.want);
    expect(wants).toContain(""); // stopword-only and empty labels
    expect(vectors.cases.some(c => c.label.length > 300)).toBe(true);
    expect(vectors.cases.some(c => /[^\x00-\x7f]/.test(c.label))).toBe(true);
  });
});

describe("one concept, one row", () => {
  test("LinkedIn URL then LinkedIn profile is one row holding both wordings", () => {
    let b = capture(emptyBank(), [field("LinkedIn URL", "https://linkedin.com/in/ada")]);
    b = capture(b, [field("LinkedIn profile", "https://linkedin.com/in/ada2")], "boards.example.com", T1);
    expect(Object.keys(b.answers)).toEqual(["linkedin url"]);
    const a = b.answers["linkedin url"]!;
    expect(a.labels).toEqual(["linkedin url", "linkedin profile"]);
    expect(a.value).toEqual(["https://linkedin.com/in/ada2"]);
    expect(a.uses).toBe(2);
  });

  test("basics 'LinkedIn' absorbs a later 'LinkedIn Profile URL' (the reported case)", () => {
    let b = capture(emptyBank(), [field("LinkedIn", "https://l/in/a")], "vani-basics");
    const r = mergeCapture(b, [field("LinkedIn Profile URL", "https://l/in/b", { automationId: "li_url" })], "greenhouse.io", T1);
    expect(Object.keys(r.bank.answers)).toEqual(["linkedin"]);
    expect(r.added).toBe(0);
    expect(r.updated).toBe(1);
    expect(r.bank.answers["linkedin"]!.signatures).toEqual(["greenhouse.io|li_url"]);
  });

  test("an intent shared across hosts is one row with the latest value", () => {
    let b = capture(emptyBank(), [field("Authorized to work", "Yes", { kind: "radio" })], "a.example.com");
    b = capture(b, [field("Are you legally authorized to work in the US?", "No", { kind: "radio" })], "b.example.org", T1);
    expect(Object.keys(b.answers)).toHaveLength(1);
    const a = Object.values(b.answers)[0]!;
    expect(a.value).toEqual(["No"]);
    expect(a.labels).toHaveLength(2);
    expect(a.signatures).toEqual([]);
  });

  test("text and select kinds of one concept stay two rows", () => {
    let b = capture(emptyBank(), [field("Country of residence", "Peru", { kind: "text" })]);
    b = capture(b, [field("Residence country", "Peru", { kind: "select" })]);
    expect(Object.keys(b.answers)).toHaveLength(2);
  });

  test("select and radio of one concept share a row", () => {
    let b = capture(emptyBank(), [field("Country of residence", "Peru", { kind: "select" })]);
    b = capture(b, [field("Residence country", "Chile", { kind: "radio" })]);
    expect(Object.keys(b.answers)).toHaveLength(1);
  });

  test("different sensitivity stays two rows", () => {
    const base: Bank = { ...emptyBank(), settings: { captureSelfId: true, disabledHosts: [] } };
    // Same concept ("info status"), but the field name makes one read as self-ID: never merged.
    let b = capture(base, [field("Status info", "Yes", { name: "pronouns" })]);
    b = capture(b, [field("Info status", "No")]);
    expect(Object.values(b.answers).map(a => a.sensitivity).sort()).toEqual(["self_id", "standard"]);
    // And the same sensitivity does merge.
    const same = capture(b, [field("Status of info", "Maybe", { name: "pronouns" })]);
    expect(Object.keys(same.answers)).toHaveLength(2);
    expect(same.answers["status info"]!.value).toEqual(["Maybe"]);
  });

  test("a wording that would flip the owner's sensitivity when joined makes no owner", () => {
    // Each wording alone is standard, but the stored row is re-classified from its key and
    // wordings joined: "security social" + "security your social" reads "social security",
    // a secret, and a secret row is dropped on the next validate. The capture keeps its own row.
    const owner = row("security social", { labels: [] });
    const bank: Bank = { ...emptyBank(), answers: { [owner.key]: owner } };
    const b = capture(bank, [field("Security, your social", "x")]);
    expect(Object.keys(b.answers).sort()).toEqual(["security social", "security your social"]);
    expect(b.answers["security social"]!.labels).toEqual([]);
  });

  test("findOwner refuses an empty concept", () => {
    const bank: Bank = { ...emptyBank(), answers: { "ab": row("ab", { labels: ["ab", "url"] }) } };
    expect(findOwner(bank, field("URL", "x"), "standard")).toBeUndefined();
    expect(findOwner(bank, field("", "x"), "standard")).toBeUndefined();
    expect(findOwner(bank, field("The", "x"), "standard")).toBeUndefined();
  });

  test("a stopword-only or empty label is skipped, not merged", () => {
    const b = capture(emptyBank(), [field("The", "x"), field("", "y"), field("Profile URL", "z")]);
    expect(Object.keys(b.answers)).toEqual(["the", "profile url"]);
    const again = capture(b, [field("Link", "q")]);
    expect(Object.keys(again.answers).sort()).toEqual(["link", "profile url", "the"]);
  });

  test("ties go to the most used, then the newest, then the key", () => {
    const answers = {
      "linkedin one": row("linkedin one", { labels: ["linkedin"], uses: 1, updatedAt: "2026-03-01T00:00:00.000Z" }),
      "linkedin two": row("linkedin two", { labels: ["linkedin"], uses: 5, updatedAt: "2026-01-01T00:00:00.000Z" }),
      "linkedin three": row("linkedin three", { labels: ["linkedin"], uses: 5, updatedAt: "2026-02-01T00:00:00.000Z" }),
    };
    const owner = ownerIndex(answers).find(field("LinkedIn Profile", "x"), "standard");
    expect(owner?.key).toBe("linkedin three");
    const tie = { a1: row("a1", { labels: ["a1", "linkedin"] }), a0: row("a0", { labels: ["a0", "linkedin"] }) };
    expect(ownerIndex(tie).find(field("LinkedIn", "x"), "standard")?.key).toBe("a0");
  });

  test("duplicate captures in one batch make one row", () => {
    const r = mergeCapture(emptyBank(), [
      field("LinkedIn", "https://l/1"),
      field("LinkedIn Profile URL", "https://l/2"),
      field("Your LinkedIn profile link", "https://l/3"),
    ], "x.example.com", T0);
    expect(Object.keys(r.bank.answers)).toEqual(["linkedin"]);
    expect(r.added).toBe(1);
    expect(r.updated).toBe(2);
    expect(r.bank.answers["linkedin"]!.value).toEqual(["https://l/3"]);
    expect(r.bank.answers["linkedin"]!.uses).toBe(3);
  });

  test("the label cap holds when wordings keep arriving", () => {
    let b = capture(emptyBank(), [field("LinkedIn", "v")]);
    // Distinct wordings of one concept: stopword-only variations normalize apart.
    const words = ["Your LinkedIn", "LinkedIn URL", "LinkedIn Link", "LinkedIn Page", "LinkedIn Handle", "Please enter your LinkedIn"];
    for (const w of words) b = capture(b, [field(w, "v")]);
    expect(Object.keys(b.answers)).toEqual(["linkedin"]);
    expect(b.answers["linkedin"]!.labels.length).toBeLessThanOrEqual(MAX_LABELS);
    expect(b.answers["linkedin"]!.labels.length).toBeGreaterThanOrEqual(5);
  });

  test("the label cap keeps the newest wordings", () => {
    const labels = Array.from({ length: MAX_LABELS }, (_, i) => (i === 0 ? "linkedin" : `linkedin w${i}`));
    const bank: Bank = { ...emptyBank(), answers: { linkedin: row("linkedin", { labels }) } };
    const r = capture(bank, [field("LinkedIn Profile URL", "v")]);
    const a = r.answers["linkedin"]!;
    expect(a.labels).toHaveLength(MAX_LABELS);
    expect(a.labels.at(-1)).toBe("linkedin profile url");
    expect(Object.keys(r.answers)).toEqual(["linkedin"]);
  });

  test("a full bank still updates an owner under a sibling key", () => {
    const answers: Record<string, Answer> = {};
    for (let i = 0; i < MAX_ANSWERS - 1; i++) answers[`filler question ${i}`] = row(`filler question ${i}`);
    answers["linkedin"] = row("linkedin");
    const r = mergeCapture({ ...emptyBank(), answers }, [field("LinkedIn Profile URL", "https://l/new")], "x.example.com", T1);
    expect(r.updated).toBe(1);
    expect(r.skippedEmpty).toBe(0);
    expect(r.bank.answers["linkedin"]!.value).toEqual(["https://l/new"]);
  });
});

describe("intent sync", () => {
  test("a captured intent updates every answer that shares it, not only answers[intent]", () => {
    const bank: Bank = {
      ...emptyBank(),
      answers: {
        "visa sponsorship": row("visa sponsorship", { value: ["No"], kind: "radio" }),
        "do you need sponsorship": row("do you need sponsorship", { value: ["No"], kind: "radio" }),
        "require sponsor now": row("require sponsor now", { value: ["No"], kind: "radio", labels: ["require sponsor now", "sponsorship needed"] }),
        "unrelated": row("unrelated", { value: ["keep"] }),
      },
    };
    const r = capture(bank, [field("Will you require sponsorship?", "Yes", { kind: "radio" })], "x.example.com", T1);
    expect(r.answers["visa sponsorship"]!.value).toEqual(["Yes"]);
    expect(r.answers["do you need sponsorship"]!.value).toEqual(["Yes"]);
    expect(r.answers["require sponsor now"]!.value).toEqual(["Yes"]);
    expect(r.answers["unrelated"]!.value).toEqual(["keep"]);
    expect(Object.keys(r.answers)).toHaveLength(4);
  });

  test("basics clearing still removes every wording of the intent", () => {
    let b = capture(emptyBank(), [field("Visa sponsorship", "No", { kind: "radio" })], "vani-basics");
    b = capture(b, [field("Do you require sponsorship?", "No", { kind: "radio" })], "x.example.com");
    expect(Object.keys(b.answers)).toHaveLength(1);
    b = capture(b, [field("Visa sponsorship", "", { kind: "radio", value: [] })], "vani-basics");
    expect(Object.keys(b.answers)).toHaveLength(0);
  });
});

describe("pendingDiff sees sibling-key owners", () => {
  test("a value already held under a sibling wording is unchanged once the wording is known", () => {
    let b = capture(emptyBank(), [field("LinkedIn", "https://l/in/a")], "vani-basics");
    b = capture(b, [field("LinkedIn Profile URL", "https://l/in/a")], "x.example.com");
    const d = pendingDiff(b, [field("LinkedIn Profile URL", "https://l/in/a")], "x.example.com");
    expect(d.unchanged).toBe(1);
    expect(d.send).toEqual([]);
  });

  test("a new wording of a held value is still sent so the wording is learned", () => {
    const b = capture(emptyBank(), [field("LinkedIn", "https://l/in/a")], "vani-basics");
    const d = pendingDiff(b, [field("LinkedIn Profile URL", "https://l/in/a")], "x.example.com");
    expect(d.send).toHaveLength(1);
  });

  test("a different value under a sibling key is a change", () => {
    const b = capture(emptyBank(), [field("LinkedIn", "https://l/in/a")], "vani-basics");
    const d = pendingDiff(b, [field("LinkedIn Profile URL", "https://l/in/zzz")], "x.example.com");
    expect(d.send).toHaveLength(1);
  });
});

describe("owner lookup cost", () => {
  test("builds the concept map once: lookups add one conceptKey call each, whatever the bank size", () => {
    const answers: Record<string, Answer> = {};
    for (let i = 0; i < MAX_ANSWERS; i++) answers[`question number ${i}`] = row(`question number ${i}`, { labels: [`question number ${i}`, `qn ${i}`] });
    answers["linkedin"] = row("linkedin");
    const idx = ownerIndex(answers);
    expect(idx.computed).toBe(0);
    expect(idx.find(field("LinkedIn Profile URL", "x"), "standard")?.key).toBe("linkedin");
    const built = idx.computed;
    const stored = Object.values(answers).reduce((n, a) => n + 1 + a.labels.length, 0);
    expect(built).toBe(stored + 1); // each stored wording once, plus the one asked
    for (let i = 0; i < 50; i++) idx.find(field(`Linkedin profile ${"link ".repeat(i % 3)}`, "x"), "standard");
    expect(idx.computed).toBe(built + 50); // 50 more lookups, 50 more calls: no rebuild, no per-pair work
  });

  test("a whole 2000-answer capture batch stays fast", () => {
    const answers: Record<string, Answer> = {};
    for (let i = 0; i < MAX_ANSWERS - 100; i++) answers[`question number ${i}`] = row(`question number ${i}`);
    const fields = Array.from({ length: 100 }, (_, i) => field(`Brand new topic ${i} ${i}x`, "v"));
    const t = performance.now();
    const r = mergeCapture({ ...emptyBank(), answers }, fields, "x.example.com", T0);
    expect(performance.now() - t).toBeLessThan(5000);
    expect(r.added).toBe(100);
  });
});
