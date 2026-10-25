// The offer Fill makes on a form whose resume box it left empty because no resume is
// chosen for this job: build one with rkb in the owner's template and attach it, or choose a PDF. Nothing
// happens without a click. Either way the PDF is then bound to the job (resumePut), so
// the next Fill on this job attaches it without asking, and it is attached now, outlined
// yellow. A build asks Vani first whether it tracks this posting: a tracked one is built as the next
// saved version of that job's resume (job kind "resumeJob", by key), any other page is built
// statelessly from the page's text (job kind "resume") and the bar says nothing was saved as a version.
// No DOM and no chrome.* here: content.ts draws the bar and supplies the pieces.
import type { JobInfo } from "./draft";
import type { FillOutcome } from "./fill";
import { MAX_RESUME_BYTES, type ResumeFetch, buildResumeJobRequest, buildResumeRequest, checkResumeName, isPdf, parseBuiltResume, toBase64 } from "./resume";

export type OfferAction = "build" | "choose" | "view" | "dismiss";

/** A picked file: bytes is null when it was over the cap and so never read. */
export interface PickedFile { name: string; size: number; bytes: Uint8Array | null }

export interface OfferUi {
  /** Show the question (or a failure, with the same choices to try again). */
  ask(text: string, actions: OfferAction[]): void;
  /** Working: the buttons are disabled and the text says what is happening. */
  busy(text: string): void;
  /** The outcome; the bar goes away by itself. A built resume does not use this: View has to stay. */
  done(text: string): void;
  hide(): void;
}

/** An agent job to run: the stateless build of a page, or the keyed build of a tracked posting. */
export interface BuildJob { kind: "resume" | "resumeJob"; request: object }

/** Which tracked posting this page is: its key, none (`key: null`, `ambiguous` when two postings claim it), or why Vani could not say. */
export type TrackedJob = { key: string | null; ambiguous?: boolean } | { error: string };

export interface OfferDeps {
  ui: OfferUi;
  job(): JobInfo;
  /** Ask Vani which tracked posting this page is. Never throws. */
  find(): Promise<TrackedJob>;
  /** Run the agent job; resolves with vani's reply or a failure object. Never throws. */
  build(job: BuildJob, onTick: (elapsedMs: number) => void): Promise<unknown>;
  /** Let the owner pick a PDF (a file input clicked from the button press); null on Cancel. */
  pick(): Promise<PickedFile | null>;
  /** Bind the PDF to this job page in the Mac app. */
  save(name: string, pdf: string): Promise<{ error?: string }>;
  /** Attach it to the page's resume box now. */
  attach(r: Extract<ResumeFetch, { ok: true }>): Promise<FillOutcome[]>;
  /**
   * Open this PDF in Preview. The bytes are the ones just built or chosen, so this works
   * even when saving them for the job failed. `error` means Preview did not open and the
   * page is showing the PDF itself.
   */
  open(r: Extract<ResumeFetch, { ok: true }>): Promise<{ error?: string }>;
}

export const OFFER_TEXT =
  "No resume is chosen for this job, so the resume box is empty. Build one now from your knowledge base " +
  "tailored to this posting in your own resume template (check it before you submit), or choose a PDF?";

export function createResumeOffer(d: OfferDeps) {
  let dismissed = false;
  let working = false;
  let ready: Extract<ResumeFetch, { ok: true }> | null = null;

  /** `kept` closes the bar's line with where the build went: a saved version, or the reason none was saved. */
  async function finish(r: ResumeFetch, what: string, kept = ""): Promise<void> {
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
    ready = r;
    const filled = out.find(o => o.filled);
    if (filled) {
      d.ui.ask(`Attached ${r.name} (yellow).${kept} Open it and check it before you submit.${saved}`, ["view", "dismiss"]);
      return;
    }
    const miss = out.find(o => !o.filled);
    const why = miss?.detail ?? (miss?.reason === "has_value" ? "the resume box already holds a file"
      : miss?.reason === "detached" || !miss ? "the resume box is gone from the page" : `the resume box was left as it was (${miss.reason})`);
    d.ui.ask(`${r.name} was not attached: ${why}.${kept}${saved}`, ["view", "choose", "dismiss"]);
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
        d.ui.busy("Checking whether Vani tracks this job…");
        let found: TrackedJob;
        try { found = await d.find(); } catch (e) { found = { error: e instanceof Error ? e.message : String(e) }; }
        const key = "error" in found ? null : found.key;
        // A check that could not run is not "untracked": the build still goes statelessly, and the bar says the check failed.
        const notSaved = "error" in found
          ? ` Vani could not tell whether it tracks this posting (${found.error.slice(0, 120)}), so this resume was not saved as a version.`
          : found.ambiguous
            ? " More than one posting Vani tracks claims this page, so this resume was not saved as a version."
            : " Vani does not track this posting, so this resume was not saved as a version.";
        let job: BuildJob;
        if (key) {
          job = { kind: "resumeJob", request: buildResumeJobRequest(key) };
        } else {
          try { job = { kind: "resume", request: buildResumeRequest(d.job()) }; } catch (e) {
            d.ui.ask(`Cannot build one here: ${e instanceof Error ? e.message : String(e)}.${notSaved}`, ["choose", "dismiss"]);
            return;
          }
        }
        const what = key ? "a version for this tracked job (saved in Vani)" : "a resume for this job (not saved as a version)";
        d.ui.busy(`Building ${what} in your template…`);
        const reply = await d.build(job, ms => d.ui.busy(`Building ${what} in your template… ${Math.round(ms / 1000)} s`));
        const built = parseBuiltResume(reply);
        const kept = !key ? notSaved : ` Saved in Vani as ${built.ok && built.version ? `v${built.version}` : "a version"} of this job's resume.`;
        await finish(built, "Building the resume", kept);
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

    async view(): Promise<void> {
      if (working || !ready) return;
      working = true;
      const file = ready;
      d.ui.busy(`Opening ${file.name} in Preview…`);
      try {
        const o = await d.open(file);
        if (o.error) d.ui.ask(`Preview did not open ${file.name} (${o.error}). It is shown on this page instead. If the preview is blank, this page blocked it. Check it before you submit.`, ["view", "dismiss"]);
        else d.ui.ask(`Opened ${file.name} in Preview. Check it before you submit.`, ["view", "dismiss"]);
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
