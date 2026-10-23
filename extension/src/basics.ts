// Your basics: the generic facts nearly every application asks (name, email,
// phone, links, where you are) and the recurring questions (intents.ts: visa
// sponsorship, in-person work, start date...), entered once in the popup so the
// first form fills them too. They are ordinary saved answers: they go through the same
// capture as a form you filled by hand and are stored under the key a form's
// field resolves to (its autocomplete meaning, else its question), so Fill
// needs nothing new to use them, and the answers table edits and deletes them.
import type { Bank, FieldInfo, FieldKind } from "./types";
import { BASICS_HOST, MAX_VALUE_CHARS, answerKey } from "./bank";
import { INTENTS, intentOf } from "./intents";
import { isCaptureReply } from "./messages";

export { BASICS_HOST };

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
 * The basics with something new typed, as fields for the capture message. A box
 * that was empty and remains empty is left out, and so is one that still shows
 * what `saved` (basicsValues) holds. When an already-saved basic is cleared
 * (!v && saved[b.id]), an empty field is sent so capture can remove it.
 */
export function basicsFields(values: Readonly<Record<string, string>>, saved: Readonly<Record<string, string>> = {}): FieldInfo[] {
  const out: FieldInfo[] = [];
  for (const b of BASICS) {
    const v = (values[b.id] ?? "").trim().slice(0, MAX_VALUE_CHARS);
    if (v && v !== saved[b.id]) {
      out.push(info(b, [v]));
    } else if (!v && saved[b.id]) {
      out.push(info(b, []));
    }
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

/** The status line and toast after a basics save. `saved`: the bank took the change, so the boxes may be refreshed from it. */
export interface BasicsSaveOutcome { saved: boolean; status: string; toast: string }

const TOAST_REASON_CHARS = 90;

/**
 * Read the background's reply to a basics capture. Its error (or the one sending threw) is carried
 * into both lines, so a failed save says why, not only that it failed. A reply that is neither counts
 * nor an error is a failure: a save that could not be confirmed must not read as one that landed.
 */
export function basicsSaveOutcome(reply: unknown): BasicsSaveOutcome {
  if (isCaptureReply(reply)) {
    const n = reply.added + reply.updated;
    const left: string[] = [];
    if (reply.skippedSecret) left.push(`${reply.skippedSecret} refused as sensitive`);
    if (reply.skippedSelfId) left.push(`${reply.skippedSelfId} kept out while self-ID is off`);
    if (reply.skippedEmpty) left.push(`${reply.skippedEmpty} not saved (too short, or the bank is full)`);
    const tail = left.length ? `; ${left.join("; ")}` : "";
    if (n === 0 && left.length > 0) return { saved: true, status: `Nothing saved${tail}.`, toast: "No basics were saved." };
    return { saved: true, status: `Saved ${n}${tail}.`, toast: `Saved ${n} ${n === 1 ? "basic" : "basics"}.` };
  }
  const raw = reply && typeof reply === "object" && typeof (reply as { error?: unknown }).error === "string" ? (reply as { error: string }).error.trim() : "";
  const why = raw || "no reply from the extension";
  const short = why.length > TOAST_REASON_CHARS ? `${why.slice(0, TOAST_REASON_CHARS - 1)}…` : why;
  return { saved: false, status: `Could not save: ${why}`, toast: `Basics not saved: ${short}` };
}
