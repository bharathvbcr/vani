// The resume attach rules that need no DOM: which file input is a resume, how
// the Mac app's resumeGet/resumePut replies are read, and the size and type checks
// the bytes pass before they reach a page. No personal data: names are neutral.
import { describe, expect, test } from "bun:test";
import {
  MAX_RESUME_BYTES, buildResumeRequest, checkResumeName, fromBase64, isPdf, isResumeField, parseBuiltResume, parseResumeGet,
  parseResumeInfo, resumeHeader, resumeKey, resumeOp, toBase64,
} from "../src/resume";
import { type FieldInfo, emptyBank } from "../src/types";
import { mergeCapture } from "../src/bank";
import { BASICS_HOST, basicsFields } from "../src/basics";

const field = (label: string, name = "", automationId = "", kind: FieldInfo["kind"] = "file") =>
  ({ kind, label, name, automationId });

const PDF = new TextEncoder().encode("%PDF-1.4\n%test\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n");

describe("isResumeField", () => {
  test.each([
    ["Resume/CV", ""],
    ["Resume", ""],
    ["Résumé", ""],
    ["Upload your CV", ""],
    ["Curriculum Vitae", ""],
    ["Resume (PDF preferred) *", ""],
    ["Attach", "resume"],                       // job-boards.greenhouse.io: the visible label is the button text
    ["Upload", "job_application[resume]"],
    ["", "_systemfield_resume"],                // Ashby
    ["Select file", "", "resumeUpload"],
    ["Select files", "", "file-upload-resume"],
  ])("resume: %p name %p", (label, name, aid = "") => {
    expect(isResumeField(field(label, name, aid))).toBe(true);
  });

  test.each([
    ["Cover Letter", ""],
    ["Attach", "cover_letter"],
    ["Other attachments", ""],
    ["Additional documents", ""],
    ["Transcript", ""],
    ["Portfolio", ""],
    ["Writing sample", ""],
    ["Resume or cover letter", ""],             // two documents in one box: never guess
    ["Resume", "cover_letter"],                 // a label and a name that disagree
    ["Supporting documents (resume, references)", ""],
    ["CVV", ""],                                // a word that only contains "cv"
    ["Attach", ""],
    ["Upload a file", "file-upload-input-ref"],
    ["", ""],
  ])("not a resume: %p name %p", (label, name) => {
    expect(isResumeField(field(label, name))).toBe(false);
  });

  test("only file inputs", () => {
    expect(isResumeField(field("Resume", "resume", "", "text"))).toBe(false);
    expect(isResumeField(field("Resume", "resume", "", "textarea"))).toBe(false);
  });

  test("oversized labels are read only up to a bound and never throw", () => {
    expect(isResumeField(field("x".repeat(100_000) + " resume", ""))).toBe(false);
    expect(isResumeField(field("Resume " + "x".repeat(100_000), ""))).toBe(true);
  });
});

describe("resumeKey", () => {
  test("is the page key: job-boards and boards are one site, the query is dropped", () => {
    expect(resumeKey("https://job-boards.greenhouse.io/acme/jobs/123?gh_src=x#app")).toBe("https://boards.greenhouse.io/acme/jobs/123");
    expect(resumeKey("https://boards.greenhouse.io/embed/job_app?for=acme&token=123")).toBe("https://boards.greenhouse.io/acme/jobs/123");
  });
  test("refuses what is not an http(s) page", () => {
    for (const u of ["", "file:///tmp/x.pdf", "javascript:alert(1)", "https://", "x".repeat(3000)]) expect(resumeKey(u)).toBeNull();
  });
});

describe("bytes", () => {
  test("base64 round trip", () => {
    const b = new Uint8Array(70_000).map((_, i) => (i * 31) & 0xff);
    expect(fromBase64(toBase64(b))).toEqual(b);
    expect(fromBase64(toBase64(new Uint8Array()))).toEqual(new Uint8Array());
  });
  test("malformed base64 is refused, not decoded to garbage", () => {
    for (const s of ["%%%", "abc", "ab=c", "QUJD\nREVG", 7 as unknown as string]) expect(fromBase64(s)).toBeNull();
  });
  test("isPdf needs the %PDF- signature", () => {
    expect(isPdf(PDF)).toBe(true);
    expect(isPdf(new TextEncoder().encode("<html>%PDF-"))).toBe(false);
    expect(isPdf(new Uint8Array())).toBe(false);
  });
  test("checkResumeName: a bounded printable .pdf name", () => {
    expect(checkResumeName("Resume_Research_Tools.pdf")).toBe("Resume_Research_Tools.pdf");
    expect(checkResumeName("  cv.PDF ")).toBe("cv.PDF");
    for (const n of ["", "cv.docx", "cv", ".pdf", "a/b.pdf", "a\\b.pdf", "x\u0000.pdf", "x\n.pdf", `${"x".repeat(200)}.pdf`, 3 as unknown as string]) {
      expect(checkResumeName(n)).toBeNull();
    }
  });
});

describe("parseResumeGet", () => {
  const ok = { found: true, name: "Resume_Test.pdf", pdf: toBase64(PDF), bytes: PDF.length, pickedAt: "2026-10-19T12:00:00Z" };

  test("a bound resume", () => {
    const r = parseResumeGet(ok);
    expect(r.ok).toBe(true);
    if (r.ok) { expect(r.name).toBe("Resume_Test.pdf"); expect(r.bytes).toEqual(PDF); expect(r.pickedAt).toBe(ok.pickedAt); }
  });

  test("nothing bound to this job is its own reason: no fallback", () => {
    expect(parseResumeGet({ found: false })).toEqual({ ok: false, reason: "no_resume", detail: expect.any(String) });
  });

  test("an unreachable app, a host error and a malformed reply are reasons, never a silent skip", () => {
    const cases: unknown[] = [
      { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" },
      { error: "the resume file is missing", code: "missing" },
      { error: "unknown op resumeGet" },             // an older Mac app
      undefined, null, "x", [], {},
      { ...ok, pdf: "%%%" },
      { ...ok, pdf: toBase64(new TextEncoder().encode("<html>not a pdf")) },
      { ...ok, bytes: PDF.length + 1 },              // the size the app read is not what arrived
      { ...ok, name: "../evil.pdf" },
      { ...ok, pdf: toBase64(new Uint8Array(MAX_RESUME_BYTES + 1).fill(0x25)) },
    ];
    for (const c of cases) {
      const r = parseResumeGet(c);
      expect(r.ok).toBe(false);
      if (!r.ok) { expect(r.reason).toBe("resume_unavailable"); expect(r.detail.length).toBeGreaterThan(0); expect(r.detail.length).toBeLessThanOrEqual(200); }
    }
  });

  test("a host error's text is bounded", () => {
    const r = parseResumeGet({ error: "e".repeat(10_000), code: "missing" });
    expect(!r.ok && r.detail.length <= 200).toBe(true);
  });
});

describe("resumeOp", () => {
  const url = "https://job-boards.greenhouse.io/acme/jobs/123?gh_src=x";
  const key = "https://boards.greenhouse.io/acme/jobs/123";
  test("reads, info and delete go out keyed by the page key only", () => {
    expect(resumeOp({ type: "resumeGet", url })).toEqual({ op: "resumeGet", key });
    expect(resumeOp({ type: "resumeInfo", url })).toEqual({ op: "resumeInfo", key });
    expect(resumeOp({ type: "resumeDelete", url })).toEqual({ op: "resumeDelete", key });
  });
  test("a put carries a checked PDF", () => {
    expect(resumeOp({ type: "resumePut", url, name: " Resume_Test.pdf ", pdf: toBase64(PDF) }))
      .toEqual({ op: "resumePut", key, name: "Resume_Test.pdf", pdf: toBase64(PDF) });
  });
  test("refusals say why and are coded", () => {
    const cases: [Parameters<typeof resumeOp>[0], string][] = [
      [{ type: "resumeGet", url: "chrome://extensions" }, "bad_request"],
      [{ type: "resumePut", url, name: "cv.docx", pdf: toBase64(PDF) }, "bad_request"],
      [{ type: "resumePut", url, name: "cv.pdf", pdf: "%%%" }, "bad_request"],
      [{ type: "resumePut", url, name: "cv.pdf", pdf: "" }, "bad_request"],
      [{ type: "resumePut", url, name: "cv.pdf", pdf: toBase64(new TextEncoder().encode("<html>")) }, "bad_request"],
      [{ type: "resumePut", url, name: "cv.pdf", pdf: toBase64(new Uint8Array(MAX_RESUME_BYTES + 1).fill(0x25)) }, "too_large"],
      [{ type: "resumePut", url, name: "cv.pdf", pdf: "A".repeat(2_000_000) }, "too_large"],
    ];
    for (const [m, code] of cases) {
      const r = resumeOp(m);
      expect("error" in r && r.code === code).toBe(true);
    }
  });
});

describe("building one", () => {
  const bankWith = (values: Record<string, string>) =>
    mergeCapture(emptyBank(), basicsFields(values), BASICS_HOST, new Date("2026-10-19T00:00:00Z")).bank;

  test("resumeHeader: the name and contact lines come from Your basics", () => {
    const b = bankWith({ first: "Test", last: "Applicant", email: "applicant@example.invalid", phone: "555-0100", city: "Testville", country: "Nowhere", linkedin: "https://example.invalid/in/t" });
    expect(resumeHeader(b)).toEqual({
      name: "Test Applicant",
      contact_lines: ["applicant@example.invalid | 555-0100 | Testville, Nowhere", "https://example.invalid/in/t"],
    });
  });
  test("resumeHeader: no name, no header (the offer says to add one)", () => {
    expect(resumeHeader(null)).toBeNull();
    expect(resumeHeader(bankWith({ email: "applicant@example.invalid" }))).toBeNull();
    expect(resumeHeader(bankWith({ first: "Test" }))).toEqual({ name: "Test", contact_lines: [] });
  });

  const header = { name: "Test Applicant", contact_lines: [] };
  test("buildResumeRequest: the job, bounded, and the header beside it", () => {
    const r = buildResumeRequest({ company: " Acme\n AI ", title: "Engineer", description: "  Build tools.  " }, header) as { job: Record<string, string>; header: unknown };
    expect(r.job).toEqual({ company: "Acme AI", title: "Engineer", description: "Build tools." });
    expect(r.header).toEqual(header);
    const long = buildResumeRequest({ company: "A", title: "T", description: "d".repeat(500_000) }, header) as { job: { description: string } };
    expect(long.job.description.length).toBe(100_000);
    expect(JSON.stringify(long).length).toBeLessThan(128 * 1024);
  });
  test("buildResumeRequest: a page with no description is refused", () => {
    expect(() => buildResumeRequest({ company: "A", title: "T", description: " " }, header)).toThrow(/no job description/);
  });

  test("parseBuiltResume: a checked PDF", () => {
    const r = parseBuiltResume({ pdf: toBase64(PDF), name: "Resume_Acme.pdf", bytes: PDF.length, pages: 2 });
    expect(r.ok && r.name === "Resume_Acme.pdf" && r.bytes.length === PDF.length).toBe(true);
  });
  test("parseBuiltResume: vani's own reason for its codes, the shared help for the agent's", () => {
    const own = parseBuiltResume({ error: "resume: devprism.binary is not set in Vani's config", code: "config" });
    expect(!own.ok && own.detail.includes("devprism.binary")).toBe(true);
    const agent = parseBuiltResume({ error: "x", code: "agent" });
    expect(!agent.ok && agent.detail.includes("agent isn't running")).toBe(true);
    for (const bad of [null, {}, { pdf: "%%%", name: "a.pdf", bytes: 1 }, { pdf: toBase64(PDF), name: "a.exe", bytes: PDF.length },
      { pdf: toBase64(new TextEncoder().encode("<html>")), name: "a.pdf", bytes: 6 }, { pdf: toBase64(PDF), name: "a.pdf", bytes: 1 }]) {
      const r = parseBuiltResume(bad);
      expect(r.ok).toBe(false);
    }
  });
});

describe("parseResumeInfo", () => {
  test("what the popup shows: the bound file's name and when it was chosen, or none", () => {
    expect(parseResumeInfo({ found: true, name: "Resume_Test.pdf", bytes: 10, pickedAt: "2026-10-19T12:00:00Z" }))
      .toEqual({ found: true, name: "Resume_Test.pdf", bytes: 10, pickedAt: "2026-10-19T12:00:00Z" });
    expect(parseResumeInfo({ found: false })).toEqual({ found: false });
    expect(parseResumeInfo({ error: "x", code: "unreachable" })).toEqual({ error: "x" });
    expect(parseResumeInfo(null)).toEqual({ error: expect.any(String) });
  });
});
