// The build-or-choose offer on a form whose resume box Fill left empty. Fakes for the
// bar, the agent job, the file picker, the Mac app and the page. Neutral test values only.
import { describe, expect, test } from "bun:test";
import { type BuildJob, OFFER_TEXT, type OfferAction, type OfferDeps, createResumeOffer } from "../src/resumeoffer";
import type { FillOutcome } from "../src/fill";
import { type ResumeFetch, toBase64 } from "../src/resume";

const PDF = new TextEncoder().encode("%PDF-1.4\n%neutral test file\n%%EOF\n");
const noResume: FillOutcome[] = [{ label: "Resume/CV", filled: false, reason: "no_resume", detail: "no resume chosen for this job" }];

function harness(over: Partial<OfferDeps> = {}) {
  const log: string[] = [];
  const asked: OfferAction[][] = [];
  const saves: { name: string; pdf: string }[] = [];
  const attached: ResumeFetch[] = [];
  const requests: BuildJob[] = [];
  const d: OfferDeps = {
    ui: {
      ask: (t, a) => { log.push(`ask: ${t}`); asked.push(a); },
      busy: t => log.push(`busy: ${t}`),
      done: t => log.push(`done: ${t}`),
      hide: () => log.push("hide"),
    },
    job: () => ({ company: "Acme AI", title: "Engineer", description: "Build research tools." }),
    find: async () => ({ key: null }),
    build: async (req, tick) => { requests.push(req); tick(3000); return { pdf: toBase64(PDF), name: "Resume_Acme_AI.pdf", bytes: PDF.length, pages: 2 }; },
    pick: async () => ({ name: "Resume_Chosen.pdf", size: PDF.length, bytes: PDF }),
    save: async (name, pdf) => { saves.push({ name, pdf }); return {}; },
    attach: async r => { attached.push(r); return [{ label: "Resume/CV", filled: true, review: true }]; },
    open: async () => ({}),
    ...over,
  };
  return { offer: createResumeOffer(d), log, asked, saves, attached, requests };
}

describe("the offer", () => {
  test("appears only after a Fill that left a resume box empty for want of a resume", () => {
    const h = harness();
    expect(h.offer.offerAfterFill([{ label: "Name", filled: true }])).toBe(false);
    expect(h.offer.offerAfterFill([{ label: "Resume/CV", filled: false, reason: "resume_unavailable", detail: "x" }])).toBe(false);
    expect(h.offer.offerAfterFill(noResume)).toBe(true);
    expect(h.log).toEqual([`ask: ${OFFER_TEXT}`]);
    expect(h.asked[0]).toEqual(["build", "choose", "dismiss"]);
    expect(OFFER_TEXT).toContain("your own resume template");
  });

  test("Not now: gone, and not offered again on this page", () => {
    const h = harness();
    h.offer.offerAfterFill(noResume);
    h.offer.dismiss();
    expect(h.offer.offerAfterFill(noResume)).toBe(false);
    expect(h.log.at(-1)).toBe("hide");
  });

  test("Build on an untracked page: the posting goes to the stateless job with no header, the PDF is bound and attached, and the bar says it is not a saved version", async () => {
    const h = harness();
    await h.offer.build();
    expect(h.requests).toEqual([{
      kind: "resume",
      request: { job: { company: "Acme AI", title: "Engineer", description: "Build research tools." } },
    }]);
    expect(JSON.stringify(h.requests)).not.toContain("header");
    expect(h.saves).toEqual([{ name: "Resume_Acme_AI.pdf", pdf: toBase64(PDF) }]);
    expect(h.attached.length === 1 && h.attached[0]!.ok && h.attached[0]!.name === "Resume_Acme_AI.pdf").toBe(true);
    expect(h.log.some(l => l.startsWith("busy: Building a resume for this job (not saved as a version)") && l.endsWith("3 s"))).toBe(true);
    expect(h.log.at(-1)).toMatch(/^ask: Attached Resume_Acme_AI\.pdf \(yellow\)\. Vani does not track this posting, so this resume was not saved as a version\. Open it/);
    expect(h.asked.at(-1)).toEqual(["view", "dismiss"]);
    expect(h.log.some(l => l.startsWith("done:"))).toBe(false);
  });

  test("Build needs no name anywhere: nothing asks for Your basics, and the request never holds one", async () => {
    const h = harness();
    await h.offer.build();
    expect(h.requests.length).toBe(1);
    expect(h.log.some(l => /Your basics/.test(l))).toBe(false);
    expect(JSON.stringify(h.requests)).not.toMatch(/Test|Applicant|contact/);
  });

  test("Build on a tracked page: the key alone goes to the keyed job (no page text), the version is named, the PDF is bound and attached", async () => {
    const keyed = { pdf: toBase64(PDF), name: "Resume_Acme_AI.pdf", bytes: PDF.length, pages: 1, key: "greenhouse:acme:4012", version: 3 };
    const h = harness({ find: async () => ({ key: "greenhouse:acme:4012" }), build: async (j, tick) => { h.requests.push(j); tick(3000); return keyed; } });
    await h.offer.build();
    expect(h.requests).toEqual([{ kind: "resumeJob", request: { key: "greenhouse:acme:4012" } }]);
    expect(h.saves).toEqual([{ name: "Resume_Acme_AI.pdf", pdf: toBase64(PDF) }]);
    expect(h.attached.length).toBe(1);
    expect(h.log.some(l => l.startsWith("busy: Building a version for this tracked job (saved in Vani)") && l.endsWith("3 s"))).toBe(true);
    expect(h.log.at(-1)).toMatch(/^ask: Attached Resume_Acme_AI\.pdf \(yellow\)\. Saved in Vani as v3 of this job's resume\. Open it/);
    expect(h.log.at(-1)).not.toMatch(/not saved as a version/);
  });

  test("a tracked page with no job description on it still builds (the posting's text is Vani's)", async () => {
    const h = harness({ find: async () => ({ key: "greenhouse:acme:4012" }), job: () => ({ company: "", title: "", description: "" }) });
    await h.offer.build();
    expect(h.requests).toEqual([{ kind: "resumeJob", request: { key: "greenhouse:acme:4012" } }]);
  });

  test("a keyed refusal is its reason with the choices again, names a saved version, and attaches nothing", async () => {
    const h = harness({
      find: async () => ({ key: "greenhouse:acme:4012" }),
      build: async () => ({ error: "the built version's fit did not pass", code: "fit", version: 2 }),
    });
    await h.offer.build();
    expect(h.saves.length + h.attached.length).toBe(0);
    expect(h.log.at(-1)).toBe("ask: Building the resume failed: the built version's fit did not pass (saved as v2 in Vani)");
    expect(h.asked.at(-1)).toEqual(["build", "choose", "dismiss"]);
  });

  test("an ambiguous page is not guessed: stateless, and the bar says why it was not saved", async () => {
    const h = harness({ find: async () => ({ key: null, ambiguous: true }) });
    await h.offer.build();
    expect(h.requests[0]!.kind).toBe("resume");
    expect(h.log.at(-1)).toMatch(/More than one posting Vani tracks claims this page, so this resume was not saved as a version/);
  });

  test("when Vani cannot say whether the page is tracked, the build still goes statelessly and the bar says the check failed (not 'untracked')", async () => {
    for (const find of [async () => ({ error: "the Vani Apply Mac app isn't reachable" }), async () => { throw new Error("worker gone"); }]) {
      const h = harness({ find });
      await h.offer.build();
      expect(h.requests.map(r => r.kind)).toEqual(["resume"]);
      expect(h.log.at(-1)).toMatch(/Vani could not tell whether it tracks this posting \(.+\), so this resume was not saved as a version/);
      expect(h.log.at(-1)).not.toMatch(/does not track/);
    }
  });

  test("Build on a page with no description: says so, never starts a job", async () => {
    const h = harness({ job: () => ({ company: "A", title: "T", description: "" }) });
    await h.offer.build();
    expect(h.requests.length).toBe(0);
    expect(h.log.at(-1)).toMatch(/no job description/);
  });

  test("a failed build is a reason with the choices again; nothing saved or attached", async () => {
    for (const reply of [
      { error: "resume: devprism.binary is not set in Vani's config", code: "config" },
      { ok: false, code: "agent", error: "x" },
      { pdf: "%%%", name: "a.pdf", bytes: 1 },
      undefined,
    ]) {
      const h = harness({ build: async () => reply });
      await h.offer.build();
      expect(h.saves.length + h.attached.length).toBe(0);
      expect(h.log.at(-1)).toMatch(/^ask: Building the resume failed: /);
      expect(h.asked.at(-1)).toEqual(["build", "choose", "dismiss"]);
    }
  });

  test("not saved for the job (the app refused): still attached, and it says the next Fill will ask", async () => {
    const h = harness({ save: async () => ({ error: "the resume index is unreadable" }) });
    await h.offer.build();
    expect(h.attached.length).toBe(1);
    expect(h.log.at(-1)).toMatch(/not saved for this job \(the resume index is unreadable\)/);
  });

  test("the page refuses the file: a reason, never 'Attached'", async () => {
    const h = harness({ attach: async () => [{ label: "Resume/CV", filled: false, reason: "refused", detail: "this page did not take the file; attach it by hand" }] });
    await h.offer.build();
    expect(h.log.at(-1)).toMatch(/^ask: Resume_Acme_AI\.pdf was not attached: this page did not take the file/);
  });

  test("a box that kept its file or vanished says which, never a wrong reason", async () => {
    const kept = harness({ attach: async () => [{ label: "Resume/CV", filled: false, reason: "has_value" }] });
    await kept.offer.build();
    expect(kept.log.at(-1)).toMatch(/was not attached: the resume box already holds a file/);
    const gone = harness({ attach: async () => [] });
    await gone.offer.build();
    expect(gone.log.at(-1)).toMatch(/was not attached: the resume box is gone from the page/);
  });

  test("Choose PDF: a checked PDF is bound and attached; Cancel does nothing; a non-PDF is refused", async () => {
    const h = harness();
    await h.offer.choose();
    expect(h.saves[0]?.name).toBe("Resume_Chosen.pdf");
    expect(h.log.at(-1)).toMatch(/^ask: Attached Resume_Chosen\.pdf/);
    expect(h.asked.at(-1)).toEqual(["view", "dismiss"]);

    const c = harness({ pick: async () => null });
    await c.offer.choose();
    expect(c.log.length + c.saves.length).toBe(0);

    for (const f of [{ name: "cv.docx", size: PDF.length, bytes: PDF }, { name: "cv.pdf", size: 6, bytes: new TextEncoder().encode("<html>") },
      { name: "cv.pdf", size: 512 * 1024 + 1, bytes: null }]) {
      const n = harness({ pick: async () => f });
      await n.offer.choose();
      expect(n.saves.length + n.attached.length).toBe(0);
      expect(n.log.at(-1)).toMatch(/^ask: Choosing the PDF failed: /);
    }
  });

  test("View opens the PDF that was just built and leaves the bar up", async () => {
    const opened: string[] = [];
    const h = harness({ open: async r => { opened.push(r.name); return {}; } });
    await h.offer.build();
    await h.offer.view();
    expect(opened).toEqual(["Resume_Acme_AI.pdf"]);
    expect(h.log.some(l => l === "busy: Opening Resume_Acme_AI.pdf in Preview…")).toBe(true);
    expect(h.log.at(-1)).toMatch(/^ask: Opened Resume_Acme_AI\.pdf in Preview/);
    expect(h.asked.at(-1)).toEqual(["view", "dismiss"]);
    expect(h.log.some(l => l === "hide")).toBe(false);
  });

  test("View still opens the PDF when saving it for the job failed", async () => {
    const opened: string[] = [];
    const h = harness({
      save: async () => ({ error: "the resume index is unreadable" }),
      open: async r => { opened.push(r.name); return {}; },
    });
    await h.offer.build();
    await h.offer.view();
    expect(opened).toEqual(["Resume_Acme_AI.pdf"]);
  });

  test("when Preview does not open, the bar says the page is showing the PDF and stays", async () => {
    const h = harness({ open: async () => ({ error: "the Vani Apply Mac app isn't reachable" }) });
    await h.offer.build();
    await h.offer.view();
    expect(h.log.at(-1)).toMatch(/Preview did not open Resume_Acme_AI\.pdf \(the Vani Apply Mac app isn't reachable\)\. It is shown on this page instead\. If the preview is blank, this page blocked it/);
    expect(h.asked.at(-1)).toEqual(["view", "dismiss"]);
  });

  test("a second click while building does not start a second job", async () => {
    let release!: () => void;
    const gate = new Promise<void>(r => { release = r; });
    let starts = 0;
    const h = harness({ build: async () => { starts++; await gate; return { pdf: toBase64(PDF), name: "Resume_A.pdf", bytes: PDF.length }; } });
    const first = h.offer.build();
    await h.offer.build();
    release();
    await first;
    expect(starts).toBe(1);
  });
});
