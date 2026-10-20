// Content script: saves your answers as you commit them (and flushes on submit,
// Next/Continue, Enter, route change, tab hide) and fills a form from saved
// answers when you ask. It never submits a form.
//
// Frames: a frame whose parent is same-origin does nothing itself; the parent's
// script walks into it (extract.ts) and listens on its document, so one frame
// owns each page and nothing is counted twice. A cross-origin frame (an iCIMS
// or Greenhouse embed on a company site) runs its own copy where the manifest
// matches its host or the popup injected it. On a host the manifest does not
// match, the extension cannot enter a cross-origin frame at all. The popup asks
// each frame that runs a copy on its own and adds the answers up (frames.ts).
import type { Bank } from "./types";
import { type FieldRef, scanForm } from "./extract";
import { type FillOutcome, clearMarks, fillAll, fillRef, insertDraft } from "./fill";
import { type Coverage, bucketOf, coverageLine, coverageOf } from "./coverage";
import { type FlushResult, MIN_FORM_FIELDS, type Tracker, createTracker } from "./tracker";
import { currentText, draftable, suggest } from "./suggest";
import { isReadableJob, navigationHref, postingKey, readJobInfo } from "./jobinfo";
import { type DraftReply, type JobInfo, buildDraftRequest, parseDraftReply } from "./draft";
import { classify } from "./classify";
import { type AppliedFlow, type AppliedUi, createAppliedFlow, offerSubject } from "./applied";
import {
  DRAFT_JOB_TIMEOUT_MS, type FieldSuggestion, SUGGEST_TIMEOUT_MS, type SuggestField,
  buildSuggestRequest, parseSuggestReply, runJob,
} from "./suggestjob";
import type { CaptureReply, DraftResponse, FillReply, ScanReply, ToBackground } from "./messages";
import { contentListener } from "./frames";
import { detectPortal, type PortalInfo } from "./portal";
import { evaluateRelevance, targetProfileFromBank, type RelevanceResult } from "./relevance";
import { type JobRow, applyVaniRow, scoreWord, withRowError } from "./jobrow";
import { auditForm, jumpToField, type FormAuditReport } from "./formaudit";
import { templateTailoring, generateTailoredDraft } from "./tailor";
import { scrapePageJobs, type ScrapeResult } from "./scraper";
import { MAX_RESUME_BYTES, RESUME_JOB_TIMEOUT_MS, type ResumeFetch, fromBase64, isPdf, isResumeField, parseResumeGet, toBase64 } from "./resume";
import { type OfferAction, type OfferUi, type PickedFile, createResumeOffer } from "./resumeoffer";
import { emptyBank, type Match } from "./types";
import { createBankMemo } from "./bankcache";
import { describeIngest, prepareIngest } from "./ingest";
import { crawlRow } from "./crawlrun";
import { QUIET_MS, carryMatches, observeRoots, runRounds, waitForQuiet } from "./rounds";

const PANEL_LIST = 8;

function send<T>(msg: ToBackground): Promise<T> {
  return chrome.runtime.sendMessage<T>(msg);
}

interface BankRead { bank: Bank; rev?: number }

async function readBank(): Promise<BankRead> {
  const r = await send<{ bank?: Bank; rev?: number; error?: string }>({ type: "getBank" });
  if (!r?.bank) throw new Error(r?.error ?? "no bank");
  return { bank: r.bank, rev: typeof r.rev === "number" ? r.rev : undefined };
}

// The last bank read, kept for the 2 s a scan may reuse it (bankcache.ts). A read with the same revision
// keeps the held object, so "the same bank" can be asked with ===. A failed read is never kept.
const bankMemo = createBankMemo<BankRead>({
  fetch: readBank,
  reuse: (held, next) => (held.rev !== undefined && held.rev === next.rev ? held : next),
});

/** The bank as the background has it now (it may serve its own memo): fill, save and the popup's scan use this. */
async function bank(): Promise<Bank | null> {
  try { return (await bankMemo.fresh()).bank; } catch { return null; }
}

/** The bank for a scan the page triggered itself, which fires on every input: a copy under 2 s old will do. */
async function scanBank(): Promise<Bank | null> {
  try { return (await bankMemo.get()).bank; } catch { return null; }
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
let lastRefs: FieldRef[] = [];
let lastAudit: FormAuditReport | undefined;
let lastRelevance: RelevanceResult | undefined;
let lastPortal: PortalInfo | undefined;

// The matches the last scan computed, with the bank they came from, for fill to reuse (see fill()).
let lastScan: { refs: FieldRef[]; matches: Map<FieldRef, Match | null>; bank: Bank | null } | undefined;

const VANI_ROW_TTL_MS = 60_000;
let vaniRow: { url: string; at: number; result: { row: JobRow | null } | { error: string } } | undefined;

/**
 * Vani's own row for this page's posting in place of the page-text estimate. Only the top frame
 * asks, and one answer is kept a minute per URL: scans run on page activity, and each ask runs `vani`.
 * No row, no Mac app, or an older vani leaves the labelled estimate; an unreadable row says why.
 */
async function vaniRelevance(estimate: RelevanceResult): Promise<RelevanceResult> {
  if (window.top !== window) return estimate;
  const url = location.href;
  if (!vaniRow || vaniRow.url !== url || Date.now() - vaniRow.at > VANI_ROW_TTL_MS) {
    try {
      const r = await send<{ row: JobRow | null } | { error: string }>({ type: "jobRow", url });
      vaniRow = { url, at: Date.now(), result: r && typeof r === "object" ? r : { row: null } };
    } catch {
      return estimate; // the worker is not there: not cached, so the next scan asks again
    }
  }
  const r = vaniRow.result;
  if ("error" in r) return withRowError(estimate, r.error);
  return r.row ? applyVaniRow(estimate, r.row) : estimate;
}

/** `cached`: a scan the page's own activity triggered may use a bank read under 2 s ago; a scan someone asked for reads it afresh. */
async function scan(cached = false): Promise<ScanReply> {
  const b = await (cached ? scanBank() : bank());
  const s = scanForm(document);
  lastBank = b;
  lastRefs = s.refs;
  const matches = new Map<FieldRef, Match | null>();
  const coverage = coverageOf(s.refs, b, location.hostname, { capped: s.capped, unread: s.unread }, matches);
  lastScan = { refs: s.refs, matches, bank: b };
  // A field is "unknown" exactly when it was matched (not skipped) and nothing matched; without a bank nothing is matched, so ask bucketOf.
  lastUnknown = b
    ? s.refs.filter(r => matches.has(r) && matches.get(r) === null && !r.manual)
    : s.refs.filter(r => bucketOf(r.info, r.manual, b, location.hostname).bucket === "unknown");
  const audit = auditForm(s.refs);
  lastAudit = audit;
  const portal = detectPortal(document, location.href);
  lastPortal = portal;
  const job = jobInfo();
  // Only a posting actually read from this page is rated: a score for an empty or half-rendered read would look like a verdict.
  const estimate = isReadableJob(job)
    ? evaluateRelevance({ title: job.title, company: job.company, description: job.description }, targetProfileFromBank(b))
    : undefined;
  const relevance = estimate ? await vaniRelevance(estimate) : undefined;
  lastRelevance = relevance;

  return {
    fields: coverage.found,
    matched: coverage.known,
    host: location.hostname,
    disabled: disabledHere(b),
    coverage,
    audit,
    relevance,
    portal,
    job,
  };
}

/** Watches the page, and every open shadow root the last scan walked, for elements being added or removed (not attributes, which marks and widgets churn constantly). */
function observeDom(roots: readonly Node[]): (onChange: () => void) => () => void {
  return onChange => observeRoots(roots.length ? roots : [document], onChange);
}

/** The resume chosen for this page's job, from the Mac app (through the background); a reason when there is none. */
async function resumeForThisJob(): Promise<ResumeFetch> {
  try {
    return parseResumeGet(await send<unknown>({ type: "resumeGet", url: location.href }));
  } catch {
    return parseResumeGet({ error: "the extension's background did not answer", code: "background" });
  }
}

async function fill(overwrite = false, typeahead = false): Promise<FillReply> {
  const b = await bank();
  if (!b || disabledHere(b)) return { outcomes: [] };
  clearMarks();
  // Taken now: filling fires input events, whose scans replace lastScan while this runs.
  const prior = lastScan?.bank === b ? lastScan : undefined;
  // Round 1 fills the page; later rounds fill what the first made appear (rounds.ts). Only round 1 may overwrite.
  let roots: readonly Node[] = [document]; // the roots the latest scan walked: the settle watches all of them
  const outcomes = await runRounds<FieldRef, FillOutcome>({
    scan: () => { const s = scanForm(document); roots = s.roots; return s.refs; },
    fill: (refs, round) => fillAll(refs, b, location.hostname, {
      overwrite: overwrite && round === 1,
      typeahead,
      resume: resumeForThisJob,
      // The scan made new field objects: the earlier scan's matches carry over only to fields provably unchanged (same bank, same elements).
      ...(round === 1 && prior ? { matches: carryMatches(refs, prior) } : {}),
    }),
    settle: ms => waitForQuiet(observeDom(roots), QUIET_MS, ms),
    now: Date.now,
  });
  bankMemo.invalidate(); // the fill counts below change the bank's revision
  const keys = outcomes.filter(o => o.filled && o.key).map(o => o.key!);
  if (keys.length) void send({ type: "filled", keys }).catch(() => undefined);
  const filled = outcomes.filter(o => o.filled).length;
  const review = outcomes.filter(o => o.filled && o.review).length;
  const left = outcomes.filter(o => !o.filled && o.reason === "no_match").length;
  const manual = outcomes.filter(o => !o.filled && o.reason === "manual").length;
  const resumeNote = outcomes.find(o => o.reason === "no_resume" || o.reason === "resume_unavailable" || o.reason === "refused");
  // With no resume chosen for this job, the offer (bottom left) says so; otherwise the reason goes in the toast.
  const offered = resumeOffer.offerAfterFill(outcomes);
  toast(`Filled ${filled} fields` + (review ? `, ${review} to review (yellow)` : "") + (left ? `, ${left} new questions` : "") + (manual ? `, ${manual} to do by hand` : "") +
    (resumeNote?.detail && !offered ? `. Resume not attached: ${resumeNote.detail}` : "") + ". Check, then submit yourself.");
  return { outcomes };
}

// --- floating panel -----------------------------------------------------------

let panel: ShadowRoot | null = null;
let statusEl: HTMLElement | null = null;
let listEl: HTMLElement | null = null;
let panelHost: HTMLElement | null = null;

function makeDraggable(host: HTMLElement, handle: HTMLElement): void {
  let isDragging = false;
  let startX = 0, startY = 0;
  let initLeft = 0, initTop = 0;

  handle.addEventListener("pointerdown", (e: PointerEvent) => {
    if ((e.target as HTMLElement).localName === "button") return;
    isDragging = true;
    startX = e.clientX;
    startY = e.clientY;
    const rect = host.getBoundingClientRect();
    initLeft = rect.left;
    initTop = rect.top;
    try { handle.setPointerCapture(e.pointerId); } catch {}
  });

  handle.addEventListener("pointermove", (e: PointerEvent) => {
    if (!isDragging) return;
    const dx = e.clientX - startX;
    const dy = e.clientY - startY;
    const rect = host.getBoundingClientRect();
    const maxLeft = Math.max(0, window.innerWidth - rect.width - 8);
    const maxTop = Math.max(0, window.innerHeight - rect.height - 8);
    const nextLeft = Math.min(maxLeft, Math.max(8, initLeft + dx));
    const nextTop = Math.min(maxTop, Math.max(8, initTop + dy));
    host.style.left = `${nextLeft}px`;
    host.style.top = `${nextTop}px`;
    host.style.right = "auto";
    host.style.bottom = "auto";
  });

  const endDrag = (e: PointerEvent): void => {
    if (!isDragging) return;
    isDragging = false;
    try { handle.releasePointerCapture(e.pointerId); } catch {}
  };
  handle.addEventListener("pointerup", endDrag);
  handle.addEventListener("pointercancel", endDrag);
}

function ensurePanel(): void {
  if (panel || window.top !== window) return; // one panel, in the top frame
  const host = document.createElement("div");
  panelHost = host;
  host.setAttribute("data-vani-panel", "");
  host.style.cssText = "position:fixed;right:16px;bottom:16px;z-index:2147483647;";
  panel = host.attachShadow({ mode: "closed" });
  panel.innerHTML = `
    <style>
      .p{font:12px/1.4 system-ui,-apple-system,sans-serif;background:rgba(15,23,42,0.95);backdrop-filter:blur(12px);color:#f1f5f9;border-radius:12px;padding:9px 12px;box-shadow:0 8px 30px rgba(0,0,0,0.35);border:1px solid rgba(255,255,255,0.12);max-width:350px;user-select:none;transition:all .18s ease}
      .p.minimized{padding:5px 10px;border-radius:20px;cursor:pointer;max-width:fit-content;background:rgba(15,23,42,0.98);box-shadow:0 4px 16px rgba(0,0,0,0.3)}
      .p.minimized .full-view{display:none}
      .p:not(.minimized) .min-view{display:none}
      .min-view{display:flex;align-items:center;gap:6px;font-weight:600;font-size:11px;color:#10b981}
      .r{display:flex;gap:5px;align-items:center;flex-wrap:wrap}
      .drag-handle{cursor:grab;color:#64748b;font-size:13px;padding:0 2px;letter-spacing:-1px}
      .drag-handle:active{cursor:grabbing}
      button{font:inherit;border:0;border-radius:6px;padding:4px 8px;cursor:pointer;background:#10b981;color:#fff;font-weight:500;transition:all .15s ease}
      button:hover:not(:disabled){filter:brightness(1.12)}
      button:active:not(:disabled){filter:brightness(0.94)}
      button.s{background:#334155}
      button.g{background:#8b5cf6}
      button.c{background:#475569}
      button.b-scrape{background:#0284c7}
      button:disabled{opacity:.5;cursor:default}
      .ctrl{background:transparent;color:#94a3b8;padding:2px 5px;font-size:12px;border-radius:4px}
      .ctrl:hover{background:rgba(255,255,255,0.12);color:#fff}
      .t{flex:1;cursor:pointer;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:110px}
      .u{margin:8px 0 0;padding-left:14px;max-height:260px;overflow-y:auto;color:#cbd5e1}
      .u[hidden]{display:none}
      .u li{margin:0 0 8px;font-size:11px}
      .h{display:flex;flex-wrap:wrap;gap:4px;margin-top:4px}
      .h button{background:#1e293b;border:1px solid #334155;padding:2px 6px;max-width:270px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px}
      .h button.d{background:#8b5cf6;border-color:#8b5cf6}
      .dr{margin-top:6px;display:flex;flex-direction:column;gap:6px}
      .dr textarea{font:11px system-ui,sans-serif;width:100%;box-sizing:border-box;min-height:90px;background:#0f172a;color:#eee;border:1px solid #334155;border-radius:6px;padding:6px;resize:vertical}
      .m{color:#94a3b8;font-size:11px}
      .pill{font-size:10px;font-weight:600;padding:2px 6px;border-radius:4px;cursor:pointer;white-space:nowrap}
      .drawer{margin-top:8px;padding:8px;border-radius:6px;font-size:11px}
    </style>
    <div class="p">
      <div class="min-view">
        <span>⚡ Vani</span> <span id="min-text" style="color:#cbd5e1;font-weight:normal;">• Active</span>
      </div>
      <div class="full-view">
        <div class="r">
          <span class="drag-handle" title="Drag to reposition">⋮⋮</span>
          <span class="t" title="Click to view new questions">Vani</span>
          <span class="pill" id="portal-badge" style="display:none;background:#334155;color:#93c5fd;" title="Detected portal"></span>
          <span class="pill" id="rel-badge" style="display:none;background:#065f46;color:#6ee7b7;" title="Real-time Relevance score"></span>
          <span class="pill" id="missed-badge" style="display:none;background:#78350f;color:#fcd34d;" title="Missed required fields"></span>
          <button class="f" title="Fill matching questions (Alt+Shift+F)">Fill</button>
          <button class="g" title="Ask decision model about new free-text questions and draft essays">Suggest</button>
          <button class="s" title="Save answers on this page">Save</button>
          <button class="b-scrape" id="scrape-btn" style="display:none;" title="Scrape job listings on this page">Scrape</button>
          <button class="c" title="Clear highlights from page">Clear</button>
          <button class="ctrl min-btn" title="Minimize to small badge">—</button>
          <button class="ctrl x" title="Hide panel">×</button>
        </div>
        <div class="drawer" id="rel-drawer" style="display:none;background:rgba(16,185,129,0.12);border:1px solid rgba(16,185,129,0.3);color:#6ee7b7;"></div>
        <div class="drawer" id="missed-drawer" style="display:none;background:rgba(245,158,11,0.12);border:1px solid rgba(245,158,11,0.3);color:#fbbf24;"></div>
        <div class="drawer" id="scrape-drawer" style="display:none;background:rgba(2,132,199,0.12);border:1px solid rgba(2,132,199,0.3);color:#7dd3fc;max-height:160px;overflow-y:auto;"></div>
        <ul class="u" hidden></ul>
      </div>
    </div>`;

  statusEl = panel.querySelector(".t");
  listEl = panel.querySelector(".u");
  const panelDiv = panel.querySelector<HTMLElement>(".p")!;
  const dragHandle = panel.querySelector<HTMLElement>(".drag-handle")!;
  const minBtn = panel.querySelector<HTMLElement>(".min-btn")!;
  const minView = panel.querySelector<HTMLElement>(".min-view")!;

  makeDraggable(host, dragHandle);

  panel.querySelector(".f")!.addEventListener("click", () => { void fill(); });
  panel.querySelector(".g")!.addEventListener("click", () => { void suggestAll(); });
  panel.querySelector(".s")!.addEventListener("click", () => { void capture("saved"); });
  panel.querySelector(".c")!.addEventListener("click", () => {
    clearMarks(document);
    toast("Highlights cleared.");
  });

  const relBadge = panel.querySelector<HTMLElement>("#rel-badge")!;
  const relDrawer = panel.querySelector<HTMLElement>("#rel-drawer")!;
  relBadge.addEventListener("click", () => {
    relDrawer.style.display = relDrawer.style.display === "none" ? "block" : "none";
  });

  const missedBadge = panel.querySelector<HTMLElement>("#missed-badge")!;
  const missedDrawer = panel.querySelector<HTMLElement>("#missed-drawer")!;
  missedBadge.addEventListener("click", () => {
    missedDrawer.style.display = missedDrawer.style.display === "none" ? "block" : "none";
  });

  const scrapeBtn = panel.querySelector<HTMLElement>("#scrape-btn")!;
  scrapeBtn.addEventListener("click", () => {
    const res = scrapePageJobs(document, location.hostname, targetProfileFromBank(lastBank), location.href, loadedHref());
    renderScrapedDrawer(res);
  });

  minBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    panelDiv.classList.add("minimized");
  });
  minView.addEventListener("click", () => {
    panelDiv.classList.remove("minimized");
  });
  panel.querySelector(".x")!.addEventListener("click", () => host.remove());
  statusEl!.addEventListener("click", () => { if (listEl && listEl.childElementCount > 0) listEl.hidden = !listEl.hidden; });
  document.documentElement.appendChild(host);
}

function renderMissedDrawer(audit: FormAuditReport): void {
  const drawer = panel?.querySelector<HTMLElement>("#missed-drawer");
  if (!drawer) return;
  drawer.textContent = "";
  const header = document.createElement("div");
  header.style.cssText = "font-weight:600;margin-bottom:4px;display:flex;justify-content:space-between;";
  const titleSpan = document.createElement("span");
  titleSpan.textContent = `⚠️ Missed Fillings (${audit.missedCount})`;
  const closeSpan = document.createElement("span");
  closeSpan.style.cssText = "cursor:pointer;color:#94a3b8;";
  closeSpan.title = "Close";
  closeSpan.textContent = "✕";
  closeSpan.addEventListener("click", () => { drawer.style.display = "none"; });
  header.append(titleSpan, closeSpan);
  drawer.append(header);

  const list = document.createElement("ul");
  list.style.cssText = "margin:0;padding-left:14px;display:flex;flex-direction:column;gap:4px;";
  audit.missed.slice(0, 10).forEach(m => {
    const li = document.createElement("li");
    li.style.cursor = "pointer";
    li.title = "Click to jump and focus this field";
    li.textContent = m.description;
    li.addEventListener("click", () => {
      const ref = lastRefs[m.refIndex];
      if (ref) {
        jumpToField(ref);
        toast(`Jumped to "${preview(m.label)}"`);
      }
    });
    list.append(li);
  });
  drawer.append(list);
}

function renderRelDrawer(rel: RelevanceResult): void {
  const drawer = panel?.querySelector<HTMLElement>("#rel-drawer");
  if (!drawer) return;
  drawer.textContent = "";
  const header = document.createElement("div");
  header.style.cssText = "font-weight:600;margin-bottom:4px;display:flex;justify-content:space-between;";
  const titleSpan = document.createElement("span");
  titleSpan.textContent = `🎯 Relevance: ${rel.fitLabel}`;
  const closeSpan = document.createElement("span");
  closeSpan.style.cssText = "cursor:pointer;color:#94a3b8;";
  closeSpan.title = "Close";
  closeSpan.textContent = "✕";
  closeSpan.addEventListener("click", () => { drawer.style.display = "none"; });
  header.append(titleSpan, closeSpan);
  drawer.append(header);

  const body = document.createElement("div");
  body.style.cssText = "display:flex;flex-direction:column;gap:3px;";
  if (rel.matchedSkills.length > 0) {
    const s = document.createElement("div");
    const label = document.createElement("span");
    label.style.color = "#94a3b8";
    label.textContent = "Skills: ";
    s.append(label, document.createTextNode(rel.matchedSkills.slice(0, 5).join(", ")));
    body.append(s);
  }
  if (rel.missingSkills.length > 0) {
    const m = document.createElement("div");
    const label = document.createElement("span");
    label.style.color = "#94a3b8";
    label.textContent = "JD mentions: ";
    m.append(label, document.createTextNode(rel.missingSkills.slice(0, 4).join(", ")));
    body.append(m);
  }
  if (rel.dealbreakers.length > 0) {
    const d = document.createElement("div");
    d.style.color = "#f87171";
    d.textContent = `⚠️ ${rel.dealbreakers.join("; ")}`;
    body.append(d);
  }
  drawer.append(body);
}

function renderScrapedDrawer(res: ScrapeResult): void {
  const drawer = panel?.querySelector<HTMLElement>("#scrape-drawer");
  if (!drawer) return;
  drawer.textContent = "";
  drawer.style.display = "block";
  const header = document.createElement("div");
  header.style.cssText = "font-weight:600;margin-bottom:6px;display:flex;justify-content:space-between;";
  const titleSpan = document.createElement("span");
  titleSpan.textContent = `🔍 Scraped ${res.totalFound} Jobs on this page`;
  const closeSpan = document.createElement("span");
  closeSpan.style.cssText = "cursor:pointer;color:#94a3b8;";
  closeSpan.title = "Close";
  closeSpan.textContent = "✕";
  closeSpan.addEventListener("click", () => { drawer.style.display = "none"; });
  header.append(titleSpan, closeSpan);
  drawer.append(header);

  if (res.totalFound === 0) {
    const empty = document.createElement("div");
    empty.textContent = "No job cards detected on this page.";
    drawer.append(empty);
    return;
  }

  const list = document.createElement("div");
  list.style.cssText = "display:flex;flex-direction:column;gap:6px;";
  res.jobs.slice(0, 6).forEach(j => {
    const item = document.createElement("div");
    item.style.cssText = "padding:4px 6px;background:rgba(255,255,255,0.06);border-radius:4px;display:flex;justify-content:space-between;align-items:center;";

    const textCol = document.createElement("div");
    textCol.style.cssText = "overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:220px;";

    const titleEl = document.createElement("div");
    titleEl.style.cssText = "font-weight:600;color:#fff;";
    titleEl.textContent = j.title;

    const subEl = document.createElement("div");
    subEl.style.cssText = "font-size:10px;color:#94a3b8;";
    subEl.textContent = `${j.company || res.host} • ${j.location || "Remote/Unspecified"}`;

    textCol.append(titleEl, subEl);

    const scoreBadge = document.createElement("span");
    scoreBadge.style.cssText = `font-size:10px;font-weight:600;padding:2px 5px;border-radius:3px;background:${j.relevance.score >= 80 ? '#065f46;color:#6ee7b7' : j.relevance.score >= 60 ? '#78350f;color:#fcd34d' : '#334155;color:#94a3b8'};`;
    scoreBadge.textContent = `${j.relevance.score}%`;

    item.append(textCol, scoreBadge);
    list.append(item);
  });
  drawer.append(list);

  const saveRow = document.createElement("div");
  saveRow.style.cssText = "margin-top:6px;display:flex;flex-direction:column;gap:4px;";
  const again = document.createElement("label");
  again.style.cssText = "display:flex;gap:4px;align-items:center;";
  const againBox = document.createElement("input");
  againBox.type = "checkbox";
  againBox.checked = true;
  again.append(againBox, document.createTextNode("Search this page again daily"));
  const saveBtn = document.createElement("button");
  saveBtn.type = "button";
  saveBtn.textContent = "Save to Vani";
  const result = document.createElement("div");
  result.setAttribute("role", "status");
  saveBtn.addEventListener("click", () => {
    saveBtn.disabled = true;
    result.textContent = "Sending…";
    void saveScrapeToVani(res, againBox.checked).then(t => { result.textContent = t; }).finally(() => { saveBtn.disabled = false; });
  });
  // The drawer shows one page; this walks every page of the listing (crawlrun.ts) and sends it the same way.
  const crawl = crawlRow(document, () => ({
    doc: document,
    pageUrl: location.href,
    pageTitle: document.title,
    profile: targetProfileFromBank(lastBank),
    send: m => send<unknown>(m as ToBackground),
  }), () => againBox.checked, toast);
  saveRow.append(again, saveBtn, result, crawl);
  drawer.append(saveRow);
  toast(`Scraped ${res.totalFound} jobs! Open popup to filter & export.`);
}

/** Send every scraped job to Vani and say what Vani answered; a failure is reported, never swallowed. */
async function saveScrapeToVani(res: ScrapeResult, saveSearch: boolean): Promise<string> {
  const p = prepareIngest(res, saveSearch);
  if ("error" in p) return p.error;
  let reply: unknown;
  try {
    reply = await send<unknown>(p.req);
  } catch (e) {
    return `Could not reach Vani: ${e instanceof Error ? e.message : String(e)}`;
  }
  return `${describeIngest(reply).text}${p.leftOut ? `. ${p.leftOut}` : ""}`;
}

/** How long a message stays up before a rescan may replace it with the coverage line. */
const HOLD_MS = 60_000;
let heldUntil = 0;

/** Show a message the user must be able to read; rescans (every DOM change) leave it alone for HOLD_MS. */
function toast(text: string): void {
  if (statusEl) statusEl.textContent = text;
  const minText = panel?.querySelector<HTMLElement>("#min-text");
  if (minText) minText.textContent = `• ${preview(text, 18)}`;
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
  no_resume: "no resume chosen for this job (choose one in the Vani Apply popup)",
  resume_unavailable: "the resume could not be had from the Vani Apply Mac app",
  refused: "this page did not take the file; attach it by hand",
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

/** The URL this document was loaded at; a single-page board's <head> still describes it after in-app navigation. */
const INJECTED_HREF = location.href;
function loadedHref(): string {
  return navigationHref() ?? INJECTED_HREF; // no navigation timing: where this script started
}

/** Rescans happen on every DOM change; the posting is re-read only when something it is read from changed (postingKey). */
let jobCache: { key: string; job: JobInfo } | null = null;
function jobInfo(): JobInfo {
  const key = postingKey(document, location.href);
  if (jobCache?.key !== key) {
    const ctx = { href: location.href, loadedHref: loadedHref(), portal: detectPortal(document, location.href).id };
    jobCache = { key, job: readJobInfo(document, ctx) };
  }
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
  const copy = btn("s", "Copy");
  const discard = btn("s", "Discard");
  copy.addEventListener("click", () => {
    const text = ta.value.trim();
    if (!text) return;
    void navigator.clipboard.writeText(text).then(() => {
      copy.textContent = "Copied!";
      setTimeout(() => { copy.textContent = "Copy"; }, 2000);
    });
  });
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
  row.append(insert, copy, discard);
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
    const tailorable = templateTailoring(labelOf(ref), job.company);
    if (tailorable) {
      const tb = btn("d", "✨ Tailor to JD");
      tb.title = `Generate tailored answer for "${tailorable.title}" using company & role from this JD`;
      tb.addEventListener("click", () => {
        const text = generateTailoredDraft(tailorable.kind, job, targetProfileFromBank(lastBank));
        insertDraft(ref, text, { overwrite: true });
        void navigator.clipboard.writeText(text).catch(() => {});
        toast(`Inserted tailored answer for "${preview(labelOf(ref))}" (yellow: review before submit). Copied to clipboard.`);
      });
      row.append(tb);
    }
    const sb = btn("s", "+ Save");
    sb.title = `Save your typed answer for "${preview(labelOf(ref))}" into Vani bank`;
    sb.addEventListener("click", () => { void capture("saved"); });
    row.append(sb);
    if (row.childElementCount) li.append(row);
    listEl.append(li);
  }
  if (c.unknown > PANEL_LIST) {
    const li = document.createElement("li");
    li.textContent = `+${c.unknown - PANEL_LIST} more (see the popup)`;
    listEl.append(li);
  }
}

/** The panel's relevance pill and drawer: shown for a rated posting, hidden otherwise. */
function showRelevance(rel: RelevanceResult | undefined): void {
  const relBadge = panel?.querySelector<HTMLElement>("#rel-badge");
  if (!relBadge) return;
  if (rel) {
    relBadge.textContent = `🎯 ${scoreWord(rel)} ${rel.score}%`;
    relBadge.style.display = "inline-block";
    renderRelDrawer(rel);
    return;
  }
  relBadge.style.display = "none";
  const drawer = panel?.querySelector<HTMLElement>("#rel-drawer");
  if (drawer) drawer.style.display = "none";
}

async function scheduleScan(): Promise<void> {
  const s = await scan(true);
  const c = s.coverage;
  // relevance is present only for a posting read from this page (scan()); the score bar is home.ts's looksLikeJob's.
  const isJobPage = s.portal?.isJobBoard || (s.relevance !== undefined && s.relevance.score > 0);
  if (s.disabled || (!isJobPage && (!c || c.found < MIN_FORM_FIELDS))) {
    // An in-app navigation away from a posting: its score must not stay on a panel left open.
    showRelevance(undefined);
    return;
  }
  ensurePanel();
  if (c && c.found >= MIN_FORM_FIELDS) renderUnknown(c);

  // Update dynamic portal badge
  const portalBadge = panel?.querySelector<HTMLElement>("#portal-badge");
  if (portalBadge && s.portal && s.portal.id !== "general") {
    portalBadge.textContent = s.portal.badge + (s.portal.stepInfo ? ` ${s.portal.stepInfo.currentStep}/${s.portal.stepInfo.totalSteps}` : "");
    portalBadge.style.display = "inline-block";
  }

  showRelevance(s.relevance);

  // Update dynamic missed fillings pill
  const missedBadge = panel?.querySelector<HTMLElement>("#missed-badge");
  if (missedBadge && s.audit && s.audit.missedCount > 0) {
    missedBadge.textContent = `⚠️ ${s.audit.missedCount} Missed`;
    missedBadge.style.display = "inline-block";
    renderMissedDrawer(s.audit);
  } else if (missedBadge) {
    missedBadge.style.display = "none";
  }

  // Update scrape button if on a board or job listings found
  const scrapeBtn = panel?.querySelector<HTMLElement>("#scrape-btn");
  if (scrapeBtn) {
    const isBoard = s.portal?.isJobBoard || document.querySelectorAll(
      "a[href*='/jobs/'], a[href*='/job/'], a[href*='/careers/'], a[href*='/positions/'], [class*='job-card'], [class*='job-post'], tr.job-post, .opening, .posting"
    ).length >= 2;
    scrapeBtn.style.display = isBoard ? "inline-block" : "none";
  }

  if (statusEl && Date.now() >= heldUntil && !statusEl.textContent?.startsWith("Filled") && !statusEl.textContent?.startsWith("Saved")) {
    if (c && c.found >= MIN_FORM_FIELDS) {
      statusEl.textContent = `Vani: ${coverageLine(c)}` + (c.unknown ? " Click for the new questions." : "");
      const minText = panel?.querySelector<HTMLElement>("#min-text");
      if (minText) minText.textContent = `• ${c.known}/${c.found}`;
    } else if (isJobPage) {
      statusEl.textContent = `Vani: ${s.relevance ? s.relevance.fitLabel : "Job page"}`;
      const minText = panel?.querySelector<HTMLElement>("#min-text");
      if (minText && s.relevance) minText.textContent = `• ${s.relevance.score}%`;
    }
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

// --- field tooltips -------------------------------------------------------------

let tooltipHost: HTMLElement | null = null;
let tooltipRoot: ShadowRoot | null = null;

function ensureTooltip(): { host: HTMLElement; root: ShadowRoot } {
  if (tooltipHost && tooltipRoot && tooltipHost.isConnected) return { host: tooltipHost, root: tooltipRoot };
  const host = document.createElement("div");
  host.setAttribute("data-vani-tooltip-host", "");
  host.style.cssText = "position:fixed;top:0;left:0;width:0;height:0;z-index:2147483647;pointer-events:none;";
  const root = host.attachShadow({ mode: "closed" });
  root.innerHTML = `
    <style>
      .tip {
        position: fixed;
        background: rgba(15, 23, 42, 0.96);
        backdrop-filter: blur(8px);
        color: #f8fafc;
        border: 1px solid rgba(255, 255, 255, 0.15);
        border-radius: 8px;
        padding: 5px 9px;
        font: 11px/1.3 system-ui, -apple-system, sans-serif;
        box-shadow: 0 4px 16px rgba(0,0,0,0.4);
        pointer-events: none;
        opacity: 0;
        transform: translateY(2px);
        transition: opacity 0.15s ease, transform 0.15s ease;
        max-width: 280px;
        z-index: 2147483647;
      }
      .tip.show { opacity: 1; transform: translateY(0); }
      .badge {
        display: inline-block;
        font-weight: 700;
        font-size: 10px;
        text-transform: uppercase;
        margin-right: 4px;
        padding: 1px 5px;
        border-radius: 4px;
        letter-spacing: 0.02em;
      }
      .badge.ok { background: rgba(16, 185, 129, 0.25); color: #10b981; border: 1px solid #10b981; }
      .badge.review { background: rgba(245, 158, 11, 0.25); color: #f59e0b; border: 1px solid #f59e0b; }
      .why { color: #cbd5e1; }
    </style>
    <div class="tip"></div>
  `;
  document.documentElement.appendChild(host);
  tooltipHost = host;
  tooltipRoot = root;
  return { host, root };
}

function showFieldTooltip(target: HTMLElement): void {
  const fillType = target.getAttribute("data-vani-fill");
  const why = target.getAttribute("data-vani-why");
  if (!fillType || !why) return;
  const { root } = ensureTooltip();
  const tip = root.querySelector<HTMLElement>(".tip")!;
  const isOk = fillType === "ok";
  tip.textContent = "";
  const badge = document.createElement("span");
  badge.className = `badge ${isOk ? "ok" : "review"}`;
  badge.textContent = isOk ? "✓ Confident" : "⚠️ Review";
  const whySpan = document.createElement("span");
  whySpan.className = "why";
  whySpan.textContent = why;
  tip.append(badge, whySpan);
  const rect = target.getBoundingClientRect();
  const tipWidth = 260;
  let left = rect.left;
  if (left + tipWidth > window.innerWidth) left = window.innerWidth - tipWidth - 10;
  if (left < 10) left = 10;
  let top = rect.top - 34;
  if (top < 10) top = rect.bottom + 6;
  tip.style.left = `${left}px`;
  tip.style.top = `${top}px`;
  tip.classList.add("show");
}

function hideFieldTooltip(): void {
  if (!tooltipRoot) return;
  const tip = tooltipRoot.querySelector<HTMLElement>(".tip");
  if (tip) tip.classList.remove("show");
}

function wireTooltips(): void {
  document.addEventListener("pointerover", (e: PointerEvent) => {
    const target = (e.target as HTMLElement)?.closest?.("[data-vani-fill]") as HTMLElement | null;
    if (target) showFieldTooltip(target);
  }, true);
  document.addEventListener("pointerout", (e: PointerEvent) => {
    const target = (e.target as HTMLElement)?.closest?.("[data-vani-fill]");
    if (target) hideFieldTooltip();
  }, true);
}
// --- build or choose a resume ---------------------------------------------------------
// After a Fill that left the resume box empty because no resume is chosen for this job,
// offer to build one (the agent runs `vani resume`) or choose a PDF. Only on a click; the
// PDF is then bound to the job and attached, outlined yellow. Text goes in as text.

let resumeBar: { host: HTMLElement; text: HTMLElement; buttons: Record<OfferAction, HTMLButtonElement> } | null = null;
let resumeHide: ReturnType<typeof setTimeout> | undefined;

function ensureResumeBar(): NonNullable<typeof resumeBar> {
  if (resumeBar?.host.isConnected) return resumeBar;
  const host = document.createElement("div");
  host.setAttribute("data-vani-resume", "");
  host.style.cssText = "position:fixed;left:16px;bottom:16px;z-index:2147483647;";
  const root = host.attachShadow({ mode: "closed" });
  root.innerHTML = `
    <style>
      .p{font:13px system-ui,sans-serif;background:#111;color:#eee;border-radius:10px;padding:10px 12px;box-shadow:0 4px 16px #0006;max-width:380px}
      .t{margin:0 0 8px}
      .r{display:flex;gap:8px;justify-content:flex-end;flex-wrap:wrap}
      button{font:inherit;border:0;border-radius:6px;padding:5px 9px;cursor:pointer;background:#444;color:#fff}
      button.build{background:#2e8b57}
      button:disabled{opacity:.5;cursor:default}
      [hidden]{display:none}
    </style>
    <div class="p" role="status" aria-live="polite"><p class="t"></p><div class="r">
      <button class="build">Build and attach</button><button class="choose">Choose PDF…</button><button class="dismiss">Not now</button></div></div>`;
  const bar = {
    host,
    text: root.querySelector<HTMLElement>(".t")!,
    buttons: {
      build: root.querySelector<HTMLButtonElement>(".build")!,
      choose: root.querySelector<HTMLButtonElement>(".choose")!,
      dismiss: root.querySelector<HTMLButtonElement>(".dismiss")!,
    },
  };
  bar.buttons.build.addEventListener("click", () => { void resumeOffer.build(); });
  bar.buttons.choose.addEventListener("click", () => { void resumeOffer.choose(); });
  bar.buttons.dismiss.addEventListener("click", () => resumeOffer.dismiss());
  document.documentElement.appendChild(host);
  resumeBar = bar;
  return bar;
}

const resumeUi: OfferUi = {
  ask(text, actions) {
    clearTimeout(resumeHide);
    const b = ensureResumeBar();
    b.text.textContent = text;
    for (const [a, btn] of Object.entries(b.buttons) as [OfferAction, HTMLButtonElement][]) { btn.hidden = !actions.includes(a); btn.disabled = false; }
  },
  busy(text) {
    const b = ensureResumeBar();
    b.text.textContent = text;
    for (const btn of Object.values(b.buttons)) btn.disabled = true;
  },
  done(text) {
    const b = ensureResumeBar();
    b.text.textContent = text;
    for (const btn of Object.values(b.buttons)) btn.hidden = true;
    clearTimeout(resumeHide);
    resumeHide = setTimeout(() => resumeUi.hide(), 8000);
  },
  hide() {
    clearTimeout(resumeHide);
    resumeBar?.host.remove();
    resumeBar = null;
  },
};

/** A file input clicked from the button press (a user gesture), resolved on its choice or Cancel. */
function pickPdf(): Promise<PickedFile | null> {
  return new Promise(resolve => {
    let resolved = false;
    const finish = (result: PickedFile | null) => {
      if (resolved) return;
      resolved = true;
      window.removeEventListener("focus", onWindowFocus);
      resolve(result);
    };

    const input = document.createElement("input");
    input.type = "file";
    input.accept = "application/pdf,.pdf";
    input.addEventListener("change", async () => {
      const f = input.files?.[0];
      if (!f) { finish(null); return; }
      // A file over the cap is never read.
      if (f.size > MAX_RESUME_BYTES) { finish({ name: f.name, size: f.size, bytes: null }); return; }
      try { finish({ name: f.name, size: f.size, bytes: new Uint8Array(await f.arrayBuffer()) }); } catch { finish(null); }
    }, { once: true });
    input.addEventListener("cancel", () => finish(null), { once: true });

    // Fallback for Safari < 17.4 where cancel event is not fired on file inputs
    const onWindowFocus = () => {
      setTimeout(() => {
        if (!resolved && (!input.files || input.files.length === 0)) {
          finish(null);
        }
      }, 500);
    };
    window.addEventListener("focus", onWindowFocus, { once: true });

    input.click();
  });
}

const resumeOffer = createResumeOffer({
  ui: resumeUi,
  bank,
  job: () => jobInfo(),
  build: async (request, onTick) => {
    const r = await runJob({
      start: () => send({ type: "jobStart", kind: "resume", request }),
      poll: id => send({ type: "jobPoll", job: id }),
      onTick,
    }, RESUME_JOB_TIMEOUT_MS);
    return r.ok ? r.reply : { error: r.error, code: r.code };
  },
  pick: pickPdf,
  save: async (name, pdf) => {
    const r = await send<{ ok?: boolean; error?: string } | undefined>({ type: "resumePut", url: location.href, name, pdf });
    return r?.error ? { error: r.error } : {};
  },
  attach: async r => {
    const localRefs = scanForm(document).refs.filter(x => x.info.kind === "file" && isResumeField(x.info));
    if (localRefs.length > 0) {
      return fillAll(localRefs, emptyBank(), location.hostname, { resume: async () => r, overwrite: true });
    }
    // If not found in current frame, route across frames via background
    try {
      const res = await send<{ outcomes?: FillOutcome[] }>({
        type: "attachResume",
        name: r.name,
        pdf: toBase64(r.bytes),
        pickedAt: r.pickedAt,
      });
      if (res?.outcomes && res.outcomes.length > 0) return res.outcomes;
    } catch {}
    return [];
  },
});

// --- wiring -------------------------------------------------------------------

if (!window.__vaniApply && !coveredByParent()) {
  window.__vaniApply = true;
  tracker = createTracker({
    doc: document,
    host: location.hostname,
    getBank: bank,
    send: async fields => {
      const r = await send<CaptureReply | { error: string }>({ type: "capture", host: location.hostname, fields });
      bankMemo.invalidate(); // the scan that follows must see what was just saved
      return r && "added" in r ? r : null;
    },
    onDom: () => { void scheduleScan(); },
    onResult: r => announce(r),
    onSubmit: () => { void applied?.submitted(); },
  });
  applied = createAppliedFlow({ send, ui: appliedUi, href: () => location.href });
  if (window.top === window) void applied.resume();
  // Every frame running this answers every message, an empty result included: the
  // popup asks each frame on its own and adds the answers up (frames.ts).
  const flow = applied;
  chrome.runtime.onMessage.addListener(contentListener({
    scan,
    fill,
    capture: () => capture("saved"),
    clearMarks: () => clearMarks(document),
    appliedShow: window.top === window ? () => flow.resume() : null,
    scrapeJobs: () => scrapePageJobs(document, location.hostname, targetProfileFromBank(lastBank), location.href, loadedHref()),
    getRelevance: () => ({
      relevance: lastRelevance ?? (isReadableJob(jobInfo()) ? evaluateRelevance(jobInfo(), targetProfileFromBank(lastBank)) : undefined),
      job: jobInfo(),
      portal: lastPortal ?? detectPortal(document),
    }),
    jumpToMissed: idx => {
      const ref = lastRefs[idx];
      if (ref) jumpToField(ref);
    },
    insertTailored: (label, text) => {
      const norm = (s: string) => s.trim().toLowerCase();
      const target = norm(label);
      const ref = lastRefs.find(r => norm(r.info.label) === target || norm(r.info.name) === target || norm(labelOf(r)) === target);
      if (ref) {
        insertDraft(ref, text, { overwrite: true });
        toast(`Inserted tailored draft for "${preview(label)}" (yellow).`);
      }
    },
    attachResume: async (name, pdf, pickedAt) => {
      const bytes = fromBase64(pdf);
      if (!bytes || !isPdf(bytes)) return { outcomes: [] };
      const refs = scanForm(document).refs.filter(x => x.info.kind === "file" && isResumeField(x.info));
      if (refs.length === 0) return { outcomes: [] };
      const outcomes = await fillAll(refs, emptyBank(), location.hostname, {
        resume: async () => ({ ok: true, name, bytes, pickedAt }),
        overwrite: true,
      });
      return { outcomes };
    },
  }));
  // The popup cannot list a cross-origin frame it may not script; this frame tells the background it is here.
  void send({ type: "frameReady" }).catch(() => undefined);
  wireTooltips();

  // Real-time reactivity as user types or selects options in form controls
  let liveInputTimer: ReturnType<typeof setTimeout> | undefined;
  const onLiveInput = (): void => {
    clearTimeout(liveInputTimer);
    liveInputTimer = setTimeout(() => { void scheduleScan(); }, 300);
  };
  document.addEventListener("input", onLiveInput, true);
  document.addEventListener("change", onLiveInput, true);

  void scheduleScan();
}
