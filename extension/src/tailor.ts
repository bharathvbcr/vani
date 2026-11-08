// Repeated questions tailored to Job Description (JD).
// Identifies open-ended application questions (Why company?, Why role?, Fit/Experience, Cover letter)
// and dynamically tailors answers using extracted company name, role title, and tech stack requirements.

import type { JobInfo } from "./draft";
import { motivationEssay } from "./intents";
import type { UserTargetProfile } from "./relevance";

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

/** A match carries no biography. Open answers stay blank until a saved answer or a grounded draft fills them. */
function tailored(kind: TailoredKind, title: string): TailoredQuestionMatch {
  return { kind, title, templateSnippet: "" };
}

const WHY_COMPANY_PATTERNS = [
  /\bwhy (do you want to|are you interested in|would you like to) (work|join|be part of)\b/i,
  /\bwhy (at|with)?\s*(this company|our company|us|our team)\b/i,
  /\bwhat (attracts|interests|excites) you (about|to) (this company|our company|us|our mission)\b/i,
  /\bwhy do you want to work here\b/i,
  /\bwhat makes you want to join\b/i,
  /\bwhy\s+(?!role|this\s+role|the\s+role|position|this\s+position|apply|applying)[A-Z][A-Za-z0-9&.,'-]+(?:\s+[A-Za-z0-9&.,'-]+){0,3}\??$/i,
];

const WHY_ROLE_PATTERNS = [
  /\bwhy (this|the) (role|position|job|opportunity)\b/i,
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
export function templateTailoring(label: string, companyName?: string): TailoredQuestionMatch | null {
  if (motivationEssay(label)) return null;
  const m = matchTailorableQuestion(label, companyName);
  return m && !DRAFT_KINDS.has(m.kind) ? m : null;
}

/** Check if a field label is a repeated question that can be tailored to the JD. */
export function matchTailorableQuestion(label: string, companyName?: string): TailoredQuestionMatch | null {
  const l = label.trim();

  if (companyName && companyName.trim().length > 1) {
    const esc = companyName.trim().replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    if (new RegExp(`\\bwhy\\s+(at\\s+|with\\s+)?${esc}\\b`, "i").test(l)) {
      return tailored("why_company", "Why This Company");
    }
  }

  for (const p of WHY_COMPANY_PATTERNS) {
    if (p.test(l)) {
      return tailored("why_company", "Why This Company");
    }
  }

  for (const p of WHY_ROLE_PATTERNS) {
    if (p.test(l)) {
      return tailored("why_role", "Why This Role");
    }
  }

  for (const p of ROLE_FIT_PATTERNS) {
    if (p.test(l)) {
      return tailored("role_fit", "Why You're a Fit");
    }
  }

  for (const p of PROJECT_EXPERIENCE_PATTERNS) {
    if (p.test(l)) {
      return tailored("project_experience", "Relevant Project");
    }
  }

  for (const p of COVER_LETTER_PATTERNS) {
    if (p.test(l)) {
      return tailored("cover_letter", "Cover Letter / Note");
    }
  }

  for (const p of SALARY_PATTERNS) {
    if (p.test(l)) {
      return tailored("salary_expectations", "Salary Expectations");
    }
  }

  for (const p of START_DATE_PATTERNS) {
    if (p.test(l)) {
      return tailored("start_date", "Earliest Start Date");
    }
  }

  for (const p of WORK_AUTH_PATTERNS) {
    if (p.test(l)) {
      return tailored("work_auth_explanation", "Work Authorization Details");
    }
  }

  for (const p of REMOTE_COLLABORATION_PATTERNS) {
    if (p.test(l)) {
      return tailored("remote_collaboration", "Remote Collaboration");
    }
  }

  for (const p of DIVERSITY_PATTERNS) {
    if (p.test(l)) {
      return tailored("diversity_perspective", "Diversity & Perspective");
    }
  }

  return null;
}

/**
 * Shown when an open answer has no saved text and no grounded draft.
 * It names what Vani will not invent. It is not itself an answer.
 */
export const OPEN_ANSWER_HOLD =
  "Vani left this blank. It will not invent experience, a project story, a date, a salary, work authorization, or another open answer. A saved answer, or a draft that already passed the grounding check, can still fill this field.";

const OPEN_ANSWER_KINDS: ReadonlySet<TailoredKind> = new Set<TailoredKind>([
  "why_company",
  "why_role",
  "role_fit",
  "project_experience",
  "cover_letter",
  "salary_expectations",
  "start_date",
  "work_auth_explanation",
  "remote_collaboration",
  "diversity_perspective",
]);

export interface OpenAnswerFill {
  filled: boolean;
  text: string;
  source?: "stored" | "grounded_draft";
  reason?: string;
}

/**
 * What may be written into an open answer. A saved answer wins, then a draft
 * that already passed the grounding check. Anything else stays empty, with a
 * reason the person can read. This never invents a biography.
 */
export function openAnswerFill(input: {
  label: string;
  companyName?: string;
  stored?: string;
  groundedDraft?: string;
}): OpenAnswerFill {
  const stored = input.stored?.trim() ?? "";
  if (stored !== "") return { filled: true, text: stored, source: "stored" };
  const grounded = input.groundedDraft?.trim() ?? "";
  if (grounded !== "") return { filled: true, text: grounded, source: "grounded_draft" };
  const match = matchTailorableQuestion(input.label, input.companyName);
  if (match && OPEN_ANSWER_KINDS.has(match.kind)) {
    return { filled: false, text: "", reason: OPEN_ANSWER_HOLD };
  }
  return { filled: false, text: "", reason: OPEN_ANSWER_HOLD };
}

/**
 * No biography. The old templates invented experience, dates, salary, and work
 * authorization from an empty profile. Callers that still import this get an
 * empty string; openAnswerFill decides whether a field is filled.
 */
export function generateTailoredDraft(
  _kind: TailoredKind,
  _job: JobInfo,
  _profile?: Partial<UserTargetProfile>,
): string {
  return "";
}
