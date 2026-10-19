import { type Answer, type Bank, type FieldInfo, type Match, CHOICE_KINDS } from "./types";
import { autocompleteKey, jaccard, normalizeLabel, signature, tokens } from "./normalize";
import { classify, untouchable } from "./classify";
import { entryReadings, legacyEntryKey } from "./sections";

/** Below this a fuzzy label match is not trusted at all. */
export const MIN_FUZZY = 0.6;
/** Matches scoring below this are filled but marked for review. */
export const CONFIDENT = 0.85;

const YES = new Set(["yes", "y", "true", "i do", "i am", "i agree", "agree", "checked"]);
const NO = new Set(["no", "n", "false", "i do not", "i am not", "unchecked"]);

/**
 * Map a remembered choice onto this field's options: exact label first, then
 * yes/no meaning, then the closest option by tokens. A value with no
 * acceptable option maps to nothing, so the field is left alone.
 */
export function mapChoice(value: string, options: string[]): string | null {
  const v = normalizeLabel(value);
  const norm = options.map(o => normalizeLabel(o));
  const exact = norm.indexOf(v);
  if (exact >= 0) return options[exact]!;
  const polarity = YES.has(v) ? "yes" : NO.has(v) ? "no" : "";
  if (polarity) {
    const set = polarity === "yes" ? YES : NO;
    const i = norm.findIndex(o => set.has(o) || o.startsWith(polarity + " "));
    if (i >= 0) return options[i]!;
  }
  const vt = tokens(value);
  let best = -1, bestScore = 0;
  norm.forEach((o, i) => {
    const s = jaccard(vt, tokens(o));
    if (s > bestScore) { best = i; bestScore = s; }
  });
  return best >= 0 && bestScore >= 0.6 ? options[best]! : null;
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
        for (const r of entryReadings(l, true)) if (r.n === page.n) best = Math.max(best, jaccard(ft, tokens(r.base)));
      }
      if (best >= MIN_FUZZY) candidates.push({ a, via: "fuzzy", score: 0.5 + 0.35 * best });
    }
  }

  candidates.sort((x, y) => y.score - x.score || y.a.uses - x.a.uses);
  for (const c of candidates) {
    const value = valueFor(field, c.a);
    if (!value) continue;
    // A multi-select that lost some saved values to options this page lacks is a partial fill: check it.
    const partial = (field.multi === true || (field.kind === "checkbox" && field.options.length > 1)) && value.length < c.a.value.length;
    return { answer: c.a, via: c.via, score: c.score, value, review: c.score < CONFIDENT || c.a.review || partial };
  }
  return null;
}
