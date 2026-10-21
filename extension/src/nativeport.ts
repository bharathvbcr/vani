// A persistent native-messaging port for the bank ops. chrome.runtime.sendNativeMessage launches the
// host once per message; one connectNative port keeps one host process for a burst of calls.
//
// The protocol has no request ids, but the host (vani-apply-host/main.swift) is one synchronous loop,
// so replies come back strictly in request order and a reply is matched to its call by FIFO position.
// That only holds while no frame is lost, so anything that could desynchronise the two sides
// (a timeout, a disconnect, a stray frame) poisons the port: it is closed, every pending call is
// rejected, and the next call opens a fresh one.
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

/** Ops answered from the bank file or the agent at once. Anything else (draft, jobFind, jobMark, jobsIngest, unknown) runs a
 *  subprocess in the host or is not known to be quick, and goes per-message so it cannot hold up the serial host's bank ops. */
const PORT_OPS = new Set([
  "ping", "get", "getChunk", "put", "patch", "log",
  "resumePut", "resumeGet", "resumeInfo", "resumeDelete", "jobStart", "jobResult",
]);
/** Ops that change nothing, so running one twice is harmless. */
const READ_ONLY_OPS = new Set(["ping", "get", "getChunk", "resumeGet", "resumeInfo"]);

interface Pending {
  msg: object;
  resolve: (reply: unknown) => void;
  reject: (err: Error) => void;
  timer: ReturnType<typeof setTimeout>;
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
      if (conn.inFlight.length >= maxInFlight) return;
      const p = queue.shift()!;
      conn.inFlight.push(p);
      try { conn.port.postMessage(p.msg); } catch (e) {
        poison(conn, "disconnect", e instanceof Error ? e.message : String(e));
        return;
      }
    }
  }

  function enqueue(msg: object): Promise<unknown> {
    if (closed) return Promise.reject(new PortError("native port closed", "closed"));
    if (queue.length + (conn?.inFlight.length ?? 0) >= maxQueued) {
      return Promise.reject(new PortError("too many pending native calls", "overflow"));
    }
    return new Promise((resolve, reject) => {
      const p: Pending = {
        msg, resolve, reject,
        timer: setTimeout(() => {
          const c = conn;
          if (c && c.inFlight.includes(p)) {
            // Its reply may still arrive and would be taken for the next call's: start over.
            poison(c, "timeout", `native call timed out after ${callTimeoutMs} ms`);
            return;
          }
          const i = queue.indexOf(p);
          if (i >= 0) queue.splice(i, 1); // never written, so nothing can come back for it
          p.reject(new PortError(`native call timed out after ${callTimeoutMs} ms`, "timeout"));
        }, callTimeoutMs),
      };
      queue.push(p);
      pump();
    });
  }

  function attempt(msg: object, op: string, retried: boolean): Promise<unknown> {
    if (failures > 0 && now() < retryAt) return opts.spawn(msg);
    return enqueue(msg).catch(e => {
      if (!retried && e instanceof PortError && e.kind === "disconnect" && READ_ONLY_OPS.has(op)) {
        return attempt(msg, op, true);
      }
      throw e;
    });
  }

  const transport = ((msg: object) => {
    const op = (msg as { op?: unknown }).op;
    if (typeof op !== "string" || !PORT_OPS.has(op)) return opts.spawn(msg);
    return attempt(msg, op, false);
  }) as Transport & { close(): void };

  transport.close = () => {
    closed = true;
    if (conn) poison(conn, "closed", "native port closed");
    rejectAll(() => new PortError("native port closed", "closed"), []);
  };
  return transport;
}
