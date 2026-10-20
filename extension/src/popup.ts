// Popup: act on the current page and manage saved answers.
import type { Answer, Bank, FieldInfo } from "./types";
import { MAX_BANK_BYTES, answerKey, bankBytes, exportBank } from "./bank";
import type { CaptureReply, FillReply, ScanReply, ToBackground } from "./messages";
import { sumCaptures, mergeFill, notAnswered, toFrames } from "./frames";
import { type Coverage, type FrameAnswer, type TabCoverage, coverageLine, tabCoverage } from "./coverage";
import { type Offer, offerSubject } from "./applied";
import { BASICS, BASICS_HOST, basicsFields, basicsSaveOutcome, basicsValues } from "./basics";
import { intentOf } from "./intents";
import { exportJobsAsCSV, exportJobsAsJSON, scrubPageUrl, type ScrapedJob, type ScrapeResult } from "./scraper";
import { type IngestSource, describeIngest, prepareIngest } from "./ingest";
import { type RelevanceResult, targetProfileFromBank } from "./relevance";
import type { PortalInfo } from "./portal";
import type { FormAuditReport } from "./formaudit";
import { templateTailoring, generateTailoredDraft } from "./tailor";
import type { JobInfo } from "./draft";
import { formatJobDetails } from "./jobinfo";
import { resumeRow } from "./resumerow";
import { type HomeView, bankSummary, basicsCompleteness, compactCount, friendlyError, homeHeadline, homeState, meterModel, nextStep, ratedFrame, viewSections } from "./home";

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;

function bg<T>(msg: ToBackground): Promise<T> {
  return chrome.runtime.sendMessage<T>(msg);
}

/** The frames of a tab whose content script registered with the background (frames.ts). */
async function registered(tabId: number): Promise<number[]> {
  const r = await bg<{ frames?: unknown }>({ type: "frames", tabId });
  return Array.isArray(r?.frames) ? r.frames.filter((f): f is number => typeof f === "number") : [];
}

export interface TabScanExtended {
  tabCoverage: TabCoverage;
  replies: Array<{ frameId: number; reply: ScanReply }>;
  primary?: ScanReply;
  audit?: FormAuditReport;
  portal?: PortalInfo;
  relevance?: RelevanceResult;
  job?: JobInfo;
}

/** Every frame's own coverage and features aggregated across all frames. */
async function scanTabExtended(): Promise<TabScanExtended> {
  const r = await toFrames<ScanReply>({ type: "scan" }, { registered });
  const answers: FrameAnswer[] = [
    ...r.answered.map(a => ({ frameId: a.frameId, result: a.reply })),
    ...r.failed.map(f => ({ frameId: f.frameId, result: null })),
  ];
  const cov = tabCoverage(answers);

  // Pick portal: prefer an explicit ATS/Job board over general
  const portalReply =
    r.answered.find(a => a.reply.portal && a.reply.portal.id !== "general")?.reply ??
    r.answered.find(a => a.frameId === 0)?.reply ??
    r.answered[0]?.reply;
  const portal = portalReply?.portal;

  const relReply = ratedFrame(r.answered)?.reply;
  const relevance = relReply?.relevance;

  // Pick job description info
  const jobReply =
    relReply ??
    r.answered.find(a => a.reply.job && a.reply.job.description)?.reply ??
    portalReply ??
    r.answered.find(a => a.frameId === 0)?.reply ??
    r.answered[0]?.reply;
  const job = jobReply?.job;

  // Aggregate form audit across all frames
  let totalFields = 0;
  let missedCount = 0;
  let fileMissing = false;
  let isReadyToSubmit = true;
  const missed: Array<FormAuditReport["missed"][number] & { frameId?: number }> = [];

  for (const a of r.answered) {
    if (a.reply.audit) {
      totalFields += a.reply.audit.totalFields;
      missedCount += a.reply.audit.missedCount;
      fileMissing = fileMissing || a.reply.audit.fileMissing;
      isReadyToSubmit = isReadyToSubmit && a.reply.audit.isReadyToSubmit;
      for (const m of a.reply.audit.missed) {
        missed.push({ ...m, frameId: a.frameId });
      }
    }
  }

  const audit: FormAuditReport = {
    totalFields,
    missedCount,
    missed,
    fileMissing,
    isReadyToSubmit: missedCount === 0,
  };

  const primary = r.answered.find(a => a.frameId === 0)?.reply ?? r.answered[0]?.reply;

  return {
    tabCoverage: cov,
    replies: r.answered,
    primary,
    audit,
    portal,
    relevance,
    job,
  };
}

let current: Bank | null = null;
let activeJob: JobInfo | undefined;
let host = "";
let mode: "mac-app" | "browser" = "browser";
let note: string | null = null;
let activeFilter = "all";
let activeSort = "recent";

let toastTimer: ReturnType<typeof setTimeout> | undefined;
function toast(msg: string): void {
  const el = $("#toast");
  if (!el) return;
  el.textContent = msg;
  el.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), 2400);
}

function flashCopied(btn: HTMLButtonElement | null, text = "✓ Copied"): void {
  if (!btn) return;
  if (!btn.dataset.origHtml) {
    btn.dataset.origHtml = btn.innerHTML;
  }
  btn.textContent = text;
  btn.classList.add("btn-copied");
  setTimeout(() => {
    if (btn.dataset.origHtml) {
      btn.innerHTML = btn.dataset.origHtml;
      delete btn.dataset.origHtml;
    }
    btn.classList.remove("btn-copied");
  }, 1600);
}

type Tone = "info" | "ok" | "warn" | "error";

/** One line under the header. `detail` (a raw message) goes in the tooltip, not on screen. */
function status(t: string, tone: Tone = "info", detail = ""): void {
  const el = $("#status");
  if (!el) return;
  el.textContent = t;
  el.dataset.tone = tone;
  el.title = detail;
}

/** An exception shown as a plain sentence; `prefix` names the action that failed. */
function statusError(e: unknown, prefix = ""): void {
  const f = friendlyError(e);
  status(prefix ? `${prefix}: ${f.line}` : f.line, "error", f.detail);
}

/** Swap a button's label while it works; returns the restore. The icon stays. */
function busy(btn: HTMLButtonElement | null, label: string): () => void {
  if (!btn) return () => undefined;
  const lbl = btn.querySelector<HTMLElement>(".lbl");
  const before = lbl?.textContent ?? "";
  btn.disabled = true;
  if (lbl) lbl.textContent = label;
  return () => {
    btn.disabled = false;
    if (lbl) lbl.textContent = before;
  };
}

function preview(a: Answer): string {
  if (a.sensitivity === "self_id") return "••• (self-ID)";
  const v = a.value.join(", ");
  return v.length > 70 ? v.slice(0, 67) + "…" : v;
}

function categoryOf(a: Answer): "contact" | "work" | "self_id" | "other" {
  if (a.sensitivity === "self_id") return "self_id";
  const intent = intentOf(a.key) || a.labels.map(l => intentOf(l)).find(Boolean);
  if (intent) {
    if (intent === "self_id") return "self_id";
    return "work";
  }
  const k = a.key.toLowerCase();
  const contactTokens = [
    "first name", "last name", "full name", "middle name", "email", "phone",
    "city", "state", "country", "street address", "postal code", "linkedin",
    "github", "website", "current company", "current title",
  ];
  if (contactTokens.some(c => k.includes(c))) return "contact";
  return "other";
}

function updateMeter(c: Coverage): void {
  const m = meterModel(c);
  $("#bar-ok").style.width = `${m.okPct}%`;
  $("#bar-rev").style.width = `${m.revPct}%`;
  $("#bar-unk").style.width = `${m.unkPct}%`;
  const percentEl = $("#meter-percent");
  percentEl.textContent = `${m.badge} ready`;
  percentEl.className = `badge meter-badge badge-${m.tone}`;
  $("#meter-subtitle").textContent = m.subtitle;
  $("#stat-ok").textContent = m.ok;
  $("#stat-rev").textContent = m.review;
  $("#stat-unk").textContent = m.fresh;
  const fillBtn = $("#fill") as HTMLButtonElement;
  fillBtn.disabled = !(c.found > 0);
}

function render(): void {
  if (!current) return;
  const q = ($("#filter") as HTMLInputElement).value.toLowerCase();
  let rows = Object.values(current.answers).filter(a => {
    if (q && !a.key.includes(q) && !a.value.join(" ").toLowerCase().includes(q)) return false;
    if (activeFilter === "all") return true;
    if (activeFilter === "review") return a.review === true;
    return categoryOf(a) === activeFilter;
  });

  // Sorting
  if (activeSort === "alpha") {
    rows.sort((x, y) => x.key.localeCompare(y.key));
  } else if (activeSort === "uses") {
    rows.sort((x, y) => (y.fills + y.uses) - (x.fills + x.uses));
  } else {
    rows.sort((x, y) => y.updatedAt.localeCompare(x.updatedAt));
  }

  const tbody = $("#answers");
  tbody.textContent = "";
  const emptyEl = $("#empty-answers");
  if (emptyEl) emptyEl.hidden = rows.length > 0;

  for (const a of rows) {
    const tr = document.createElement("tr");
    const k = document.createElement("td");
    k.textContent = a.key;
    k.title = a.labels.join("\n");
    if (a.labels.length > 1) {
      const badge = document.createElement("span");
      badge.className = "badge badge-muted";
      badge.style.marginLeft = "4px";
      badge.textContent = `+${a.labels.length - 1}`;
      badge.title = `${a.labels.length} matching wordings`;
      k.append(badge);
    }

    const v = document.createElement("td");
    const valText = preview(a);
    v.textContent = valText;
    if (a.review) {
      v.className = "review";
      const chip = document.createElement("span");
      chip.className = "badge badge-rev";
      chip.style.marginLeft = "6px";
      chip.textContent = "Review";
      v.append(chip);
    }

    const n = document.createElement("td");
    n.textContent = `${a.fills}`;
    n.title = `${a.uses} times used on page, ${a.fills} times submitted`;

    const act = document.createElement("td");
    const copy = document.createElement("button");
    copy.className = "sm";
    copy.textContent = "Copy";
    copy.title = "Copy answer value to clipboard";
    copy.onclick = async () => {
      try {
        await navigator.clipboard.writeText(a.value.join(", "));
        flashCopied(copy, "✓ Copied");
        toast("Copied to clipboard!");
      } catch {
        toast("Could not copy to clipboard.");
      }
    };

    const edit = document.createElement("button");
    edit.className = "sm";
    edit.textContent = "Edit";
    edit.onclick = () => openEditModal(a);

    const del = document.createElement("button");
    del.className = "sm danger";
    del.textContent = "Del";
    del.title = `Forget "${a.key}"`;
    del.onclick = async () => {
      if (confirm(`Forget "${a.key}"?`)) {
        await bg({ type: "deleteAnswer", key: a.key });
        toast("Answer deleted.");
        await refresh();
      }
    };

    act.append(copy, edit, del);
    tr.append(k, v, n, act);
    tbody.append(tr);
  }

  $("#count").textContent = `${Object.keys(current.answers).length} saved answers`;
  $("#storage").textContent = (mode === "mac-app" ? "Stored by the Vani Apply Mac app." : "Stored in this browser only.") + (note ? ` ${note}` : "");
  const bytes = bankBytes(current);
  $("#size").textContent = `${Math.round(bytes / 1000)} KB`;
  const quotaFill = $("#quota-fill");
  if (quotaFill) {
    quotaFill.style.display = "none";
  }

  renderBasics();
  ($("#selfid") as HTMLInputElement).checked = current.settings.captureSelfId;
  ($("#disabled") as HTMLInputElement).checked = current.settings.disabledHosts.includes(host);
}

// --- interactive edit modal -----------------------------------------------------------

let editingKey: string | null = null;
function openEditModal(a: Answer): void {
  editingKey = a.key;
  ($("#edit-key") as HTMLInputElement).value = a.key;
  ($("#edit-value") as HTMLTextAreaElement).value = a.value.join(", ");
  ($("#edit-review") as HTMLInputElement).checked = !!a.review;
  $("#modal-edit").hidden = false;
  ($("#edit-value") as HTMLTextAreaElement).focus();
}

function closeEditModal(): void {
  $("#modal-edit").hidden = true;
  editingKey = null;
}

$("#modal-edit-close").onclick = closeEditModal;
$("#modal-edit-cancel").onclick = closeEditModal;
$("#modal-edit-save").onclick = async () => {
  if (!editingKey) return;
  const next = ($("#edit-value") as HTMLTextAreaElement).value.trim();
  const review = ($("#edit-review") as HTMLInputElement).checked;
  const a = current?.answers[editingKey];
  const kind = a?.kind ?? "text";
  const val = kind === "checkbox" || kind === "select" || kind === "radio"
    ? next.split(",").map(s => s.trim()).filter(Boolean)
    : [next];
  await bg({ type: "editAnswer", key: editingKey, value: val, review });
  closeEditModal();
  toast("Answer updated.");
  await refresh();
};

// --- interactive add modal -------------------------------------------------------------

function openAddModal(label?: string): void {
  ($("#add-label") as HTMLInputElement).value = typeof label === "string" ? label : "";
  ($("#add-value") as HTMLTextAreaElement).value = "";
  ($("#add-review") as HTMLInputElement).checked = false;
  $("#modal-add").hidden = false;
  if (typeof label === "string" && label) {
    ($("#add-value") as HTMLTextAreaElement).focus();
  } else {
    ($("#add-label") as HTMLInputElement).focus();
  }
}

function closeAddModal(): void {
  $("#modal-add").hidden = true;
}

$("#add-btn").onclick = () => openAddModal();
$("#modal-add-close").onclick = closeAddModal;
$("#modal-add-cancel").onclick = closeAddModal;
$("#modal-add-save").onclick = async () => {
  const label = ($("#add-label") as HTMLInputElement).value.trim();
  const val = ($("#add-value") as HTMLTextAreaElement).value.trim();
  const review = ($("#add-review") as HTMLInputElement).checked;
  if (!label || !val) {
    toast("Question label and answer value are required.");
    return;
  }
  const f: FieldInfo = {
    kind: "text",
    label,
    name: "",
    autocomplete: "",
    automationId: "",
    options: [],
    required: false,
    value: [val],
  };
  const r = await bg<CaptureReply | { error?: string } | undefined>({
    type: "capture",
    host: BASICS_HOST,
    fields: [f],
  });
  if (r && "added" in r) {
    if (review) {
      const key = answerKey(f);
      await bg({ type: "editAnswer", key, value: [val], review: true });
    }
    closeAddModal();
    toast("Answer added to bank.");
    await refresh();
  } else {
    toast("Could not save answer.");
  }
};

// --- your basics ---------------------------------------------------------------------

let basicsTyped = false;

function basicInput(id: string): HTMLInputElement | HTMLSelectElement | null {
  return $(`#basic-${id}`) as HTMLInputElement | HTMLSelectElement | null;
}

function option(value: string, text = value): HTMLOptionElement {
  const o = document.createElement("option");
  o.value = value;
  o.textContent = text;
  return o;
}

/** One row per basic, in its group: a dropdown where the answer is a choice, else a text box. */
function buildBasics(): void {
  for (const b of BASICS) {
    const label = document.createElement("label");
    label.htmlFor = `basic-${b.id}`;
    label.textContent = b.label;
    let box: HTMLInputElement | HTMLSelectElement;
    if (b.options) {
      box = document.createElement("select");
      box.append(option("", "—"), ...b.options.map(o => option(o)));
      box.onchange = () => { basicsTyped = true; };
      box.onkeydown = (e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          void saveBasics();
        }
      };
    } else {
      box = document.createElement("input");
      box.type = b.kind === "email" || b.kind === "tel" || b.kind === "url" ? b.kind : "text";
      box.setAttribute("autocomplete", b.autocomplete || "off");
      box.oninput = () => { basicsTyped = true; };
      box.onkeydown = (e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          void saveBasics();
        }
      };
    }
    box.id = `basic-${b.id}`;
    $(`#basic-${b.group}`)?.append(label, box);
  }
}

function renderBasics(): void {
  const v = basicsValues(current);
  if (!basicsTyped) {
    for (const b of BASICS) {
      const box = basicInput(b.id);
      if (!box) continue;
      const saved = v[b.id] ?? "";
      if (box instanceof HTMLSelectElement && saved && !Array.from(box.options).some(o => o.value === saved)) {
        box.append(option(saved));
      }
      box.value = saved;
    }
  }
  const selfIdBox = $("#basic-selfid-box");
  if (selfIdBox) selfIdBox.hidden = !current?.settings.captureSelfId;
}

let savingBasics = false;

async function saveBasics(): Promise<void> {
  if (savingBasics) return; // Enter and the button both save: one capture at a time
  const out = $("#basic-status");
  const button = $<HTMLButtonElement>("#basic-save");
  const values: Record<string, string> = {};
  for (const b of BASICS) {
    const el = basicInput(b.id);
    if (el) values[b.id] = el.value;
  }
  const fields = basicsFields(values, basicsValues(current));
  if (fields.length === 0) {
    if (out) out.textContent = "Nothing new to save.";
    toast("No changes in basics.");
    return;
  }
  savingBasics = true;
  if (button) button.disabled = true;
  if (out) out.textContent = "Saving basics...";
  let reply: unknown;
  try {
    reply = await bg<CaptureReply | { error?: string } | undefined>({ type: "capture", host: BASICS_HOST, fields });
  } catch (e) {
    reply = { error: friendlyError(e, "the extension did not answer").detail || "the extension did not answer" };
  } finally {
    savingBasics = false;
    if (button) button.disabled = false;
  }
  const o = basicsSaveOutcome(reply);
  if (out) out.textContent = o.status;
  toast(o.toast);
  if (!o.saved) return; // what was typed stays in the boxes for another try
  basicsTyped = false;
  // The save landed; a failure to redraw from the bank is reported as that, not as a failed save.
  try {
    await refresh();
  } catch (e) {
    if (out) out.textContent = `${o.status} The list could not be redrawn: ${friendlyError(e).line}`;
  }
}

async function refresh(): Promise<void> {
  const r = await bg<{ bank?: Bank; mode?: "mac-app" | "browser"; note?: string | null; error?: string }>({ type: "getBank" });
  if (r.error || !r.bank) { status("Could not read your saved answers.", "error", r.error ?? "no reply from the extension"); return; }
  current = r.bank;
  mode = r.mode ?? "browser";
  note = r.note ?? null;
  render();
  renderDashboard();
}

// --- home tab: which blocks show, and the dashboard -------------------------------------

/** Show the blocks the view calls for and hide the rest. home.ts decides the view; this only toggles. */
function setHomeView(view: HomeView): void {
  const s = viewSections(view);
  $("#tab-page").dataset.view = view;
  $("#form-view").hidden = !s.form;
  $("#form-extra").hidden = !s.form;
  $("#job-area").hidden = !s.job;
  $("#home-empty-card").hidden = !s.dashboard;
  if (!s.form) $("#missed-card").style.display = "none";
  const h = homeHeadline(view, host);
  $("#home-title").textContent = h.title;
  $("#home-desc").textContent = h.desc;
  renderDashboard();
}

/** Profile completeness, saved-answer counts and the next step, from the bank the background returned. Nothing when it could not be read. */
function renderDashboard(): void {
  const card = $("#home-profile");
  card.hidden = current === null;
  if (!current) return;
  const c = basicsCompleteness(current);
  const s = bankSummary(current);
  const step = nextStep(c, s);
  const pct = $("#profile-pct");
  pct.textContent = `${c.pct}%`;
  pct.className = `badge badge-${c.pct >= 70 ? "ok" : c.pct > 0 ? "rev" : "muted"}`;
  $("#profile-bar").style.width = `${c.pct}%`;
  $("#profile-track").setAttribute("aria-valuenow", String(c.pct));
  $("#profile-text").textContent = step ? step.text : "Your basics are saved, so Fill can complete the usual first fields.";
  const answers = `${compactCount(s.answers)} saved ${s.answers === 1 ? "answer" : "answers"}`;
  $("#profile-meta").textContent = `${c.filled} of ${c.total} basics · ${answers} · ${mode === "mac-app" ? "kept by the Mac app" : "kept in this browser"}`;
  const next = $("#btn-profile-next");
  next.hidden = step === null;
  if (step) {
    next.textContent = step.button;
    next.dataset.tab = step.tab;
  }
  $("#answers-label").textContent = s.answers > 0 ? answers : "Saved answers";
}

function openTab(name: string): void {
  document.querySelector<HTMLButtonElement>(`.tab-btn[data-tab="${name}"]`)?.click();
}

/** Per-page breakdown and the questions with no saved answer. */
function renderCoverage(c: Coverage): void {
  const list = $("#unknown");
  list.textContent = "";
  for (const label of c.unknownLabels) {
    const li = document.createElement("li");
    li.style.cssText = "display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:6px;padding:3px 0;";

    const textSpan = document.createElement("span");
    textSpan.textContent = label;
    textSpan.style.cssText = "flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px;";
    textSpan.title = label;

    const btnGroup = document.createElement("div");
    btnGroup.style.cssText = "display:flex;gap:4px;flex-shrink:0;";

    const tailorable = templateTailoring(label, activeJob?.company);
    if (tailorable) {
      const tb = document.createElement("button");
      tb.className = "sm";
      tb.textContent = "✨ Tailor";
      tb.title = `Generate tailored answer for "${tailorable.title}" from JD and insert into field`;
      tb.onclick = async () => {
        const text = generateTailoredDraft(
          tailorable.kind,
          activeJob ?? { title: "", company: "", description: "" },
          targetProfileFromBank(current)
        );
        try {
          await toFrames({ type: "insertTailored", label, text }, { registered });
          await navigator.clipboard.writeText(text).catch(() => {});
          toast(`Tailored answer inserted into "${label}" & copied!`);
        } catch (e) {
          toast(`Could not insert answer: ${e instanceof Error ? e.message : String(e)}`);
        }
      };
      btnGroup.append(tb);
    }

    const ab = document.createElement("button");
    ab.className = "sm";
    ab.textContent = "+ Answer";
    ab.title = `Save an answer for "${label}"`;
    ab.onclick = () => openAddModal(label);
    btnGroup.append(ab);

    li.append(textSpan, btnGroup);
    list.append(li);
  }
  if (c.unknown > c.unknownLabels.length) {
    const li = document.createElement("li");
    li.textContent = `+${c.unknown - c.unknownLabels.length} more not listed`;
    list.append(li);
  }
  for (const label of c.manualLabels) {
    const li = document.createElement("li");
    li.textContent = `${label} (do by hand)`;
    list.append(li);
  }
  $("#coverage").hidden = c.unknown === 0 && c.manual === 0;
  updateMeter(c);
}

/** Which frame found what, when the form is not all in the top frame. */
function renderFrames(t: TabCoverage): void {
  const list = $("#frames");
  list.textContent = "";
  const unanswered = t.asked - t.answered;
  if (t.frames.length > 1) {
    for (const f of t.frames) {
      const li = document.createElement("li");
      li.textContent = `${f.frameId === 0 ? "This page" : "Frame"} (${f.host || "?"}): ${coverageLine(f.coverage)}`;
      list.append(li);
    }
  }
  if (unanswered > 0 && t.frames.length > 0) {
    const li = document.createElement("li");
    li.textContent = `${unanswered} of ${t.asked} frames running Vani did not answer in time; their fields are not counted.`;
    list.append(li);
  }
  $("#framebox").hidden = list.childElementCount === 0;
}

/**
 * A page Vani tracks as a job: offer to mark it Applied.
 */
async function appliedRow(tabId: number, url: string | undefined): Promise<void> {
  const row = $("#applied");
  const text = $("#applied-text");
  const yes = $("#applied-yes") as HTMLButtonElement;
  const no = $("#applied-no") as HTMLButtonElement;
  let offer = (await bg<{ offer?: Offer | null }>({ type: "appliedPending", tabId }).catch(() => undefined))?.offer ?? null;
  if (!offer && url) {
    const r = await bg<{ offer?: Offer | null }>({ type: "appliedSignal", url, tabId }).catch(() => undefined);
    offer = r?.offer ?? null;
  }
  if (!offer) return;
  const subject = offerSubject(offer);
  text.textContent = `Tracked job on this page: ${subject}.`;
  yes.hidden = no.hidden = false;
  yes.disabled = no.disabled = false;
  row.hidden = false;
  yes.onclick = async () => {
    yes.disabled = no.disabled = true;
    const r: { ok?: boolean; error?: string } | undefined = await bg<{ ok?: boolean; error?: string }>({ type: "appliedMark", tabId })
      .catch((e: unknown) => ({ error: e instanceof Error ? e.message : String(e) }));
    if (r?.ok) {
      text.textContent = `Marked ${subject} as Applied.`;
      yes.hidden = no.hidden = true;
      toast(`Marked ${subject} as Applied!`);
    } else {
      text.textContent = `Could not mark it Applied: ${r?.error ?? "no reply"}`;
      yes.disabled = no.disabled = false;
    }
  };
  no.onclick = async () => {
    await bg({ type: "appliedDismiss", tabId }).catch(() => undefined);
    row.hidden = true;
  };
}

function initTabs(): void {
  const tabs = document.querySelectorAll<HTMLButtonElement>(".tab-btn");
  tabs.forEach(btn => {
    btn.onclick = () => {
      const target = btn.dataset.tab;
      if (!target) return;
      if (basicsTyped && target !== "basics") {
        void saveBasics();
      }
      tabs.forEach(t => {
        t.classList.toggle("active", t === btn);
        t.setAttribute("aria-selected", String(t === btn));
      });
      document.querySelectorAll<HTMLElement>(".tab-pane").forEach(pane => {
        pane.classList.toggle("active", pane.id === `tab-${target}`);
      });
    };
  });

  // Filter chips on Answers tab
  const chips = document.querySelectorAll<HTMLButtonElement>(".chip-btn");
  chips.forEach(chip => {
    chip.onclick = () => {
      chips.forEach(c => c.classList.toggle("active", c === chip));
      activeFilter = chip.dataset.filter || "all";
      render();
    };
  });

  // Sort dropdown on Answers tab
  const sortSelect = $("#sort-answers") as HTMLSelectElement | null;
  if (sortSelect) {
    sortSelect.onchange = () => {
      activeSort = sortSelect.value;
      render();
    };
  }
}

let scrapedJobs: ScrapedJob[] = [];
/** The listing page the scrape came from (ScrapeResult.pageUrl is already scrubbed). */
let scrapedPage = { pageUrl: "", pageTitle: "" };

/** Send jobs to Vani and say what Vani answered, or why nothing was sent; a failure is shown, never swallowed. */
async function sendToVani(scrape: { pageUrl: string; pageTitle: string; jobs: readonly IngestSource[] }, saveSearch: boolean): Promise<{ ok: boolean; text: string }> {
  const p = prepareIngest(scrape, saveSearch);
  if ("error" in p) return { ok: false, text: p.error };
  let reply: unknown;
  try {
    reply = await bg<unknown>(p.req);
  } catch (e) {
    return { ok: false, text: `Could not reach Vani: ${friendlyError(e).line}` };
  }
  const out = describeIngest(reply);
  return { ok: out.ok, text: `${out.text}${p.leftOut ? `. ${p.leftOut}` : ""}` };
}

function renderPortal(portal?: PortalInfo): void {
  const card = $("#portal-card");
  if (!card) return;
  if (!portal || portal.id === "general") {
    card.style.display = "none";
    return;
  }
  card.style.display = "block";
  $("#portal-icon").textContent = portal.icon;
  $("#portal-name").textContent = portal.name;
  const stepEl = $("#portal-step");
  if (stepEl) {
    if (portal.stepInfo) {
      stepEl.textContent = `Step ${portal.stepInfo.currentStep} of ${portal.stepInfo.totalSteps}`;
      stepEl.style.display = "inline-flex";
    } else {
      stepEl.style.display = "none";
    }
  }
  $("#portal-tips").textContent = portal.tips.join(" ");
}

/** "Track in Vani" for the posting on this page: only for an http(s) page with a title, sent as the scraper's Track button sends it. */
function renderJobActions(job: JobInfo | undefined, pageUrl: string | undefined): void {
  const row = $("#job-actions");
  const btn = $("#job-track") as HTMLButtonElement;
  const title = typeof job?.title === "string" ? job.title.trim() : "";
  row.hidden = !title || !pageUrl || !/^https?:\/\//i.test(pageUrl);
  btn.onclick = async () => {
    btn.disabled = true;
    try {
      const url = scrubPageUrl(pageUrl ?? "");
      const r = await sendToVani({ pageUrl: url, pageTitle: "", jobs: [{ title, company: job?.company?.trim() || host, location: "", url, snippet: "" }] }, false);
      status(r.ok ? `Vani: ${r.text}.` : r.text, r.ok ? "ok" : "error");
    } finally {
      btn.disabled = false;
    }
  };
}

function renderJobDetails(job?: JobInfo, pageUrl?: string): void {
  const card = $("#job-details-card");
  if (!card) return;
  const title = job?.title?.trim() ?? "";
  const desc = job?.description?.trim() ?? "";
  const company = job?.company?.trim() ?? "";

  if (!title && !desc) {
    card.style.display = "none";
    return;
  }
  card.style.display = "block";

  const titleEl = $("#job-title-text");
  if (titleEl) {
    titleEl.textContent = title || "Job Details";
    titleEl.title = title || "Job Details";
  }

  const compEl = $("#job-company-text");
  if (compEl) {
    compEl.textContent = company ? `🏢 ${company}` : (host ? `🌐 ${host}` : "");
  }

  const snippetEl = $("#job-desc-snippet");
  const toggleBtn = $("#toggle-job-desc") as HTMLButtonElement | null;
  const fullDescEl = $("#job-desc-full");

  if (snippetEl) {
    if (desc) {
      snippetEl.textContent = desc.slice(0, 240) + (desc.length > 240 ? "…" : "");
      snippetEl.style.display = "-webkit-box";
    } else {
      snippetEl.style.display = "none";
    }
  }

  if (toggleBtn && fullDescEl) {
    if (desc && desc.length > 200) {
      toggleBtn.style.display = "inline-flex";
      fullDescEl.textContent = desc;
      let expanded = false;
      toggleBtn.onclick = () => {
        expanded = !expanded;
        fullDescEl.style.display = expanded ? "block" : "none";
        toggleBtn.textContent = expanded ? "Hide description ▴" : "Show full description ▾";
      };
    } else {
      toggleBtn.style.display = "none";
      fullDescEl.style.display = "none";
    }
  }

  const copyDetailsBtn = $("#copy-job-details") as HTMLButtonElement | null;
  if (copyDetailsBtn) {
    copyDetailsBtn.onclick = async () => {
      const textToCopy = formatJobDetails(job ?? {}, pageUrl);
      try {
        await navigator.clipboard.writeText(textToCopy);
        flashCopied(copyDetailsBtn, "✓ Copied Details!");
        toast("Job details copied to clipboard!");
      } catch {
        toast("Could not copy job details.");
      }
    };
  }

  const copyTitleBtn = $("#copy-job-title") as HTMLButtonElement | null;
  if (copyTitleBtn) {
    copyTitleBtn.disabled = !title;
    copyTitleBtn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(title);
        flashCopied(copyTitleBtn, "✓ Copied!");
        toast("Job title copied!");
      } catch {
        toast("Could not copy title.");
      }
    };
  }

  const copyDescBtn = $("#copy-job-desc") as HTMLButtonElement | null;
  if (copyDescBtn) {
    copyDescBtn.disabled = !desc;
    copyDescBtn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(desc);
        flashCopied(copyDescBtn, "✓ Copied!");
        toast("Job description copied!");
      } catch {
        toast("Could not copy description.");
      }
    };
  }
}

function renderRelevance(rel?: RelevanceResult): void {
  const card = $("#relevance-card");
  if (!card) return;
  if (!rel) {
    card.style.display = "none";
    return;
  }
  card.style.display = "block";
  const scoreBadge = $("#rel-score-badge");
  scoreBadge.textContent = `${rel.score}% Match`;
  scoreBadge.className = `badge ${rel.score >= 80 ? "badge-ok" : rel.score >= 60 ? "badge-rev" : "badge-muted"}`;

  const scoreBar = $("#rel-score-bar-fill");
  if (scoreBar) {
    scoreBar.style.width = `${Math.min(100, Math.max(0, rel.score))}%`;
    scoreBar.className = `rel-bar-fill ${rel.score >= 80 ? "fill-ok" : rel.score >= 60 ? "fill-rev" : "fill-muted"}`;
  }

  $("#rel-fit-label").textContent = rel.fitLabel;
  const wpEl = $("#rel-workplace");
  if (wpEl) {
    const parts: string[] = [];
    if (rel.workplaceType && rel.workplaceType !== "Unknown") {
      parts.push(rel.workplaceType === "Remote" ? "🏡 Remote" : rel.workplaceType === "Hybrid" ? "🏢 Hybrid" : `📍 ${rel.workplaceType}`);
    }
    if (rel.salaryRange) {
      parts.push(`💰 ${rel.salaryRange}`);
    }
    wpEl.textContent = parts.length > 0 ? parts.join(" · ") : (rel.workplaceType ?? "");
    wpEl.style.display = wpEl.textContent ? "inline-flex" : "none";
  }

  const skillsRow = $("#rel-skills-row");
  skillsRow.textContent = "";
  for (const s of rel.matchedSkills.slice(0, 5)) {
    const chip = document.createElement("span");
    chip.className = "badge badge-ok";
    chip.textContent = `✓ ${s}`;
    chip.title = `Matched in your profile: ${s}`;
    skillsRow.append(chip);
  }
  for (const s of rel.missingSkills.slice(0, 3)) {
    const chip = document.createElement("span");
    chip.className = "badge badge-muted";
    chip.textContent = `+ ${s}`;
    chip.title = `Mentioned in job description: ${s}`;
    skillsRow.append(chip);
  }
  const totalExtra = Math.max(0, rel.matchedSkills.length - 5) + Math.max(0, rel.missingSkills.length - 3);
  if (totalExtra > 0) {
    const moreChip = document.createElement("span");
    moreChip.className = "badge badge-muted";
    moreChip.textContent = `+${totalExtra} more`;
    moreChip.title = `${totalExtra} additional skills analyzed`;
    skillsRow.append(moreChip);
  }

  const dbEl = $("#rel-dealbreaker");
  if (dbEl) {
    if (rel.dealbreakers.length > 0) {
      dbEl.textContent = `⛔ Dealbreaker: ${rel.dealbreakers.join("; ")}`;
      dbEl.style.display = "block";
    } else {
      dbEl.style.display = "none";
    }
  }
}

function renderMissedChecklist(audit?: FormAuditReport): void {
  const card = $("#missed-card");
  if (!card) return;
  if (!audit || audit.missedCount === 0) {
    card.style.display = "none";
    return;
  }
  card.style.display = "block";
  $("#missed-count-badge").textContent = `${audit.missedCount} Missed`;

  const list = $("#missed-list");
  list.textContent = "";
  for (const m of audit.missed) {
    const li = document.createElement("li");

    const descWrap = document.createElement("div");
    descWrap.style.cssText = "display:flex;align-items:center;gap:6px;flex:1;overflow:hidden;";

    const icon = document.createElement("span");
    icon.textContent = m.type === "required" ? "⚠️" : "❌";
    icon.style.cssText = "font-size:12px;flex-shrink:0;";

    const desc = document.createElement("span");
    desc.textContent = m.description;
    desc.style.cssText = "overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11.5px;";
    desc.title = m.description;

    descWrap.append(icon, desc);

    const jump = document.createElement("button");
    jump.className = "sm";
    jump.textContent = "Jump";
    jump.title = `Scroll to "${m.label}" and highlight on page`;
    jump.onclick = async () => {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      if (tab?.id !== undefined) {
        if (m.frameId !== undefined) {
          await chrome.tabs.sendMessage(tab.id, { type: "jumpToMissed", index: m.refIndex }, { frameId: m.frameId });
        } else {
          await chrome.tabs.sendMessage(tab.id, { type: "jumpToMissed", index: m.refIndex });
        }
        flashCopied(jump, "✓ Focused");
        toast(`Jumped to "${m.label}"`);
      }
    };

    li.append(descWrap, jump);
    list.append(li);
  }
}

function renderScraper(): void {
  const list = $("#scraper-list");
  if (!list) return;

  const q = ($("#scraper-filter") as HTMLInputElement | null)?.value.toLowerCase() ?? "";
  const minScore = parseInt(($("#scraper-score-filter") as HTMLSelectElement | null)?.value ?? "0", 10);
  const remoteOnly = ($("#scraper-remote-only") as HTMLInputElement | null)?.checked ?? false;

  const filtered = scrapedJobs.filter(j => {
    if (j.relevance.score < minScore) return false;
    if (remoteOnly && j.relevance.workplaceType !== "Remote" && !j.location.toLowerCase().includes("remote")) return false;
    if (q) {
      const haystack = `${j.title} ${j.company} ${j.location} ${j.snippet} ${j.relevance.matchedSkills.join(" ")}`.toLowerCase();
      if (!haystack.includes(q)) return false;
    }
    return true;
  });

  const countEl = $("#scraper-count");
  if (countEl) countEl.textContent = `${filtered.length} of ${scrapedJobs.length} jobs`;

  list.textContent = "";
  const empty = $("#scraper-empty");
  if (filtered.length === 0) {
    if (empty) {
      empty.hidden = false;
      list.append(empty);
    }
    return;
  }

  for (const j of filtered) {
    const card = document.createElement("div");
    card.className = "section-card";
    card.style.cssText = "margin-bottom:8px;padding:10px 12px;border:1px solid var(--border);border-radius:var(--radius);background:var(--surface);";

    const topRow = document.createElement("div");
    topRow.className = "row-between";
    topRow.style.margin = "0 0 4px";

    const titleEl = document.createElement("a");
    titleEl.href = j.url;
    titleEl.target = "_blank";
    titleEl.textContent = j.title;
    titleEl.style.cssText = "font-weight:600;font-size:13px;color:var(--text);text-decoration:none;max-width:320px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;";
    titleEl.title = j.title;

    const scoreBadge = document.createElement("span");
    scoreBadge.className = `badge ${j.relevance.score >= 80 ? "badge-ok" : j.relevance.score >= 60 ? "badge-rev" : "badge-muted"}`;
    scoreBadge.textContent = `${j.relevance.score}% Match`;
    topRow.append(titleEl, scoreBadge);

    const subRow = document.createElement("div");
    subRow.className = "row";
    subRow.style.cssText = "margin:2px 0 6px;gap:6px;font-size:11px;color:var(--muted);";

    const compSpan = document.createElement("span");
    compSpan.style.fontWeight = "600";
    compSpan.textContent = j.company || "Company";

    const locSpan = document.createElement("span");
    locSpan.textContent = `• ${j.location || "Remote/Unspecified"}`;

    subRow.append(compSpan, locSpan);
    if (j.salary) {
      const salSpan = document.createElement("span");
      salSpan.className = "badge badge-muted";
      salSpan.textContent = j.salary;
      subRow.append(salSpan);
    }

    const skillsRow = document.createElement("div");
    skillsRow.className = "row";
    skillsRow.style.cssText = "margin:4px 0 8px;gap:4px;";
    for (const s of j.relevance.matchedSkills.slice(0, 4)) {
      const chip = document.createElement("span");
      chip.className = "badge badge-ok";
      chip.style.fontSize = "10px";
      chip.textContent = s;
      skillsRow.append(chip);
    }

    const actRow = document.createElement("div");
    actRow.className = "row";
    actRow.style.cssText = "margin:0;justify-content:flex-end;gap:6px;";

    const copyDetailsBtn = document.createElement("button");
    copyDetailsBtn.className = "sm";
    copyDetailsBtn.textContent = "Copy details";
    copyDetailsBtn.title = "Copy job title, company, URL and description snippet to clipboard";
    copyDetailsBtn.onclick = async () => {
      const textToCopy = formatJobDetails({ title: j.title, company: j.company, description: j.snippet }, j.url);
      try {
        await navigator.clipboard.writeText(textToCopy);
        flashCopied(copyDetailsBtn, "✓ Copied");
        toast("Job details copied to clipboard!");
      } catch {
        toast("Could not copy job details.");
      }
    };

    const copyBtn = document.createElement("button");
    copyBtn.className = "sm";
    copyBtn.textContent = "Copy link";
    copyBtn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(j.url);
        flashCopied(copyBtn, "✓ Copied");
        toast("Link copied to clipboard!");
      } catch {
        toast("Could not copy link.");
      }
    };

    const trackBtn = document.createElement("button");
    trackBtn.className = "sm";
    trackBtn.textContent = "Track in Vani";
    trackBtn.onclick = async () => {
      trackBtn.disabled = true;
      try {
        const r = await sendToVani({ pageUrl: scrapedPage.pageUrl, pageTitle: scrapedPage.pageTitle, jobs: [j] }, false);
        status(r.ok ? `Vani: ${r.text}.` : r.text, r.ok ? "ok" : "error");
        toast(r.ok ? `Sent "${j.title.slice(0, 60)}" to Vani.` : "Vani did not take the job.");
      } finally {
        trackBtn.disabled = false;
      }
    };

    const openBtn = document.createElement("button");
    openBtn.className = "sm primary";
    openBtn.textContent = "Open role";
    openBtn.onclick = () => {
      window.open(j.url, "_blank");
    };

    actRow.append(copyDetailsBtn, copyBtn, trackBtn, openBtn);
    card.append(topRow, subRow, skillsRow, actRow);
    list.append(card);
  }
}

function initScraper(): void {
  const scrapeBtn = $("#scrape-page-btn");
  if (scrapeBtn) {
    scrapeBtn.onclick = async () => {
      const b = scrapeBtn as HTMLButtonElement;
      b.disabled = true;
      b.textContent = "Scraping page...";
      try {
        const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
        if (tab?.id !== undefined) {
          // The top frame answers: its URL is the listing page's (every frame has a content script and would answer otherwise).
          const r = await chrome.tabs.sendMessage<ScrapeResult>(tab.id, { type: "scrapeJobs" }, { frameId: 0 }).catch(() => null);
          if (r && Array.isArray(r.jobs)) {
            scrapedJobs = r.jobs;
            scrapedPage = { pageUrl: typeof r.pageUrl === "string" ? r.pageUrl : "", pageTitle: typeof r.pageTitle === "string" ? r.pageTitle : "" };
            renderScraper();
            toast(`Scraped ${r.jobs.length} jobs!`);
          } else {
            toast("No jobs found to scrape on this page.");
          }
        }
      } catch (e) {
        toast(`Scrape failed: ${e instanceof Error ? e.message : String(e)}`);
      } finally {
        b.disabled = false;
        b.textContent = "🔍 Scrape Jobs on This Page";
      }
    };
  }

  const saveBtn = $("#scraper-save") as HTMLButtonElement | null;
  if (saveBtn) {
    saveBtn.onclick = async () => {
      const out = $("#scraper-save-result");
      if (scrapedJobs.length === 0) { if (out) out.textContent = "Scrape a page first."; return; }
      saveBtn.disabled = true;
      if (out) out.textContent = "Sending…";
      try {
        const again = ($("#scraper-save-search") as HTMLInputElement | null)?.checked ?? false;
        const r = await sendToVani({ ...scrapedPage, jobs: scrapedJobs }, again);
        if (out) out.textContent = r.text;
        status(r.ok ? `Vani: ${r.text}.` : r.text, r.ok ? "ok" : "error");
      } finally {
        saveBtn.disabled = false;
      }
    };
  }

  const exportCsv = $("#export-csv");
  if (exportCsv) {
    exportCsv.onclick = () => {
      if (scrapedJobs.length === 0) { toast("No scraped jobs to export."); return; }
      const csv = exportJobsAsCSV(scrapedJobs);
      const blob = new Blob([csv], { type: "text/csv" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `vani-jobs-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      toast("Exported jobs to CSV!");
    };
  }

  const exportJson = $("#export-json");
  if (exportJson) {
    exportJson.onclick = () => {
      if (scrapedJobs.length === 0) { toast("No scraped jobs to export."); return; }
      const json = exportJobsAsJSON(scrapedJobs);
      const blob = new Blob([json], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `vani-jobs-${new Date().toISOString().slice(0, 10)}.json`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      toast("Exported jobs to JSON!");
    };
  }

  const scraperFilter = $("#scraper-filter");
  if (scraperFilter) scraperFilter.oninput = renderScraper;

  const scoreFilter = $("#scraper-score-filter");
  if (scoreFilter) scoreFilter.onchange = renderScraper;

  const remoteOnly = $("#scraper-remote-only");
  if (remoteOnly) remoteOnly.onchange = renderScraper;
}

async function init(): Promise<void> {
  initTabs();
  initScraper();
  const gotoScraper = $("#btn-goto-scraper");
  if (gotoScraper) {
    // "Scrape this page": open the scraper and run its own Scrape button (the existing scrapeJobs message).
    gotoScraper.onclick = () => {
      openTab("scraper");
      $("#scrape-page-btn")?.click();
    };
  }
  $("#btn-goto-basics").onclick = () => openTab("basics");
  $("#btn-goto-answers").onclick = () => openTab("answers");
  $("#btn-profile-next").onclick = () => openTab($("#btn-profile-next").dataset.tab ?? "basics");
  const btnRescan = $("#btn-rescan");
  if (btnRescan) {
    btnRescan.onclick = () => void rescanTab(true);
  }
  const btnEmptyRescan = $("#btn-empty-rescan");
  if (btnEmptyRescan) {
    btnEmptyRescan.onclick = () => void rescanTab(true);
  }

  // Keyboard shortcut for re-scan: ⌥⇧R
  window.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.altKey && e.shiftKey && (e.key === "R" || e.key === "r")) {
      e.preventDefault();
      void rescanTab(true);
    }
  });

  await rescanTab(false);
}

async function rescanTab(notify = false): Promise<void> {
  const rescanBtn = $("#btn-rescan");
  if (rescanBtn) rescanBtn.classList.add("spinning");
  const emptyRescan = $("#btn-empty-rescan") as HTMLButtonElement | null;
  if (emptyRescan) emptyRescan.disabled = true;

  try {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    host = tab?.url ? new URL(tab.url).hostname : "";
    const hostEl = $("#host");
    if (hostEl) hostEl.textContent = host || "(no page)";
    if (tab?.id !== undefined) void appliedRow(tab.id, tab.url);
    void resumeRow(tab?.url, { row: $("#resume"), name: $("#resume-name"), pick: $("#resume-pick"), remove: $("#resume-remove") }, bg);
    await refresh();

    const t = await scanTabExtended();
    activeJob = t.job;
    renderFrames(t.tabCoverage);
    renderPortal(t.portal);
    renderJobDetails(t.job, tab?.url);
    renderRelevance(t.relevance);
    renderMissedChecklist(t.audit);
    // The scan only reports "disabled" for frames that found fields; the saved setting says it for the host outright.
    const off = t.tabCoverage.disabled || (host !== "" && current?.settings?.disabledHosts?.includes(host) === true);
    const view = homeState({
      found: off ? 0 : t.tabCoverage.total.found,
      disabled: off,
      portal: t.portal,
      job: t.job,
      relevance: t.relevance,
    });
    if (view === "form") {
      status(coverageLine(t.tabCoverage.total) + (t.tabCoverage.frames.length > 1 ? ` Across ${t.tabCoverage.frames.length} frames.` : ""));
      renderCoverage(t.tabCoverage.total);
    } else {
      if (current) status(""); // keep "Could not read your saved answers" when that is why the dashboard has no profile
      updateMeter(t.tabCoverage.total);
    }
    renderJobActions(t.job, tab?.url);
    setHomeView(view);
    if (notify) toast("Tab scan refreshed!");
  } catch (e) {
    statusError(e);
    renderPortal(undefined);
    setHomeView("error");
    updateMeter({
      found: 0,
      known: 0,
      fillable: 0,
      unknown: 0,
      manual: 0,
      skipped: 0,
      capped: false,
      unread: 0,
      unknownLabels: [],
      unknownListed: 0,
      manualLabels: [],
    });
    if (notify) toast("Re-scan failed.");
  } finally {
    if (rescanBtn) rescanBtn.classList.remove("spinning");
    if (emptyRescan) emptyRescan.disabled = false;
  }
}

$("#fill").onclick = async () => {
  const done = busy($("#fill") as HTMLButtonElement | null, "Filling…");
  try {
    const r = await toFrames<FillReply>({ type: "fill", overwrite: ($("#overwrite") as HTMLInputElement).checked, typeahead: ($("#typeahead") as HTMLInputElement).checked }, { registered });
    const outcomes = mergeFill(r);
    const f = outcomes.filter(o => o.filled);
    const manual = outcomes.filter(o => !o.filled && o.reason === "manual").length;
    status(`Filled ${f.length} (${f.filter(o => o.review).length} to review)` + (manual ? `, ${manual} to do by hand` : "") +
      (r.answered.length > 1 ? ` across ${r.answered.length} frames` : "") + ". Check the page, then submit yourself." + notAnswered(r), "ok");
    toast(`Filled ${f.length} fields!`);
    await refresh();
    const t = await scanTabExtended().catch(() => null);
    if (t) {
      renderMissedChecklist(t.audit);
      renderCoverage(t.tabCoverage.total);
    }
  } catch (e) {
    statusError(e, "Fill failed");
  } finally {
    done();
  }
};

$("#save").onclick = async () => {
  const done = busy($("#save") as HTMLButtonElement | null, "Saving…");
  try {
    const r = await toFrames<CaptureReply>({ type: "capture" }, { registered });
    const c = sumCaptures(r);
    status(`Saved ${c.added + c.updated} answers` + (c.unchanged ? ` (${c.unchanged} already saved)` : "") + (c.skippedSecret ? `; ${c.skippedSecret} sensitive fields not saved.` : ".") + notAnswered(r), "ok");
    toast(`Saved ${c.added + c.updated} answers!`);
    await refresh();
  } catch (e) {
    statusError(e, "Save failed");
  } finally {
    done();
  }
};

const clearMarksBtn = $("#clear-marks");
if (clearMarksBtn) {
  clearMarksBtn.onclick = async () => {
    try {
      await toFrames({ type: "clearMarks" }, { registered });
      toast("Field highlights cleared from page.");
    } catch (e) {
      statusError(e, "Could not clear highlights");
    }
  };
}

$("#basic-save").onclick = () => { void saveBasics(); };
$("#filter").oninput = render;
$("#selfid").onchange = async () => {
  await bg({ type: "setSettings", settings: { captureSelfId: ($("#selfid") as HTMLInputElement).checked } });
  await refresh();
};
$("#disabled").onchange = async () => {
  if (!current || !host) return;
  const on = ($("#disabled") as HTMLInputElement).checked;
  const hosts = current.settings.disabledHosts.filter(h => h !== host);
  if (on) hosts.push(host);
  await bg({ type: "setSettings", settings: { disabledHosts: hosts } });
  await refresh();
};
$("#export").onclick = () => {
  if (!current) return;
  const url = URL.createObjectURL(new Blob([exportBank(current)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url; a.download = `vani-answers-${new Date().toISOString().slice(0, 10)}.json`; a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  toast("Answers exported to JSON.");
};
$("#import").onchange = async ev => {
  const input = ev.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  if (!confirm("Replace all saved answers with this file?")) return;
  try {
    const r = await bg<{ ok?: boolean; answers?: number; droppedSecret?: number; error?: string }>({ type: "replaceBank", json: await file.text() });
    status(r.error ? `Import refused: ${r.error}` : `Imported ${r.answers} answers` + (r.droppedSecret ? `; ${r.droppedSecret} sensitive entries dropped.` : "."));
    toast(r.error ? "Import refused" : `Imported ${r.answers} answers!`);
    await refresh();
  } catch (e) { status(`Import failed: ${e instanceof Error ? e.message : String(e)}`); }
};

buildBasics();
void init();
