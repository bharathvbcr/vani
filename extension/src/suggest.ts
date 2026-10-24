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
