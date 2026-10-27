// The answer bank's storage. When the Vani Apply Mac app is installed and
// answers a native-messaging ping, it holds the bank (one file shared with
// Safari); otherwise the bank stays in chrome.storage.local.
import { createLock, isRecord } from "./guards";
import { type Answer, type Bank, emptyBank } from "./types";
import { MAX_BANK_BYTES, bankBytes, mergeBanks, parseBank } from "./bank";
import { createBankMemo } from "./bankcache";
import { createPortTransport } from "./nativeport";

export const NATIVE_HOST = "dev.vbcr.vani_apply";
const KEY = "vaniBank";
const CALL_TIMEOUT_MS = 5_000;
const MODE_TTL_MS = 30_000;
const MAX_ATTEMPTS = 3;

export type StorageMode = "mac-app" | "browser";

export interface PatchOutcome {
  rev: number;
  droppedSecret: number;
  /** Set only when a `baseRev` was given and the Mac app's bank had moved on: nothing was written, `rev` is its current one. */
  conflict?: boolean;
}

export interface LogEntry {
  event: "capture" | "fill" | "edit" | "delete" | "import" | "settings";
  host?: string;
  counts?: Record<string, number>;
}

/** Sends one JSON request to the native host and resolves with its JSON reply; rejects when the host cannot be reached.
 *  `timeoutMs`, when a caller passes it, is that call's deadline only. It is never a field of `msg`. */
export type Transport = (msg: object, timeoutMs?: number) => Promise<unknown>;

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
/** A chunked bank arrived with a different byte length than the host said it had. Nothing is parsed or written back. */
export class ChunkIntegrityError extends NativeError {
  override name = "ChunkIntegrityError";
}

/** One host process per message: Safari's promise API when present, else Chrome's callback API.
 *  A passed deadline is this call's only timer. A late reply after it fires is ignored. */
export const spawnTransport: Transport = (msg, timeoutMs) =>
  new Promise((resolve, reject) => {
    let settled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const finish = (fn: () => void) => {
      if (settled) return;
      settled = true;
      if (timer !== undefined) clearTimeout(timer);
      fn();
    };
    if (typeof timeoutMs === "number" && Number.isFinite(timeoutMs) && timeoutMs >= 0) {
      timer = setTimeout(() => finish(() => reject(new Error("timed out"))), timeoutMs);
    }
    if (globalThis.browser?.runtime?.sendNativeMessage) {
      globalThis.browser.runtime.sendNativeMessage(NATIVE_HOST, msg).then(
        v => finish(() => resolve(v)),
        e => finish(() => reject(e instanceof Error ? e : new Error(String(e)))),
      );
      return;
    }
    chrome.runtime.sendNativeMessage(NATIVE_HOST, msg, response => {
      const err = chrome.runtime.lastError;
      if (err) finish(() => reject(new Error(err.message ?? "native messaging failed")));
      else finish(() => resolve(response));
    });
  });

/** Production transport: Safari and a Chrome without connectNative go per message; Chrome keeps one port for the bank ops. */
export const nativeTransport: Transport =
  globalThis.browser?.runtime?.sendNativeMessage || typeof chrome === "undefined" || !chrome.runtime?.connectNative
    ? spawnTransport
    : createPortTransport({ connect: () => chrome.runtime.connectNative(NATIVE_HOST), spawn: spawnTransport });

const chromeLocal: LocalArea = {
  get: k => chrome.storage.local.get(k),
  set: i => chrome.storage.local.set(i),
  remove: k => chrome.storage.local.remove(k),
};

function isRev(x: unknown): x is number {
  return typeof x === "number" && Number.isSafeInteger(x) && x >= 0;
}

/**
 * Mirrors Swift's NativeMessaging.chunkRanges: a chunk's JSON-string form must fit Chrome's 1 MiB
 * reply minus 4096 bytes of envelope, and a raw byte costs at most 6 once escaped, so every chunk
 * but the last holds at least this many bytes.
 */
const MIN_FULL_CHUNK_BYTES = Math.floor((1024 * 1024 - 4096) / 6);
/** The most chunks a bank at the cap (Swift Limits.maxBankBytes = MAX_BANK_BYTES) can need. */
export const MAX_CHUNK_COUNT = Math.ceil(MAX_BANK_BYTES / MIN_FULL_CHUNK_BYTES);

/**
 * The host's chunk count is untrusted input to a polling loop. Refuse before the first getChunk
 * unless totalBytes is a positive safe integer within the bank cap and chunkCount is an integer in
 * 1..min(totalBytes, MAX_CHUNK_COUNT) (each chunk carries at least one byte).
 */
function checkedChunkCount(count: unknown, totalBytes: unknown): number {
  if (typeof totalBytes !== "number" || !Number.isSafeInteger(totalBytes) || totalBytes < 1 || totalBytes > MAX_BANK_BYTES) {
    throw new ChunkIntegrityError(`chunked get reports an impossible totalBytes: ${String(totalBytes)}`);
  }
  if (typeof count !== "number" || !Number.isSafeInteger(count) || count < 1 || count > Math.min(totalBytes, MAX_CHUNK_COUNT)) {
    throw new ChunkIntegrityError(`chunked get reports an impossible chunkCount: ${String(count)}`);
  }
  return count;
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
  /** How long a bank read from the Mac app is served again without asking (default BANK_FRESH_MS). */
  freshMs?: number;
  now?: () => number;
}

export function createStore(opts: StoreOptions = {}) {
  const transport = opts.transport ?? nativeTransport;
  const local = opts.local ?? chromeLocal;
  const timeoutMs = opts.timeoutMs ?? CALL_TIMEOUT_MS;
  const ttl = opts.modeTtlMs ?? MODE_TTL_MS;
  const now = opts.now ?? Date.now;

  // The Mac app's bank as last read, for loadSnapshot(). Every write below invalidates it (the write's
  // result is not known exactly, or the host may have changed what it stored), and so does any failed call.
  const memo = createBankMemo<Snapshot>({ fetch: () => getSnapshot(), now, freshMs: opts.freshMs });

  let nativeUntil = 0; // native confirmed reachable until this time
  let migrated = false;
  let note: string | null = null;
  let pingPromise: Promise<boolean> | null = null;
  let migratePromise: Promise<void> | null = null;

  const serializeWrite = createLock();

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
      memo.invalidate();
      throw new TransportError(`Mac app unreachable: ${msgOf(e)}`);
    } finally {
      clearTimeout(timer);
    }
    if (!isRecord(reply)) { nativeUntil = 0; memo.invalidate(); throw new TransportError("Mac app sent no reply"); }
    if (typeof reply.error === "string") throw new NativeError(reply.error);
    return reply;
  }

  async function getSnapshot(): Promise<Snapshot> {
    const r = await call({ op: "get" });
    if (r.chunked === true) {
      const rev = r.rev;
      if (!isRev(rev)) throw new NativeError("chunked get reply has no valid rev");
      const chunkCount = checkedChunkCount(r.chunkCount, r.totalBytes);
      const chunks: string[] = [typeof r.chunk === "string" ? r.chunk : ""];
      for (let i = 1; i < chunkCount; i++) {
        const next = await call({ op: "getChunk", rev, chunkIndex: i });
        if (next.conflict === true) {
          return getSnapshot();
        }
        if (next.rev !== rev || typeof next.chunk !== "string") {
          throw new NativeError("chunked transfer rev mismatch or missing chunk");
        }
        chunks.push(next.chunk);
      }
      const joined = chunks.join("");
      // totalBytes is the UTF-8 byte length of the whole encoded bank (Swift's Data.count). A mismatch
      // means a chunk was lost, doubled or cut inside a character: refuse rather than parse, and
      // never let a damaged bank reach a write-back.
      const received = new TextEncoder().encode(joined).length;
      if (typeof r.totalBytes !== "number" || r.totalBytes !== received) {
        throw new ChunkIntegrityError(`chunked bank is ${received} bytes but the host sent ${String(r.totalBytes)}`);
      }
      return { rev, bank: parseBank(joined).bank };
    }
    return snapshot(r);
  }

  /**
   * A patch as one put, for a Mac app that has no patch op. With a baseRev it is one rev-checked
   * attempt (a moved bank is a conflict, as a patch's would be: the rows were computed from that
   * revision); without one, it retries on the current bank like commit.
   */
  async function patchByPut(upsert: Answer[], deleteKeys: string[], baseRev?: number): Promise<PatchOutcome> {
    for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
      const snap = await getSnapshot();
      if (baseRev !== undefined && snap.rev !== baseRev) return { rev: snap.rev, droppedSecret: 0, conflict: true };
      const answers = { ...snap.bank.answers };
      for (const k of deleteKeys) delete answers[k];
      for (const a of upsert) answers[a.key] = a;
      const next: Bank = { ...snap.bank, answers };
      const bytes = bankBytes(next);
      if (bytes > MAX_BANK_BYTES) throw tooBig(bytes);
      let r: Record<string, unknown>;
      try {
        r = await call({ op: "put", baseRev: snap.rev, bank: next });
      } finally {
        memo.invalidate();
      }
      if (r.conflict === true) {
        if (baseRev !== undefined) return { rev: isRev(r.rev) ? r.rev : snap.rev, droppedSecret: 0, conflict: true };
        continue;
      }
      if (!isRev(r.rev)) throw new NativeError("native put reply has no valid rev");
      return { rev: r.rev, droppedSecret: typeof r.droppedSecret === "number" ? r.droppedSecret : 0 };
    }
    throw new Error("answer bank changed concurrently; try again");
  }

  /** get -> fn -> put(baseRev), re-running fn on the current snapshot after each conflict. */
  async function commit(fn: (b: Bank) => Bank, first?: Snapshot): Promise<Bank> {
    let snap = first ?? await getSnapshot();
    for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
      const next = fn(snap.bank);
      const bytes = bankBytes(next);
      if (bytes > MAX_BANK_BYTES) throw tooBig(bytes);
      let r: Record<string, unknown>;
      try {
        r = await call({ op: "put", baseRev: snap.rev, bank: next });
      } finally {
        memo.invalidate(); // even a timed-out put may have landed
      }
      if (r.conflict === true) {
        if (r.chunked === true || !r.bank) {
          snap = await getSnapshot();
        } else {
          snap = snapshot(r);
        }
        continue;
      }
      if (!isRev(r.rev)) throw new NativeError("native put reply has no valid rev");
      return next;
    }
    throw new Error("answer bank changed concurrently; try again");
  }

  /** Move a bank left in chrome.storage.local into the Mac app, once per worker lifetime. */
  async function migrate(): Promise<void> {
    if (migrated) return;
    if (migratePromise) return migratePromise;
    migratePromise = (async () => {
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
      } finally {
        migratePromise = null;
      }
    })();
    return migratePromise;
  }

  /** True when the Mac app answers a ping (cached for 30 s). Only an unreachable host means "no". */
  async function useNative(): Promise<boolean> {
    if (now() < nativeUntil) { await migrate(); return true; } // no-op once migrated; retries a failed one
    if (pingPromise) return pingPromise;
    pingPromise = (async () => {
      try {
        let r: Record<string, unknown>;
        try {
          r = await call({ op: "ping" });
        } catch (e) {
          if (e instanceof TransportError) return false;
          throw e;
        }
        if (r.ok !== true || r.protocol !== 1) throw new NativeError(`unsupported Mac app protocol ${String(r.protocol)}`);
        await migrate();
        nativeUntil = now() + ttl;
        return true;
      } finally {
        pingPromise = null;
      }
    })();
    return pingPromise;
  }

  async function localLoad(): Promise<Bank> {
    const raw = (await local.get(KEY))[KEY];
    if (raw === undefined) return emptyBank();
    try {
      return parseBank(JSON.stringify(raw)).bank;
    } catch (e) {
      note = `Answers saved in this browser could not be read and were left in place: ${msgOf(e)}`;
      console.error(note);
      return emptyBank();
    }
  }

  async function localWrite(bank: Bank): Promise<void> {
    const bytes = bankBytes(bank);
    if (bytes > MAX_BANK_BYTES) throw tooBig(bytes);
    await local.set({ [KEY]: bank });
    // Written while the Mac app was unreachable: move it over at the next
    // native contact, even if an earlier migration already ran.
    migrated = false;
  }

  /**
   * The bank and, from the Mac app, its revision (null: kept in this browser, which has none). A bank
   * read from the Mac app in the last `freshMs` is served again without a native call, and concurrent
   * reads share one; `fresh` always asks (use it before a read-modify-write that sends no baseRev).
   */
  async function loadSnapshot(o: { fresh?: boolean } = {}): Promise<{ bank: Bank; rev: number | null }> {
    if (!o.fresh) {
      // Held only because the Mac app answered moments ago. Not while a migration is owed: useNative() retries it.
      const held = migrated ? memo.peek() : undefined;
      if (held) return held;
    }
    if (await useNative()) return o.fresh ? memo.fresh() : memo.get();
    return { bank: await serializeWrite(async () => localLoad()), rev: null };
  }

  return {
    loadSnapshot,

    async loadBank(o: { fresh?: boolean } = {}): Promise<Bank> {
      return (await loadSnapshot(o)).bank;
    },

    async updateBank(fn: (b: Bank) => Bank): Promise<Bank> {
      return serializeWrite(async () => {
        if (await useNative()) return commit(fn);
        const next = fn(await localLoad());
        await localWrite(next);
        return next;
      });
    },

    /** Import: replace the whole bank. */
    async replaceBank(bank: Bank): Promise<void> {
      return serializeWrite(async () => {
        if (await useNative()) { await commit(() => bank); return; }
        await localWrite(bank);
      });
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

    /**
     * Granular row-level delta update without full bank transfer. With `baseRev` (Mac app only) the
     * host refuses the patch when its bank has moved on: nothing is written and `conflict` is set.
     */
    async patchAnswers(upsert: Answer[], deleteKeys: string[] = [], baseRev?: number): Promise<PatchOutcome> {
      return serializeWrite(async () => {
        if (await useNative()) {
          const held = baseRev === undefined ? undefined : memo.peek();
          let r: Record<string, unknown>;
          try {
            r = await call({ op: "patch", upsert, delete: deleteKeys, ...(baseRev === undefined ? {} : { baseRev }) });
          } catch (e) {
            // A Mac app built before the patch op answers "unknown op": make the same change with get + put.
            if (e instanceof NativeError && /^unknown op\b/.test(e.message)) return patchByPut(upsert, deleteKeys, baseRev);
            throw e;
          } finally {
            memo.invalidate(); // even a timed-out patch may have landed
          }
          if (r.conflict === true) return { rev: isRev(r.rev) ? r.rev : 0, droppedSecret: 0, conflict: true };
          if (!isRev(r.rev)) throw new NativeError("native patch reply has no valid rev");
          const droppedSecret = typeof r.droppedSecret === "number" ? r.droppedSecret : 0;
          // The held bank plus exactly this patch is the host's bank now, provided the host stored every row
          // and moved one revision; otherwise the next read asks.
          if (held && baseRev !== undefined && held.rev === baseRev && r.rev === baseRev + 1 && droppedSecret === 0) {
            const answers = { ...held.bank.answers };
            for (const k of deleteKeys) delete answers[k];
            for (const a of upsert) answers[a.key] = a;
            memo.set({ rev: r.rev, bank: { ...held.bank, answers } });
          }
          return { rev: r.rev, droppedSecret };
        }
        const current = await localLoad();
        const answers = { ...current.answers };
        for (const k of deleteKeys) {
          delete answers[k];
        }
        let droppedSecret = 0;
        for (const a of upsert) {
          if (a.sensitivity === "secret") {
            droppedSecret++;
            continue;
          }
          answers[a.key] = a;
        }
        const updated: Bank = { ...current, answers };
        await localWrite(updated);
        return { rev: 0, droppedSecret };
      });
    },
  };
}

const store = createStore();
export const loadBank = store.loadBank;
export const loadSnapshot = store.loadSnapshot;
export const updateBank = store.updateBank;
export const replaceBank = store.replaceBank;
export const storageMode = store.storageMode;
export const migrationNote = store.migrationNote;
export const logActivity = store.logActivity;
export const patchAnswers = store.patchAnswers;
