// Background service worker: the single writer to the answer bank, and the
// keyboard shortcut. It makes no network requests.
import type { Bank } from "./types";
import { type CaptureResult, MAX_OPTIONS, MAX_VALUE_CHARS, mergeCapture, parseBank } from "./bank";
import { logActivity, loadBank, migrationNote, replaceBank, storageMode, updateBank } from "./store";
import type { ToBackground, ToContent } from "./messages";
import { DRAFT_HELP, createDrafter } from "./draft";

const draft = createDrafter();

function str(x: unknown): string {
  return typeof x === "string" ? x : "";
}

// Serialize every read-modify-write: two tabs capturing at once must not lose
// one another's answers. (The store also retries on a conflicting write from
// another process, e.g. the Safari extension.)
let chain: Promise<unknown> = Promise.resolve();
function locked<T>(fn: () => Promise<T>): Promise<T> {
  const next = chain.then(fn, fn);
  chain = next.catch(() => undefined);
  return next;
}

async function update(fn: (b: Bank) => Bank): Promise<Bank> {
  return locked(() => updateBank(fn));
}

async function handle(msg: ToBackground): Promise<unknown> {
  switch (msg.type) {
    case "getBank": {
      const bank = await loadBank();
      return { bank, mode: await storageMode(), note: migrationNote() };
    }
    case "capture": {
      if (!Array.isArray(msg.fields) || msg.fields.length > 500 || typeof msg.host !== "string") return { error: "bad capture" };
      let result: Omit<CaptureResult, "bank"> | undefined;
      await update(b => {
        const { bank, ...counts } = mergeCapture(b, msg.fields, msg.host, new Date());
        result = counts;
        return bank;
      });
      if (result) void logActivity({ event: "capture", host: msg.host, counts: { ...result } });
      return result;
    }
    case "filled": {
      const keys = msg.keys.slice(0, 500);
      await update(b => {
        const answers = { ...b.answers };
        for (const k of keys) if (answers[k]) answers[k] = { ...answers[k]!, fills: answers[k]!.fills + 1 };
        return { ...b, answers };
      });
      void logActivity({ event: "fill", counts: { filled: keys.length } });
      return { ok: true };
    }
    case "setSettings":
      await update(b => ({
        ...b,
        settings: {
          captureSelfId: typeof msg.settings.captureSelfId === "boolean" ? msg.settings.captureSelfId : b.settings.captureSelfId,
          disabledHosts: Array.isArray(msg.settings.disabledHosts) ? msg.settings.disabledHosts.slice(0, 500).map(String) : b.settings.disabledHosts,
        },
      }));
      void logActivity({ event: "settings" });
      return { ok: true };
    case "deleteAnswer":
      await update(b => {
        const answers = { ...b.answers };
        delete answers[msg.key];
        return { ...b, answers };
      });
      void logActivity({ event: "delete" });
      return { ok: true };
    case "editAnswer":
      await update(b => {
        const a = b.answers[msg.key];
        if (!a || !Array.isArray(msg.value)) return b;
        const value = msg.value.slice(0, MAX_OPTIONS).map(v => String(v).slice(0, MAX_VALUE_CHARS)).filter(Boolean);
        return { ...b, answers: { ...b.answers, [msg.key]: { ...a, value, updatedAt: new Date().toISOString() } } };
      });
      void logActivity({ event: "edit" });
      return { ok: true };
    case "draft": {
      // Messages cross a trust boundary (a compromised page can't send these,
      // but a buggy content script can): re-check the shape here.
      const job = typeof msg.job === "object" && msg.job !== null ? msg.job : { company: "", title: "", description: "" };
      const r = await draft(str(msg.question), { company: str(job.company), title: str(job.title), description: str(job.description) },
        typeof msg.maxWords === "number" ? msg.maxWords : undefined);
      // The user sees our explanation; the host's own text is kept only where it adds detail.
      if (!r.ok && r.code !== "provider" && r.code !== "bad_request") return { ...r, error: DRAFT_HELP[r.code] };
      return r;
    }
    case "replaceBank": {
      const { bank, droppedSecret } = parseBank(msg.json); // throws on a malformed file
      await locked(() => replaceBank(bank));
      const answers = Object.keys(bank.answers).length;
      void logActivity({ event: "import", counts: { answers, droppedSecret } });
      return { ok: true, answers, droppedSecret };
    }
  }
}

chrome.runtime.onMessage.addListener((msg: ToBackground, _sender, sendResponse) => {
  handle(msg).then(sendResponse, (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
  return true; // reply asynchronously
});

/** Send a command to the active tab, injecting the content script on sites it does not run on by default. */
export async function toActiveTab(msg: ToContent): Promise<unknown> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (tab?.id === undefined) throw new Error("no active tab");
  try {
    return await chrome.tabs.sendMessage(tab.id, msg);
  } catch {
    await chrome.scripting.executeScript({ target: { tabId: tab.id, allFrames: true }, files: ["content.js"] });
    return chrome.tabs.sendMessage(tab.id, msg);
  }
}

chrome.commands.onCommand.addListener(command => {
  if (command === "fill-form") void toActiveTab({ type: "fill" }).catch(() => undefined);
});
