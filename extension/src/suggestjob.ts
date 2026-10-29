// Suggest: the page's new free-text questions go to Vani (`vani suggest`, in
// the Vani Apply agent) as labels only. The decision model sorts them; Claude
// drafts the ones read as essays. Visa, consent and self-identification
// questions are answered by the applicant, never by a model. The request
// carries the labels and the posting, never a field value or a saved answer.
//
// The agent runs it as a job: the background starts it and the content script
// polls, so no browser message stays open for the minutes a draft can take
// (Safari unloads an idle background page; Chrome an idle worker).
import { clean, clip, isRecord } from "./guards";
import type { DraftCode, JobInfo } from "./draft";
import { DRAFT_HELP, MAX_COMPANY_CHARS, MAX_DESCRIPTION_CHARS, MAX_TITLE_CHARS, ungroundedHelp, ungroundedKind } from "./draft";

export const MAX_FIELDS = 60;
/** The largest max_chars vani suggest accepts (fieldtriage.MaxMaxChars). */
export const MAX_SENT_CHARS = 100_000;
/** The option text of all choice questions in one request; vani's batched answer turn holds about this much too. */
export const MAX_REQUEST_OPTION_CHARS = 20_000;
export const MAX_LABEL_CHARS = 1000;
export const MAX_DRAFT_CHARS = 8000;
export const POLL_MS = 2000;
/** The agent allows a suggest 330 s; leave room for start-up and the last poll. */
export const SUGGEST_TIMEOUT_MS = 345_000;
/** A draft job is bounded at 150 s by the agent. */
export const DRAFT_JOB_TIMEOUT_MS = 165_000;

/** `resume` is the stateless build (`vani resume`); `resumeJob` is the keyed one (`vani jobs resume build` then `use`) for a tracked posting. */
export type JobKind = "suggest" | "draft" | "resume" | "resumeJob";

/** A choice question carries 2 to 50 options of at most 200 characters each (vani suggest's own bounds). */
export const MIN_CHOICE_OPTIONS = 2;
export const MAX_CHOICE_OPTIONS = 50;
export const MAX_OPTION_CHARS = 200;

/**
 * `maxChars`: the field's live character limit, when it has one; sent as `max_chars` so a draft is written to fit.
 * `options`: a "choice" field's option labels (a native select or a single-choice radio group); a choice field has no limit.
 */
export interface SuggestField { id: string; label: string; kind: "text" | "textarea" | "choice"; maxChars?: number; options?: string[] }

/** What a drafted row is: an essay or a short fact written as text, or one option of a choice question. */
export type AnswerKind = "essay" | "fact" | "choice";
const ANSWER_KINDS: ReadonlySet<string> = new Set(["essay", "fact", "choice"]);

export type SuggestStatus = "drafted" | "manual" | "unsure" | "failed" | "skipped";
const STATUSES: ReadonlySet<string> = new Set(["drafted", "manual", "unsure", "failed", "skipped"]);

export interface FieldSuggestion {
  id: string;
  status: SuggestStatus;
  category: string;
  reason: string;
  draft: string;
  model: string;
  evidenceItems: number;
  code: string;
  /** Set when vani says what kind of answer it drafted. A "choice" row also carries `choice`, one of the options that were sent. */
  answerKind?: AnswerKind;
  choice?: string;
}

export type JobFailure = { ok: false; code: string; error: string };
export type SuggestResult = { ok: true; fields: FieldSuggestion[] } | JobFailure;

/** What to tell the user for failures the agent path adds. */
export const JOB_HELP: Record<string, string> = {
  agent: "The Vani Apply agent isn't running. Open Vani Apply, then allow it in System Settings > General > Login Items.",
  no_such_job: "The Vani Apply agent no longer has this request (it finished a few minutes ago, or the agent restarted). Run it again.",
  unreachable: "The Vani Apply Mac app isn't reachable from this browser.",
  busy: "Vani is still working on an earlier request. Wait for it to finish.",
  timeout: "Vani took too long. Try again.",
  config: "Vani has no model or no Anthropic key configured (Vani Apply > Settings).",
  keychain: "Keychain access was denied, so Vani couldn't read its API key.",
  budget_exhausted: "Today's Claude budget is spent. Try again tomorrow.",
  ungrounded: ungroundedHelp(),
  bad_reply: "Vani sent a reply that isn't a suggestion.",
  unsupported: "This version of the Vani Apply app can't run suggestions. Rebuild and reopen it.",
  // Resume Studio's codes. Where vani's own message says more (no_model names why, ineligible names the bullet),
  // resume.ts lists the code in OWN_TEXT and the message is shown instead.
  kb_changed: "Your knowledge base changed while the resume was being built, so Vani did not use that build. Build again.",
  unchecked: "Vani could not check the resume against your knowledge base, so it did not use it. Try again.",
  stale: "A bullet on that resume changed in your knowledge base after it was built, so Vani did not use it. Build again.",
};

/** One field as `vani suggest` reads it. */
export type WireField = { id: string; label: string; kind: "text" | "textarea"; max_chars?: number } | { id: string; label: string; kind: "choice"; options: string[] };
export interface SuggestRequest { job: { company: string; title: string; description: string }; fields: WireField[] }

/**
 * A choice field's options as sent: whitespace folded, empty ones (a "select" placeholder with no label) left out. Null when
 * the field cannot be asked: fewer than two options, more than 50, one over 200 characters, or the same label twice (an answer
 * that names it could not be told apart on the page).
 */
export function choiceOptions(options: readonly string[] | undefined): string[] | null {
  if (!Array.isArray(options)) return null;
  const out: string[] = [];
  for (const o of options) {
    if (typeof o !== "string") return null;
    const t = clean(o);
    if (t === "") continue;
    if (t.length > MAX_OPTION_CHARS || out.includes(t)) return null;
    out.push(t);
    if (out.length > MAX_CHOICE_OPTIONS) return null;
  }
  return out.length >= MIN_CHOICE_OPTIONS ? out : null;
}

/** The `vani suggest` request, or an error when there is nothing to ask about. */
export function buildSuggestRequest(fields: SuggestField[], job: JobInfo): SuggestRequest {
  const seen = new Set<string>();
  const out: WireField[] = [];
  let optionChars = 0;
  for (const f of fields) {
    const label = clean(f.label);
    if (!label || label.length > MAX_LABEL_CHARS || seen.has(f.id) || !/^[A-Za-z0-9_-]{1,64}$/.test(f.id)) continue;
    if (f.kind === "choice") {
      const options = choiceOptions(f.options);
      if (!options) continue;
      // The agent takes a request of 256 KB at most: a page of long option lists stops adding choice questions here.
      const chars = options.reduce((n, o) => n + o.length, 0);
      if (optionChars + chars > MAX_REQUEST_OPTION_CHARS) continue;
      optionChars += chars;
      seen.add(f.id);
      out.push({ id: f.id, label, kind: "choice", options });
    } else if (f.kind === "text" || f.kind === "textarea") {
      seen.add(f.id);
      // Only a real limit goes out: a field with none carries no key, so the request is what it always was. A limit past
      // MAX_SENT_CHARS (pages set 524288 or 2147483647 to mean "none") is no limit, and vani suggest refuses it.
      out.push({ id: f.id, label, kind: f.kind, ...(Number.isSafeInteger(f.maxChars) && f.maxChars! >= 1 && f.maxChars! <= MAX_SENT_CHARS ? { max_chars: f.maxChars } : {}) });
    } else continue;
    if (out.length >= MAX_FIELDS) break;
  }
  if (out.length === 0) throw new Error("no new free-text questions on this page to suggest answers for");
  return {
    job: {
      company: clip(clean(job.company), MAX_COMPANY_CHARS),
      title: clip(clean(job.title), MAX_TITLE_CHARS),
      description: clip(job.description.trim(), MAX_DESCRIPTION_CHARS),
    },
    fields: out,
  };
}

function str(x: unknown, n: number): string {
  return typeof x === "string" ? clip(x, n) : "";
}

/** Codes `vani suggest` adds to draft's. */
const SUGGEST_CODE_HELP: Record<string, string> = {
  attempts_exhausted: "Vani stopped drafting on this page because earlier drafts failed, so this one was not tried. Use Draft on it.",
  too_long: "The answer did not fit this field's character limit. Write it yourself, or use Draft on a longer field.",
  no_evidence: "Vani found nothing in your knowledge base to answer this from. Write it yourself.",
  answer_cap: "Vani answers at most 15 short or choice questions per page, so this one was not tried. Answer it yourself.",
  too_large: "This question's options are too long to send, so Vani did not answer it. Choose it yourself.",
};
/** Codes whose host text is the detail (a malformed request names the field; a provider error names the provider) that a general sentence would hide. */
const PLAIN_HOST_CODES: ReadonlySet<string> = new Set(["bad_request", "provider", "ungrounded"]);

/** Our own sentence for a failure code the host may send (draft's, then suggest's), or undefined when the host's text is the better one. */
export function codeHelp(code: string): string | undefined {
  if (PLAIN_HOST_CODES.has(code)) return undefined;
  if (Object.hasOwn(DRAFT_HELP, code)) return DRAFT_HELP[code as DraftCode];
  return Object.hasOwn(SUGGEST_CODE_HELP, code) ? SUGGEST_CODE_HELP[code] : undefined;
}

function failure(code: string, error?: string, kind?: unknown): JobFailure {
  if (code === "ungrounded") return { ok: false, code, error: ungroundedHelp(ungroundedKind(kind)) };
  return { ok: false, code, error: (Object.hasOwn(JOB_HELP, code) ? JOB_HELP[code] : undefined) ?? codeHelp(code) ?? clip(error ?? "Vani refused the request.", 500) };
}

/**
 * What a row that was not drafted says. A refused draft says what was unbacked in the page's words. Otherwise the
 * host's own reason stands (it names the page's numbers and what it saw); a row that came with none gets our sentence
 * for its code (truncated, refused, kb, not_drafted, too_long, attempts_exhausted and the rest), and a failed row with
 * neither still says it failed rather than showing nothing.
 */
function rowReason(status: SuggestStatus, code: string, kind: unknown, reason: string): string {
  if (status === "failed" && code === "ungrounded") return ungroundedHelp(ungroundedKind(kind));
  if (status === "drafted" || reason !== "") return reason;
  return codeHelp(code) ?? (status === "failed" ? "Vani could not draft this one. Try Draft on it, or write it yourself." : "");
}

/** A host or agent error object as a failure; null when x is not one. */
export function jobError(x: unknown): JobFailure | null {
  if (!isRecord(x)) return null;
  // `{ ok: false, code }` is an error too: a reply that carries a code and no text still says it failed.
  if (typeof x.error !== "string" && !(x.ok === false && typeof x.code === "string")) return null;
  const text = typeof x.error === "string" ? x.error : "";
  const code = typeof x.code === "string" ? x.code : "provider";
  // The agent names a job it no longer holds with the code it uses for "not running"; that is a different thing to tell the person.
  if (code === "agent" && /^no such job\b/i.test(text)) return failure("no_such_job");
  return failure(code, text || undefined, x.kind);
}

/**
 * Strictly read `vani suggest`'s reply. Only ids we asked about are kept, a
 * draft is kept only on a "drafted" field, and anything unexpected is a
 * failure, never a suggestion.
 */
export function parseSuggestReply(x: unknown, asked: ReadonlySet<string>, options?: ReadonlyMap<string, readonly string[]>): SuggestResult {
  const err = jobError(x);
  if (err) return err;
  if (!isRecord(x) || !Array.isArray(x.fields)) return failure("bad_reply");
  const fields: FieldSuggestion[] = [];
  const seen = new Set<string>();
  for (const f of x.fields.slice(0, MAX_FIELDS)) {
    if (!isRecord(f) || typeof f.id !== "string" || !asked.has(f.id) || seen.has(f.id)) continue;
    if (typeof f.status !== "string" || !STATUSES.has(f.status)) continue;
    const status = f.status as SuggestStatus;
    const answerKind = typeof f.answer_kind === "string" && ANSWER_KINDS.has(f.answer_kind) ? (f.answer_kind as AnswerKind) : undefined;
    const sent = options?.get(f.id);
    const claimed = typeof f.choice === "string" ? clean(f.choice) : "";
    // A choice question's answer is one of the options that were sent. Anything else (a draft for a choice field, a choice
    // for a text field, an option the field does not have) is not used, and the row says so.
    if (status === "drafted" && (sent !== undefined || answerKind === "choice")) {
      seen.add(f.id);
      const row = { id: f.id, category: str(f.category, 40), model: str(f.model, 100), evidenceItems: 0 };
      if (!sent || answerKind !== "choice") {
        fields.push({ ...row, status: "failed", draft: "", code: "bad_choice", reason: "Vani answered this question in a form the page cannot take, so it was not used. Answer it yourself." });
      } else if (claimed === "" || !sent.includes(claimed)) {
        fields.push({ ...row, status: "failed", draft: "", code: "bad_choice", reason: "Vani named an option this field does not have, so it was not used. Choose it yourself." });
      } else {
        const ev = f.evidence_items;
        fields.push({
          ...row, status, draft: claimed, answerKind, choice: claimed, code: str(f.code, 40), reason: str(f.reason, 300),
          evidenceItems: typeof ev === "number" && Number.isSafeInteger(ev) && ev >= 0 ? ev : 0,
        });
      }
      continue;
    }
    const draft = status === "drafted" ? str(f.draft, MAX_DRAFT_CHARS + 1).trim() : "";
    seen.add(f.id);
    if (status === "drafted" && (draft === "" || draft.length > MAX_DRAFT_CHARS)) {
      // A draft that is not one (nothing, or too long to be an answer) is a failed row the person can see, not a question that vanishes.
      fields.push({
        id: f.id, status: "failed", draft: "", category: str(f.category, 40), model: str(f.model, 100), evidenceItems: 0, code: "bad_draft",
        reason: draft === "" ? "Vani returned an empty draft for this question. Try Draft on it, or write it yourself."
          : `Vani returned a draft longer than ${MAX_DRAFT_CHARS} characters, which is not an answer. Try Draft on it, or write it yourself.`,
      });
      continue;
    }
    const ev = f.evidence_items;
    const code = str(f.code, 40);
    fields.push({
      id: f.id, status, draft,
      category: str(f.category, 40), reason: rowReason(status, code, f.kind, str(f.reason, 300)), model: str(f.model, 100), code,
      evidenceItems: typeof ev === "number" && Number.isSafeInteger(ev) && ev >= 0 ? ev : 0,
      ...(answerKind ? { answerKind } : {}),
    });
  }
  return { ok: true, fields };
}

/** One step of a job as the background reports it. */
export type JobStep = { job: string } | { pending: true } | { done: true; reply: unknown } | JobFailure;

/** Read the background's reply to jobStart or jobPoll. */
export function parseJobStep(x: unknown): JobStep {
  const err = jobError(x);
  if (err) return err;
  if (isRecord(x)) {
    if (typeof x.job === "string" && x.job.length > 0 && x.job.length <= 64) return { job: x.job };
    if (x.pending === true) return { pending: true };
    if (x.done === true && "reply" in x) return { done: true, reply: x.reply };
  }
  return failure("bad_reply");
}

export interface JobDriver {
  start(): Promise<unknown>;
  poll(job: string): Promise<unknown>;
  sleep?(ms: number): Promise<void>;
  now?(): number;
  onTick?(elapsedMs: number): void;
}

/**
 * Poll failures that say "not now" rather than "no": the Mac app did not answer this call
 * (background.native turns an unreachable host into a coded reply, it does not throw). The job may be
 * running fine, so the wait goes on, slower each time, inside the overall bound. "agent" (the agent
 * is not running) and "no_such_job" are not here: they will not get better by asking again.
 */
const TRANSIENT_POLL: ReadonlySet<string> = new Set(["unreachable", "timeout"]);
/** The longest gap the backoff between transient poll failures grows to. */
export const MAX_BACKOFF_MS = 16_000;

/**
 * Start a job and poll until it finishes, fails or times out. Never throws. A poll that throws is
 * ridden out up to three in a row; one that comes back as an unreachable or timeout failure is
 * retried with a doubling wait (to MAX_BACKOFF_MS) until the overall bound, and ends the wait then with
 * that failure. Any other failure, and a finished job, is terminal.
 */
export async function runJob(d: JobDriver, timeoutMs: number, pollMs = POLL_MS): Promise<{ ok: true; reply: unknown } | JobFailure> {
  const sleep = d.sleep ?? (ms => new Promise<void>(r => setTimeout(r, ms)));
  const now = d.now ?? Date.now;
  const t0 = now();
  let step: JobStep;
  try {
    step = parseJobStep(await d.start());
  } catch {
    return failure("unreachable");
  }
  if (!("job" in step)) return "ok" in step ? step : failure("bad_reply");
  const id = step.job;
  let misses = 0;
  let wait = pollMs;
  let lastTransient: JobFailure | undefined;
  while (now() - t0 < timeoutMs) {
    await sleep(wait);
    d.onTick?.(now() - t0);
    let s: JobStep;
    try {
      s = parseJobStep(await d.poll(id));
      misses = 0;
    } catch {
      // A background page that was unloaded between polls answers the next one.
      if (++misses >= 3) return failure("unreachable");
      continue;
    }
    if ("ok" in s && TRANSIENT_POLL.has(s.code)) {
      lastTransient = s;
      wait = Math.min(wait * 2, MAX_BACKOFF_MS);
      continue;
    }
    lastTransient = undefined;
    wait = pollMs;
    if ("pending" in s) continue;
    if ("done" in s) return { ok: true, reply: s.reply };
    if ("ok" in s) return s;
    return failure("bad_reply");
  }
  return lastTransient ?? failure("timeout");
}

// --- an older vani ------------------------------------------------------------------------------------
// `vani suggest` refuses a request field it does not know (DisallowUnknownFields), so a vani older than this
// extension fails the whole page on `max_chars`, `options` or kind "choice". One resend with the old shape
// lets the essays through, and the person is told to reinstall.

/** What to say when the resend was needed: the newer questions were left out. */
export const OLDER_VANI = "Vani is older than this extension: it refused the newer question format, so choice questions and character limits were left out. Reinstall Vani and the Vani Apply app to get them.";

/** The refusal an older vani gives for the newer request shape: a bad_request that names a field, an option list or a choice. */
export function isOldShapeRefusal(r: SuggestResult): boolean {
  // The two ways a vani from before max_chars and choice fields refuses them: its strict JSON decoder, and its field
  // check. A current vani's own complaint about a field ("choice field c3 has 1 options") is neither.
  return !r.ok && r.code === "bad_request" && /unknown field "(max_chars|options)"|kind "choice" is not text or textarea/.test(r.error);
}

/** Whether the request uses anything an older vani does not know. Without it a refusal is not about the shape and is not resent. */
function usesNewShape(req: SuggestRequest): boolean {
  return req.fields.some(f => f.kind === "choice" || f.max_chars !== undefined);
}

/** The request an older vani reads: text and textarea fields only, none with a limit. */
export function legacyRequest(req: SuggestRequest): SuggestRequest | null {
  const fields: WireField[] = [];
  for (const f of req.fields) if (f.kind !== "choice") fields.push({ id: f.id, label: f.label, kind: f.kind });
  return fields.length > 0 ? { job: req.job, fields } : null;
}

export interface SuggestRun {
  result: SuggestResult;
  /** The request that was finally answered (or refused): its ids are the questions the rows can be about. */
  sent: SuggestRequest;
  /** The first request was refused as too new, and the result is from the old shape (or says it could not be). */
  older: boolean;
}

/** One request through the job driver, read strictly against what it asked. */
async function attempt(req: SuggestRequest, driverFor: (request: SuggestRequest) => JobDriver, timeoutMs: number, pollMs: number): Promise<SuggestResult> {
  const r = await runJob(driverFor(req), timeoutMs, pollMs);
  if (!r.ok) return r;
  const options = new Map<string, readonly string[]>();
  for (const f of req.fields) if (f.kind === "choice") options.set(f.id, f.options);
  return parseSuggestReply(r.reply, new Set(req.fields.map(f => f.id)), options);
}

/**
 * Run one suggest request; if an older vani refuses it for its shape, run it once more in the old shape. Never throws
 * for a refusal. `timeoutMs` bounds both attempts together.
 */
export async function runSuggest(req: SuggestRequest, driverFor: (request: SuggestRequest) => JobDriver, timeoutMs: number, pollMs = POLL_MS): Promise<SuggestRun> {
  const t0 = Date.now();
  const first = await attempt(req, driverFor, timeoutMs, pollMs);
  if (!isOldShapeRefusal(first) || !usesNewShape(req)) return { result: first, sent: req, older: false };
  const old = legacyRequest(req);
  // A page with only choice questions has nothing the older vani can be asked.
  if (!old) return { result: { ok: false, code: "older", error: OLDER_VANI }, sent: req, older: true };
  const left = timeoutMs - (Date.now() - t0);
  if (left <= 0) return { result: { ok: false, code: "timeout", error: JOB_HELP.timeout! }, sent: req, older: true };
  return { result: await attempt(old, driverFor, left, pollMs), sent: old, older: true };
}
