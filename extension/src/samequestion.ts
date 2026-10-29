// Fill's last word on an ambiguous answer. When this site's field id belongs to one saved answer and the
// field's exact wording to another, match.ts fills the exact-wording answer yellow and names the other as
// `alternative`. Before filling, the Mac app's on-device model is asked whether the two wordings are the
// same question: "same" fills the id's answer, "different" fills the exact-wording answer, both confident.
// Anything else (an old app, no model, a refusal, too slow) leaves today's yellow fill. The model sees two
// question labels and nothing else: never a field value, a saved answer or the posting.
import { clean, clip, isRecord } from "./guards";
import { matchField } from "./match";
import { MAX_SAME_LABEL } from "./messages";
import { normalizeLabel, signature } from "./normalize";
import { classify } from "./classify";

/** Work authorization, immigration, agreements and attestations: never settled by the on-device model (normalized text). */
const LEGAL = /\b(sponsor\w*|authori[sz]\w*|visa|citizen\w*|immigra\w*|work permit|right to work|eligib\w*|legally|agree\w*|consent\w*|arbitrat\w*|certif(y|ies|ication)|attest\w*|acknowledg\w*|polic(y|ies)|background check|criminal|convict\w*)\b/;
import type { Answer, Bank, FieldInfo, Match } from "./types";

/** At most this many questions are asked about per Fill. */
export const MAX_SAME_ASKS = 10;
/** How many asks are in flight at once (the host runs one model; more would only queue). */
export const SAME_CONCURRENCY = 2;
/** The whole ask phase, however many asks are left. A fill never waits longer than this for the model. */
export const SAME_PHASE_MS = 8_000;

/** The id's answer, as match.ts sets it on a Match: `wording` is the saved question wording it was captured under. */
export interface Alternative { answer: Answer; value: string[]; wording: string }

/** The alternative a match carries, or null when it has none or it is malformed. */
export function alternativeOf(m: Match | null | undefined): Alternative | null {
  const alt: unknown = m?.alternative; // typed by match.ts; read as unknown because a match may cross a frame or a cache
  if (!isRecord(alt) || !isRecord(alt.answer) || typeof alt.answer.key !== "string") return null;
  if (!Array.isArray(alt.value) || alt.value.length === 0 || !alt.value.every(v => typeof v === "string")) return null;
  if (typeof alt.wording !== "string") return null;
  return { answer: alt.answer as unknown as Answer, value: alt.value as string[], wording: alt.wording };
}

/**
 * What the host said. "down" is a reply that says asking again will not help (an old app that does not know the
 * op, no on-device model, a transport error, junk); "abstained" is the model declining this one pair.
 */
export type Reading = "same" | "different" | "abstained" | "down";

/** The host's reply to `{op: "sameQuestion"}` as a reading; every shape but an answer is "abstained" or "down", never a throw. */
export function parseSameQuestion(x: unknown): Reading {
  if (!isRecord(x)) return "down";
  if (typeof x.error === "string" || x.ok === false) return "down";
  if (x.reading === "answered") return x.value === "same" || x.value === "different" ? x.value : "abstained";
  if (x.reading === "unavailable") return "down";
  if (x.reading === "abstained" || x.reading === "refused" || x.reading === "failed") return "abstained";
  return "down";
}

/** Matches for `refs`: the ones in `carried` as they are, the rest computed. Files and passwords are never matched. */
export function matchAll<R extends { info: FieldInfo }>(
  refs: readonly R[], bank: Bank, host: string, carried?: Map<R, Match | null>,
): Map<R, Match | null> {
  const out = new Map<R, Match | null>();
  for (const r of refs) {
    if (carried?.has(r)) { out.set(r, carried.get(r) ?? null); continue; }
    if (r.info.kind === "file" || r.info.kind === "password") continue;
    out.set(r, matchField(r.info, bank, host));
  }
  return out;
}

export interface SameReport {
  /** Fields with an alternative that Fill was about to write. */
  candidates: number;
  /** Asks sent (at most MAX_SAME_ASKS). */
  asked: number;
  same: number;
  different: number;
  /** True when the phase ended at its deadline with asks unanswered. */
  timedOut: boolean;
}

export interface SameOptions {
  /** Fill will replace what the person typed, so a field with text is still a candidate. */
  overwrite?: boolean;
  maxAsks?: number;
  concurrency?: number;
  phaseMs?: number;
  /**
   * The bank and host the matches came from. With them a settled match keeps every reason to check it that is not the
   * doubt itself (an acknowledgement, a multi-select that lost values), taken from match.ts's own walk run on a bank where
   * the doubt cannot arise. Without them the match is built from the answers alone.
   */
  bank?: Bank;
  host?: string;
}

/**
 * The match for `field` on a copy of the bank from which the other answer has been taken out of the way: the one
 * that lost (same: the exact wording's answer) or the id's signature (different: so the id no longer disputes it).
 * match.ts's own walk decides, so a reason to check the answer that has nothing to do with the doubt still stands.
 * Null when the walk does not land on `want`, and the caller falls back.
 */
function rematch(field: FieldInfo, bank: Bank, host: string, patch: (a: Record<string, Answer>) => void, want: Answer): Match | null {
  const answers = { ...bank.answers };
  patch(answers);
  const m = matchField(field, { ...bank, answers }, host);
  return m && m.answer.key === want.key && m.alternative === undefined ? m : null;
}

/** The match Fill writes once the model has said the two wordings ask the same question. */
function idsAnswer(field: FieldInfo, alt: Alternative, own: Match, o: SameOptions): Match {
  const walked = o.bank && o.host ? rematch(field, o.bank, o.host, a => { delete a[own.answer.key]; }, alt.answer) : null;
  return walked ?? { answer: alt.answer, via: "signature", score: own.score, value: alt.value, review: alt.answer.review === true };
}

/** The match Fill writes once the model has said they differ: the exact wording's own answer, no longer in doubt. */
function wordingsAnswer(field: FieldInfo, alt: Alternative, own: Match, o: SameOptions): Match {
  const sig = signature(o.host ?? "", field);
  const walked = o.bank && o.host && sig
    ? rematch(field, o.bank, o.host, a => {
      const held = a[alt.answer.key];
      if (held) a[alt.answer.key] = { ...held, signatures: held.signatures.filter(s => s !== sig) };
    }, own.answer)
    : null;
  if (walked) return walked;
  const out: Match = { ...own, review: own.answer.review === true };
  delete out.alternative;
  delete out.note;
  return out;
}

/**
 * Settle each ambiguous match in `matches` (replacing it in place) by asking `ask(fieldLabel, savedWording)`.
 * At most `maxAsks` asks, `concurrency` at a time, and the whole phase ends at `phaseMs`: an answer that arrives
 * after that is dropped, and anything unanswered keeps its yellow match. A reply that says the model is down
 * (an app too old to know the op, no model) stops further asks, since they would fail the same way. Never throws.
 */
export async function settleAmbiguous<R extends { info: FieldInfo; manual: string }>(
  refs: readonly R[], matches: Map<R, Match | null>, ask: (a: string, b: string) => Promise<unknown>, opts: SameOptions = {},
): Promise<SameReport> {
  const maxAsks = opts.maxAsks ?? MAX_SAME_ASKS;
  const phaseMs = opts.phaseMs ?? SAME_PHASE_MS;
  const todo: Array<{ ref: R; own: Match; alt: Alternative; a: string; b: string }> = [];
  const report: SameReport = { candidates: 0, asked: 0, same: 0, different: 0, timedOut: false };
  for (const ref of refs) {
    const own = matches.get(ref);
    const alt = alternativeOf(own);
    if (!own || !alt || ref.manual) continue;
    // Fill leaves a field that has text alone, so asking about it would only spend the model's time.
    if (!opts.overwrite && ref.info.value.some(v => v.trim() !== "")) continue;
    // A legal or sensitive question turns on one word ("authorized to work *without* sponsorship"), which a small
    // model may read past: it keeps its yellow fill, for the person to read, whatever the model would say.
    if (classify(ref.info) !== "standard" || LEGAL.test(normalizeLabel(ref.info.label)) || LEGAL.test(normalizeLabel(alt.wording))) continue;
    report.candidates++;
    const a = clip(clean(ref.info.label), MAX_SAME_LABEL);
    const b = clip(clean(alt.wording), MAX_SAME_LABEL);
    if (a === "" || b === "") continue;
    todo.push({ ref, own, alt, a, b });
  }
  if (todo.length === 0) return report;
  const queue = todo.slice(0, Math.max(0, maxAsks));
  let next = 0;
  let halted = false;
  let closed = false;
  const worker = async (): Promise<void> => {
    while (!halted && !closed && next < queue.length) {
      const item = queue[next++]!;
      report.asked++;
      let reading: Reading;
      try {
        reading = parseSameQuestion(await ask(item.a, item.b));
      } catch {
        reading = "down";
      }
      if (closed) return; // the phase is over: a late answer is not applied
      if (reading === "same") { matches.set(item.ref, idsAnswer(item.ref.info, item.alt, item.own, opts)); report.same++; }
      else if (reading === "different") { matches.set(item.ref, wordingsAnswer(item.ref.info, item.alt, item.own, opts)); report.different++; }
      else if (reading === "down") halted = true;
    }
  };
  let timer: ReturnType<typeof setTimeout> | undefined;
  const deadline = new Promise<"deadline">(resolve => { timer = setTimeout(() => resolve("deadline"), phaseMs); });
  const pool = Promise.all(Array.from({ length: Math.max(1, Math.min(opts.concurrency ?? SAME_CONCURRENCY, queue.length)) }, worker));
  try {
    if ((await Promise.race([pool.then(() => "done" as const), deadline])) === "deadline") report.timedOut = true;
  } finally {
    closed = true;
    clearTimeout(timer);
  }
  return report;
}
