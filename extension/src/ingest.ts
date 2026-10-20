// Sending scraped jobs to Vani (docs/EXTENSION_INGEST.md): the message's bounds, the batch the UI builds
// from a scrape, and the words for the host's reply. The Go CLI enforces the same bounds again; this
// side refuses a batch over a bound and never cuts a field to fit.

import { isRecord } from "./guards";

export interface IngestJob {
  title: string;
  company: string;
  location: string;
  url: string;
  snippet: string;
  posted?: string;
}

export interface IngestPage { url: string; title: string }

export const MAX_INGEST_JOBS = 200;
const MAX_TITLE = 300;
const MAX_COMPANY = 200;
const MAX_LOCATION = 200;
const MAX_URL = 2048;
/** The most text of a posting Vani takes (bytes); it becomes the daemon's `Posting.Description`. */
export const MAX_SNIPPET = 4000;
const MAX_POSTED = 64;
const MAX_PAGE_TITLE = 300;

const bytes = (s: string): number => new TextEncoder().encode(s).length;

/** The URL's protocol when it parses and is within the byte bound, else null. */
function urlProtocol(u: string): string | null {
  if (bytes(u) > MAX_URL) return null;
  try {
    return new URL(u).protocol;
  } catch {
    return null;
  }
}

/** A string field within `max` bytes (`min` 0 allows empty). Returns the problem or null. */
function textProblem(what: string, v: unknown, min: number, max: number): string | null {
  if (typeof v !== "string") return `${what} is not text`;
  const n = bytes(v);
  if (n < min) return `${what} is required`;
  if (n > max) return `${what} is over ${max} bytes`;
  return null;
}

/** Why a job cannot be sent, or null when it fits every bound. */
export function ingestJobProblem(j: unknown): string | null {
  if (!isRecord(j)) return "job is not an object";
  const p = textProblem("title", j.title, 1, MAX_TITLE)
    ?? textProblem("company", j.company, 0, MAX_COMPANY)
    ?? textProblem("location", j.location, 0, MAX_LOCATION)
    ?? textProblem("snippet", j.snippet, 0, MAX_SNIPPET)
    ?? (j.posted === undefined ? null : textProblem("posted", j.posted, 0, MAX_POSTED))
    ?? textProblem("url", j.url, 1, MAX_URL);
  if (p) return p;
  const proto = urlProtocol(j.url as string);
  return proto === "http:" || proto === "https:" ? null : "url is not http(s)";
}

/** The first bound an `ingestJobs` message breaks, or null when it is sendable. */
export function ingestJobsProblem(m: Record<string, unknown>): string | null {
  if (typeof m.saveSearch !== "boolean") return "saveSearch is not a boolean";
  if (!isRecord(m.page)) return "page is not an object";
  const t = textProblem("page.title", m.page.title, 0, MAX_PAGE_TITLE) ?? textProblem("page.url", m.page.url, 1, MAX_URL);
  if (t) return t;
  const proto = urlProtocol(m.page.url as string);
  if (proto !== "http:" && proto !== "https:") return "page.url is not http(s)";
  if (m.saveSearch && proto !== "https:") return "page.url must be https to save a search";
  if (!Array.isArray(m.jobs)) return "jobs is not a list";
  if (m.jobs.length < 1) return "no jobs to send";
  if (m.jobs.length > MAX_INGEST_JOBS) return `more than ${MAX_INGEST_JOBS} jobs`;
  for (let i = 0; i < m.jobs.length; i++) {
    const p = ingestJobProblem(m.jobs[i]);
    if (p) return `job ${i}: ${p}`;
  }
  return null;
}

/**
 * What a scraped listing carries that the batch needs. `snippet` is the short text a listing shows;
 * `description` is the role text already cut to MAX_SNIPPET bytes, set only where the page held the
 * whole posting. Vani is sent `description` when there is one, because it analyses that text and
 * fetches none itself.
 */
export interface IngestSource { title: string; company: string; location: string; url: string; snippet: string; description?: string; posted?: string }

export interface IngestBatch {
  jobs: IngestJob[];
  /** Jobs left out because they break a bound (no http(s) link, no title, a field too long). */
  skipped: number;
  /** Jobs left out because the batch holds at most MAX_INGEST_JOBS. */
  overLimit: number;
}

/**
 * The jobs of a scrape that can be sent. A job that breaks a bound is left out whole (counted in
 * `skipped`), never cut to fit; past MAX_INGEST_JOBS the rest are counted in `overLimit`.
 */
export function buildIngestBatch(scraped: readonly IngestSource[]): IngestBatch {
  const jobs: IngestJob[] = [];
  let skipped = 0;
  let overLimit = 0;
  for (const s of scraped) {
    const j: IngestJob = { title: s.title, company: s.company, location: s.location, url: s.url, snippet: s.description || s.snippet };
    if (s.posted !== undefined && s.posted !== "") j.posted = s.posted;
    if (ingestJobProblem(j) !== null) { skipped++; continue; }
    if (jobs.length >= MAX_INGEST_JOBS) { overLimit++; continue; }
    jobs.push(j);
  }
  return { jobs, skipped, overLimit };
}

/** Words for what was left out of a batch; empty when nothing was. */
export function leftOutText(b: Pick<IngestBatch, "skipped" | "overLimit">): string {
  const parts: string[] = [];
  if (b.skipped > 0) parts.push(`${b.skipped} not sent (no usable link or title, or a field too long)`);
  if (b.overLimit > 0) parts.push(`${b.overLimit} not sent (at most ${MAX_INGEST_JOBS} at a time)`);
  return parts.join("; ");
}

export interface IngestRequest { type: "ingestJobs"; page: IngestPage; saveSearch: boolean; jobs: IngestJob[] }

/**
 * The message for a scrape, or why none can be sent. A search is saved only from an https listing page;
 * without a listing URL the first job's link stands in as the page, for a send that saves nothing.
 * A page title over its bound is sent empty (it is a label, never needed to ingest).
 */
export function prepareIngest(
  scrape: { pageUrl: string; pageTitle: string; jobs: readonly IngestSource[] },
  saveSearch: boolean,
): { req: IngestRequest; leftOut: string } | { error: string } {
  const batch = buildIngestBatch(scrape.jobs);
  const leftOut = leftOutText(batch);
  if (batch.jobs.length === 0) return { error: `Nothing to send${leftOut ? `: ${leftOut}` : ""}.` };
  if (saveSearch && !scrape.pageUrl.startsWith("https://")) {
    return { error: "This page cannot be saved as a daily search (it is not an https page). Untick the box to send the jobs only." };
  }
  const title = bytes(scrape.pageTitle) <= MAX_PAGE_TITLE ? scrape.pageTitle : "";
  const req: IngestRequest = {
    type: "ingestJobs",
    page: { url: scrape.pageUrl || batch.jobs[0]!.url, title },
    saveSearch,
    jobs: batch.jobs,
  };
  const problem = ingestJobsProblem(req as unknown as Record<string, unknown>);
  return problem === null ? { req, leftOut } : { error: `Cannot send: ${problem}.` };
}

export interface IngestReply {
  queued: number;
  duplicates?: number;
  rejected?: Array<{ index: number; reason: string }>;
  search?: { status: string; reason?: string; company?: string };
  analysis?: string;
}

const isCount = (x: unknown): x is number => typeof x === "number" && Number.isFinite(x) && x >= 0;

export function isIngestReply(r: unknown): r is IngestReply {
  return isRecord(r) && isCount(r.queued) &&
    (r.duplicates === undefined || isCount(r.duplicates)) &&
    (r.rejected === undefined || Array.isArray(r.rejected)) &&
    (r.search === undefined || (isRecord(r.search) && typeof r.search.status === "string")) &&
    (r.analysis === undefined || typeof r.analysis === "string");
}

export interface IngestOutcome { ok: boolean; text: string }

/**
 * The host's answer in words. Only what the reply says is reported: an error, or a reply that is not an
 * ingest result, is a failure and never reads as "queued".
 */
export function describeIngest(reply: unknown): IngestOutcome {
  if (isRecord(reply) && typeof reply.error === "string" && reply.error !== "") {
    const code = typeof reply.code === "string" ? reply.code : "";
    if (code === "unsupported" || /unknown op/i.test(reply.error)) {
      return { ok: false, text: "This version of the Vani app cannot take scraped jobs. Update the Vani app." };
    }
    if (code === "unreachable") return { ok: false, text: "The Vani app is not reachable. Open it and try again." };
    return { ok: false, text: `Vani refused the jobs: ${reply.error}` };
  }
  if (!isIngestReply(reply)) return { ok: false, text: "Vani sent an answer this extension does not understand." };

  const parts = [`${reply.queued} queued`];
  if (reply.duplicates) parts.push(`${reply.duplicates} duplicate${reply.duplicates === 1 ? "" : "s"}`);
  const rejected = reply.rejected ?? [];
  if (rejected.length > 0) {
    const why = typeof rejected[0]?.reason === "string" ? ` (${rejected[0].reason})` : "";
    parts.push(`${rejected.length} rejected${why}`);
  }
  const s = reply.search;
  if (s && s.status !== "not_requested") {
    if (s.status === "added") parts.push("search saved");
    else if (s.status === "exists") parts.push("search already saved");
    else parts.push(`search not saved${s.reason ? `: ${s.reason}` : ` (${s.status})`}`);
  }
  if (reply.analysis === "daemon") parts.push("analysing now");
  else if (reply.analysis === "next_run") parts.push("analysed at next run");
  return { ok: reply.queued > 0 || s?.status === "added", text: parts.join(", ") };
}
