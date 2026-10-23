// The jobs-row contract, owned by Go (internal/jobview/row.go) and carried on the native host's
// jobFind reply as `job.row`. When Vani has a row for the page's posting, its relevance, priority and
// fit label are what the extension shows; relevance.ts's page-text score is only an offline estimate,
// labelled as one, in Vani's fit vocabulary. The golden replay is
// internal/jobview/testdata/rows.golden.json (test/jobrow.test.ts).

import type { RelevanceResult } from "./relevance";

/** The schema version this build reads (jobview.Version). */
export const JOB_ROW_VERSION = 1;

export interface JobRow {
  v: number;
  key: string;
  title: string;
  company: string;
  location?: string;
  status: string;
  status_label: string;
  /** 0-100, age-free. */
  relevance: number;
  relevance_basis?: string;
  /** Relevance discounted by age. */
  priority: number;
  fit?: string;
  fit_label?: string;
  similar: number;
  age_days?: number;
  fresh_src?: string;
  age_label?: string;
}

/**
 * Vani's fit vocabulary (jobview.FitLabels), for the offline estimate only: a row carries its own
 * `fit_label`. test/jobrow.test.ts holds this table to the golden file.
 */
export const FIT_LABELS: Readonly<Record<string, string>> = {
  strong_fit: "Strong fit",
  partial_fit: "Partial fit",
  adjacent: "Adjacent",
  mismatch_seniority: "Seniority mismatch",
  mismatch_domain: "Different field",
};

export type RowParse = { ok: true; row: JobRow } | { ok: false; error: string };

const MAX_TEXT = 600;

function text(x: unknown): x is string {
  return typeof x === "string" && x.length <= MAX_TEXT;
}
function num(x: unknown): x is number {
  return typeof x === "number" && Number.isFinite(x);
}

/** One row off the wire. An unknown schema version is refused by name, before any other field is read. */
export function parseJobRow(x: unknown): RowParse {
  if (typeof x !== "object" || x === null || Array.isArray(x)) return { ok: false, error: "the Vani row is not an object" };
  const o = x as Record<string, unknown>;
  if (o.v !== JOB_ROW_VERSION) {
    return { ok: false, error: `Vani's jobs row schema version ${String(o.v)} is not supported (this extension reads ${JOB_ROW_VERSION})` };
  }
  if (!text(o.key) || o.key === "" || !text(o.title) || !text(o.company) || !text(o.status) || !text(o.status_label)) {
    return { ok: false, error: "the Vani row is missing a field" };
  }
  if (!num(o.relevance) || !num(o.priority) || !num(o.similar)) return { ok: false, error: "the Vani row has a bad score" };
  for (const k of ["location", "relevance_basis", "fit", "fit_label", "fresh_src", "age_label"]) {
    if (o[k] !== undefined && !text(o[k])) return { ok: false, error: `the Vani row has a bad ${k}` };
  }
  if (o.age_days !== undefined && !num(o.age_days)) return { ok: false, error: "the Vani row has a bad age_days" };
  return { ok: true, row: o as unknown as JobRow };
}

/**
 * The row in a host jobFind reply: `row: null` when the page is not a tracked posting or this vani
 * predates the contract (the estimate stays); `error` when a row is there and cannot be trusted.
 */
export function parseFindRow(reply: unknown): { row: JobRow | null } | { error: string } {
  if (typeof reply !== "object" || reply === null) return { row: null };
  const r = reply as Record<string, unknown>;
  if (r.found !== true || typeof r.job !== "object" || r.job === null) return { row: null };
  const raw = (r.job as Record<string, unknown>).row;
  if (raw === undefined || raw === null) return { row: null };
  const p = parseJobRow(raw);
  return p.ok ? { row: p.row } : { error: p.error };
}

/** Vani's own numbers and label in place of the page-text estimate; the estimate's details (skills, salary) stay. */
export function applyVaniRow(rel: RelevanceResult, row: JobRow): RelevanceResult {
  const fit = row.fit && row.fit in FIT_LABELS ? row.fit : rel.fit;
  const label = row.fit_label || row.status_label;
  return {
    ...rel,
    source: "vani",
    vani: row,
    score: Math.max(0, Math.min(100, Math.round(row.relevance))),
    fit: fit as RelevanceResult["fit"],
    fitLabel: `${label} (priority ${Math.round(row.priority)})`,
  };
}

/** The estimate, when a Vani row exists but cannot be read: say why, rather than pass the estimate off as Vani's. */
export function withRowError(rel: RelevanceResult, error: string): RelevanceResult {
  return { ...rel, fitLabel: `${rel.fitLabel} · Vani row not shown: ${error}` };
}

/** What a score is called on screen: "relevance" from Vani, "estimate" from the page text. */
export function scoreWord(rel: Pick<RelevanceResult, "source">): string {
  return rel.source === "vani" ? "Relevance" : "Estimate";
}
