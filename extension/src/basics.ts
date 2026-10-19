// Your basics: the generic facts nearly every application asks (name, email,
// phone, links, where you are) and the recurring questions (intents.ts: visa
// sponsorship, in-person work, start date...), entered once in the popup so the
// first form fills them too. They are ordinary saved answers: they go through the same
// capture as a form you filled by hand and are stored under the key a form's
// field resolves to (its autocomplete meaning, else its question), so Fill
// needs nothing new to use them, and the answers table edits and deletes them.
import type { Bank, FieldInfo, FieldKind } from "./types";
import { MAX_VALUE_CHARS, answerKey } from "./bank";
import { INTENTS, intentOf } from "./intents";

/** The capture host for answers typed in the popup: never a real site, so no per-site signature is kept. */
export const BASICS_HOST = "vani-basics";

/** contact: facts about you; work: recurring questions; self_id: voluntary self-identification (only with self-ID on). */
export type BasicGroup = "contact" | "work" | "self_id";

export interface Basic {
  id: string;
  label: string;
  kind: FieldKind;
  /** The autocomplete token whose meaning (normalize.AUTOCOMPLETE_KEYS) is this answer's key, or "" to key by label. */
  autocomplete: string;
  group: BasicGroup;
  /** Offered as a dropdown with these choices ("" is always the unanswered first choice); absent: a text box. */
  options?: readonly string[];
}

const CONTACT: readonly Basic[] = [
  { id: "first", label: "First name", kind: "text", autocomplete: "given-name", group: "contact" },
  { id: "last", label: "Last name", kind: "text", autocomplete: "family-name", group: "contact" },
  { id: "email", label: "Email", kind: "email", autocomplete: "email", group: "contact" },
  { id: "phone", label: "Phone", kind: "tel", autocomplete: "tel", group: "contact" },
  { id: "city", label: "City", kind: "text", autocomplete: "address-level2", group: "contact" },
  { id: "country", label: "Country", kind: "text", autocomplete: "country-name", group: "contact" },
  { id: "linkedin", label: "LinkedIn", kind: "url", autocomplete: "", group: "contact" },
  { id: "github", label: "GitHub", kind: "url", autocomplete: "", group: "contact" },
  { id: "website", label: "Website", kind: "url", autocomplete: "url", group: "contact" },
];

/** Each intent is one basic, saved under its own label, which resolves to the intent's key. */
const QUESTIONS: readonly Basic[] = INTENTS.map(i => ({
  id: i.key.replace(/ /g, "-"),
  label: i.label,
  kind: i.answer === "text" ? "text" : "select",
  autocomplete: "",
  group: i.selfId ? "self_id" : "work",
  ...(i.answer === "yesno" ? { options: ["Yes", "No"] } : i.answer === "choice" ? { options: i.options ?? [] } : {}),
}));

export const BASICS: readonly Basic[] = [...CONTACT, ...QUESTIONS];

function info(b: Basic, value: string[]): FieldInfo {
  return { kind: b.kind, label: b.label, name: "", autocomplete: b.autocomplete, automationId: "", options: [], required: false, value };
}

/**
 * The basics with something new typed, as fields for the capture message. An
 * empty box is left out (it never clears a saved answer), and so is one that
 * still shows what `saved` (basicsValues) holds, so saving twice changes nothing.
 */
export function basicsFields(values: Readonly<Record<string, string>>, saved: Readonly<Record<string, string>> = {}): FieldInfo[] {
  const out: FieldInfo[] = [];
  for (const b of BASICS) {
    const v = (values[b.id] ?? "").trim().slice(0, MAX_VALUE_CHARS);
    if (v && v !== saved[b.id]) out.push(info(b, [v]));
  }
  return out;
}

/**
 * What the bank holds for each basic, whichever form taught it ("" when nothing).
 * A question basic shows what Fill would use for it (match.ts): the latest
 * answer saved under any wording of its intent.
 */
export function basicsValues(bank: Bank | null): Record<string, string> {
  const out: Record<string, string> = {};
  const answers = bank ? Object.values(bank.answers) : [];
  for (const b of BASICS) {
    if (b.group === "contact") {
      out[b.id] = bank?.answers[answerKey(info(b, []))]?.value.join(", ") ?? "";
      continue;
    }
    // Self-identification stays out of the popup while self-ID is off (Fill ignores it then too).
    if (b.group === "self_id" && !bank?.settings.captureSelfId) { out[b.id] = ""; continue; }
    const want = intentOf(b.label);
    let latest: (typeof answers)[number] | undefined;
    for (const a of answers) {
      if ((!latest || a.updatedAt > latest.updatedAt) && [a.key, ...a.labels].some(l => intentOf(l) === want)) latest = a;
    }
    out[b.id] = latest?.value.join(", ") ?? "";
  }
  return out;
}
