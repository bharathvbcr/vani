import { describe, expect, test } from "bun:test";
import {
  DEFAULT_PROFILE,
  detectDealbreakers,
  detectSeniority,
  evaluateRelevance,
  extractSalaryRange,
  extractSkills,
  extractWorkplaceType,
  targetProfileFromBank,
} from "../src/relevance";

describe("relevance engine", () => {
  test("extractWorkplaceType recognizes remote, hybrid, and onsite", () => {
    expect(extractWorkplaceType("This is a 100% remote software engineer role.")).toBe("Remote");
    expect(extractWorkplaceType("Hybrid position: 2 days in office per week.")).toBe("Hybrid");
    expect(extractWorkplaceType("Strictly on-site position at our headquarters.")).toBe("On-site");
    expect(extractWorkplaceType("Standard job description with no location mention.")).toBe("Unknown");
  });

  test("extractSalaryRange extracts various salary formats", () => {
    expect(extractSalaryRange("Salary: $150,000 - $190,000 per year")).toBe("$150,000 - $190,000");
    expect(extractSalaryRange("Comp: $140k to $180k")).toBe("$140k to $180k");
    expect(extractSalaryRange("Hourly rate: $75/hr")).toBe("$75/hr");
    expect(extractSalaryRange("Competitive compensation and equity")).toBeUndefined();
  });

  test("extractSkills extracts technical skills accurately", () => {
    const text = "We are building high-throughput microservices using Go, Rust, and Kubernetes on GCP with PostgreSQL and Redis.";
    const skills = extractSkills(text);
    expect(skills).toContain("go");
    expect(skills).toContain("rust");
    expect(skills).toContain("kubernetes");
    expect(skills).toContain("gcp");
    expect(skills).toContain("postgresql");
    expect(skills).toContain("redis");
    expect(skills).toContain("microservices");
    expect(skills).not.toContain("java");
  });

  test("detectDealbreakers catches visa and clearance restrictions", () => {
    const profile = { ...DEFAULT_PROFILE, requiresSponsorship: true };
    const noSponsorText = "We are unable to sponsor visas for this role at this time.";
    expect(detectDealbreakers(noSponsorText, profile)).toContain("Visa sponsorship not available");

    const clearanceText = "Must hold active secret clearance and US Citizens only.";
    expect(detectDealbreakers(clearanceText, profile)).toContain("Requires US Citizenship or Security Clearance");

    const okText = "We sponsor visas for exceptional candidates.";
    expect(detectDealbreakers(okText, profile)).toHaveLength(0);
  });

  test("evaluateRelevance computes high score for matching role and skills", () => {
    const res = evaluateRelevance({
      title: "Senior Backend Engineer (Go / Rust)",
      company: "Acme Corp",
      location: "Remote - US",
      description: "Looking for an engineer with strong experience in Go, Rust, Kubernetes, and distributed systems to build cloud services on GCP.",
    });

    expect(res.score).toBeGreaterThanOrEqual(80);
    expect(res.fit).toBe("strong_fit");
    expect(res.matchedSkills).toContain("go");
    expect(res.matchedSkills).toContain("rust");
    expect(res.matchedSkills).toContain("kubernetes");
    expect(res.workplaceType).toBe("Remote");
  });

  test("evaluateRelevance discounts domain mismatch", () => {
    const res = evaluateRelevance({
      title: "Senior Marketing Manager",
      company: "Acme Corp",
      location: "New York, NY",
      description: "Lead our digital marketing campaigns, SEO, and paid social strategy.",
    });

    expect(res.score).toBeLessThan(40);
    expect(res.fit).toBe("mismatch_domain");
  });

  test("evaluateRelevance penalizes dealbreakers", () => {
    const res = evaluateRelevance(
      {
        title: "Senior Backend Engineer",
        company: "Defense contractor",
        location: "Remote",
        description: "Build microservices in Go. Note: US Citizens only, no visa sponsorship available.",
      },
      { requiresSponsorship: true }
    );

    expect(res.dealbreakers.length).toBeGreaterThan(0);
    expect(res.score).toBeLessThan(50);
  });

  test("boundary conditions: handles empty inputs gracefully", () => {
    const res = evaluateRelevance({
      title: "",
      company: "",
      location: "",
      description: "",
    });

    expect(res.score).toBeGreaterThanOrEqual(0);
    expect(res.score).toBeLessThanOrEqual(100);
    expect(res.matchedSkills).toHaveLength(0);
    expect(res.dealbreakers).toHaveLength(0);
  });

  test("targetProfileFromBank derives preferences from bank basics and answers", () => {
    const mockBank = {
      version: 1,
      answers: {
        "ans-1": {
          key: "basics:visa-sponsorship",
          value: ["Yes, I require sponsorship"],
          kind: "text",
          labels: ["Visa sponsorship"],
          uses: 1,
          fills: 1,
          savedAt: Date.now(),
        },
        "ans-2": {
          key: "basics:salary-expectations",
          value: ["$175,000"],
          kind: "text",
          labels: ["Salary expectations"],
          uses: 1,
          fills: 1,
          savedAt: Date.now(),
        },
        "ans-3": {
          key: "target role",
          value: ["Staff Distributed Systems Engineer"],
          kind: "text",
          labels: ["Desired role"],
          uses: 1,
          fills: 1,
          savedAt: Date.now(),
        },
        "ans-4": {
          key: "skills",
          value: ["Kafka, Cassandra, Rust, Go"],
          kind: "text",
          labels: ["Core technologies"],
          uses: 1,
          fills: 1,
          savedAt: Date.now(),
        },
      },
      settings: {
        captureSelfId: false,
        disabledHosts: [],
      },
    } as any;

    const profile = targetProfileFromBank(mockBank);
    expect(profile.requiresSponsorship).toBe(true);
    expect(profile.minSalary).toBe(175000);
    expect(profile.targetTitles).toContain("staff distributed systems engineer");
    expect(profile.skills).toContain("kafka");
    expect(profile.skills).toContain("rust");
  });

  test("detectSeniority identifies correct ranks and detects genuine seniority mismatches", () => {
    const principal = detectSeniority("Principal Platform Architect");
    expect(principal?.rank).toBe(4);

    const lead = detectSeniority("Lead Go Engineer");
    expect(lead?.rank).toBe(3);

    const senior = detectSeniority("Senior Software Engineer");
    expect(senior?.rank).toBe(2);

    const mid = detectSeniority("Mid-level Developer");
    expect(mid?.rank).toBe(1);

    const junior = detectSeniority("Junior Software Intern");
    expect(junior?.rank).toBe(0);

    // Junior role against Staff-only target profile
    const mismatchRes = evaluateRelevance(
      {
        title: "Software Engineering Intern",
        company: "Acme Corp",
        description: "Entry-level summer internship learning Go programming.",
      },
      {
        targetTitles: ["Principal Engineer", "Staff Software Engineer"],
      }
    );

    expect(mismatchRes.fit).toBe("mismatch_seniority");
    expect(mismatchRes.fitLabel).toContain("Seniority mismatch");
  });

  test("extractWorkplaceType prioritizes Hybrid over Remote mentions and respects negations", () => {
    expect(extractWorkplaceType("This role is hybrid (2 days remote, 3 days in office)")).toBe("Hybrid");
    expect(extractWorkplaceType("Flexible hybrid setup with partially remote schedule")).toBe("Hybrid");
    expect(extractWorkplaceType("Strictly on-site position; no remote work offered")).toBe("On-site");
    expect(extractWorkplaceType("100% fully remote from anywhere in the US")).toBe("Remote");
  });

  test("extractSalaryRange does not falsely match minor stipends or 401(k) mentions", () => {
    expect(extractSalaryRange("Benefits include $15 daily lunch credit and $500 wellness stipend")).toBeUndefined();
    expect(extractSalaryRange("Company matches up to 4% on $401(k) plan")).toBeUndefined();
    expect(extractSalaryRange("Expected salary is $160k per year")).toBe("$160k per year");
    expect(extractSalaryRange("Base salary: $155,000")).toBe("$155,000");
    expect(extractSalaryRange("Competitive pay of $85/hr for contract")).toBe("$85/hr");
  });

  test("detectDealbreakers respects negative security clearance phrasing", () => {
    const profile = { ...DEFAULT_PROFILE, requiresSponsorship: true };
    const noClearanceText = "Role details: No security clearance required. Work on public cloud software.";
    expect(detectDealbreakers(noClearanceText, profile)).not.toContain("Requires US Citizenship or Security Clearance");

    const mustHaveText = "Top secret security clearance required for all applicants.";
    expect(detectDealbreakers(mustHaveText, profile)).toContain("Requires US Citizenship or Security Clearance");
  });

  test("evaluateRelevance does not flag seniority mismatch when candidate targets neutral software engineer", () => {
    const res = evaluateRelevance(
      {
        title: "Software Engineer I",
        company: "Acme Corp",
        description: "Develop services in Go and Kubernetes.",
      },
      DEFAULT_PROFILE
    );

    expect(res.fit).not.toBe("mismatch_seniority");
    expect(res.score).toBeGreaterThanOrEqual(60);
  });
});

describe("title match is by whole word, and the non-engineering penalty comes first", () => {
  const rate = (title: string, profile?: Parameters<typeof evaluateRelevance>[1]) =>
    evaluateRelevance({ title, description: "Go Python" }, profile);

  for (const title of [
    "Software Engineering Recruiter",
    "Director of Software Engineering",
    "Software Engineering Intern",
    "Product Manager, Engineering Platforms",
    "Engineering Manager, Platform",
    "Recruiter, Software Engineering",
    "Platform Engineering Sales Executive",
  ]) {
    test(`${title} is not a title match and does not rate as a good match`, () => {
      const r = rate(title);
      expect(r.highlights.some(h => h.startsWith("Role title matches"))).toBe(false);
      expect(r.score).toBeLessThan(50);
      expect(r.fit).not.toBe("strong_fit");
    });
  }

  test("a recruiter for an engineer title is penalised even though every target word is present", () => {
    const r = rate("Software Engineer Recruiter");
    expect(r.fit).toBe("mismatch_domain");
    expect(r.score).toBeLessThan(40);
  });

  test("a manager of engineers is penalised even though the title matches a target", () => {
    const r = rate("Software Engineer Manager");
    expect(r.fit).toBe("mismatch_domain");
  });

  test("hyphens and punctuation still separate words", () => {
    const r = rate("Full-Stack Engineer (Remote)");
    expect(r.highlights).toContain('Role title matches "Full-Stack Engineer (Remote)"');
  });

  test("developer still reads as engineer", () => {
    expect(rate("Backend Developer").highlights.some(h => h.startsWith("Role title matches"))).toBe(true);
  });

  test("a product word beside an engineer title is not a penalty", () => {
    const r = rate("Software Engineer, Product");
    expect(r.fit).not.toBe("mismatch_domain");
    expect(r.highlights.some(h => h.startsWith("Role title matches"))).toBe(true);
  });

  test("a user who targets managers is not penalised for them", () => {
    const r = rate("Engineering Manager, Platform", { targetTitles: ["engineering manager"] });
    expect(r.fit).not.toBe("mismatch_domain");
    expect(r.highlights.some(h => h.startsWith("Role title matches"))).toBe(true);
  });
});

describe("sponsorship and citizenship dealbreakers", () => {
  const needs = { ...DEFAULT_PROFILE, requiresSponsorship: true };
  const SPONSOR = "Visa sponsorship not available";
  const CITIZEN = "Requires US Citizenship or Security Clearance";

  for (const text of [
    "Must be authorized to work in the United States on a full-time basis for any employer without sponsorship.",
    "Applicants must be authorized to work in the US without sponsorship.",
    "Candidates must work in the US without current or future visa sponsorship.",
    "We are unable to provide visa sponsorship.",
    "This position does not offer visa sponsorship.",
    "This position does not offer H-1B visa sponsorship.",
    "Visa sponsorship is not provided.",
    "Sponsorship will not be provided for this role.",
    "Sponsorship is not available for this role.",
    "We are not able to sponsor.",
    "We will not be able to sponsor work visas.",
    "We do not provide sponsorship.",
    "No visa sponsorship.",
    "No sponsorship now or in the future.",
  ]) {
    test(`flags: ${text}`, () => {
      expect(detectDealbreakers(text, needs)).toContain(SPONSOR);
    });
  }

  for (const text of [
    "Must be a U.S. citizen.",
    "Must be a US citizen or permanent resident.",
    "U.S. citizenship is required.",
    "US citizenship required for this position.",
    "This role requires U.S. citizenship.",
    "You must hold United States citizenship.",
  ]) {
    test(`flags: ${text}`, () => {
      expect(detectDealbreakers(text, needs)).toContain(CITIZEN);
    });
  }

  for (const text of [
    "We can sponsor visas for the right candidate.",
    "Visa sponsorship available.",
    "We offer visa sponsorship.",
    "You do not need to be a U.S. citizen to apply.",
    "No security clearance required.",
    "We do not discriminate on the basis of citizenship.",
  ]) {
    test(`does not flag: ${text}`, () => {
      expect(detectDealbreakers(text, needs)).toEqual([]);
    });
  }

  test("nothing is flagged for a candidate who needs no sponsorship", () => {
    const free = { ...DEFAULT_PROFILE, requiresSponsorship: false };
    expect(detectDealbreakers("Must be a U.S. citizen. We are unable to provide visa sponsorship.", free)).toEqual([]);
  });

  test("evaluateRelevance penalises a posting that rules out sponsorship", () => {
    const r = evaluateRelevance(
      { title: "Software Engineer", description: "Go and Kubernetes. Must be authorized to work in the United States without sponsorship." },
      { requiresSponsorship: true },
    );
    expect(r.dealbreakers).toContain(SPONSOR);
    expect(r.fit).toBe("mismatch_domain");
  });
});

describe("skill aliases fold to one canonical skill", () => {
  const cases: [string, string][] = [
    ["Google Cloud", "gcp"],
    ["Google Cloud Platform", "gcp"],
    ["Amazon Web Services", "aws"],
    ["Postgres", "postgresql"],
    ["WebAssembly", "wasm"],
    ["k8s", "kubernetes"],
    ["Golang", "go"],
  ];
  for (const [alias, canonical] of cases) {
    test(`${alias} is ${canonical}`, () => {
      const skills = extractSkills(`Experience with ${alias}, daily.`);
      expect(skills).toContain(canonical);
      expect(skills).not.toContain(alias.toLowerCase());
    });
  }

  test("a matched alias is not reported missing", () => {
    const r = evaluateRelevance({
      title: "Software Engineer",
      description: "Experience with Google Cloud, Amazon Web Services, Postgres and WebAssembly.",
    });
    expect(r.matchedSkills).toEqual(expect.arrayContaining(["gcp", "aws", "postgresql"]));
    // wasm is not in the default profile: it is missing, under its canonical name only.
    expect(r.missingSkills).toEqual(["wasm"]);
  });

  test("a profile that names a skill by its alias still matches the canonical skill", () => {
    const r = evaluateRelevance(
      { title: "Software Engineer", description: "We run on Google Cloud with PostgreSQL." },
      { skills: ["Postgres", "Google Cloud"] },
    );
    expect(r.matchedSkills).toEqual(expect.arrayContaining(["gcp", "postgresql"]));
    expect(r.missingSkills).toEqual([]);
  });

  test("every default profile skill can be extracted from a posting", () => {
    for (const skill of DEFAULT_PROFILE.skills) {
      expect({ skill, found: extractSkills(`We use ${skill}, every day.`).length > 0 }).toEqual({ skill, found: true });
    }
  });
});

describe("skill tokens that never matched, or matched English", () => {
  test("CI/CD is extracted however it is written", () => {
    for (const text of ["Own the CI/CD pipeline.", "CI / CD experience", "ci-cd tooling", "CICD systems"]) {
      expect(extractSkills(text)).toContain("ci/cd");
    }
  });

  test("a slash between skills still separates them", () => {
    expect(extractSkills("Python/Go services on AWS/GCP")).toEqual(expect.arrayContaining(["python", "go", "aws", "gcp"]));
  });

  test("CI/CD counts toward a default-profile match", () => {
    const r = evaluateRelevance({ title: "Software Engineer", description: "You will own the CI/CD pipeline." });
    expect(r.matchedSkills).toContain("ci/cd");
  });

  for (const text of [
    "You will go above and beyond.",
    "Go above and beyond for our customers.",
    "You will be the go-to person for the team.",
    "Our go to market strategy.",
    "Go to market with us.",
    "Let's go further together.",
    "Ready to go the extra mile?",
    "The rest of the team will help.",
    "Rest assured, we care.",
    "The team will rest on weekends.",
    "Git it done.",
  ]) {
    test(`English is not a skill: ${text}`, () => {
      expect(extractSkills(text)).toEqual([]);
    });
  }

  for (const [text, skill] of [
    ["Go, Rust and Python", "go"],
    ["Experience with Go and Kubernetes", "go"],
    ["We write Go", "go"],
    ["Go (the language)", "go"],
    ["Go-lang services", "go"],
    ["Design the REST API", "rest"],
    ["REST and GraphQL", "rest"],
    ["Version control with Git.", "git"],
  ] as const) {
    test(`still a skill: ${text}`, () => {
      expect(extractSkills(text)).toContain(skill);
    });
  }

  test("APIs and RESTful are the api and rest skills", () => {
    expect(extractSkills("Design APIs for partners")).toContain("api");
    expect(extractSkills("Build RESTful services")).toContain("rest");
  });

  test("prose alone does not lift the score of an unrelated posting", () => {
    const plain = evaluateRelevance({ title: "Software Engineer", description: "Build services." });
    const prose = evaluateRelevance({
      title: "Software Engineer",
      description: "You will go above and beyond. The rest of the team is great. Go to market with us.",
    });
    expect(prose.matchedSkills).toEqual([]);
    expect(prose.score).toBe(plain.score);
  });

  test("a long run of ambiguous words stays fast", () => {
    const t0 = performance.now();
    extractSkills("go to ".repeat(100_000));
    extractSkills("the rest of ".repeat(100_000));
    expect(performance.now() - t0).toBeLessThan(2000);
  });
});

describe("workplace type reads negations", () => {
  const cases: [string, "Remote" | "Hybrid" | "On-site" | "Unknown"][] = [
    ["Fully remote (no hybrid)", "Remote"],
    ["This is a remote position, not hybrid.", "Remote"],
    ["This is an on-site role in Austin; remote work is not available", "On-site"],
    ["Remote work not permitted", "On-site"],
    ["Not eligible for remote work", "On-site"],
    ["No hybrid or remote options", "On-site"],
    ["This role is not hybrid, not remote", "On-site"],
    ["Position is not hybrid and requires on-site", "On-site"],
    ["Candidates cannot work remotely", "On-site"],
    ["Remote, no on-site requirement", "Remote"],
    ["No on-site requirement", "Unknown"],
    ["You will work remotely from anywhere", "Remote"],
    ["Work remotely", "Remote"],
    ["We are remote-first", "Remote"],
    ["You may work remotely up to 50% of the time", "Hybrid"],
    ["Work remotely at least 50% of the time", "Hybrid"],
    ["Hybrid or remote", "Hybrid"],
    ["Hybrid; remote work is not available", "Hybrid"],
    ["Flexible work arrangements are available, including the ability to work remotely at least 50% of the time.", "Hybrid"],
    ["100% remote", "Remote"],
  ];
  for (const [text, want] of cases) {
    test(`${text} is ${want}`, () => {
      expect(extractWorkplaceType(text)).toBe(want);
    });
  }

  test("a negated remote does not raise the workplace score", () => {
    const r = evaluateRelevance({
      title: "Software Engineer",
      description: "On-site in Austin; remote work is not available.",
    });
    expect(r.workplaceType).toBe("On-site");
    expect(r.highlights).toContain("On-site position");
  });
});

describe("seniority reads the title, not a word inside it", () => {
  const rank = (t: string) => detectSeniority(t)?.name ?? null;

  test("associate is junior only as a level", () => {
    expect(rank("Associate Software Engineer")).toBe("Junior / Entry");
    expect(rank("Associate Product Manager")).toBe("Junior / Entry");
    expect(rank("Associate Professor of Computer Science")).toBeNull();
    expect(rank("Associate Dean")).toBeNull();
    expect(rank("Associate Director")).toBe("Director / Executive");
  });

  test("lead is a level, not a sales collocation", () => {
    expect(rank("Lead Go Engineer")).toBe("Lead");
    expect(rank("Engineering Lead")).toBe("Lead");
    expect(rank("Tech Lead")).toBe("Lead");
    expect(rank("Lead Generation Specialist")).toBeNull();
    expect(rank("Lead Gen Engineer")).toBeNull();
    expect(rank("Lead Qualification Analyst")).toBeNull();
  });

  test("an academic fellow is not a principal", () => {
    expect(rank("Postdoctoral Fellow")).toBeNull();
    expect(rank("Post-doctoral Research Fellow")).toBeNull();
    expect(rank("Research Fellow")).toBeNull();
    expect(rank("Teaching Fellow")).toBeNull();
    expect(rank("Distinguished Engineer")).toBe("Principal / Staff");
    expect(rank("Engineering Fellow")).toBe("Principal / Staff");
    expect(rank("Technical Fellow")).toBe("Principal / Staff");
    expect(rank("Fellow, Engineering")).toBe("Principal / Staff");
  });

  test("head, vp and director are an executive level above principal", () => {
    for (const t of ["Director, Platform", "Director of Engineering", "Head of Engineering", "VP of Engineering", "Vice President, Engineering", "Senior Director of Engineering", "CTO"]) {
      expect({ t, name: rank(t) }).toEqual({ t, name: "Director / Executive" });
    }
    expect(detectSeniority("Director, Platform")?.rank).toBe(5);
  });

  test("the levels that were already right still are", () => {
    expect(rank("Principal Software Engineer")).toBe("Principal / Staff");
    expect(rank("Senior Staff Engineer")).toBe("Principal / Staff");
    expect(rank("Senior Software Engineer")).toBe("Senior");
    expect(rank("Software Engineer II")).toBe("Mid-level");
    expect(rank("Software Engineer I")).toBe("Junior / Entry");
    expect(rank("Forward Deployed Engineer")).toBeNull();
    expect(rank("Engineering Manager")).toBeNull();
  });

  test("an academic title is not a seniority mismatch against an engineer profile", () => {
    const r = evaluateRelevance({ title: "Associate Professor of Computer Science", description: "teach" });
    expect(r.fit).not.toBe("mismatch_seniority");
  });

  test("a director against a staff-level profile is not a mismatch of two or more ranks", () => {
    const r = evaluateRelevance({ title: "Director of Platform Engineering", description: "Go" }, { targetTitles: ["staff engineer"] });
    expect(r.fit).not.toBe("mismatch_seniority");
  });
});

describe("a partial profile falls back to the default for every field it leaves undefined", () => {
  const job = { title: "Software Engineer", description: "Build Go services on Kubernetes. This is a remote role." };

  test("an explicit undefined skills or targetTitles does not throw", () => {
    expect(() => evaluateRelevance(job, { skills: undefined })).not.toThrow();
    expect(() => evaluateRelevance(job, { targetTitles: undefined })).not.toThrow();
    expect(() => evaluateRelevance(job, { skills: undefined, targetTitles: undefined })).not.toThrow();
  });

  test("an undefined field scores exactly like the default profile", () => {
    const base = evaluateRelevance(job);
    expect(evaluateRelevance(job, { skills: undefined })).toEqual(base);
    expect(evaluateRelevance(job, { targetTitles: undefined })).toEqual(base);
    expect(evaluateRelevance(job, { openToRemote: undefined, requiresSponsorship: undefined })).toEqual(base);
  });

  test("a field that is set still overrides the default", () => {
    const r = evaluateRelevance(job, { skills: ["kubernetes"], targetTitles: undefined });
    expect(r.matchedSkills).toEqual(["kubernetes"]);
    expect(evaluateRelevance(job, { skills: [] }).matchedSkills).toEqual([]);
  });

  test("an explicit false is a value, not a missing field", () => {
    const text = { title: "Software Engineer", description: "No visa sponsorship is available." };
    expect(evaluateRelevance(text, { requiresSponsorship: true }).dealbreakers).toContain("Visa sponsorship not available");
    expect(evaluateRelevance(text, { requiresSponsorship: undefined }).dealbreakers).toEqual([]);
  });
});

describe("react, swift, spark and rust are skills only when they are not prose", () => {
  for (const text of [
    "Engineers who react quickly to incidents.",
    "You will react to changing priorities.",
    "Ability to react calmly under pressure.",
    "We react to customer feedback.",
    "A swift response to outages.",
    "Take swift action on incidents.",
    "Swift decisions are made here.",
    "Our swift growth continues.",
    "We spark joy in everyone.",
    "Ideas that spark innovation.",
    "You will spark curiosity in the team.",
    "Spark interest in our product.",
    "Welcome to the rust belt.",
    "Rust belt manufacturing.",
    "Rust-proof coatings.",
  ]) {
    test(`English is not a skill: ${text}`, () => {
      expect(extractSkills(text)).toEqual([]);
    });
  }

  for (const [text, skill] of [
    ["React and Redux", "react"],
    ["Experience with React, Vue or Angular", "react"],
    ["Migrating from Angular to React", "react"],
    ["Use React to build dashboards", "react"],
    ["React Native apps", "react"],
    ["Swift and Kotlin for mobile", "swift"],
    ["iOS development in Swift.", "swift"],
    ["Swift 5 concurrency", "swift"],
    ["Apache Spark pipelines", "spark"],
    ["Migrate jobs to Spark", "spark"],
    ["Hadoop, Spark and Kafka", "spark"],
    ["Rust and Go services", "rust"],
    ["Rust-based storage engines", "rust"],
    ["Systems programming in Rust.", "rust"],
  ] as const) {
    test(`still a skill: ${text}`, () => {
      expect(extractSkills(text)).toContain(skill);
    });
  }

  test("prose alone does not lift the score of an unrelated posting", () => {
    const plain = evaluateRelevance({ title: "Software Engineer", description: "Build services." });
    const prose = evaluateRelevance({
      title: "Software Engineer",
      description: "React quickly. A swift response. Spark joy. Welcome to the rust belt.",
    });
    expect(prose.matchedSkills).toEqual([]);
    expect(prose.score).toBe(plain.score);
  });
});
