// Job sites Vani does not search on its own: LinkedIn, Glassdoor and Naukri. Each one's terms forbid
// automated reading:
//   - LinkedIn User Agreement §8.2 (read 2026-11-17): no "software, devices, scripts, robots … (such as
//     crawlers, browser plugins and add-ons …) to scrape or copy the Services".
//   - Naukri's terms: no "automated means to crawl and/or scrape content".
//   - Glassdoor's terms (in excerpts; its page answered 403): no data mining, robots, spiders or
//     similar data-gathering tools.
// So Vani's part ends at opening the site's own search page in a tab, from the user's click, with
// the user's search terms filled in. The user searches and reads the results there.
// `readsPages` is false for Glassdoor and Naukri: the extension's Scrape and Crawl refuse their pages
// (scraper.ts, crawlrun.ts). LinkedIn's existing Scrape reader is kept at the owner's choice
// (2026-11-17), so LinkedIn's is true.
//
// The search URLs come from third-party descriptions of each site's own address bar (2026-11-17), not
// from documentation the sites publish. LinkedIn's `keywords` and `location` are widely described.
// Glassdoor's `sc.keyword` is too, but its plain-text location parameter is reported to be ignored,
// so no location is sent and the user picks one on the site. Naukri reads keyword and city from the
// path (`/data-engineer-jobs-in-hyderabad`), with `k` repeating the keyword.

import { DEFAULT_PROFILE, type UserTargetProfile } from "./relevance";

export type JobSite = "linkedin" | "glassdoor" | "naukri";

export interface JobSiteInfo {
  id: JobSite;
  name: string;
  /** Whether the extension's Scrape and Crawl read this site's pages. */
  readsPages: boolean;
  /** Whether the search URL carries the location. */
  takesLocation: boolean;
}

export const JOB_SITES: readonly JobSiteInfo[] = [
  { id: "linkedin", name: "LinkedIn", readsPages: true, takesLocation: true },
  { id: "glassdoor", name: "Glassdoor", readsPages: false, takesLocation: false },
  { id: "naukri", name: "Naukri", readsPages: false, takesLocation: true },
];

/** The longest search text and location a search URL carries. */
export const MAX_SEARCH_CHARS = 200;
export const MAX_LOCATION_CHARS = 100;
const MAX_SLUG_CHARS = 100;

// A site's own domain and its subdomains, never a lookalike (`notlinkedin.com`, `linkedin.com.evil.io`).
const HOSTS: Record<JobSite, RegExp> = {
  linkedin: /(^|\.)linkedin\.com$/,
  // Glassdoor runs country sites: glassdoor.com, .co.in, .co.uk, .ca, .com.au, .sg, .de, …
  glassdoor: /(^|\.)glassdoor\.(com|[a-z]{2}|co\.[a-z]{2}|com\.[a-z]{2})$/,
  naukri: /(^|\.)naukri\.com$/,
};

/** The job site a page's host belongs to, or null. */
export function jobSiteOf(host: string): JobSiteInfo | null {
  const h = host.trim().toLowerCase().replace(/\.$/, "");
  if (!h) return null;
  return JOB_SITES.find(s => HOSTS[s.id].test(h)) ?? null;
}

/** The job site on `host` whose pages the extension must not read, or null when it may. */
export function unreadSite(host: string): JobSiteInfo | null {
  const s = jobSiteOf(host);
  return s && !s.readsPages ? s : null;
}

/** Why Scrape and Crawl do nothing on an unread site's page, for the user. */
export function unreadSiteText(site: JobSiteInfo): string {
  return `Vani does not read ${site.name} pages: ${site.name}'s terms forbid automated reading. Search there yourself, and open a job's own apply page to use Vani on it.`;
}

/** Text with control characters removed and whitespace collapsed, cut to max characters. */
function tidy(s: string, max: number): string {
  return Array.from(s.replace(/[\u0000-\u001f\u007f]/g, " ").replace(/\s+/g, " ").trim()).slice(0, max).join("").trim();
}

/**
 * What the search boxes start with: the first target title the user saved (not one of the built-in
 * defaults every profile carries) and their preferred location. Both stay editable, and nothing is
 * opened until the user clicks a site.
 */
export function searchDefaults(p: UserTargetProfile): { terms: string; location: string } {
  const own = p.targetTitles.find(t => !DEFAULT_PROFILE.targetTitles.includes(t)) ?? "";
  return { terms: tidy(own, MAX_SEARCH_CHARS), location: tidy(p.preferredLocation ?? "", MAX_LOCATION_CHARS) };
}

/** A path segment of lower-case ASCII letters and digits joined by '-', or "" when none are left. */
function slug(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, MAX_SLUG_CHARS).replace(/-+$/, "");
}

/**
 * The URL of `site`'s own job search for `terms` (and `location` where the site takes one), or null
 * when the terms leave nothing to search for. Nothing here fetches it: the caller opens it in a tab.
 */
export function searchUrl(site: JobSite, terms: string, location = ""): string | null {
  const q = tidy(terms, MAX_SEARCH_CHARS);
  const loc = tidy(location, MAX_LOCATION_CHARS);
  if (!q) return null;
  switch (site) {
    case "linkedin": {
      const p = new URLSearchParams({ keywords: q });
      if (loc) p.set("location", loc);
      return `https://www.linkedin.com/jobs/search/?${p.toString()}`;
    }
    case "glassdoor":
      return `https://www.glassdoor.com/Job/jobs.htm?${new URLSearchParams({ "sc.keyword": q }).toString()}`;
    case "naukri": {
      const k = slug(q);
      if (!k) return null; // the path is the search, and it is ASCII only
      const where = slug(loc);
      return `https://www.naukri.com/${k}-jobs${where ? `-in-${where}` : ""}?${new URLSearchParams({ k: q }).toString()}`;
    }
  }
}
