// Repeated questions tailored to Job Description (JD).
// Identifies open-ended application questions (Why company?, Why role?, Fit/Experience, Cover letter)
// and dynamically tailors answers using extracted company name, role title, and tech stack requirements.

import type { JobInfo } from "./draft";
import { motivationEssay } from "./intents";
import { extractSalaryRange, extractSkills, type UserTargetProfile } from "./relevance";

export type TailoredKind =
  | "why_company"
  | "why_role"
  | "role_fit"
  | "project_experience"
  | "cover_letter"
  | "salary_expectations"
  | "start_date"
  | "work_auth_explanation"
  | "remote_collaboration"
  | "diversity_perspective";

export interface TailoredQuestionMatch {
  kind: TailoredKind;
  title: string;
  templateSnippet: string;
}

const WHY_COMPANY_PATTERNS = [
  /\bwhy (do you want to|are you interested in|would you like to) (work|join|be part of)\b/i,
  /\bwhy (at|with)?\s*(this company|our company|us|our team)\b/i,
  /\bwhat (attracts|interests|excites) you (about|to) (this company|our company|us|our mission)\b/i,
  /\bwhy do you want to work here\b/i,
];

const WHY_ROLE_PATTERNS = [
  /\bwhy (are you interested in|do you want) (this|the) (role|position|job|opportunity)\b/i,
  /\bwhat (interests|excites|appeals to) you about this (role|position|job)\b/i,
  /\bwhy (apply|applying) for this (role|position)\b/i,
];

const ROLE_FIT_PATTERNS = [
  /\bwhat makes you a (good|great|strong|ideal) fit\b/i,
  /\bwhy are you (the right|a good|the best) candidate\b/i,
  /\bdescribe your relevant (experience|background)\b/i,
  /\bhow does your (experience|background) align\b/i,
  /\bwhat qualifications make you\b/i,
];

const PROJECT_EXPERIENCE_PATTERNS = [
  /\b(describe|tell us about) a (?:[a-z]+\s+)*(project|system|technical challenge)\b/i,
  /\bdescribe a (?:[a-z]+\s+)*project you (are proud of|built|led|worked on)\b/i,
  /\bmost significant technical achievement\b/i,
];

const COVER_LETTER_PATTERNS = [
  /\bcover letter\b/i,
  /\badditional (information|comments|notes|details)\b/i,
  /\banything else you( would like|'d like) (us|the team) to know\b/i,
  /\bnote to (the )?hiring manager\b/i,
];

const SALARY_PATTERNS = [
  /\b(salary|compensation|pay|rate)\b.*\b(expectations?|desired|requirements?|target|range|looking for)\b/i,
  /\b(desired|expected|target) (salary|compensation|pay|rate|base)\b/i,
];

const START_DATE_PATTERNS = [
  /\b(earliest|soonest|available|desired) start\b/i,
  /\b(when|how soon) (can|could|are) you (able to )?start\b/i,
  /\bnotice period\b/i,
];

const WORK_AUTH_PATTERNS = [
  /\b(visa|sponsorship|work authori[sz]ation)\b.*\b(explain|details?|specify|status|notes?|comments?)\b/i,
  /\bif (you require|yes|applicable).*\b(sponsorship|visa)\b/i,
];

const REMOTE_COLLABORATION_PATTERNS = [
  /\b(remote|distributed|wfh|async)\b.*\b(experience|work|collaboration|teams?)\b/i,
  /\bhow do you (work|collaborate|communicate) (remotely|in a distributed)\b/i,
];

const DIVERSITY_PATTERNS = [
  /\b(diversity|inclusion|diverse perspectives?|underrepresented|belonging|dei)\b/i,
  /\bcontribute to (our )?(culture|diversity|inclusive environment)\b/i,
];

/**
 * Why this company and why this role are written for one posting by `vani draft`, from the JD and
 * the owner's profile (the Draft button): a template never answers them.
 */
export const DRAFT_KINDS: ReadonlySet<TailoredKind> = new Set<TailoredKind>(["why_company", "why_role"]);

/**
 * The question a template may answer, or null. A why-company or why-role question, or any wording
 * intents.ts reads as a motivation essay ("Why Anthropic?"), is left to Draft.
 */
export function templateTailoring(label: string): TailoredQuestionMatch | null {
  if (motivationEssay(label)) return null;
  const m = matchTailorableQuestion(label);
  return m && !DRAFT_KINDS.has(m.kind) ? m : null;
}

/** Check if a field label is a repeated question that can be tailored to the JD. */
export function matchTailorableQuestion(label: string): TailoredQuestionMatch | null {
  const l = label.trim();

  for (const p of WHY_COMPANY_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "why_company",
        title: "Why This Company",
        templateSnippet: "I am drawn to the mission and engineering culture here...",
      };
    }
  }

  for (const p of WHY_ROLE_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "why_role",
        title: "Why This Role",
        templateSnippet: "This role directly aligns with my focus on scalable backend systems...",
      };
    }
  }

  for (const p of ROLE_FIT_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "role_fit",
        title: "Why You're a Fit",
        templateSnippet: "My background in high-throughput services and clean APIs matches...",
      };
    }
  }

  for (const p of PROJECT_EXPERIENCE_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "project_experience",
        title: "Relevant Project",
        templateSnippet: "In my recent work, I designed and delivered a resilient service...",
      };
    }
  }

  for (const p of COVER_LETTER_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "cover_letter",
        title: "Cover Letter / Note",
        templateSnippet: "Dear Hiring Team, I am excited to apply for this opportunity...",
      };
    }
  }

  for (const p of SALARY_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "salary_expectations",
        title: "Salary Expectations",
        templateSnippet: "My salary expectations are flexible and in line with market rates...",
      };
    }
  }

  for (const p of START_DATE_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "start_date",
        title: "Earliest Start Date",
        templateSnippet: "I am available to start within a standard two-week notice period...",
      };
    }
  }

  for (const p of WORK_AUTH_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "work_auth_explanation",
        title: "Work Authorization Details",
        templateSnippet: "I am legally authorized to work...",
      };
    }
  }

  for (const p of REMOTE_COLLABORATION_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "remote_collaboration",
        title: "Remote Collaboration",
        templateSnippet: "I have substantial experience collaborating effectively in distributed teams...",
      };
    }
  }

  for (const p of DIVERSITY_PATTERNS) {
    if (p.test(l)) {
      return {
        kind: "diversity_perspective",
        title: "Diversity & Perspective",
        templateSnippet: "I believe strong engineering teams thrive on diverse viewpoints...",
      };
    }
  }

  return null;
}

/** Generate an adaptive, grounded draft tailored to the specific company, role, skills, and seeker profile. */
export function generateTailoredDraft(
  kind: TailoredKind,
  job: JobInfo,
  profile?: Partial<UserTargetProfile>,
): string {
  const company = job.company?.trim() || "your team";
  const title = job.title?.trim() || "this position";
  const skills = extractSkills(job.description);
  const skillList = skills.slice(0, 3).join(", ");
  const skillText = skillList ? ` particularly working with ${skillList}` : "";
  const salaryRange = extractSalaryRange(job.description);

  switch (kind) {
    case "why_company":
      return `I have been following ${company}'s work and appreciate the engineering excellence and high standard of product craftsmanship you bring to the problem space. I am excited by the opportunity to contribute to a collaborative, ambitious team where solid systems design and meaningful user impact are prioritized.`;

    case "why_role":
      return `I am excited about the ${title} role at ${company} because it offers the ideal intersection of high-impact challenges and technical depth${skillText}. The responsibilities outlined in the job description align closely with where I excel: building dependable, well-tested services and taking end-to-end ownership from design through production delivery.`;

    case "role_fit":
      return `My background directly matches the needs of the ${title} role. I have extensive hands-on experience designing, scaling, and maintaining robust software systems${skillText}. I take pride in pragmatic engineering—writing clean code, establishing automated tests, and communicating effectively across cross-functional teams to reliably ship quality software.`;

    case "project_experience":
      return `In a recent project, I architected and implemented an end-to-end service designed for reliability and low latency${skillText}. I focused on thorough boundary validation, clear error contracts, and deterministic test coverage. The solution handled production traffic reliably, reduced error rates, and significantly boosted developer velocity.`;

    case "cover_letter":
      return `Dear ${company} Hiring Team,\n\nI am writing to express my strong enthusiasm for the ${title} position at ${company}. Having reviewed the requirements and responsibilities, I am confident that my engineering experience${skillText} and focus on building resilient, performant systems will allow me to make an immediate impact on your team.\n\nI would love the opportunity to discuss how my skill set and dedication can help drive ${company}'s engineering goals forward.\n\nThank you for your time and consideration.`;

    case "salary_expectations":
      if (salaryRange) {
        return `My compensation expectations align with the posted range of ${salaryRange} for this role. I am open to discussing a competitive total package that reflects the role's scope, responsibilities, and equity opportunities.`;
      }
      if (profile?.minSalary) {
        return `My target base compensation is around $${profile.minSalary.toLocaleString()}, flexible depending on the full compensation structure, equity, and benefits.`;
      }
      return `My salary expectations are flexible and in line with market rates for a ${title} role with this scope. I am open to discussing a competitive package including base salary, performance incentives, and benefits.`;

    case "start_date":
      return `I am available to start within a standard two-week notice period, though I can remain flexible based on team onboarding cycles and project schedules.`;

    case "work_auth_explanation":
      if (profile?.requiresSponsorship) {
        return `I will require employment visa sponsorship (e.g. H-1B transfer or work authorization) to work in the United States and look forward to discussing the onboarding timeline.`;
      }
      return `I am fully authorized to work in the United States without restriction and do not require employer sponsorship now or in the future.`;

    case "remote_collaboration":
      return `I have substantial experience collaborating effectively in distributed, remote-first engineering teams. I prioritize asynchronous communication, high-clarity technical documentation, proactive issue tracking, and clean pull request reviews to ensure seamless velocity across time zones.`;

    case "diversity_perspective":
      return `I believe strong engineering teams thrive on diverse viewpoints, thoughtful mentorship, and inclusive collaboration. In my day-to-day work, I focus on empathetic code reviews, creating psychological safety for teammates, and designing software accessible to all users.`;
  }
}
