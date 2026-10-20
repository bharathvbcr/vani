import { describe, expect, test } from "bun:test";
import type { Answer, Bank, FieldInfo, Match } from "../src/types";
import { CONFIDENT, MIN_FUZZY, matchField, valueFor } from "../src/match";
import { bankIndexBuilds, bankIndexFor, fuzzyCandidates } from "../src/bankindex";
import { autocompleteKey, jaccard, normalizeLabel, signature, tokens } from "../src/normalize";
import { classify, untouchable } from "../src/classify";
import { entryReadings, legacyEntryKey } from "../src/sections";
import { intentByKey, intentOf, motivationEssay } from "../src/intents";
import { emptyBank } from "../src/types";

/**
 * The oracle: matchField exactly as it was before the bank index, scanning every answer for every
 * field. Kept here only, so the indexed matchField is held to the old behaviour answer for answer.
 */
function naiveMatchField(field: FieldInfo, bank: Bank, host: string): Match | null {
  if (untouchable(field.kind)) return null;
  const sens = classify(field);
  if (sens === "secret") return null;
  if (sens === "self_id" && !bank.settings.captureSelfId) return null;
  if (motivationEssay(field.label)) return null;

  const answers = Object.values(bank.answers);
  const usable = (a: Answer) => a.sensitivity !== "secret" && (a.sensitivity !== "self_id" || bank.settings.captureSelfId);
  const candidates: Array<{ a: Answer; via: Match["via"]; score: number }> = [];

  const sig = signature(host, field);
  if (sig) {
    for (const a of answers) if (usable(a) && a.signatures.includes(sig)) candidates.push({ a, via: "signature", score: 1 });
  }
  const ak = autocompleteKey(field.autocomplete);
  if (ak && bank.answers[ak] && usable(bank.answers[ak]!)) candidates.push({ a: bank.answers[ak]!, via: "autocomplete", score: 0.95 });

  const label = normalizeLabel(field.label);
  const legacy = legacyEntryKey(label);
  const [page] = entryReadings(label, false);
  if (label && page) {
    const ft = tokens(page.base);
    for (const a of answers) {
      if (!usable(a)) continue;
      if (a.key === label || a.labels.includes(label)) {
        candidates.push({ a, via: "label", score: 0.9 });
        continue;
      }
      if (legacy && (a.key === legacy || a.labels.includes(legacy))) {
        candidates.push({ a, via: "label", score: 0.89 });
        continue;
      }
      let best = 0;
      for (const l of [a.key, ...a.labels]) {
        for (const e of entryReadings(l, true)) if (e.n === page.n) best = Math.max(best, jaccard(ft, tokens(e.base)));
      }
      if (best >= MIN_FUZZY) candidates.push({ a, via: "fuzzy", score: 0.5 + 0.35 * best });
    }
  }

  const want = intentOf(field.label);
  const intent = want ? intentByKey(want) : undefined;
  if (intent) {
    for (const a of answers) {
      if (usable(a) && [a.key, ...a.labels].some(l => intentOf(l) === want)) candidates.push({ a, via: "intent", score: 0.88 });
    }
  }

  const isWant = (a: Answer) => Boolean(want && [a.key, ...a.labels].some(l => intentOf(l) === want));

  candidates.sort((x, y) => {
    if (want && isWant(x.a) && isWant(y.a)) {
      const timeCmp = (y.a.updatedAt ?? "").localeCompare(x.a.updatedAt ?? "");
      if (timeCmp !== 0) return timeCmp;
    }
    return y.score - x.score ||
      (x.via === "intent" && y.via === "intent" ? (y.a.updatedAt ?? "").localeCompare(x.a.updatedAt ?? "") : y.a.uses - x.a.uses);
  });
  for (const c of candidates) {
    const value = valueFor(field, c.a);
    if (!value) continue;
    const partial = (field.multi === true || (field.kind === "checkbox" && field.options.length > 1)) && value.length < c.a.value.length;
    return { answer: c.a, via: c.via, score: c.score, value, review: c.score < CONFIDENT || c.a.review || partial || intent?.review === true };
  }
  return null;
}

// --- seeded generator ---------------------------------------------------------------------------

function prng(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const WORDS = [
  "first", "last", "name", "email", "phone", "city", "state", "country", "company", "title", "years", "experience",
  "salary", "expectations", "sponsorship", "visa", "authorized", "work", "relocate", "relocation", "open", "willing",
  "linkedin", "github", "website", "portfolio", "start", "date", "notice", "period", "hear", "about", "gender", "veteran",
  "degree", "school", "major", "gpa", "skills", "languages", "location", "address", "zip", "referral", "employer",
  "current", "previous", "manager", "reason", "leaving", "the", "your", "of", "a",
];
const ISO = ["2024-01-01T00:00:00Z", "2024-06-01T00:00:00Z", "2025-01-01T00:00:00Z"];
const SIG_IDS = ["first_name", "last_name", "email", "phone", "resume_url", "linkedin", "github", "website"];
const INTENT_LABELS = [
  "Are you authorized to work in the United States?", "Do you require visa sponsorship?", "Will you now or in the future require sponsorship?",
  "Are you willing to relocate?", "What are your salary expectations?", "How did you hear about us?", "What is your notice period?",
  "Please describe your gender", "Are you a protected veteran?",
];
const HOST = "boards.greenhouse.io";
const OPTIONS = ["Yes", "No", "United States", "Canada", "Decline to self-identify", "Male", "Female"];

function gen(seed: number, size: number): { bank: Bank; fields: FieldInfo[] } {
  const rnd = prng(seed);
  const pick = <T>(xs: readonly T[]): T => xs[Math.floor(rnd() * xs.length)]!;
  const phrase = (): string => {
    const n = 1 + Math.floor(rnd() * 5);
    let s = Array.from({ length: n }, () => pick(WORDS)).join(" ");
    const r = rnd();
    if (r < 0.08) s += " (vani section 2)";
    else if (r < 0.14) s += " section 3";
    else if (r < 0.2) s = pick(INTENT_LABELS);
    return s;
  };
  const bank = emptyBank();
  bank.settings.captureSelfId = rnd() < 0.5;
  const pool: string[] = [];
  for (let i = 0; i < size; i++) {
    const wording = phrase();
    const key = i % 37 === 0 ? pick(["first name", "last name", "full name"]) + (i > 40 ? ` ${i}` : "") : normalizeLabel(wording);
    const labels = Array.from({ length: Math.floor(rnd() * 4) }, () => {
      const r = rnd();
      if (r < 0.15) return ""; // empty wording
      if (r < 0.35 && pool.length) return pick(pool); // a wording another answer also holds
      return normalizeLabel(phrase());
    });
    if (rnd() < 0.2) labels.push(key); // duplicate of its own key
    pool.push(key, ...labels);
    const sensR = rnd();
    const a: Answer = {
      key: i === 0 ? "first name" : key,
      labels,
      kind: pick(["text", "select", "checkbox", "textarea"] as const),
      value: rnd() < 0.1 ? [] : Array.from({ length: 1 + Math.floor(rnd() * 2) }, () => pick(OPTIONS)),
      sensitivity: sensR < 0.08 ? "secret" : sensR < 0.2 ? "self_id" : "standard",
      signatures: Array.from({ length: Math.floor(rnd() * 3) }, () => signature(HOST, { name: pick(SIG_IDS), automationId: "" })),
      uses: Math.floor(rnd() * 4), // few distinct values: plenty of ties
      fills: 0,
      updatedAt: pick(ISO),
      review: rnd() < 0.1,
    };
    // Same key twice would collapse in the record: keep every generated answer distinct by key.
    let k = a.key, n = 2;
    while (bank.answers[k]) k = `${a.key} #${n++}`;
    a.key = k;
    bank.answers[k] = a;
  }
  const keys = Object.keys(bank.answers);
  const fields: FieldInfo[] = [];
  for (let i = 0; i < 30; i++) {
    const r = rnd();
    const label = r < 0.1 ? "" : r < 0.4 && pool.length ? pick([...keys, ...pool]) : r < 0.5 ? pick(INTENT_LABELS) : phrase();
    const kind = pick(["text", "select", "checkbox", "radio", "textarea", "email", "file", "password"] as const);
    const choice = kind === "select" || kind === "radio" || kind === "checkbox";
    fields.push({
      kind,
      label,
      name: rnd() < 0.4 ? pick(SIG_IDS) : "",
      autocomplete: rnd() < 0.2 ? pick(["given-name", "family-name", "name", "email"]) : "",
      automationId: "",
      options: choice ? Array.from({ length: rnd() < 0.2 ? 0 : 1 + Math.floor(rnd() * 3) }, () => pick(OPTIONS)) : [],
      required: false,
      value: [],
      multi: rnd() < 0.1,
    });
  }
  return { bank, fields };
}

const summary = (m: Match | null) => m && { key: m.answer.key, via: m.via, score: m.score, review: m.review, value: m.value };

describe("indexed matchField equals the full scan", () => {
  test("randomized banks and fields (seeded), 2000-answer bank included", () => {
    let cases = 0, hits = 0;
    const vias = new Set<string>();
    for (let seed = 1; seed <= 24; seed++) {
      const size = seed === 24 ? 2000 : seed % 6 === 0 ? 0 : 5 + seed * 17;
      const { bank, fields } = gen(seed, size);
      for (const f of fields) {
        const got = matchField(f, bank, HOST);
        const want = naiveMatchField(f, bank, HOST);
        expect(summary(got)).toEqual(summary(want));
        expect(got?.answer).toBe(want?.answer); // the same object, not just an equal one
        cases++;
        if (want) { hits++; vias.add(want.via); }
      }
    }
    expect(cases).toBeGreaterThanOrEqual(500);
    expect(hits).toBeGreaterThan(100); // the generator reaches real matches, not just misses
    for (const via of ["signature", "autocomplete", "label", "fuzzy", "intent"]) expect(vias.has(via)).toBe(true);
    // The naive oracle scans every answer per field: about 2 s alone, 7 s on a loaded machine.
  }, 60_000);

  test("the bank changing under the same settings does not matter: a second bank is indexed on its own", () => {
    const a = gen(101, 80), b = gen(102, 80);
    for (const f of a.fields) {
      expect(summary(matchField(f, a.bank, HOST))).toEqual(summary(naiveMatchField(f, a.bank, HOST)));
      expect(summary(matchField(f, b.bank, HOST))).toEqual(summary(naiveMatchField(f, b.bank, HOST)));
    }
  });

  test("captureSelfId read per call, not frozen into the index", () => {
    const { bank, fields } = gen(7, 300);
    for (const on of [false, true, false]) {
      bank.settings.captureSelfId = on;
      for (const f of fields) expect(summary(matchField(f, bank, HOST))).toEqual(summary(naiveMatchField(f, bank, HOST)));
    }
  });
});

describe("fuzzy candidates are exact", () => {
  test("jaccard at or above MIN_FUZZY needs a shared token, so token-sharing candidates lose nothing", () => {
    expect(MIN_FUZZY).toBeGreaterThan(0);
    const rnd = prng(99);
    const sample = (): Set<string> => new Set(Array.from({ length: Math.floor(rnd() * 5) }, () => WORDS[Math.floor(rnd() * 12)]!));
    for (let i = 0; i < 3000; i++) {
      const a = sample(), b = sample();
      const shares = [...a].some(t => b.has(t));
      if (!shares) expect(jaccard(a, b)).toBe(0);
      if (jaccard(a, b) >= MIN_FUZZY) expect(shares).toBe(true);
    }
  });

  test("candidates are the answers with a reading token in common with the field, in bank order", () => {
    const { bank } = gen(5, 200);
    const idx = bankIndexFor(bank);
    const ft = tokens("current employer");
    const got = fuzzyCandidates(idx, ft);
    expect(got).toEqual([...got].sort((x, y) => x - y));
    const want = idx.answers.flatMap((_, i) => (idx.readings[i]!.some(r => [...r.tokens].some(t => ft.has(t))) ? [i] : []));
    expect(got).toEqual(want);
    expect(fuzzyCandidates(idx, new Set())).toEqual([]);
  });
});

describe("one index per bank", () => {
  test("many matchField calls on one bank build the index once", () => {
    const { bank, fields } = gen(11, 300);
    const before = bankIndexBuilds();
    for (let round = 0; round < 10; round++) for (const f of fields) matchField(f, bank, HOST);
    expect(bankIndexBuilds() - before).toBe(1);
  });

  test("a different bank object is indexed separately, and each stays cached", () => {
    const x = gen(12, 50).bank, y = gen(13, 50).bank;
    const before = bankIndexBuilds();
    const f = gen(12, 50).fields[0]!;
    for (let i = 0; i < 5; i++) { matchField(f, x, HOST); matchField(f, y, HOST); }
    expect(bankIndexBuilds() - before).toBe(2);
  });

  test("an answer added to the same bank object is seen (the index is rebuilt, not stale)", () => {
    const bank = emptyBank();
    const f: FieldInfo = { kind: "text", label: "Favourite colour", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [] };
    expect(matchField(f, bank, HOST)).toBeNull();
    bank.answers["favourite colour"] = {
      key: "favourite colour", labels: [], kind: "text", value: ["green"], sensitivity: "standard", signatures: [],
      uses: 1, fills: 0, updatedAt: ISO[0]!, review: false,
    };
    expect(matchField(f, bank, HOST)?.value).toEqual(["green"]);
    delete bank.answers["favourite colour"];
    expect(matchField(f, bank, HOST)).toBeNull();
  });

  test("an empty bank and an empty label match nothing", () => {
    const f: FieldInfo = { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [] };
    expect(matchField(f, emptyBank(), HOST)).toBeNull();
    expect(matchField(f, gen(3, 40).bank, HOST)).toEqual(naiveMatchField(f, gen(3, 40).bank, HOST));
  });
});
