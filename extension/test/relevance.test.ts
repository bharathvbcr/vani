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
    expect(mismatchRes.fitLabel).toContain("Seniority Mismatch");
  });
});
