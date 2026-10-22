// Offering to mark a job Applied. When the user presses Submit on a page that is
// a posting Vani tracks, the Jobs tab should say Applied, but only if the user
// agrees: the extension never marks anything by itself. The pieces here have no
// DOM and no chrome.* in them, so `bun test` covers each:
//
//   pageKey          page URL -> scheme, host, path and the parameters that name a job
//                    (the rest of the query and the fragment are dropped before the URL
//                    leaves the browser; internal/joburl is the Go twin, and
//                    test/vectors/joburl.json holds the cases both must agree on)
//   postingKey       pageKey with one key per Workday posting across its routes (the per-job
//                    resume's key; pageKey itself stays what jobFind sends to Go)
//   isFinalSubmit    the button label that means "send the application"
//   parseFind        the host's jobFind reply -> an offer, or nothing to offer
//   createBroker     the background's side: find on a submit signal (or the popup being
//                    opened on a tracked page), keep the offer for its tab (a confirmation
//                    page is a new document, so the page that asked is gone by the time
//                    the user sees the question), mark only the job that was offered,
//                    only when asked
//   createAppliedFlow the content script's side: ask, show, accept or dismiss
import { createLock } from "./guards";
import type { ToBackground } from "./messages";

/** internal/joburl.MaxBytes: a longer page URL is not sent, never cut. */
export const MAX_URL_BYTES = 2048;
const MAX_TEXT = 200;
/** An offer is forgotten after this long: a stale question about an old tab is worse than none. */
export const OFFER_TTL_MS = 10 * 60_000;
export const MAX_OFFERS = 32;
/** After "Not now", the same job is not asked about again for this long. */
export const DISMISS_QUIET_MS = 10_000;

const REFUSED = /[\s<>\\\u0000-\u001f\u007f-\u009f]/;
const HTTP_WITH_HOST = /^https?:\/\/[^/?#\s]/i;

/**
 * The query parameters that name a job on boards serving every posting from one path
 * (internal/joburl.IDParams, in the same order). Every other parameter stays in the browser.
 */
export const ID_PARAMS = ["gh_jid", "jid", "job_id", "jobid", "req_id"];
const ID_VALUE = /^[A-Za-z0-9_-]{1,64}$/;
const GH_BOARD = /^[A-Za-z0-9_-]{1,100}$/;
const GH_TOKEN = /^[0-9]{1,20}$/;

/**
 * Scheme, host and path of an http(s) page, plus the job-naming parameters, or null. A Greenhouse
 * embedded form is the posting it embeds. Mirrors internal/joburl.Normalize.
 */
export function pageKey(raw: unknown): string | null {
  if (typeof raw !== "string" || raw.length > MAX_URL_BYTES || new TextEncoder().encode(raw).length > MAX_URL_BYTES) return null;
  const s = raw.trim();
  if (s === "" || REFUSED.test(s) || !HTTP_WITH_HOST.test(s)) return null;
  let u: URL;
  try { u = new URL(s); } catch { return null; }
  if (u.protocol !== "http:" && u.protocol !== "https:") return null;
  let host = u.hostname.toLowerCase().replace(/\.$/, "");
  if (host === "") return null;
  if (host === "job-boards.greenhouse.io") host = "boards.greenhouse.io";
  const hostPort = `${host}${u.port ? `:${u.port}` : ""}`;
  let path = u.pathname.replace(/\/{2,}/g, "/");
  if (path.length > 1 && path.endsWith("/")) path = path.slice(0, -1);
  path = path || "/";
  if (hostPort === "boards.greenhouse.io" && path === "/embed/job_app") {
    const board = u.searchParams.get("for") ?? "", token = u.searchParams.get("token") ?? "";
    if (GH_BOARD.test(board) && GH_TOKEN.test(token)) return `${u.protocol}//${hostPort}/${board}/jobs/${token}`;
  }
  const kept: string[] = [];
  for (const name of ID_PARAMS) {
    const v = u.searchParams.get(name);
    if (v !== null && ID_VALUE.test(v)) kept.push(`${name}=${v}`);
  }
  return `${u.protocol}//${hostPort}${path}${kept.length ? `?${kept.join("&")}` : ""}`;
}

const WORKDAY_HOST = /\.myworkdayjobs\.com$/;
/** Workday's shared host (Go: sources.WorkdaySiteHost): no tenant label, its pages are /recruiting/{tenant}/{site}/job/.... */
const WORKDAY_SHARED_HOST = /^wd[0-9]{1,3}\.myworkdaysite\.com$/;
/** Workday's language-region path prefix (en-US, fr-CA). A site called "ext" or "careers" never has this shape. */
const WORKDAY_LOCALE = /^[a-z]{2}-[A-Z]{2}$/;

/**
 * One key per posting. Same as pageKey except on Workday, where one posting has several routes
 * (/site/job/Location/Title_ID, /en-US/site/job/..., /en-US/site/details/Title_ID from a search,
 * and its /apply/... pages, whose location may be encoded differently): they fold to
 * scheme://host/site/job/Title_ID, so a per-job resume chosen on one is found on the others.
 *
 * pageKey itself stays route-specific: it is what jobFind sends to internal/joburl, which compares
 * it with the ledger's own URLs, so changing it is a change to that shared contract.
 */
export function postingKey(raw: unknown): string | null {
  const page = pageKey(raw);
  if (page === null) return null;
  const u = new URL(page);
  const shared = WORKDAY_SHARED_HOST.test(u.hostname);
  if (!shared && !WORKDAY_HOST.test(u.hostname)) return page;
  const segs = u.pathname.split("/").filter(Boolean);
  if (segs.length > 0 && WORKDAY_LOCALE.test(segs[0]!)) segs.shift();
  // On the shared host the site is recruiting/{tenant}/{site}; elsewhere it is the first segment.
  let site: string | undefined;
  if (shared) {
    if (segs[0] !== "recruiting" || segs.length < 3) return page;
    site = segs.splice(0, 3).join("/");
  } else {
    site = segs.shift();
  }
  const [kind, ...rest] = segs;
  if (site === undefined || (kind !== "job" && kind !== "details")) return page;
  const apply = rest.findIndex(s => s.toLowerCase() === "apply");
  const posting = apply < 0 ? rest : rest.slice(0, apply);
  const slug = posting.at(-1);
  if (slug === undefined) return page;
  return `${u.protocol}//${u.host}/${site}/job/${slug}`;
}

/**
 * The label of a button that sends the application: Submit, Submit application,
 * Send application. Not Next, Continue, Save or Review (steps along the way), not
 * "Review and submit" (a step before it), and not "Apply" (which usually opens
 * the form).
 */
export function isFinalSubmit(label: string): boolean {
  const t = label.replace(/\s+/g, " ").trim();
  return t.length > 0 && t.length <= 40 && /^(submit\b|send (my |your |the )?application\b)/i.test(t);
}

export interface Offer {
  key: string;
  company: string;
  title: string;
}

/** “Title” at Company, for a bar or a popup row. Posting text, so it is only ever set as text. */
export function offerSubject(o: Offer): string {
  const title = o.title ? `“${o.title.length > 80 ? o.title.slice(0, 79) + "…" : o.title}”` : "this job";
  const company = o.company.length > 60 ? o.company.slice(0, 59) + "…" : o.company;
  return company ? `${title} at ${company}` : title;
}

export type FindOutcome =
  | { ok: true; offer: Offer | null; why: "match" | "none" | "ambiguous" | "applied" }
  | { ok: false; error: string; code: string };

function clip(x: unknown): string {
  return typeof x === "string" ? x.replace(/[\u0000-\u001f\u007f-\u009f]/g, " ").trim().slice(0, MAX_TEXT) : "";
}

/** The host's jobFind reply. A job already marked Applied, or a page two postings claim, is not offered. */
export function parseFind(r: unknown): FindOutcome {
  if (typeof r !== "object" || r === null) return { ok: false, error: "no reply from the Mac app", code: "bad_reply" };
  const o = r as Record<string, unknown>;
  if (typeof o.error === "string") return { ok: false, error: o.error.slice(0, 300), code: typeof o.code === "string" ? o.code : "error" };
  if (typeof o.found !== "boolean") return { ok: false, error: "bad reply from the Mac app", code: "bad_reply" };
  if (!o.found) return { ok: true, offer: null, why: o.ambiguous === true ? "ambiguous" : "none" };
  const j = o.job;
  if (typeof j !== "object" || j === null) return { ok: false, error: "bad reply from the Mac app", code: "bad_reply" };
  const job = j as Record<string, unknown>;
  if (typeof job.key !== "string" || job.key === "" || job.key.length > 512) return { ok: false, error: "bad reply from the Mac app", code: "bad_reply" };
  if (job.mark === "applied") return { ok: true, offer: null, why: "applied" };
  return { ok: true, offer: { key: job.key, company: clip(job.company), title: clip(job.title) }, why: "match" };
}

// --- background side -------------------------------------------------------------

export interface StoredOffer {
  offer: Offer;
  expires: number;
}

/**
 * Where offers are kept between messages. chrome.storage.session survives the worker being
 * stopped (and is cleared when the browser closes); without it they live in memory only.
 */
export interface OfferStore {
  load(): Promise<Record<string, StoredOffer>>;
  save(offers: Record<string, StoredOffer>): Promise<void>;
}

export interface BrokerDeps {
  /** One bounded native call; an unreachable host is a coded reply, never a throw. */
  native(msg: object): Promise<unknown>;
  now?(): number;
  store?: OfferStore;
}

export type SignalReply = { offer: Offer | null; shownInTop?: boolean } | { error: string; code: string };
export type MarkReply = { ok: true; offer: Offer } | { error: string; code: string };

export interface Broker {
  /** A page pressed Submit (or the popup was opened on it): find its job and keep the offer for this tab. */
  signal(tabId: number | undefined, url: unknown): Promise<SignalReply>;
  /** The offer waiting for this tab, if it has not expired. */
  pending(tabId: number | undefined): Promise<Offer | null>;
  /** The user said yes: mark the job this tab was offered, and no other. */
  mark(tabId: number | undefined): Promise<MarkReply>;
  dismiss(tabId: number | undefined): Promise<void>;
}

export function createBroker(d: BrokerDeps): Broker {
  const now = d.now ?? Date.now;
  let memory: Record<string, StoredOffer> = {};
  const store: OfferStore = d.store ?? { load: async () => memory, save: async v => { memory = v; } };
  let cache: Record<string, StoredOffer> = {};
  /** Reads and writes of the table run one at a time; a native call never holds the lock. */
  const locked = createLock();

  async function read(): Promise<Record<string, StoredOffer>> {
    let m: Record<string, StoredOffer>;
    try { m = await store.load(); } catch { m = cache; } // storage failing must not lose the offers this worker holds
    const live: Record<string, StoredOffer> = {};
    for (const [k, e] of Object.entries(m ?? {})) {
      if (e && typeof e.expires === "number" && e.expires > now() && e.offer && typeof e.offer.key === "string") live[k] = e;
    }
    return live;
  }

  async function write(m: Record<string, StoredOffer>): Promise<void> {
    const keep = Object.entries(m).sort((a, b) => b[1].expires - a[1].expires).slice(0, MAX_OFFERS);
    cache = Object.fromEntries(keep);
    try { await store.save(cache); } catch { /* the in-memory copy still serves this worker */ }
  }

  const slot = (tabId: number | undefined): string | null => (tabId === undefined || !Number.isInteger(tabId) ? null : String(tabId));

  return {
    async signal(tabId, url) {
      const k = slot(tabId);
      if (k === null) return { error: "not from a tab", code: "bad_request" };
      const page = pageKey(url);
      if (page === null) return { offer: null };
      let outcome: FindOutcome;
      try { outcome = parseFind(await d.native({ op: "jobFind", url: page })); } catch {
        return { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" };
      }
      if (!outcome.ok) return { error: outcome.error, code: outcome.code };
      await locked(async () => {
        const m = await read();
        delete m[k];
        if (outcome.ok && outcome.offer) m[k] = { offer: outcome.offer, expires: now() + OFFER_TTL_MS };
        await write(m);
      });
      return { offer: outcome.offer };
    },
    async pending(tabId) {
      const k = slot(tabId);
      if (k === null) return null;
      return locked(async () => (await read())[k]?.offer ?? null);
    },
    async mark(tabId) {
      const k = slot(tabId);
      const offer = k === null ? null : await locked(async () => (await read())[k]?.offer ?? null);
      if (k === null || !offer) return { error: "there is nothing to mark", code: "no_offer" };
      let r: unknown;
      try { r = await d.native({ op: "jobMark", key: offer.key }); } catch {
        return { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" };
      }
      const o = typeof r === "object" && r !== null ? r as Record<string, unknown> : {};
      if (o.ok === true && o.mark === "applied" && o.key === offer.key) {
        await locked(async () => {
          const m = await read();
          if (m[k]?.offer.key === offer.key) delete m[k]; // a newer offer for this tab stays
          await write(m);
        });
        return { ok: true, offer };
      }
      // A refusal keeps the offer, so the user can try again.
      return { error: typeof o.error === "string" ? o.error.slice(0, 300) : "the Mac app did not confirm the mark", code: typeof o.code === "string" ? o.code : "error" };
    },
    async dismiss(tabId) {
      const k = slot(tabId);
      if (k === null) return;
      await locked(async () => {
        const m = await read();
        delete m[k];
        await write(m);
      });
    },
  };
}

// --- content side ----------------------------------------------------------------

export interface AppliedUi {
  /** Ask: Mark "title" at company as Applied? */
  offer(o: Offer): void;
  working(o: Offer): void;
  done(o: Offer): void;
  failed(o: Offer, message: string): void;
  hide(): void;
}

export interface FlowDeps {
  send(msg: ToBackground): Promise<unknown>;
  ui: AppliedUi;
  href(): string;
  now?(): number;
}

export interface AppliedFlow {
  /** The user pressed a final Submit on this page. */
  submitted(): Promise<void>;
  /** A page just loaded (or the top frame was asked to): show an offer waiting for this tab. */
  resume(): Promise<void>;
  /** "Mark Applied" was clicked. */
  accept(): Promise<void>;
  /** "Not now" was clicked. */
  dismiss(): Promise<void>;
}

function isOffer(x: unknown): x is Offer {
  return typeof x === "object" && x !== null && typeof (x as Offer).key === "string" && typeof (x as Offer).company === "string" && typeof (x as Offer).title === "string";
}

export function createAppliedFlow(d: FlowDeps): AppliedFlow {
  const now = d.now ?? Date.now;
  let shown: Offer | null = null;
  let marking = false;
  let asking = false;
  const dismissedAt = new Map<string, number>();

  function present(o: Offer): void {
    shown = o;
    d.ui.offer(o);
  }

  return {
    async submitted() {
      if (marking || asking) return;
      const url = pageKey(d.href());
      if (url === null) return; // not an http(s) page: nothing to look up
      asking = true;
      let r: unknown;
      try { r = await d.send({ type: "appliedSignal", url }); } catch { return; } finally { asking = false; }
      // An unreachable app, an untracked page and an ambiguous one are all silent: this is a convenience, not a gate.
      const reply = typeof r === "object" && r !== null ? r as { offer?: unknown; shownInTop?: unknown } : {};
      if (!isOffer(reply.offer)) return;
      // A form in a frame: the top frame already shows the question, so it is not asked twice.
      if (reply.shownInTop === true) return;
      const t = dismissedAt.get(reply.offer.key);
      if (t !== undefined && now() - t < DISMISS_QUIET_MS) return;
      if (shown?.key !== reply.offer.key) present(reply.offer);
    },
    async resume() {
      let r: unknown;
      try { r = await d.send({ type: "appliedPending" }); } catch { return; }
      const offer = typeof r === "object" && r !== null ? (r as { offer?: unknown }).offer : null;
      if (isOffer(offer) && !shown) present(offer);
    },
    async accept() {
      const offer = shown;
      if (!offer || marking) return;
      marking = true;
      d.ui.working(offer);
      try {
        let r: unknown;
        try { r = await d.send({ type: "appliedMark" }); } catch { r = { error: "the Vani Apply Mac app isn't reachable" }; }
        const o = typeof r === "object" && r !== null ? r as Record<string, unknown> : {};
        if (o.ok === true) {
          shown = null;
          d.ui.done(offer);
        } else {
          d.ui.failed(offer, typeof o.error === "string" ? o.error : "could not mark the job");
        }
      } finally {
        marking = false;
      }
    },
    async dismiss() {
      const offer = shown;
      if (!offer || marking) return;
      shown = null;
      dismissedAt.set(offer.key, now());
      d.ui.hide();
      try { await d.send({ type: "appliedDismiss" }); } catch { /* the offer expires by itself */ }
    },
  };
}
