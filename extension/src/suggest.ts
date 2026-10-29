// Live help for a question the bank has no confident answer to: the saved
// answers whose questions look most like it, below the threshold fill trusts.
// The user picks one; it is filled yellow, for review. No model, no network.
import type { Answer, Bank, FieldInfo, Match } from "./types";
import type { FieldRef } from "./extract";
import { classify, untouchable } from "./classify";
import { jaccard, tokens } from "./normalize";
import { MIN_FUZZY, valueFor } from "./match";
import { entryReadings } from "./sections";
import { normalizeLabel } from "./normalize";
import { MIN_DRAFT_CHARS } from "./draft";
import type { FillOutcome } from "./fill";
import { choiceOptions } from "./suggestjob";

/** Below this two questions share too little to be worth showing. */
export const MIN_SUGGEST = 0.25;
export const MAX_SUGGESTIONS = 3;

export interface Suggestion {
  match: Match;
  /** The saved question it came from, for the user to judge. */
  question: string;
}

export function suggest(field: FieldInfo, bank: Bank, host: string, limit = MAX_SUGGESTIONS): Suggestion[] {
  if (untouchable(field.kind)) return [];
  const sens = classify(field);
  if (sens === "secret" || (sens === "self_id" && !bank.settings.captureSelfId)) return [];
  const [page] = entryReadings(normalizeLabel(field.label), false);
  const ft = tokens(page?.base ?? "");
  if (ft.size === 0) return [];
  const usable = (a: Answer) => a.sensitivity !== "secret" && (a.sensitivity !== "self_id" || bank.settings.captureSelfId);

  const scored: Array<{ a: Answer; s: number; q: string }> = [];
  for (const a of Object.values(bank.answers)) {
    if (!usable(a)) continue;
    let best = 0, q = a.key;
    for (const l of [a.key, ...a.labels]) {
      for (const r of entryReadings(l, true)) {
        if (r.n !== page!.n) continue;
        const s = jaccard(ft, tokens(r.base));
        if (s > best) { best = s; q = l; }
      }
    }
    // At or above MIN_FUZZY the fill path already uses it; here only the near misses.
    if (best >= MIN_SUGGEST && best < MIN_FUZZY) scored.push({ a, s: best, q });
  }
  scored.sort((x, y) => y.s - x.s || y.a.uses - x.a.uses);

  const out: Suggestion[] = [];
  const seen = new Set<string>();
  for (const { a, s, q } of scored) {
    const value = valueFor(field, a);
    if (!value) continue; // a choice that maps onto none of this field's options
    const k = value.join("\u0000");
    if (seen.has(k)) continue; // two saved questions with the same answer: show it once
    seen.add(k);
    out.push({ match: { answer: a, via: "fuzzy", score: s, value, review: true }, question: q });
    if (out.length >= limit) break;
  }
  return out;
}

/** What the user has in a free-text field right now, whitespace folded. */
export function currentText(ref: FieldRef): string {
  const el = ref.els[0];
  if (!el) return "";
  const v = ref.widget === "contenteditable" ? el.textContent ?? "" : (el as HTMLInputElement).value ?? "";
  return v.replace(/\s+/g, " ").trim();
}

/**
 * Draft is offered only for an empty free-text field: the request carries the
 * question and the posting, so an empty field means nothing the user typed can
 * reach it, and there is nothing to overwrite.
 */
export function draftable(ref: FieldRef): boolean {
  const textual = ref.els.length === 1 && (ref.info.kind === "text" || ref.info.kind === "textarea" || ref.widget === "contenteditable");
  return textual && !ref.manual && ref.info.value.length === 0 && currentText(ref) === "";
}

/** Same floor `vani draft` uses: a shorter posting is not something to draft from. */
export const MIN_POSTING_CHARS = 40;

/**
 * What the row can do for one detected field. Regenerating reads the question
 * and the posting again. The offer never includes the field's text.
 */
export type DraftOffer = "draft" | "regenerate" | "replace" | "needs-posting" | "choice" | "manual" | "other";

export interface DraftOfferInfo {
  offer: DraftOffer;
  /** Button label. Empty when the row has no draft action. */
  button: string;
  /** One sentence for the row. A manual row uses the scanner's reason. */
  note: string;
  chip: string;
}

export const DRAFT_EMPTY_NOTE = "Empty. A draft sends the question and the posting only.";
export const UNDRAFTED_NOTE = "Vani does not draft this.";
const REGENERATE_NOTE = "Vani drafted this. Regenerating asks again and does not send what is in the field.";
const REPLACE_NOTE = "This field has your text. Nothing is replaced until you confirm.";
const EDITED_NOTE = "You edited this draft. Nothing is replaced until you confirm.";
const UNTRACKED_NOTE = "Vani drafted this, and the original wording was not recorded. Nothing is replaced until you confirm.";
const POSTING_NOTE = "No job description on this page to draft from.";
/** Why a short-answer field is not offered Draft. */
export function shortNote(maxChars: number): string {
  return `This field allows only ${maxChars} characters, too short for a drafted answer (at least ${MIN_DRAFT_CHARS}). Write it yourself.`;
}

const OFFERS: ReadonlySet<string> = new Set(["draft", "regenerate", "replace", "needs-posting", "choice", "manual", "other"]);

export function isDraftOffer(x: unknown): x is DraftOffer {
  return typeof x === "string" && OFFERS.has(x);
}

/** A model draft is only for a single free-text control. Choice widgets and pickers are not. */
function proseKind(kind: FieldInfo["kind"]): boolean {
  return kind === "text" || kind === "textarea";
}

export function draftOffer(info: FieldInfo, manual: string, postingChars: number): DraftOfferInfo {
  if (manual) {
    const note = /[.!?]$/.test(manual) ? manual : `${manual}.`;
    return { offer: "manual", button: "", note, chip: "By hand" };
  }
  if (!proseKind(info.kind)) {
    if (info.kind === "select" || info.kind === "radio" || info.kind === "checkbox") {
      return { offer: "choice", button: "", note: "Choose an option on the page.", chip: "Choice" };
    }
    return { offer: "other", button: "", note: "Fill this one on the page.", chip: "On the page" };
  }
  const sens = classify(info);
  if (sens === "secret" || sens === "self_id") {
    return { offer: "other", button: "", note: UNDRAFTED_NOTE, chip: "On the page" };
  }
  // A field that allows less than the shortest draft is a short-answer field: a draft written to fit it would be a fragment.
  if (info.maxChars !== undefined && info.maxChars < MIN_DRAFT_CHARS) {
    return { offer: "other", button: "", note: shortNote(info.maxChars), chip: "Short answer" };
  }
  const text = (info.value[0] ?? "").replace(/\s+/g, " ").trim();
  const empty = info.value.length === 0 || text === "";
  if (!Number.isFinite(postingChars) || postingChars < MIN_POSTING_CHARS) {
    return { offer: "needs-posting", button: "Draft", note: POSTING_NOTE, chip: "No posting" };
  }
  if (info.drafted === true && !empty) {
    const stored = (info.draftText ?? "").replace(/\s+/g, " ").trim();
    if (stored !== "" && stored === text) return { offer: "regenerate", button: "Regenerate", note: REGENERATE_NOTE, chip: "Drafted" };
    return { offer: "replace", button: "New draft", note: stored === "" ? UNTRACKED_NOTE : EDITED_NOTE, chip: "Your text" };
  }
  if (empty) return { offer: "draft", button: "Draft", note: DRAFT_EMPTY_NOTE, chip: "Empty" };
  return { offer: "replace", button: "New draft", note: REPLACE_NOTE, chip: "Your text" };
}

/**
 * Which scanned field a label refers to. An ordinal is trusted only when it
 * still points at that label. Without one, a label that matches two fields is
 * ambiguous: the first field must not stand in for the others.
 */
export function pickOrdinal(count: number, label: string, ordinal: number | undefined, labelAt: (i: number) => string): number | "absent" | "ambiguous" {
  const want = label.trim().toLowerCase();
  if (typeof ordinal === "number") {
    if (!Number.isInteger(ordinal) || ordinal < 0 || ordinal >= count) return "absent";
    return labelAt(ordinal).trim().toLowerCase() === want ? ordinal : "absent";
  }
  const hits: number[] = [];
  for (let i = 0; i < count; i++) {
    if (labelAt(i).trim().toLowerCase() === want) hits.push(i);
  }
  if (hits.length === 1) return hits[0]!;
  if (hits.length === 0) return "absent";
  return "ambiguous";
}

/** True when the button asks for a draft. A needs-posting row shows the button disabled. */
export function draftEnabled(offer: DraftOffer): boolean {
  return offer === "draft" || offer === "regenerate" || offer === "replace";
}

export function draftButton(offer: DraftOffer): string {
  if (offer === "regenerate") return "Regenerate";
  if (offer === "replace") return "New draft";
  if (offer === "draft" || offer === "needs-posting") return "Draft";
  return "";
}

// --- Suggest, the whole page at once ---------------------------------------------------------------

/** Why Suggest does not start on a posting too short to draft from, or null when it is long enough. */
export function postingTooShort(postingChars: number): string | null {
  if (Number.isFinite(postingChars) && postingChars >= MIN_POSTING_CHARS) return null;
  const found = Number.isFinite(postingChars) ? postingChars : 0;
  return `Suggest drafts from the job description on this page, and found ${found} characters of it (at least ${MIN_POSTING_CHARS} are needed). Open the posting, or use Draft on a question once it loads.`;
}

/** An option that is a prompt, not an answer ("Select...", "Please choose"): never sent, and never one Suggest may pick. */
const PROMPT_OPTION = /^[-–—\s]*(please\s+)?(select|choose|pick)(\s+(one|an?\s+option|an?\s+answer|from\s+the\s+list))?[\s.…:-]*$/i;

/**
 * The option labels Suggest may send for a choice question, or null when it may not ask it: only a native select or a
 * single-choice radio group, never a multi-select or a checkbox, never a field the person or Fill has already answered,
 * never a sensitive or self-identification question, and only with 2 to 50 options (suggestjob.choiceOptions). A select's
 * own prompt option (empty value, or "Select...") is not an option.
 */
export function askableChoice(r: FieldRef): string[] | null {
  const i = r.info;
  if (r.manual !== "" || i.multi === true || classify(i) !== "standard") return null;
  if (i.value.some(v => v.trim() !== "")) return null;
  const select = i.kind === "select" && r.widget === "native";
  const radio = i.kind === "radio" && (r.widget === "native" || r.widget === "aria-radio");
  if (!select && !radio) return null;
  let labels = i.options;
  const live = select ? (r.els[0] as Partial<HTMLSelectElement> | undefined)?.options : undefined;
  if (live && typeof live.length === "number") {
    // The live list knows which option is a prompt (empty value) or unavailable (disabled); info.options does not.
    labels = Array.from(live).filter(o => o.value !== "" && !o.disabled).map(o => o.text);
  }
  return choiceOptions(labels.filter(o => !PROMPT_OPTION.test(o.trim())));
}

/**
 * The match Fill writes for an option Suggest picked: a synthetic answer, marked review, that is in no bank. `via: "fuzzy"`
 * and `review: true` outline it yellow; fill maps `choice` onto the field's own options (match.mapChoice). Its key is
 * prefixed so it can never be taken for a saved answer's.
 */
export function choiceMatch(label: string, kind: FieldInfo["kind"], choice: string, now: Date = new Date()): Match {
  const question = normalizeLabel(label);
  const answer: Answer = {
    key: `suggest:${question}`, labels: question ? [question] : [], kind, value: [choice], sensitivity: "standard", signatures: [],
    uses: 0, fills: 0, updatedAt: now.toISOString(), review: true,
  };
  return { answer, via: "fuzzy", score: 0, value: [choice], review: true, note: "picked by Suggest from the question and its options alone: check it" };
}

/** The page's questions with no saved answer, sorted by what Suggest does with each. */
export interface UnknownSplit {
  /** Empty free-text fields Suggest sends. */
  eligible: FieldRef[];
  /** Single-choice selects and radio groups Suggest sends with their options (askableChoice), in page order. */
  choiceAsk: FieldRef[];
  /** Choice widgets that are not sent (checkboxes, multi-selects, comboboxes, too many options): filled from saved answers only. */
  choice: number;
  /** Free-text fields that already hold text: never sent, so nothing typed is overwritten. */
  prefilled: number;
  /** Self-identification, sensitive and by-hand widgets: left to the person. */
  byHand: number;
  /** Anything else (a date, a number, a split field). */
  other: number;
}

export function splitUnknown(unknown: readonly FieldRef[]): UnknownSplit {
  const s: UnknownSplit = { eligible: [], choiceAsk: [], choice: 0, prefilled: 0, byHand: 0, other: 0 };
  for (const r of unknown) {
    const k = r.info.kind;
    if (r.manual || classify(r.info) !== "standard") s.byHand++;
    else if (draftable(r)) s.eligible.push(r);
    else if (askableChoice(r)) s.choiceAsk.push(r);
    else if (k === "select" || k === "radio" || k === "checkbox") s.choice++;
    else if (k === "text" || k === "textarea" || r.widget === "contenteditable") s.prefilled++;
    else s.other++;
  }
  return s;
}

/** What happened to the drafts Suggest wrote and the rows it left. `why`: the first reason an insert failed for a reason other than text in the field or a limit. */
export interface SuggestTally {
  inserted: number;
  hasText: number;
  tooLong: number;
  notInserted: number;
  why: string;
  failed: number;
  left: number;
  /** Choice questions Suggest filled with the option it picked (yellow). */
  chosen: number;
  /** Options it picked that did not go in, and the first reason why. */
  choiceHeld: number;
  choiceWhy: string;
}

export function newTally(): SuggestTally {
  return { inserted: 0, hasText: 0, tooLong: 0, notInserted: 0, why: "", failed: 0, left: 0, chosen: 0, choiceHeld: 0, choiceWhy: "" };
}

/** Count one picked option by what really happened to it. */
export function tallyChoice(t: SuggestTally, o: Pick<FillOutcome, "filled">, why: string): void {
  if (o.filled) t.chosen++;
  else { t.choiceHeld++; t.choiceWhy ||= why; }
}

/** Count one insert by what really happened to it, never every failure as "the field has text". */
export function tallyInsert(t: SuggestTally, o: Pick<FillOutcome, "filled" | "reason">, why: string): void {
  if (o.filled) t.inserted++;
  else if (o.reason === "has_value") t.hasText++;
  else if (o.reason === "too_long") t.tooLong++;
  else { t.notInserted++; t.why ||= why; }
}

/** The one-line result of a Suggest run: each count named for what it counts, none left out. */
export function suggestMessage(t: SuggestTally, s: UnknownSplit): string {
  const n = (c: number, what: string): string => (c > 0 ? `, ${c} ${what}` : "");
  return `Suggest: ${t.inserted} drafted for review (yellow)` +
    n(t.chosen, "options chosen for review (yellow)") +
    n(t.choiceHeld, `options not filled (${t.choiceWhy})`) +
    n(t.hasText, "drafts not inserted (the field has text)") +
    n(t.tooLong, "drafts longer than the field allows (not inserted: shorten by hand)") +
    n(t.notInserted, `drafts not inserted (${t.why})`) +
    n(t.failed, "questions it could not draft") +
    n(t.left, "for you to answer") +
    n(s.choice, "choice questions for you") +
    n(s.prefilled, "already have text (left alone)") +
    n(s.byHand, "self-identification or by-hand questions for you") +
    n(s.other, "of another kind for you") +
    ". Click for why. Read every draft before you submit.";
}
