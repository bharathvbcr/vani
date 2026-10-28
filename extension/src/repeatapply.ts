// The "you already applied to this company lately" notice. Vani's `jobs find` attaches it to the
// jobFind reply (cmd/vani/jobsfind.go, internal/jobview/same.go) when other postings at the page's
// company were applied to inside the window some application systems silently reject a repeat
// application in. It is a note on the page's panel and nothing more: it never blocks, delays or
// changes Fill, Suggest or Save, and an absent, old or malformed note simply shows nothing. The
// pieces here have no chrome.* and need no real DOM, so `bun test` covers each.

const MAX_TEXT = 600;
const MAX_FIELD = 200;

/** The note as vani words it (jobview.RepeatApplyNote). `text` is the sentence to show. */
export interface RepeatApply {
  count: number;
  windowDays: number;
  latestTitle: string;
  latestAt: string;
  text: string;
}

function clip(x: unknown, max: number): string {
  return typeof x === "string" ? x.replace(/[\u0000-\u001f\u007f-\u009f]/g, " ").replace(/\s+/g, " ").trim().slice(0, max) : "";
}

const isCount = (x: unknown, max: number): x is number => typeof x === "number" && Number.isInteger(x) && x >= 1 && x <= max;

/** One note off the wire, or null when it is missing or not trustworthy (nothing is shown then). */
export function parseRepeatApply(x: unknown): RepeatApply | null {
  if (typeof x !== "object" || x === null || Array.isArray(x)) return null;
  const o = x as Record<string, unknown>;
  const text = clip(o.text, MAX_TEXT);
  if (text === "" || !isCount(o.count, 100_000) || !isCount(o.window_days, 3650)) return null;
  return { count: o.count, windowDays: o.window_days, latestTitle: clip(o.latest_title, MAX_FIELD), latestAt: clip(o.latest_at, 40), text };
}

/**
 * The note in a host jobFind reply (`job.repeatApply`), or null: the page is not a tracked posting,
 * the reply predates the field, or the note is malformed.
 */
export function parseFindRepeatApply(reply: unknown): RepeatApply | null {
  if (typeof reply !== "object" || reply === null) return null;
  const r = reply as Record<string, unknown>;
  if (r.found !== true || typeof r.job !== "object" || r.job === null) return null;
  return parseRepeatApply((r.job as Record<string, unknown>).repeatApply);
}

/** What the notice needs of an element and of the document: small enough to fake in a test. */
export interface NoticeEl<E> {
  textContent: string | null;
  title: string;
  style: { display: string; cssText: string };
  append(...children: E[]): void;
  replaceChildren(...children: E[]): void;
  addEventListener(type: "click", fn: () => void): void;
}
export interface NoticeDoc<E> {
  createElement(tag: string): E;
}

export interface RepeatNotice {
  /** Show the note for `page`, or hide the drawer when there is none (or it was dismissed for this page). */
  show(note: { text: string } | undefined, page: string): void;
}

/**
 * The drawer's behaviour. A dismissal lasts for that page: scans run on page activity and must not
 * bring back a note the person closed.
 */
export function createRepeatNotice<E extends NoticeEl<E>>(doc: NoticeDoc<E>, drawer: E): RepeatNotice {
  let dismissedFor: string | null = null;
  return {
    show(note, page) {
      if (!note || dismissedFor === page) {
        drawer.replaceChildren();
        drawer.style.display = "none";
        return;
      }
      const row = doc.createElement("div");
      row.style.cssText = "display:flex;justify-content:space-between;gap:8px;align-items:flex-start;";
      const body = doc.createElement("span");
      body.textContent = note.text;
      const close = doc.createElement("span");
      close.style.cssText = "cursor:pointer;color:#94a3b8;flex:none;";
      close.title = "Dismiss";
      close.textContent = "x";
      close.addEventListener("click", () => {
        dismissedFor = page;
        drawer.replaceChildren();
        drawer.style.display = "none";
      });
      row.append(body, close);
      drawer.replaceChildren(row);
      drawer.style.display = "block";
    },
  };
}
