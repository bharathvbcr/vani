// The build-or-choose offer on a form whose resume box Fill left empty. Fakes for the
// bar, the agent job, the file picker, the Mac app and the page. Neutral test values only.
import { describe, expect, test } from "bun:test";
import { OFFER_TEXT, type OfferAction, type OfferDeps, createResumeOffer } from "../src/resumeoffer";
import type { FillOutcome } from "../src/fill";
import { type ResumeFetch, toBase64 } from "../src/resume";
import { emptyBank } from "../src/types";
import { mergeCapture } from "../src/bank";
import { BASICS_HOST, basicsFields } from "../src/basics";

const PDF = new TextEncoder().encode("%PDF-1.4\n%neutral test file\n%%EOF\n");
const named = mergeCapture(emptyBank(), basicsFields({ first: "Test", last: "Applicant", email: "applicant@example.invalid" }), BASICS_HOST, new Date()).bank;
const noResume: FillOutcome[] = [{ label: "Resume/CV", filled: false, reason: "no_resume", detail: "no resume chosen for this job" }];

function harness(over: Partial<OfferDeps> = {}) {
  const log: string[] = [];
  const asked: OfferAction[][] = [];
  const saves: { name: string; pdf: string }[] = [];
  const attached: ResumeFetch[] = [];
  const requests: object[] = [];
  const d: OfferDeps = {
    ui: {
      ask: (t, a) => { log.push(`ask: ${t}`); asked.push(a); },
      busy: t => log.push(`busy: ${t}`),
      done: t => log.push(`done: ${t}`),
      hide: () => log.push("hide"),
    },
    bank: async () => named,
    job: () => ({ company: "Acme AI", title: "Engineer", description: "Build research tools." }),
    build: async (req, tick) => { requests.push(req); tick(3000); return { pdf: toBase64(PDF), name: "Resume_Acme_AI.pdf", bytes: PDF.length, pages: 2 }; },
    pick: async () => ({ name: "Resume_Chosen.pdf", size: PDF.length, bytes: PDF }),
    save: async (name, pdf) => { saves.push({ name, pdf }); return {}; },
    attach: async r => { attached.push(r); return [{ label: "Resume/CV", filled: true, review: true }]; },
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
    expect(OFFER_TEXT).toContain("not one of your LaTeX resumes");
  });

  test("Not now: gone, and not offered again on this page", () => {
    const h = harness();
    h.offer.offerAfterFill(noResume);
    h.offer.dismiss();
    expect(h.offer.offerAfterFill(noResume)).toBe(false);
    expect(h.log.at(-1)).toBe("hide");
  });

  test("Build: the job and the header go to the agent, the PDF is bound to the job and attached", async () => {
    const h = harness();
    await h.offer.build();
    expect(h.requests).toEqual([{
      job: { company: "Acme AI", title: "Engineer", description: "Build research tools." },
      header: { name: "Test Applicant", contact_lines: ["applicant@example.invalid"] },
    }]);
    expect(h.saves).toEqual([{ name: "Resume_Acme_AI.pdf", pdf: toBase64(PDF) }]);
    expect(h.attached.length === 1 && h.attached[0]!.ok && h.attached[0]!.name === "Resume_Acme_AI.pdf").toBe(true);
    expect(h.log.some(l => l.startsWith("busy: Building a resume") && l.endsWith("3 s"))).toBe(true);
    expect(h.log.at(-1)).toMatch(/^done: Attached Resume_Acme_AI\.pdf \(yellow\)/);
  });

  test("Build without a name in Your basics: says so, never starts a job", async () => {
    const h = harness({ bank: async () => emptyBank() });
    await h.offer.build();
    expect(h.requests.length).toBe(0);
    expect(h.log.at(-1)).toMatch(/Your basics/);
    expect(h.asked.at(-1)).toEqual(["choose", "dismiss"]);
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
    expect(h.log.at(-1)).toMatch(/^done: Attached Resume_Chosen\.pdf/);

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
