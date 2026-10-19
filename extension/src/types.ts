// Shared shapes for the Vani application-form extension. Pure data: no DOM,
// no chrome APIs, so the matching core is testable under `bun test`.

export type FieldKind =
  | "text" | "email" | "tel" | "url" | "number" | "date" | "textarea"
  | "select" | "radio" | "checkbox" | "file" | "password" | "hidden" | "other";

/** Choice kinds answer with option labels; the rest answer with text. */
export const CHOICE_KINDS: ReadonlySet<FieldKind> = new Set<FieldKind>(["select", "radio", "checkbox"]);

/**
 * secret: never stored, never filled (passwords, government ids, card and bank numbers).
 * self_id: voluntary self-identification (gender, race, veteran, disability); stored and
 *          filled only when the user turns it on.
 * standard: everything else.
 */
export type Sensitivity = "secret" | "self_id" | "standard";

/** One question on a form, as read from the page. */
export interface FieldInfo {
  kind: FieldKind;
  label: string;        // the question as a person reads it
  name: string;         // name or id attribute
  autocomplete: string; // autocomplete attribute, lowercased
  automationId: string; // data-automation-id / data-qa / data-testid
  options: string[];    // option labels for choice kinds
  required: boolean;
  value: string[];      // chosen value(s); text kinds hold one element
  drafted?: boolean;    // holds a model draft the user inserted: saved for review, never as a confident answer
  multi?: boolean;      // a multi-select widget: it takes every saved value, not the first
}

/** One remembered answer. */
export interface Answer {
  key: string;          // canonical question key (normalized label or autocomplete token)
  labels: string[];     // wordings seen for this question
  kind: FieldKind;      // kind of the field it was last captured from
  value: string[];
  sensitivity: Sensitivity;
  signatures: string[]; // host-scoped stable field identities, e.g. "greenhouse.io|first_name"
  uses: number;         // times captured
  fills: number;        // times filled
  updatedAt: string;    // ISO time of the last capture
  review: boolean;      // long free text: filled only as a draft to review
}

export interface Settings {
  captureSelfId: boolean;
  disabledHosts: string[];
}

export interface Bank {
  version: 1;
  answers: Record<string, Answer>;
  settings: Settings;
}

export type MatchVia = "signature" | "autocomplete" | "label" | "intent" | "fuzzy";

export interface Match {
  answer: Answer;
  via: MatchVia;
  score: number;     // 0..1
  value: string[];   // the value to set, mapped onto this field's options for choice kinds
  review: boolean;   // fill, but mark for review (yellow)
}

export function emptyBank(): Bank {
  return { version: 1, answers: {}, settings: { captureSelfId: false, disabledHosts: [] } };
}
