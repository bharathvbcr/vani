import { describe, expect, test } from "bun:test";
import { generateTailoredDraft, matchTailorableQuestion, templateTailoring } from "../src/tailor";
import type { JobInfo } from "../src/draft";

describe("templateTailoring: why-company and why-role questions are Draft's", () => {
  test("no template for a motivation question, in any wording the matchers know", () => {
    for (const label of [
      "Why do you want to work at Acme?",
      "What attracts you to our company and mission?",
      "Why are you interested in this position?",
      "Why Anthropic?",
      "Why Anthropic",
      "Why us?",
    ]) {
      expect(templateTailoring(label, "Anthropic")).toBeNull();
      expect(templateTailoring(label)).toBeNull();
    }
    // The company-name matcher alone would have called this why_company.
    expect(matchTailorableQuestion("Why at Anthropic, specifically?", "Anthropic")?.kind).toBe("why_company");
    expect(templateTailoring("Why at Anthropic, specifically?", "Anthropic")).toBeNull();
  });

  test("other tailorable questions keep their templates", () => {
    expect(templateTailoring("What makes you a strong fit for this team?")?.kind).toBe("role_fit");
    expect(templateTailoring("Cover letter or note to the hiring manager")?.kind).toBe("cover_letter");
    expect(templateTailoring("What are your salary expectations?")?.kind).toBe("salary_expectations");
    expect(templateTailoring("")).toBeNull();
    expect(templateTailoring("What is your phone number?")).toBeNull();
  });
});

describe("tailored repeated questions engine", () => {
  test("matchTailorableQuestion categorizes repeated questions correctly", () => {
    // Why company
    const q1 = matchTailorableQuestion("Why do you want to work at Acme?");
    expect(q1?.kind).toBe("why_company");
    expect(q1?.title).toBe("Why This Company");

    const q1b = matchTailorableQuestion("What attracts you to our company and mission?");
    expect(q1b?.kind).toBe("why_company");

    // Why role
    const q2 = matchTailorableQuestion("Why are you interested in this position?");
    expect(q2?.kind).toBe("why_role");
    expect(q2?.title).toBe("Why This Role");

    // Role fit
    const q3 = matchTailorableQuestion("What makes you a strong fit for this team?");
    expect(q3?.kind).toBe("role_fit");

    const q3b = matchTailorableQuestion("Describe your relevant experience for this job");
    expect(q3b?.kind).toBe("role_fit");

    // Project experience
    const q4 = matchTailorableQuestion("Describe a challenging technical project you led");
    expect(q4?.kind).toBe("project_experience");

    // Cover letter
    const q5 = matchTailorableQuestion("Cover letter or note to the hiring manager");
    expect(q5?.kind).toBe("cover_letter");

    const q5b = matchTailorableQuestion("Anything else you would like us to know?");
    expect(q5b?.kind).toBe("cover_letter");

    // Non-tailorable questions
    expect(matchTailorableQuestion("What is your phone number?")).toBeNull();
    expect(matchTailorableQuestion("Years of experience with SQL")).toBeNull();
    expect(matchTailorableQuestion("")).toBeNull();
  });

  test("generateTailoredDraft injects company, title, and skills into all 5 templates", () => {
    const job: JobInfo = {
      title: "Senior Infrastructure Engineer",
      company: "CloudTech Labs",
      description: "We are seeking a senior infrastructure engineer skilled in Go, Kubernetes, and GCP to scale distributed systems.",
    };

    const whyCompany = generateTailoredDraft("why_company", job);
    expect(whyCompany).toContain("CloudTech Labs");

    const whyRole = generateTailoredDraft("why_role", job);
    expect(whyRole).toContain("Senior Infrastructure Engineer");
    expect(whyRole).toContain("CloudTech Labs");
    expect(whyRole.toLowerCase()).toContain("go");

    const roleFit = generateTailoredDraft("role_fit", job);
    expect(roleFit).toContain("Senior Infrastructure Engineer");
    expect(roleFit.toLowerCase()).toContain("kubernetes");

    const project = generateTailoredDraft("project_experience", job);
    expect(project.toLowerCase()).toContain("go");
    expect(project).toContain("latency");

    const coverLetter = generateTailoredDraft("cover_letter", job);
    expect(coverLetter).toContain("Dear CloudTech Labs Hiring Team");
    expect(coverLetter).toContain("Senior Infrastructure Engineer");
  });

  test("generateTailoredDraft handles missing or minimal job info gracefully", () => {
    const emptyJob: JobInfo = {
      title: "",
      company: "",
      description: "",
    };

    const draftRole = generateTailoredDraft("why_role", emptyJob);
    expect(draftRole).toContain("this position");
    expect(draftRole).toContain("your team");

    const draftCover = generateTailoredDraft("cover_letter", emptyJob);
    expect(draftCover).toContain("Dear your team Hiring Team");
  });

  test("matchTailorableQuestion recognizes all new repeated question categories", () => {
    // Salary expectations
    const qSalary = matchTailorableQuestion("What are your salary expectations?");
    expect(qSalary?.kind).toBe("salary_expectations");
    expect(qSalary?.title).toBe("Salary Expectations");

    // Start date
    const qStart = matchTailorableQuestion("When is your earliest available start date?");
    expect(qStart?.kind).toBe("start_date");
    expect(qStart?.title).toBe("Earliest Start Date");

    // Work authorization
    const qAuth = matchTailorableQuestion("Please specify your visa sponsorship status and work authorization");
    expect(qAuth?.kind).toBe("work_auth_explanation");
    expect(qAuth?.title).toBe("Work Authorization Details");

    // Remote collaboration
    const qRemote = matchTailorableQuestion("How do you manage async communication and remote collaboration?");
    expect(qRemote?.kind).toBe("remote_collaboration");
    expect(qRemote?.title).toBe("Remote Collaboration");

    // Diversity
    const qDiv = matchTailorableQuestion("How do you contribute to diversity, equity, and inclusion in engineering?");
    expect(qDiv?.kind).toBe("diversity_perspective");
    expect(qDiv?.title).toBe("Diversity & Perspective");
  });

  test("generateTailoredDraft tailors new categories based on JD and profile", () => {
    const jobWithComp: JobInfo = {
      title: "Backend Engineer",
      company: "DataCo",
      description: "Base salary $160,000 - $190,000 per year. Looking for Go developers.",
    };

    const salDraft = generateTailoredDraft("salary_expectations", jobWithComp);
    expect(salDraft).toContain("$160,000 - $190,000");

    const salDraftProfile = generateTailoredDraft("salary_expectations", { title: "Dev", company: "A", description: "" }, { minSalary: 155000 });
    expect(salDraftProfile).toContain("155,000");

    const authSponsor = generateTailoredDraft("work_auth_explanation", jobWithComp, { requiresSponsorship: true });
    expect(authSponsor).toContain("visa sponsorship");

    const authNoSponsor = generateTailoredDraft("work_auth_explanation", jobWithComp, { requiresSponsorship: false });
    expect(authNoSponsor).toContain("fully authorized");

    const startDate = generateTailoredDraft("start_date", jobWithComp);
    expect(startDate).toContain("two-week notice period");

    const remoteDraft = generateTailoredDraft("remote_collaboration", jobWithComp);
    expect(remoteDraft).toContain("asynchronous communication");

    const diversityDraft = generateTailoredDraft("diversity_perspective", jobWithComp);
    expect(diversityDraft).toContain("diverse viewpoints");
  });

  test("matchTailorableQuestion matches company-specific prompts", () => {
    // Direct company questions
    const qAnthropic = matchTailorableQuestion("Why Anthropic?");
    expect(qAnthropic?.kind).toBe("why_company");

    const qStripe = matchTailorableQuestion("Why Stripe?");
    expect(qStripe?.kind).toBe("why_company");

    const qWithCompany = matchTailorableQuestion("Why at OpenAI?", "OpenAI");
    expect(qWithCompany?.kind).toBe("why_company");

    const qJoin = matchTailorableQuestion("What makes you want to join our team?");
    expect(qJoin?.kind).toBe("why_company");

    // Negative lookahead check: ensure "Why this role?" stays why_role
    const qRole = matchTailorableQuestion("Why this role?");
    expect(qRole?.kind).toBe("why_role");
  });
});
