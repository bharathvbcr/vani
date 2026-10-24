// Drafting a long free-text answer ("Why this company?") through the Vani Apply
// Mac app, which runs `vani draft` with the owner's Claude budget. The request
// carries only the question label and the posting as shown on the page: no
// field values and nothing from the answer bank. A draft is never filled as a
// confident answer; the page shows it yellow, for review.
import { clean, clip } from "./guards";
import type { Transport } from "./store";
import { NATIVE_HOST } from "./store";

export const MAX_QUESTION_CHARS = 1000;
export const MAX_DESCRIPTION_CHARS = 60_000; // vani draft and suggest take a posting up to 60,000 runes (draft.MaxPostingRunes)
export const MAX_COMPANY_CHARS = 200;
export const MAX_TITLE_CHARS = 300;
export const MIN_WORDS = 40;
export const MAX_WORDS = 400;
export const DEFAULT_WORDS = 180;
/** How many distinct wordings one question can ask for. 0 is the first draft. */
export const MAX_ATTEMPT = 8;
/** The host allows a draft up to 150 s; leave room for process start and the reply. */
export const DRAFT_TIMEOUT_MS = 160_000;
/** A reply longer than this is not a draft we asked for. */
const MAX_DRAFT_CHARS = 8000;

export interface JobInfo {
  company: string;
  title: string;
  description: string;
}

export type DraftCode =
  | "bad_request" | "budget_exhausted" | "timeout" | "provider" | "empty_reply"
  | "config" | "kb" | "account" | "unsupported" | "unreachable" | "bad_reply" | "busy";

export type DraftReply =
  | { ok: true; draft: string; model: string; evidenceItems: number }
  | { ok: false; code: DraftCode; error: string };

const HOST_CODES: ReadonlySet<string> = new Set([
  "bad_request", "budget_exhausted", "timeout", "provider", "empty_reply", "config", "kb", "account", "unsupported",
]);

/** What to tell the user for each failure. */
export const DRAFT_HELP: Record<DraftCode, string> = {
  bad_request: "The draft request was refused as malformed.",
  budget_exhausted: "Today's drafting budget is spent. Try again tomorrow or write this one yourself.",
  timeout: "The draft took too long. Try again.",
  provider: "The model provider returned an error. Try again later.",
  empty_reply: "The model returned an empty draft. Try again.",
  config: "Drafting isn't set up: Vani has no model or no Anthropic key configured.",
  kb: "Vani couldn't read your knowledge base for evidence.",
  account: "anthropic-account couldn't start Vani with an Anthropic key. Pick an account in the Vani Apply app's Settings, or run anthropic-account doctor.",
  unsupported: "Drafting isn't available in Safari yet. Use Draft in the Vani Apply app.",
  unreachable: "The Vani Apply Mac app isn't reachable, so drafting is unavailable.",
  bad_reply: "The Mac app sent a reply that isn't a draft.",
  busy: "Another draft is still being written. Wait for it to finish.",
};

/** The frame that was asked does not hold this question. */
export const DRAFT_FIELD_ABSENT = "That question is not on this page.";
/** A draft editor or a Suggest run is already open. */
export const DRAFT_FIELD_BUSY = "Finish or close the open draft first.";
/** No frame answered the request to start a draft. */
export const DRAFT_FIELD_NONE = "Vani is not running on this page.";
/** The label matches more than one field, and no ordinal said which. */
export const DRAFT_FIELD_AMBIGUOUS = "Two fields share that question. Use the row on the page for the one you want.";

/**
 * One error for a fan-out of `draftField` replies, or null when any frame started.
 * "Not on this page" loses to a real refusal (no posting, not a text field, already drafting).
 */
export function draftStartError(replies: readonly { ok?: boolean; error?: string }[]): string | null {
  if (replies.some(r => r.ok === true)) return null;
  for (const r of replies) {
    if (typeof r.error === "string" && r.error !== "" && r.error !== DRAFT_FIELD_ABSENT) return r.error;
  }
  if (replies.length === 0) return DRAFT_FIELD_NONE;
  return DRAFT_FIELD_ABSENT;
}

/**
 * The draft transport. A draft takes up to 150 s, longer than Chrome keeps an
 * idle service worker, and Chrome documents an open native port (not
 * sendNativeMessage) as keeping the worker alive. So Chrome gets a port per
 * draft, closed after the one reply. Safari's promise API answers at once
 * (drafting is unsupported there).
 */
export const draftTransport: Transport = msg =>
  new Promise((resolve, reject) => {
    if (globalThis.browser?.runtime?.sendNativeMessage) {
      globalThis.browser.runtime.sendNativeMessage(NATIVE_HOST, msg).then(resolve, reject);
      return;
    }
    let port: chrome.runtime.Port;
    try {
      port = chrome.runtime.connectNative(NATIVE_HOST);
    } catch (e) {
      reject(e);
      return;
    }
    let done = false;
    port.onMessage.addListener(reply => {
      if (done) return;
      done = true;
      resolve(reply);
      port.disconnect();
    });
    port.onDisconnect.addListener(() => {
      if (done) return;
      done = true;
      reject(new Error(chrome.runtime.lastError?.message ?? "native host disconnected"));
    });
    port.postMessage(msg);
  });

/**
 * At most one draft in flight: each one spends real money and holds a model
 * call for up to 150 s, so a second click while one is running is refused,
 * not queued.
 */
export function createDrafter(transport: Transport = draftTransport, timeoutMs = DRAFT_TIMEOUT_MS) {
  let inFlight = false;
  return async (question: string, job: JobInfo, maxWords?: number, attempt?: number): Promise<DraftReply> => {
    if (inFlight) return { ok: false, code: "busy", error: DRAFT_HELP.busy };
    inFlight = true;
    try {
      return await requestDraft(question, job, { maxWords, attempt, transport, timeoutMs });
    } finally {
      inFlight = false;
    }
  };
}

/** The native request, or an error when there is nothing to draft for. */
export function buildDraftRequest(question: string, job: JobInfo, maxWords = DEFAULT_WORDS, attempt = 0): { op: "draft"; request: object } {
  const q = clip(clean(question), MAX_QUESTION_CHARS);
  if (q.length < 3) throw new Error("this field has no readable question to draft an answer for");
  const description = clip(job.description.trim(), MAX_DESCRIPTION_CHARS);
  if (description.length < 40) throw new Error("no job description was found on this page to draft from");
  const words = Number.isFinite(maxWords) ? Math.min(MAX_WORDS, Math.max(MIN_WORDS, Math.round(maxWords))) : DEFAULT_WORDS;
  if (!Number.isInteger(attempt) || attempt < 0 || attempt > MAX_ATTEMPT) throw new Error("attempt is out of range");
  const request: { question: string; job: { company: string; title: string; description: string }; max_words: number; attempt?: number } = {
    question: q,
    job: { company: clip(clean(job.company), MAX_COMPANY_CHARS), title: clip(clean(job.title), MAX_TITLE_CHARS), description },
    max_words: words,
  };
  // A first draft stays the request it always was. A later attempt is what makes temperature 0 write different prose.
  if (attempt > 0) request.attempt = attempt;
  return { op: "draft", request };
}

/** Strictly read the host's reply. Anything unexpected is a failure, never a draft. */
export function parseDraftReply(x: unknown): DraftReply {
  if (typeof x !== "object" || x === null || Array.isArray(x)) return { ok: false, code: "bad_reply", error: DRAFT_HELP.bad_reply };
  const r = x as Record<string, unknown>;
  if (typeof r.error === "string") {
    const code = typeof r.code === "string" && HOST_CODES.has(r.code) ? (r.code as DraftCode) : "provider";
    return { ok: false, code, error: clip(r.error, 500) };
  }
  if (typeof r.draft !== "string" || r.draft.trim() === "" || r.draft.length > MAX_DRAFT_CHARS) {
    return { ok: false, code: "bad_reply", error: DRAFT_HELP.bad_reply };
  }
  const evidence = typeof r.evidence_items === "number" && Number.isSafeInteger(r.evidence_items) && r.evidence_items >= 0 ? r.evidence_items : 0;
  return { ok: true, draft: r.draft.trim(), model: typeof r.model === "string" ? clip(r.model, 100) : "", evidenceItems: evidence };
}

/** Ask the Mac app for a draft. Never throws: every failure comes back as a coded reply. */
export async function requestDraft(
  question: string,
  job: JobInfo,
  opts: { maxWords?: number; attempt?: number; transport?: Transport; timeoutMs?: number } = {},
): Promise<DraftReply> {
  let msg: { op: "draft"; request: object };
  try {
    msg = buildDraftRequest(question, job, opts.maxWords, opts.attempt ?? 0);
  } catch (e) {
    return { ok: false, code: "bad_request", error: e instanceof Error ? e.message : String(e) };
  }
  const transport = opts.transport ?? draftTransport;
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const reply = await Promise.race([
      transport(msg),
      new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error("timed out")), opts.timeoutMs ?? DRAFT_TIMEOUT_MS); }),
    ]);
    return parseDraftReply(reply);
  } catch (e) {
    const timedOut = e instanceof Error && e.message === "timed out";
    return { ok: false, code: timedOut ? "timeout" : "unreachable", error: timedOut ? DRAFT_HELP.timeout : DRAFT_HELP.unreachable };
  } finally {
    clearTimeout(timer);
  }
}
