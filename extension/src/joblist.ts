// What the Jobs tab and the in-page scrape drawer show. Filtering and the score
// pill live here so both screens say the same thing: a page-text score is an
// estimate, and Vani's own row is relevance (jobrow.ts scoreWord).

import { type RelevanceResult, extractWorkplaceType } from "./relevance";
import type { ScrapedJob } from "./scraper";
import { scoreWord } from "./jobrow";

/** How many listings the in-page drawer previews; the popup lists the rest. */
export const DRAWER_PREVIEW = 8;

export interface JobFilter {
  query: string;
  minScore: number;
  remoteOnly: boolean;
}

/** A non-finite score sorts after every finite one, so it never reads as a top match. */
function scoreOf(j: ScrapedJob): number {
  return Number.isFinite(j.relevance.score) ? j.relevance.score : Number.NEGATIVE_INFINITY;
}

export function scoreTone(score: number): "ok" | "rev" | "muted" {
  if (!Number.isFinite(score) || score < 60) return "muted";
  if (score < 80) return "rev";
  return "ok";
}

/** "82% estimate" or "82% relevance". The word is which number it is. */
export function scorePillText(rel: Pick<RelevanceResult, "score" | "source">): string {
  const n = Number.isFinite(rel.score) ? Math.max(0, Math.min(100, Math.round(rel.score))) : 0;
  return `${n}% ${scoreWord(rel).toLowerCase()}`;
}

export function filtering(f: JobFilter): boolean {
  const min = Number.isFinite(f.minScore) ? f.minScore : 0;
  return f.query.trim() !== "" || min > 0 || f.remoteOnly;
}

export function countLabel(shown: number, total: number, active: boolean): string {
  if (total <= 0) return "Nothing scraped yet";
  if (!active) return total === 1 ? "1 job" : `${total} jobs`;
  return `${shown} of ${total}`;
}

function isRemote(j: ScrapedJob): boolean {
  const typed = j.relevance.workplaceType;
  if (typed === "Remote") return true;
  // Hybrid and on-site already applied the scorer's negation rules. A location
  // that merely contains the word "remote" must not override them.
  if (typed === "Hybrid" || typed === "On-site") return false;
  return extractWorkplaceType(j.location) === "Remote";
}

function haystack(j: ScrapedJob): string {
  return `${j.title} ${j.company} ${j.location} ${j.snippet} ${j.relevance.matchedSkills.join(" ")}`.toLowerCase();
}

/** Jobs that pass the filter, best score first. Equal scores keep scrape order. */
export function selectJobs(jobs: readonly ScrapedJob[], f: JobFilter): ScrapedJob[] {
  const q = f.query.trim().toLowerCase();
  const min = Number.isFinite(f.minScore) ? f.minScore : 0;
  const ranked = jobs.map((j, i) => ({ j, i })).filter(({ j }) => {
    if (min > 0 && scoreOf(j) < min) return false;
    if (f.remoteOnly && !isRemote(j)) return false;
    if (q && !haystack(j).includes(q)) return false;
    return true;
  });
  ranked.sort((a, b) => scoreOf(b.j) - scoreOf(a.j) || a.i - b.i);
  return ranked.map(x => x.j);
}

/** A workplace pill, omitted when the location already states that type or the type is unknown. A location that only contains the word ("not remote") does not count as stating it. */
export function workplaceLabel(j: Pick<ScrapedJob, "location" | "relevance">): string {
  const t = j.relevance.workplaceType;
  if (!t || t === "Unknown") return "";
  if (extractWorkplaceType(j.location) === t) return "";
  return t;
}

/** The company line. A blank name is said plainly; the page host is not used as a stand-in. */
export function companyLine(company: string): string {
  const name = company.trim();
  return name === "" ? "Company not listed" : name;
}

/** Tooltip for Copy. It names a link or a snippet only when the copied text will include one. */
export function copyActionTitle(hasLink: boolean, hasSnippet: boolean): string {
  const bits = ["title", "company"];
  if (hasLink) bits.push("link");
  if (hasSnippet) bits.push("snippet");
  const last = bits[bits.length - 1] ?? "title";
  return `Copy the ${bits.slice(0, -1).join(", ")} and ${last}`;
}

/** What Save and export send. A filter sends the jobs on screen, in that order. No filter sends the whole scrape, in scrape order. */
export function jobsToActOn(jobs: readonly ScrapedJob[], f: JobFilter): ScrapedJob[] {
  return filtering(f) ? selectJobs(jobs, f) : jobs.slice();
}

/** The Save button's own words. A narrowed list says how many of the scrape will be sent. */
export function saveLabel(shown: number, _total: number, active: boolean): string {
  if (active && shown > 0) return shown === 1 ? "Save 1 shown" : `Save ${shown} shown`;
  return "Save to Vani";
}

function setPhrase(shown: number, total: number, active: boolean): string {
  if (active) return shown === 1 ? "the 1 shown job" : `the ${shown} shown jobs`;
  return total === 1 ? "the 1 scraped job" : `all ${total} scraped jobs`;
}

/** Tooltip for Save, CSV, or JSON. It names the set the click will send. */
export function actionTitle(kind: "save" | "csv" | "json", shown: number, total: number, active: boolean): string {
  if (shown <= 0) {
    const empty = total <= 0 ? (kind === "save" ? "Scrape a page first" : "Nothing to export") : "Nothing matches these filters";
    return empty;
  }
  const set = setPhrase(shown, total, active);
  if (kind === "save") return `Send ${set} to Vani`;
  if (kind === "csv") return `Export ${set} as CSV`;
  return `Export ${set} as JSON`;
}

/** A sentence under the buttons when the list is narrower than the scrape. Empty when the two sets are the same. */
export function scopeNote(shown: number, total: number, active: boolean): string {
  if (!active || shown <= 0 || shown === total) return "";
  return shown === 1
    ? "Save and export use the 1 shown job."
    : `Save and export use the ${shown} shown jobs.`;
}

/**
 * Extra sentence when Save will leave jobs out of the set it was given. Empty when every job
 * in that set can be sent. `leftOut` is ingest.ts `leftOutText` (the reason), not a second count.
 */
export function saveSendNote(actedOn: number, sendable: number, leftOut: string, lead = "Save"): string {
  if (!Number.isFinite(actedOn) || actedOn <= 0) return "";
  const n = Number.isFinite(sendable) ? Math.max(0, Math.min(actedOn, Math.trunc(sendable))) : actedOn;
  if (n >= actedOn) return "";
  const why = leftOut.trim().replace(/[.\s]+$/, "");
  if (n === 0) return why ? `${lead} cannot send these jobs: ${why}.` : `${lead} cannot send these jobs.`;
  const count = n === 1 ? "1" : String(n);
  return why ? `${lead} sends ${count} of these ${actedOn}. ${why}.` : `${lead} sends ${count} of these ${actedOn}.`;
}

/** The drawer's one-line summary. `shown` is the preview length. `total` is the jobs array Save is given, never the page's own totalFound. */
export function drawerSummary(total: number, shown: number, sendable = total, leftOut = ""): string {
  if (!Number.isFinite(total) || total <= 0) return "No jobs on this page.";
  const whole = Math.trunc(total);
  const head = shown < whole
    ? `Showing ${shown} of ${whole}.`
    : whole === 1 ? "1 job on this page." : `${whole} jobs on this page.`;
  const skipped = saveSendNote(whole, sendable, leftOut, "Save below");
  if (skipped) return `${head} ${skipped}`;
  if (shown < whole) return `${head} Save below sends all ${whole}.`;
  return head;
}
