// Crawling a whole listing, not just the page on screen, and handing it to Vani (docs/EXTENSION_INGEST.md).
//
// Two modes, both started by the user and both bounded:
// - Workday boards are read through the board's own JSON listing API (same origin as the page, so the
//   content script needs no host permission). That API serves at most WORKDAY_WINDOW rows of one query:
//   it reports `total` capped at that number, and past it it serves page 1 again instead of an error.
//   A board bigger than the window is split by one of its own facets (each bucket under the window),
//   and a page that repeats page 1 ends the walk. Each job's description is then fetched for the most
//   relevant jobs, because Vani analyses the description it is sent and fetches none itself.
// - Any other listing is walked through its own pagination: the page's "next" / "load more" control is
//   clicked, or a rel=next link that stays on the same listing is fetched and parsed. Links that lead
//   anywhere else are never followed.
//
// Page content is untrusted: it only ever becomes data in an ingest batch, bounded by ingest.ts.

import { isRecord } from "./guards";
import { type IngestOutcome, type IngestPage, type IngestSource, MAX_INGEST_JOBS, MAX_SNIPPET, buildIngestBatch, describeIngest, isIngestReply, leftOutText } from "./ingest";
import { type UserTargetProfile, rankListing } from "./relevance";

export const WORKDAY_PAGE = 20;
/** The most rows one Workday listing query serves; offsets at or past it return page 1 again. */
export const WORKDAY_WINDOW = 2000;
const MAX_SPLIT_DEPTH = 3;
const MAX_LOCATION_BYTES = 200;
const MAX_BODY_BYTES = 4 * 1024 * 1024;
const MAX_FACET_PARAMS = 20;
/** Facet values taken from the page's URL. */
const MAX_FACET_VALUES = 200;
/** Facet values read from an answer; a facet with more is partial and never used to split. */
const MAX_ANSWER_FACET_VALUES = 1000;
const MAX_ROWS = 10_000;
const MAX_RETRIES = 2;
/** Descriptions that fail in a row before the rest are not tried: the board is refusing them. */
const MAX_DETAIL_FAILS_IN_ROW = 8;

export interface CrawlOptions {
  /** Stops the crawl; what was collected so far is still returned. */
  signal?: AbortSignal;
  /** Most HTTP requests in one crawl (listing pages plus details). */
  maxRequests?: number;
  /** Most job descriptions fetched; the most relevant jobs are fetched first. */
  maxDetails?: number;
  /** Most pages walked in DOM mode. */
  maxPages?: number;
  /** Most jobs kept. */
  maxJobs?: number;
  /** Pause before each request after the first, in ms. */
  delayMs?: number;
  /** Per-request timeout, in ms. */
  timeoutMs?: number;
  /** Requests in flight at once in Workday mode (facet buckets, descriptions); 1 to 6. */
  concurrency?: number;
  /** The whole Workday crawl's time bound, in ms (default 15 minutes). */
  deadlineMs?: number;
  /** How long a clicked "next" may take to change the list, in ms. */
  pageWaitMs?: number;
  /** The company the jobs belong to; Workday's API does not name it. */
  company?: string;
  profile?: Partial<UserTargetProfile>;
  onProgress?: (p: CrawlProgress) => void;
}

export interface CrawlProgress {
  phase: "listing" | "details" | "pages";
  /** Requests made, details fetched, or pages walked so far. */
  done: number;
  /** The phase's planned size when known. */
  of?: number;
  jobs: number;
}

export interface CrawlReport {
  mode: "workday" | "pages";
  /** Jobs ready to send, each with a description when one could be read. */
  jobs: IngestSource[];
  /** Jobs listed on the board. In Workday mode this counts rows whose description was not fetched too. */
  listed: number;
  /** The board's own count of matching jobs when it gives one (Workday, from its facets). */
  expected?: number;
  requests: number;
  /** Workday mode: queries the board was split into. DOM mode: pages walked. */
  parts: number;
  /** Descriptions shortened to fit the ingest bound. */
  shortened: number;
  /** True only when the walk reached the listing's end with nothing left out. */
  complete: boolean;
  /** Why the walk stopped early or what it left out, in words. Empty when complete. */
  notes: string[];
}

class Stop extends Error {}

export interface Http {
  fetch(url: string, init: RequestInit): Promise<Response>;
  sleep(ms: number): Promise<void>;
}

const defaultHttp = (): Http => ({
  fetch: (url, init) => globalThis.fetch(url, init),
  sleep: ms => new Promise(r => setTimeout(r, ms)),
});

const bytes = (s: string): number => new TextEncoder().encode(s).length;

function clean(s: unknown): string {
  return typeof s === "string" ? s.replace(/\s+/g, " ").trim() : "";
}

/** `s` cut to at most `max` UTF-8 bytes at a whole code point, then back to a word end, marked with an ellipsis. */
export function clipBytes(s: string, max: number): string {
  if (bytes(s) <= max) return s;
  const mark = max >= bytes("…") ? "…" : "";
  const room = max - bytes(mark);
  let out = "";
  let n = 0;
  for (const ch of s) {
    const b = bytes(ch);
    if (n + b > room) break;
    out += ch;
    n += b;
  }
  const space = out.search(/\s\S*$/);
  if (space > room / 2) out = out.slice(0, space);
  return `${out.trimEnd()}${mark}`;
}

// Where a description's substance starts; the company boilerplate before it is what gets dropped first.
const SUBSTANCE = /(what you('|’)ll be doing|what you will do|responsibilities|what we need to see|requirements|qualifications|about the (role|team|job)|the role|job description|you will)\b/i;

/** A description within the ingest bound, starting where the role is described when it has to be cut. */
export function excerpt(text: string, max = MAX_SNIPPET): { text: string; shortened: boolean } {
  if (bytes(text) <= max) return { text, shortened: false };
  const hit = text.search(SUBSTANCE);
  // From the start of the line the role starts on, so the excerpt never opens mid-sentence.
  const at = hit > 0 ? text.lastIndexOf("\n", hit) + 1 : 0;
  const from = at > 0 && bytes(text.slice(0, at)) < bytes(text) / 2 ? text.slice(at) : text;
  return { text: clipBytes(from, max), shortened: true };
}

const ENTITY: Record<string, string> = {
  amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " ", ensp: " ", emsp: " ", thinsp: " ", shy: "",
  rsquo: "’", lsquo: "‘", rdquo: "”", ldquo: "“", ndash: "–", mdash: "—", hellip: "…", bull: "•", middot: "·",
};

/** Character references decoded in one pass, so `&amp;lt;` stays the text `&lt;`. */
function decodeEntities(s: string): string {
  return s.replace(/&(#\d{1,7}|#x[0-9a-f]{1,6}|[a-z]{2,8});/gi, (m, ref: string) => {
    if (ref[0] !== "#") return ENTITY[ref.toLowerCase()] ?? m;
    const cp = ref[1] === "x" || ref[1] === "X" ? parseInt(ref.slice(2), 16) : parseInt(ref.slice(1), 10);
    return cp > 0 && cp <= 0x10ffff && !(cp >= 0xd800 && cp <= 0xdfff) ? String.fromCodePoint(cp) : "";
  });
}

const BLOCK_TAG = /^(br|p|li|div|h[1-6]|ul|ol|tr|table|section|article|header|footer|blockquote|pre|dd|dt)$/;
const RAW_TAG = /^(script|style|noscript|template|textarea|title|xmp|iframe|noembed|noframes)$/;

/**
 * HTML flattened to text without running or loading anything, in one forward pass (no backtracking
 * on hostile input). Script and style bodies are dropped; an element left open drops the rest.
 */
export function plainText(html: string): string {
  const lower = html.toLowerCase();
  const parts: string[] = [];
  let i = 0;
  let gtAt = -2; // the next ">" found so far, kept so "<a<a<a…" is scanned once, not once per "<"
  while (i < html.length) {
    const lt = html.indexOf("<", i);
    if (lt < 0) { parts.push(decodeEntities(html.slice(i))); break; }
    if (lt > i) parts.push(decodeEntities(html.slice(i, lt)));
    const c = html[lt + 1] ?? "";
    if (!/[A-Za-z/!?]/.test(c)) { parts.push("<"); i = lt + 1; continue; } // "a < b" is text
    if (html.startsWith("<!--", lt)) {
      const end = html.indexOf("-->", lt + 4);
      if (end < 0) break;
      i = end + 3;
      continue;
    }
    if (gtAt !== -1 && gtAt <= lt) gtAt = html.indexOf(">", lt + 1);
    const gt = gtAt;
    if (gt < 0) break; // an unclosed tag: the rest is markup, never text
    const inner = html.indexOf("<", lt + 1);
    if (inner >= 0 && inner < gt) { parts.push(" "); i = inner; continue; } // "<scr<script>": the inner "<" opens the tag
    const name = /^\/?\s*([a-z][a-z0-9-]*)/.exec(lower.slice(lt + 1, Math.min(gt, lt + 40)))?.[1] ?? "";
    const closing = html[lt + 1] === "/";
    if (!closing && RAW_TAG.test(name)) {
      const close = lower.indexOf(`</${name}`, gt + 1);
      if (close < 0) break; // a raw element left open drops everything after it
      const end = html.indexOf(">", close);
      parts.push(" ");
      if (end < 0) break;
      i = end + 1;
      continue;
    }
    parts.push(BLOCK_TAG.test(name) ? "\n" : " ");
    i = gt + 1;
  }
  return parts.join("")
    .replace(/[​-‍⁠﻿­]/g, "")
    .replace(/[  -   　]/g, " ")
    .replace(/[ \t\f\v\r]+/g, " ")
    .replace(/ *\n\s*/g, "\n")
    .trim();
}

/** Text that says something: at least one letter or digit. */
const hasText = (s: string): boolean => /[\p{L}\p{N}]/u.test(s);

/**
 * `fn` over `items` with at most `n` in flight. After the first failure no new item starts, every
 * started one is awaited (nothing keeps running once this returns), and that failure is rethrown.
 */
export async function pool<T>(items: readonly T[], n: number, fn: (item: T) => Promise<void>): Promise<void> {
  let next = 0;
  let failure: { e: unknown } | null = null;
  const worker = async (): Promise<void> => {
    while (failure === null && next < items.length) {
      const item = items[next++]!;
      try {
        await fn(item);
      } catch (e) {
        failure ??= { e };
      }
    }
  };
  await Promise.all(Array.from({ length: Math.max(1, Math.min(n, items.length)) }, worker));
  if (failure !== null) throw (failure as { e: unknown }).e;
}

// ---------------------------------------------------------------- Workday

export interface WorkdayBoard {
  origin: string;
  tenant: string;
  site: string;
  searchText: string;
  /** Facets the page has applied (multi-select within a facet is OR, across facets AND). */
  facets: Record<string, string[]>;
}

const WD_HOST = /^([a-z0-9][a-z0-9-]{0,62})\.wd\d{1,3}\.myworkdayjobs\.com$/i;
const WD_LOCALE = /^[a-z]{2}(-[A-Za-z]{2,4})?$/;
const WD_SITE = /^[A-Za-z0-9_-]{1,100}$/;
const WD_FACET_NAME = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;
const WD_FACET_ID = /^[0-9a-f]{32}$/i;
const WD_PATH = /^\/job\/[^?#\s]{1,1000}$/;

/** The Workday board a page belongs to, with the search and facets it shows; null when it is not one. */
export function parseWorkdayBoard(href: string): WorkdayBoard | null {
  let u: URL;
  try {
    u = new URL(href);
  } catch {
    return null;
  }
  const host = WD_HOST.exec(u.hostname);
  if (u.protocol !== "https:" || !host || u.port !== "") return null;
  const segs = u.pathname.split("/").filter(Boolean);
  if (segs[0] && WD_LOCALE.test(segs[0])) segs.shift();
  const site = segs[0] ?? "";
  if (!WD_SITE.test(site) || site === "job" || site === "wday") return null;
  const facets: Record<string, string[]> = {};
  let names = 0;
  for (const name of new Set(u.searchParams.keys())) {
    if (name === "q" || !WD_FACET_NAME.test(name)) continue;
    const ids = u.searchParams.getAll(name).flatMap(v => v.split(",")).filter(v => WD_FACET_ID.test(v));
    if (ids.length === 0) continue;
    if (++names > MAX_FACET_PARAMS) break;
    facets[name] = [...new Set(ids)].slice(0, MAX_FACET_VALUES);
  }
  return {
    origin: u.origin,
    tenant: host[1]!.toLowerCase(),
    site,
    searchText: (u.searchParams.get("q") ?? "").slice(0, 200),
    facets,
  };
}

interface Row { title: string; path: string; location: string; posted: string }
interface FacetChoice {
  param: string;
  values: Array<{ id: string; count: number }>;
  /** Some values were unreadable or past the bound: its buckets do not cover what it counts. */
  partial?: boolean;
}
/** `total` is null when the answer gave none that can be believed. */
interface ListPage { total: number | null; rows: Row[]; facets: FacetChoice[] }

/** The facets a listing answer offers, nested groups (Workday's location group) flattened. */
export function readFacets(raw: unknown): FacetChoice[] {
  const out: FacetChoice[] = [];
  const visit = (f: unknown, depth: number): void => {
    if (!isRecord(f) || depth > 3 || out.length >= 50) return;
    const param = typeof f.facetParameter === "string" ? f.facetParameter : "";
    const all = Array.isArray(f.values) ? f.values : [];
    let partial = all.length > MAX_ANSWER_FACET_VALUES;
    const leaf: Array<{ id: string; count: number }> = [];
    const ids = new Set<string>();
    for (const v of all.slice(0, MAX_ANSWER_FACET_VALUES)) {
      if (!isRecord(v)) { partial = true; continue; }
      if (Array.isArray(v.values)) { visit(v, depth + 1); continue; }
      const id = typeof v.id === "string" ? v.id.toLowerCase() : "";
      const count = typeof v.count === "number" && Number.isFinite(v.count) && v.count >= 0 ? v.count : -1;
      if (!WD_FACET_ID.test(id) || count < 0) { partial = true; continue; }
      if (ids.has(id)) continue; // the same bucket twice would be walked twice
      ids.add(id);
      leaf.push({ id, count });
    }
    if (WD_FACET_NAME.test(param) && leaf.length > 0) out.push(partial ? { param, values: leaf, partial } : { param, values: leaf });
  };
  if (Array.isArray(raw)) for (const f of raw) visit(f, 0);
  return out;
}

/**
 * The facet to split an over-window query by: one the query has not applied, whose buckets add up to
 * every job (a facet some jobs lack would lose them), with the fewest buckets still over the window.
 * Location facets come last: a job in three places is in three buckets. Null when none can split.
 */
export function chooseSplit(facets: readonly FacetChoice[], applied: Record<string, string[]>): (FacetChoice & { sum: number }) | null {
  const open = facets
    .filter(f => !f.partial && !(f.param in applied) && f.values.filter(v => v.count > 0).length >= 2)
    .map(f => ({ ...f, sum: f.values.reduce((a, v) => a + v.count, 0), loc: /location/i.test(f.param) }));
  if (open.length === 0) return null;
  const plain = open.filter(f => !f.loc);
  const expected = Math.max(...(plain.length > 0 ? plain : open).map(f => f.sum));
  // Location before the oversized count: its counts are inflated, so "fewer buckets over" proves nothing.
  const rank = (f: (typeof open)[number]): number[] => [
    f.sum >= expected ? 0 : 1,
    f.loc ? 1 : 0,
    f.values.filter(v => v.count >= WORKDAY_WINDOW).length,
    f.values.length,
  ];
  open.sort((a, b) => {
    const ra = rank(a), rb = rank(b);
    for (let i = 0; i < ra.length; i++) if (ra[i] !== rb[i]) return ra[i]! - rb[i]!;
    return 0;
  });
  const best = open[0]!;
  return { param: best.param, values: best.values, sum: best.sum };
}

/**
 * The board's own count of jobs: the total when under the window, else the largest sum of a facet's
 * buckets (facets count past the window). `corroborated` says two independent facets agree on it (or
 * the total itself is believable); one facet alone cannot show which jobs it leaves out.
 */
export function expectedTotal(total: number | null, facets: readonly FacetChoice[], applied: Record<string, string[]> = {}): { count: number; corroborated: boolean } {
  // Under the window the total is the board's own count of this query.
  if (total !== null && total < WORKDAY_WINDOW) return { count: total, corroborated: true };
  // A facet the query applies counts its other options across the whole board (so they can be added),
  // never this query's jobs: it is left out.
  const plain = facets.filter(f => !f.partial && !(f.param in applied) && !/location/i.test(f.param));
  const sums = plain.map(f => f.values.reduce((a, v) => a + v.count, 0));
  const count = Math.max(total ?? 0, ...sums);
  return { count, corroborated: sums.filter(s => s === count).length >= 2 };
}

/** A listing row's job path: under /job/, with no dot segment, encoded separator or backslash to climb out of it. */
export function safeJobPath(p: string): boolean {
  if (!WD_PATH.test(p) || p.includes("\\") || p.includes("//") || /%(2e|2f|5c)/i.test(p)) return false;
  return p.split("/").every(s => s !== "." && s !== "..");
}

function readRows(raw: unknown): { rows: Row[]; bad: number } {
  const rows: Row[] = [];
  let bad = 0;
  if (!Array.isArray(raw)) return { rows, bad };
  for (const r of raw.slice(0, 100)) {
    const title = isRecord(r) ? clean(r.title) : "";
    const path = isRecord(r) && typeof r.externalPath === "string" ? r.externalPath : "";
    if (!title || !safeJobPath(path)) { bad++; continue; }
    rows.push({ title, path, location: clean(r.locationsText), posted: clean(r.postedOn) });
  }
  return { rows, bad };
}

interface Ctx {
  http: Http;
  opts: Required<Pick<CrawlOptions, "maxRequests" | "delayMs" | "timeoutMs">> & CrawlOptions;
  requests: number;
  notes: string[];
  concurrency: number;
  /** Epoch ms past which no request starts. */
  deadline: number;
  /** Cleared by anything that leaves a job out. */
  complete: boolean;
  parts: number;
  expected?: number;
  corroborated?: boolean;
}

/** A note once, however many parts of the board hit it. */
function note(ctx: Ctx, s: string): void {
  if (!ctx.notes.includes(s)) ctx.notes.push(s);
}

/** Progress to the caller; a throwing callback never ends the crawl. */
function progress(ctx: Ctx, p: CrawlProgress): void {
  try { ctx.opts.onProgress?.(p); } catch { /* the caller's problem, not the crawl's */ }
}

class TooLarge extends Error {}

/** The body as text, refused (and the stream cancelled) the moment it passes `max` bytes. */
export async function readBody(res: Response, max: number): Promise<string> {
  const declared = Number(res.headers.get("content-length") ?? "");
  if (Number.isFinite(declared) && declared > max) throw new TooLarge("the board's answer is too large");
  const reader = res.body?.getReader();
  if (!reader) {
    const t = await res.text();
    if (bytes(t) > max) throw new TooLarge("the board's answer is too large");
    return t;
  }
  const chunks: Uint8Array[] = [];
  let n = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    n += value.byteLength;
    if (n > max) {
      await reader.cancel().catch(() => {});
      throw new TooLarge("the board's answer is too large");
    }
    chunks.push(value);
  }
  const all = new Uint8Array(n);
  let at = 0;
  for (const c of chunks) { all.set(c, at); at += c.byteLength; }
  return new TextDecoder().decode(all);
}

/** One bounded request: paced, timed out, retried on 429/5xx, its body size checked, its JSON parsed. */
async function requestJson(ctx: Ctx, url: string, init: RequestInit): Promise<unknown> {
  for (let attempt = 0; ; attempt++) {
    if (ctx.opts.signal?.aborted) throw new Stop("stopped");
    if (ctx.requests > 0) await ctx.http.sleep(attempt > 0 ? ctx.opts.delayMs * 4 * attempt : ctx.opts.delayMs);
    if (ctx.opts.signal?.aborted) throw new Stop("stopped");
    if (Date.now() >= ctx.deadline) throw new Stop(`stopped at the crawl's time limit of ${Math.round((ctx.opts.deadlineMs ?? 0) / 60_000)} min`);
    // Checked after the pause and counted with no await in between, so parallel workers cannot overshoot it.
    if (ctx.requests >= ctx.opts.maxRequests) throw new Stop(`stopped at the limit of ${ctx.opts.maxRequests} requests`);
    ctx.requests++;
    const ac = new AbortController();
    const onAbort = () => ac.abort();
    ctx.opts.signal?.addEventListener("abort", onAbort);
    const timer = setTimeout(() => ac.abort(), ctx.opts.timeoutMs);
    let res: Response;
    let body: string;
    try {
      res = await ctx.http.fetch(url, { ...init, signal: ac.signal, credentials: "same-origin", redirect: "error" });
      body = res.ok ? await readBody(res, MAX_BODY_BYTES) : "";
    } catch (e) {
      if (ctx.opts.signal?.aborted) throw new Stop("stopped");
      if (e instanceof TooLarge) throw e; // the same answer again would be as large
      if (attempt < MAX_RETRIES) continue;
      throw new Error(ac.signal.aborted ? `timed out after ${ctx.opts.timeoutMs} ms` : `request failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      clearTimeout(timer);
      ctx.opts.signal?.removeEventListener("abort", onAbort);
    }
    if (!res.ok) {
      if ((res.status === 429 || res.status >= 500) && attempt < MAX_RETRIES) continue;
      throw new Error(`the board answered HTTP ${res.status}`);
    }
    try {
      return JSON.parse(body);
    } catch {
      throw new Error("the board's answer is not JSON");
    }
  }
}

async function listPage(ctx: Ctx, b: WorkdayBoard, facets: Record<string, string[]>, offset: number): Promise<ListPage> {
  const raw = await requestJson(ctx, `${b.origin}/wday/cxs/${b.tenant}/${b.site}/jobs`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({ appliedFacets: facets, limit: WORKDAY_PAGE, offset, searchText: b.searchText }),
  });
  // A body without the array is an error page or a new shape, never an empty board.
  if (!isRecord(raw) || !Array.isArray(raw.jobPostings)) throw new Error(`the listing at offset ${offset} has no jobPostings`);
  const { rows, bad } = readRows(raw.jobPostings);
  if (bad > 0) {
    ctx.complete = false;
    note(ctx, `${bad} listing row${bad === 1 ? "" : "s"} at offset ${offset} had no title or usable job link`);
  }
  const total = typeof raw.total === "number" && Number.isInteger(raw.total) && raw.total >= 0 ? raw.total : null;
  return { total, rows, facets: readFacets(raw.facets) };
}

/**
 * Every row of one query into `seen`. Over the window the query is split by a facet; a page that
 * starts like page 1 is Workday serving page 1 again, and ends the walk.
 */
async function walkQuery(ctx: Ctx, b: WorkdayBoard, facets: Record<string, string[]>, depth: number, seen: Map<string, Row>): Promise<void> {
  const first = await listPage(ctx, b, facets, 0);
  ctx.parts++;
  const est = expectedTotal(first.total, first.facets, facets);
  if (depth === 0) {
    ctx.expected = est.count;
    ctx.corroborated = est.corroborated;
  }
  const here = new Set<string>(); // this query's rows, apart from what other buckets found
  const add = (rows: Row[]): number => {
    let n = 0;
    for (const r of rows) {
      if (here.has(r.path)) continue;
      here.add(r.path);
      n++;
      if (seen.has(r.path)) continue;
      if (seen.size >= MAX_ROWS) { ctx.complete = false; throw new Stop(`stopped at ${MAX_ROWS} jobs`); }
      seen.set(r.path, r);
    }
    return n;
  };
  const tell = () => progress(ctx, { phase: "listing", done: ctx.requests, of: ctx.expected ? Math.ceil(ctx.expected / WORKDAY_PAGE) : undefined, jobs: seen.size });
  add(first.rows);
  tell();

  // Over the window by the board's own count (its total, or its facets when the total is missing).
  if (est.count >= WORKDAY_WINDOW) {
    const split = depth < MAX_SPLIT_DEPTH ? chooseSplit(first.facets, facets) : null;
    if (split) {
      // Buckets of the top split run side by side; deeper splits run in turn, so in-flight requests stay at `concurrency`.
      const buckets = split.values.filter(v => v.count > 0);
      await pool(buckets, depth === 0 ? ctx.concurrency : 1, v => walkQuery(ctx, b, { ...facets, [split.param]: [v.id] }, depth + 1, seen));
      return;
    }
    ctx.complete = false;
    note(ctx, `a part of the board with ${WORKDAY_WINDOW}+ jobs could not be split further; only its first ${WORKDAY_WINDOW} were read`);
  }

  // Paged until a short page, an empty page, page 1 served again, or a page with nothing new; `total`
  // only says whether stopping there missed anything (a believable total, reached, means nothing was).
  const firstPath = first.rows[0]?.path;
  let ended: "short" | "empty" | "wrapped" | "stuck" | "window" = first.rows.length < WORKDAY_PAGE ? "short" : "window";
  for (let offset = WORKDAY_PAGE; ended === "window" && offset < WORKDAY_WINDOW; offset += WORKDAY_PAGE) {
    const page = await listPage(ctx, b, facets, offset);
    if (page.rows.length === 0) { ended = "empty"; break; }
    if (firstPath !== undefined && page.rows[0]?.path === firstPath) { ended = "wrapped"; break; }
    if (add(page.rows) === 0) { ended = "stuck"; break; }
    tell();
    if (page.rows.length < WORKDAY_PAGE) ended = "short";
  }
  const reached = first.total !== null && first.total < WORKDAY_WINDOW && here.size >= first.total;
  if (reached || ((ended === "short" || ended === "empty") && first.total === null)) return;
  if (ended === "short" || ended === "empty") {
    ctx.complete = false;
    note(ctx, `the board stopped listing at ${here.size} of the ${first.total} jobs it counts`);
  } else if (ended === "wrapped" || ended === "stuck") {
    ctx.complete = false;
    note(ctx, `the board ${ended === "wrapped" ? "served its first page again" : "repeated a page"} after ${here.size} jobs${first.total !== null ? ` of ${first.total}` : ""}`);
  } else if (est.count < WORKDAY_WINDOW) {
    ctx.complete = false;
    note(ctx, `a part of the board gave no usable count; only its first ${WORKDAY_WINDOW} jobs were read`);
  }
}

interface Detail { description: string; location: string; posted?: string; url?: string; title?: string }

/** The posting's own public link when it is this board's job page; anything else is ignored. */
function boardJobUrl(raw: unknown, b: WorkdayBoard): string | undefined {
  if (typeof raw !== "string" || raw.length > 2048) return undefined;
  try {
    const u = new URL(raw);
    if (u.origin !== b.origin || u.username || u.password) return undefined;
    const segs = u.pathname.split("/");
    const at = segs[1] && WD_LOCALE.test(segs[1]) ? 2 : 1;
    if (segs[at] !== b.site || !safeJobPath(`/${segs.slice(at + 1).join("/")}`)) return undefined;
    u.hash = "";
    return u.href;
  } catch {
    return undefined;
  }
}

function readDetail(raw: unknown, b: WorkdayBoard): Detail | null {
  const info = isRecord(raw) && isRecord(raw.jobPostingInfo) ? raw.jobPostingInfo : null;
  if (!info) return null;
  const places = [clean(info.location), ...(Array.isArray(info.additionalLocations) ? info.additionalLocations.slice(0, 200).map(clean) : [])].filter(Boolean);
  let location = places.join("; ");
  if (bytes(location) > MAX_LOCATION_BYTES) location = places.length > 1 ? `${places[0]} (+${places.length - 1} more)` : places[0] ?? "";
  if (bytes(location) > MAX_LOCATION_BYTES) location = clipBytes(location, MAX_LOCATION_BYTES);
  const start = typeof info.startDate === "string" && /^\d{4}-\d{2}-\d{2}$/.test(info.startDate) ? `${info.startDate}T00:00:00Z` : undefined;
  const text = plainText(typeof info.jobDescription === "string" ? info.jobDescription : "");
  return {
    description: hasText(text) ? text : "",
    location,
    posted: start,
    url: boardJobUrl(info.externalUrl, b),
    title: clean(info.title) || undefined,
  };
}

/** The whole Workday board `b`: its listing, then the descriptions of its most relevant jobs. */
export async function crawlWorkday(b: WorkdayBoard, options: CrawlOptions = {}, http: Http = defaultHttp()): Promise<CrawlReport> {
  const deadlineMs = options.deadlineMs ?? 15 * 60_000;
  const ctx: Ctx = {
    http,
    opts: { maxRequests: 600, delayMs: 250, timeoutMs: 15_000, ...options, deadlineMs },
    requests: 0,
    notes: [],
    concurrency: Math.max(1, Math.min(6, Math.floor(options.concurrency ?? 3))),
    deadline: Date.now() + deadlineMs,
    complete: true,
    parts: 0,
  };
  const seen = new Map<string, Row>();
  const company = clean(options.company) || b.tenant;
  try {
    await walkQuery(ctx, b, b.facets, 0, seen);
  } catch (e) {
    ctx.complete = false;
    note(ctx, e instanceof Stop ? e.message : `listing failed: ${e instanceof Error ? e.message : String(e)}`);
  }
  if (ctx.expected !== undefined && seen.size < ctx.expected && ctx.complete) {
    ctx.complete = false;
    note(ctx, `listed ${seen.size} of the ${ctx.expected} jobs the board counts`);
  }
  if (ctx.complete && ctx.expected !== undefined && ctx.expected >= WORKDAY_WINDOW && !ctx.corroborated) {
    ctx.complete = false;
    note(ctx, "the board's count rests on one facet, so jobs that facet leaves out cannot be ruled out");
  }

  const maxDetails = Math.max(0, Math.min(options.maxDetails ?? MAX_INGEST_JOBS, options.maxJobs ?? MAX_ROWS));
  const ranked = [...seen.values()]
    .map(r => ({ r, score: rankListing(r, options.profile) }))
    .sort((a, b2) => b2.score - a.score)
    .map(x => x.r);
  const slots: Array<IngestSource | undefined> = [];
  let shortened = 0;
  let failed = 0;
  let done = 0;
  const picked = ranked.slice(0, maxDetails);
  let failedInRow = 0;
  // A listing that failed part way still has its rows; only the user's stop ends the crawl here.
  if (options.signal?.aborted) {
    ctx.complete = false;
    note(ctx, "stopped");
  } else {
    try {
      await pool(picked.map((r, i) => ({ r, i })), ctx.concurrency, async ({ r, i }) => {
        let d: Detail | null = null;
        try {
          d = readDetail(await requestJson(ctx, `${b.origin}/wday/cxs/${b.tenant}/${b.site}${r.path}`, { method: "GET", headers: { Accept: "application/json" } }), b);
        } catch (e) {
          if (e instanceof Stop) throw e;
          d = null;
        }
        done++;
        if (!d || !d.description) {
          failed++;
          if (++failedInRow >= MAX_DETAIL_FAILS_IN_ROW) throw new Stop(`stopped after ${MAX_DETAIL_FAILS_IN_ROW} failures in a row`);
          return;
        }
        failedInRow = 0;
        const ex = excerpt(d.description);
        if (ex.shortened) shortened++;
        const job: IngestSource = {
          title: d.title ?? r.title,
          company,
          location: d.location || r.location,
          url: d.url ?? `${b.origin}/${b.site}${r.path}`,
          snippet: ex.text,
        };
        if (d.posted) job.posted = d.posted;
        slots[i] = job;
        progress(ctx, { phase: "details", done, of: picked.length, jobs: done - failed });
      });
    } catch (e) {
      ctx.complete = false;
      note(ctx, `descriptions ${e instanceof Error ? e.message : String(e)}`);
    }
  }
  const jobs = slots.filter((j): j is IngestSource => j !== undefined);
  if (failed > 0) {
    ctx.complete = false;
    note(ctx, `${failed} description${failed === 1 ? "" : "s"} could not be read; those jobs are not sent`);
  }
  if (seen.size > picked.length) {
    note(ctx, `${seen.size - picked.length} less relevant jobs were listed but not sent (descriptions fetched for the top ${picked.length})`);
  }
  const complete = ctx.complete && seen.size <= picked.length && jobs.length === picked.length;
  if (!complete && ctx.notes.length === 0) note(ctx, "stopped early");
  return {
    mode: "workday",
    jobs,
    listed: seen.size,
    expected: ctx.expected,
    requests: ctx.requests,
    parts: ctx.parts,
    shortened,
    complete,
    notes: ctx.notes,
  };
}

// ---------------------------------------------------------------- Any other listing

export interface PageJob { title: string; company: string; location: string; url: string; snippet: string; posted?: string }

export interface PageDeps {
  /** The listing on one document (scraper.ts's scrapePageJobs, adapted). */
  scrape(doc: Document, pageUrl: string): PageJob[];
  /** Fetch a same-listing URL and parse it; absent where no parser exists. */
  load?(url: string, signal: AbortSignal): Promise<Document>;
  sleep(ms: number): Promise<void>;
}

// Whole-string matches only: "Next Gen Platform Engineer" or "Next slide" is not a pager.
const ARROWS = "[›»>→]{1,2}";
const NEXT_WORDS = "(go to )?(the )?next( page| results| set of results)?|more results";
const NEXT_TEXT = new RegExp(`^(${NEXT_WORDS})(\\s*${ARROWS})?$`, "i");
const ARROW_ONLY = new RegExp(`^${ARROWS}$`);
const MORE_TEXT = /^(load|show|see|view) more( jobs| results| positions| openings| roles)?$/i;
/** Controls scanned for the pager; past this many, a missing pager is reported, never read as the end. */
const MAX_CONTROLS = 5000;

/** Disabled on itself or on one of its three nearest ancestors (Bootstrap disables the <li>). */
function disabled(el: Element): boolean {
  let n: Element | null = el;
  for (let up = 0; n && up < 4; up++, n = n.parentElement) {
    if (n.hasAttribute("disabled") || n.getAttribute("aria-disabled") === "true" || /\bdisabled\b/i.test(n.getAttribute("class") ?? "")) return true;
  }
  return false;
}

export type NextStep = { kind: "click"; el: HTMLElement } | { kind: "load"; url: string } | { kind: "none"; why?: string };

// Query parameters that only page through a listing; any other difference is a different search.
const PAGE_PARAM = /^(page|p|pg|pagenum|pagenumber|page_number|paged|offset|start|startrow|from|skip|cursor|after|pageindex|currentpage)$/i;

/**
 * Two URLs are pages of one listing: same origin, the same path (or one that differs only by a
 * trailing /page/N), and the same query apart from paging parameters.
 */
export function sameListing(a: string, b: string): boolean {
  let x: URL, y: URL;
  try {
    x = new URL(a);
    y = new URL(b);
  } catch {
    return false;
  }
  if (x.origin !== y.origin || (y.protocol !== "https:" && y.protocol !== "http:")) return false;
  const strip = (p: string) => p.replace(/\/+$/, "").replace(/\/page\/\d{1,5}$/i, "");
  if (strip(x.pathname) !== strip(y.pathname)) return false;
  const search = (u: URL) => [...u.searchParams].filter(([k]) => !PAGE_PARAM.test(k)).map(([k, v]) => `${k}=${v}`).sort().join("&");
  return search(x) === search(y);
}

/** The same document with only the fragment changed: a JS pager's link, to be clicked. */
function sameDocument(a: string, b: string): boolean {
  try {
    const x = new URL(a), y = new URL(b);
    x.hash = "";
    y.hash = "";
    return x.href === y.href;
  } catch {
    return false;
  }
}

/** A submit button inside a form would submit it and navigate the tab. */
function submits(el: HTMLElement): boolean {
  const form = (el as Partial<HTMLButtonElement>).form;
  return el.localName === "button" && form != null && (el.getAttribute("type") ?? "submit").toLowerCase() === "submit";
}

/** The listing's own control for its next page, or why there is none to take. */
export function findNext(doc: Document, pageUrl: string): NextStep {
  const toStep = (el: HTMLElement | null, href: string): NextStep | null => {
    if (/^\s*(javascript|vbscript|data):/i.test(href)) return el ? { kind: "click", el } : null;
    let url = "";
    try { url = new URL(href, pageUrl).href; } catch { return { kind: "none", why: "the next-page link is not a link" }; }
    if (sameDocument(pageUrl, url)) return el ? { kind: "click", el } : null;
    if (!sameListing(pageUrl, url)) return { kind: "none", why: "the next-page link leaves this listing, so it was not followed" };
    // A real link would navigate the tab away (and end this script), so its page is fetched instead.
    return { kind: "load", url };
  };

  const rel = doc.querySelector('a[rel~="next"][href], link[rel~="next"][href]');
  const relHref = rel?.getAttribute("href");
  if (rel && relHref) {
    if (/^\s*(javascript|vbscript|data):/i.test(relHref)) return { kind: "none", why: "the next-page link leaves this listing, so it was not followed" };
    const step = toStep(rel.localName === "a" && !disabled(rel) ? rel as HTMLElement : null, relHref);
    if (step) return step;
  }

  const found = doc.querySelectorAll('button, a, [role="button"]');
  const all = Array.from(found).slice(0, MAX_CONTROLS) as HTMLElement[];
  const label = (el: HTMLElement) => (el.getAttribute("aria-label") ?? "").replace(/\s+/g, " ").trim();
  const text = (el: HTMLElement) => (el.textContent ?? "").replace(/\s+/g, " ").trim();
  // Strongest signal first; a bare arrow (often a carousel's) only when nothing better is there.
  const tiers: Array<(el: HTMLElement) => boolean> = [
    el => el.getAttribute("data-uxi-element-id") === "next",
    el => NEXT_TEXT.test(label(el)) || NEXT_TEXT.test(text(el)),
    el => MORE_TEXT.test(text(el)) || MORE_TEXT.test(label(el)),
    el => ARROW_ONLY.test(text(el)) && label(el) === "",
  ];
  let next: HTMLElement | undefined;
  for (const tier of tiers) {
    const matches = all.filter(el => tier(el) && !submits(el));
    next = matches.find(el => !disabled(el));
    // Every match disabled is the last page: the listing's end.
    if (!next && matches.length > 0) return { kind: "none" };
    if (next) break;
  }
  if (!next) {
    return found.length > MAX_CONTROLS ? { kind: "none", why: `the page has over ${MAX_CONTROLS} links and buttons; its pager was not found among them` } : { kind: "none" };
  }
  const href = next.localName === "a" ? (next.getAttribute("href") ?? "").trim() : "";
  if (!href || href === "#") return { kind: "click", el: next };
  return toStep(next, href) ?? { kind: "click", el: next };
}

const fingerprint = (jobs: readonly PageJob[]): string => `${jobs.length}|${jobs[0]?.url ?? ""}|${jobs[jobs.length - 1]?.url ?? ""}`;

/** A job link as a dedupe key: the URL as parsed (host lowercased, path case kept), without a trailing slash or fragment. */
function jobKey(url: string): string {
  try {
    const u = new URL(url);
    u.hash = "";
    return u.href.replace(/\/$/, "");
  } catch {
    return "";
  }
}

/** Every page of the listing on `doc`, walked through its own pagination. */
export async function crawlPages(doc: Document, pageUrl: string, deps: PageDeps, options: CrawlOptions = {}): Promise<CrawlReport> {
  const maxPages = Math.max(1, options.maxPages ?? 50);
  const maxJobs = Math.max(1, options.maxJobs ?? 2000);
  const waitMs = options.pageWaitMs ?? 10_000;
  const delay = options.delayMs ?? 400;
  const notes: string[] = [];
  const seen = new Map<string, PageJob>();
  let cur = doc;
  let url = pageUrl;
  let pages = 0;
  let requests = 0;
  let complete = false;
  let shortened = 0;
  const keep = (jobs: readonly PageJob[]): number => {
    let n = 0;
    for (const j of jobs) {
      const key = jobKey(j.url);
      if (!key || seen.has(key)) continue;
      if (seen.size >= maxJobs) return n;
      const ex = excerpt(j.snippet);
      if (ex.shortened) shortened++;
      seen.set(key, { ...j, snippet: ex.text });
      n++;
    }
    return n;
  };
  try {
    for (;;) {
      if (options.signal?.aborted) { notes.push("stopped"); break; }
      const found = deps.scrape(cur, url);
      const fresh = keep(found);
      pages++;
      options.onProgress?.({ phase: "pages", done: pages, jobs: seen.size });
      if (pages > 1 && fresh === 0) { notes.push(`page ${pages} showed no jobs not already seen`); break; }
      if (seen.size >= maxJobs) { notes.push(`stopped at ${maxJobs} jobs`); break; }
      if (pages >= maxPages) { notes.push(`stopped at the limit of ${maxPages} pages`); break; }
      const step = findNext(cur, url);
      if (step.kind === "none") {
        if (step.why) notes.push(step.why);
        else complete = true;
        break;
      }
      await deps.sleep(delay);
      if (options.signal?.aborted) { notes.push("stopped"); break; }
      if (step.kind === "load") {
        if (!deps.load) { notes.push("this page's next page is a separate page, which cannot be read here"); break; }
        const ac = new AbortController();
        const onAbort = () => ac.abort();
        options.signal?.addEventListener("abort", onAbort);
        const timer = setTimeout(() => ac.abort(), options.timeoutMs ?? 15_000);
        try {
          requests++;
          cur = await deps.load(step.url, ac.signal);
          url = step.url;
        } catch (e) {
          notes.push(options.signal?.aborted ? "stopped" : `page ${pages + 1} could not be loaded: ${e instanceof Error ? e.message : String(e)}`);
          break;
        } finally {
          clearTimeout(timer);
          options.signal?.removeEventListener("abort", onAbort);
        }
        continue;
      }
      if (cur !== doc) { notes.push("a fetched page's controls cannot be clicked"); break; }
      const before = fingerprint(found);
      step.el.click();
      // The new page counts once the list has changed, is not empty, and reads the same on two polls in a
      // row: an emptied list while loading, or a half-rendered one, is never taken as the page.
      let changed = false;
      let last = "";
      for (let waited = 0; waited < waitMs; waited += 250) {
        await deps.sleep(250);
        if (options.signal?.aborted) break;
        const now = deps.scrape(doc, url);
        const fp = fingerprint(now);
        if (now.length > 0 && fp !== before && fp === last) { changed = true; break; }
        last = now.length > 0 && fp !== before ? fp : "";
      }
      if (options.signal?.aborted) { notes.push("stopped"); break; }
      if (!changed) { notes.push(`page ${pages + 1} did not load within ${Math.round(waitMs / 1000)} s`); break; }
    }
  } catch (e) {
    notes.push(`the walk failed: ${e instanceof Error ? e.message : String(e)}`);
  }
  return {
    mode: "pages",
    jobs: [...seen.values()],
    listed: seen.size,
    requests,
    parts: pages,
    shortened,
    complete,
    notes: complete ? [] : notes,
  };
}

// ---------------------------------------------------------------- Handing the crawl to Vani

export interface SendResult {
  /** Jobs Vani took (queued, or already known), as its replies count them. */
  sent: number;
  batches: number;
  outcomes: IngestOutcome[];
  /** Words for jobs no batch could carry. */
  leftOut: string;
}

/**
 * A batch's size as the Mac host measures it against its 1 MiB bound (JobIngest.maxPayloadBytes,
 * then `vani jobs ingest`'s stdin bound): JSONSerialization writes every "/" as "\/", which
 * JSON.stringify does not, so each one counts twice.
 */
export function wireBytes(x: unknown): number {
  const s = JSON.stringify(x);
  let slashes = 0;
  for (let i = s.indexOf("/"); i >= 0; i = s.indexOf("/", i + 1)) slashes++;
  return bytes(s) + slashes;
}

/** A batch is closed before it passes this, well under the host's 1 MiB. */
export const MAX_BATCH_BYTES = 800 * 1024;

/** The next batch from `start`: at most MAX_INGEST_JOBS jobs and MAX_BATCH_BYTES on the wire. */
function nextBatch(jobs: readonly IngestSource[], start: number, page: IngestPage): { jobs: ReturnType<typeof buildIngestBatch>["jobs"]; skipped: number; end: number } {
  const out: ReturnType<typeof buildIngestBatch>["jobs"] = [];
  let skipped = 0;
  let size = wireBytes({ op: "jobsIngest", page, save_search: true, jobs: [] });
  let i = start;
  for (; i < jobs.length && out.length < MAX_INGEST_JOBS; i++) {
    const one = buildIngestBatch([jobs[i]!]);
    const job = one.jobs[0];
    if (!job) { skipped += one.skipped; continue; }
    const add = wireBytes(job) + 1;
    if (out.length > 0 && size + add > MAX_BATCH_BYTES) break;
    out.push(job);
    size += add;
  }
  return { jobs: out, skipped, end: i };
}

/**
 * The crawl's jobs to the background as `ingestJobs` messages of at most MAX_INGEST_JOBS jobs and
 * MAX_BATCH_BYTES each. Only the first batch asks to save the search. A failed batch ends the send:
 * the next would fail alike.
 */
export async function sendCrawl(
  report: Pick<CrawlReport, "jobs">,
  page: IngestPage,
  saveSearch: boolean,
  send: (msg: unknown) => Promise<unknown> = m => chrome.runtime.sendMessage(m),
  signal?: AbortSignal,
): Promise<SendResult> {
  const outcomes: IngestOutcome[] = [];
  let sent = 0;
  let batches = 0;
  let skipped = 0;
  for (let i = 0; i < report.jobs.length;) {
    if (signal?.aborted) {
      return { sent, batches, outcomes, leftOut: joinLeftOut(leftOutText({ skipped, overLimit: 0 }), `${report.jobs.length - i} not sent (stopped)`) };
    }
    const start = i;
    const batch = nextBatch(report.jobs, start, page);
    i = batch.end;
    if (batch.jobs.length === 0) { skipped += batch.skipped; continue; }
    let reply: unknown;
    try {
      reply = await send({ type: "ingestJobs", page, saveSearch: saveSearch && batches === 0, jobs: batch.jobs });
    } catch (e) {
      reply = { error: e instanceof Error ? e.message : String(e) };
    }
    batches++;
    const outcome = describeIngest(reply);
    outcomes.push(outcome);
    // Only a well-formed ingest result with no error is an answer to go on from; anything else ends the send.
    if (!isIngestReply(reply) || (isRecord(reply) && reply.error !== undefined)) {
      // This batch's own unusable jobs are in the remainder, so they are not counted again as skipped.
      // Why is the batch's own outcome (in `outcomes`); repeating it here would say it twice.
      return { sent, batches, outcomes, leftOut: joinLeftOut(leftOutText({ skipped, overLimit: 0 }), `${report.jobs.length - start} not sent`) };
    }
    skipped += batch.skipped;
    sent += reply.queued + (reply.duplicates ?? 0);
  }
  return { sent, batches, outcomes, leftOut: leftOutText({ skipped, overLimit: 0 }) };
}

const joinLeftOut = (...parts: string[]): string => parts.filter(Boolean).join("; ");

/** The crawl a page calls for: Workday's API on a Workday board, its own pagination anywhere else. */
export async function crawlListing(doc: Document, pageUrl: string, deps: PageDeps, options: CrawlOptions = {}, http?: Http): Promise<CrawlReport> {
  const wd = parseWorkdayBoard(pageUrl);
  return wd ? crawlWorkday(wd, options, http) : crawlPages(doc, pageUrl, deps, options);
}

/** The crawl's outcome in one line, saying what was left out; never reads as complete when it was not. */
export function describeCrawl(r: CrawlReport): string {
  const of = r.expected !== undefined && r.expected > r.listed ? ` of ${r.expected}` : "";
  const head = r.mode === "workday"
    ? `Listed ${r.listed}${of} jobs in ${r.parts} quer${r.parts === 1 ? "y" : "ies"}; ${r.jobs.length} ready with descriptions`
    : `Read ${r.jobs.length} jobs from ${r.parts} page${r.parts === 1 ? "" : "s"}`;
  const tail = r.complete ? "" : ` (incomplete: ${r.notes.join("; ") || "stopped early"})`;
  return `${head}${tail}.`;
}
