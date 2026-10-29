// A lookup index over one answer bank, built once per bank object so matching a field does not scan
// every saved answer. It only narrows the answers matchField has to look at; the scoring and the
// sort stay in match.ts, and every candidate list here is in bank order (the order Object.values
// gives), which is the order the full scan pushed candidates in, so ties sort exactly as before.
//
// The index belongs to the Bank object it was built for. Banks are replaced, not edited in place
// (store.ts and bank.ts write copies); as a guard against an in-place add, remove or replace of an
// answer, `bankIndexFor` also checks that the answer list is still the same objects in the same
// order and rebuilds if not. An edit inside one answer (its labels or signatures) is not detectable
// without re-reading everything; callers that do that must pass a new Bank object.
import type { Answer, Bank } from "./types";
import { AUTOCOMPLETE_MEANINGS, contactOf, tokens } from "./normalize";
import { entryReadings } from "./sections";
import { intentOf } from "./intents";

export type Reading = { n: number; tokens: Set<string> };

/**
 * A saved wording's entry readings with their tokens, by wording: every field of every rescan
 * compares against every saved wording, and these never change. Bounded; cleared when full.
 */
const readings = new Map<string, Reading[]>();
const MAX_READINGS = 8192;

function savedReadings(label: string): Reading[] {
  let r = readings.get(label);
  if (r) return r;
  r = entryReadings(label, true).map(e => ({ n: e.n, tokens: tokens(e.base) }));
  if (readings.size >= MAX_READINGS) readings.clear();
  readings.set(label, r);
  return r;
}

export interface BankIndex {
  /** Object.values(bank.answers), the order the full scan used. */
  readonly answers: readonly Answer[];
  /** Answer positions by host-scoped signature. */
  readonly bySignature: ReadonlyMap<string, readonly number[]>;
  /** Answer positions by exact key or label text (a stored wording is already normalized). */
  readonly byKeyOrLabel: ReadonlyMap<string, readonly number[]>;
  /** Answer positions by the intent any of its wordings reads as. */
  readonly byIntent: ReadonlyMap<string, readonly number[]>;
  /** The intents of an answer's wordings, for "is this answer for the field's intent". */
  readonly intentsOf: ReadonlyMap<Answer, ReadonlySet<string>>;
  /** Answer positions by the contact basic (or autocomplete meaning) any of its wordings is: "email", "postal code". */
  readonly byContact: ReadonlyMap<string, readonly number[]>;
  /** The contact meanings of an answer's wordings. */
  readonly contactsOf: ReadonlyMap<Answer, ReadonlySet<string>>;
  /** Entry readings of every wording of each answer ([key, ...labels]), by position. */
  readonly readings: ReadonlyArray<readonly Reading[]>;
  /** Answer positions with at least one wording token, by token. */
  readonly byToken: ReadonlyMap<string, readonly number[]>;
}

let builds = 0;
/** How many indexes have been built in this process; for tests that assert a bank is indexed once. */
export function bankIndexBuilds(): number { return builds; }

function push<K>(m: Map<K, number[]>, k: K, i: number): void {
  const l = m.get(k);
  if (!l) m.set(k, [i]);
  else if (l[l.length - 1] !== i) l.push(i); // positions arrive ascending: dedupe per answer by the tail
}

function build(answers: Answer[]): BankIndex {
  builds++;
  const bySignature = new Map<string, number[]>();
  const byKeyOrLabel = new Map<string, number[]>();
  const byIntent = new Map<string, number[]>();
  const byToken = new Map<string, number[]>();
  const byContact = new Map<string, number[]>();
  const intentsOf = new Map<Answer, Set<string>>();
  const contactsOf = new Map<Answer, Set<string>>();
  const rd: Reading[][] = [];
  answers.forEach((a, i) => {
    for (const s of a.signatures ?? []) push(bySignature, s, i);
    const words = [a.key, ...(a.labels ?? [])];
    const intents = intentsOf.get(a) ?? new Set<string>();
    const contacts = contactsOf.get(a) ?? new Set<string>();
    const mine: Reading[] = [];
    for (const w of words) {
      push(byKeyOrLabel, w, i);
      const it = intentOf(w);
      if (it) { intents.add(it); push(byIntent, it, i); }
      const ct = contactOf(w) || (AUTOCOMPLETE_MEANINGS.has(w) ? w : "");
      if (ct) { contacts.add(ct); push(byContact, ct, i); }
      for (const r of savedReadings(w)) {
        mine.push(r);
        for (const t of r.tokens) push(byToken, t, i);
      }
    }
    intentsOf.set(a, intents);
    contactsOf.set(a, contacts);
    rd.push(mine);
  });
  return { answers, bySignature, byKeyOrLabel, byIntent, intentsOf, byContact, contactsOf, readings: rd, byToken };
}

const cache = new WeakMap<Bank, BankIndex>();

/** The index for this bank, built on first use and reused until the bank's answers change. */
export function bankIndexFor(bank: Bank): BankIndex {
  const now = Object.values(bank.answers);
  const have = cache.get(bank);
  if (have && have.answers.length === now.length && now.every((a, i) => a === have.answers[i])) return have;
  const made = build(now);
  cache.set(bank, made);
  return made;
}

/**
 * Positions of the answers whose fuzzy score against a field reading with tokens `ft` can reach
 * MIN_FUZZY (match.ts): those with a wording reading that shares a token with `ft`.
 *
 * Exactness: jaccard(a, b) = |a ∩ b| / |a ∪ b| is 0 when the sets share no token, and MIN_FUZZY is
 * greater than 0, so an answer none of whose reading tokens appear in `ft` can never be a fuzzy
 * candidate. Every answer with a shared token is returned (a superset), and matchField then runs
 * the unchanged jaccard test on each, so the survivors are exactly the full scan's. Ascending.
 */
export function fuzzyCandidates(idx: BankIndex, ft: ReadonlySet<string>): number[] {
  const seen = new Set<number>();
  for (const t of ft) for (const i of idx.byToken.get(t) ?? []) seen.add(i);
  return [...seen].sort((x, y) => x - y);
}
