// Question normalization: different wordings of one question should land on
// one key, and near-wordings should score high in a fuzzy match.

const REQUIRED_MARKS = /\(\s*(required|optional)\s*\)|\*|†/gi;
const STOPWORDS = new Set([
  "a", "an", "the", "your", "you", "yours", "do", "does", "are", "is", "be", "of", "to", "in", "on",
  "for", "and", "or", "please", "enter", "provide", "what", "which", "if", "any", "this", "that",
  "with", "at", "by", "as", "me", "my", "we", "our", "us", "will", "would", "can", "have", "has",
  // Low-information field words: "LinkedIn profile" = "LinkedIn URL", "Phone number" = "Phone".
  "url", "link", "profile", "page", "number", "handle", "here",
]);

// Phrases that mean the same thing, folded before tokenizing.
const PHRASES: Array<[RegExp, string]> = [
  [/\b(the )?united states( of america)?\b|\bu s a\b|\busa\b|\bthe us\b|\bu s\b/g, " usa "],
  [/\be mail\b/g, " email "],
  [/\bmobile\b|\bcell( phone)?\b/g, " phone "],
];

/** Lowercase, drop required/optional markers and punctuation, collapse spaces. */
export function normalizeLabel(s: string): string {
  return s
    .normalize("NFKC")
    .replace(REQUIRED_MARKS, " ")
    .toLowerCase()
    .replace(/[’']/g, "")
    .replace(/[^\p{L}\p{N}]+/gu, " ")
    .trim()
    .replace(/\s+/g, " ")
    .slice(0, 200);
}

/** Content words of a label, for fuzzy matching. */
export function tokens(s: string): Set<string> {
  const out = new Set<string>();
  let n = normalizeLabel(s);
  for (const [re, to] of PHRASES) n = n.replace(re, to);
  for (const w of n.split(/\s+/)) {
    if (w && !STOPWORDS.has(w)) out.add(stem(w));
  }
  return out;
}

// A light plural/verb-ending stem: enough for "sponsorship"/"sponsor",
// "years"/"year", "authorized"/"authorization" to meet.
function stem(w: string): string {
  if (w.length <= 3) return w;
  for (const suf of ["ization", "ation", "ship", "ing", "ed", "es", "s"]) {
    if (w.endsWith(suf) && w.length - suf.length >= 3) return w.slice(0, -suf.length);
  }
  return w;
}

/** Jaccard similarity of two token sets; 0 when either is empty. */
export function jaccard(a: Set<string>, b: Set<string>): number {
  if (a.size === 0 || b.size === 0) return 0;
  let inter = 0;
  for (const x of a) if (b.has(x)) inter++;
  return inter / (a.size + b.size - inter);
}

/**
 * autocomplete tokens that name a stable personal field. Mapping them to a
 * canonical key lets "Given name", "First name" and "Legal first name" share
 * one answer whenever the site declares autocomplete.
 */
const AUTOCOMPLETE_KEYS: Record<string, string> = {
  "given-name": "first name",
  "family-name": "last name",
  "additional-name": "middle name",
  "name": "full name",
  "email": "email",
  "tel": "phone",
  "tel-national": "phone",
  "street-address": "street address",
  "address-line1": "street address",
  "address-line2": "address line 2",
  "address-level2": "city",
  "address-level1": "state",
  "postal-code": "postal code",
  "country": "country",
  "country-name": "country",
  "organization": "current company",
  "organization-title": "current title",
  "url": "website",
};

/** The canonical key an autocomplete attribute implies, or "". */
export function autocompleteKey(autocomplete: string): string {
  // "section-x shipping given-name" -> last token is the field name.
  const parts = autocomplete.trim().toLowerCase().split(/\s+/);
  return AUTOCOMPLETE_KEYS[parts[parts.length - 1] ?? ""] ?? "";
}

/**
 * A stable field identity worth remembering per site: ATS field names like
 * "first_name" or "urls[LinkedIn]" are stable; generated ones like
 * "question_8123456" or "answers_attributes][3]" are per job and are not.
 */
export function signature(host: string, field: { name: string; automationId: string }): string {
  const id = (field.automationId || field.name).trim();
  if (!id || id.length > 120) return "";
  if (/\d{3,}/.test(id) || /\[\d+\]/.test(id) || /^[0-9a-f-]{16,}$/i.test(id)) return "";
  return `${siteKey(host)}|${id}`;
}

/**
 * Generic second levels that country registries sell names under
 * (acme.co.uk, acme.com.au, acme.co.jp): under a two-letter country code
 * they are part of the suffix, not the company.
 */
const COUNTRY_SECOND_LEVELS = new Set(["ac", "co", "com", "edu", "go", "gob", "gov", "ltd", "mil", "ne", "net", "or", "org", "plc"]);

/**
 * The registrable part of a host: boards.greenhouse.io -> greenhouse.io,
 * jobs.acme.co.uk -> acme.co.uk. Not a full public-suffix list: it covers
 * the two-part country suffixes above, which is where taking the last two
 * labels put unrelated companies under one key ("co.uk").
 */
export function siteKey(host: string): string {
  const parts = host.toLowerCase().replace(/\.$/, "").split(".");
  const n = parts.length;
  const countrySuffix = n >= 3 && /^[a-z]{2}$/.test(parts[n - 1] ?? "") && COUNTRY_SECOND_LEVELS.has(parts[n - 2] ?? "");
  const keep = countrySuffix ? 3 : 2;
  return n <= keep ? parts.join(".") : parts.slice(-keep).join(".");
}
