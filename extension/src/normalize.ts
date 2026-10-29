// Question normalization: different wordings of one question should land on
// one key, and near-wordings should score high in a fuzzy match.

const REQUIRED_MARKS = /\(\s*(required|optional)\s*\)|\*|†/gi;
const STOPWORDS = new Set([
  "a", "an", "the", "your", "you", "yours", "do", "does", "are", "is", "be", "of", "to", "in", "on",
  "for", "and", "or", "please", "enter", "provide", "what", "which", "if", "any", "this", "that",
  "with", "at", "by", "as", "me", "my", "we", "our", "us", "will", "would", "can", "have", "has",
  // Low-information field words: "LinkedIn profile" = "LinkedIn URL", "Phone number" = "Phone".
  // "handle" is not one: a handle is the account name ("@ada"), a different value from the URL that holds it.
  "url", "link", "profile", "page", "number", "here",
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

/**
 * A trailing international dialling code. Phone-country pickers append it to
 * the country ("United States +1", "Antigua & Barbuda (+1 268)"), and some
 * show only the code once a country is chosen. Only a "+code" counts, so
 * "Level 1" keeps its number.
 */
const DIAL_CODE = /\s*\(?\+\d{1,4}(?:[ -]\d{1,4})?\)?\s*$/;

/** The text with a trailing dialling code removed. */
export function withoutDialCode(s: string): string {
  return s.replace(DIAL_CODE, "");
}

/** Nothing but a dialling code ("+1", "(+44)"). */
export function dialCodeOnly(s: string): boolean {
  return s.includes("+") && normalizeLabel(withoutDialCode(s)) === "";
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

/** The meanings AUTOCOMPLETE_KEYS maps to: the keys a basic or an autocomplete-declared field is stored under. */
export const AUTOCOMPLETE_MEANINGS: ReadonlySet<string> = new Set(Object.values(AUTOCOMPLETE_KEYS));

const CONTACT_LEAD = "(?:your |my |primary |contact |legal )?";
const contact = (body: string) => new RegExp(`^${CONTACT_LEAD}(?:${body})$`);

/**
 * The contact basics a label names, in the keys the popup stores them under. Each pattern covers the whole
 * label, so "Sales Region", "Cell Biology Lab" and "Statement of Purpose" are not state, phone or name.
 * Matched on normalizeLabel text, never on tokens(): its phrase folding turns "cell" into "phone".
 */
const CONTACT_LABELS: ReadonlyArray<readonly [string, RegExp]> = [
  ["first name", contact("(?:first|given) ?name|forename")],
  ["last name", contact("(?:last|family) ?name|surname")],
  // A bare "Name" or "Site" is not taken as the applicant's own: a reference or emergency-contact block asks it too.
  ["full name", contact("(?:full |candidate |applicant )name")],
  ["full name", /^(?:your|my|legal) (?:full )?name$/],
  ["email", contact("(?:personal )?e ?mail(?: address)?")],
  ["phone", contact("(?:phone|telephone|tel|mobile|cell)(?: phone)?(?: number| no)?")],
  ["city", contact("(?:current |home )?city(?: or town| town)?|town")],
  ["state", contact("state(?: (?:or )?(?:province|region))?|province")],
  ["country", contact("country(?: (?:or )?region| of residence)?")],
  ["postal code", contact("(?:zip|postal|post) ?code|postcode|zip|pin ?code|zip (?:or )?postal(?: code)?|postal (?:or )?zip(?: code)?")],
  // A handle or username is the account name, not the URL: "GitHub username" is no more the "github" basic (a URL)
  // than "Twitter handle" is "Twitter URL". Answers saved under such a wording are still found by that wording.
  ["linkedin", contact("(?:linkedin|linked in)(?: (?:profile|url|link|page|profile url|profile link))?")],
  ["github", contact("(?:github|git hub)(?: (?:profile|url|link|page|account|profile url))?")],
  ["website", contact("(?:personal )?(?:website|web site)(?: (?:url|link|address))?|personal site")],
];

/** The contact basic a question is, as the key it is stored under ("email", "postal code"), or "". */
export function contactOf(label: string): string {
  const q = normalizeLabel(label);
  if (!q) return "";
  for (const [meaning, re] of CONTACT_LABELS) if (re.test(q)) return meaning;
  return "";
}

/**
 * Ids a framework numbers by render order, or that every field of a kind shares: the same string names a
 * different question on the next form, so remembering it per site would fill one question's answer into another.
 */
const UNSTABLE_ID: readonly RegExp[] = [
  /:[a-z0-9]+:/i, // React useId: ":r3:", ":R1a:"
  /^(?:react-select|react-aria|downshift|headlessui|radix|mantine|chakra)-/i,
  /^mui-\d+/i,
  /^[\d-]+$/,
  // Bare generic words, optionally numbered: "input", "textInput", "field0", "answer-2".
  /^(?:text[-_]?input|input[-_]?text|input|field|text|textbox|textfield|value|select|textarea|question|answer|control|form[-_]?control)[-_]?\d*$/i,
];

/**
 * A stable field identity worth remembering per site: ATS field names like
 * "first_name" or "urls[LinkedIn]" are stable; generated ones like
 * "question_8123456" or "answers_attributes][3]" are per job and are not, and nor
 * are ids that are a position or a generic word (UNSTABLE_ID).
 */
export function signature(host: string, field: { name: string; automationId: string }): string {
  const id = (field.automationId || field.name).trim();
  if (!id || id.length > 120) return "";
  if (/\d{3,}/.test(id) || /\[\d+\]/.test(id) || /^[0-9a-f-]{16,}$/i.test(id)) return "";
  if (UNSTABLE_ID.some(re => re.test(id))) return "";
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
