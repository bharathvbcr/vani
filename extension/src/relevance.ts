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
    "aws", "grpc", "graphql", "rest", "ci/cd", "git", "redis",
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
  "restful", "apis",
];

// Spellings that name the same skill. The dictionary lists each spelling, extraction reports the
// canonical one, and a profile's own skills are folded through the same map, so a skill is never
// "matched" under one name and "missing" under another.
const SKILL_ALIASES: ReadonlyMap<string, string> = new Map([
  ["k8s", "kubernetes"],
  ["golang", "go"],
  ["google cloud", "gcp"],
  ["amazon web services", "aws"],
  ["postgres", "postgresql"],
  ["webassembly", "wasm"],
  ["restful", "rest"],
  ["apis", "api"],
]);

/** The canonical name of a skill: aliases fold to one spelling, anything else is lower-cased. */
export function canonicalSkill(skill: string): string {
  const key = skill.trim().toLowerCase();
  return SKILL_ALIASES.get(key) ?? key;
}

// Skills that are also English words are only a skill when the words around them do not read as
// prose: "go above and beyond", "the go-to person", "the rest of the team", "git it done".
// `before`/`after` test the text just outside the match (already normalized, anchored to the match).
const SKILL_VETOES: ReadonlyMap<string, { before?: RegExp; after?: RegExp }> = new Map([
  ["go", {
    before: /(?:^|\s)(?:will|would|can|could|should|must|may|let s|we|you|they)\s+$/,
    after: /^\s*(?:above|beyond|further|ahead|forward|live|out|through|into|to\s+(?:market|the|a|an|our|your|their|his|her|my|each|every|all|any)|the\s+(?:extra|distance|mile|way|whole))\b/,
  }],
  ["rest", {
    before: /(?:^|\s)(?:will|would|can|could|should|must|may|you|we|they)\s+$/,
    after: /^\s*(?:of|assured|day|days|period|stop|room|easy)\b/,
  }],
  ["git", { after: /^\s*(?:it|r)\b/ }],
  // "React to change" is prose, "Migrate to React" and "use React to build" are not: only the verb
  // after a subject or modal, an adverb, or a reaction to something that happens is vetoed.
  ["react", {
    before: /(?:^|\s)(?:will|would|can|could|should|must|may|you|we|they|who)\s+$/,
    after: /^\s*(?:quickly|fast|rapidly|promptly|swiftly|calmly|decisively|appropriately|accordingly|positively|to\s+(?:the\s+)?(?:chang\w*|customer\w*|feedback|incident\w*|events?|market|new|unexpected|emerging|evolving|shifting|pressure|alerts?|requests?|users?))\b/,
  }],
  ["swift", { after: /^\s*(?:response|responses|reply|action|actions|decision|decisions|decisive|resolution|turnaround|delivery|progress|growth|pace|execution|moving|justice)\b/ }],
  ["spark", {
    before: /(?:^|\s)(?:will|would|can|could|should|must|may|to\s+help)\s+$/,
    after: /^\s*(?:joy|innovation|curiosity|interest|creativity|ideas?|conversations?|change|excitement|passion|growth|imagination|debate|discussion)\b/,
  }],
  ["rust", { after: /^\s*(?:belt|proof|resistant|removal|inhibitors?|prevention|stains?|colou?red)\b/ }],
]);

interface CompiledSkill {
  canonical: string;
  pattern: RegExp;
  veto?: { before?: RegExp; after?: RegExp };
}

// Precompile skill dictionary patterns once to eliminate RegExp re-compilation in hot extraction loops.
// "ci/cd" loses its slash here as it does in the text (see normalizeSkillText); a skill with a
// veto gets a global pattern so every occurrence can be judged, not just the first.
const COMPILED_SKILLS: readonly CompiledSkill[] = SKILL_DICTIONARY.map(skill => {
  const escaped = skill.toLowerCase().replace(/\//g, "").replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const veto = SKILL_VETOES.get(skill);
  return {
    canonical: canonicalSkill(skill),
    veto,
    pattern: new RegExp(`(?<=[\\s,;.])(?:${escaped})(?=[\\s,;.])`, veto ? "gi" : "i"),
  };
});

/** Lower-case the text and reduce it to the characters a skill is written with. Slash and hyphen
 *  forms of CI/CD and "go-to" are settled first, because the general pass turns every `/` and `-`
 *  into a space, which would leave "ci cd" unmatchable and "go to" indistinguishable from prose. */
function normalizeSkillText(text: string): string {
  const lowered = text.toLowerCase()
    .replace(/\bci\s*[/-]\s*cd\b/g, "cicd")
    .replace(/\bgo-to\b/g, "goto");
  return ` ${lowered.replace(/[^a-z0-9#+.]+/g, " ")} `;
}

function hasUnvetoedMatch(t: string, { pattern, veto }: CompiledSkill): boolean {
  for (const m of t.matchAll(pattern)) {
    const start = m.index;
    const end = start + m[0].length;
    if (veto?.before?.test(t.slice(Math.max(0, start - 16), start))) continue;
    if (veto?.after?.test(t.slice(end, end + 32))) continue;
    return true;
  }
  return false;
}

import type { Bank } from "./types";
import { basicsValues } from "./basics";

// Checked top to bottom, so a title is read at its highest level ("Senior Director" is a director,
// "Associate Director" is not a junior). Words that are a level only in some collocations carry a
// lookahead: an academic "Associate Professor", "Lead Generation" and a "Postdoctoral Fellow" are
// not levels, while "Engineering Fellow" and a bare "Fellow" are.
export const SENIORITY_LEVELS: readonly { name: string; pattern: RegExp; rank: number }[] = [
  { name: "Director / Executive", pattern: /\b(director|head\s+of|vp|svp|evp|vice\s+president|chief|cto|cio|ceo)\b/i, rank: 5 },
  { name: "Principal / Staff", pattern: /\b(principal|staff|distinguished|architect|(?:engineering|technical|corporate)\s+fellow)\b|^\s*fellow\b/i, rank: 4 },
  { name: "Lead", pattern: /\b(lead(?!\s+(?:gen(?:eration)?|capture|scoring|qualification|nurturing|sourcing|management)\b)|tech lead|team lead)\b/i, rank: 3 },
  { name: "Senior", pattern: /\b(senior|sr\.?|iii|iv)\b/i, rank: 2 },
  { name: "Mid-level", pattern: /\b(mid|intermediate|ii)\b/i, rank: 1 },
  { name: "Junior / Entry", pattern: /\b(junior|jr\.?|associate(?!\s+(?:professor|dean|partner|general|chair|provost)\b)|entry|intern|internship|grad|graduate|level\s+1|level\s+i|engineer\s+i|developer\s+i)\b/i, rank: 0 },
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

const WORKPLACE_MODE = String.raw`(?:hybrid|remote(?:ly)?|on-?site|in-?office|in-?person|work from home|wfh)`;
// "no hybrid", "not a remote", "non-remote", "no hybrid or remote": a mode the posting rules out.
const NEGATED_WORKPLACE_MODE = new RegExp(
  String.raw`\b(?:no|not|non|neither|nor|without|never)[\s-]+(?:(?:a|an|fully|purely|strictly|100%)[\s-]+)*${WORKPLACE_MODE}(?:\s*(?:,|/|or|nor|and)\s*${WORKPLACE_MODE}){0,3}`,
  "g",
);
// "remote work is not available", "not eligible for remote work", "cannot work remotely".
const REMOTE_UNAVAILABLE: readonly RegExp[] = [
  /\b(?:remote(?:ly)?|telework|telecommut\w*)(?: (?:work|working|option|options|arrangements?|opportunities|positions?))? (?:(?:is|are|will be|would be|can't be|cannot be) )?(?:currently )?(?:not (?:be )?(?:available|permitted|allowed|offered|possible|eligible|an option|supported|approved)|unavailable)\b/g,
  /\b(?:not eligible|ineligible|not available|not permitted|not allowed|not open)\s+(?:for|to)\s+(?:work(?:ing)?\s+)?(?:remote|remotely|telework)/g,
  /\b(?:cannot|can't|can not|unable to|not able to|not permitted to|not allowed to) work remotely/g,
];
const HYBRID_SIGNAL = new RegExp([
  String.raw`\b(?:hybrid|flexible work|partially remote|\d\s*days? in office|\d\s*days? on-site)\b`,
  // A capped share of remote time is a hybrid arrangement: "remotely up to 50% of the time".
  String.raw`\bremote(?:ly)?\s+(?:for\s+)?(?:up to|at least|no more than|a maximum of|about|approximately)\s+\d{1,2}\s*%`,
  String.raw`\b(?:up to|at least|no more than|about|approximately)\s+\d{1,2}\s*%\s+(?:of\s+(?:the|your)\s+time\s+)?remote(?:ly)?\b`,
].join("|"));
const REMOTE_SIGNAL = /\b(?:remote(?:ly)?|work from anywhere|wfh|telecommut\w*|telework\w*)\b/;
const STRICT_ONSITE_SIGNAL = /\b(?:strictly (?:on-site|in office)|on-site only|onsite only)\b/;
const ONSITE_SIGNAL = /\b(?:on-site|onsite|in-office|in office|in person|in-person|office-based)\b/;

/** Extract workplace type (Remote / Hybrid / On-site) from text. */
export function extractWorkplaceType(text: string): "Remote" | "Hybrid" | "On-site" | "Unknown" {
  let t = text.toLowerCase().replace(/[’‘]/g, "'");
  // A mode the posting rules out must not count as a mode it offers ("Fully remote (no hybrid)"
  // is remote; "on-site; remote work is not available" is on-site). Ruled-out phrases are removed
  // before the signals below are read, and a ruled-out remote is kept as an on-site signal.
  let remoteRuledOut = false;
  t = t.replace(NEGATED_WORKPLACE_MODE, m => {
    if (/remote|wfh|work from home/.test(m)) remoteRuledOut = true;
    return " ";
  });
  for (const unavailable of REMOTE_UNAVAILABLE) {
    t = t.replace(unavailable, () => {
      remoteRuledOut = true;
      return " ";
    });
  }
  // Hybrid first (hybrid postings often contain words like remote and office)
  if (HYBRID_SIGNAL.test(t)) return "Hybrid";
  if (remoteRuledOut || STRICT_ONSITE_SIGNAL.test(t)) return "On-site";
  if (REMOTE_SIGNAL.test(t)) return "Remote";
  if (ONSITE_SIGNAL.test(t)) return "On-site";
  return "Unknown";
}

/** Extract salary range from text, e.g. "$140,000 - $180,000". */
export function extractSalaryRange(text: string): string | undefined {
  const rangeMatch = text.match(/\$[\d,]+(?:\.\d+)?(?:\s*(?:k|thousand))?\s*(?:-|to|–)\s*\$[\d,]+(?:\.\d+)?(?:\s*(?:k|thousand))?/i);
  if (rangeMatch) return rangeMatch[0].trim();

  const singleMatch = text.match(/\$[\d,]+(?:\.\d+)?\s*(?:k|thousand)\s*(?:\/|per\s+)?(?:yr|year|annum|hr|hour)?/i)
    ?? text.match(/\$[\d,]+(?:\.\d+)?\s*(?:\/|per\s+)(?:yr|year|annum|hr|hour)/i)
    ?? text.match(/\$(?:[3-9]\d,\d{3}|\d{3},\d{3})(?:\.\d+)?/);
  return singleMatch ? singleMatch[0].trim() : undefined;
}

/** Extract recognized skills mentioned in the job description text. */
export function extractSkills(text: string): string[] {
  const t = normalizeSkillText(text);
  const found = new Set<string>();
  for (const skill of COMPILED_SKILLS) {
    if (found.has(skill.canonical)) continue;
    if (skill.veto ? hasUnvetoedMatch(t, skill) : skill.pattern.test(t)) {
      found.add(skill.canonical);
    }
  }
  return Array.from(found);
}

// A posting that rules out sponsoring the candidate's visa. Every form is negative, so none can
// fire on "we can sponsor" or "visa sponsorship available".
const NEGATION = String.raw`(?:unable|not able|cannot|can ?not|can't|won't|will not|do not|does not|don't|doesn't)(?: (?:to|be|able|currently|presently))*`;
const NO_SPONSORSHIP: readonly RegExp[] = [
  // "no visa sponsorship", "no sponsorship now or in the future"
  /\bno (?:(?:visa|employment|work|h-?1b|immigration|employer|company|current|future)\s+)*sponsorship\b/,
  // "authorized to work in the US without (current or future visa) sponsorship"
  /\bwithout (?:[\w-]+ ){0,4}sponsorship\b/,
  // "unable to sponsor", "will not be able to sponsor", "do not sponsor"
  new RegExp(String.raw`\b${NEGATION} sponsor(?:ing)?\b`),
  // "does not offer H-1B visa sponsorship", "unable to provide visa sponsorship"
  new RegExp(String.raw`\b${NEGATION} (?:provide|offer|support|extend|give|accept)(?: [\w-]+){0,4} sponsorship\b`),
  // "sponsorship is not available", "visa sponsorship is not provided", "sponsorship unavailable"
  /\bsponsorship (?:(?:is|are|will be|would be|can be|could be) )?(?:currently )?(?:not (?:be )?(?:available|provided|offered|supported|possible|permitted)|unavailable)\b/,
  // "sponsorship will not be provided", "sponsorship cannot be offered"
  /\bsponsorship (?:cannot|can't|will not|won't|would not) be\b/,
];

// A posting that requires citizenship. "Must be a U.S. citizen", "U.S. citizenship is required",
// "US citizens only"; a negation just before ("do not need to be a U.S. citizen") is not a requirement.
const US_COUNTRY = String.raw`(?:u\.?s\.?|united states)`;
const CITIZENSHIP_REQUIRED: readonly RegExp[] = [
  /\b(?:us|u\.s\.) citizens? only\b/,
  new RegExp(String.raw`(?<!not )(?<!n't )(?<!no )\b(?:must|need to|required to|has to|have to)\s+(?:be|hold|have|possess)\s+(?:an?\s+)?(?:(?:current|active|valid)\s+)?${US_COUNTRY}\s+citizen(?:ship)?\b`),
  new RegExp(String.raw`\b${US_COUNTRY}\s+citizenship\s+(?:is\s+)?(?:required|mandatory|necessary|a must)\b`),
  new RegExp(String.raw`\brequires?\s+${US_COUNTRY}\s+citizenship\b`),
];

/** Check for explicit dealbreakers in the job text. */
export function detectDealbreakers(text: string, profile: UserTargetProfile): string[] {
  const t = text.toLowerCase().replace(/[’‘]/g, "'");
  const dealbreakers: string[] = [];

  // Visa sponsorship restrictions
  if (profile.requiresSponsorship) {
    if (NO_SPONSORSHIP.some(re => re.test(t))) {
      dealbreakers.push("Visa sponsorship not available");
    }
    if (
      CITIZENSHIP_REQUIRED.some(re => re.test(t)) ||
      /\b(active secret clearance|top secret)\b/.test(t) ||
      (/\b(security clearance required)\b/.test(t) && !/\b(no|not|without|neither)\s+security clearance required\b/.test(t))
    ) {
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

// Titles whose role noun is not engineering work, whatever engineering words surround it.
// ("product" alone is weaker: "Software Engineer, Product" is an engineer, so it is judged last.)
const NON_ENGINEERING_ROLE = /\b(manager|recruiter|recruiting|director|sales|marketing|account|designer)\b/;

/** Evaluate real-time relevance of a job posting or scraped card. */
export function evaluateRelevance(
  job: { title: string; company?: string; description: string; location?: string },
  customProfile?: Partial<UserTargetProfile>,
): RelevanceResult {
  // A spread would let an explicit `undefined` replace a default, so only defined fields override.
  const profile: UserTargetProfile = { ...DEFAULT_PROFILE };
  for (const [key, value] of Object.entries(customProfile ?? {})) {
    if (value !== undefined) Object.assign(profile, { [key]: value });
  }
  const fullText = `${job.title} ${job.company ?? ""} ${job.location ?? ""} ${job.description}`;
  const titleLower = job.title.toLowerCase();

  // 1. Title & Seniority match
  let titleScore = 0.35; // neutral base
  // Words, not substrings: "engineering" is not "engineer", so a recruiter, director or intern of
  // "software engineering" does not match the target "software engineer".
  const titleWords = (term: string): Set<string> =>
    new Set(term.toLowerCase().replace(/\b(developer|programmer)\b/g, "engineer").split(/[^a-z0-9]+/).filter(Boolean));

  const jobWords = titleWords(titleLower);
  const matchesTarget = (target: string): boolean => {
    const targetWords = [...titleWords(target)].filter(w => w.length > 2);
    return targetWords.length > 0 && targetWords.every(w => jobWords.has(w));
  };

  const matchedTargets = profile.targetTitles.filter(matchesTarget);
  // A manager, recruiter, director or the like is not the engineer the title's other words name,
  // unless the candidate's own target titles say they want that role.
  const nonEngineeringRole = NON_ENGINEERING_ROLE.test(titleLower)
    && !matchedTargets.some(target => NON_ENGINEERING_ROLE.test(target.toLowerCase()));
  const matchesTargetTitle = matchedTargets.length > 0 && !nonEngineeringRole;

  const jobSeniority = detectSeniority(job.title);
  const targetSeniorities = profile.targetTitles
    .map(detectSeniority)
    .filter((s): s is { name: string; rank: number } => s !== null);

  let seniorityMismatch = false;
  let mismatchedTarget: { name: string; rank: number } | null = null;
  if (jobSeniority && targetSeniorities.length > 0) {
    // If targetTitles already includes a neutral unconstrained title (e.g. "software engineer") that matches,
    // do not penalize seniority unless targetTitles only specifies distinct ranks
    const hasNeutralTargetMatch = profile.targetTitles.some(target => detectSeniority(target) === null && matchesTarget(target));

    if (!hasNeutralTargetMatch) {
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
  }

  if (nonEngineeringRole) {
    titleScore = 0.15;
  } else if (matchesTargetTitle) {
    titleScore = seniorityMismatch ? 0.65 : 0.95;
  } else if (/\b(engineer|developer|architect|programmer|scientist)\b/.test(titleLower)) {
    titleScore = seniorityMismatch ? 0.45 : 0.70;
  } else if (/\bproduct\b/.test(titleLower)) {
    titleScore = 0.15;
  }

  // 2. Skill match
  const extracted = extractSkills(fullText);
  const userSkills = new Set(profile.skills.map(canonicalSkill));
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
