// Reading one job card on a listing page: the title, the place and the rest of the card's text, from
// the card's own markup rather than from the whole card's concatenated text.
//
// Many careers pages make the whole card one <a>: a title, a "Featured" badge and a location row sit
// inside it as sibling elements with no whitespace between them. `a.textContent` then reads
// "Academy LeaderFeaturedOffice: New York". The title is the card's first run of text, the badge and the
// location are left out of it, and the location is read from its own element.
//
// Page content is untrusted: only text is read, nothing is executed, and every walk is bounded by a
// node budget and a depth bound, so a huge or deeply nested card cannot stall a scrape.

import { clean } from "./guards";

const ELEMENT_NODE = 1;
const TEXT_NODE = 3;
/** Most nodes each pass of a card read visits (finding elements, reading the title, reading the place). */
export const MAX_CARD_NODES = 600;
const MAX_DEPTH = 24;
/** Most bytes a location may be: ingest.ts refuses a longer one, and a refused job is lost whole. */
const MAX_LOCATION_BYTES = 200;
/** Most text a card's remainder keeps; callers cut it further. */
const MAX_REST_CHARS = 2000;

/** Elements whose content is never part of a card's text. */
const SKIPPED = new Set([
  "script", "style", "noscript", "template", "svg", "button", "img", "picture", "select", "option",
  "iframe", "canvas", "video", "audio", "input", "textarea",
]);
/** Elements that flow inside a line of text, so their text joins their neighbours' without a break. */
const PHRASING = new Set(["span", "em", "strong", "b", "i", "u", "mark", "small", "abbr", "code", "sub", "sup", "cite", "q", "s", "wbr"]);

export interface Card {
  /** The card's own title; "" when the card holds no text outside its badges and location. */
  title: string;
  /** The places the card lists, "; "-joined, without a leading "Office:"-style label; "" when it has none. */
  location: string;
}

interface Budget { left: number }

function attr(n: Element, name: string): string {
  return (n.getAttribute(name) ?? "").toLowerCase();
}

/** A status chip ("Featured", "New"): marked as one by `data-variant` or by a badge/chip/pill class word. */
function isBadge(n: Element): boolean {
  if (/^(status|badge|chip|pill)$/.test(attr(n, "data-variant"))) return true;
  const cls = attr(n, "class");
  return /(^|[\s_-])(badge|chip|pill)($|[\s_-])/.test(cls) && !/title|name/.test(cls);
}

/** The element a card keeps its place in. */
function isLocation(n: Element): boolean {
  return attr(n, "data-testid").includes("location") ||
    attr(n, "itemprop") === "joblocation" ||
    /(^|[\s_-])locations?($|[\s_-])/.test(attr(n, "class"));
}

/** An element a card read steps over: not text, a badge, or the location row. */
function isLeftOut(n: Element): boolean {
  return SKIPPED.has(n.localName) || isBadge(n) || isLocation(n);
}

function kids(n: Node): Node[] {
  return Array.from(n.childNodes ?? []);
}

/**
 * The text of `root` when every element under it flows inline, else null. Left-out elements are
 * skipped; a block element (div, p, br, a list) makes the text more than one run, which the caller reads
 * run by run.
 */
function phrasingText(root: Node, budget: Budget, depth = 0): string | null {
  if (depth > MAX_DEPTH) return null;
  let out = "";
  for (const c of kids(root)) {
    if (--budget.left < 0) return null;
    if (c.nodeType === TEXT_NODE) {
      out += c.textContent ?? "";
    } else if (c.nodeType === ELEMENT_NODE) {
      const e = c as Element;
      if (isLeftOut(e)) continue;
      if (!PHRASING.has(e.localName)) return null;
      const t = phrasingText(e, budget, depth + 1);
      if (t === null) return null;
      out += t;
    }
  }
  return out;
}

/** The first run of text under `root`: the whole text when it flows inline, else the first block's. */
function leadText(root: Node, budget: Budget, depth = 0): string {
  const inline = phrasingText(root, budget);
  if (inline !== null) return clean(inline);
  if (depth > MAX_DEPTH) return "";
  for (const c of kids(root)) {
    if (--budget.left < 0) return "";
    if (c.nodeType === TEXT_NODE) {
      const t = clean(c.textContent);
      if (t) return t;
    } else if (c.nodeType === ELEMENT_NODE && !isLeftOut(c as Element)) {
      const t = leadText(c, budget, depth + 1);
      if (t) return t;
    }
  }
  return "";
}

/** Every element under `root` in document order that is not inside a left-out element, up to the budget. */
function descendants(root: Node, budget: Budget, depth = 0): Element[] {
  const out: Element[] = [];
  if (depth > MAX_DEPTH) return out;
  for (const c of kids(root)) {
    if (--budget.left < 0) break;
    if (c.nodeType !== ELEMENT_NODE) continue;
    const e = c as Element;
    if (SKIPPED.has(e.localName) || isBadge(e)) continue;
    out.push(e);
    if (!isLocation(e)) out.push(...descendants(e, budget, depth + 1));
  }
  return out;
}

const LOCATION_LABEL = /^(?:offices?|locations?|workplace|work location)\s*:\s*/i;

/** A location row's text as places: the label dropped and "|"-separated places joined as "; ". */
function placesOf(text: string): string {
  const places = clean(text).replace(LOCATION_LABEL, "").split("|").map(clean).filter(Boolean);
  const joined = places.join("; ");
  return new TextEncoder().encode(joined).length > MAX_LOCATION_BYTES ? "" : joined;
}

/** The text of a left-in-place subtree, for the location row: every text node, badges and icons left out. */
function rowText(root: Node, budget: Budget, depth = 0): string {
  if (depth > MAX_DEPTH) return "";
  let out = "";
  for (const c of kids(root)) {
    if (--budget.left < 0) break;
    if (c.nodeType === TEXT_NODE) out += c.textContent ?? "";
    else if (c.nodeType === ELEMENT_NODE) {
      const e = c as Element;
      if (SKIPPED.has(e.localName) || isBadge(e)) continue;
      out += rowText(e, budget, depth + 1);
    }
  }
  return out;
}

/**
 * The title and location of the card whose link is `a`. The title is the text of the first headline
 * element (h1-h6, a "title" class) that has any, else the card's first run of text; badges, buttons and
 * the location row never contribute. Text that sits in a location row is returned as `location`.
 */
export function readCard(a: Element): Card {
  // Each pass has its own budget, so a long card cannot starve the pass that reads the title.
  const inside = descendants(a, { left: MAX_CARD_NODES });
  const reading: Budget = { left: MAX_CARD_NODES };
  let title = "";
  for (const h of inside) {
    if (!isHeadline(h)) continue;
    title = leadText(h, reading);
    if (title) break;
  }
  if (!title) title = leadText(a, reading);

  let location = "";
  const row: Budget = { left: MAX_CARD_NODES };
  for (const e of inside) {
    if (!isLocation(e)) continue;
    location = placesOf(rowText(e, row));
    if (location) break;
  }
  return { title, location };
}

/** A heading element, or one that names itself a title (the selectors scrapeGeneric has always used). */
function isHeadline(e: Element): boolean {
  return /^h[1-6]$/.test(e.localName) || attr(e, "itemprop") === "title" || attr(e, "class").includes("title");
}

/**
 * The text of a card's container once its badges and location row are left out, each text run on its
 * own, with the first occurrence of `title` removed: what the card says beyond its title. Empty when it
 * says nothing more, so a card never hands its title back as its own description.
 */
export function cardRest(container: Element, title: string): string {
  const budget: Budget = { left: MAX_CARD_NODES };
  const runs: string[] = [];
  const walk = (n: Node, depth: number): void => {
    if (depth > MAX_DEPTH) return;
    for (const c of kids(n)) {
      if (--budget.left < 0) return;
      if (c.nodeType === TEXT_NODE) {
        const t = clean(c.textContent);
        if (t) runs.push(t);
      } else if (c.nodeType === ELEMENT_NODE && !isLeftOut(c as Element)) {
        walk(c, depth + 1);
      }
    }
  };
  walk(container, 0);
  let text = clean(runs.join(" "));
  const at = title ? text.indexOf(title) : -1;
  if (at >= 0) text = clean(text.slice(0, at) + " " + text.slice(at + title.length));
  return text.slice(0, MAX_REST_CHARS);
}

/**
 * A title whose markup was lost: location text glued on behind it ("Lead AI EngineerFeaturedOffice:
 * Austin"). The label is cut only where it follows the title with no space, as glued text does, so a
 * title that says "Front Office: Manager" is left alone.
 */
export function withoutGluedLabel(title: string): string {
  const cut = title.replace(/(?<=\S)(?:Featured)?(?:Office|Offices|Location|Locations|Workplace)\s*:.*$/, "");
  return cut.length >= 3 ? clean(cut) : title;
}
