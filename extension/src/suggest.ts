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
