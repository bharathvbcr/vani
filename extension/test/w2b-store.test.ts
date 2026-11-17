// Slice w2b (105): getSnapshot re-reads a moving bank a bounded number of times.
import { describe, expect, test } from "bun:test";
import { type LocalArea, type Transport, createStore } from "../src/store";
import type { Answer } from "../src/types";

function answer(key: string): Answer {
  return { key, labels: [key], kind: "text", value: ["v"], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01", review: false };
}
class FakeLocal implements LocalArea {
  data: Record<string, unknown> = {};
  async get(k: string) { return k in this.data ? { [k]: this.data[k] } : {}; }
  async set(i: Record<string, unknown>) { Object.assign(this.data, i); }
  async remove(k: string) { delete this.data[k]; }
}

describe("105: getSnapshot gives up after a stated number of conflicting transfers", () => {
  /** A host that moves its revision under every chunked transfer: every getChunk is a conflict. Capped so a regression ends. */
  function restless() {
    let gets = 0;
    let chunks = 0;
    const transport: Transport = async msg => {
      const m = msg as Record<string, unknown>;
      if (m.op === "ping") return { ok: true, protocol: 1, rev: 3 };
      if (m.op === "get") { gets++; return { rev: 3 + gets, chunked: true, chunkIndex: 0, chunkCount: 2, totalBytes: 10, chunk: "{" }; }
      if (m.op === "getChunk") {
        if (++chunks > 40) throw new Error("test cap: the client never stopped re-reading");
        return { conflict: true, rev: 99 };
      }
      return { error: "unexpected op in test" };
    };
    return { transport, gets: () => gets };
  }

  test("a store that conflicts forever stops after 5 reads and rejects with a message the caller can show", async () => {
    const h = restless();
    const store = createStore({ transport: h.transport, local: new FakeLocal() });
    await expect(store.loadBank()).rejects.toThrow(/kept changing while it was being read \(5 tries\)/);
    expect(h.gets()).toBe(5);
  });

  test("every reader of the snapshot gets the same bound, and no write is attempted", async () => {
    const h = restless();
    const store = createStore({ transport: h.transport, local: new FakeLocal() });
    await expect(store.updateBank(b => b)).rejects.toThrow(/kept changing/);
    expect(h.gets()).toBe(5);
  });

  test("a transfer that conflicts twice and then settles still succeeds", async () => {
    const text = JSON.stringify({ version: 1, answers: { kk: answer("kk") }, settings: { captureSelfId: false, disabledHosts: [] } });
    const mid = Math.floor(text.length / 2);
    const total = new TextEncoder().encode(text).length;
    let gets = 0;
    const transport: Transport = async msg => {
      const m = msg as Record<string, unknown>;
      if (m.op === "ping") return { ok: true, protocol: 1, rev: 3 };
      if (m.op === "get") { gets++; return { rev: 3 + gets, chunked: true, chunkIndex: 0, chunkCount: 2, totalBytes: total, chunk: text.slice(0, mid) }; }
      if (m.op === "getChunk") return gets <= 2 ? { conflict: true, rev: 99 } : { rev: m.rev, chunkIndex: 1, chunkCount: 2, chunk: text.slice(mid) };
      return { error: "unexpected op in test" };
    };
    const bank = await createStore({ transport, local: new FakeLocal() }).loadBank();
    expect(bank.answers.kk).toBeDefined();
    expect(gets).toBe(3);
  });
});
