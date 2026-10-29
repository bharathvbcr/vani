// Question intents: the questions nearly every application asks in its own
// words ("Do you require visa sponsorship?", "Will you now or in the future
// require sponsorship...?"). One saved answer to an intent fills every wording
// of it (match.ts), and the popup's basics offer each intent as one box
// (basics.ts). Storage keys are unchanged: an answer is found by intent when it
// is read, so a hand-typed answer under its own wording counts too.
//
// A wrong intent fills a confident wrong answer, so the patterns are narrow and
// a label that reads as two intents (authorized to work *without* sponsorship:
// opposite answers) reads as none, and so does one that names another country or asks the
// opposite question ("without needing sponsorship"): the answer saved for the usual wording is not its answer.
import { normalizeLabel } from "./normalize";

/** How the popup asks for the answer. */
export type IntentAnswer = "yesno" | "text" | "choice";

export interface Intent {
  /** normalizeLabel(label): the key a basic saved under `label` is stored at. */
  key: string;
  /** The wording the popup shows and saves the answer under; it must resolve to this intent. */
  label: string;
  answer: IntentAnswer;
  /** For "choice": the options the popup offers (mapped onto each form's own options by match.mapChoice). */
  options?: readonly string[];
  /** Run on normalizeLabel(question); any match is this intent. */
  patterns: readonly RegExp[];
  /** An acknowledgement or agreement: always filled yellow, for review. */
  review?: true;
  /** Voluntary self-identification (classify.ts): offered and filled only with self-ID turned on. */
  selfId?: true;
  /** A question that matches any of these is not this intent: its answer would mean the opposite ("without sponsorship"). */
  unless?: readonly RegExp[];
  /** The answer is about the US: a question that names another country is not this intent (FOREIGN). */
  usOnly?: true;
  /** A saved answer older than this many days is filled for review (match.ts): it changes with the calendar. */
  staleAfterDays?: number;
  /** The answer may be a calendar date: one already past is not filled at all (match.ts). */
  dated?: true;
}

const DECLINE = "Decline to self-identify";

/**
 * Countries and blocs other than the US, on normalizeLabel text ("U.K." reads "u k"). A work-authorization,
 * sponsorship or relocation answer given for the US says nothing about them, so such a question is no intent.
 */
const FOREIGN = new RegExp("\\b(" + [
  "uk", "u k", "united kingdom", "great britain", "britain", "england", "scotland", "wales", "northern ireland", "ireland",
  "eu", "e u", "european union", "europe", "eea", "canada", "mexico", "india", "germany", "france", "spain", "italy",
  "netherlands", "holland", "belgium", "switzerland", "austria", "sweden", "norway", "denmark", "finland", "poland",
  "portugal", "australia", "new zealand", "singapore", "japan", "china", "hong kong", "korea", "israel", "brazil",
  "argentina", "uae", "united arab emirates", "south africa", "nigeria", "kenya", "philippines", "pakistan",
  "bangladesh", "vietnam", "indonesia", "malaysia", "thailand", "taiwan", "turkey", "czechia", "romania", "ukraine",
  "greece", "hungary",
].join("|") + ")\\b");

function intent(label: string, answer: IntentAnswer, patterns: RegExp[], extra: Partial<Intent> = {}): Intent {
  return { key: normalizeLabel(label), label, answer, patterns, ...extra };
}

export const INTENTS: readonly Intent[] = [
  intent("Authorized to work", "yesno", [
    /\b(authori[sz]ed|eligible|permitted|legally able|entitled) to work\b/,
    /\bwork authori[sz]ation\b/,
    /\bright to work\b/,
  ], {
    usOnly: true,
    unless: [
      /\b(not|unable to|cannot|cant) (be )?(legally )?(authori[sz]ed|eligible|permitted|entitled)\b/,
      /\bwithout (work )?authori[sz]ation\b/,
    ],
  }),
  intent("Visa sponsorship", "yesno", [/\bsponsor(ship|ed|ing)?\b/], {
    usOnly: true,
    unless: [
      /\bwithout\b.*\bsponsor/,
      /\b(not|no longer|never|dont|wont|cannot|cant)\b.{0,30}\b(requir\w*|need\w*)\b.{0,30}\bsponsor/,
      /\bno (visa )?sponsorship\b/,
      /\bsponsorship\b.{0,20}\bnot (required|needed|necessary)\b/,
    ],
  }),
  intent("Open to relocation", "yesno", [
    /\b(open|willing|able|consider|considering|happy)\b.*\breloca(te|tion|ting)\b/,
  ], { usOnly: true }),
  intent("Open to working in person", "yesno", [
    /\b(open|willing|able|comfortable|happy|commit|agree)\b.*\b(in person|onsite|on site|in office|in the office|in (one of )?(our|the|an|a) offices?|from (our|the) [a-z ]*office|hybrid)\b/,
    /\b(in person|onsite|on site|hybrid)\b.*\b(open|willing|able|comfortable)\b/,
  ]),
  intent("Where you'll work from", "text", [
    /\baddress from which you\b/,
    /\bwhere (youll|you will|will you) (be )?(work|working|based)( from)?\b/,
    /\bwhere (are|do) you (currently )?(located|based|live|living)\b/,
    /\b(your|current) (current )?location\b/,
  ], { staleAfterDays: 180 }),
  intent("Interviewed here before", "yesno", [
    /\b(ever|previously|before|past)\b.*\binterviewed (at|with|for|here)\b/,
    /\binterviewed (at|with|for|here)\b.*\b(before|previously|in the past)\b/,
  ]),
  intent("Applied here before", "yesno", [
    /\b(ever|previously|before|past)\b.*\bapplied (to|at|with|here)\b/,
    /\bapplied (to|at|with|here)\b.*\b(before|previously|in the past)\b/,
  ]),
  intent("Earliest start date", "text", [
    /\b(earliest|soonest)\b.*\bstart\b/,
    /\b(when|how soon)\b.*\b(can|could|would|able to)\b.*\bstart\b/,
    /\b(desired|preferred|expected|available|availability|potential) start\b/,
    /\bavailab\w*\b.*\bto start\b/,
    /\bnotice period\b/,
  ], { staleAfterDays: 21, dated: true }),
  intent("Deadlines or timeline considerations", "text", [
    /\bdeadlines?\b/,
    /\btimeline\b/,
    /\b(other|competing|pending|outstanding) offers?\b/,
  ], { staleAfterDays: 21, dated: true }),
  intent("How did you hear about us", "text", [
    /\bhow did you (hear|learn|find out|come across|discover)\b/,
    /\bwhere did you (hear|learn|find|see)\b/,
  ]),
  intent("Salary expectations", "text", [
    /\b(salary|compensation|pay)\b.*\b(expectations?|requirements?|range|desired|expected)\b/,
    /\b(expected|desired|target) (salary|compensation|pay|base)\b/,
  ], { staleAfterDays: 90 }),
  intent("AI policy for applications", "yesno", [
    /\bai\b.*\bpolic(y|ies)\b/,
    /\bpolic(y|ies)\b.*\bai\b/,
    /\b(use of|using) (ai|artificial intelligence)\b/,
  ], { review: true }),
  // A legal waiver: opt-in (the box is empty until you choose "I agree"), and always filled yellow, to read.
  intent("Arbitration agreement", "choice", [/\barbitrat(e|ion|or)\b/], { review: true, options: ["I agree"] }),
  intent("Gender", "choice", [/\bgender\b/], { selfId: true, options: [DECLINE, "Male", "Female", "Non-binary"] }),
  intent("Hispanic or Latino", "choice", [/\bhispanic\b/, /\blatin[oax]\b/], { selfId: true, options: [DECLINE, "Yes", "No"] }),
  intent("Race", "choice", [/\brace\b/, /\bracial\b/], {
    selfId: true,
    options: [DECLINE, "American Indian or Alaska Native", "Asian", "Black or African American", "Native Hawaiian or Other Pacific Islander", "White", "Two or more races"],
  }),
  intent("Veteran status", "choice", [/\bveteran\b/], {
    selfId: true,
    options: [DECLINE, "I am not a protected veteran", "I identify as one or more of the classifications of protected veteran"],
  }),
  intent("Disability status", "choice", [/\bdisabilit(y|ies)\b/, /\bdisabled\b/], {
    selfId: true,
    options: [DECLINE, "No, I do not have a disability", "Yes, I have a disability"],
  }),
];

const BY_KEY = new Map(INTENTS.map(i => [i.key, i]));

/** A later entry of a repeated section ("... (vani section 2)") keeps its own key and never reads as an intent. */
const SECTION_ENTRY = /\bvani section \d+$/;

/**
 * Answers already read, by question text: Fill asks for every saved wording on every field of every
 * rescan (each DOM change), and the answer for a text never changes. Bounded; cleared when full.
 */
const seen = new Map<string, string>();
const MAX_SEEN = 8192;

/** The intent a question asks, or "" when it asks none, or reads as more than one. */
export function intentOf(question: string): string {
  const hit = seen.get(question);
  if (hit !== undefined) return hit;
  const found = readIntent(question);
  if (seen.size >= MAX_SEEN) seen.clear();
  seen.set(question, found);
  return found;
}

function readIntent(question: string): string {
  const q = normalizeLabel(question);
  if (!q || SECTION_ENTRY.test(q)) return "";
  let found = "";
  for (const i of INTENTS) {
    if (!i.patterns.some(p => p.test(q))) continue;
    if (i.usOnly && FOREIGN.test(q)) return ""; // an answer for the US says nothing about another country
    if (i.unless?.some(p => p.test(q))) return ""; // phrased as the opposite question: its answer may be too
    if (found) return ""; // two intents: their answers may be opposite
    found = i.key;
  }
  return found;
}

/**
 * Why this company, this role, this mission. An answer to one is written for
 * one posting, and every Greenhouse company shares one host, so a saved answer
 * cannot tell which company it was for: Fill leaves these to Draft and Suggest,
 * which write from the posting on the page.
 */
const MOTIVATION: readonly RegExp[] = [
  /^why( [a-z0-9]+){1,2}$/, // "Why Anthropic", "Why us"
  /\bwhy do you want to (work|join)\b/,
  /\bwhy\b.*\b(us|here|join|joining|work (at|for|with)|working (at|for|with))\b$/,
  /\bwhy\b.*\binterested in (this|the|our|joining|working)\b/,
  /\bwhat (excites|draws|attracts|interests|motivates|appeals to) you\b/,
];

export function motivationEssay(question: string): boolean {
  const q = normalizeLabel(question);
  return q !== "" && MOTIVATION.some(p => p.test(q));
}

export function intentByKey(key: string): Intent | undefined {
  return BY_KEY.get(key);
}
