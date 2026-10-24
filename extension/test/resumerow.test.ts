// The popup's resume row must not open a file for a page it is no longer showing.
// No DOM and no chrome: the elements and the tab lookup are fakes. Names are synthetic.
import { describe, expect, test } from "bun:test";
import { type ResumeRowEls, resumeRow } from "../src/resumerow";
import type { ToBackground } from "../src/messages";
import { resumeKey } from "../src/resume";

const JOB_A = "https://boards.greenhouse.io/acme/jobs/1";
const JOB_B = "https://boards.greenhouse.io/acme/jobs/2";
const H = "https://utaustin.wd1.myworkdayjobs.com";
const SLUG = "Forward-Deployed-Engineer_R_00049483";
const WORKDAY = `${H}/UTstaff/job/${SLUG}`;
const WORKDAY_APPLY = `${H}/en-US/UTstaff/job/AUSTIN%2C-TX/${SLUG}/apply/applyManually`;

interface Fake {
  row: { hidden: boolean };
  name: { textContent: string };
  status: { hidden: boolean; textContent: string };
  pick: { value: string; files: null; onchange: (() => void) | null };
  view: { hidden: boolean; disabled: boolean; onclick: (() => Promise<void>) | null };
  remove: { hidden: boolean; onclick: (() => void) | null };
}

function fake(): Fake {
  return {
    row: { hidden: true },
    name: { textContent: "" },
    status: { hidden: true, textContent: "" },
    pick: { value: "", files: null, onchange: null },
    view: { hidden: true, disabled: false, onclick: null },
    remove: { hidden: true, onclick: null },
  };
}

function info(url: string): unknown {
  if (url === JOB_A || url === WORKDAY || url === WORKDAY_APPLY) {
    return { found: true, name: "Resume_Acme.pdf", bytes: 2048, pickedAt: "2026-10-19T00:00:00Z" };
  }
  if (url === JOB_B) return { found: true, name: "Resume_Other.pdf", bytes: 1024, pickedAt: "2026-10-20T00:00:00Z" };
  return { found: false };
}

async function draw(
  url: string,
  live: () => Promise<string | { url?: string; tabId?: number } | undefined>,
  preview: unknown = { ok: true, name: "Resume_Acme.pdf", bytes: 2048, pdf: "SECRET" },
  tabId?: number,
) {
  const els = fake();
  const sent: ToBackground[] = [];
  await resumeRow(url, els as unknown as ResumeRowEls, async msg => {
    sent.push(msg);
    if (msg.type === "resumeInfo") return info(msg.url);
    if (msg.type === "resumePreview") return preview;
    return {};
  }, live, tabId);
  return { els, sent };
}

describe("resume row View", () => {
  test("opens when the tab is still the same posting, and the status never repeats a PDF", async () => {
    const { els, sent } = await draw(JOB_A, async () => JOB_A);
    expect(els.view.hidden).toBe(false);
    expect(els.name.textContent).toContain("Resume_Acme.pdf");
    await els.view.onclick!();
    expect(sent.map(m => m.type)).toEqual(["resumeInfo", "resumePreview"]);
    expect(els.status.textContent).toBe("Opened Resume_Acme.pdf in Preview");
    expect(els.status.textContent).not.toContain("SECRET");
  });

  test("a Workday apply route is the same posting, so View still opens", async () => {
    const { els, sent } = await draw(WORKDAY, async () => WORKDAY_APPLY);
    await els.view.onclick!();
    expect(sent.map(m => m.type)).toEqual(["resumeInfo", "resumePreview"]);
    expect(els.status.textContent).toBe("Opened Resume_Acme.pdf in Preview");
  });

  test("a tab that moved to another job does not open the resume the row was showing", async () => {
    const { els, sent } = await draw(JOB_A, async () => JOB_B);
    await els.view.onclick!();
    expect(sent.map(m => m.type)).toEqual(["resumeInfo", "resumeInfo"]);
    expect(els.name.textContent).toContain("Resume_Other.pdf");
    expect(els.status.textContent).toBe("This page changed, so that resume was not opened.");
    expect(els.view.hidden).toBe(false);
  });

  test("a tab that left the job page does not open, and says so on the row", async () => {
    const { els, sent } = await draw(JOB_A, async () => "chrome://extensions");
    await els.view.onclick!();
    expect(sent.map(m => m.type)).toEqual(["resumeInfo"]);
    expect(els.view.hidden).toBe(true);
    expect(els.name.textContent).toBe("this page is not a job page");
    expect(els.status.textContent).toBe("This page changed, so that resume was not opened.");
  });

  test("a tab lookup that fails does not open", async () => {
    const { els, sent } = await draw(JOB_A, async () => { throw new Error("tabs unavailable"); });
    await els.view.onclick!();
    expect(sent.map(m => m.type)).toEqual(["resumeInfo"]);
    expect(els.status.textContent).toBe("Could not check which page is open, so the resume was not opened.");
    expect(els.name.textContent).toContain("Resume_Acme.pdf");
  });

  test("View names the posting it is showing and the tab the background re-reads", async () => {
    const row = await draw(JOB_A, async () => ({ url: JOB_A, tabId: 7 }), { ok: true, name: "Resume_Acme.pdf", bytes: 2048 }, 7);
    await row.els.view.onclick!();
    expect(row.sent[0]).toMatchObject({ type: "resumeInfo", url: JOB_A, tabId: 7 });
    expect(row.sent[1]).toEqual({ type: "resumePreview", key: resumeKey(JOB_A), tabId: 7 });
    expect(JSON.stringify(row.sent[1])).not.toContain("SECRET");
  });

  test("the background saying the page changed does not count as opened", async () => {
    const { els, sent } = await draw(JOB_A, async () => JOB_A, { found: false, changed: true });
    await els.view.onclick!();
    expect(sent.map(m => m.type)).toEqual(["resumeInfo", "resumePreview"]);
    expect(els.status.textContent).toBe("This page changed, so that resume was not opened.");
    expect(els.status.textContent).not.toContain("Opened");
  });

  test("more than one resume on the page is said, and View is not offered", async () => {
    const els = fake();
    await resumeRow(JOB_A, els as unknown as ResumeRowEls, async msg => {
      if (msg.type === "resumeInfo") return { found: false, several: true };
      return { ok: true, name: "Resume_Acme.pdf", bytes: 10 };
    }, async () => JOB_A);
    expect(els.view.hidden).toBe(true);
    expect(els.name.textContent).toBe("More than one resume is saved on this page.");
  });

  test("Choose does not bind a file to the posting the row was showing after the tab moved", async () => {
    const { els, sent } = await draw(JOB_A, async () => JOB_B);
    const file = {
      name: "Resume_New.pdf",
      size: 128,
      arrayBuffer: async () => new Uint8Array([0x25, 0x50, 0x44, 0x46, 0x2d]).buffer,
    };
    (els.pick as { files: unknown }).files = [file];
    await els.pick.onchange!();
    expect(sent.filter(m => m.type === "resumePut")).toEqual([]);
    expect(els.name.textContent).toContain("Resume_Other.pdf");
    expect(els.status.textContent).toBe("This page changed, so that resume was not chosen.");
  });

  test("Remove does not unbind the posting the row was showing after the tab moved", async () => {
    const { els, sent } = await draw(JOB_A, async () => JOB_B);
    await els.remove.onclick!();
    expect(sent.filter(m => m.type === "resumeDelete")).toEqual([]);
    expect(els.name.textContent).toContain("Resume_Other.pdf");
    expect(els.status.textContent).toBe("This page changed, so that resume was not removed.");
  });

  test("Remove does not unbind when the tab cannot be re-read", async () => {
    const { els, sent } = await draw(JOB_A, async () => { throw new Error("tabs unavailable"); });
    await els.remove.onclick!();
    expect(sent.filter(m => m.type === "resumeDelete")).toEqual([]);
    expect(els.status.textContent).toBe("Could not check which page is open, so the resume was not removed.");
    expect(els.name.textContent).toContain("Resume_Acme.pdf");
  });

  test("Remove unbinds the posting the row is showing, including one found in a frame", async () => {
    const els = fake();
    const sent: ToBackground[] = [];
    await resumeRow(JOB_A, els as unknown as ResumeRowEls, async msg => {
      sent.push(msg);
      if (msg.type === "resumeInfo") return { found: true, name: "Resume_Other.pdf", bytes: 1024, pickedAt: "2026-10-20T00:00:00Z", key: JOB_B };
      return { ok: true };
    }, async () => JOB_A);
    expect(els.view.hidden).toBe(false);
    await els.remove.onclick!();
    expect(sent.filter(m => m.type === "resumeDelete")).toEqual([{ type: "resumeDelete", url: JOB_B }]);
  });

  test("a resume removed since the row was drawn is no longer offered", async () => {
    const { els } = await draw(JOB_A, async () => JOB_A, { found: false });
    await els.view.onclick!();
    expect(els.view.hidden).toBe(true);
    expect(els.remove.hidden).toBe(true);
    expect(els.name.textContent).toBe("none chosen; Fill leaves the resume box empty");
    expect(els.status.textContent).toBe("No resume is saved for this page");
  });
});
