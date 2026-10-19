// The offer Fill makes on a form whose resume box it left empty because no resume is
// chosen for this job: build one with DevPrism and attach it, or choose a PDF. Nothing
// happens without a click. Either way the PDF is then bound to the job (resumePut), so
// the next Fill on this job attaches it without asking, and it is attached now, outlined
// yellow. No DOM and no chrome.* here: content.ts draws the bar and supplies the pieces.
import type { Bank } from "./types";
import type { JobInfo } from "./draft";
import type { FillOutcome } from "./fill";
import { MAX_RESUME_BYTES, type ResumeFetch, buildResumeRequest, checkResumeName, isPdf, parseBuiltResume, resumeHeader, toBase64 } from "./resume";

export type OfferAction = "build" | "choose" | "dismiss";

/** A picked file: bytes is null when it was over the cap and so never read. */
export interface PickedFile { name: string; size: number; bytes: Uint8Array | null }

export interface OfferUi {
  /** Show the question (or a failure, with the same choices to try again). */
  ask(text: string, actions: OfferAction[]): void;
  /** Working: the buttons are disabled and the text says what is happening. */
  busy(text: string): void;
  /** The outcome; the bar goes away by itself. */
  done(text: string): void;
  hide(): void;
}

export interface OfferDeps {
  ui: OfferUi;
  bank(): Promise<Bank | null>;
  job(): JobInfo;
  /** Run the agent's resume job on this request; resolves with vani's reply or a failure object. Never throws. */
  build(request: object, onTick: (elapsedMs: number) => void): Promise<unknown>;
  /** Let the owner pick a PDF (a file input clicked from the button press); null on Cancel. */
  pick(): Promise<PickedFile | null>;
  /** Bind the PDF to this job page in the Mac app. */
  save(name: string, pdf: string): Promise<{ error?: string }>;
  /** Attach it to the page's resume box now. */
  attach(r: ResumeFetch): Promise<FillOutcome[]>;
}

export const OFFER_TEXT =
  "No resume is chosen for this job, so the resume box is empty. Build one now from your knowledge base " +
  "(DevPrism's plain layout, not one of your LaTeX resumes: check it), or choose a PDF?";

export function createResumeOffer(d: OfferDeps) {
  let dismissed = false;
  let working = false;

  async function finish(r: ResumeFetch, what: string): Promise<void> {
    if (!r.ok) { d.ui.ask(`${what} failed: ${r.detail}`, ["build", "choose", "dismiss"]); return; }
    d.ui.busy(`Attaching ${r.name}…`);
    let saved = "";
    try {
      const s = await d.save(r.name, toBase64(r.bytes));
      if (s.error) saved = ` It was not saved for this job (${s.error}), so the next Fill will ask again.`;
    } catch (e) {
      saved = ` It was not saved for this job (${e instanceof Error ? e.message : String(e)}).`;
    }
    const out = await d.attach(r);
    const filled = out.find(o => o.filled);
    if (filled) {
      d.ui.done(`Attached ${r.name} (yellow): open it and check it before you submit.${saved}`);
      return;
    }
    const why = out.find(o => o.detail)?.detail ?? "the resume box is gone from the page";
    d.ui.ask(`${r.name} was not attached: ${why}.${saved}`, ["choose", "dismiss"]);
  }

  return {
    /** After a Fill: offer when a resume box was left empty for want of a resume. */
    offerAfterFill(outcomes: FillOutcome[]): boolean {
      if (dismissed || working || !outcomes.some(o => o.reason === "no_resume")) return false;
      d.ui.ask(OFFER_TEXT, ["build", "choose", "dismiss"]);
      return true;
    },

    async build(): Promise<void> {
      if (working) return;
      working = true;
      try {
        const header = resumeHeader(await d.bank());
        if (!header) { d.ui.ask("Add your first and last name under Your basics in the Vani Apply popup first: the resume header uses them.", ["choose", "dismiss"]); return; }
        let request: object;
        try { request = buildResumeRequest(d.job(), header); } catch (e) {
          d.ui.ask(`Cannot build one here: ${e instanceof Error ? e.message : String(e)}.`, ["choose", "dismiss"]);
          return;
        }
        d.ui.busy("Building a resume for this job with DevPrism…");
        const reply = await d.build(request, ms => d.ui.busy(`Building a resume for this job with DevPrism… ${Math.round(ms / 1000)} s`));
        await finish(parseBuiltResume(reply), "Building the resume");
      } finally {
        working = false;
      }
    },

    async choose(): Promise<void> {
      if (working) return;
      working = true;
      try {
        const f = await d.pick();
        if (!f) return;
        const name = checkResumeName(f.name);
        let r: ResumeFetch;
        if (!name) r = { ok: false, reason: "resume_unavailable", detail: "choose a .pdf file" };
        else if (!f.bytes || f.size > MAX_RESUME_BYTES || f.bytes.length > MAX_RESUME_BYTES) r = { ok: false, reason: "resume_unavailable", detail: `the file is over ${MAX_RESUME_BYTES / 1024} KB` };
        else if (!isPdf(f.bytes)) r = { ok: false, reason: "resume_unavailable", detail: "that file is not a PDF" };
        else r = { ok: true, name, bytes: f.bytes, pickedAt: new Date().toISOString() };
        await finish(r, "Choosing the PDF");
      } finally {
        working = false;
      }
    },

    dismiss(): void {
      dismissed = true;
      d.ui.hide();
    },
  };
}
