// The resume attach rules that need no DOM: which file input is a resume, how
// the Mac app's resumeGet/resumePut replies are read, and the size and type checks
// the bytes pass before they reach a page. No personal data: names are neutral.
import { describe, expect, test } from "bun:test";
import {
  MAX_RESUME_BYTES, RESUME_JOB_TIMEOUT_MS, RESUME_KEYED_TIMEOUT_MS, buildResumeJobRequest, buildResumeRequest, checkResumeName, fromBase64, isPdf,
  isResumeField, openHeldResume, parseBuiltResume, parseResumeGet, parseResumeInfo, parseResumeOpen, resumeKey, resumeOp, runResume, toBase64,
} from "../src/resume";
import type { FieldInfo } from "../src/types";

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

describe("parseResumeOpen", () => {
  test("an opened file is its name and size, never the PDF", () => {
    expect(parseResumeOpen({ ok: true, name: "Resume_Acme.pdf", bytes: 1200 })).toEqual({ ok: true, name: "Resume_Acme.pdf", bytes: 1200 });
    expect(parseResumeOpen({ ok: true, name: " Resume_Acme.pdf ", bytes: 1200, pdf: "AAAA" })).toEqual({ ok: true, name: "Resume_Acme.pdf", bytes: 1200 });
  });
  test("nothing saved is not an error", () => {
    expect(parseResumeOpen({ found: false })).toEqual({ found: false });
    expect(parseResumeOpen({ found: false, changed: true })).toEqual({ found: false, changed: true });
    expect(parseResumeOpen({ found: false, several: true, pdf: "SECRET" })).toEqual({ found: false, several: true });
  });
  test("a missing reply, a bad name, a bad size and a huge error are refused", () => {
    for (const c of [null, undefined, "ok", 1, {}, { ok: true }, { ok: true, name: "../x.pdf", bytes: 10 },
      { ok: true, name: "a.pdf", bytes: 0 }, { ok: true, name: "a.pdf", bytes: MAX_RESUME_BYTES + 1 },
      { ok: true, name: "a.pdf", bytes: 1.5 }, { ok: true, name: "a.docx", bytes: 10 }]) {
      const r = parseResumeOpen(c);
      expect("error" in r && r.error.length > 0 && r.error.length <= 200).toBe(true);
    }
    const huge = parseResumeOpen({ error: "e".repeat(10_000), code: "io" });
    expect("error" in huge && huge.error.length <= 200).toBe(true);
  });
});

describe("openHeldResume", () => {
  const file = { name: "Resume_Acme.pdf", bytes: PDF };
  function preview() {
    return { closed: 0, shown: 0, close() { this.closed++; }, show() { this.shown++; } };
  }

  test("a Preview that opens closes a fallback preview and does not draw another", async () => {
    const ui = preview();
    const sent: unknown[] = [];
    const r = await openHeldResume(file, async msg => {
      sent.push(msg);
      return { ok: true, name: file.name, bytes: file.bytes.length, pdf: "SECRET" };
    }, ui);
    expect(r).toEqual({});
    expect(ui.closed).toBe(1);
    expect(ui.shown).toBe(0);
    expect(sent).toEqual([{ type: "resumeShow", name: file.name, pdf: toBase64(PDF) }]);
  });

  test("a refusal, a missing file, or a throw shows the page's own copy and leaves any open preview to be replaced", async () => {
    for (const reply of [{ error: "the Vani Apply Mac app isn't reachable" }, { found: false }, null]) {
      const ui = preview();
      const r = await openHeldResume(file, async () => reply, ui);
      expect(r.error && r.error.length > 0).toBe(true);
      expect(ui.closed).toBe(0);
      expect(ui.shown).toBe(1);
    }
    const ui = preview();
    const r = await openHeldResume(file, async () => { throw new Error("the port closed"); }, ui);
    expect(r).toEqual({ error: "the port closed" });
    expect(ui.shown).toBe(1);
    expect(ui.closed).toBe(0);
  });
});

describe("resumeOp", () => {
  const url = "https://job-boards.greenhouse.io/acme/jobs/123?gh_src=x";
  const key = "https://boards.greenhouse.io/acme/jobs/123";
  test("reads, info and delete go out keyed by the page key only", () => {
    expect(resumeOp({ type: "resumeGet", url })).toEqual({ op: "resumeGet", key });
    expect(resumeOp({ type: "resumeInfo", url })).toEqual({ op: "resumeInfo", key });
    expect(resumeOp({ type: "resumeDelete", url })).toEqual({ op: "resumeDelete", key });
    expect(resumeOp({ type: "resumeOpen", url })).toEqual({ op: "resumeOpen", key });
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

describe("resumeKey on Workday", () => {
  const H = "https://utaustin.wd1.myworkdayjobs.com";
  test("a resume chosen on the posting is the one Fill finds on the apply page, on any route", () => {
    const k = resumeKey(`${H}/UTstaff/job/AUSTIN-TX/FDE_R_1`);
    expect(k).toBe(`${H}/UTstaff/job/FDE_R_1`);
    expect(resumeKey(`${H}/en-US/UTstaff/details/FDE_R_1?q=x`)).toBe(k);
    expect(resumeKey(`${H}/en-US/UTstaff/job/AUSTIN%2C-TX/FDE_R_1/apply/applyManually`)).toBe(k);
  });
});

describe("runResume: the canonical key, then the key a resume was stored under before it", () => {
  const H = "https://utaustin.wd1.myworkdayjobs.com";
  const canon = `${H}/UTstaff/job/FDE_R_1`;
  const oldDirect = `${H}/UTstaff/job/AUSTIN-TX/FDE_R_1`;
  const oldApply = `${H}/en-US/UTstaff/job/AUSTIN%2C-TX/FDE_R_1/apply/applyManually`;
  const B64 = toBase64(PDF);

  /** A Mac app that keeps bindings by key; `broken` makes every call fail like an unreachable app. */
  function mac(bound: Record<string, string> = {}, broken = false) {
    const calls: { op: string; key: string }[] = [];
    const native = async (op: object): Promise<unknown> => {
      const o = op as { op: string; key: string; pdf?: string };
      calls.push({ op: o.op, key: o.key });
      if (broken) return { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" };
      if (o.op === "resumePut") { bound[o.key] = o.pdf!; return { ok: true, key: o.key }; }
      if (o.op === "resumeDelete") { const had = o.key in bound; delete bound[o.key]; return { ok: true, removed: had }; }
      return o.key in bound ? { found: true, name: "r.pdf", bytes: 1, pickedAt: "", pdf: bound[o.key] } : { found: false };
    };
    return { bound, calls, native };
  }

  test("a resume bound under the canonical key is found on the first call", async () => {
    const m = mac({ [canon]: B64 });
    expect(await runResume({ type: "resumeGet", url: oldApply }, m.native)).toMatchObject({ found: true });
    expect(m.calls).toEqual([{ op: "resumeGet", key: canon }]);
  });

  test("a resume stored before the change under this page's own old key is still found (and not for another route)", async () => {
    const m = mac({ [oldApply.replace(/\/en-US/, "")]: B64 });
    expect(await runResume({ type: "resumeInfo", url: oldDirect }, m.native)).toEqual({ found: false });
    const n = mac({ [oldDirect]: B64 });
    expect(await runResume({ type: "resumeInfo", url: oldDirect }, n.native)).toMatchObject({ found: true });
    expect(n.calls).toEqual([{ op: "resumeInfo", key: canon }, { op: "resumeInfo", key: oldDirect }]);
  });

  test("nothing bound anywhere is not found, and costs no more than the two lookups", async () => {
    const m = mac();
    expect(await runResume({ type: "resumeGet", url: oldDirect }, m.native)).toEqual({ found: false });
    expect(m.calls.length).toBe(2);
  });

  test("opening falls back to the old key the same way a read does", async () => {
    const m = mac({ [oldDirect]: B64 });
    expect(await runResume({ type: "resumeOpen", url: oldDirect }, m.native)).toMatchObject({ found: true });
    expect(m.calls).toEqual([{ op: "resumeOpen", key: canon }, { op: "resumeOpen", key: oldDirect }]);
  });

  test("a page whose key is unchanged is looked up once", async () => {
    const m = mac();
    await runResume({ type: "resumeGet", url: "https://boards.greenhouse.io/acme/jobs/1" }, m.native);
    expect(m.calls.length).toBe(1);
  });

  test("an unreachable app is an error, never 'not found', and is not retried under the old key", async () => {
    const m = mac({}, true);
    expect(await runResume({ type: "resumeGet", url: oldDirect }, m.native)).toMatchObject({ code: "unreachable" });
    expect(m.calls.length).toBe(1);
  });

  test("when the old-key lookup is the one that fails, the failure is returned, not 'not found'", async () => {
    let n = 0;
    const r = await runResume({ type: "resumeGet", url: oldDirect }, async () => (n++ === 0 ? { found: false } : { error: "io", code: "io" }));
    expect(r).toEqual({ error: "io", code: "io" });
  });

  test("Remove takes the old binding too, or the fallback would bring it back", async () => {
    const m = mac({ [canon]: B64, [oldDirect]: B64 });
    expect(await runResume({ type: "resumeDelete", url: oldDirect }, m.native)).toEqual({ ok: true, removed: true });
    expect(m.bound).toEqual({});
    expect(await runResume({ type: "resumeGet", url: oldDirect }, m.native)).toEqual({ found: false });
    const none = mac();
    expect(await runResume({ type: "resumeDelete", url: oldDirect }, none.native)).toEqual({ ok: true, removed: false });
  });

  test("a delete that fails on either key is an error", async () => {
    const r = await runResume({ type: "resumeDelete", url: oldDirect }, async () => ({ error: "io", code: "io" }));
    expect(r).toEqual({ error: "io", code: "io" });
  });

  test("a put binds the canonical key and retires this page's old binding, so one job holds one slot", async () => {
    const m = mac({ [oldDirect]: "OLD=" });
    const r = await runResume({ type: "resumePut", url: oldDirect, name: "r.pdf", pdf: B64 }, m.native);
    expect(r).toMatchObject({ ok: true });
    expect(m.bound).toEqual({ [canon]: B64 });
  });

  test("a put the Mac app refused leaves the old binding alone", async () => {
    const m = mac({ [oldDirect]: "OLD=" });
    const r = await runResume({ type: "resumePut", url: oldDirect, name: "r.pdf", pdf: B64 }, async op => {
      m.calls.push({ op: (op as { op: string }).op, key: "" });
      return { error: "full", code: "full" };
    });
    expect(r).toEqual({ error: "full", code: "full" });
    expect(m.bound).toEqual({ [oldDirect]: "OLD=" });
    expect(m.calls.length).toBe(1);
  });

  test("a bad request is refused before any call", async () => {
    const m = mac();
    expect(await runResume({ type: "resumeGet", url: "chrome://extensions" }, m.native)).toMatchObject({ code: "bad_request" });
    expect(await runResume({ type: "resumePut", url: oldDirect, name: "cv.docx", pdf: B64 }, m.native)).toMatchObject({ code: "bad_request" });
    expect(m.calls).toEqual([]);
  });
});

describe("building one", () => {
  test("buildResumeRequest: the job, bounded, and nothing of the owner beside it", () => {
    const r = buildResumeRequest({ company: " Acme\n AI ", title: "Engineer", description: "  Build tools.  " }) as { job: Record<string, string> };
    expect(r).toEqual({ job: { company: "Acme AI", title: "Engineer", description: "Build tools." } });
    expect("header" in r).toBe(false);
    const long = buildResumeRequest({ company: "A", title: "T", description: "d".repeat(500_000) }) as { job: { description: string } };
    expect(long.job.description.length).toBe(100_000);
    expect(JSON.stringify(long).length).toBeLessThan(128 * 1024);
  });
  test("buildResumeRequest: a page with no description is refused", () => {
    expect(() => buildResumeRequest({ company: "A", title: "T", description: " " })).toThrow(/no job description/);
  });
  test("buildResumeJobRequest: the posting's key and nothing else", () => {
    expect(buildResumeJobRequest("greenhouse:acme:4012")).toEqual({ key: "greenhouse:acme:4012" });
  });
  test("the keyed job outlasts the agent's build and use (375 s + 25 s), the stateless one its 375 s", () => {
    expect(RESUME_KEYED_TIMEOUT_MS).toBeGreaterThan((375 + 25) * 1000);
    expect(RESUME_JOB_TIMEOUT_MS).toBeGreaterThan(375 * 1000);
    expect(RESUME_KEYED_TIMEOUT_MS).toBeGreaterThan(RESUME_JOB_TIMEOUT_MS);
  });

  test("parseBuiltResume: a checked PDF", () => {
    const r = parseBuiltResume({ pdf: toBase64(PDF), name: "Resume_Acme.pdf", bytes: PDF.length, pages: 2 });
    expect(r.ok && r.name === "Resume_Acme.pdf" && r.bytes.length === PDF.length && r.version === undefined).toBe(true);
  });
  test("parseBuiltResume: a keyed build carries its version; a bad version is dropped, not trusted", () => {
    const good = parseBuiltResume({ pdf: toBase64(PDF), name: "Resume_Acme.pdf", bytes: PDF.length, key: "greenhouse:acme:1", version: 3 });
    expect(good.ok && good.version).toBe(3);
    for (const v of [0, -1, 1.5, "3", null, Number.MAX_SAFE_INTEGER + 2]) {
      const r = parseBuiltResume({ pdf: toBase64(PDF), name: "Resume_Acme.pdf", bytes: PDF.length, version: v });
      expect(r.ok && r.version === undefined).toBe(true);
    }
  });
  test("parseBuiltResume: the codes Resume Studio added are worded, and vani's own text stays where it says more", () => {
    const help = (code: string) => { const r = parseBuiltResume({ error: "vani's own message", code }); return r.ok ? "" : r.detail; };
    expect(help("busy")).toMatch(/still working on an earlier request/);
    expect(help("kb_changed")).toMatch(/knowledge base changed while the resume was being built/);
    expect(help("unchecked")).toMatch(/could not check the resume/);
    expect(help("stale")).toMatch(/changed in your knowledge base after it was built/);
    // vani names the reason (no_model) or the bullet (ineligible) itself, so its message is shown.
    expect(help("no_model")).toBe("vani's own message");
    expect(help("ineligible")).toBe("vani's own message");
    expect(help("not_usable")).toBe("vani's own message");
    // A code the extension does not know falls back to vani's message.
    expect(help("something_new")).toBe("vani's own message");
    expect(help("")).toBe("vani's own message");
  });
  test("no_name is no longer a code the extension words: nothing sends a header for vani to refuse", () => {
    const r = parseBuiltResume({ error: "vani's own message", code: "no_name" });
    expect(!r.ok && r.detail).toBe("vani's own message");
  });
  test("parseBuiltResume: a saved version that could not be used is named, within the detail bound", () => {
    const r = parseBuiltResume({ error: "the built version's fit did not pass", code: "fit", version: 4 });
    expect(!r.ok && r.detail).toBe("the built version's fit did not pass (saved as v4 in Vani)");
    const long = parseBuiltResume({ error: "x".repeat(500), code: "fit", version: 12 });
    expect(!long.ok && long.detail.length <= 200 && long.detail.endsWith("(saved as v12 in Vani)")).toBe(true);
    const none = parseBuiltResume({ error: "boom", code: "fit", version: 0 });
    expect(!none.ok && none.detail).toBe("boom");
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
    expect(parseResumeInfo({ found: true, name: "Resume_Test.pdf", bytes: 10, pickedAt: "2026-10-19T12:00:00Z", key: "https://boards.greenhouse.io/acme/jobs/1" }))
      .toEqual({ found: true, name: "Resume_Test.pdf", bytes: 10, pickedAt: "2026-10-19T12:00:00Z", key: "https://boards.greenhouse.io/acme/jobs/1" });
    expect(parseResumeInfo({ found: true, name: "Resume_Test.pdf", bytes: 10, pickedAt: "", key: "not a posting" }))
      .toEqual({ found: true, name: "Resume_Test.pdf", bytes: 10, pickedAt: "" });
    expect(parseResumeInfo({ found: false })).toEqual({ found: false });
    expect(parseResumeInfo({ found: false, several: true })).toEqual({ found: false, several: true });
    expect(parseResumeInfo({ error: "x", code: "unreachable" })).toEqual({ error: "x" });
    expect(parseResumeInfo(null)).toEqual({ error: expect.any(String) });
  });
});
