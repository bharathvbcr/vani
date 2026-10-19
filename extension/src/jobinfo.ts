// The posting an application form belongs to, read from the page: JSON-LD
// JobPosting first (Greenhouse, Lever, Ashby, Workday and most careers pages
// publish it), then Open Graph and the document title. Page content is
// untrusted: it is only ever sent as data to the drafting model, clipped.
import type { JobInfo } from "./draft";

const MAX_LD_BLOCKS = 20;
const MAX_LD_CHARS = 500_000;

function text(x: unknown): string {
  return typeof x === "string" ? x : "";
}

/** HTML in a JSON-LD description, flattened to text without running or loading anything. */
export function htmlToText(html: string, doc: Document): string {
  const t = doc.createElement("template"); // inert: no scripts run, no images load
  t.innerHTML = html.replace(/<\s*(br|\/p|\/li|\/h[1-6])\s*\/?>/gi, "$&\n");
  return (t.content.textContent ?? "").replace(/[ \t\f\v]+/g, " ").replace(/\n\s*\n+/g, "\n").trim();
}

function postings(node: unknown, out: Record<string, unknown>[], depth = 0): void {
  if (depth > 6 || out.length >= 10 || typeof node !== "object" || node === null) return;
  if (Array.isArray(node)) { for (const n of node) postings(n, out, depth + 1); return; }
  const o = node as Record<string, unknown>;
  const type = o["@type"];
  if (type === "JobPosting" || (Array.isArray(type) && type.includes("JobPosting"))) out.push(o);
  if (o["@graph"]) postings(o["@graph"], out, depth + 1);
}

export function readJobInfo(doc: Document): JobInfo {
  const found: Record<string, unknown>[] = [];
  const blocks = Array.from(doc.querySelectorAll('script[type="application/ld+json"]')).slice(0, MAX_LD_BLOCKS);
  for (const s of blocks) {
    const raw = s.textContent ?? "";
    if (raw.length > MAX_LD_CHARS) continue;
    try { postings(JSON.parse(raw), found); } catch { /* malformed JSON-LD is common; skip it */ }
  }
  const jp = found[0];
  const meta = (p: string) => doc.querySelector(`meta[property="${p}"], meta[name="${p}"]`)?.getAttribute("content") ?? "";
  const org = jp?.hiringOrganization;
  const company = text(typeof org === "object" && org !== null ? (org as Record<string, unknown>).name : org) || meta("og:site_name");
  const title = text(jp?.title) || meta("og:title") || doc.title;
  const description = jp ? htmlToText(text(jp.description), doc) : meta("og:description") || meta("description");
  return { company: company.trim(), title: title.trim(), description };
}
