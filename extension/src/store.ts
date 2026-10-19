// The answer bank's storage. When the Vani Apply Mac app is installed and
// answers a native-messaging ping, it holds the bank (one file shared with
// Safari); otherwise the bank stays in chrome.storage.local.
import { type Bank, emptyBank } from "./types";
import { MAX_BANK_BYTES, bankBytes, mergeBanks, parseBank } from "./bank";

export const NATIVE_HOST = "dev.vbcr.vani_apply";
const KEY = "vaniBank";
const CALL_TIMEOUT_MS = 5_000;
const MODE_TTL_MS = 30_000;
const MAX_ATTEMPTS = 3;

export type StorageMode = "mac-app" | "browser";

export interface LogEntry {
  event: "capture" | "fill" | "edit" | "delete" | "import" | "settings";
  host?: string;
  counts?: Record<string, number>;
}

/** Sends one JSON request to the native host and resolves with its JSON reply; rejects when the host cannot be reached. */
export type Transport = (msg: object) => Promise<unknown>;

/** The slice of chrome.storage.local the store uses. */
export interface LocalArea {
  get(key: string): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
  remove(key: string): Promise<void>;
}

/** The host could not be reached (not installed, disconnected, timed out). */
class TransportError extends Error {}
/** The host answered `{"error": ...}`. Never a reason to fall back: the bank lives there. */
class NativeError extends Error {}

/** Production transport: Safari's promise API when present, else Chrome's callback API. */
export const nativeTransport: Transport = msg =>
  new Promise((resolve, reject) => {
    if (globalThis.browser?.runtime?.sendNativeMessage) {
      globalThis.browser.runtime.sendNativeMessage(NATIVE_HOST, msg).then(resolve, reject);
      return;
    }
    chrome.runtime.sendNativeMessage(NATIVE_HOST, msg, response => {
      const err = chrome.runtime.lastError;
      if (err) reject(new Error(err.message ?? "native messaging failed"));
      else resolve(response);
    });
  });

const chromeLocal: LocalArea = {
  get: k => chrome.storage.local.get(k),
  set: i => chrome.storage.local.set(i),
  remove: k => chrome.storage.local.remove(k),
};

function isObj(x: unknown): x is Record<string, unknown> {
  return typeof x === "object" && x !== null && !Array.isArray(x);
}

function isRev(x: unknown): x is number {
  return typeof x === "number" && Number.isSafeInteger(x) && x >= 0;
}

interface Snapshot { rev: number; bank: Bank }

function snapshot(r: Record<string, unknown>): Snapshot {
  if (!isRev(r.rev)) throw new NativeError("native reply has no valid rev");
  return { rev: r.rev, bank: parseBank(JSON.stringify(r.bank)).bank };
}

function tooBig(bytes: number): Error {
  return new Error(`answer bank is ${Math.round(bytes / 1000)} KB, over the ${MAX_BANK_BYTES / 1000} KB limit; delete some answers and try again`);
}

function msgOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

export interface StoreOptions {
  transport?: Transport;
  local?: LocalArea;
  timeoutMs?: number;
  modeTtlMs?: number;
  now?: () => number;
}

export function createStore(opts: StoreOptions = {}) {
  const transport = opts.transport ?? nativeTransport;
  const local = opts.local ?? chromeLocal;
  const timeoutMs = opts.timeoutMs ?? CALL_TIMEOUT_MS;
  const ttl = opts.modeTtlMs ?? MODE_TTL_MS;
  const now = opts.now ?? Date.now;

  let nativeUntil = 0; // native confirmed reachable until this time
  let migrated = false;
  let note: string | null = null;

  /** One bounded request. Throws TransportError (unreachable) or NativeError (host said no). */
  async function call(msg: object): Promise<Record<string, unknown>> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let reply: unknown;
    try {
      reply = await Promise.race([
        transport(msg),
        new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error("timed out")), timeoutMs); }),
      ]);
    } catch (e) {
      nativeUntil = 0;
      throw new TransportError(`Mac app unreachable: ${msgOf(e)}`);
    } finally {
      clearTimeout(timer);
    }
    if (!isObj(reply)) { nativeUntil = 0; throw new TransportError("Mac app sent no reply"); }
    if (typeof reply.error === "string") throw new NativeError(reply.error);
    return reply;
  }

  async function getSnapshot(): Promise<Snapshot> {
    return snapshot(await call({ op: "get" }));
  }

  /** get -> fn -> put(baseRev), re-running fn on the current snapshot after each conflict. */
  async function commit(fn: (b: Bank) => Bank, first?: Snapshot): Promise<Bank> {
    let snap = first ?? await getSnapshot();
    for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
      const next = fn(snap.bank);
      const bytes = bankBytes(next);
      if (bytes > MAX_BANK_BYTES) throw tooBig(bytes);
      const r = await call({ op: "put", baseRev: snap.rev, bank: next });
      if (r.conflict === true) { snap = snapshot(r); continue; }
      if (!isRev(r.rev)) throw new NativeError("native put reply has no valid rev");
      return next;
    }
    throw new Error("answer bank changed concurrently; try again");
  }

  /** Move a bank left in chrome.storage.local into the Mac app, once per worker lifetime. */
  async function migrate(): Promise<void> {
    if (migrated) return;
    try {
      const raw = (await local.get(KEY))[KEY];
      if (raw === undefined) { migrated = true; return; }
      let localBank: Bank;
      try {
        localBank = parseBank(JSON.stringify(raw)).bank;
      } catch (e) {
        migrated = true;
        note = `Answers saved in this browser could not be read and were left in place: ${msgOf(e)}`;
        console.error(note);
        return;
      }
      await commit(b => mergeBanks(b, localBank));
      await local.remove(KEY); // only after the put succeeded
      migrated = true;
      note = null;
    } catch (e) {
      note = `Answers saved in this browser were not moved to the Mac app (left in place): ${msgOf(e)}`;
      console.error(note);
    }
  }

  /** True when the Mac app answers a ping (cached for 30 s). Only an unreachable host means "no". */
  async function useNative(): Promise<boolean> {
    if (now() < nativeUntil) { await migrate(); return true; } // no-op once migrated; retries a failed one
    let r: Record<string, unknown>;
    try {
      r = await call({ op: "ping" });
    } catch (e) {
      if (e instanceof TransportError) return false;
      throw e;
    }
    if (r.ok !== true || r.protocol !== 1) throw new NativeError(`unsupported Mac app protocol ${String(r.protocol)}`);
    nativeUntil = now() + ttl;
    await migrate();
    return true;
  }

  async function localLoad(): Promise<Bank> {
    const raw = (await local.get(KEY))[KEY];
    if (raw === undefined) return emptyBank();
    return parseBank(JSON.stringify(raw)).bank;
  }

  async function localWrite(bank: Bank): Promise<void> {
    const bytes = bankBytes(bank);
    if (bytes > MAX_BANK_BYTES) throw tooBig(bytes);
    await local.set({ [KEY]: bank });
    // Written while the Mac app was unreachable: move it over at the next
    // native contact, even if an earlier migration already ran.
    migrated = false;
  }

  return {
    async loadBank(): Promise<Bank> {
      return (await useNative()) ? (await getSnapshot()).bank : localLoad();
    },

    async updateBank(fn: (b: Bank) => Bank): Promise<Bank> {
      if (await useNative()) return commit(fn);
      const next = fn(await localLoad());
      await localWrite(next);
      return next;
    },

    /** Import: replace the whole bank. */
    async replaceBank(bank: Bank): Promise<void> {
      if (await useNative()) { await commit(() => bank); return; }
      await localWrite(bank);
    },

    async storageMode(): Promise<StorageMode> {
      return (await useNative()) ? "mac-app" : "browser";
    },

    /** Set when browser-stored answers could not be moved to (or read for) the Mac app. */
    migrationNote(): string | null { return note; },

    /** Fire-and-forget activity log. Mac app only; never throws, never delays the caller. */
    logActivity(entry: LogEntry): Promise<void> {
      if (now() >= nativeUntil) return Promise.resolve();
      return call({ op: "log", entry }).then(() => undefined, () => undefined);
    },
  };
}

const store = createStore();
export const loadBank = store.loadBank;
export const updateBank = store.updateBank;
export const replaceBank = store.replaceBank;
export const storageMode = store.storageMode;
export const migrationNote = store.migrationNote;
export const logActivity = store.logActivity;
