// Application portal & job board detection and adaptive features.
// Identifies ATS vendors (Greenhouse, Lever, Workday, Ashby, Taleo, etc.)
// and job boards (LinkedIn, Indeed, etc.), providing portal-tailored guidance,
// step detection, and form interaction tips.

export type PortalId =
  | "greenhouse"
  | "lever"
  | "workday"
  | "ashby"
  | "taleo"
  | "smartrecruiters"
  | "icims"
  | "bamboohr"
  | "rippling"
  | "linkedin"
  | "indeed"
  | "generic_ats"
  | "job_board"
  | "general";

export interface PortalInfo {
  id: PortalId;
  name: string;
  badge: string;
  icon: string;
  isATS: boolean;
  isJobBoard: boolean;
  isMultiStep: boolean;
  stepInfo?: { currentStep: number; totalSteps: number; stepName?: string };
  tips: string[];
}

export interface QueryableDoc {
  querySelector(sel: string): unknown;
  querySelectorAll(sel: string): ArrayLike<unknown>;
}

export function detectPortal(
  doc?: QueryableDoc | null,
  href?: string,
): PortalInfo {
  const currentHref = href ?? (typeof location !== "undefined" ? location.href : "https://example.com");
  const currentDoc = doc !== undefined ? doc : (typeof document !== "undefined" ? document : null);
  const url = new URL(currentHref, "https://example.com");
  const host = url.hostname.toLowerCase();
  const path = url.pathname.toLowerCase();

  const q = (sel: string): unknown => (currentDoc?.querySelector ? currentDoc.querySelector(sel) : null);
  const qa = (sel: string): unknown[] => (currentDoc?.querySelectorAll ? Array.from(currentDoc.querySelectorAll(sel)) : []);

  // 1. Workday
  if (host.includes("myworkdayjobs.com") || host.includes("workday.com")) {
    let stepInfo: PortalInfo["stepInfo"] | undefined;
    // Workday multi-step indicator
    const stepEls = qa("[data-automation-id*='progressStep'], [data-automation-id*='stepItem']") as Array<{
      getAttribute(name: string): string | null;
      classList?: { contains(c: string): boolean };
      textContent?: string | null;
    }>;
    if (stepEls.length > 1) {
      const activeIdx = stepEls.findIndex(el => el.getAttribute("aria-current") === "step" || el.classList?.contains("active") || el.getAttribute("data-automation-id")?.includes("active"));
      stepInfo = {
        currentStep: activeIdx >= 0 ? activeIdx + 1 : 1,
        totalSteps: stepEls.length,
        stepName: stepEls[activeIdx >= 0 ? activeIdx : 0]?.textContent?.trim() || "Application Step",
      };
    }
    return {
      id: "workday",
      name: "Workday ATS",
      badge: "Workday",
      icon: "🏢",
      isATS: true,
      isJobBoard: path.includes("/search") || path.includes("/jobs"),
      isMultiStep: true,
      stepInfo,
      tips: [
        "Workday forms are multi-step: Vani adapts and re-checks every page as you click Next.",
        "Check each section accordion before clicking Continue.",
        "Resume and Experience fields often populate automatically on later steps.",
      ],
    };
  }

  // 2. Greenhouse
  if (
    host.includes("greenhouse.io") ||
    q("form#application_form, div#app_body, [data-qa='greenhouse-job-board']") !== null ||
    (qa("script") as Array<{ src?: string }>).some(s => s.src?.includes("greenhouse.io"))
  ) {
    return {
      id: "greenhouse",
      name: "Greenhouse",
      badge: "Greenhouse",
      icon: "🌿",
      isATS: true,
      isJobBoard: !q("form#application_form, [data-qa='greenhouse-job-board'], input[type=file]"),
      isMultiStep: false,
      tips: [
        "Single-page form: Fill populates contact, questions, and custom dropdowns at once.",
        "EEOC / Self-identification choices are mapped seamlessly to Greenhouse options.",
      ],
    };
  }

  // 3. Lever
  if (
    host.includes("lever.co") ||
    q(".application-form, .lever-job, [data-qa='lever-application']") !== null ||
    (qa("script") as Array<{ src?: string }>).some(s => s.src?.includes("lever.co"))
  ) {
    return {
      id: "lever",
      name: "Lever",
      badge: "Lever",
      icon: "💼",
      isATS: true,
      isJobBoard: path.endsWith("/jobs") || !q("form"),
      isMultiStep: false,
      tips: [
        "Lever uses simple linear forms. URLs for LinkedIn, GitHub, and Portfolio map directly.",
        "Custom question textareas can be quickly drafted using Vani's JD-tailor buttons.",
      ],
    };
  }

  // 4. Ashby
  if (
    host.includes("ashbyhq.com") ||
    q("[data-ashby-embed], [class*='ashby-application']") !== null ||
    (qa("script") as Array<{ src?: string }>).some(s => s.src?.includes("ashbyhq.com"))
  ) {
    return {
      id: "ashby",
      name: "Ashby",
      badge: "Ashby",
      icon: "⚡",
      isATS: true,
      isJobBoard: path.includes("/jobs") && !path.includes("/application"),
      isMultiStep: false,
      tips: [
        "Ashby uses modern React comboboxes and multi-select pill inputs.",
        "Typeahead options and custom date widgets are supported with simulated keyboard inputs.",
      ],
    };
  }

  // 5. SmartRecruiters
  if (host.includes("smartrecruiters.com") || q("[data-smartrecruiters]") !== null) {
    return {
      id: "smartrecruiters",
      name: "SmartRecruiters",
      badge: "SmartRecruiters",
      icon: "🎯",
      isATS: true,
      isJobBoard: false,
      isMultiStep: true,
      tips: [
        "SmartRecruiters splits Personal Info and Experience. Step through each section to auto-fill.",
      ],
    };
  }

  // 6. Taleo / Oracle Cloud
  if (host.includes("taleo.net") || host.includes("oraclecloud.com") || q("#taleoContent, [data-taleo]") !== null) {
    return {
      id: "taleo",
      name: "Taleo (Oracle)",
      badge: "Taleo",
      icon: "🏛️",
      isATS: true,
      isJobBoard: false,
      isMultiStep: true,
      tips: [
        "Taleo portals use multiple tabs/steps. Keep Vani's floating panel open to review missed fillings.",
      ],
    };
  }

  // 7. iCIMS
  if (host.includes("icims.com") || q("iframe[src*='icims.com']") !== null) {
    return {
      id: "icims",
      name: "iCIMS ATS",
      badge: "iCIMS",
      icon: "📑",
      isATS: true,
      isJobBoard: false,
      isMultiStep: true,
      tips: [
        "iCIMS forms often live inside nested frames; Vani queries each frame and aggregates coverage.",
      ],
    };
  }

  // 8. BambooHR
  if (host.includes("bamboohr.com")) {
    return {
      id: "bamboohr",
      name: "BambooHR",
      badge: "BambooHR",
      icon: "🎋",
      isATS: true,
      isJobBoard: !q("form"),
      isMultiStep: false,
      tips: ["BambooHR uses clean, straightforward applications. Fill completes them in one go."],
    };
  }

  // 9. Rippling
  if (host.includes("rippling-ats.com") || host.includes("rippling.com") || q("[data-rippling]") !== null) {
    return {
      id: "rippling",
      name: "Rippling ATS",
      badge: "Rippling",
      icon: "🌊",
      isATS: true,
      isJobBoard: false,
      isMultiStep: false,
      tips: ["Rippling applications feature dynamic multi-choice questions. Review yellow marks."],
    };
  }

  // 10. Major Job Boards
  if (host.includes("linkedin.com")) {
    return {
      id: "linkedin",
      name: "LinkedIn Jobs",
      badge: "LinkedIn",
      icon: "🔗",
      isATS: q(".jobs-easy-apply-modal, .jobs-apply-form") !== null,
      isJobBoard: true,
      isMultiStep: q(".jobs-easy-apply-modal") !== null,
      tips: [
        "Click 'Scrape Jobs' to extract all visible job listings and check real-time relevance.",
        "LinkedIn Easy Apply forms are supported across all multi-step review dialogs.",
      ],
    };
  }

  if (host.includes("indeed.com")) {
    return {
      id: "indeed",
      name: "Indeed",
      badge: "Indeed",
      icon: "🔍",
      isATS: q(".ia-BasePage, [data-testid='ia-container']") !== null,
      isJobBoard: true,
      isMultiStep: false,
      tips: [
        "Use 'Scrape Jobs' on Indeed search pages to rank and filter postings by relevance score.",
      ],
    };
  }

  // Generic Job Board detection
  const isLikelyBoard =
    path.includes("/jobs") || path.includes("/careers") || path.includes("/search") ||
    qa("a[href*='/job/'], a[href*='/careers/'], a[href*='/position/'], [class*='job-card'], [class*='job-listing']").length >= 3;

  if (isLikelyBoard && !q("input[type=file], form button[type=submit]")) {
    return {
      id: "job_board",
      name: "Job Listings Board",
      badge: "Job Board",
      icon: "📋",
      isATS: false,
      isJobBoard: true,
      isMultiStep: false,
      tips: [
        "Job board detected: Click 'Scrape Jobs' to extract all listings and compute relevance in real-time.",
      ],
    };
  }

  // Generic ATS detection
  const hasForm = q("form") !== null;
  const hasInputs = qa("input, select, textarea").length >= 3;
  if (hasForm && hasInputs) {
    return {
      id: "generic_ats",
      name: "Career Application",
      badge: "Application",
      icon: "📝",
      isATS: true,
      isJobBoard: false,
      isMultiStep: qa("[class*='step'], [class*='wizard'], [class*='progress']").length > 0,
      tips: [
        "Custom career portal detected: Vani will automatically map form fields, highlight review items, and track missed fillings.",
      ],
    };
  }

  return {
    id: "general",
    name: "Web Page",
    badge: "Web Page",
    icon: "🌐",
    isATS: false,
    isJobBoard: false,
    isMultiStep: false,
    tips: ["Vani is ready: open an application form to auto-fill or a careers page to scrape listings."],
  };
}
