// The "this application form has a knock-out question" note. Vani's `jobs find` attaches it to the
// jobFind reply (cmd/vani/jobsfind.go, internal/jobview/applyform.go) only when the posting's public
// application form has a flagged question: sponsorship with no answer that allows it, a yes/no on
// the right to work, or a short list of places you must be in. It shares the repeat-apply drawer
// (repeatapply.ts) and is a note and nothing more: it never blocks, delays or changes Fill, Suggest
// or Save, and an absent, old or malformed note shows nothing. No chrome.* and no DOM here.

const MAX_TEXT = 600;

/** The note's sentence in a host jobFind reply (`job.applyFormNote`), or null: not a tracked posting, an older reply, or malformed. */
export function parseFindFormNote(reply: unknown): string | null {
  if (typeof reply !== "object" || reply === null) return null;
  const r = reply as Record<string, unknown>;
  if (r.found !== true || typeof r.job !== "object" || r.job === null) return null;
  const x = (r.job as Record<string, unknown>).applyFormNote;
  if (typeof x !== "string") return null;
  const text = x.replace(/[\u0000-\u001f\u007f-\u009f]/g, " ").replace(/\s+/g, " ").trim().slice(0, MAX_TEXT);
  return text === "" ? null : text;
}

/** What the one drawer shows: the repeat-apply sentence and the form note, in that order, or nothing when there is neither. */
export function noticeOf(repeat: { text: string } | undefined, form: string | undefined): { text: string } | undefined {
  const parts = [repeat?.text, form].filter((p): p is string => typeof p === "string" && p !== "");
  return parts.length === 0 ? undefined : { text: parts.join(" ") };
}
