// Boilerplate-pruned job detail extraction and bounded prose helpers for scraped posting pages.
// Page content is untrusted: scripts/styles/hidden elements never execute or contribute prose, and every
// DOM walk is bounded by MAX_DETAIL_NODES and MAX_DETAIL_DEPTH.

import { clean, isRecord } from "./guards";
import { MAX_SNIPPET } from "./ingest";

export const MAX_DETAIL_NODES = 8000;
export const MAX_DETAIL_DEPTH = 40;

const MAX_LD_BLOCKS = 20;
const MAX_LD_CHARS = 500_000;
const MAX_LD_POSTINGS = 50;
const MAX_RAW_DESC_CHARS = 800_000;
const MAX_LOCATION_BYTES = 200;
const MAX_POSTED_BYTES = 64;

const ELEMENT_NODE = 1;
const TEXT_NODE = 3;

export interface DetailExtract {
  description: string;
  location?: string;
  posted?: string;
  shortened: boolean;
}

const enc = new TextEncoder();
const bytes = (s: string): number => enc.encode(s).length;

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

function normalizeProse(s: string): string {
  return s
    .replace(/[\u200b-\u200d\u2060\ufeff\u00ad]/g, "")
    .replace(/[\u00a0\u2000-\u200a\u202f\u205f\u3000]/g, " ")
    .replace(/[ \t\f\v\r]+/g, " ")
    .replace(/ *\n\s*/g, "\n")
    .trim();
}

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
  return normalizeProse(parts.join(""));
}

/** Text that says something: at least one letter or digit. */
const hasText = (s: string): boolean => /[\p{L}\p{N}]/u.test(s);

const TRACKING_PARAM = /^(utm_.*|gclid|fbclid|msclkid|ref|refid|trk|trackingid)$/i;

function normHttpUrl(raw: string, base: string): string {
  try {
    const u = new URL(raw, base);
    if (u.protocol !== "http:" && u.protocol !== "https:") return "";
    u.hash = "";
    for (const k of [...new Set(u.searchParams.keys())]) {
      if (TRACKING_PARAM.test(k)) u.searchParams.delete(k);
    }
    u.pathname = u.pathname.replace(/\/+$/, "") || "/";
    return u.href;
  } catch {
    return "";
  }
}

function isJobPostingType(type: unknown): boolean {
  return type === "JobPosting" || (Array.isArray(type) && type.includes("JobPosting"));
}

function collectJsonLdPostings(node: unknown, out: Record<string, unknown>[], depth = 0): void {
  if (depth > 6 || out.length >= MAX_LD_POSTINGS || typeof node !== "object" || node === null) return;
  if (Array.isArray(node)) {
    for (const n of node) {
      if (out.length >= MAX_LD_POSTINGS) return;
      collectJsonLdPostings(n, out, depth + 1);
    }
    return;
  }
  const o = node as Record<string, unknown>;
  if (isJobPostingType(o["@type"])) {
    out.push(o);
    if (out.length >= MAX_LD_POSTINGS) return;
  }
  if (o["@type"] === "ItemList" && Array.isArray(o.itemListElement)) {
    for (const it of o.itemListElement) {
      if (out.length >= MAX_LD_POSTINGS) return;
      if (isRecord(it)) collectJsonLdPostings(it.item ?? it, out, depth + 1);
    }
  }
  if (o["@graph"]) collectJsonLdPostings(o["@graph"], out, depth + 1);
}

function formatSinglePlace(loc: unknown): string {
  if (typeof loc === "string") return clean(loc);
  if (!isRecord(loc)) return "";
  if (typeof loc.name === "string" && clean(loc.name)) return clean(loc.name);
  const addr = loc.address;
  if (typeof addr === "string") return clean(addr);
  if (isRecord(addr)) {
    const parts: string[] = [];
    for (const k of ["addressLocality", "addressRegion", "addressCountry"]) {
      const v = typeof addr[k] === "string" ? clean(addr[k]) : "";
      if (v && !parts.includes(v)) parts.push(v);
    }
    return parts.join(", ");
  }
  return "";
}

function formatJsonLdLocation(jobLocation: unknown, jobLocationType?: unknown): string {
  const rawList = Array.isArray(jobLocation) ? jobLocation : [jobLocation];
  const places: string[] = [];
  for (const item of rawList.slice(0, 200)) {
    const formatted = formatSinglePlace(item);
    if (formatted && !places.includes(formatted)) places.push(formatted);
  }
  let location = places.join("; ");
  if (bytes(location) > MAX_LOCATION_BYTES) {
    location = places.length > 1 ? `${places[0]} (+${places.length - 1} more)` : (places[0] ?? "");
  }
  if (bytes(location) > MAX_LOCATION_BYTES) location = clipBytes(location, MAX_LOCATION_BYTES);
  if (location) return location;
  if (typeof jobLocationType === "string" && jobLocationType.trim().toUpperCase() === "TELECOMMUTE") {
    return "Remote";
  }
  return "";
}

function postingUrlCandidate(p: Record<string, unknown>): string {
  if (typeof p.url === "string" && p.url.trim()) return p.url.trim();
  const me = p.mainEntityOfPage;
  if (typeof me === "string" && me.trim()) return me.trim();
  if (isRecord(me)) {
    if (typeof me["@id"] === "string" && me["@id"].trim()) return me["@id"].trim();
    if (typeof me.url === "string" && me.url.trim()) return me.url.trim();
  }
  return "";
}

function extractJsonLdDetail(doc: Document, pageUrl: string): DetailExtract | null {
  const scripts = doc.querySelectorAll ? Array.from(doc.querySelectorAll('script[type="application/ld+json"]')).slice(0, MAX_LD_BLOCKS) : [];
  if (scripts.length === 0) return null;

  const postings: Record<string, unknown>[] = [];
  for (const s of scripts) {
    const raw = s.textContent ?? "";
    if (!raw || raw.length > MAX_LD_CHARS) continue;
    try {
      collectJsonLdPostings(JSON.parse(raw), postings);
    } catch {
      // Malformed JSON-LD is common on career pages; skip it.
    }
  }

  const withDesc: Array<{ posting: Record<string, unknown>; text: string }> = [];
  for (const p of postings) {
    if (typeof p.description !== "string" || !p.description.trim()) continue;
    const text = plainText(p.description.slice(0, MAX_RAW_DESC_CHARS));
    if (hasText(text)) withDesc.push({ posting: p, text });
  }
  if (withDesc.length === 0) return null;

  const target = normHttpUrl(pageUrl, pageUrl);
  const exact = target
    ? withDesc.find(({ posting }) => {
        const u = postingUrlCandidate(posting);
        return u !== "" && normHttpUrl(u, pageUrl) === target;
      })
    : undefined;
  const chosen = exact ?? (withDesc.length === 1 ? withDesc[0] : undefined);
  if (!chosen) return null;

  const ex = excerpt(chosen.text, MAX_SNIPPET);
  const location = formatJsonLdLocation(chosen.posting.jobLocation, chosen.posting.jobLocationType);
  const rawPosted = typeof chosen.posting.datePosted === "string" ? clean(chosen.posting.datePosted) : "";
  const posted = rawPosted && bytes(rawPosted) <= MAX_POSTED_BYTES ? rawPosted : undefined;

  return {
    description: ex.text,
    ...(location ? { location } : {}),
    ...(posted ? { posted } : {}),
    shortened: ex.shortened,
  };
}

const ALWAYS_SKIP_TAGS = new Set(["script", "style", "noscript", "template", "svg", "iframe"]);
const CHROME_TAGS = new Set(["nav", "footer", "aside", "form", "dialog"]);
const SKIP_ROLES = new Set(["navigation", "banner", "contentinfo", "complementary", "dialog", "alertdialog", "search"]);
const BOILERPLATE_ATTR = /(?:cookie|consent|gdpr|onetrust|cookiebot|banner|breadcrumb|sidebar|side-bar|modal)/i;
const CONTENT_ATTR = /(?:job|posting|description|content|role|career)/i;
const INLINE_NO_PRUNE = new Set(["a", "span", "em", "strong", "b", "i", "u", "mark", "small", "abbr", "code", "sub", "sup", "cite", "q", "s", "wbr", "label", "time", "h1", "h2", "h3", "h4", "h5", "h6"]);
const TABLE_CELL_TAGS = new Set(["td", "th"]);

function tagOf(e: Element): string {
  return String((e as { localName?: string }).localName ?? e.tagName ?? (e as { tag?: string }).tag ?? "").toLowerCase();
}

function attrLower(e: Element, name: string): string {
  return typeof e.getAttribute === "function" ? (e.getAttribute(name) ?? "").toLowerCase() : "";
}

function isHiddenElement(e: Element): boolean {
  if (typeof e.getAttribute === "function" && e.getAttribute("hidden") !== null) return true;
  if (attrLower(e, "aria-hidden") === "true") return true;
  const style = attrLower(e, "style");
  return style !== "" && /\bdisplay\s*:\s*none\b|\bvisibility\s*:\s*hidden\b/i.test(style);
}

function isBoilerplateElement(e: Element, inSection: boolean): boolean {
  const tag = tagOf(e);
  if (ALWAYS_SKIP_TAGS.has(tag) || CHROME_TAGS.has(tag)) return true;
  if (tag === "header" && !inSection) return true;
  if (isHiddenElement(e)) return true;
  const role = attrLower(e, "role");
  if (role && role.split(/\s+/).some(r => SKIP_ROLES.has(r))) return true;
  const clsId = `${attrLower(e, "class")} ${attrLower(e, "id")}`;
  return clsId.trim() !== "" && BOILERPLATE_ATTR.test(clsId) && !CONTENT_ATTR.test(clsId);
}

function wordCount(s: string): number {
  let count = 0;
  for (const tok of s.trim().split(/\s+/)) {
    if (tok && hasText(tok)) count++;
  }
  return count;
}

interface Budget { left: number }

interface WalkRes {
  text: string;
  totalBytes: number;
  anchorBytes: number;
  nonAnchorWords: number;
}

const EMPTY_WALK: WalkRes = { text: "", totalBytes: 0, anchorBytes: 0, nonAnchorWords: 0 };

interface ChildSource {
  nodes: Node[];
  elementOnly: boolean;
}

function childList(e: Element): ChildSource {
  const cn = e.childNodes;
  if (cn && cn.length > 0) return { nodes: Array.from(cn), elementOnly: false };
  const ch = e.children;
  if (ch && ch.length > 0) return { nodes: Array.from(ch), elementOnly: true };
  return { nodes: [], elementOnly: false };
}

function leafTextRes(raw: string, inAnchor: boolean): WalkRes {
  const trimmed = clean(raw);
  if (!trimmed) return { text: /\s/.test(raw) ? " " : "", totalBytes: 0, anchorBytes: 0, nonAnchorWords: 0 };
  const b = bytes(trimmed);
  return {
    text: raw,
    totalBytes: b,
    anchorBytes: inAnchor ? b : 0,
    nonAnchorWords: inAnchor ? 0 : wordCount(trimmed),
  };
}

function wrapElementText(tag: string, inner: string): string {
  if (!inner) return "";
  if (BLOCK_TAG.test(tag)) return `\n${inner}\n`;
  if (TABLE_CELL_TAGS.has(tag)) return ` ${inner} `;
  return inner;
}

function walkPruned(node: Node, budget: Budget, depth: number, inAnchor: boolean, inSection: boolean): WalkRes {
  if (depth > MAX_DETAIL_DEPTH || --budget.left < 0) return EMPTY_WALK;
  const nt = node.nodeType;
  if (nt === TEXT_NODE) return leafTextRes(node.textContent ?? "", inAnchor);
  if (nt !== ELEMENT_NODE && nt !== undefined) return EMPTY_WALK;

  const e = node as Element;
  const tag = tagOf(e);
  if (depth > 0 && isBoilerplateElement(e, inSection)) return EMPTY_WALK;
  if (depth === 0 && ALWAYS_SKIP_TAGS.has(tag)) return EMPTY_WALK;
  if (tag === "br") return { text: "\n", totalBytes: 0, anchorBytes: 0, nonAnchorWords: 0 };

  const childAnchor = inAnchor || tag === "a";
  const childSection = inSection || tag === "main" || tag === "article";
  const { nodes, elementOnly } = childList(e);

  let totalBytes = 0;
  let anchorBytes = 0;
  let nonAnchorWords = 0;
  const parts: string[] = [];

  if (nodes.length > 0) {
    for (const c of nodes) {
      if (budget.left < 0) break;
      const r = walkPruned(c, budget, depth + 1, childAnchor, childSection);
      if (r.text) {
        if (elementOnly && parts.length > 0) parts.push(" ");
        parts.push(r.text);
      }
      totalBytes += r.totalBytes;
      anchorBytes += r.anchorBytes;
      nonAnchorWords += r.nonAnchorWords;
    }
  } else {
    const raw = e.textContent ?? "";
    if (raw) {
      const r = leafTextRes(raw, childAnchor);
      if (r.text) parts.push(r.text);
      totalBytes += r.totalBytes;
      anchorBytes += r.anchorBytes;
      nonAnchorWords += r.nonAnchorWords;
    }
  }

  // Link-dense nav containers: >= 70% anchor text and < 12 non-anchor words.
  if (
    depth > 0 &&
    !inAnchor &&
    !INLINE_NO_PRUNE.has(tag) &&
    totalBytes > 0 &&
    anchorBytes * 100 >= totalBytes * 70 &&
    nonAnchorWords < 12
  ) {
    return EMPTY_WALK;
  }

  return {
    text: wrapElementText(tag, parts.join("")),
    totalBytes,
    anchorBytes,
    nonAnchorWords,
  };
}

function walkRaw(node: Node, budget: Budget, depth: number): string {
  if (depth > MAX_DETAIL_DEPTH || --budget.left < 0) return "";
  const nt = node.nodeType;
  if (nt === TEXT_NODE) return node.textContent ?? "";
  if (nt !== ELEMENT_NODE && nt !== undefined) return "";

  const e = node as Element;
  const tag = tagOf(e);
  if (ALWAYS_SKIP_TAGS.has(tag)) return "";
  if (tag === "br") return "\n";

  const { nodes, elementOnly } = childList(e);
  if (nodes.length === 0) return wrapElementText(tag, e.textContent ?? "");

  const parts: string[] = [];
  for (const c of nodes) {
    if (budget.left < 0) break;
    const t = walkRaw(c, budget, depth + 1);
    if (t) {
      if (elementOnly && parts.length > 0) parts.push(" ");
      parts.push(t);
    }
  }
  return wrapElementText(tag, parts.join(""));
}

function pruneContainer(el: Element, inSection: boolean): string {
  const res = walkPruned(el as unknown as Node, { left: MAX_DETAIL_NODES }, 0, false, inSection);
  return normalizeProse(res.text);
}

function rawContainerText(el: Element): string {
  return normalizeProse(walkRaw(el as unknown as Node, { left: MAX_DETAIL_NODES }, 0));
}

function pickArticle(articles: Element[]): Element | null {
  if (articles.length === 0) return null;
  if (articles.length === 1) return articles[0]!;
  let best: Element | null = null;
  let bestLen = -1;
  const budget: Budget = { left: MAX_DETAIL_NODES };
  for (const a of articles.slice(0, 10)) {
    if (budget.left <= 0) break;
    const len = bytes(normalizeProse(walkPruned(a as unknown as Node, budget, 0, false, true).text));
    if (len > bestLen) {
      bestLen = len;
      best = a;
    }
  }
  return best;
}

function extractDomDetail(doc: Document): DetailExtract {
  const bodyEl =
    doc.body ??
    doc.querySelector?.("body") ??
    doc.documentElement ??
    (doc as unknown as Element);

  const candidates: Array<{ el: Element; inSection: boolean; isBody: boolean }> = [];
  const addCandidate = (el: Element | null | undefined, inSection: boolean, isBody: boolean) => {
    if (el && !candidates.some(c => c.el === el)) candidates.push({ el, inSection, isBody });
  };

  addCandidate(doc.querySelector?.("main"), true, false);
  addCandidate(doc.querySelector?.('[role="main"]'), true, false);
  const articles = doc.querySelectorAll ? Array.from(doc.querySelectorAll("article")) : [];
  addCandidate(pickArticle(articles), true, false);
  addCandidate(doc.querySelector?.('[class*="job-description"]'), true, false);
  addCandidate(doc.querySelector?.('[class*="posting-description"]'), true, false);
  addCandidate(bodyEl, false, true);

  if (candidates.length === 0) return { description: "", shortened: false };

  const rawBody = bodyEl ? rawContainerText(bodyEl) : "";
  let chosen = "";

  // First pass: prefer the first candidate whose pruned text passes the >65% retention check.
  for (const c of candidates) {
    if (c.isBody) continue;
    const pruned = pruneContainer(c.el, c.inSection);
    const raw = rawContainerText(c.el);
    if (bytes(pruned) < 40 && bytes(raw) < 40) continue;
    if (bytes(pruned) >= 20 && (bytes(raw) < 100 || bytes(pruned) * 100 >= bytes(raw) * 35)) {
      chosen = pruned;
      break;
    }
  }

  // Second pass: fall back to body pruning, applying the >65% text-loss safety fallback to raw body text.
  if (!chosen) {
    const prunedBody = bodyEl ? pruneContainer(bodyEl, false) : "";
    if (bytes(rawBody) >= 100 && bytes(prunedBody) * 100 < bytes(rawBody) * 35) {
      chosen = rawBody;
    } else if (prunedBody) {
      chosen = prunedBody;
    } else {
      // Tiny specific container (< 80 bytes) with empty body fallback.
      for (const c of candidates) {
        const raw = c.isBody ? rawBody : rawContainerText(c.el);
        if (raw) { chosen = raw; break; }
      }
    }
  }

  if (!hasText(chosen)) return { description: "", shortened: false };

  const ex = excerpt(chosen, MAX_SNIPPET);
  return { description: ex.text, shortened: ex.shortened };
}

/**
 * Extract a posting's description (and optional location / posted date) from a fetched detail document.
 * Prefers a matching JSON-LD JobPosting, falling back to boilerplate-pruned main DOM prose.
 */
export function extractDetail(doc: Document, pageUrl: string): DetailExtract {
  if (!doc) return { description: "", shortened: false };
  const fromLd = extractJsonLdDetail(doc, pageUrl);
  if (fromLd) return fromLd;
  return extractDomDetail(doc);
}
