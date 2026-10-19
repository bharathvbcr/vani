// Real-time relevance engine for job postings and scraped listings.
// Deterministic, fast, and local: compares job descriptions against target roles,
// skills, work authorization, location/remote preferences, and salary expectations.

export type RoleFit =
  | "strong_fit"
  | "good_fit"
  | "partial_fit"
  | "adjacent"
  | "mismatch_seniority"
  | "mismatch_domain";

export interface UserTargetProfile {
  targetTitles: string[];
  skills: string[];
  preferredLocation?: string;
  openToRemote?: boolean;
  requiresSponsorship?: boolean;
  minSalary?: number;
}

export interface RelevanceResult {
  score: number; // 0..100
  fit: RoleFit;
  fitLabel: string;
  matchedSkills: string[];
  missingSkills: string[];
  dealbreakers: string[];
  highlights: string[];
  salaryRange?: string;
  workplaceType?: "Remote" | "Hybrid" | "On-site" | "Unknown";
}

// Default target profile if none configured in user basics/bank
export const DEFAULT_PROFILE: UserTargetProfile = {
  targetTitles: [
    "software engineer", "backend engineer", "systems engineer", "full stack engineer",
    "infrastructure engineer", "platform engineer", "distributed systems engineer",
    "software developer", "golang engineer", "rust engineer", "staff engineer", "senior engineer",
  ],
  skills: [
    "go", "golang", "rust", "typescript", "javascript", "python", "distributed systems",
    "kubernetes", "docker", "microservices", "sql", "postgresql", "linux", "gcp",
    "aws", "cloud", "grpc", "graphql", "rest", "ci/cd", "git", "redis",
  ],
  openToRemote: true,
  requiresSponsorship: false,
};

// Common technical skills dictionary for extraction from JD
const SKILL_DICTIONARY: readonly string[] = [
  "go", "golang", "rust", "typescript", "javascript", "python", "c++", "c#", "java",
  "kubernetes", "k8s", "docker", "microservices", "distributed systems", "linux",
  "postgresql", "postgres", "mysql", "mongodb", "redis", "elasticsearch", "kafka",
  "gcp", "google cloud", "aws", "amazon web services", "azure", "terraform",
  "grpc", "graphql", "rest", "api", "protobuf", "ci/cd", "git", "react", "next.js",
  "node.js", "swift", "kotlin", "wasm", "webassembly", "llm", "machine learning",
  "data engineering", "spark", "sql", "nosql", "dynamodb", "system design",
];

import type { Bank } from "./types";
import { basicsValues } from "./basics";

export const SENIORITY_LEVELS: readonly { name: string; pattern: RegExp; rank: number }[] = [
  { name: "Principal / Staff", pattern: /\b(principal|staff|distinguished|fellow|architect)\b/i, rank: 4 },
  { name: "Lead", pattern: /\b(lead|tech lead|team lead)\b/i, rank: 3 },
  { name: "Senior", pattern: /\b(senior|sr\.?|iii|iv)\b/i, rank: 2 },
  { name: "Mid-level", pattern: /\b(mid|intermediate|ii)\b/i, rank: 1 },
  { name: "Junior / Entry", pattern: /\b(junior|jr\.?|associate|entry|intern|internship|grad|graduate|i)\b/i, rank: 0 },
];

/** Detect seniority level from text. */
export function detectSeniority(text: string): { name: string; rank: number } | null {
  for (const level of SENIORITY_LEVELS) {
    if (level.pattern.test(text)) {
      return { name: level.name, rank: level.rank };
    }
  }
  return null;
}

/** Construct a tailored UserTargetProfile dynamically from user's Bank and Basics. */
export function targetProfileFromBank(bank: Bank | null): UserTargetProfile {
  if (!bank) return { ...DEFAULT_PROFILE };
  const v = basicsValues(bank);
  const profile: UserTargetProfile = { ...DEFAULT_PROFILE };

  // Visa sponsorship
  const sponsorVal = (v["visa-sponsorship"] ?? "").toLowerCase();
  if (sponsorVal.includes("yes") || sponsorVal.includes("require") || sponsorVal === "true") {
    profile.requiresSponsorship = true;
  } else if (sponsorVal.includes("no") || sponsorVal === "false") {
    profile.requiresSponsorship = false;
  }

  // Remote & location preferences
  const inPersonVal = (v["open-to-working-in-person"] ?? "").toLowerCase();
  if (inPersonVal.includes("no")) {
    profile.openToRemote = true;
  }
  const locVal = v["where-youll-work-from"] || v["city"] || v["country"];
  if (locVal) {
    profile.preferredLocation = locVal;
  }

  // Salary expectations
  const salVal = v["salary-expectations"];
  if (salVal) {
    const num = parseInt(salVal.replace(/[^0-9]/g, ""), 10);
    if (!isNaN(num) && num > 1000) {
      profile.minSalary = num;
    }
  }

  // Check saved answers for customized titles and skills
  const bankAnswers = Object.values(bank.answers);
  const titles = new Set<string>(profile.targetTitles);
  const skills = new Set<string>(profile.skills);

  for (const a of bankAnswers) {
    const k = a.key.toLowerCase();
    if (k.includes("target role") || k.includes("target title") || k.includes("job title") || k.includes("current title") || k.includes("desired position")) {
      for (const val of a.value) {
        if (val.trim().length > 3 && val.trim().length < 50) {
          titles.add(val.trim().toLowerCase());
        }
      }
    }
    if (k.includes("skill") || k.includes("technolog") || k.includes("languages") || k.includes("framework")) {
      for (const val of a.value) {
        for (const s of extractSkills(val)) {
          skills.add(s);
        }
      }
    }
  }

  profile.targetTitles = Array.from(titles);
  profile.skills = Array.from(skills);

  return profile;
}

/** Extract workplace type (Remote / Hybrid / On-site) from text. */
export function extractWorkplaceType(text: string): "Remote" | "Hybrid" | "On-site" | "Unknown" {
  const t = text.toLowerCase();
  if (/\b(remote|work from anywhere|wfh|fully remote|100% remote)\b/.test(t)) return "Remote";
  if (/\b(hybrid|flexible work|partially remote|2 days in office|3 days in office)\b/.test(t)) return "Hybrid";
  if (/\b(on-site|onsite|in-office|in office|in person|in-person|office-based)\b/.test(t)) return "On-site";
  return "Unknown";
}

/** Extract salary range from text, e.g. "$140,000 - $180,000". */
export function extractSalaryRange(text: string): string | undefined {
  const match = text.match(/\$[\d,]+(?:\.\d+)?(?:\s*(?:k|thousand))?\s*(?:-|to|–)\s*\$[\d,]+(?:\.\d+)?(?:\s*(?:k|thousand))?/i)
    ?? text.match(/\$[\d,]+(?:\.\d+)?(?:\s*(?:k|thousand))?\s*(?:\/|per\s+)?(?:yr|year|annum|hr|hour)?/i);
  return match ? match[0].trim() : undefined;
}

/** Extract recognized skills mentioned in the job description text. */
export function extractSkills(text: string): string[] {
  const t = ` ${text.toLowerCase().replace(/[^a-z0-9#+.]+/g, " ")} `;
  const found = new Set<string>();
  for (const skill of SKILL_DICTIONARY) {
    const term = skill.toLowerCase();
    const regex = new RegExp(`(?<=[\\s,;.])${term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?=[\\s,;.])`, "i");
    if (regex.test(t)) {
      found.add(skill === "k8s" ? "kubernetes" : skill === "golang" ? "go" : skill);
    }
  }
  return Array.from(found);
}

/** Check for explicit dealbreakers in the job text. */
export function detectDealbreakers(text: string, profile: UserTargetProfile): string[] {
  const t = text.toLowerCase();
  const dealbreakers: string[] = [];

  // Visa sponsorship restrictions
  if (profile.requiresSponsorship) {
    if (
      /\b(no visa sponsorship|not provide sponsorship|unable to sponsor|will not sponsor|cannot sponsor|do not sponsor|no sponsorship now or in the future|sponsorship is not available)\b/.test(t)
    ) {
      dealbreakers.push("Visa sponsorship not available");
    }
    if (/\b(us citizens? only|u\.s\. citizens? only|security clearance required|active secret clearance|top secret)\b/.test(t)) {
      dealbreakers.push("Requires US Citizenship or Security Clearance");
    }
  }

  // Location / Remote restrictions
  if (profile.openToRemote && !profile.preferredLocation) {
    if (/\b(must be willing to relocate|no remote|on-site only|strictly in office)\b/.test(t) && !/\bremote\b/.test(t)) {
      dealbreakers.push("Strictly On-site requirement");
    }
  }

  return dealbreakers;
}

/** Evaluate real-time relevance of a job posting or scraped card. */
export function evaluateRelevance(
  job: { title: string; company?: string; description: string; location?: string },
  customProfile?: Partial<UserTargetProfile>,
): RelevanceResult {
  const profile: UserTargetProfile = { ...DEFAULT_PROFILE, ...customProfile };
  const fullText = `${job.title} ${job.company ?? ""} ${job.location ?? ""} ${job.description}`;
  const titleLower = job.title.toLowerCase();

  // 1. Title & Seniority match
  let titleScore = 0.35; // neutral base
  const normalizeTitleTerm = (term: string) =>
    term.toLowerCase().replace(/\b(developer|programmer)\b/g, "engineer");

  const normalizedJobTitle = normalizeTitleTerm(titleLower);

  const matchesTargetTitle = profile.targetTitles.some(target => {
    const targetWords = normalizeTitleTerm(target).split(/\s+/).filter(w => w.length > 2);
    return targetWords.length > 0 && targetWords.every(w => normalizedJobTitle.includes(w));
  });

  const jobSeniority = detectSeniority(job.title);
  const targetSeniorities = profile.targetTitles
    .map(detectSeniority)
    .filter((s): s is { name: string; rank: number } => s !== null);

  let seniorityMismatch = false;
  let mismatchedTarget: { name: string; rank: number } | null = null;
  if (jobSeniority && targetSeniorities.length > 0) {
    let minDiff = Infinity;
    let closest: { name: string; rank: number } | null = null;
    for (const ts of targetSeniorities) {
      const diff = Math.abs(jobSeniority.rank - ts.rank);
      if (diff < minDiff) {
        minDiff = diff;
        closest = ts;
      }
    }
    if (minDiff >= 2 && closest) {
      seniorityMismatch = true;
      mismatchedTarget = closest;
    }
  }

  if (matchesTargetTitle) {
    titleScore = seniorityMismatch ? 0.65 : 0.95;
  } else if (/\b(engineer|developer|architect|programmer|scientist)\b/.test(titleLower)) {
    titleScore = seniorityMismatch ? 0.45 : 0.70;
  } else if (/\b(manager|product|designer|recruiter|sales|marketing|account)\b/.test(titleLower)) {
    titleScore = 0.15;
  }

  // 2. Skill match
  const extracted = extractSkills(fullText);
  const userSkills = new Set(profile.skills.map(s => s.toLowerCase()));
  const matchedSkills: string[] = [];
  const missingSkills: string[] = [];

  for (const s of extracted) {
    if (userSkills.has(s.toLowerCase())) {
      matchedSkills.push(s);
    } else {
      missingSkills.push(s);
    }
  }

  // Skill overlap curve matching Vani's jobinsight (exponential saturation)
  const targetHits = matchedSkills.length;
  const keywordScale = 3.0;
  const keywordScore = 1 - Math.exp(-targetHits / keywordScale);

  // 3. Workplace & Location
  const workplace = extractWorkplaceType(fullText);
  let workplaceScore = 0.5;
  if (workplace === "Remote" && profile.openToRemote) workplaceScore = 1.0;
  else if (workplace === "Hybrid") workplaceScore = 0.7;
  else if (workplace === "On-site") workplaceScore = profile.openToRemote && !profile.preferredLocation ? 0.3 : 0.8;

  // 4. Dealbreakers
  const dealbreakers = detectDealbreakers(fullText, profile);
  const dealbreakerPenalty = dealbreakers.length * 0.35;

  // 5. Total Relevance calculation: 50% title + 40% skills + 10% workplace - penalties
  const rawScore = (titleScore * 0.50 + keywordScore * 0.40 + workplaceScore * 0.10) - dealbreakerPenalty;
  const clamped = Math.max(0, Math.min(1, rawScore));
  const score = Math.round(clamped * 100);

  // Fit categorization
  let fit: RoleFit = "adjacent";
  let fitLabel = "Adjacent Match";

  if (dealbreakers.length > 0) {
    fit = "mismatch_domain";
    fitLabel = `Mismatch: ${dealbreakers[0]}`;
  } else if (titleScore <= 0.20) {
    fit = "mismatch_domain";
    fitLabel = "Domain Mismatch";
  } else if (seniorityMismatch && jobSeniority && mismatchedTarget) {
    fit = "mismatch_seniority";
    fitLabel = `Seniority Mismatch: ${jobSeniority.name} vs ${mismatchedTarget.name}`;
  } else if (score >= 80) {
    fit = "strong_fit";
    fitLabel = "Strong Match";
  } else if (score >= 65) {
    fit = "good_fit";
    fitLabel = "Good Match";
  } else if (score >= 45) {
    fit = "partial_fit";
    fitLabel = "Partial Fit";
  }

  // Highlights
  const highlights: string[] = [];
  if (matchesTargetTitle) highlights.push(`Role title matches "${job.title}"`);
  if (seniorityMismatch && jobSeniority) highlights.push(`Seniority: ${jobSeniority.name}`);
  if (matchedSkills.length > 0) highlights.push(`Mentions ${matchedSkills.slice(0, 4).join(", ")}`);
  if (workplace !== "Unknown") highlights.push(`${workplace} position`);
  const salary = extractSalaryRange(fullText);
  if (salary) highlights.push(`Salary: ${salary}`);

  return {
    score,
    fit,
    fitLabel: `${fitLabel} (${score}%)`,
    matchedSkills,
    missingSkills: missingSkills.slice(0, 6),
    dealbreakers,
    highlights,
    salaryRange: salary,
    workplaceType: workplace,
  };
}
