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
import type { JobInfo } from "./draft";
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
    <div class="p"><div class="r"><span class="t">Vani</span><button class="f">Fill</button><button class="s">Save answers</button><button class="x" title="Hide">×</button></div>
    <ul class="u" hidden></ul></div>`;
  statusEl = panel.querySelector(".t");
  listEl = panel.querySelector(".u");
  panel.querySelector(".f")!.addEventListener("click", () => { void fill(); });
  panel.querySelector(".s")!.addEventListener("click", () => { void capture("saved"); });
  panel.querySelector(".x")!.addEventListener("click", () => host.remove());
  statusEl!.addEventListener("click", () => { if (listEl && listEl.childElementCount > 0) listEl.hidden = !listEl.hidden; });
  document.documentElement.appendChild(host);
}

function toast(text: string): void {
  if (statusEl) statusEl.textContent = text;
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
  if (statusEl && !statusEl.textContent?.startsWith("Filled") && !statusEl.textContent?.startsWith("Saved")) {
    statusEl.textContent = `Vani: ${coverageLine(c)}` + (c.unknown ? " Click for the new questions." : "");
  }
}

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
  });
  chrome.runtime.onMessage.addListener((msg: ToContent, _sender, sendResponse) => {
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
