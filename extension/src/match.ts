import { type Answer, type Bank, type FieldInfo, type Match, CHOICE_KINDS } from "./types";
import { autocompleteKey, contactOf, jaccard, normalizeLabel, signature, tokens, withoutDialCode } from "./normalize";
import { classify, untouchable } from "./classify";
import { entryReadings, legacyEntryKey } from "./sections";
import { bankIndexFor, fuzzyCandidates } from "./bankindex";
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

/** Options that differ only in case and spacing are one option shown twice. */
const sameText = (a: string, b: string): boolean => a.trim().replace(/\s+/g, " ").toLowerCase() === b.trim().replace(/\s+/g, " ").toLowerCase();

/** The one option among `hits`, or null when they are different options ("C", "C++" and "C#" all normalize to "c"). */
function onlyOne(options: string[], hits: number[]): string | null {
  if (hits.length === 0) return null;
  const first = options[hits[0]!]!;
  return hits.every(i => sameText(options[i]!, first)) ? first : null;
}

/**
 * Map a remembered choice onto this field's options: the option with exactly this text first, then the same label
 * once normalized (then once dialling codes are set aside), then declining (a saved "decline" takes the form's own
 * decline option, and nothing else), then yes/no meaning, then the closest option by tokens. Normalizing drops
 * punctuation, so where it makes two different options read alike (C++, C#) nothing is guessed. A value with no
 * acceptable option maps to nothing, so the field is left alone.
 */
export function mapChoice(value: string, options: string[]): string | null {
  const literal = options.findIndex(o => o.trim() === value.trim());
  if (literal >= 0) return options[literal]!;
  const v = normalizeLabel(value);
  const norm = options.map(o => normalizeLabel(o));
  const exacts = norm.flatMap((o, i) => (o === v ? [i] : []));
  if (exacts.length > 0) return onlyOne(options, exacts);
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
  const scores = options.map(o => jaccard(vt, tokens(withoutDialCode(o))));
  const bestScore = Math.max(0, ...scores);
  if (bestScore < 0.6) return null;
  return onlyOne(options, scores.flatMap((s, i) => (s === bestScore ? [i] : [])));
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
export function matchField(field: FieldInfo, bank: Bank, host: string, now: number = Date.now()): Match | null {
  return walk(field, bank, host, now);
}

/**
 * Why a field got no answer when one was refused, for the person to read next to the question; null when nothing
 * was refused (no saved answer, a secret field, an answer that is only a weak resemblance) or when the field is
 * filled. Non-null exactly when matchField returns null because its best candidate was refused: a saved date that
 * has passed, or a saved choice this form does not offer. It runs the same walk as matchField, so the two cannot
 * disagree about what was refused.
 */
export function whyNoMatch(field: FieldInfo, bank: Bank, host: string, now: number = Date.now()): string | null {
  const sink: Refusals = {};
  if (walk(field, bank, host, now, sink)) return null;
  const r = sink.refusal;
  if (!r) return null;
  return r.kind === "past"
    ? `Your saved ${r.noun} (${clipSaved(r.saved)}) has passed: enter a new one.`
    : `Your saved answer "${clipSaved(r.saved)}" is not one of this form's options.`;
}

/** A candidate the walk refused for a reason worth telling the person (whyNoMatch); only filled when asked for. */
interface Refusals {
  refusal?: { kind: "past"; saved: string; noun: string } | { kind: "options"; saved: string };
}

/** What a dated question is called in the message: its intent's own noun, else just "date" (today's date, a signature date). */
const DATED_NOUN: Readonly<Record<string, string>> = { "earliest start date": "start date", "deadlines or timeline considerations": "deadline" };

/** A saved value as the person typed it, at most 40 characters (counted in code points, so no character is cut in two). */
function clipSaved(s: string): string {
  const chars = [...s.trim()];
  return chars.length <= 40 ? chars.join("") : chars.slice(0, 39).join("") + "…";
}

/** A choice field that lists options of its own: a saved value with none of them there is a refusal worth naming. */
function listsOptions(field: FieldInfo): boolean {
  return CHOICE_KINDS.has(field.kind) && field.options.length > 0 && !(field.kind === "checkbox" && field.options.length <= 1 && field.multi !== true);
}

/** The one walk behind matchField and whyNoMatch. `sink`, when given, is told what was refused; matching never reads it. */
function walk(field: FieldInfo, bank: Bank, host: string, now: number, sink?: Refusals): Match | null {
  if (untouchable(field.kind)) return null;
  const sens = classify(field);
  if (sens === "secret") return null;
  if (sens === "self_id" && !bank.settings.captureSelfId) return null;
  if (motivationEssay(field.label)) return null; // written per posting: Draft and Suggest, never a saved answer

  // Candidates come from a per-bank index (bankindex.ts) instead of a scan of every answer; each
  // list is in bank order, so the candidate order (and so every tie) is the full scan's.
  const idx = bankIndexFor(bank);
  const answers = idx.answers;
  const usable = (a: Answer) => a.sensitivity !== "secret" && (a.sensitivity !== "self_id" || bank.settings.captureSelfId);
  const candidates: Array<{ a: Answer; via: Match["via"]; score: number }> = [];

  const label = normalizeLabel(field.label);
  // An answer saved before the entry marker was namespaced sits under "<question> section n".
  const legacy = legacyEntryKey(label);
  const [page] = entryReadings(label, false);

  const stamp = (a: Answer) => Date.parse(a.updatedAt) || 0;
  // Set when this site's id for the field and the field's exact wording name two different answers: the site
  // either reworded one question or gave its id to another, and nothing here can tell which. `held` is the
  // newest answer the id named and the wording overruled: the Match carries it as its alternative.
  let ambiguous = false;
  let held = -1;
  const sig = signature(host, field);
  if (sig) {
    const ft = page ? tokens(page.base) : new Set<string>();
    const worded = label ? (idx.byKeyOrLabel.get(label) ?? []).filter(i => usable(answers[i]!)) : [];
    const readsAsThis = (i: number) => worded.includes(i) || ft.size === 0 || idx.readings[i]!.some(r => jaccard(ft, r.tokens) >= MIN_FUZZY);
    for (const i of idx.bySignature.get(sig) ?? []) {
      if (!usable(answers[i]!)) continue;
      // The id vouches on its own when no other answer has this exact wording (a reworded question keeps its id).
      if (worded.length > 0 && !readsAsThis(i)) {
        ambiguous = true;
        if (held < 0 || stamp(answers[i]!) > stamp(answers[held]!)) held = i;
        continue;
      }
      candidates.push({ a: answers[i]!, via: "signature", score: 1 });
    }
  }
  const ak = autocompleteKey(field.autocomplete);
  if (ak && bank.answers[ak] && usable(bank.answers[ak]!)) candidates.push({ a: bank.answers[ak]!, via: "autocomplete", score: 0.95 });
  if (label && page) {
    const ft = tokens(page.base);
    const exact = new Set(idx.byKeyOrLabel.get(label) ?? []);
    const old = new Set(legacy ? idx.byKeyOrLabel.get(legacy) ?? [] : []);
    // Exact and legacy hits are looked up; the rest are fuzzy, but only answers sharing a token with
    // the field can reach MIN_FUZZY (see fuzzyCandidates). Merged back into bank order.
    const near = fuzzyCandidates(idx, ft);
    const hit = [...new Set([...exact, ...old, ...near])].sort((x, y) => x - y);
    for (const i of hit) {
      const a = answers[i]!;
      if (!usable(a)) continue;
      if (exact.has(i)) {
        candidates.push({ a, via: "label", score: 0.9 });
        continue;
      }
      // Just under an exact hit, so an entry re-saved under the new key outranks its old twin.
      if (old.has(i)) {
        candidates.push({ a, via: "label", score: 0.89 });
        continue;
      }
      let best = 0;
      // Entry 2 of a repeated section never borrows entry 1's answer (or the reverse).
      for (const r of idx.readings[i]!) if (r.n === page.n) best = Math.max(best, jaccard(ft, r.tokens));
      if (best >= MIN_FUZZY) candidates.push({ a, via: "fuzzy", score: 0.5 + 0.35 * best });
    }
  }

  // The same question in other words (intents.ts): any answer saved under a wording of it. Below an
  // exact wording, so a question answered specifically keeps its own answer; among wordings the latest wins.
  const want = intentOf(field.label);
  const intent = want ? intentByKey(want) : undefined;
  if (intent) {
    for (const i of idx.byIntent.get(want) ?? []) {
      if (usable(answers[i]!)) candidates.push({ a: answers[i]!, via: "intent", score: 0.88 });
    }
  }

  // A contact basic (name, email, phone, city...) is one question however it was saved: under its autocomplete
  // key by the popup, or under the form's own label. Answers to it compete as one concept, like an intent's.
  const contact = want ? "" : ak || contactOf(field.label);
  if (contact) {
    for (const i of idx.byContact.get(contact) ?? []) {
      if (usable(answers[i]!)) candidates.push({ a: answers[i]!, via: "autocomplete", score: 0.9 });
    }
  }
  const member = (a: Answer) => want ? idx.intentsOf.get(a)?.has(want) === true : contact !== "" && idx.contactsOf.get(a)?.has(contact) === true;
  // A direct claim on this question (this field's id, its exact wording, its declared meaning) or an answer to
  // its concept. The user's latest word among them wins, then the more used: how often an old answer was
  // confirmed must not outweigh a correction. Everything below is a looser resemblance, best score first.
  const direct = (c: { a: Answer; score: number }) => c.score >= 0.9 || member(c.a);
  candidates.sort((x, y) => {
    const dx = direct(x), dy = direct(y);
    if (dx !== dy) return dx ? -1 : 1;
    if (!dx && x.score !== y.score) return y.score - x.score;
    return stamp(y.a) - stamp(x.a) || y.a.uses - x.a.uses || (x.a.key < y.a.key ? -1 : x.a.key > y.a.key ? 1 : 0) || y.score - x.score;
  });
  for (const c of candidates) {
    const value = valueFor(field, c.a);
    if (!value) {
      // The best claim on this question has no option here. An older answer to the same question would be a
      // superseded one, so a confident candidate that cannot map ends the search.
      if (c.score >= CONFIDENT) {
        const saved = c.a.value.map(v => v.trim()).filter(Boolean).join(", ");
        if (sink && saved && listsOptions(field)) sink.refusal = { kind: "options", saved };
        return null;
      }
      continue;
    }
    const age = ageOf(field, want, idx.intentsOf.get(c.a), c.a, value, now);
    if (age === "past") { // a date that has gone by is not an answer
      if (sink) {
        const keys = want ? [want] : [...(idx.intentsOf.get(c.a) ?? [])];
        sink.refusal = { kind: "past", saved: value.join(" "), noun: keys.map(k => DATED_NOUN[k]).find(n => n !== undefined) ?? "date" };
      }
      return null;
    }
    // A multi-select that lost some saved values to options this page lacks is a partial fill: check it.
    const partial = (field.multi === true || (field.kind === "checkbox" && field.options.length > 1)) && value.length < c.a.value.length;
    // An acknowledgement (intents.ts review) is always checked by hand, however it matched.
    const m: Match = { answer: c.a, via: c.via, score: c.score, value, review: c.score < CONFIDENT || c.a.review || partial || intent?.review === true || age !== null || ambiguous };
    if (age) m.note = age.note;
    else if (ambiguous) m.note = "This site used this field for a differently worded question too: check it is the right answer.";
    if (held >= 0) {
      // The id's answer, as this field would take it, for a caller that can tell whether the two wordings are one
      // question. Not offered when it would be stale (a past date, an aged answer): that is a yellow fill at best.
      const x = answers[held]!;
      const xv = x === c.a ? null : valueFor(field, x);
      if (xv && ageOf(field, want, idx.intentsOf.get(x), x, xv, now) === null) m.alternative = { answer: x, value: xv, wording: x.labels.at(-1) ?? x.key };
    }
    return m;
  }
  return null;
}

const DAY_MS = 86_400_000;
const MONTHS = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];
/** Questions about today or the moment of signing: a date saved for one form is wrong on the next. */
const DATED_NOW = /\b(todays date|current date|signature date|date signed|date of signature|signed on|application date|date of application|date applied)\b/;

/** The calendar date `s` is as UTC milliseconds, or null when it is not exactly one (ISO, M/D/YYYY, "Oct 5, 2026"). */
function parseDate(s: string): number | null {
  const v = s.trim();
  let y: number, m: number, d: number;
  let hit: RegExpMatchArray | null;
  if ((hit = /^(\d{4})-(\d{2})-(\d{2})(?:[T ].*)?$/.exec(v))) [y, m, d] = [+hit[1]!, +hit[2]!, +hit[3]!];
  else if ((hit = /^(\d{1,2})\/(\d{1,2})\/(\d{4})$/.exec(v))) [y, m, d] = [+hit[3]!, +hit[1]!, +hit[2]!];
  else if ((hit = /^([A-Za-z]{3,9})\.? (\d{1,2}),? (\d{4})$/.exec(v))) [y, m, d] = [+hit[3]!, MONTHS.indexOf(hit[1]!.slice(0, 3).toLowerCase()) + 1, +hit[2]!];
  else return null;
  const t = Date.UTC(y, m - 1, d);
  const back = new Date(t);
  return m >= 1 && back.getUTCFullYear() === y && back.getUTCMonth() === m - 1 && back.getUTCDate() === d ? t : null;
}

/**
 * Whether the answer has gone stale for this field. "past": a date that has gone by, for a question that wants
 * a future one (a start date, a deadline, today's date). A note: it changes with the calendar (an intent's
 * staleAfterDays) and was saved longer ago than that, so it is filled for review with its age. null: current.
 * The intents are the field's own, or the answer's wordings' when the field reads as none.
 */
export function ageOf(field: FieldInfo, want: string, answerIntents: ReadonlySet<string> | undefined, a: Answer, value: string[], now: number): "past" | { note: string } | null {
  const keys = want ? [want] : [...(answerIntents ?? [])];
  const intents = keys.flatMap(k => intentByKey(k) ?? []);
  const dated = intents.some(i => i.dated) || DATED_NOW.test(normalizeLabel(`${field.label} ${field.name}`)) || normalizeLabel(field.label) === "date";
  if (dated) {
    const when = parseDate(value.join(" "));
    // Today as the person's own calendar has it, in the UTC-midnight form parseDate gives: an evening in the US is
    // already tomorrow in UTC, and a start date of today is not past.
    const local = new Date(now);
    if (when !== null && when < Date.UTC(local.getFullYear(), local.getMonth(), local.getDate())) return "past";
  }
  const limits = intents.flatMap(i => i.staleAfterDays ?? []);
  if (limits.length === 0) return null;
  const saved = Date.parse(a.updatedAt);
  if (!Number.isFinite(saved)) return { note: "Saved at an unknown time: check it is still right." };
  const days = Math.floor((now - saved) / DAY_MS);
  if (days <= Math.min(...limits)) return null;
  return { note: `Saved ${days} ${days === 1 ? "day" : "days"} ago: check it is still right.` };
}
