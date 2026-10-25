// Live job listing scraper for any web page (LinkedIn, Indeed, Google Jobs,
// Greenhouse/Lever/Ashby boards, company careers pages, and generic job listings).
// Extracts listings and immediately computes real-time relevance scores for each.

import { clean } from "./guards";
import { cardRest, readCard, withoutGluedLabel } from "./card";
import {
  type RelevanceResult,
  type UserTargetProfile,
  evaluateRelevance,
  extractSalaryRange,
  extractWorkplaceType,
} from "./relevance";
import { isWorkdaySharedHost, postingKey } from "./applied";
import { excerpt } from "./crawler";
import { type JobReadContext, htmlToText, navigationHref, postingIsCurrent } from "./jobinfo";
import { scorePillText } from "./joblist";

const WORKDAY_JOBS_HOST = /(^|\.)myworkdayjobs\.com$/;
/** A Workday host: tenant-labelled, or the shared wd{N}.myworkdaysite.com. */
const isWorkdayHost = (host: string): boolean => WORKDAY_JOBS_HOST.test(host) || isWorkdaySharedHost(host);

function hostOf(url: string): string {
  try {
    return new URL(url).hostname.toLowerCase();
  } catch {
    return "";
  }
}

/** What is kept of a posting's text on the listing; its rating reads the whole of it. */
const SNIPPET_CHARS = 300;
/** Description text read from one posting, so a huge page cannot stall a scrape. */
const MAX_RATED_CHARS = 200_000;

export interface ScrapedJob {
  id: string;
  title: string;
  company: string;
  location: string;
  url: string;
  salary?: string;
  posted?: string;
  snippet: string;
  /**
   * The role text sent to Vani in place of `snippet`, cut to the ingest bound. Set only where the page
   * holds the whole posting (JSON-LD, a Greenhouse posting page); a list card has none. Not exported.
   */
  description?: string;
  relevance: RelevanceResult;
}

export interface ScrapeResult {
  host: string;
  /** The listing page's URL without its fragment or tracking parameters; "" when it is not http(s). */
  pageUrl: string;
  pageTitle: string;
  totalFound: number;
  jobs: ScrapedJob[];
}

/**
 * A scraped link as an absolute http(s) URL, or "" for anything else: a javascript:, data:, file: or
 * other-scheme link (page content is untrusted, and the popup turns this into an anchor and window.open),
 * or one that does not parse. The URL parser already drops leading whitespace and folds the scheme's case,
 * so the check is on the parsed protocol, never on the raw text.
 */
function resolveUrl(href: string | null | undefined, base?: string): string {
  if (!href) return "";
  const baseHref = base ?? (globalThis.location ? globalThis.location.href : "https://example.com");
  try {
    const u = new URL(href, baseHref);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : "";
  } catch {
    return "";
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

const NON_JOB_EXACT_TEXT = /^(privacy(\s+policy)?|terms(\s+of\s+(service|use))?|cookie(s|\s+policy)?|our\s+culture|life\s+at\s+[\w\s]+|benefits|about(\s+us)?|contact(\s+us)?|log\s*in|sign\s*in|blog|press|news|events|all\s+jobs|search\s+jobs|back\s+to\s+jobs?|menu|close)$/i;
const NON_JOB_HREF = /\/(privacy(-policy)?|terms(-of-(service|use))?|cookies?|about(-us)?|contact(-us)?|login|signin|blog|news|events)\/?$/i;

/** Check if an anchor element looks like a job posting link. */
function isJobLink(a: HTMLAnchorElement): boolean {
  const href = a.getAttribute("href")?.toLowerCase() ?? "";
  const text = clean(a.textContent).toLowerCase();
  if (!href || href.startsWith("#") || href.startsWith("javascript:")) return false;
  if (NON_JOB_EXACT_TEXT.test(text)) return false;
  if (NON_JOB_HREF.test(href) && !/\/(jobs?|careers?|positions?|openings?)\//i.test(href)) return false;

  const pathMatches = /\b(jobs?|careers?|positions?|openings?|apply|postings?|opportunities)\b/.test(href);
  const textMatches = /\b(engineer|developer|designer|manager|specialist|lead|analyst|architect|consultant|director|scientist|fellow|intern|counsel|officer|associate|coordinator|head|vp)\b/.test(text);

  return pathMatches && (textMatches || text.length > 5);
}

/** Intelligently extract company name from document or ATS metadata. */
function extractCompanyName(doc: Document, fallbackTitle?: string): string {
  // 1. Open Graph site_name
  const ogSite = doc.querySelector("meta[property='og:site_name']")?.getAttribute("content");
  if (ogSite && ogSite.trim()) return clean(ogSite);

  // 2. Logo image alt text (e.g. "Anthropic Logo" -> "Anthropic")
  const logoAlt = doc.querySelector(".image-container img[alt], .logo img[alt], img[alt*='logo' i]")?.getAttribute("alt");
  if (logoAlt) {
    const cleaned = clean(logoAlt.replace(/\s+logo$/i, ""));
    if (cleaned && cleaned.length > 1 && !/^(company|careers?|jobs?)$/i.test(cleaned)) {
      return cleaned;
    }
  }

  // 3. Document title pattern: "Jobs at Anthropic", "Job Application for X at Anthropic", "Anthropic Careers"
  const docTitle = doc.title ?? "";
  const atMatch = docTitle.match(/\bat\s+([A-Z][A-Za-z0-9\s&.,'-]+)$/);
  if (atMatch && atMatch[1]) {
    const candidate = clean(atMatch[1]);
    if (candidate && !/^(greenhouse|lever|ashby|workday)$/i.test(candidate)) {
      return candidate;
    }
  }
  const jobsAtMatch = docTitle.match(/^jobs\s+at\s+([^|-]+)/i) ?? docTitle.match(/^careers\s+at\s+([^|-]+)/i);
  if (jobsAtMatch && jobsAtMatch[1]) {
    return clean(jobsAtMatch[1]);
  }

  // 4. Open Graph title (only if not matching the job h1 title)
  const h1Text = clean(doc.querySelector("h1")?.textContent);
  const ogTitle = doc.querySelector("meta[property='og:title']")?.getAttribute("content");
  if (ogTitle && ogTitle.trim()) {
    const cleanedOg = clean(ogTitle);
    if (
      !/^(open roles|jobs|careers|job application)/i.test(cleanedOg) &&
      cleanedOg.toLowerCase() !== h1Text.toLowerCase()
    ) {
      return cleanedOg;
    }
  }

  // 5. Check h1 or .company-name, avoiding generic page headers
  const h1 = doc.querySelector("h1, .company-name")?.textContent;
  if (h1) {
    const cleanedH1 = clean(h1);
    if (!/^(open\s+roles|current\s+openings|openings|jobs|job\s+board|careers|we('re|\s+are)\s+hiring|available\s+positions|all\s+jobs)$/i.test(cleanedH1)) {
      return cleanedH1;
    }
  }

  return clean(fallbackTitle ?? "");
}

/** The role text of a posting the page holds whole, cut to the ingest bound; nothing when there is none. */
function roleExcerpt(full: string): { description?: string } {
  const text = full.trim();
  return text ? { description: excerpt(text).text } : {};
}

/** A JSON-LD description as text, bounded before it is parsed. A document that cannot parse markup keeps the text as written, so the posting is still listed. */
function descriptionText(raw: string, doc: Document): string {
  const bounded = raw.slice(0, MAX_RATED_CHARS * 4);
  try {
    return htmlToText(bounded, doc).slice(0, MAX_RATED_CHARS);
  } catch {
    return clean(bounded).slice(0, MAX_RATED_CHARS);
  }
}

/**
 * Scrape structured JSON-LD JobPosting data from scripts if available. On a single-page board
 * <head> keeps describing the URL the document loaded at, so a posting with no url of its own is
 * listed (under the current URL) only when it belongs to the current route (jobinfo.ts).
 */
function scrapeJsonLd(doc: Document, profile: Partial<UserTargetProfile> | undefined, ctx: JobReadContext): ScrapedJob[] {
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
        const own = typeof it.url === "string" && it.url.trim() !== "";
        if (!own && !postingIsCurrent(it, doc, ctx)) continue;
        const url = resolveUrl(own ? String(it.url) : ctx.href, ctx.href || undefined);
        const full = descriptionText(String(it.description ?? ""), doc);
        const snippet = clean(full).slice(0, SNIPPET_CHARS);

        if (title) {
          const relevance = evaluateRelevance({ title, company, description: full, location }, profile);
          jobs.push({
            id: jobId(title, company, url),
            title,
            company,
            location,
            url,
            salary: it.baseSalary ? String(it.baseSalary) : undefined,
            snippet,
            ...roleExcerpt(full),
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
/**
 * The company a Workday board's page names; empty when it does not say. The page heading is the
 * site's ("Search for Jobs"), not the company's, and boards title themselves "CAREERS AT NVIDIA"
 * (seen live) while naming the company nowhere else.
 */
export function workdayCompany(doc: Document): string {
  const meta = (p: string) => doc.querySelector?.(`meta[property="${p}"]`)?.getAttribute("content") ?? "";
  const named = doc.querySelector?.("[data-automation-id='companyName']")?.textContent ?? "";
  const careersAt = /^\s*careers\s+(?:at|with)\s+(.{1,100}?)\s*$/i.exec(meta("og:title"))?.[1] ?? "";
  return (named || meta("og:site_name") || careersAt).replace(/\s+/g, " ").trim().slice(0, 200);
}

/** The page's URL as the base for its relative links when it is on `host`; otherwise the host's root. */
function baseOn(href: string, host: string): string {
  try {
    if (new URL(href).hostname.toLowerCase() === host.toLowerCase()) return href;
  } catch { /* no usable page URL */ }
  return `https://${host}/`;
}

/** A link to one posting on this board: same host as the page, with a /job/ or /details/ segment before the posting. */
function isWorkdayPostingUrl(url: string, host: string): boolean {
  try {
    const u = new URL(url);
    return (u.protocol === "https:" || u.protocol === "http:") && u.hostname.toLowerCase() === host.toLowerCase() && /\/(job|details)\/[^/]/i.test(u.pathname);
  } catch {
    return false;
  }
}

/**
 * Scrape listings from Workday career boards: one card per jobTitle anchor. The posting page's
 * own markup (jobPostingPage, jobPostingDescription) is not a card, and neither is a link inside it.
 */
function scrapeWorkday(doc: Document, host: string, base: string, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const company = workdayCompany(doc);

  doc.querySelectorAll<HTMLAnchorElement>("a[data-automation-id='jobTitle']").forEach(a => {
    const url = resolveUrl(a.getAttribute("href"), base);
    if (!isWorkdayPostingUrl(url, host)) return;
    const card = a.closest("li, tr") ?? a.parentElement ?? a;
    const title = clean(a.textContent);
    const locEl = card.querySelector("[data-automation-id='locations'], [data-automation-id='compositeSubHeaderOne'], [class*='location']");
    const location = clean(locEl?.textContent);
    const timeEl = card.querySelector("[data-automation-id='postedOn']");
    const posted = clean(timeEl?.textContent) || undefined;

    if (title && title.length > 3 && !/^(apply|view|learn|details|submit)/i.test(title)) {
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

/** Scrape structured job listings from modern Greenhouse (job-boards.greenhouse.io) Remix state if present. */
function scrapeRemixGreenhouse(doc: Document, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const scripts = Array.from(doc.querySelectorAll("script"));

  for (const s of scripts) {
    const raw = s.textContent ?? "";
    if (!raw.includes("__remixContext") && !raw.includes("jobPosts") && !raw.includes("jobPost")) continue;

    const match = raw.match(/window\.__remixContext\s*=\s*(\{.+?\});/s) ?? raw.match(/__remixContext\s*=\s*(\{.+?\});/s);
    if (!match) continue;

    try {
      const parsed = JSON.parse(match[1]!);
      const loaderData = parsed?.state?.loaderData as Record<string, Record<string, unknown>> | undefined;
      if (!loaderData || typeof loaderData !== "object") continue;

      let company = "";
      const root = loaderData["root"] as Record<string, unknown> | undefined;
      const boardConfig = root?.boardConfiguration as Record<string, unknown> | undefined;
      if (typeof boardConfig?.name === "string") company = clean(boardConfig.name);

      for (const routeKey of Object.keys(loaderData)) {
        const routeData = loaderData[routeKey];
        if (!routeData || typeof routeData !== "object") continue;

        const board = routeData.board as Record<string, unknown> | undefined;
        if (!company && typeof board?.name === "string") company = clean(board.name);

        // Check jobPosts.data (board listings)
        const jobPostsObj = routeData.jobPosts as Record<string, unknown> | undefined;
        const postList: unknown[] = [];
        if (Array.isArray(jobPostsObj?.data)) postList.push(...jobPostsObj.data);

        // Check featuredPosts.data
        const featuredObj = routeData.featuredPosts as Record<string, unknown> | undefined;
        if (Array.isArray(featuredObj?.data)) postList.push(...featuredObj.data);

        for (const it of postList) {
          if (!it || typeof it !== "object") continue;
          const post = it as Record<string, unknown>;
          const title = clean(String(post.title ?? ""));
          const location = clean(String(post.location ?? post.job_post_location ?? ""));
          const url = resolveUrl(String(post.absolute_url ?? post.public_url ?? ""));
          const dept = post.department as Record<string, unknown> | undefined;
          const deptName = typeof dept?.name === "string" ? clean(dept.name) : "";
          const snippet = deptName ? `${deptName} • ${location}` : location;

          if (title && url) {
            const relevance = evaluateRelevance({ title, company, description: snippet, location }, profile);
            jobs.push({
              id: jobId(title, company, url),
              title,
              company,
              location,
              url,
              snippet,
              relevance,
            });
          }
        }

        // Check single jobPost
        const singlePost = routeData.jobPost as Record<string, unknown> | undefined;
        if (singlePost && typeof singlePost === "object") {
          const title = clean(String(singlePost.title ?? ""));
          const postCompany = clean(String(singlePost.company_name ?? company));
          const location = clean(String(singlePost.job_post_location ?? singlePost.location ?? ""));
          const url = resolveUrl(String(singlePost.public_url ?? (globalThis.location ? globalThis.location.href : "")));
          const rawContent = clean(String(singlePost.content ?? singlePost.introduction ?? "")).slice(0, MAX_RATED_CHARS);
          const snippet = rawContent.slice(0, SNIPPET_CHARS);

          if (title && url) {
            const relevance = evaluateRelevance({ title, company: postCompany, description: rawContent, location }, profile);
            jobs.push({
              id: jobId(title, postCompany, url),
              title,
              company: postCompany,
              location,
              url,
              snippet,
              ...roleExcerpt(rawContent),
              relevance,
            });
          }
        }
      }
    } catch {
      // Malformed or different remix context structure; ignore and fallback to DOM
    }
  }

  return jobs;
}

/** Scrape listings from ATS boards (Greenhouse, Lever, Ashby, Workday). */
function scrapeATSBoards(doc: Document, host: string, base: string, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];

  // Workday
  jobs.push(...scrapeWorkday(doc, host, base, profile));

  // Greenhouse: Support both modern job-boards.greenhouse.io (tr.job-post) and classic boards.greenhouse.io (.opening)
  const ghCompany = extractCompanyName(doc);

  // Modern Greenhouse: tr.job-post, [class*='job-post'], .job-posts--table tbody tr
  doc.querySelectorAll("tr.job-post, [class*='job-post'], .job-posts--table tbody tr").forEach(el => {
    const elTag = (el.localName ?? el.tagName ?? (el as unknown as { tag?: string }).tag ?? "").toLowerCase();
    // Avoid re-scraping wrapper containers that contain child job-post rows
    if (elTag !== "tr" && el.querySelector("tr.job-post")) return;

    const a = (el.querySelector("a[href*='/jobs/'], a[href*='/job/'], a") ?? (elTag === "a" ? el : null)) as HTMLAnchorElement | null;
    if (!a) return;
    const url = resolveUrl(a.getAttribute("href"));
    if (!url) return;

    // Title: p.body--medium, heading, or first <p> in anchor
    const titleEl = el.querySelector("p.body--medium, [class*='body--medium'], [class*='job__title'], [class*='title'], h3, h4, h5");
    const locEl = el.querySelector("p.body__secondary, [class*='body--metadata'], .location, [class*='location'], [class*='metadata']");

    let title = "";
    if (titleEl) {
      title = clean(titleEl.textContent);
    } else {
      const pElements = a.querySelectorAll("p");
      if (pElements.length >= 2) {
        title = clean(pElements[0]?.textContent);
      } else if (locEl) {
        const rawA = clean(a.textContent);
        const locText = clean(locEl.textContent);
        title = clean(rawA.replace(locText, ""));
      } else {
        title = clean(a.textContent);
      }
    }

    let location = clean(locEl?.textContent);
    if (!location) {
      const pElements = a.querySelectorAll("p");
      if (pElements.length >= 2) {
        location = clean(pElements[1]?.textContent);
      }
    }

    // Preceding department header
    const deptHeader = el.closest(".job-posts--table--department, [class*='department'], section")?.querySelector("h2, h3, h4, .section-header");
    const deptName = clean(deptHeader?.textContent);
    const snippet = deptName ? `${deptName} • ${location}` : location;

    if (title && url && !/^(apply|view|details|back\s+to\s+jobs)/i.test(title)) {
      jobs.push({
        id: jobId(title, ghCompany, url),
        title,
        company: ghCompany,
        location,
        url,
        snippet,
        relevance: evaluateRelevance({ title, company: ghCompany, description: snippet, location }, profile),
      });
    }
  });

  // Classic Greenhouse: .opening, tr.job-row
  doc.querySelectorAll(".opening, [class*='opening'], tr.job-row").forEach(el => {
    const a = el.querySelector("a") as HTMLAnchorElement | null;
    const loc = el.querySelector(".location, [class*='location']");
    const title = clean(a?.textContent);
    const url = resolveUrl(a?.getAttribute("href"));
    const location = clean(loc?.textContent);
    if (title && url && !/^(apply|view|details)/i.test(title)) {
      jobs.push({
        id: jobId(title, ghCompany, url),
        title,
        company: ghCompany,
        location,
        url,
        snippet: location,
        relevance: evaluateRelevance({ title, company: ghCompany, description: location, location }, profile),
      });
    }
  });

  // Modern Greenhouse single job posting page (e.g. /anthropic/jobs/5183044008)
  if (jobs.length === 0) {
    const singlePost = doc.querySelector("main.job-post, .job-post-container, div.job__description");
    if (singlePost) {
      const titleEl = doc.querySelector("h1.section-header, .job__title h1, h1");
      const locEl = doc.querySelector(".job__location, [class*='location']");
      const descEl = doc.querySelector(".job__description, [class*='description'], main");
      const title = clean(titleEl?.textContent);
      const location = clean(locEl?.textContent);
      const full = clean(descEl?.textContent).slice(0, MAX_RATED_CHARS);
      const snippet = full.slice(0, SNIPPET_CHARS);
      const url = resolveUrl(doc.querySelector("link[rel='canonical']")?.getAttribute("href") ?? (globalThis.location ? globalThis.location.href : ""));

      if (title && !/^(open\s+roles|jobs|careers)$/i.test(title)) {
        jobs.push({
          id: jobId(title, ghCompany, url),
          title,
          company: ghCompany,
          location,
          url,
          snippet,
          ...roleExcerpt(full),
          relevance: evaluateRelevance({ title, company: ghCompany, description: full, location }, profile),
        });
      }
    }
  }

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
function scrapeGeneric(doc: Document, base: string, profile?: Partial<UserTargetProfile>): ScrapedJob[] {
  const jobs: ScrapedJob[] = [];
  const seenUrls = new Set<string>();

  // Look for links that match job URLs
  const candidateLinks = Array.from(doc.querySelectorAll<HTMLAnchorElement>("a[href]")).filter(isJobLink);

  for (const a of candidateLinks.slice(0, 100)) {
    const url = resolveUrl(a.getAttribute("href"), base);
    if (!url || seenUrls.has(url)) continue;
    seenUrls.add(url);

    // A card that is one link around several elements (title, badge, location row) is read from its own
    // markup: a.textContent glues them into "TitleFeaturedOffice: City". A plain text link keeps its text.
    const card = a.children.length > 0 ? readCard(a) : null;
    if (card !== null && card.title === "") continue; // badges, icons or a location only: no title here
    let title = card ? card.title : withoutGluedLabel(clean(a.textContent));
    const container = a.closest("li, tr, article, div[class*='job'], div[class*='card'], div[class*='item']") ?? a.parentElement;

    // If the title is generic ("Apply", etc.), extract the actual title from a heading beside the link. A text
    // link also does so when its text is too short or too long to be a title; a card's own title is kept.
    const heading = container?.querySelector("h1, h2, h3, h4, h5, [class*='title'], [class*='name']");
    const generic = /^(apply|apply now|view job|view|read more|details|learn more|see details|open position)$/i.test(title);
    if (heading && (generic || (card === null && (!title || title.length < 3 || title.length > 100)))) {
      title = clean(heading.textContent);
    }

    if (!title || title.length < 4 || title.length > 160 || /^(apply|apply now|view job|view|read more|details)$/i.test(title)) continue;

    const containerText = clean(container?.textContent);
    const workplace = extractWorkplaceType(containerText);
    const place = card?.location ?? "";
    const salary = extractSalaryRange(containerText);
    // What the card says beyond its title; empty when it says nothing, never the title handed back.
    const rest = card ? (container ? cardRest(container, title) : "") : containerText;
    const snippet = (rest === title ? "" : rest).slice(0, 250);

    const relevance = evaluateRelevance({ title, description: `${snippet} ${salary ?? ""}`, location: place || workplace }, profile);
    jobs.push({
      id: jobId(title, "", url),
      title,
      company: extractCompanyName(doc, clean(doc.querySelector("meta[property='og:site_name']")?.getAttribute("content") ?? doc.title)),
      location: place || (workplace !== "Unknown" ? workplace : ""),
      url,
      salary,
      snippet,
      relevance,
    });
  }

  return jobs;
}

const TRACKING_PARAM = /^(utm_.*|gclid|fbclid|msclkid|ref|refid|trk|trackingid)$/i;

/** A listing page's URL without credentials, fragment and tracking parameters (search parameters stay); "" unless http(s). */
export function scrubPageUrl(href: string): string {
  let u: URL;
  try {
    u = new URL(href);
  } catch {
    return "";
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") return "";
  u.username = "";
  u.password = "";
  u.hash = "";
  for (const k of [...new Set(u.searchParams.keys())]) {
    if (TRACKING_PARAM.test(k)) u.searchParams.delete(k);
  }
  return u.href;
}

/**
 * Run live scraper on the given document, returning ranked listings. `pageUrl` is the listing page's
 * own URL (the caller passes the top frame's location.href); it defaults to this window's.
 */
export function scrapePageJobs(
  doc?: Document,
  targetHost?: string,
  profile?: Partial<UserTargetProfile>,
  pageUrl?: string,
  loadedHref?: string,
): ScrapeResult {
  const currentDoc = doc !== undefined ? doc : (typeof document !== "undefined" ? document : null);
  const host = targetHost ?? (typeof location !== "undefined" ? location.hostname : "example.com");
  const page = scrubPageUrl(pageUrl ?? (typeof location !== "undefined" ? location.href : ""));
  if (!currentDoc) {
    return { host, pageUrl: page, pageTitle: "", totalFound: 0, jobs: [] };
  }
  // Where this document is now, and where it was loaded. On the live document both are its own
  // window's (a frame is not at the top frame's URL); a fetched or parsed one is at pageUrl and,
  // unless the caller says otherwise, has not navigated.
  const live = typeof document !== "undefined" && currentDoc === document;
  const docHref = live && typeof location !== "undefined" ? location.href : (pageUrl ?? (typeof location !== "undefined" ? location.href : ""));
  const ctx: JobReadContext = { href: docHref, loadedHref: loadedHref ?? (live ? navigationHref() : undefined) };
  let jobs: ScrapedJob[] = [];

  // Try JSON-LD first
  const jsonLdJobs = scrapeJsonLd(currentDoc, profile, ctx);
  if (jsonLdJobs.length > 0) {
    jobs.push(...jsonLdJobs);
  }

  // Try Remix structured state (modern Greenhouse job-boards.greenhouse.io)
  const remixJobs = scrapeRemixGreenhouse(currentDoc, profile);
  if (remixJobs.length > 0) {
    jobs.push(...remixJobs);
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
    isWorkdaySharedHost(host) ||
    host.includes("workday.com") ||
    currentDoc.querySelector(".opening, .posting, [class*='ashby-job-posting'], [data-automation-id*='job'], tr.job-post, [class*='job-post']") !== null
  ) {
    jobs.push(...scrapeATSBoards(currentDoc, host, baseOn(docHref, host), profile));
  }

  // If still empty or very few, try generic DOM scraper
  if (jobs.length < 2) {
    jobs.push(...scrapeGeneric(currentDoc, baseOn(docHref, host), profile));
  }

  // Deduplicate by normalized URL or Title+Company
  const deduped: ScrapedJob[] = [];
  const seen = new Set<string>();
  for (const j of jobs) {
    // A Workday posting has several routes (/job/, /details/, locale-prefixed); applied.ts folds them to one key.
    const folded = isWorkdayHost(hostOf(j.url)) ? postingKey(j.url) : null;
    const key = (folded ?? (j.url ? j.url.replace(/\/$/, "") : `${j.title}|${j.company}`)).toLowerCase();
    if (!seen.has(key)) {
      seen.add(key);
      deduped.push(j);
    }
  }

  // Sort descending by relevance score (highest relevance first)
  deduped.sort((a, b) => b.relevance.score - a.relevance.score);

  return {
    host,
    pageUrl: page,
    pageTitle: currentDoc.title ?? "",
    totalFound: deduped.length,
    jobs: deduped,
  };
}

/** Escape and sanitize a CSV cell, preventing CSV Formula Injection (CWE-1236). */
function escapeCsvCell(raw: string): string {
  const s = raw.replace(/"/g, '""');
  const safe = /^[=+\-@\t\r]/.test(s) ? `'${s}` : s;
  return `"${safe}"`;
}

/** Export scraped jobs to CSV format. */
export function exportJobsAsCSV(jobs: ScrapedJob[]): string {
  const header = ["Title", "Company", "Score", "Fit", "Location", "Salary", "URL"];
  const rows = jobs.map(j => [
    escapeCsvCell(j.title),
    escapeCsvCell(j.company),
    escapeCsvCell(scorePillText(j.relevance)),
    escapeCsvCell(j.relevance.fitLabel),
    escapeCsvCell(j.location),
    escapeCsvCell(j.salary ?? ""),
    escapeCsvCell(j.url),
  ]);
  return [header.join(","), ...rows.map(r => r.join(","))].join("\n");
}

/** Export scraped jobs to formatted JSON. */
export function exportJobsAsJSON(jobs: ScrapedJob[]): string {
  return JSON.stringify(jobs.map(({ description: _description, ...rest }) => rest), null, 2);
}
