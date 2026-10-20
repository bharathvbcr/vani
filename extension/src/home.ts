// The popup's Home tab, as pure functions: which view the current page gets, how ready the
// form is, how complete the saved profile is, and what a raw exception should say to a
// person. No DOM and no chrome.* here, so every rule is unit-tested (test/home.test.ts).
import type { Bank } from "./types";
import { BASICS, basicsValues } from "./basics";
import type { Coverage } from "./coverage";

/** idle: nothing Vani can act on here. disabled: the user switched Vani off for this host. error: the page could not be read. */
export type HomeView = "form" | "job" | "idle" | "disabled" | "error";

export interface HomeInput {
  /** The scan itself failed (no tab, no access, no reply). */
  error?: boolean;
  disabled?: boolean;
  /** Controls found across every frame. */
  found: number;
  portal?: { isJobBoard?: boolean; isATS?: boolean } | null;
  job?: { title?: unknown; description?: unknown } | null;
  relevance?: { score?: unknown } | null;
}

/** A description shorter than this is a snippet or a menu, not a posting (content.ts uses the same bar). */
export const MIN_JOB_DESCRIPTION = 40;

function text(x: unknown): string {
  return typeof x === "string" ? x.trim() : "";
}

/** Whether the page reads as a job posting with something to show. */
export function looksLikeJob(i: Pick<HomeInput, "portal" | "job" | "relevance">): boolean {
  const title = text(i.job?.title);
  const desc = text(i.job?.description);
  if (!title && !desc) return false;
  if (i.portal?.isJobBoard === true || i.portal?.isATS === true) return true;
  const score = typeof i.relevance?.score === "number" && Number.isFinite(i.relevance.score) ? i.relevance.score : 0;
  return score > 0 && desc.length >= MIN_JOB_DESCRIPTION;
}

/**
 * The frame whose posting Home shows and rates. A frame reports relevance only for a
 * posting it read (content.ts scan()), so the job and its rating come from one frame:
 * the top frame's when it rated one, else the first frame that did.
 */
export function ratedFrame<T extends { frameId: number; reply: { relevance?: unknown } }>(answered: readonly T[]): T | undefined {
  const rated = answered.filter(a => a.reply.relevance !== undefined && a.reply.relevance !== null);
  return rated.find(a => a.frameId === 0) ?? rated[0];
}

/** The one place that decides what Home shows; the card toggles elsewhere only follow it. */
export function homeState(i: HomeInput): HomeView {
  if (i.error) return "error";
  if (i.disabled) return "disabled";
  if (Number.isFinite(i.found) && i.found > 0) return "form";
  return looksLikeJob(i) ? "job" : "idle";
}

/** The three blocks of the Home tab: the form tools, the job card, and the dashboard. */
export function viewSections(v: HomeView): { form: boolean; job: boolean; dashboard: boolean } {
  return { form: v === "form", job: v === "form" || v === "job", dashboard: v !== "form" && v !== "job" };
}

export function homeHeadline(v: HomeView, host: string): { title: string; desc: string } {
  switch (v) {
    case "disabled":
      return { title: "Vani is off on this site", desc: `You turned Vani off for ${host || "this site"}. Turn it back on in Settings.` };
    case "error":
      return { title: "Can't read this page", desc: "Open a job application in a normal browser tab, then re-scan. Your saved answers are still here." };
    default:
      return { title: "No application form here", desc: "Open an application on Greenhouse, Lever, Ashby or Workday to fill it, or find roles with the scraper." };
  }
}

/** 99,999+ would crowd a badge; a huge or broken count still reads as a count. */
export function compactCount(n: unknown): string {
  if (typeof n !== "number" || !Number.isFinite(n) || n <= 0) return "0";
  const v = Math.floor(n);
  if (v < 1000) return String(v);
  if (v < 10_000) return `${(v / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  if (v < 1_000_000) return `${Math.floor(v / 1000)}k`;
  return "1m+";
}

export interface MeterModel {
  okPct: number;
  revPct: number;
  unkPct: number;
  readyPct: number;
  badge: string;
  tone: "ok" | "rev" | "muted";
  subtitle: string;
  ok: string;
  review: string;
  fresh: string;
}

const clampPct = (x: number) => (Number.isFinite(x) ? Math.max(0, Math.min(100, Math.round(x))) : 0);

/** Readiness numbers for the meter; the three bars never add past 100 whatever the counts say. */
export function meterModel(c: Pick<Coverage, "found" | "fillable" | "manual" | "unknown" | "capped">): MeterModel {
  const n = (x: number) => (Number.isFinite(x) && x > 0 ? Math.floor(x) : 0);
  const found = n(c.found), fillable = n(c.fillable), manual = n(c.manual), unknown = n(c.unknown);
  const counts = { ok: compactCount(fillable), review: compactCount(manual), fresh: compactCount(unknown) };
  if (found === 0) {
    return { okPct: 0, revPct: 0, unkPct: 0, readyPct: 0, badge: "0%", tone: "muted", subtitle: "No form fields detected on this tab", ...counts };
  }
  const okPct = clampPct((fillable / found) * 100);
  const revPct = clampPct(Math.min(manual / found * 100, 100 - okPct));
  const unkPct = clampPct(Math.min(unknown / found * 100, 100 - okPct - revPct));
  const readyPct = clampPct(((fillable + manual) / found) * 100);
  const sample = c.capped ? " (first fields only)" : "";
  return {
    okPct, revPct, unkPct, readyPct,
    badge: `${readyPct}%`,
    tone: readyPct >= 70 ? "ok" : "rev",
    subtitle: `${compactCount(fillable)} of ${compactCount(found)} fields ready to auto-fill${sample}`,
    ...counts,
  };
}

export interface Completeness {
  filled: number;
  total: number;
  pct: number;
  /** Labels of the contact basics still empty, in form order. */
  missingContact: string[];
}

function usableBank(bank: unknown): Bank | null {
  const b = bank as Partial<Bank> | null | undefined;
  return b && typeof b === "object" && b.answers && typeof b.answers === "object" && !Array.isArray(b.answers) ? (b as Bank) : null;
}

/** How much of the profile Fill can draw on. Self-ID basics count only while self-ID capture is on (basicsValues blanks them otherwise). */
export function basicsCompleteness(bank: Bank | null | undefined): Completeness {
  const b = usableBank(bank);
  const values = basicsValues(b);
  const counted = BASICS.filter(x => x.group !== "self_id" || b?.settings?.captureSelfId === true);
  const isSet = (id: string) => (values[id] ?? "").trim() !== "";
  const filled = counted.filter(x => isSet(x.id)).length;
  return {
    filled,
    total: counted.length,
    pct: counted.length === 0 ? 0 : Math.round((filled / counted.length) * 100),
    missingContact: counted.filter(x => x.group === "contact" && !isSet(x.id)).map(x => x.label),
  };
}

export interface BankSummary {
  answers: number;
  review: number;
}

export function bankSummary(bank: Bank | null | undefined): BankSummary {
  const b = usableBank(bank);
  const all = b ? Object.values(b.answers) : [];
  return { answers: all.length, review: all.filter(a => a?.review === true).length };
}

export interface NextStep {
  text: string;
  /** The tab the button opens. */
  tab: "basics" | "answers";
  button: string;
}

/** The single most useful thing to do next, or null when the profile needs nothing. */
export function nextStep(c: Completeness, s: BankSummary): NextStep | null {
  if (c.missingContact.length > 0) {
    const shown = c.missingContact.slice(0, 3).join(", ");
    const more = c.missingContact.length > 3 ? ` and ${c.missingContact.length - 3} more` : "";
    return { text: `Missing: ${shown}${more}. Fill uses them on every application.`, tab: "basics", button: "Finish basics" };
  }
  if (s.review > 0) {
    return { text: `${compactCount(s.review)} saved ${s.review === 1 ? "answer is" : "answers are"} marked for review before Vani fills them.`, tab: "answers", button: "Review answers" };
  }
  return null;
}

export interface FriendlyError {
  line: string;
  /** The raw message, trimmed and bounded, for a tooltip. */
  detail: string;
}

const MAX_DETAIL = 300;

const ERROR_RULES: ReadonlyArray<[RegExp, string]> = [
  [/reading '(query|sendMessage|executeScript)'|reading "(query|sendMessage|executeScript)"|\bchrome\.tabs\b|\bchrome is not defined/i, "Vani can't reach the current tab. Open it from the toolbar on a normal web page."],
  [/receiving end does not exist|could not establish connection/i, "Vani isn't running on this page yet. Reload the tab, then re-scan."],
  [/extension context invalidated|message port closed/i, "The extension was updated. Close this popup and open it again."],
  [/cannot access|extensions gallery|chrome:\/\/|not allowed|permission|blocked/i, "The browser doesn't let extensions read this page."],
  [/timed? ?out|timeout/i, "The page took too long to answer. Re-scan to try again."],
];

/** A short, plain line for a raw exception; the raw text stays in `detail`. Never throws. */
export function friendlyError(e: unknown, fallback = "Something went wrong reading this page."): FriendlyError {
  let raw = "";
  try {
    raw = e instanceof Error ? e.message : typeof e === "string" ? e : e && typeof e === "object" && typeof (e as { message?: unknown }).message === "string" ? (e as { message: string }).message : e == null ? "" : String(e);
  } catch {
    raw = "";
  }
  const detail = raw.trim().slice(0, MAX_DETAIL);
  const hit = ERROR_RULES.find(([re]) => re.test(detail));
  return { line: hit ? hit[1] : fallback, detail };
}
