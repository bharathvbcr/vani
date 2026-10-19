// Question intents: the questions nearly every application asks in its own
// words ("Do you require visa sponsorship?", "Will you now or in the future
// require sponsorship...?"). One saved answer to an intent fills every wording
// of it (match.ts), and the popup's basics offer each intent as one box
// (basics.ts). Storage keys are unchanged: an answer is found by intent when it
// is read, so a hand-typed answer under its own wording counts too.
//
// A wrong intent fills a confident wrong answer, so the patterns are narrow and
// a label that reads as two intents (authorized to work *without* sponsorship:
// opposite answers) reads as none.
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
}

const DECLINE = "Decline to self-identify";

function intent(label: string, answer: IntentAnswer, patterns: RegExp[], extra: Partial<Intent> = {}): Intent {
  return { key: normalizeLabel(label), label, answer, patterns, ...extra };
}

export const INTENTS: readonly Intent[] = [
  intent("Authorized to work", "yesno", [
    /\b(authori[sz]ed|eligible|permitted|legally able|entitled) to work\b/,
    /\bwork authori[sz]ation\b/,
    /\bright to work\b/,
  ]),
  intent("Visa sponsorship", "yesno", [/\bsponsor(ship|ed|ing)?\b/]),
  intent("Open to relocation", "yesno", [
    /\b(open|willing|able|consider|considering|happy)\b.*\breloca(te|tion|ting)\b/,
  ]),
  intent("Open to working in person", "yesno", [
    /\b(open|willing|able|comfortable|happy|commit|agree)\b.*\b(in person|onsite|on site|in office|in the office|in (one of )?(our|the|an|a) offices?|from (our|the) [a-z ]*office|hybrid)\b/,
    /\b(in person|onsite|on site|hybrid)\b.*\b(open|willing|able|comfortable)\b/,
  ]),
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
  ]),
  intent("Deadlines or timeline considerations", "text", [
    /\bdeadlines?\b/,
    /\btimeline\b/,
    /\b(other|competing|pending|outstanding) offers?\b/,
  ]),
  intent("How did you hear about us", "text", [
    /\bhow did you (hear|learn|find out|come across|discover)\b/,
    /\bwhere did you (hear|learn|find|see)\b/,
  ]),
  intent("Salary expectations", "text", [
    /\b(salary|compensation|pay)\b.*\b(expectations?|requirements?|range|desired|expected)\b/,
    /\b(expected|desired|target) (salary|compensation|pay|base)\b/,
  ]),
  intent("AI policy for applications", "yesno", [
    /\bai\b.*\bpolic(y|ies)\b/,
    /\bpolic(y|ies)\b.*\bai\b/,
    /\b(use of|using) (ai|artificial intelligence)\b/,
  ], { review: true }),
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

/** The intent a question asks, or "" when it asks none, or reads as more than one. */
export function intentOf(question: string): string {
  const q = normalizeLabel(question);
  if (!q || SECTION_ENTRY.test(q)) return "";
  let found = "";
  for (const i of INTENTS) {
    if (!i.patterns.some(p => p.test(q))) continue;
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
