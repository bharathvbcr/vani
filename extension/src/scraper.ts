// Live job listing scraper for any web page (LinkedIn, Indeed, Google Jobs,
// Greenhouse/Lever/Ashby boards, company careers pages, and generic job listings).
// Extracts listings and immediately computes real-time relevance scores for each.

import {
  type RelevanceResult,
  type UserTargetProfile,
  evaluateRelevance,
  extractSalaryRange,
  extractWorkplaceType,
} from "./relevance";

export interface ScrapedJob {
  id: string;
  title: string;
  company: string;
  location: string;
  url: string;
  salary?: string;
  posted?: string;
  snippet: string;
  relevance: RelevanceResult;
}

export interface ScrapeResult {
  host: string;
  pageTitle: string;
  totalFound: number;
  jobs: ScrapedJob[];
}

function clean(s: string | null | undefined): string {
  return (s ?? "").replace(/\s+/g, " ").trim();
}

function resolveUrl(href: string | null | undefined, base?: string): string {
  if (!href) return "";
  const baseHref = base ?? (typeof location !== "undefined" ? location.href : "https://example.com");
  try {
    return new URL(href, baseHref).href;
  } catch {
    return href;
  }
}

/** Generate a stable ID for a scraped listing. */
function jobId(title: string, company: string, url: string): string {
  const raw = `${title}|${company}|${url}`;
  let hash = 0;
  for (let i = 0; i < raw.length; i++) {
    hash = (hash << 5) - hash + raw.charCodeAt(i);
    hash |= 0;
  }
  return `job-${Math.abs(hash).toString(36)}`;
}

const NON_JOB_WORDS = /\b(privacy|terms|policy|cookie|cookies|culture|benefit|benefits|about|contact|login|sign in|blog|press|events|all jobs|search jobs)\b/i;

/** Check if an anchor element looks like a job posting link. */
function isJobLink(a: HTMLAnchorElement): boolean {
  const href = a.getAttribute("href")?.toLowerCase() ?? "";
  const text = a.textContent?.toLowerCase() ?? "";
  if (!href || href.startsWith("#") || href.startsWith("javascript:")) return false;
  if (NON_JOB_WORDS.test(text) || NON_JOB_WORDS.test(href)) return false;

  const pathMatches = /\b(jobs?|careers?|positions?|openings?|apply|postings?|opportunities)\b/.test(href);
  const textMatches = /\b(engineer|developer|designer|manager|specialist|lead|analyst|architect|consultant|director|scientist)\b/.test(text);

  return pathMatches && (textMatches || text.length > 5);
}

/** Scrape structured JSON-LD JobPosting data from scripts if available. */
function scrapeJsonLd(doc: Document, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const scripts = Array.from(doc.querySelectorAll('script[type="application/ld+json"]'));

  for (const s of scripts) {
    try {
      const parsed = JSON.parse(s.textContent ?? "");
      const items: Record<string, unknown>[] = [];

      if (Array.isArray(parsed)) items.push(...parsed);
      else if (parsed["@type"] === "ItemList" && Array.isArray(parsed.itemListElement)) {
        for (const it of parsed.itemListElement) {
          if (it && typeof it === "object") items.push(it.item ?? it);
        }
      } else if (parsed["@type"] === "JobPosting") {
        items.push(parsed);
      }

      for (const it of items) {
        if (!it || it["@type"] !== "JobPosting") continue;
        const title = clean(String(it.title ?? ""));
        const org = it.hiringOrganization;
        const company = clean(typeof org === "object" && org !== null ? String((org as Record<string, unknown>).name ?? "") : String(org ?? ""));
        const location = clean(typeof it.jobLocation === "object" && it.jobLocation !== null ? String((it.jobLocation as Record<string, unknown>).name ?? "") : String(it.jobLocation ?? ""));
        const url = resolveUrl(String(it.url ?? location.href));
        const snippet = clean(String(it.description ?? "")).slice(0, 300);

        if (title) {
          const relevance = evaluateRelevance({ title, company, description: snippet, location }, profile);
          jobs.push({
            id: jobId(title, company, url),
            title,
            company,
            location,
            url,
            salary: it.baseSalary ? String(it.baseSalary) : undefined,
            snippet,
            relevance,
          });
        }
      }
    } catch {}
  }
  return jobs;
}

/** Scrape listings from LinkedIn jobs results. */
function scrapeLinkedIn(doc: Document, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const cards = doc.querySelectorAll("li.jobs-search-results__list-item, .job-card-container, .base-card");

  cards.forEach(card => {
    const titleEl = card.querySelector(".job-card-list__title, .base-search-card__title, a.job-card-container__link");
    const compEl = card.querySelector(".job-card-container__company-name, .base-search-card__subtitle");
    const locEl = card.querySelector(".job-card-container__metadata-item, .job-search-card__location");
    const linkEl = (card.querySelector("a[href*='/jobs/view/'], a.base-card__full-link") ?? titleEl) as HTMLAnchorElement | null;
    const snippetEl = card.querySelector(".job-card-list__snippet, .base-search-card__metadata");

    const title = clean(titleEl?.textContent);
    const company = clean(compEl?.textContent);
    const location = clean(locEl?.textContent);
    const url = resolveUrl(linkEl?.getAttribute("href"));
    const snippet = clean(snippetEl?.textContent);

    if (title && url) {
      const relevance = evaluateRelevance({ title, company, description: snippet, location }, profile);
      jobs.push({
        id: jobId(title, company, url),
        title,
        company,
        location,
        url,
        salary: extractSalaryRange(card.textContent ?? ""),
        snippet,
        relevance,
      });
    }
  });

  return jobs;
}

/** Scrape listings from Indeed search results. */
function scrapeIndeed(doc: Document, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const cards = doc.querySelectorAll(".job_seen_beacon, td.resultContent, [data-testid='slider_item']");

  cards.forEach(card => {
    const titleEl = card.querySelector("h2.jobTitle a, a.jcs-JobTitle");
    const compEl = card.querySelector("[data-testid='company-name'], .companyName");
    const locEl = card.querySelector("[data-testid='text-location'], .companyLocation");
    const snippetEl = card.querySelector(".job-snippet, [data-testid='job-snippet']");
    const salaryEl = card.querySelector(".salary-snippet-container, [data-testid='attribute_snippet_testid']");

    const title = clean(titleEl?.textContent);
    const company = clean(compEl?.textContent);
    const location = clean(locEl?.textContent);
    const url = resolveUrl(titleEl?.getAttribute("href"));
    const snippet = clean(snippetEl?.textContent);
    const salary = clean(salaryEl?.textContent) || undefined;

    if (title && url) {
      const relevance = evaluateRelevance({ title, company, description: `${snippet} ${salary ?? ""}`, location }, profile);
      jobs.push({
        id: jobId(title, company, url),
        title,
        company,
        location,
        url,
        salary,
        snippet,
        relevance,
      });
    }
  });

  return jobs;
}

/** Scrape listings from Workday career boards. */
function scrapeWorkday(doc: Document, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const items = doc.querySelectorAll("[data-automation-id*='job'], [data-automation-id='jobResults'] li, tr[data-automation-id]");

  items.forEach(el => {
    const a = (el.querySelector("a[data-automation-id*='job'], a[href*='/job/'], a") ?? (el.localName === "a" ? el : null)) as HTMLAnchorElement | null;
    const titleEl = el.querySelector("[data-automation-id='jobTitle'], [data-automation-id='job-title'], h3, h4") ?? a;
    const title = clean(titleEl?.textContent);
    const url = resolveUrl(a?.getAttribute("href"));
    const locEl = el.querySelector("[data-automation-id='locations'], [data-automation-id='compositeSubHeaderOne'], [class*='location']");
    const location = clean(locEl?.textContent);
    const timeEl = el.querySelector("[data-automation-id='postedOn']");
    const posted = clean(timeEl?.textContent) || undefined;

    if (title && url && title.length > 3 && !/^(apply|view|learn|details|submit)/i.test(title)) {
      const company = clean(doc.querySelector("[data-automation-id='companyName'], h1")?.textContent);
      const relevance = evaluateRelevance({ title, company, description: location, location }, profile);
      jobs.push({
        id: jobId(title, company, url),
        title,
        company,
        location,
        url,
        posted,
        snippet: location,
        relevance,
      });
    }
  });

  return jobs;
}

/** Scrape listings from ATS boards (Greenhouse, Lever, Ashby, Workday). */
function scrapeATSBoards(doc: Document, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];

  // Workday
  jobs.push(...scrapeWorkday(doc, profile));

  // Greenhouse
  doc.querySelectorAll(".opening, [class*='opening'], tr.job-row").forEach(el => {
    const a = el.querySelector("a") as HTMLAnchorElement | null;
    const loc = el.querySelector(".location, [class*='location']");
    const title = clean(a?.textContent);
    const url = resolveUrl(a?.getAttribute("href"));
    const location = clean(loc?.textContent);
    if (title && url && !/^(apply|view|details)/i.test(title)) {
      jobs.push({
        id: jobId(title, "", url),
        title,
        company: clean(doc.querySelector("h1, .company-name")?.textContent),
        location,
        url,
        snippet: location,
        relevance: evaluateRelevance({ title, description: location, location }, profile),
      });
    }
  });

  // Lever
  doc.querySelectorAll(".posting, [class*='posting-item']").forEach(el => {
    const a = (el.querySelector("a.posting-title, a[href*='/']") ?? el.querySelector("a")) as HTMLAnchorElement | null;
    const title = clean(a?.querySelector("h5, [data-qa='posting-name']")?.textContent ?? a?.textContent);
    const loc = el.querySelector(".workplaceTypes, .location, .posting-categories");
    const url = resolveUrl(a?.getAttribute("href"));
    const location = clean(loc?.textContent);
    if (title && url && !/^(apply|view|details)/i.test(title)) {
      jobs.push({
        id: jobId(title, "", url),
        title,
        company: clean(doc.querySelector(".main-header-logo img, h1")?.textContent),
        location,
        url,
        snippet: location,
        relevance: evaluateRelevance({ title, description: location, location }, profile),
      });
    }
  });

  // Ashby
  doc.querySelectorAll("[class*='ashby-job-posting'], [class*='job-posting-item'], a[href*='/jobs/']").forEach(el => {
    const a = (el.localName === "a" ? el : el.querySelector("a")) as HTMLAnchorElement | null;
    const title = clean(el.querySelector("h3, h4, [class*='title']")?.textContent ?? a?.textContent);
    const loc = el.querySelector("[class*='location'], [class*='metadata']");
    const url = resolveUrl(a?.getAttribute("href"));
    const location = clean(loc?.textContent);
    if (title && url && title.length > 3 && !/^(apply|view|details)/i.test(title)) {
      jobs.push({
        id: jobId(title, "", url),
        title,
        company: clean(doc.querySelector("h1, [class*='header-title']")?.textContent),
        location,
        url,
        snippet: location,
        relevance: evaluateRelevance({ title, description: location, location }, profile),
      });
    }
  });

  return jobs;
}

/** Generic DOM scraper for any career page or job listing page. */
function scrapeGeneric(doc: Document, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const seenUrls = new Set<string>();

  // Look for links that match job URLs
  const candidateLinks = Array.from(doc.querySelectorAll<HTMLAnchorElement>("a[href]")).filter(isJobLink);

  for (const a of candidateLinks.slice(0, 80)) {
    const url = resolveUrl(a.getAttribute("href"));
    if (!url || seenUrls.has(url)) continue;
    seenUrls.add(url);

    let title = clean(a.textContent);
    const container = a.closest("li, tr, article, div[class*='job'], div[class*='card'], div[class*='item']") ?? a.parentElement;

    // If anchor text is generic ("Apply", "Apply Now", "View Job", etc.), find actual title from heading
    if (!title || title.length < 3 || /^(apply|apply now|view job|view|read more|details|learn more|see details|open position)$/i.test(title)) {
      const heading = container?.querySelector("h1, h2, h3, h4, h5, [class*='title'], [class*='name']");
      if (heading) {
        title = clean(heading.textContent);
      }
    }

    if (!title || title.length < 4 || title.length > 120 || /^(apply|apply now|view job|view|read more|details)$/i.test(title)) continue;

    const containerText = clean(container?.textContent);
    const location = extractWorkplaceType(containerText);
    const salary = extractSalaryRange(containerText);
    const snippet = containerText.slice(0, 250);

    const relevance = evaluateRelevance({ title, description: `${snippet} ${salary ?? ""}`, location }, profile);
    jobs.push({
      id: jobId(title, "", url),
      title,
      company: clean(doc.querySelector("meta[property='og:site_name']")?.getAttribute("content") ?? doc.title),
      location: location !== "Unknown" ? location : "",
      url,
      salary,
      snippet,
      relevance,
    });
  }

  return jobs;
}

/** Run live scraper on the given document, returning ranked listings. */
export function scrapePageJobs(
  doc?: Document,
  targetHost?: string,
  profile?: Partial<UserTargetProfile>,
): ScrapeResult {
  const currentDoc = doc !== undefined ? doc : (typeof document !== "undefined" ? document : null);
  const host = targetHost ?? (typeof location !== "undefined" ? location.hostname : "example.com");
  if (!currentDoc) {
    return { host, pageTitle: "", totalFound: 0, jobs: [] };
  }
  let jobs: ScrapedJob[] = [];

  // Try JSON-LD first
  const jsonLdJobs = scrapeJsonLd(currentDoc, profile);
  if (jsonLdJobs.length > 0) {
    jobs.push(...jsonLdJobs);
  }

  // Check specific platforms or DOM ATS signatures
  if (host.includes("linkedin.com")) {
    jobs.push(...scrapeLinkedIn(currentDoc, profile));
  } else if (host.includes("indeed.com")) {
    jobs.push(...scrapeIndeed(currentDoc, profile));
  } else if (
    host.includes("greenhouse.io") ||
    host.includes("lever.co") ||
    host.includes("ashbyhq.com") ||
    host.includes("myworkdayjobs.com") ||
    host.includes("workday.com") ||
    currentDoc.querySelector(".opening, .posting, [class*='ashby-job-posting'], [data-automation-id*='job']") !== null
  ) {
    jobs.push(...scrapeATSBoards(currentDoc, profile));
  }

  // If still empty or very few, try generic DOM scraper
  if (jobs.length < 2) {
    jobs.push(...scrapeGeneric(currentDoc, profile));
  }

  // Deduplicate by URL or Title+Company
  const deduped: ScrapedJob[] = [];
  const seen = new Set<string>();
  for (const j of jobs) {
    const key = `${j.title.toLowerCase()}|${j.url.toLowerCase()}`;
    if (!seen.has(key)) {
      seen.add(key);
      deduped.push(j);
    }
  }

  // Sort descending by relevance score (highest relevance first)
  deduped.sort((a, b) => b.relevance.score - a.relevance.score);

  return {
    host,
    pageTitle: currentDoc.title ?? "",
    totalFound: deduped.length,
    jobs: deduped,
  };
}

/** Export scraped jobs to CSV format. */
export function exportJobsAsCSV(jobs: ScrapedJob[]): string {
  const header = ["Title", "Company", "Relevance", "Fit", "Location", "Salary", "URL"];
  const rows = jobs.map(j => [
    `"${j.title.replace(/"/g, '""')}"`,
    `"${j.company.replace(/"/g, '""')}"`,
    `${j.relevance.score}%`,
    `"${j.relevance.fitLabel}"`,
    `"${j.location.replace(/"/g, '""')}"`,
    `"${(j.salary ?? "").replace(/"/g, '""')}"`,
    `"${j.url.replace(/"/g, '""')}"`,
  ]);
  return [header.join(","), ...rows.map(r => r.join(","))].join("\n");
}

/** Export scraped jobs to formatted JSON. */
export function exportJobsAsJSON(jobs: ScrapedJob[]): string {
  return JSON.stringify(jobs, null, 2);
}
