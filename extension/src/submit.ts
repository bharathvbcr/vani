// Submit this page is a separate action from Fill. It is off until the owner
// turns it on, and it clicks at most one final-submit control. It does not
// type a password, a captcha, or an agreement.
import { isFinalSubmit } from "./applied";

export interface SubmitGate {
  /** The popup checkbox. Off is not a click. */
  armed: boolean;
  /** null when auditForm did not run. That is not a pass. */
  missed: number | null;
  /** Labels of controls that match isFinalSubmit. Next, Continue, Save and Review are not in this list. */
  finals: string[];
  /** Fields the extension wrote. held false means the page no longer has that value. */
  writes: { label: string; held: boolean }[];
  /** absent: no required cover-letter file. attached: the used letter is on it. missing: it is not. */
  cover: "absent" | "attached" | "missing";
  /** The previous click did not show a confirmation. A second click is refused. */
  unconfirmed: boolean;
}

export type SubmitDecision =
  | { click: false; code: "off" | "audit" | "controls" | "readback" | "cover" | "unconfirmed"; detail: string }
  | { click: true; label: string };

export function decideSubmit(g: SubmitGate): SubmitDecision {
  if (!g.armed) return { click: false, code: "off", detail: "Submit this page is off." };
  if (g.unconfirmed) return { click: false, code: "unconfirmed", detail: "The last submit was not confirmed, so it will not be clicked again." };
  if (g.missed === null) return { click: false, code: "audit", detail: "The form was not checked, so it was not submitted." };
  if (g.missed > 0) return { click: false, code: "audit", detail: `${g.missed} required field${g.missed === 1 ? " is" : "s are"} still missed.` };
  if (g.finals.length !== 1) {
    const which = g.finals.length === 0 ? "none" : g.finals.join(", ");
    return { click: false, code: "controls", detail: `Expected one submit control, found ${which}.` };
  }
  const bad = g.writes.find(w => !w.held);
  if (bad) return { click: false, code: "readback", detail: `A value Vani wrote is no longer in "${bad.label}".` };
  if (g.cover === "missing") return { click: false, code: "cover", detail: "The required cover letter is not the letter chosen for this job." };
  return { click: true, label: g.finals[0]! };
}

/** Visible confirmation text. A page that does not say this is unconfirmed, not sent. */
export function looksConfirmed(text: string): boolean {
  return /thank you for (?:your |the )?application|application (?:has been |was )?(?:submitted|received)|we(?:'ve| have) received your application|your application (?:has been |was )?submitted/i.test(text);
}

/** Labels this action must not click. isFinalSubmit is false for all of them. */
export function isNotASubmit(label: string): boolean {
  const t = label.replace(/\s+/g, " ").trim();
  return /^(next|continue|save|review)\b/i.test(t) && !isFinalSubmit(t);
}
