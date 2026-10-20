// The posting an application form belongs to, read from the page: JSON-LD
// JobPosting first (Greenhouse, Lever, Ashby, Workday and most careers pages
// publish it), then Open Graph and the document title. Page content is
// untrusted: it is only ever sent as data to the drafting model, clipped.
import type { JobInfo } from "./draft";
import { MIN_JOB_DESCRIPTION } from "./home";
import { pageKey } from "./applied";

const MAX_LD_BLOCKS = 20;
const MAX_LD_CHARS = 500_000;

function text(x: unknown): string {
  return typeof x === "string" ? x : "";
}

// Tags after which a line ends, so "<b>Title:</b></p>AI Engineer" or adjacent cells do not run together.
const LINE_END = /<\s*(br|\/p|\/li|\/h[1-6]|\/div|\/tr|\/td|\/th|\/section|\/ul|\/ol|\/dd|\/dt)\s*\/?>/gi;

/**
 * Drops script, style, noscript and template elements, whose text is code or a fallback
 * and never posting prose (textContent would include it). One pass: a lazy regex over the
 * same text is quadratic on unclosed tags, which page content controls. An unclosed one
 * runs to the end, as a browser reads it.
 */
function stripNotProse(html: string): string {
  const open = /<(script|style|noscript|template)\b/gi;
  const lower = html.toLowerCase();
  let out = "", from = 0;
  for (let m = open.exec(html); m; m = open.exec(html)) {
    out += html.slice(from, m.index) + " ";
    const close = lower.indexOf(`</${m[1]!.toLowerCase()}`, open.lastIndex);
    if (close < 0) return out;
    const end = lower.indexOf(">", close);
    from = end < 0 ? html.length : end + 1;
    open.lastIndex = from;
  }
  return out + html.slice(from);
}

/** HTML in a JSON-LD description, flattened to text without running or loading anything. */
export function htmlToText(html: string, doc: Document): string {
  const t = doc.createElement("template") as HTMLTemplateElement; // inert: no scripts run, no images load
  t.innerHTML = stripNotProse(html).replace(LINE_END, "$&\n");
  const node = t.content ?? t;
  return (node.textContent ?? "").replace(/[ \t\f\v]+/g, " ").replace(/\n\s*\n+/g, "\n").trim();
}

function postings(node: unknown, out: Record<string, unknown>[], depth = 0): void {
  if (depth > 6 || out.length >= 10 || typeof node !== "object" || node === null) return;
  if (Array.isArray(node)) { for (const n of node) postings(n, out, depth + 1); return; }
  const o = node as Record<string, unknown>;
  const type = o["@type"];
  if (type === "JobPosting" || (Array.isArray(type) && type.includes("JobPosting"))) out.push(o);
  if (o["@graph"]) postings(o["@graph"], out, depth + 1);
}

function clean(s: string | null | undefined): string {
  return (s ?? "").replace(/\s+/g, " ").trim();
}

// The posting as the page renders it. On a single-page board (Workday) this is the only
// part of the document that follows in-app navigation: <head>'s JSON-LD and Open Graph
// tags keep describing the URL the document was loaded at.
// The posting page's header, or the apply flow's (Workday's /apply pages show the job title there).
const RENDERED_TITLE = '[data-automation-id="jobPostingHeader"], [data-automation-id="jobTitleHeading"]';
const RENDERED_DESCRIPTION = '[data-automation-id="jobPostingDescription"]';
// The requisition id the board shows with the posting: <dl><dt>job requisition id</dt><dd>R_00049483</dd></dl>.
const RENDERED_ID = '[data-automation-id="requisitionId"] dd';
// Description bodies of boards that render one, read when there is no JSON-LD posting
// (modern Greenhouse's og:description is only the location) and they are longer.
const DESCRIPTION_BODY = ".job__description, [class*='job__description'], .job-post-content, #content, [data-qa='job-description']";
/** Rendered text read from one element, so a huge page cannot stall a scan. */
const MAX_RENDERED_CHARS = 200_000;

// Boards whose posting is always rendered in known markup: there, <head> metadata alone
// (a board landing page's og tags) is never a posting.
const RENDERS_POSTING: ReadonlySet<string> = new Set(["workday"]);

/** Where the page was loaded and where it is now, and which board it is (portal.ts id); without them only the rendered header can show the metadata is stale. */
export interface JobReadContext {
  href?: string;
  loadedHref?: string;
  portal?: string;
}

/**
 * Whether the page moved to another posting's URL since its document loaded (an in-app
 * navigation). URLs are compared as applied.ts's pageKey reduces them (tracking
 * parameters and the fragment dropped, job-id parameters kept); a URL it refuses
 * (about:srcdoc, blob:) is compared as written. An unknown load URL is no change.
 */
export function routeChanged(href: string, loadedHref: string | undefined): boolean {
  if (!loadedHref || href === loadedHref) return false;
  return (pageKey(href) ?? href) !== (pageKey(loadedHref) ?? loadedHref);
}

const ENTITIES: Readonly<Record<string, string>> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };

/** The few HTML entities a JSON-LD title carries, decoded so "R&amp;D" compares equal to "R&D". */
function decodeEntities(s: string): string {
  return s.replace(/&(#x[0-9a-f]{1,6}|#[0-9]{1,7}|[a-z]{2,6});/gi, (m, e: string) => {
    if (e[0] !== "#") return ENTITIES[e.toLowerCase()] ?? m;
    const cp = e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10);
    return cp > 0 && cp <= 0x10ffff ? String.fromCodePoint(cp) : m;
  });
}

/** Lowercased words, with + and # kept so "C++ Developer" and "C# Developer" stay apart. */
function words(s: string): string {
  return ` ${decodeEntities(s).toLowerCase().normalize("NFKC").replace(/[^\p{L}\p{N}+#]+/gu, " ").trim()} `;
}

/** Same posting title, ignoring case, punctuation and spacing. */
function sameTitle(a: string, b: string): boolean {
  const x = words(a);
  return x.trim() !== "" && x === words(b);
}

/**
 * The tab title names this posting: one of its parts ("Staff Go Engineer | Acme") is the
 * title, or a title of two or more words appears in it ("Job Application for X at Acme").
 * A one-word title ("Engineer") inside a longer tab title is not evidence.
 */
function names(tabTitle: string, name: string): boolean {
  const n = words(name);
  if (n.trim() === "") return false;
  if (tabTitle.split(/\s+[|–—·:-]\s+/).some(part => sameTitle(part, name))) return true;
  return n.trim().includes(" ") && words(tabTitle).includes(n);
}

/** A requisition id ("R_00049483") is distinctive enough to find in a URL; a short or generic one is not. */
const POSTING_ID = /^[A-Za-z0-9][A-Za-z0-9_-]{3,63}$/;

function postingId(p: Record<string, unknown>): string {
  const id = p.identifier;
  const v = typeof id === "object" && id !== null && !Array.isArray(id) ? (id as Record<string, unknown>).value : id;
  const s = typeof v === "number" ? String(v) : text(v).trim();
  return POSTING_ID.test(s) && /\d/.test(s) ? s : "";
}

/** `id` appears in `s` as a whole token: "R1000" is not in ".../R10001". */
function hasId(s: string, id: string): boolean {
  // POSTING_ID admits only [A-Za-z0-9_-], so the id needs no escaping.
  return new RegExp(`(?<![a-z0-9])${id.toLowerCase()}(?![a-z0-9])`).test(s.toLowerCase());
}

/** Whether the posting's id appears in the URL (path or query); false without a usable id. */
function idIn(p: Record<string, unknown>, href: string): boolean {
  const id = postingId(p);
  if (!id) return false;
  try {
    const u = new URL(href);
    let s = u.pathname + u.search;
    try { s = decodeURIComponent(s); } catch { /* a malformed escape: match it as written */ }
    return hasId(s, id);
  } catch {
    return false;
  }
}

/** Normalized opening and closing of a description, to tell two postings with one title apart. */
function ends(s: string): string {
  const w = words(s);
  return `${w.slice(0, 400)}\u0000${w.slice(-400)}`;
}

function renderedText(el: Element | null, doc: Document): string {
  if (!el) return "";
  // Bounded before parsing; block boundaries become line breaks and script/style text is dropped (htmlToText).
  return htmlToText((el.innerHTML ?? "").slice(0, MAX_RENDERED_CHARS * 4), doc).slice(0, MAX_RENDERED_CHARS);
}

/** A title and enough description to rate: anything less is not a posting read from this page. */
export function isReadableJob(j: JobInfo): boolean {
  return j.title.trim() !== "" && j.description.trim().length >= MIN_JOB_DESCRIPTION;
}

/**
 * Everything readJobInfo's answer depends on that changes as a single-page board
 * navigates: the URL, the tab title, the rendered header and the size of the rendered
 * description. A read is reused only while this is unchanged.
 */
export function postingKey(doc: Document, href: string): string {
  const body = doc.querySelector(RENDERED_DESCRIPTION) ?? doc.querySelector(DESCRIPTION_BODY);
  const ld = doc.querySelectorAll('script[type="application/ld+json"]').length;
  const bodyText = body?.textContent ?? "";
  const meta = (p: string) => doc.querySelector(`meta[property="${p}"]`)?.getAttribute("content") ?? "";
  // Length and opening together: two postings' bodies rarely agree on both.
  return [
    href, doc.title ?? "", clean(doc.querySelector(RENDERED_TITLE)?.textContent), clean(doc.querySelector(RENDERED_ID)?.textContent),
    bodyText.length, bodyText.slice(0, 200), ld, meta("og:title"), meta("og:description").length,
  ].join("\n");
}

/**
 * Whether a JSON-LD posting read from <head> describes the page now shown, not just the one
 * the document loaded at. Not navigated: it does, unless a rendered header names another
 * title. Navigated: a posting whose id is only in the URL the document loaded at is left
 * behind; one with its id in this URL does, unless a rendered header names another title
 * (Workday's apply steps keep the id in every URL and may show no header); one without
 * an id here, only when, with no header, the tab title names it.
 */
export function postingIsCurrent(p: Record<string, unknown>, doc: Document, ctx: JobReadContext): boolean {
  const href = ctx.href ?? "", loadedHref = ctx.loadedHref ?? "";
  const heading = clean(doc.querySelector(RENDERED_TITLE)?.textContent);
  const t = text(p.title);
  if (!routeChanged(href, ctx.loadedHref)) return heading ? sameTitle(t, heading) : true;
  if (idIn(p, loadedHref) && !idIn(p, href)) return false;
  if (idIn(p, href)) return heading ? sameTitle(t, heading) : true;
  return !heading && names(clean(doc.title), t);
}

/** The URL this document was loaded at, from the browser's navigation timing; undefined when it cannot be read. */
export function navigationHref(): string | undefined {
  try {
    return performance.getEntriesByType("navigation")[0]?.name || undefined;
  } catch {
    return undefined;
  }
}

export function readJobInfo(doc: Document, ctx: JobReadContext = {}): JobInfo {
  const found: Record<string, unknown>[] = [];
  const blocks = Array.from(doc.querySelectorAll('script[type="application/ld+json"]')).slice(0, MAX_LD_BLOCKS);
  for (const s of blocks) {
    const raw = s.textContent ?? "";
    if (raw.length > MAX_LD_CHARS) continue;
    try { postings(JSON.parse(raw), found); } catch { /* malformed JSON-LD is common; skip it */ }
  }
  const href = ctx.href ?? "", loadedHref = ctx.loadedHref ?? "";
  const navigated = routeChanged(href, ctx.loadedHref);
  const heading = clean(doc.querySelector(RENDERED_TITLE)?.textContent);
  const renderedBody = renderedText(doc.querySelector(RENDERED_DESCRIPTION), doc);
  // A board changes the URL before it swaps the posting on screen. A posting whose id is
  // in the URL the document loaded at and not in this one is the loaded page's; while the
  // page still shows it, it has not caught up yet and there is nothing of this URL's to
  // read. The requisition id on screen says which posting is shown; without one, the
  // header and the body's opening and closing do (two postings can share a title).
  const leftBehind = navigated ? found.filter(p => idIn(p, loadedHref) && !idIn(p, href)) : [];
  const shownId = clean(doc.querySelector(RENDERED_ID)?.textContent);
  const stillShown = (p: Record<string, unknown>): boolean => {
    if (shownId) return hasId(shownId, postingId(p));
    return heading !== "" && sameTitle(text(p.title), heading) &&
      (renderedBody === "" || ends(renderedBody) === ends(htmlToText(text(p.description), doc)));
  };
  if (leftBehind.some(stillShown)) return { company: "", title: "", description: "" };

  const tabTitle = clean(doc.title);
  const rendersPosting = RENDERS_POSTING.has(ctx.portal ?? "");
  // og tags carry no id, so after an in-app navigation they are read only with no header
  // and a tab title that names them as one part of itself ("Job Application for X at Y");
  // a tab title that is nothing but the og:title is a page that updated neither (a board's
  // name in both). On a board that always renders its posting, og tags alone are a landing
  // page, not a posting.
  const jp = found.find(p => postingIsCurrent(p, doc, ctx));
  const meta = (p: string) => doc.querySelector(`meta[property="${p}"], meta[name="${p}"]`)?.getAttribute("content") ?? "";
  const ogTitle = meta("og:title");
  const metaOk = !(rendersPosting && !jp) && (
    navigated ? !heading && names(tabTitle, ogTitle) && !sameTitle(tabTitle, ogTitle)
      : heading ? sameTitle(ogTitle, heading) : true);
  const org = jp?.hiringOrganization;
  let company = text(typeof org === "object" && org !== null ? (org as Record<string, unknown>).name : org) || (metaOk ? meta("og:site_name") : "");
  const title = heading || text(jp?.title) || (metaOk ? ogTitle : "") || (navigated || rendersPosting ? "" : doc.title);
  let description = jp ? htmlToText(text(jp.description), doc) : metaOk ? meta("og:description") || meta("description") : "";

  // The rendered posting (Workday) replaces a shorter description. A generic description
  // body only stands in for og/meta text (modern Greenhouse's og:description is only the
  // location), never for a JSON-LD posting: a wrapper such as #content can hold a whole form.
  const bodies = jp ? [renderedBody] : [renderedBody, clean(doc.querySelector(DESCRIPTION_BODY)?.textContent).slice(0, MAX_RENDERED_CHARS)];
  for (const body of bodies) {
    if (body.length > description.length) description = body;
  }
  // Company fallback: Logo alt, title pattern ("... at Company"), or Remix context
  if (!company.trim()) {
    const logoAlt = doc.querySelector(".image-container img[alt], .logo img[alt], img[alt*='logo' i]")?.getAttribute("alt");
    if (logoAlt) {
      const cleaned = clean(logoAlt.replace(/\s+logo$/i, ""));
      if (cleaned && cleaned.length > 1 && !/^(company|careers?|jobs?|logo)$/i.test(cleaned)) {
        company = cleaned;
      }
    }
    if (!company.trim()) {
      // Only a tab title that names this posting ("Job Application for X at Company"), not a board's.
      const atMatch = names(tabTitle, title) ? tabTitle.match(/\bat\s+([A-Z][A-Za-z0-9\s&.,'-]+)$/) : null;
      if (atMatch && atMatch[1]) {
        company = clean(atMatch[1]);
      }
    }
    if (!company.trim()) {
      const scripts = Array.from(doc.querySelectorAll("script"));
      for (const s of scripts) {
        const raw = s.textContent ?? "";
        if (!raw.includes("__remixContext")) continue;
        const m = raw.match(/window\.__remixContext\s*=\s*(\{.+?\});/s) ?? raw.match(/__remixContext\s*=\s*(\{.+?\});/s);
        if (m) {
          try {
            const data = JSON.parse(m[1]!);
            const loaderData = data?.state?.loaderData;
            for (const k of Object.keys(loaderData ?? {})) {
              const post = loaderData[k]?.jobPost;
              if (post?.company_name) {
                company = clean(String(post.company_name));
                break;
              }
              const b = loaderData[k]?.board;
              if (b?.name) {
                company = clean(String(b.name));
                break;
              }
            }
          } catch {}
        }
      }
    }
  }

  return { company: company.trim(), title: title.trim(), description: description.trim() };
}

/** Format a job's details (title, company, URL, and full job description) as clean text for clipboard copy. */
export function formatJobDetails(job: Partial<JobInfo>, url?: string): string {
  const title = (job.title ?? "").trim();
  const company = (job.company ?? "").trim();
  const desc = (job.description ?? "").trim();
  const cleanUrl = (url ?? "").trim();

  const lines: string[] = [];
  if (title && company) {
    lines.push(`${title} at ${company}`);
  } else if (title) {
    lines.push(title);
  } else if (company) {
    lines.push(company);
  }

  if (cleanUrl) {
    lines.push(cleanUrl);
  }

  if (desc) {
    if (lines.length > 0) lines.push("");
    lines.push(desc);
  }

  return lines.join("\n");
}

