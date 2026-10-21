import { describe, expect, test } from "bun:test";
import { type Answer, type Bank, emptyBank } from "../src/types";
import { ChunkIntegrityError, type LocalArea, type Transport, createStore } from "../src/store";

function answer(key: string, value: string): Answer {
  return { key, labels: [key], kind: "text", value: [value], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01", review: false };
}
function bankOf(...as: Answer[]): Bank {
  const b = emptyBank();
  for (const a of as) b.answers[a.key] = a;
  return b;
}
class FakeLocal implements LocalArea {
  data: Record<string, unknown> = {};
  async get(k: string) { return k in this.data ? { [k]: this.data[k] } : {}; }
  async set(i: Record<string, unknown>) { Object.assign(this.data, i); }
  async remove(k: string) { delete this.data[k]; }
}

const bytes = (s: string) => new TextEncoder().encode(s).length;

/** Scripted chunked host. `first` is spread over the get reply; `rest` are the getChunk replies by index. */
function host(first: Record<string, unknown>, rest: (i: number) => Record<string, unknown>) {
  const calls: string[] = [];
  const transport: Transport = async msg => {
    const m = msg as Record<string, unknown>;
    calls.push(String(m.op));
    if (m.op === "ping") return { ok: true, protocol: 1, rev: 3 };
    if (m.op === "get") return { rev: 3, chunked: true, chunkIndex: 0, ...first };
    if (m.op === "getChunk") return rest(Number(m.chunkIndex));
    return { error: "unexpected op in test" };
  };
  return { transport, calls, writes: () => calls.filter(c => c === "put" || c === "patch") };
}

const bank = () => bankOf(answer("kk", "café € \u{1F600}"));
const text = () => JSON.stringify(bank());
const halves = () => { const t = text(); const mid = Math.floor(t.length / 2); return [t.slice(0, mid), t.slice(mid)]; };

describe("chunked get: hostile host replies", () => {
  test("totalBytes that is not exactly the joined UTF-8 length is refused", async () => {
    const [a, b] = halves();
    const good = bytes(text());
    const bad: unknown[] = [NaN, Infinity, -Infinity, -1, 0, good + 0.5, String(good), `${good}`, null, undefined, true, [good], { v: good }, BigInt(good), good * 2];
    for (const total of bad) {
      const h = host({ totalBytes: total, chunkCount: 2, chunk: a }, () => ({ rev: 3, chunkIndex: 1, chunkCount: 2, chunk: b }));
      const store = createStore({ transport: h.transport, local: new FakeLocal() });
      await expect(store.loadBank(), String(typeof total) + ":" + String(total)).rejects.toBeInstanceOf(ChunkIntegrityError);
      await expect(store.updateBank(x => x)).rejects.toBeInstanceOf(ChunkIntegrityError);
      await expect(store.replaceBank(bank())).rejects.toBeInstanceOf(ChunkIntegrityError);
      expect(h.writes()).toEqual([]);
    }
  });

  test("an extra trailing chunk beyond chunkCount is ignored only when the total still matches", async () => {
    const [a, b] = halves();
    const total = bytes(text());
    // host sends a second reply for index 2 that the client never asks for: same result as the honest transfer
    const ok = host({ totalBytes: total, chunkCount: 2, chunk: a }, i => ({ rev: 3, chunkIndex: i, chunkCount: 2, chunk: i === 1 ? b : "JUNK" }));
    expect((await createStore({ transport: ok.transport, local: new FakeLocal() }).loadBank()).answers.kk).toBeDefined();
    // chunkCount lies low: the missing tail makes the total mismatch
    const short = host({ totalBytes: total, chunkCount: 1, chunk: a }, () => ({ rev: 3, chunk: b }));
    await expect(createStore({ transport: short.transport, local: new FakeLocal() }).loadBank()).rejects.toBeInstanceOf(ChunkIntegrityError);
    // chunkCount lies high: an extra chunk makes the total mismatch
    const long = host({ totalBytes: total, chunkCount: 3, chunk: a }, i => ({ rev: 3, chunkIndex: i, chunkCount: 3, chunk: i === 1 ? b : "x" }));
    const store = createStore({ transport: long.transport, local: new FakeLocal() });
    await expect(store.loadBank()).rejects.toBeInstanceOf(ChunkIntegrityError);
    expect(long.writes()).toEqual([]);
  });

  test("zero chunks / empty first chunk / missing chunk fields never load and never write", async () => {
    const total = bytes(text());
    const cases: Array<[string, Record<string, unknown>, (i: number) => Record<string, unknown>]> = [
      ["no chunk field", { totalBytes: total, chunkCount: 1 }, () => ({})],
      ["chunkCount NaN, chunk empty", { totalBytes: total, chunkCount: NaN, chunk: "" }, () => ({})],
      ["chunkCount negative, chunk empty", { totalBytes: total, chunkCount: -5, chunk: "" }, () => ({})],
      ["chunkCount a string", { totalBytes: total, chunkCount: "2", chunk: "" }, () => ({})],
      ["chunk of the wrong type", { totalBytes: total, chunkCount: 1, chunk: 42 }, () => ({})],
      ["second reply has no chunk", { totalBytes: total, chunkCount: 2, chunk: halves()[0] }, () => ({ rev: 3 })],
      ["second reply has another rev", { totalBytes: total, chunkCount: 2, chunk: halves()[0] }, () => ({ rev: 4, chunk: halves()[1] })],
    ];
    for (const [name, first, rest] of cases) {
      const h = host(first, rest);
      const store = createStore({ transport: h.transport, local: new FakeLocal() });
      await expect(store.loadBank(), name).rejects.toBeInstanceOf(Error);
      await expect(store.updateBank(x => x), name).rejects.toBeInstanceOf(Error);
      expect(h.writes(), name).toEqual([]);
    }
  });

  test("after a failed load, a later edit refuses again instead of writing a partial bank", async () => {
    const [a] = halves();
    const h = host({ totalBytes: bytes(text()), chunkCount: 1, chunk: a }, () => ({}));
    const store = createStore({ transport: h.transport, local: new FakeLocal() });
    await expect(store.loadBank()).rejects.toBeInstanceOf(ChunkIntegrityError);
    for (let i = 0; i < 3; i++) await expect(store.updateBank(b => ({ ...b, answers: { ...b.answers, new: answer("new", "v") } }))).rejects.toBeInstanceOf(ChunkIntegrityError);
    // patchAnswers does not read the bank at all (host-side patch op), so it is not a vector, but it must not fall back to put either
    expect(h.writes().filter(w => w === "put")).toEqual([]);
  });

  test("a conflict that points at a chunked bank is re-read through the integrity check", async () => {
    const [a] = halves();
    const calls: string[] = [];
    let gets = 0;
    const transport: Transport = async msg => {
      const m = msg as Record<string, unknown>;
      calls.push(String(m.op));
      if (m.op === "ping") return { ok: true, protocol: 1, rev: 3 };
      if (m.op === "get") { gets++; return gets === 1 ? { rev: 3, bank: bank() } : { rev: 4, chunked: true, totalBytes: bytes(text()), chunkIndex: 0, chunkCount: 1, chunk: a }; }
      if (m.op === "put") return { conflict: true, rev: 4, chunked: true };
      return { error: "x" };
    };
    const store = createStore({ transport, local: new FakeLocal() });
    await expect(store.updateBank(b => b)).rejects.toBeInstanceOf(ChunkIntegrityError);
    expect(calls.filter(c => c === "put").length).toBe(1);
  });

  test("a rev change mid-transfer (conflict) restarts the whole transfer and yields only the new bank", async () => {
    const t2 = JSON.stringify(bankOf(answer("kk", "café € \u{1F600}"), answer("zz", "new")));
    const mid2 = Math.floor(t2.length / 2);
    let gen = 0;
    const calls: string[] = [];
    const transport: Transport = async msg => {
      const m = msg as Record<string, unknown>;
      calls.push(String(m.op));
      if (m.op === "ping") return { ok: true, protocol: 1, rev: 3 };
      if (m.op === "get") {
        gen++;
        if (gen === 1) return { rev: 3, chunked: true, totalBytes: bytes(text()), chunkIndex: 0, chunkCount: 2, chunk: halves()[0] };
        return { rev: 4, chunked: true, totalBytes: bytes(t2), chunkIndex: 0, chunkCount: 2, chunk: t2.slice(0, mid2) };
      }
      if (m.op === "getChunk") return m.rev === 3 ? { conflict: true, rev: 4 } : { rev: 4, chunkIndex: 1, chunkCount: 2, chunk: t2.slice(mid2) };
      return { error: "x" };
    };
    const loaded = await createStore({ transport, local: new FakeLocal() }).loadBank();
    expect(Object.keys(loaded.answers).sort()).toEqual(["kk", "zz"]);
  });

  test("two different banks with the same total length, mixed across revs, are refused (rev mismatch on the chunk)", async () => {
    const a1 = JSON.stringify(bankOf(answer("kk", "A".repeat(40))));
    const a2 = JSON.stringify(bankOf(answer("kk", "B".repeat(40))));
    expect(bytes(a1)).toBe(bytes(a2));
    const m = Math.floor(a1.length / 2);
    // host replies to chunk 1 with the OTHER bank's second half under a different rev and no conflict flag
    const h = host({ totalBytes: bytes(a1), chunkCount: 2, chunk: a1.slice(0, m) }, () => ({ rev: 4, chunkIndex: 1, chunkCount: 2, chunk: a2.slice(m) }));
    const store = createStore({ transport: h.transport, local: new FakeLocal() });
    await expect(store.loadBank()).rejects.toBeInstanceOf(Error);
    expect(h.writes()).toEqual([]);
  });
});

describe("chunked get: hostile chunkCount", () => {
  test("a chunkCount that is not a sane integer is refused before any getChunk poll", async () => {
    const [a] = halves();
    const total = bytes(text());
    const bad: unknown[] = [1_000_000, Infinity, -Infinity, NaN, 0, -1, 1.5, total + 1, 289, Number.MAX_SAFE_INTEGER, "2", null, undefined, true];
    for (const count of bad) {
      let chunkCalls = 0;
      const h = host({ totalBytes: total, chunkCount: count, chunk: a }, i => { chunkCalls++; return { rev: 3, chunkIndex: i, chunkCount: count, chunk: "" }; });
      const store = createStore({ transport: h.transport, local: new FakeLocal() });
      const label = `${typeof count}:${String(count)}`;
      await expect(store.loadBank(), label).rejects.toBeInstanceOf(ChunkIntegrityError);
      await expect(store.updateBank(x => x), label).rejects.toBeInstanceOf(ChunkIntegrityError);
      expect(chunkCalls, label).toBeLessThan(3);
      expect(h.writes(), label).toEqual([]);
    }
  });

  test("a totalBytes over the bank cap is refused before any getChunk poll", async () => {
    const [a] = halves();
    let chunkCalls = 0;
    const h = host({ totalBytes: 50_000_001, chunkCount: 2, chunk: a }, i => { chunkCalls++; return { rev: 3, chunkIndex: i, chunkCount: 2, chunk: "" }; });
    await expect(createStore({ transport: h.transport, local: new FakeLocal() }).loadBank()).rejects.toBeInstanceOf(ChunkIntegrityError);
    expect(chunkCalls).toBe(0);
  });
});
