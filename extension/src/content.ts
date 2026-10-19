// Content script: saves your answers as you commit them (and flushes on submit,
// Next/Continue, Enter, route change, tab hide) and fills a form from saved
// answers when you ask. It never submits a form.
//
// Frames: a frame whose parent is same-origin does nothing itself; the parent's
// script walks into it (extract.ts) and listens on its document, so one frame
// owns each page and nothing is counted twice. A cross-origin frame (an iCIMS
// or Greenhouse embed on a company site) runs its own copy where the manifest
// matches its host or the popup injected it. On a host the manifest does not
// match, the extension cannot enter a cross-origin frame at all.
import type { Bank } from "./types";
import { type FieldRef, scanForm } from "./extract";
import { type FillOutcome, clearMarks, fillAll, fillRef, insertDraft } from "./fill";
import { type Coverage, bucketOf, coverageLine, coverageOf } from "./coverage";
import { type FlushResult, type Tracker, createTracker } from "./tracker";
import { currentText, draftable, suggest } from "./suggest";
import { readJobInfo } from "./jobinfo";
import { type DraftReply, type JobInfo, buildDraftRequest, parseDraftReply } from "./draft";
import { classify } from "./classify";
import { type AppliedFlow, type AppliedUi, createAppliedFlow, offerSubject } from "./applied";
import {
  DRAFT_JOB_TIMEOUT_MS, type FieldSuggestion, SUGGEST_TIMEOUT_MS, type SuggestField,
  buildSuggestRequest, parseSuggestReply, runJob,
} from "./suggestjob";
import type { CaptureReply, DraftResponse, FillReply, ScanReply, ToBackground, ToContent } from "./messages";

declare global {
  interface Window {
    __vaniApply?: boolean;
    /** Set by the frame that owns a page; the popup calls it in every frame to read each frame's own coverage. */
    __vaniScan?: () => Promise<ScanReply>;
  }
}

const MIN_FORM_FIELDS = 3;
const PANEL_LIST = 8;

function send<T>(msg: ToBackground): Promise<T> {
  return chrome.runtime.sendMessage<T>(msg);
}

async function bank(): Promise<Bank | null> {
  try {
    const r = await send<{ bank?: Bank; error?: string }>({ type: "getBank" });
    return r.bank ?? null;
  } catch {
    return null;
  }
}

function disabledHere(b: Bank | null): boolean {
  return !!b && b.settings.disabledHosts.includes(location.hostname);
}

function coveredByParent(): boolean {
  if (window.parent === window) return false;
  try { return !!window.parent.document; } catch { return false; }
}

const NOTHING: CaptureReply = { added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, unchanged: 0 };

// Created only in the frame that owns the page (see the wiring at the bottom),
// so a deferring frame or a second injection never holds listeners.
let tracker: Tracker;

function announce(r: FlushResult): void {
  const saved = r.added + r.updated;
  if (r.failed) toast("Could not save answers: the extension's storage did not accept them");
  else if (saved > 0) toast(`Saved ${saved} answers (${r.reason})` + (r.skippedSecret ? `; ${r.skippedSecret} sensitive fields not saved` : "") + (r.notSent ? `; ${r.notSent} more waiting for the next save` : ""));
  else if (r.reason === "saved") toast(`Nothing new to save (${r.unchanged} answers already saved)`);
}

async function capture(reason: string): Promise<CaptureReply> {
  const r = await tracker.flush(reason, reason === "saved");
  return r ?? NOTHING;
}

// The last scan's unanswered fields, for the live-help rows in the panel.
let lastBank: Bank | null = null;
let lastUnknown: FieldRef[] = [];

async function scan(): Promise<ScanReply> {
  const b = await bank();
  const s = scanForm(document);
  lastBank = b;
  lastUnknown = s.refs.filter(r => bucketOf(r.info, r.manual, b, location.hostname).bucket === "unknown");
  const coverage = coverageOf(s.refs, b, location.hostname, { capped: s.capped, unread: s.unread });
  return { fields: coverage.found, matched: coverage.known, host: location.hostname, disabled: disabledHere(b), coverage };
}

async function fill(overwrite = false, typeahead = false): Promise<FillReply> {
  const b = await bank();
  if (!b || disabledHere(b)) return { outcomes: [] };
  clearMarks();
  const outcomes = await fillAll(scanForm(document).refs, b, location.hostname, { overwrite, typeahead });
  const keys = outcomes.filter(o => o.filled && o.key).map(o => o.key!);
  if (keys.length) void send({ type: "filled", keys });
  const review = outcomes.filter(o => o.filled && o.review).length;
  const left = outcomes.filter(o => !o.filled && o.reason === "no_match").length;
  const manual = outcomes.filter(o => !o.filled && o.reason === "manual").length;
  toast(`Filled ${keys.length} fields` + (review ? `, ${review} to review (yellow)` : "") + (left ? `, ${left} new questions` : "") + (manual ? `, ${manual} to do by hand` : "") + ". Check, then submit yourself.");
  return { outcomes };
}

// --- floating panel -----------------------------------------------------------

let panel: ShadowRoot | null = null;
let statusEl: HTMLElement | null = null;
let listEl: HTMLElement | null = null;

function ensurePanel(): void {
  if (panel || window.top !== window) return; // one panel, in the top frame
  const host = document.createElement("div");
  host.setAttribute("data-vani-panel", "");
  host.style.cssText = "position:fixed;right:16px;bottom:16px;z-index:2147483647;";
  panel = host.attachShadow({ mode: "closed" });
  panel.innerHTML = `
    <style>
      .p{font:13px system-ui,sans-serif;background:#111;color:#eee;border-radius:10px;padding:10px 12px;box-shadow:0 4px 16px #0006;max-width:320px}
      .r{display:flex;gap:8px;align-items:center}
      button{font:inherit;border:0;border-radius:6px;padding:5px 9px;cursor:pointer;background:#2e8b57;color:#fff}
      button.s{background:#444}
      button.g{background:#6b4f9e}
      button:disabled{opacity:.5;cursor:default}
      .x{background:transparent;color:#aaa;padding:2px 6px}
      .t{flex:1}
      .u{margin:8px 0 0;padding-left:16px;max-height:300px;overflow:auto;color:#ddd}
      .u[hidden]{display:none}
      .u li{margin:0 0 8px}
      .h{display:flex;flex-wrap:wrap;gap:4px;margin-top:4px}
      .h button{background:#33475b;padding:3px 7px;max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
      .h button.d{background:#6b4f9e}
      .dr{margin-top:6px;display:flex;flex-direction:column;gap:6px}
      .dr textarea{font:12px system-ui,sans-serif;width:100%;box-sizing:border-box;min-height:110px;background:#1d1d1d;color:#eee;border:1px solid #444;border-radius:6px;padding:6px}
      .m{color:#aaa;font-size:12px}
    </style>
    <div class="p"><div class="r"><span class="t">Vani</span><button class="f">Fill</button><button class="g" title="Ask the decision model about the new free-text questions and have Claude draft the essays (labels and the posting only)">Suggest</button><button class="s">Save answers</button><button class="x" title="Hide">×</button></div>
    <ul class="u" hidden></ul></div>`;
  statusEl = panel.querySelector(".t");
  listEl = panel.querySelector(".u");
  panel.querySelector(".f")!.addEventListener("click", () => { void fill(); });
  panel.querySelector(".g")!.addEventListener("click", () => { void suggestAll(); });
  panel.querySelector(".s")!.addEventListener("click", () => { void capture("saved"); });
  panel.querySelector(".x")!.addEventListener("click", () => host.remove());
  statusEl!.addEventListener("click", () => { if (listEl && listEl.childElementCount > 0) listEl.hidden = !listEl.hidden; });
  document.documentElement.appendChild(host);
}

/** How long a message stays up before a rescan may replace it with the coverage line. */
const HOLD_MS = 60_000;
let heldUntil = 0;

/** Show a message the user must be able to read; rescans (every DOM change) leave it alone for HOLD_MS. */
function toast(text: string): void {
  if (statusEl) statusEl.textContent = text;
  heldUntil = Date.now() + HOLD_MS;
}

// --- live help ------------------------------------------------------------------
// Each question with no saved answer gets: the saved answers to similar
// questions (one click fills it yellow, for review), and for free text a
// Draft button that asks the Mac app for a draft from the posting. Nothing is
// filled without a click, and nothing here ever submits. Page and bank text
// goes into the panel as text, never as markup.

/** A draft request or preview is open: rescans must not redraw the list under the user. */
let helpBusy = false;

const REASON: Record<NonNullable<FillOutcome["reason"]>, string> = {
  no_match: "no saved answer fits",
  has_value: "the field already has text (use Fill with overwrite, or clear it first)",
  no_option: "none of the field's options matches",
  untouchable: "this kind of field is never filled",
  manual: "this widget has to be filled by hand",
  detached: "the field is gone from the page",
};

function btn(cls: string, text: string): HTMLButtonElement {
  const b = document.createElement("button");
  b.className = cls;
  b.textContent = text;
  return b;
}

function preview(s: string, n = 40): string {
  const t = s.replace(/\s+/g, " ").trim();
  return t.length > n ? t.slice(0, n - 1) + "…" : t;
}


function labelOf(ref: FieldRef): string {
  return ref.info.label.trim() || ref.info.name || "(unlabelled)";
}

/** Rescans happen on every DOM change; the posting is read once per page URL. */
let jobCache: { href: string; job: JobInfo } | null = null;
function jobInfo(): JobInfo {
  if (jobCache?.href !== location.href) jobCache = { href: location.href, job: readJobInfo(document) };
  return jobCache.job;
}

async function startDraft(ref: FieldRef, job: JobInfo, li: HTMLElement): Promise<void> {
  if (helpBusy) return;
  // The draft request carries the question, never what is in the field.
  const own = currentText(ref);
  if (own && ref.info.label.toLowerCase().includes(own.toLowerCase())) {
    toast("Draft is only offered for an empty field with a readable question.");
    return;
  }
  helpBusy = true;
  const box = document.createElement("div");
  box.className = "dr";
  const meta = document.createElement("div");
  meta.className = "m";
  meta.textContent = "Drafting from the job description… this can take a minute or two.";
  box.append(meta);
  li.append(box);
  const close = (): void => { box.remove(); helpBusy = false; void scheduleScan(); };

  let r: DraftResponse;
  try {
    r = await send<DraftResponse>({ type: "draft", question: ref.info.label, job });
  } catch (e) {
    r = { ok: false, code: "unreachable", error: e instanceof Error ? e.message : String(e) };
  }
  if (!r || typeof r !== "object" || !("ok" in r)) r = { ok: false, code: "bad_reply", error: "No reply from the extension." };
  // Safari's extension cannot start vani: the same draft runs as a job in the agent.
  if (!r.ok && r.code === "unsupported") r = await draftJob(ref.info.label, job);
  if (!r.ok) {
    meta.textContent = r.error;
    const ok = btn("s", "Close");
    ok.addEventListener("click", close);
    box.append(ok);
    return;
  }
  meta.textContent = `Draft by ${r.model || "the model"}` + (r.evidenceItems ? `, using ${r.evidenceItems} knowledge-base items` : "") +
    ". Edit it here, then insert. It stays yellow and is saved for review, never as a confident answer.";
  const ta = document.createElement("textarea");
  ta.value = r.draft;
  const row = document.createElement("div");
  row.className = "h";
  const insert = btn("f", "Insert for review");
  const discard = btn("s", "Discard");
  let overwrite = false;
  insert.addEventListener("click", () => {
    const text = ta.value.trim();
    if (!text) return;
    const o = insertDraft(ref, text, { overwrite });
    if (!o.filled && o.reason === "has_value" && !overwrite) {
      overwrite = true;
      insert.textContent = "Replace the field's text";
      toast(`"${preview(labelOf(ref))}" already has text. Click again to replace it.`);
      return;
    }
    toast(o.filled ? `Draft inserted into "${preview(labelOf(ref))}" (yellow). Read it before you submit.` : `Couldn't insert: ${REASON[o.reason ?? "manual"]}`);
    close();
  });
  discard.addEventListener("click", close);
  row.append(insert, discard);
  box.append(ta, row);
}

/** One draft through the agent (Safari's path). Never throws. */
async function draftJob(question: string, job: JobInfo): Promise<DraftReply> {
  let request: object;
  try {
    request = buildDraftRequest(question, job).request;
  } catch (e) {
    return { ok: false, code: "bad_request", error: e instanceof Error ? e.message : String(e) };
  }
  const r = await runJob({
    start: () => send({ type: "jobStart", kind: "draft", request }),
    poll: id => send({ type: "jobPoll", job: id }),
  }, DRAFT_JOB_TIMEOUT_MS);
  if (!r.ok) return { ok: false, code: "unreachable", error: r.error };
  return parseDraftReply(r.reply);
}

// --- suggest ----------------------------------------------------------------------
// One click for the whole page: the new free-text questions (labels only) go
// to the decision model, Claude drafts the ones it reads as essays, and each
// draft is inserted yellow, for review, into a field that is still empty. The
// rest are listed with the reason they are left to you.

/** Per question label: what Suggest said about it, shown in the panel list. */
const suggestNotes = new Map<string, FieldSuggestion>();
let suggesting = false;

function suggestButton(): HTMLButtonElement | null {
  return panel?.querySelector<HTMLButtonElement>("button.g") ?? null;
}

async function suggestAll(): Promise<void> {
  if (suggesting) { toast("Suggest is already running on this page."); return; }
  if (helpBusy) { toast("Finish or close the open draft in the list first, then Suggest."); return; }
  const job = jobInfo();
  // Only empty free-text fields that are neither secret nor self-identification.
  const refs = lastUnknown.filter(r => draftable(r) && classify(r.info) === "standard");
  const fields: SuggestField[] = refs.map((r, i) => ({ id: `f${i}`, label: labelOf(r), kind: r.info.kind === "text" ? "text" : "textarea" }));
  let request: object;
  try {
    request = buildSuggestRequest(fields, job);
  } catch (e) {
    const why = e instanceof Error ? e.message : String(e);
    // Counts only, never labels or values: enough to tell which filter emptied the list.
    console.warn("Vani Suggest:", why, { unknown: lastUnknown.length, eligible: refs.length, posting: job.description.length });
    toast(`Suggest: ${why} (${lastUnknown.length} new questions, ${refs.length} empty free-text).`);
    return;
  }
  const byId = new Map(refs.map((r, i) => [`f${i}`, r]));
  suggesting = true;
  helpBusy = true;
  const b = suggestButton();
  if (b) b.disabled = true;
  toast(`Asking Vani about ${fields.length} questions…`);
  try {
    const r = await runJob({
      start: () => send({ type: "jobStart", kind: "suggest", request }),
      poll: id => send({ type: "jobPoll", job: id }),
      onTick: ms => toast(`Vani is reading ${fields.length} questions and drafting the essays… ${Math.round(ms / 1000)} s`),
    }, SUGGEST_TIMEOUT_MS);
    const parsed = r.ok ? parseSuggestReply(r.reply, new Set(byId.keys())) : r;
    if (!parsed.ok) {
      toast(parsed.error);
      return;
    }
    let inserted = 0, review = 0, left = 0;
    for (const s of parsed.fields) {
      const ref = byId.get(s.id);
      if (!ref) continue;
      suggestNotes.set(labelOf(ref), s);
      if (s.status === "drafted") {
        // Never over text typed while Suggest ran.
        const o = insertDraft(ref, s.draft, { overwrite: false });
        if (o.filled) inserted++;
        else review++;
      } else {
        left++;
      }
    }
    const choices = lastUnknown.length - refs.length;
    toast(`Suggest: ${inserted} drafted for review (yellow)` + (review ? `, ${review} drafts not inserted (the field has text)` : "") +
      (left ? `, ${left} for you to answer` : "") + (choices > 0 ? `, ${choices} choice questions for you` : "") +
      ". Click for why. Read every draft before you submit.");
  } finally {
    suggesting = false;
    helpBusy = false;
    if (b) b.disabled = false;
    void scheduleScan();
  }
}

/** The questions with no saved answer, with live help for each. */
function renderUnknown(c: Coverage): void {
  if (!listEl || helpBusy) return;
  listEl.textContent = "";
  const job = jobInfo();
  const canDraft = job.description.length >= 40;
  for (const ref of lastUnknown.slice(0, PANEL_LIST)) {
    const li = document.createElement("li");
    const q = document.createElement("div");
    q.textContent = labelOf(ref);
    li.append(q);
    const note = suggestNotes.get(labelOf(ref));
    if (note && note.status !== "drafted") {
      const m = document.createElement("div");
      m.className = "m";
      m.textContent = note.reason || "Left for you to answer.";
      li.append(m);
    }
    const row = document.createElement("div");
    row.className = "h";
    if (lastBank && !ref.manual) {
      for (const s of suggest(ref.info, lastBank, location.hostname)) {
        const b = btn("c", `Use: ${preview(s.match.value.join(", "))}`);
        b.title = `Your answer to: ${s.question}`;
        b.addEventListener("click", () => {
          void fillRef(ref, s.match).then(o => {
            toast(o.filled ? `Filled "${preview(labelOf(ref))}" from a similar saved answer (yellow: check it).` : `Couldn't fill: ${REASON[o.reason ?? "manual"]}`);
          });
        });
        row.append(b);
      }
    }
    if (canDraft && draftable(ref)) {
      const d = btn("d", "Draft");
      d.title = "Ask the Vani Apply Mac app for a draft from this job description (sends the question and the posting only)";
      d.addEventListener("click", () => { void startDraft(ref, job, li); });
      row.append(d);
    }
    if (row.childElementCount) li.append(row);
    listEl.append(li);
  }
  if (c.unknown > PANEL_LIST) {
    const li = document.createElement("li");
    li.textContent = `+${c.unknown - PANEL_LIST} more (see the popup)`;
    listEl.append(li);
  }
}

async function scheduleScan(): Promise<void> {
  const s = await scan();
  const c = s.coverage;
  if (s.disabled || !c || c.found < MIN_FORM_FIELDS) return;
  ensurePanel();
  renderUnknown(c);
  if (statusEl && Date.now() >= heldUntil && !statusEl.textContent?.startsWith("Filled") && !statusEl.textContent?.startsWith("Saved")) {
    statusEl.textContent = `Vani: ${coverageLine(c)}` + (c.unknown ? " Click for the new questions." : "");
  }
}

// --- mark Applied ---------------------------------------------------------------------
// After a final Submit on a page Vani tracks as a job, ask whether to mark it
// Applied in the Jobs tab. It only asks: the mark is written when you click, never
// before. A confirmation page is a new document, so the question can also appear
// there (the background holds it for the tab). A form in a frame asks the top
// frame to show the question and shows it itself only if the top frame cannot.
// Posting text goes into the bar as text, never as markup.

let applied: AppliedFlow | null = null;
let appliedBar: { host: HTMLElement; text: HTMLElement; yes: HTMLButtonElement; no: HTMLButtonElement } | null = null;
let appliedHide: ReturnType<typeof setTimeout> | undefined;

function offerText(o: Parameters<typeof offerSubject>[0]): string {
  return `Did your application go through? Mark ${offerSubject(o)} as Applied in Vani?`;
}

function ensureAppliedBar(): NonNullable<typeof appliedBar> {
  if (appliedBar?.host.isConnected) return appliedBar;
  const host = document.createElement("div");
  host.setAttribute("data-vani-applied", "");
  host.style.cssText = "position:fixed;right:16px;top:16px;z-index:2147483647;";
  const root = host.attachShadow({ mode: "closed" });
  root.innerHTML = `
    <style>
      .p{font:13px system-ui,sans-serif;background:#111;color:#eee;border-radius:10px;padding:10px 12px;box-shadow:0 4px 16px #0006;max-width:340px}
      .t{margin:0 0 8px}
      .r{display:flex;gap:8px;justify-content:flex-end}
      button{font:inherit;border:0;border-radius:6px;padding:5px 9px;cursor:pointer;background:#2e8b57;color:#fff}
      button.n{background:#444}
      button:disabled{opacity:.5;cursor:default}
      [hidden]{display:none}
    </style>
    <div class="p" role="status" aria-live="polite"><p class="t"></p><div class="r"><button class="y">Mark Applied</button><button class="n">Not now</button></div></div>`;
  const bar = {
    host,
    text: root.querySelector<HTMLElement>(".t")!,
    yes: root.querySelector<HTMLButtonElement>(".y")!,
    no: root.querySelector<HTMLButtonElement>(".n")!,
  };
  bar.yes.addEventListener("click", () => { void applied?.accept(); });
  bar.no.addEventListener("click", () => { void applied?.dismiss(); });
  document.documentElement.appendChild(host);
  appliedBar = bar;
  return bar;
}

const appliedUi: AppliedUi = {
  offer(o) {
    clearTimeout(appliedHide);
    const b = ensureAppliedBar();
    b.text.textContent = offerText(o);
    b.yes.textContent = "Mark Applied";
    b.yes.disabled = b.no.disabled = false;
    b.yes.hidden = b.no.hidden = false;
  },
  working() {
    if (appliedBar) { appliedBar.yes.disabled = appliedBar.no.disabled = true; appliedBar.text.textContent = "Marking Applied…"; }
  },
  done(o) {
    const b = ensureAppliedBar();
    b.text.textContent = `Marked ${offerSubject(o)} as Applied.`;
    b.yes.hidden = b.no.hidden = true;
    clearTimeout(appliedHide);
    appliedHide = setTimeout(() => appliedUi.hide(), 4000);
  },
  failed(_o, message) {
    const b = ensureAppliedBar();
    b.text.textContent = `Could not mark it Applied: ${preview(message, 160)}`;
    b.yes.textContent = "Try again";
    b.yes.disabled = b.no.disabled = false;
    b.yes.hidden = b.no.hidden = false;
  },
  hide() {
    clearTimeout(appliedHide);
    appliedBar?.host.remove();
    appliedBar = null;
  },
};

// --- wiring -------------------------------------------------------------------

if (!window.__vaniApply && !coveredByParent()) {
  window.__vaniApply = true;
  window.__vaniScan = scan;
  tracker = createTracker({
    doc: document,
    host: location.hostname,
    getBank: bank,
    send: async fields => {
      const r = await send<CaptureReply | { error: string }>({ type: "capture", host: location.hostname, fields });
      return r && "added" in r ? r : null;
    },
    onDom: () => { void scheduleScan(); },
    onResult: r => announce(r),
    onSubmit: () => { void applied?.submitted(); },
  });
  applied = createAppliedFlow({ send, ui: appliedUi, href: () => location.href });
  if (window.top === window) void applied.resume();
  chrome.runtime.onMessage.addListener((msg: ToContent, _sender, sendResponse) => {
    if (msg.type === "appliedShow") {
      // Only the top frame is asked; answering "shown" is what lets the frame stay quiet.
      if (window.top !== window || !applied) return false;
      void applied.resume().then(() => sendResponse({ shown: true }), () => sendResponse({ shown: false }));
      return true;
    }
    const run: Promise<unknown> = msg.type === "scan" ? scan() : msg.type === "fill" ? fill(msg.overwrite, msg.typeahead) : capture("saved");
    // A sub-frame with nothing to say stays silent so it cannot win the reply race against the frame that has the form.
    const silent = (r: unknown): boolean => {
      if (window.top === window) return false;
      if (msg.type === "scan") return (r as ScanReply).fields === 0;
      if (msg.type === "fill") return (r as FillReply).outcomes.length === 0;
      return (r as CaptureReply).unchanged === 0 && (r as CaptureReply).added + (r as CaptureReply).updated === 0;
    };
    run.then(r => { if (!silent(r)) sendResponse(r); }, (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
    return true;
  });
  void scheduleScan();
}
