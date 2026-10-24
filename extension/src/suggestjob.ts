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
import type { JobInfo } from "./draft";
import { MAX_COMPANY_CHARS, MAX_DESCRIPTION_CHARS, MAX_TITLE_CHARS } from "./draft";

export const MAX_FIELDS = 60;
export const MAX_LABEL_CHARS = 1000;
export const MAX_DRAFT_CHARS = 8000;
export const POLL_MS = 2000;
/** The agent allows a suggest 330 s; leave room for start-up and the last poll. */
export const SUGGEST_TIMEOUT_MS = 345_000;
/** A draft job is bounded at 150 s by the agent. */
export const DRAFT_JOB_TIMEOUT_MS = 165_000;

export type JobKind = "suggest" | "draft" | "resume";

export interface SuggestField { id: string; label: string; kind: "text" | "textarea" }

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
}

export type JobFailure = { ok: false; code: string; error: string };
export type SuggestResult = { ok: true; fields: FieldSuggestion[] } | JobFailure;

/** What to tell the user for failures the agent path adds. */
export const JOB_HELP: Record<string, string> = {
  agent: "The Vani Apply agent isn't running. Open Vani Apply, then allow it in System Settings > General > Login Items.",
  unreachable: "The Vani Apply Mac app isn't reachable from this browser.",
  busy: "Vani is still working on an earlier request. Wait for it to finish.",
  timeout: "Vani took too long. Try again.",
  config: "Vani has no model or no Anthropic key configured (Vani Apply > Settings).",
  keychain: "Keychain access was denied, so Vani couldn't read its API key.",
  budget_exhausted: "Today's Claude budget is spent. Try again tomorrow.",
  ungrounded: "The draft cited a figure that isn't in your knowledge base or the posting, so Vani threw it away. Try again, or write this answer yourself.",
  bad_reply: "Vani sent a reply that isn't a suggestion.",
  unsupported: "This version of the Vani Apply app can't run suggestions. Rebuild and reopen it.",
};

/** The `vani suggest` request, or an error when there is nothing to ask about. */
export function buildSuggestRequest(fields: SuggestField[], job: JobInfo): object {
  const seen = new Set<string>();
  const out: SuggestField[] = [];
  for (const f of fields) {
    const label = clean(f.label);
    if (!label || label.length > MAX_LABEL_CHARS || seen.has(f.id) || !/^[A-Za-z0-9_-]{1,64}$/.test(f.id)) continue;
    if (f.kind !== "text" && f.kind !== "textarea") continue;
    seen.add(f.id);
    out.push({ id: f.id, label, kind: f.kind });
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

function failure(code: string, error?: string): JobFailure {
  return { ok: false, code, error: JOB_HELP[code] ?? clip(error ?? "Vani refused the request.", 500) };
}

/** A host or agent error object as a failure; null when x is not one. */
export function jobError(x: unknown): JobFailure | null {
  if (!isRecord(x) || typeof x.error !== "string") return null;
  return failure(typeof x.code === "string" ? x.code : "provider", x.error);
}

/**
 * Strictly read `vani suggest`'s reply. Only ids we asked about are kept, a
 * draft is kept only on a "drafted" field, and anything unexpected is a
 * failure, never a suggestion.
 */
export function parseSuggestReply(x: unknown, asked: ReadonlySet<string>): SuggestResult {
  const err = jobError(x);
  if (err) return err;
  if (!isRecord(x) || !Array.isArray(x.fields)) return failure("bad_reply");
  const fields: FieldSuggestion[] = [];
  const seen = new Set<string>();
  for (const f of x.fields.slice(0, MAX_FIELDS)) {
    if (!isRecord(f) || typeof f.id !== "string" || !asked.has(f.id) || seen.has(f.id)) continue;
    if (typeof f.status !== "string" || !STATUSES.has(f.status)) continue;
    const status = f.status as SuggestStatus;
    const draft = status === "drafted" ? str(f.draft, MAX_DRAFT_CHARS + 1).trim() : "";
    if (status === "drafted" && (draft === "" || draft.length > MAX_DRAFT_CHARS)) continue;
    seen.add(f.id);
    const ev = f.evidence_items;
    fields.push({
      id: f.id, status, draft,
      category: str(f.category, 40), reason: str(f.reason, 300), model: str(f.model, 100), code: str(f.code, 40),
      evidenceItems: typeof ev === "number" && Number.isSafeInteger(ev) && ev >= 0 ? ev : 0,
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

/** Start a job and poll until it finishes, fails or times out. Never throws. */
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
  while (now() - t0 < timeoutMs) {
    await sleep(pollMs);
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
    if ("pending" in s) continue;
    if ("done" in s) return { ok: true, reply: s.reply };
    if ("ok" in s) return s;
    return failure("bad_reply");
  }
  return failure("timeout");
}
