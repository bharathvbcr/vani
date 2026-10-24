// The popup's "Resume for this job" row: shows which PDF is bound to the active
// tab's job page, binds one (Choose PDF), opens it in Preview (View) and unbinds it
// (Remove). The bytes stay in the Mac app. View asks the app to open Preview; the
// popup never receives the file.
import type { ToBackground } from "./messages";
import { MAX_RESUME_BYTES, parseResumeInfo, parseResumeOpen, resumeKey, toBase64 } from "./resume";

export interface ResumeRowEls {
  row: HTMLElement;
  name: HTMLElement;
  status: HTMLElement;
  pick: HTMLInputElement;
  view: HTMLButtonElement;
  remove: HTMLButtonElement;
}

type Send = <T>(msg: ToBackground) => Promise<T>;

const kb = (n: number) => `${Math.max(1, Math.round(n / 1024))} KB`;

function asLive(v: string | { url?: string; tabId?: number } | undefined): { url?: string; tabId?: number } {
  if (typeof v === "string") return { url: v };
  return v ?? {};
}

export async function resumeRow(
  tabUrl: string | undefined,
  els: ResumeRowEls,
  send: Send,
  /** The tab at the moment View is clicked. A string is its URL. The row was drawn for `tabUrl`. */
  liveURL?: () => Promise<string | { url?: string; tabId?: number } | undefined>,
  /** The tab the row was drawn for. The background re-reads this tab; the message never carries its URL as the page to open. */
  tabId?: number,
): Promise<void> {
  if (!tabUrl || !resumeKey(tabUrl)) { els.row.hidden = true; return; }
  els.row.hidden = false;
  let pageURL = tabUrl;
  let boundTab = tabId;
  /** The posting key the row is showing. View, Choose and Remove send this, and only while the tab is still this page. */
  let shownKey: string | null = resumeKey(tabUrl);

  /** The live tab must still be the posting this row was drawn for. A move or a failed read does not open, choose, or remove. */
  async function stillThisPage(action: "opened" | "chosen" | "removed"): Promise<boolean> {
    if (!liveURL) return true;
    let now: { url?: string; tabId?: number };
    try { now = asLive(await liveURL()); } catch { now = {}; }
    if (now.url === undefined) {
      els.status.hidden = false;
      els.status.textContent = `Could not check which page is open, so the resume was not ${action}.`;
      return false;
    }
    if (now.tabId !== undefined) boundTab = now.tabId;
    if (resumeKey(now.url) !== resumeKey(pageURL)) {
      pageURL = now.url;
      if (resumeKey(now.url)) await show();
      else {
        els.name.textContent = "this page is not a job page";
        els.view.hidden = true;
        els.remove.hidden = true;
      }
      els.status.hidden = false;
      els.status.textContent = `This page changed, so that resume was not ${action}.`;
      return false;
    }
    return true;
  }

  async function show(): Promise<void> {
    let info;
    try {
      info = parseResumeInfo(await send<unknown>(
        boundTab === undefined ? { type: "resumeInfo", url: pageURL } : { type: "resumeInfo", url: pageURL, tabId: boundTab },
      ));
    } catch (e) { info = { error: String(e) }; }
    els.remove.hidden = true;
    els.view.hidden = true;
    els.status.hidden = true;
    els.status.textContent = "";
    shownKey = resumeKey(pageURL);
    if ("error" in info) {
      els.name.textContent = /reachable/.test(info.error)
        ? "needs the Vani Apply Mac app, which keeps the resume"
        : `could not be read: ${info.error}`;
    } else if (!info.found) {
      els.name.textContent = info.several
        ? "More than one resume is saved on this page."
        : "none chosen; Fill leaves the resume box empty";
    } else {
      shownKey = info.key ?? shownKey;
      els.name.textContent = `${info.name} (${kb(info.bytes)}${info.pickedAt ? `, chosen ${info.pickedAt.slice(0, 10)}` : ""})`;
      els.remove.hidden = false;
      els.view.hidden = false;
    }
  }

  els.view.onclick = async () => {
    els.view.disabled = true;
    els.status.hidden = false;
    try {
      // View opens the posting this row is showing. A job board can change the posting
      // while the popup stays open; do not open, choose, or remove that other file.
      if (!await stillThisPage("opened")) return;
      els.status.textContent = "Opening in Preview…";
      const key = shownKey ?? resumeKey(pageURL);
      const r = parseResumeOpen(await send<unknown>({
        type: "resumePreview",
        ...(key ? { key } : {}),
        ...(boundTab !== undefined ? { tabId: boundTab } : {}),
      }));
      if ("ok" in r) els.status.textContent = `Opened ${r.name} in Preview`;
      else if ("found" in r && r.changed) {
        // The background read the tab after we did. If that page is already a different posting, show it.
        if (liveURL) {
          try {
            const again = asLive(await liveURL());
            if (again.tabId !== undefined) boundTab = again.tabId;
            if (again.url && resumeKey(again.url) && resumeKey(again.url) !== resumeKey(pageURL)) {
              pageURL = again.url;
              await show();
            }
          } catch { /* the sentence below still says we did not open */ }
        }
        els.status.hidden = false;
        els.status.textContent = "This page changed, so that resume was not opened.";
      } else if ("found" in r && r.several) {
        els.view.hidden = true;
        els.remove.hidden = true;
        els.name.textContent = "More than one resume is saved on this page.";
        els.status.textContent = "More than one resume is saved on this page, so none was opened.";
      } else if ("found" in r) {
        els.name.textContent = "none chosen; Fill leaves the resume box empty";
        els.status.textContent = "No resume is saved for this page";
        els.view.hidden = true;
        els.remove.hidden = true;
      } else els.status.textContent = `Could not open it: ${r.error}`;
    } catch (e) {
      els.status.textContent = `Could not open it: ${e instanceof Error ? e.message : String(e)}`;
    } finally {
      els.view.disabled = false;
    }
  };

  els.pick.onchange = async () => {
    const file = els.pick.files?.[0];
    els.pick.value = ""; // so the same file can be chosen again
    if (!file) return;
    if (!await stillThisPage("chosen")) return;
    if (file.size > MAX_RESUME_BYTES) { els.name.textContent = `${file.name} is over ${MAX_RESUME_BYTES / 1024} KB; not chosen`; return; }
    try {
      const pdf = toBase64(new Uint8Array(await file.arrayBuffer()));
      const r = await send<{ ok?: boolean; error?: string }>({ type: "resumePut", url: shownKey ?? pageURL, name: file.name, pdf });
      if (r?.error) { els.name.textContent = `not chosen: ${r.error}`; return; }
    } catch (e) {
      els.name.textContent = `not chosen: ${e instanceof Error ? e.message : String(e)}`;
      return;
    }
    await show();
  };

  els.remove.onclick = async () => {
    if (!await stillThisPage("removed")) return;
    try {
      const r = await send<{ ok?: boolean; error?: string }>({ type: "resumeDelete", url: shownKey ?? pageURL });
      if (r?.error) { els.name.textContent = `not removed: ${r.error}`; return; }
    } catch (e) {
      els.name.textContent = `not removed: ${e instanceof Error ? e.message : String(e)}`;
      return;
    }
    await show();
  };

  await show();
}
