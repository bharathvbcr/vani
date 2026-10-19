import { type Answer, type Bank, type FieldInfo, type Match, CHOICE_KINDS } from "./types";
import { autocompleteKey, jaccard, normalizeLabel, signature, tokens, withoutDialCode } from "./normalize";
import { classify, untouchable } from "./classify";
import { entryReadings, legacyEntryKey } from "./sections";
import { intentByKey, intentOf, motivationEssay } from "./intents";

/** Below this a fuzzy label match is not trusted at all. */
export const MIN_FUZZY = 0.6;
/** Matches scoring below this are filled but marked for review. */
export const CONFIDENT = 0.85;

const YES = new Set(["yes", "y", "true", "i do", "i am", "i agree", "agree", "checked"]);
const NO = new Set(["no", "n", "false", "i do not", "i am not", "unchecked"]);
/** An acknowledgement option, and the refusals that read like one (on normalized text). */
const ACK = /\b(agree|accept|acknowledge|understand|consent|confirm|will read|have read)\b/;
const REFUSAL = /\b(not|disagree|decline|dont|refuse|reject)\b/;
/** Declining to answer, in the wordings self-identification lists use (on normalized text). */
const DECLINE = /\b(decline[ds]?|prefer not|rather not|choose not|do not wish|dont wish|not wish|(do not|dont) want to (answer|say|disclose|identify|self identify)|not to (answer|say|disclose|identify|self identify))\b/;

/**
 * Map a remembered choice onto this field's options: exact label first (then
 * exact once dialling codes are set aside), then declining (a saved "decline"
 * takes the form's own decline option, and nothing else), then yes/no meaning,
 * then the closest option by tokens. A value with no acceptable option maps to
 * nothing, so the field is left alone.
 */
export function mapChoice(value: string, options: string[]): string | null {
  const v = normalizeLabel(value);
  const norm = options.map(o => normalizeLabel(o));
  const exact = norm.indexOf(v);
  if (exact >= 0) return options[exact]!;
  const bare = normalizeLabel(withoutDialCode(value));
  const bareOptions = options.map(o => normalizeLabel(withoutDialCode(o)));
  if (bare) {
    const hits = bareOptions.flatMap((o, i) => (o === bare ? [i] : []));
    if (hits.length === 1) return options[hits[0]!]!;
  }
  if (DECLINE.test(v)) {
    const declines = norm.flatMap((o, i) => (DECLINE.test(o) ? [i] : []));
    return declines.length === 1 ? options[declines[0]!]! : null;
  }
  const polarity = YES.has(v) ? "yes" : NO.has(v) ? "no" : "";
  if (polarity) {
    const set = polarity === "yes" ? YES : NO;
    const i = norm.findIndex(o => set.has(o) || o.startsWith(polarity + " "));
    if (i >= 0) return options[i]!;
  }
  if (polarity === "yes") {
    // Agreeing takes the one acknowledgement the form offers ("I will read the agreement below"), never a refusal.
    const acks = norm.flatMap((o, i) => (ACK.test(o) && !REFUSAL.test(o) ? [i] : []));
    if (acks.length === 1) return options[acks[0]!]!;
  }
  const vt = tokens(withoutDialCode(value));
  let best = -1, bestScore = 0;
  options.forEach((o, i) => {
    const s = jaccard(vt, tokens(withoutDialCode(o)));
    if (s > bestScore) { best = i; bestScore = s; }
  });
  return best >= 0 && bestScore >= 0.6 ? options[best]! : null;
}

/**
 * A saved wording's entry readings with their tokens, by wording: every field of every rescan
 * compares against every saved wording, and these never change. Bounded; cleared when full.
 */
const readings = new Map<string, Array<{ n: number; tokens: Set<string> }>>();
const MAX_READINGS = 8192;

function savedReadings(label: string): Array<{ n: number; tokens: Set<string> }> {
  let r = readings.get(label);
  if (r) return r;
  r = entryReadings(label, true).map(e => ({ n: e.n, tokens: tokens(e.base) }));
  if (readings.size >= MAX_READINGS) readings.clear();
  readings.set(label, r);
  return r;
}

export function valueFor(field: FieldInfo, a: Answer): string[] | null {
  // A multi-select (select multiple, chip picker) takes every saved value, whatever its option count.
  const many = field.kind === "checkbox" || field.multi === true;
  if (field.kind === "checkbox" && field.options.length <= 1 && field.multi !== true) {
    // A single checkbox: the answer's meaning decides checked or not.
    const v = normalizeLabel(a.value[0] ?? "");
    if (YES.has(v)) return ["yes"];
    if (NO.has(v)) return ["no"];
    return null;
  }
  if (CHOICE_KINDS.has(field.kind) && field.options.length === 0) {
    // A widget whose options are not on the page until it is opened (combobox,
    // typeahead): the saved choice goes through as is and is mapped onto the
    // real options when it is filled.
    return a.value.length > 0 ? (many ? a.value : [a.value[0]!]) : null;
  }
  if (CHOICE_KINDS.has(field.kind)) {
    const mapped = a.value.map(v => mapChoice(v, field.options)).filter((v): v is string => v !== null);
    if (mapped.length === 0) return null;
    return many ? mapped : [mapped[0]!];
  }
  // A text field takes a text answer; a choice answer can fill a text box too
  // ("Country: United States").
  return a.value.length > 0 ? [a.value.join(", ")] : null;
}

/**
 * The best remembered answer for a field, or null. Order of trust: the same
 * field on the same site, the autocomplete meaning, the same question, then a
 * fuzzy question match. Secret fields never match; self-id only when enabled.
 */
export function matchField(field: FieldInfo, bank: Bank, host: string): Match | null {
  if (untouchable(field.kind)) return null;
  const sens = classify(field);
  if (sens === "secret") return null;
  if (sens === "self_id" && !bank.settings.captureSelfId) return null;
  if (motivationEssay(field.label)) return null; // written per posting: Draft and Suggest, never a saved answer

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
  // An answer saved before the entry marker was namespaced sits under "<question> section n".
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
      // Just under an exact hit, so an entry re-saved under the new key outranks its old twin.
      if (legacy && (a.key === legacy || a.labels.includes(legacy))) {
        candidates.push({ a, via: "label", score: 0.89 });
        continue;
      }
      let best = 0;
      // Entry 2 of a repeated section never borrows entry 1's answer (or the reverse).
      for (const l of [a.key, ...a.labels]) {
        for (const r of savedReadings(l)) if (r.n === page.n) best = Math.max(best, jaccard(ft, r.tokens));
      }
      if (best >= MIN_FUZZY) candidates.push({ a, via: "fuzzy", score: 0.5 + 0.35 * best });
    }
  }

  // The same question in other words (intents.ts): any answer saved under a wording of it. Below an
  // exact wording, so a question answered specifically keeps its own answer; among wordings the latest wins.
  const want = intentOf(field.label);
  const intent = want ? intentByKey(want) : undefined;
  if (intent) {
    for (const a of answers) {
      if (usable(a) && [a.key, ...a.labels].some(l => intentOf(l) === want)) candidates.push({ a, via: "intent", score: 0.88 });
    }
  }

  candidates.sort((x, y) => y.score - x.score ||
    (x.via === "intent" && y.via === "intent" ? y.a.updatedAt.localeCompare(x.a.updatedAt) : y.a.uses - x.a.uses));
  for (const c of candidates) {
    const value = valueFor(field, c.a);
    if (!value) continue;
    // A multi-select that lost some saved values to options this page lacks is a partial fill: check it.
    const partial = (field.multi === true || (field.kind === "checkbox" && field.options.length > 1)) && value.length < c.a.value.length;
    // An acknowledgement (intents.ts review) is always checked by hand, however it matched.
    return { answer: c.a, via: c.via, score: c.score, value, review: c.score < CONFIDENT || c.a.review || partial || intent?.review === true };
  }
  return null;
}
