// A persistent native-messaging port for the bank ops. chrome.runtime.sendNativeMessage launches the
// host once per message; one connectNative port keeps one host process for a burst of calls.
//
// The protocol has no request ids, but the host (vani-apply-host/main.swift) is one synchronous loop,
// so replies come back strictly in request order and a reply is matched to its call by FIFO position.
// A disconnect or a stray frame poisons the port: it is closed, every pending call is rejected, and
// the next call opens a fresh one. A call that times out while still queued was never written, so it
// is dropped and the port stays up. A call that times out after it was written stays in line as one
// tombstone: its caller is rejected, the one reply that belongs to it is discarded, and calls that
// have not been written are not failed and the timeout is not a failed connect. The port stays up so
// a later bank call can use it. Tombstones that have filled the queue and can never be retired drop
// the connection without failing those unwritten calls and without backoff. A caller's shorter
// deadline is that call's only timer; it is not sent to the host, and it does not outlive the call.
import type { Transport } from "./store";

/** The slice of chrome.runtime.Port this transport uses. */
export interface NativePort {
  postMessage(message: object): void;
  disconnect(): void;
  onMessage: { addListener(cb: (message: unknown) => void): void };
  onDisconnect: { addListener(cb: () => void): void };
}

export type PortFailure = "disconnect" | "timeout" | "overflow" | "protocol" | "closed";

/** Why a call was rejected; `kind` decides whether a retry is safe. */
export class PortError extends Error {
  constructor(message: string, readonly kind: PortFailure) {
    super(message);
  }
}

export interface PortTransportOptions {
  connect: () => NativePort;
  /** One process per message: used for ops that can take seconds, and while the port is backing off. */
  spawn: Transport;
  /** Calls written to the host before the first reply is back. */
  maxInFlight?: number;
  /** Calls in flight plus waiting; one more is rejected at once. */
  maxQueued?: number;
  callTimeoutMs?: number;
  /** Wait after the k-th consecutive failed connection (a disconnect before its first reply), the last entry repeating. */
  backoff?: number[];
  now?: () => number;
}

/** Ops answered from the bank file at once. Anything else (draft, jobFind, jobMark, jobsIngest,
 *  jobStart, jobResult, resumeOpen, resumeOpenData, unknown) runs per message so it cannot hold up the
 *  serial host's bank ops, and so the caller's deadline is not clamped to callTimeoutMs.
 *  resumeOpen is excluded on purpose: the host then calls NSWorkspace.open, which can stall until the
 *  port's own timeout and would block every bank call queued behind it. sameQuestion is excluded for the
 *  same reason (it runs the on-device model, seconds not milliseconds) and because the per-message spawn
 *  keeps the caller's 8 s. jobStart and jobResult are excluded so a cold host gets the caller's 10 s:
 *  the agent returns as soon as the job is queued, and the slow part is the hop, not the model. */
const PORT_OPS = new Set([
  "ping", "get", "getChunk", "put", "patch", "log",
  "resumePut", "resumeGet", "resumeInfo", "resumeDelete",
]);
/** Ops that change nothing, so running one twice is harmless. resumeOpen is absent with resumeOpenData:
 *  opening Preview twice is not harmless, and resumeOpenData carries the PDF. */
const READ_ONLY_OPS = new Set(["ping", "get", "getChunk", "resumeGet", "resumeInfo"]);

interface Pending {
  msg: object;
  resolve: (reply: unknown) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
  /** Set when a written call times out. The slot stays in FIFO order so its reply is not delivered later. */
  tombstone?: boolean;
}

interface Connection {
  port: NativePort;
  inFlight: Pending[];
  replied: boolean;
}

function isNotify(x: unknown): boolean {
  return typeof x === "object" && x !== null && !Array.isArray(x) && "notify" in x;
}

function lastErrorMessage(): string | undefined {
  return typeof chrome === "undefined" ? undefined : chrome.runtime?.lastError?.message;
}

export function createPortTransport(opts: PortTransportOptions): Transport & { close(): void } {
  const maxInFlight = opts.maxInFlight ?? 4;
  const maxQueued = opts.maxQueued ?? 64;
  const callTimeoutMs = opts.callTimeoutMs ?? 5_000;
  const backoff = opts.backoff ?? [1_000, 2_000, 4_000, 8_000, 16_000, 30_000];
  const now = opts.now ?? Date.now;

  /** A passed deadline shorter than the port's own bound. Anything else (omitted, longer, or not a
   *  finite non-negative number) stays at callTimeoutMs. Short ops do not take a caller's longer budget. */
  function callDeadline(timeoutMs: number | undefined): number {
    if (typeof timeoutMs !== "number" || !Number.isFinite(timeoutMs) || timeoutMs < 0 || timeoutMs >= callTimeoutMs) {
      return callTimeoutMs;
    }
    return timeoutMs;
  }

  let conn: Connection | null = null;
  const queue: Pending[] = [];
  let failures = 0;
  let retryAt = 0;
  let closed = false;

  /** A connection failed before it ever answered: wait before trusting the port again. */
  function failedConnect() {
    failures++;
    retryAt = now() + (backoff[Math.min(failures, backoff.length) - 1] ?? 0);
  }

  function rejectAll(err: (p: Pending) => Error, extra: Pending[]) {
    for (const p of [...extra, ...queue.splice(0)]) {
      clearTimeout(p.timer);
      p.reject(err(p));
    }
  }

  /** Drop the connection and fail everything pending on it. */
  function poison(c: Connection, kind: PortFailure, message: string) {
    if (conn === c) conn = null;
    try { c.port.disconnect(); } catch { /* already gone */ }
    const stranded = c.inFlight.splice(0);
    rejectAll(() => new PortError(message, kind), stranded);
  }

  /** Calls still waiting on the host. A tombstone is only a discarded-reply slot, so it does not hold the window. */
  function liveInFlight(c: Connection): number {
    let n = 0;
    for (const p of c.inFlight) if (!p.tombstone) n++;
    return n;
  }

  /** The host never retired these written timeouts. Close so the next call opens a fresh port.
   *  Unwritten queued calls stay queued, and this is not a failed connect. */
  function abandonUnreplied(c: Connection) {
    if (conn === c) conn = null;
    for (const p of c.inFlight.splice(0)) clearTimeout(p.timer);
    try { c.port.disconnect(); } catch { /* already gone */ }
  }

  function open(): Connection {
    const port = opts.connect();
    const c: Connection = { port, inFlight: [], replied: false };
    port.onMessage.addListener(msg => {
      if (conn !== c) return; // a frame from a port we already gave up on
      if (isNotify(msg)) return; // reserved for a push; it is not a reply and takes no FIFO slot
      const head = c.inFlight.shift();
      if (!head) { poison(c, "protocol", "native host sent a reply nobody asked for"); return; }
      clearTimeout(head.timer);
      c.replied = true;
      failures = 0;
      if (head.tombstone) { pump(); return; } // the reply belongs to a call that already timed out
      head.resolve(msg);
      pump();
    });
    port.onDisconnect.addListener(() => {
      if (conn !== c) return;
      if (!c.replied) failedConnect();
      poison(c, "disconnect", lastErrorMessage() ?? "native host disconnected");
    });
    return c;
  }

  function pump() {
    while (queue.length > 0) {
      if (!conn) {
        try { conn = open(); } catch (e) {
          failedConnect();
          rejectAll(() => new PortError(e instanceof Error ? e.message : String(e), "disconnect"), []);
          return;
        }
      }
      if (liveInFlight(conn) >= maxInFlight) return;
      const p = queue.shift()!;
      conn.inFlight.push(p);
      try { conn.port.postMessage(p.msg); } catch (e) {
        poison(conn, "disconnect", e instanceof Error ? e.message : String(e));
        return;
      }
    }
  }

  function enqueue(msg: object, timeoutMs?: number): Promise<unknown> {
    if (closed) return Promise.reject(new PortError("native port closed", "closed"));
    if (queue.length + (conn?.inFlight.length ?? 0) >= maxQueued) {
      return Promise.reject(new PortError("too many pending native calls", "overflow"));
    }
    const limit = callDeadline(timeoutMs);
    return new Promise((resolve, reject) => {
      const p: Pending = {
        msg, resolve, reject,
        timer: setTimeout(() => {
          const c = conn;
          const at = c?.inFlight.indexOf(p) ?? -1;
          if (c && at >= 0) {
            // Written. Keep the FIFO slot so the late reply is discarded, not handed to the next caller.
            p.tombstone = true;
            p.reject(new PortError(`native call timed out after ${limit} ms`, "timeout"));
            if (c.inFlight.length >= maxQueued && c.inFlight.every(x => x.tombstone)) abandonUnreplied(c);
            pump();
            return;
          }
          const i = queue.indexOf(p);
          if (i >= 0) queue.splice(i, 1); // never written, so nothing can come back for it
          p.reject(new PortError(`native call timed out after ${limit} ms`, "timeout"));
        }, limit),
      };
      queue.push(p);
      pump();
    });
  }

  function attempt(msg: object, op: string, retried: boolean, timeoutMs?: number): Promise<unknown> {
    if (failures > 0 && now() < retryAt) return opts.spawn(msg, timeoutMs);
    return enqueue(msg, timeoutMs).catch(e => {
      if (!retried && e instanceof PortError && e.kind === "disconnect" && READ_ONLY_OPS.has(op)) {
        return attempt(msg, op, true, timeoutMs);
      }
      throw e;
    });
  }

  const transport = ((msg: object, timeoutMs?: number) => {
    const op = (msg as { op?: unknown }).op;
    if (typeof op !== "string" || !PORT_OPS.has(op)) return opts.spawn(msg, timeoutMs);
    return attempt(msg, op, false, timeoutMs);
  }) as Transport & { close(): void };

  transport.close = () => {
    closed = true;
    if (conn) poison(conn, "closed", "native port closed");
    rejectAll(() => new PortError("native port closed", "closed"), []);
  };
  return transport;
}
