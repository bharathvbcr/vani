// "Crawl all pages → Vani" in the scrape drawer: runs crawler.ts on the page the user is on, shows its
// progress in the page, and hands the jobs to Vani through the ingest path (ingestJobs). It runs in the
// content script, where the board's own API is same-origin, and keeps going if the popup closes.

import { type CrawlProgress, type CrawlReport, type Http, type PageDeps, type PageJob, crawlListing, describeCrawl, readBody, sendCrawl } from "./crawler";
import type { UserTargetProfile } from "./relevance";
import { scrapePageJobs, scrubPageUrl, workdayCompany } from "./scraper";

const MAX_PAGE_TITLE_BYTES = 300;
/**
 * The most postings the daemon's Workday adapter lists for a board with no `max_postings` of its
 * own (internal/sources/workday MaxPostings), which a board saved from here never has; a bigger
 * board saved as a daily search would fail on every run.
 */
export const DAEMON_WORKDAY_MAX = 5_000;
const MAX_PAGE_BYTES = 4 * 1024 * 1024;

export interface CrawlRunDeps {
  doc: Document;
  pageUrl: string;
  pageTitle: string;
  profile?: Partial<UserTargetProfile>;
  /** chrome.runtime.sendMessage, to the background. */
  send(msg: unknown): Promise<unknown>;
  /** For tests: the Workday API and the page loader's transport. */
  http?: Http;
  parseHtml?(html: string): Document;
}

/** Progress in words. */
export function progressText(p: CrawlProgress): string {
  if (p.phase === "listing") return `Listing the board… ${p.jobs} jobs found${p.of ? ` (about ${p.of} pages)` : ""}`;
  if (p.phase === "details") return `Reading descriptions ${p.done}/${p.of ?? "?"}…`;
  return `Page ${p.done}: ${p.jobs} jobs so far…`;
}

/** A same-listing page fetched and parsed inert (no script runs, nothing loads), its size bounded. */
function pageLoader(deps: CrawlRunDeps): PageDeps["load"] {
  const parse = deps.parseHtml ?? (typeof DOMParser === "undefined" ? undefined : (html: string) => new DOMParser().parseFromString(html, "text/html"));
  if (!parse) return undefined;
  const get = deps.http?.fetch ?? ((u: string, i: RequestInit) => globalThis.fetch(u, i));
  return async (url, signal) => {
    const res = await get(url, { signal, credentials: "same-origin", redirect: "error", headers: { Accept: "text/html" } });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    if (!/html/i.test(res.headers.get("content-type") ?? "")) throw new Error("the next page is not HTML");
    return parse(await readBody(res, MAX_PAGE_BYTES));
  };
}

const bytes = (s: string): number => new TextEncoder().encode(s).length;

/**
 * Crawl the listing `deps.pageUrl` shows and send it to Vani. Stop (the signal) ends the crawl and
 * sends nothing. The answer says what was read, what was left out and what Vani said, never more.
 */
export async function runCrawlToVani(deps: CrawlRunDeps, saveSearch: boolean, signal: AbortSignal, onStatus: (text: string) => void): Promise<{ text: string; report: CrawlReport }> {
  const host = (() => { try { return new URL(deps.pageUrl).hostname; } catch { return ""; } })();
  const scrape = (d: Document, url: string): PageJob[] =>
    scrapePageJobs(d, host, deps.profile, url).jobs.map(j => ({ title: j.title, company: j.company, location: j.location, url: j.url, snippet: j.description || j.snippet, posted: j.posted }));
  const pageDeps: PageDeps = { scrape, load: pageLoader(deps), sleep: ms => (deps.http ? deps.http.sleep(ms) : new Promise(r => setTimeout(r, ms))) };
  const company = workdayCompany(deps.doc); // the board's API does not name it
  onStatus("Starting the crawl…");
  const report = await crawlListing(deps.doc, deps.pageUrl, pageDeps, {
    signal,
    profile: deps.profile,
    company: company || undefined,
    onProgress: p => onStatus(progressText(p)),
  }, deps.http);
  const read = describeCrawl(report);
  if (signal.aborted) return { text: `Stopped. ${read} Nothing was sent.`, report };
  if (report.jobs.length === 0) return { text: `${read} Nothing to send.`, report };

  const pageUrl = scrubPageUrl(deps.pageUrl);
  if (!pageUrl) return { text: `${read} This page has no web address Vani can use, so nothing was sent.`, report };
  const tooBig = report.mode === "workday" && Math.max(report.expected ?? 0, report.listed) > DAEMON_WORKDAY_MAX;
  const save = saveSearch && pageUrl.startsWith("https://") && !tooBig;
  const page = { url: pageUrl, title: bytes(deps.pageTitle) <= MAX_PAGE_TITLE_BYTES ? deps.pageTitle : "" };
  onStatus(`Sending ${report.jobs.length} jobs to Vani…`);
  const sent = await sendCrawl(report, page, save, deps.send, signal);
  // Each part is a sentence of its own; its own closing stop is dropped so none ends in "..".
  const bare = (s: string) => s.replace(/[.\s]+$/, "");
  const said = sent.outcomes.map(o => bare(o.text));
  const vani = said.length === 0 ? "nothing was sent" : said.length === 1 ? said[0] : `${sent.batches} batches: ${said.join("; ")}`;
  const notSaved = !saveSearch || save ? ""
    : tooBig ? ` The search was not saved: this board has over ${DAEMON_WORKDAY_MAX} jobs, more than Vani's daily Workday listing reads; narrow it with a search or filter and crawl again to save it.`
      : " The search was not saved (the page is not https).";
  return { text: `${read} Vani: ${vani}.${sent.leftOut ? ` ${bare(sent.leftOut)}.` : ""}${notSaved}`, report };
}

let running: AbortController | null = null;

/**
 * The drawer row: a "Crawl all pages → Vani" button, a Stop button while it runs, and its status.
 * One crawl at a time per page. Status text is set as text, never as markup.
 */
export function crawlRow(doc: Document, deps: () => CrawlRunDeps, saveSearch: () => boolean, toast: (text: string) => void): HTMLElement {
  const row = doc.createElement("div");
  row.style.cssText = "display:flex;flex-direction:column;gap:4px;margin-top:4px;";
  const buttons = doc.createElement("div");
  buttons.style.cssText = "display:flex;gap:4px;";
  const go = doc.createElement("button");
  go.type = "button";
  go.textContent = "Crawl all pages → Vani";
  go.title = "Read every page of this listing and send its jobs to Vani. On a Workday board it reads the board's own API and fetches the descriptions of the most relevant jobs; elsewhere a job carries what its listing shows";
  const stop = doc.createElement("button");
  stop.type = "button";
  stop.textContent = "Stop";
  stop.className = "s";
  stop.hidden = true;
  const status = doc.createElement("div");
  status.setAttribute("role", "status");
  status.style.cssText = "font-size:10px;color:#cbd5e1;";
  buttons.append(go, stop);
  row.append(buttons, status);

  go.addEventListener("click", () => {
    if (running) { status.textContent = "A crawl is already running on this page."; return; }
    const ac = new AbortController();
    running = ac;
    go.disabled = true;
    stop.hidden = false;
    const say = (t: string) => { status.textContent = t; };
    void runCrawlToVani(deps(), saveSearch(), ac.signal, say)
      .then(r => { say(r.text); toast(r.report.jobs.length > 0 && !ac.signal.aborted ? "Crawl finished; see the drawer for what Vani took." : "Crawl ended; see the drawer."); })
      .catch((e: unknown) => { say(`The crawl failed: ${e instanceof Error ? e.message : String(e)}`); })
      .finally(() => { running = null; go.disabled = false; stop.hidden = true; });
  });
  stop.addEventListener("click", () => { running?.abort(); status.textContent = "Stopping…"; });
  return row;
}
