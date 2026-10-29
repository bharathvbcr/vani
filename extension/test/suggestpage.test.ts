import { describe, expect, test } from "bun:test";
import type { FieldInfo } from "../src/types";
import { emptyBank } from "../src/types";
import { mergeCapture } from "../src/bank";
import type { FieldRef } from "../src/extract";
import { DEFAULT_WORDS, MAX_WORDS, MIN_DRAFT_CHARS, MIN_WORDS, buildDraftRequest, maxWordsFor } from "../src/draft";
import {
  MIN_POSTING_CHARS, askableChoice, choiceMatch, draftEnabled, draftOffer, newTally, postingTooShort, splitUnknown, suggestMessage, tallyChoice, tallyInsert,
} from "../src/suggest";
import { MAX_CHOICE_OPTIONS } from "../src/suggestjob";

function info(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "Why this team?", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}

/** A scanned field, as far as suggest.ts reads one: its info, its single control's text, its widget. */
function ref(p: Partial<FieldInfo>, o: { manual?: string; text?: string; widget?: FieldRef["widget"] } = {}): FieldRef {
  const i = info(p);
  const el = { value: o.text ?? i.value[0] ?? "", textContent: o.text ?? i.value[0] ?? "" } as unknown as HTMLElement;
  return { info: i, els: [el], widget: o.widget ?? "native", manual: o.manual ?? "" };
}

const POSTING = 80;

describe("Draft is sized to the field's limit", () => {
  test("a field with no limit keeps the default request, and a roomy one is not made longer", () => {
    expect(maxWordsFor(undefined)).toBeUndefined();
    expect(maxWordsFor(Number.NaN)).toBeUndefined();
    expect(maxWordsFor(100_000)).toBe(DEFAULT_WORDS);
    expect(maxWordsFor(2000)).toBe(DEFAULT_WORDS);
    const none = buildDraftRequest("Why us?", { company: "A", title: "B", description: "d".repeat(80) }, maxWordsFor(undefined)).request as { max_words: number };
    expect(none.max_words).toBe(DEFAULT_WORDS);
  });

  test("a limit that binds becomes floor(chars / 6.5) words, within the request's own bounds", () => {
    expect(maxWordsFor(650)).toBe(100);
    expect(maxWordsFor(1000)).toBe(153);
    expect(maxWordsFor(MIN_DRAFT_CHARS)).toBe(MIN_WORDS);
    expect(maxWordsFor(10)).toBe(MIN_WORDS); // clamped: offering Draft there is refused separately
    expect(maxWordsFor(0)).toBe(MIN_WORDS);
    for (const n of [0, 1, 259, 260, 1000, 5000, 1e9]) {
      const w = maxWordsFor(n)!;
      expect(w).toBeGreaterThanOrEqual(MIN_WORDS);
      expect(w).toBeLessThanOrEqual(MAX_WORDS);
    }
  });

  test("the words it asks for fit the characters the field allows", () => {
    for (const chars of [MIN_DRAFT_CHARS, 300, 500, 1000, 1170]) {
      const request = buildDraftRequest("Why us?", { company: "A", title: "B", description: "d".repeat(80) }, maxWordsFor(chars)).request as { max_words: number };
      expect(request.max_words * 6.5).toBeLessThanOrEqual(Math.max(chars, MIN_DRAFT_CHARS));
    }
  });

  test("a field too short for the shortest draft is not offered Draft, and says why", () => {
    expect(MIN_DRAFT_CHARS).toBe(260);
    for (const maxChars of [0, 1, 100, 259]) {
      const o = draftOffer(info({ maxChars }), "", POSTING);
      expect(o).toMatchObject({ offer: "other", button: "", chip: "Short answer" });
      expect(draftEnabled(o.offer)).toBe(false);
      expect(o.note).toContain(`only ${maxChars} characters`);
      expect(o.note).toContain("Write it yourself");
    }
    expect(draftOffer(info({ maxChars: 260 }), "", POSTING).offer).toBe("draft");
    expect(draftOffer(info({ maxChars: 5000 }), "", POSTING).offer).toBe("draft");
    expect(draftOffer(info({}), "", POSTING).offer).toBe("draft");
  });

  test("a short-answer field is not offered a regenerate or replace either, nor held for a missing posting", () => {
    expect(draftOffer(info({ maxChars: 100, value: ["Acme"], drafted: true, draftText: "Acme" }), "", POSTING).offer).toBe("other");
    expect(draftOffer(info({ maxChars: 100, value: ["my own text"] }), "", POSTING).offer).toBe("other");
    expect(draftOffer(info({ maxChars: 100 }), "", 0).offer).toBe("other");
  });

  test("a secret or self-ID field still says it is not drafted, whatever its limit", () => {
    expect(draftOffer(info({ label: "Gender", maxChars: 50 }), "", POSTING).note).toBe("Vani does not draft this.");
  });
});

describe("Suggest on a posting too short to draft from", () => {
  test("says so, with the number it found, and names the floor", () => {
    for (const n of [0, 1, MIN_POSTING_CHARS - 1, Number.NaN]) {
      const why = postingTooShort(n);
      expect(why).not.toBeNull();
      expect(why).toContain(`at least ${MIN_POSTING_CHARS}`);
    }
    expect(postingTooShort(0)).toContain("found 0 characters");
    expect(postingTooShort(12)).toContain("found 12 characters");
    expect(postingTooShort(MIN_POSTING_CHARS)).toBeNull();
    expect(postingTooShort(60_000)).toBeNull();
  });
});

describe("the Suggest toast counts each kind of question as itself", () => {
  const unknown = [
    ref({ label: "Why us?", kind: "textarea" }),
    ref({ label: "Tell us more", kind: "text" }),
    ref({ label: "Cover note", kind: "textarea", value: ["typed by me"] }),
    ref({ label: "Notice period", kind: "select", options: ["A", "B"] }),
    ref({ label: "Referral", kind: "radio", options: ["Yes", "No"] }),
    ref({ label: "Skills", kind: "select", options: ["Go", "Rust"], multi: true }),
    ref({ label: "Subscribe", kind: "checkbox", value: ["no"] }),
    ref({ label: "Gender", kind: "select", options: ["Man", "Woman"] }),
    ref({ label: "Start", kind: "text" }, { manual: "date or dialog picker" }),
    ref({ label: "Salary", kind: "number" }),
  ];

  test("eligible, choice asked, choice left, already-typed, self-ID or by-hand, and other are separate counts", () => {
    const s = splitUnknown(unknown);
    expect(s.eligible.map(r => r.info.label)).toEqual(["Why us?", "Tell us more"]);
    expect(s.choiceAsk.map(r => r.info.label)).toEqual(["Notice period", "Referral"]);
    expect({ choice: s.choice, prefilled: s.prefilled, byHand: s.byHand, other: s.other }).toEqual({ choice: 2, prefilled: 1, byHand: 2, other: 1 });
    expect(s.eligible.length + s.choiceAsk.length + s.choice + s.prefilled + s.byHand + s.other).toBe(unknown.length);
  });

  test("the old toast called every unsent field a choice question; the new one does not", () => {
    const s = splitUnknown(unknown);
    const msg = suggestMessage({ ...newTally(), inserted: 2 }, s);
    expect(msg).toContain("2 choice questions for you");
    expect(msg).toContain("1 already have text (left alone)");
    expect(msg).toContain("2 self-identification or by-hand questions for you");
    expect(msg).toContain("1 of another kind for you");
    // The pre-fix arithmetic: unknown.length - eligible.length, all of it called "choice".
    expect(unknown.length - s.eligible.length).toBe(8);
    expect(msg).not.toContain("8 choice");
  });

  test("a zero count is not mentioned", () => {
    const msg = suggestMessage({ ...newTally(), inserted: 1 }, splitUnknown([ref({ label: "Why us?", kind: "textarea" })]));
    expect(msg).toBe("Suggest: 1 drafted for review (yellow). Click for why. Read every draft before you submit.");
  });
});

describe("an insert that failed is reported for what failed it", () => {
  test("text in the field, a limit and anything else are counted apart; only text in the field is 'the field has text'", () => {
    const t = newTally();
    tallyInsert(t, { filled: true }, "");
    tallyInsert(t, { filled: false, reason: "has_value" }, "has text");
    tallyInsert(t, { filled: false, reason: "too_long" }, "too long");
    tallyInsert(t, { filled: false, reason: "too_long" }, "too long");
    tallyInsert(t, { filled: false, reason: "manual" }, "the page did not keep the value");
    tallyInsert(t, { filled: false, reason: "detached" }, "the field is gone from the page");
    expect(t).toMatchObject({ inserted: 1, hasText: 1, tooLong: 2, notInserted: 2, why: "the page did not keep the value" });
    const msg = suggestMessage(t, splitUnknown([]));
    expect(msg).toContain("1 drafts not inserted (the field has text)");
    expect(msg).toContain("2 drafts longer than the field allows (not inserted: shorten by hand)");
    expect(msg).toContain("2 drafts not inserted (the page did not keep the value)");
  });

  test("failed rows and rows left for the person are their own counts", () => {
    const msg = suggestMessage({ ...newTally(), failed: 2, left: 3 }, splitUnknown([]));
    expect(msg).toContain("2 questions it could not draft");
    expect(msg).toContain("3 for you to answer");
    expect(msg).toContain("Suggest: 0 drafted for review (yellow)");
  });
});

describe("which choice questions Suggest may send", () => {
  const opts = ["Immediately", "2 weeks", "1 month"];
  const select = (p: Partial<FieldInfo>, o: Parameters<typeof ref>[1] = {}) => ref({ kind: "select", label: "Notice period", options: opts, ...p }, o);

  test("a single native select or radio group, empty and standard, with 2 to 50 options, is asked with those options", () => {
    expect(askableChoice(select({}))).toEqual(opts);
    expect(askableChoice(ref({ kind: "radio", label: "Authorized to travel?", options: ["Yes", "No"] }))).toEqual(["Yes", "No"]);
    expect(askableChoice(ref({ kind: "radio", label: "Authorized to travel?", options: ["Yes", "No"] }, { widget: "aria-radio" }))).toEqual(["Yes", "No"]);
  });

  test("a multi-select, a checkbox, a combobox, a manual widget and a text field are not", () => {
    expect(askableChoice(select({ multi: true }))).toBeNull();
    expect(askableChoice(ref({ kind: "checkbox", label: "Agree", options: ["Yes", "No"], value: ["no"] }))).toBeNull();
    expect(askableChoice(select({}, { widget: "combobox" }))).toBeNull();
    expect(askableChoice(select({}, { manual: "read-only field (picker)" }))).toBeNull();
    expect(askableChoice(ref({ kind: "text", label: "Why us?" }))).toBeNull();
  });

  test("a secret or self-identification question is never sent, nor one that already has an answer", () => {
    expect(askableChoice(select({ label: "Gender", options: ["Man", "Woman"] }))).toBeNull();
    expect(askableChoice(select({ label: "Veteran status", options: ["Yes", "No"] }))).toBeNull();
    expect(askableChoice(select({ label: "Bank account type", options: ["Checking", "Savings"] }))).toBeNull();
    expect(askableChoice(select({ value: ["2 weeks"] }))).toBeNull();
    expect(askableChoice(select({ value: [" "] }))).not.toBeNull(); // whitespace is not an answer
  });

  test("fewer than 2 options, more than 50, a 201-character option or the same option twice is not asked", () => {
    expect(askableChoice(select({ options: ["Only"] }))).toBeNull();
    expect(askableChoice(select({ options: [] }))).toBeNull();
    expect(askableChoice(select({ options: Array.from({ length: MAX_CHOICE_OPTIONS + 1 }, (_, i) => `Option ${i}`) }))).toBeNull();
    expect(askableChoice(select({ options: Array.from({ length: MAX_CHOICE_OPTIONS }, (_, i) => `Option ${i}`) }))?.length).toBe(MAX_CHOICE_OPTIONS);
    expect(askableChoice(select({ options: ["A", "x".repeat(201)] }))).toBeNull();
    expect(askableChoice(select({ options: ["A", "B", "A"] }))).toBeNull();
  });

  test("a select's own prompt is not an option, by its empty value or its wording", () => {
    expect(askableChoice(select({ options: ["Select...", "Yes", "No"] }))).toEqual(["Yes", "No"]);
    expect(askableChoice(select({ options: ["Please choose", "", "Yes", "No"] }))).toEqual(["Yes", "No"]);
    expect(askableChoice(select({ options: ["Select...", "Only"] }))).toBeNull();
    expect(askableChoice(select({ options: ["-- Please select --", "Select one", "Choose an option", "Yes", "No"] }))).toEqual(["Yes", "No"]);
    // A real option that merely starts with those words is an option.
    expect(askableChoice(select({ options: ["Pick-up", "Select all that apply", "Choose your own", "Selection panel"] }))).toEqual(["Pick-up", "Select all that apply", "Choose your own", "Selection panel"]);
    // The live list knows a prompt by its empty value and a dead option by `disabled`.
    const el = { options: [{ value: "", text: "Pick one", disabled: false }, { value: "a", text: " Yes ", disabled: false }, { value: "b", text: "No", disabled: false }, { value: "c", text: "Retired", disabled: true }] };
    const live = ref({ kind: "select", label: "Relocate?", options: ["Pick one", "Yes", "No", "Retired"] });
    live.els = [el as unknown as HTMLElement];
    expect(askableChoice(live)).toEqual(["Yes", "No"]);
  });

  test("splitUnknown sends a choice question that has no match and counts the rest", () => {
    const s = splitUnknown([select({}), select({ multi: true }), select({ label: "Gender", options: ["M", "F"] }), select({ value: ["1 month"] })]);
    expect(s.choiceAsk.map(r => r.info.label)).toEqual(["Notice period"]);
    expect({ choice: s.choice, byHand: s.byHand }).toEqual({ choice: 2, byHand: 1 });
  });
});

describe("the option Suggest picked", () => {
  test("is a synthetic review answer, outlined yellow by fill, whose key can never be a saved answer's", () => {
    const m = choiceMatch("Notice period?", "select", "2 weeks", new Date("2026-10-28T00:00:00Z"));
    expect(m).toMatchObject({ via: "fuzzy", review: true, value: ["2 weeks"] });
    expect(m.answer).toMatchObject({ review: true, sensitivity: "standard", kind: "select", value: ["2 weeks"], uses: 0, fills: 0 });
    expect(m.answer.key.startsWith("suggest:")).toBe(true);
    expect(m.note).toContain("check it");
  });

  test("once tagged a draft (and its fill record dropped) a saved choice lands for review, never as a confident answer", () => {
    const radio = info({ kind: "radio", label: "Notice period", options: ["Immediately", "2 weeks"], value: ["2 weeks"], drafted: true });
    const saved = mergeCapture(emptyBank(), [radio], "jobs.example.com", new Date("2026-10-28T00:00:00Z"));
    expect(saved.added).toBe(1);
    expect(Object.values(saved.bank.answers)[0]).toMatchObject({ value: ["2 weeks"], review: true });
    // The same field still carrying Fill's record is skipped as Vani's own fill coming back: the reason the record is dropped.
    const filled = mergeCapture(emptyBank(), [{ ...radio, filledBy: { key: "suggest:notice period", review: true } }], "jobs.example.com", new Date("2026-10-28T00:00:00Z"));
    expect(filled.added).toBe(0);
  });

  test("the toast counts options chosen and options held back, each by its own reason", () => {
    const t = newTally();
    tallyChoice(t, { filled: true }, "");
    tallyChoice(t, { filled: false }, "the field already has text");
    tallyChoice(t, { filled: false }, "none of the field's options matches");
    expect(t).toMatchObject({ chosen: 1, choiceHeld: 2, choiceWhy: "the field already has text" });
    const msg = suggestMessage({ ...t, inserted: 1 }, splitUnknown([]));
    expect(msg).toContain("1 drafted for review (yellow), 1 options chosen for review (yellow), 2 options not filled (the field already has text)");
  });
});
