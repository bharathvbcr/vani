import { describe, expect, test } from "bun:test";
import { MAX_OBSERVED_ROOTS, carryMatches, observeRoots, recordAttempts, runRounds, untriedRefs, waitForQuiet } from "../src/rounds";

// A fake page: fields appear when another is filled, as a state list does after the country.
interface FakeEl { id: string; isConnected: boolean }
interface FakeRef { id: string; els: FakeEl[]; info: { value: string[]; kind: string; label: string; name: string; options: string[] }; widget: string; manual: string }
interface FakeOutcome { filled: boolean; reason?: string }

const el = (id: string, isConnected = true): FakeEl => ({ id, isConnected });
const field = (id: string, e: FakeEl = el(id), value: string[] = []): FakeRef =>
  ({ id, els: [e], info: { value, kind: "text", label: id, name: id, options: [] }, widget: "native", manual: "" });
const asEl = (e: FakeEl): Element => e as unknown as Element;

function page(initial: FakeRef[]) {
  const p = {
    fields: initial,
    rounds: [] as string[][],
    clock: 0,
    settles: [] as number[],
    /** What filling does to the page: id -> fields it adds. */
    reveals: {} as Record<string, FakeRef[]>,
    cost: 0,
    never: new Set<string>(),
  };
  const deps = {
    scan: () => p.fields.slice(),
    fill: async (refs: FakeRef[], round: number): Promise<FakeOutcome[]> => {
      p.rounds.push(refs.map(r => r.id));
      p.clock += p.cost;
      return refs.map(r => {
        if (p.never.has(r.id)) return { filled: false, reason: "no_match" };
        r.info.value = ["x"];
        if (p.reveals[r.id]) p.fields = [...p.fields, ...p.reveals[r.id]!.map(f => ({ ...f }))];
        return { filled: true };
      });
    },
    settle: async (ms: number) => { p.settles.push(ms); p.clock += 250; },
    now: () => p.clock,
  };
  return { p, deps };
}

describe("runRounds", () => {
  test("a state list that appears only after the country is filled is filled in round 2", async () => {
    const { p, deps } = page([field("country"), field("name")]);
    p.reveals.country = [field("state")];
    const out = await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["country", "name"], ["state"]]);
    expect(out.filter(o => o.filled).length).toBe(3);
  });

  test("an 'Other: please specify' box that appears after a radio is filled", async () => {
    const { p, deps } = page([field("choice")]);
    p.reveals.choice = [field("other")];
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["choice"], ["other"]]);
  });

  test("a page where nothing appears settles once and stops after round 1", async () => {
    const { p, deps } = page([field("a"), field("b")]);
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["a", "b"]]);
    expect(p.settles.length).toBe(1);
  });

  test("a round that filled nothing does not wait for the page", async () => {
    const { p, deps } = page([field("a")]);
    p.never.add("a");
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.settles).toEqual([]);
  });

  test("a page that grows a new field after every fill stops after 3 rounds", async () => {
    const { p, deps } = page([field("f0")]);
    for (let i = 0; i < 10; i++) p.reveals[`f${i}`] = [field(`f${i + 1}`)];
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["f0"], ["f1"], ["f2"]]);
  });

  test("a slow page stops at the 6 s budget, not starting a round it has no time for", async () => {
    const { p, deps } = page([field("f0")]);
    for (let i = 0; i < 10; i++) p.reveals[`f${i}`] = [field(`f${i + 1}`)];
    p.cost = 3000; // each round takes 3 s
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    // round 1 ends at 3000, settle to 3250, round 2 ends at 6250: the budget is spent, so no round 3.
    expect(p.rounds).toEqual([["f0"], ["f1"]]);
  });

  test("the wait for quiet is capped by what is left of the budget", async () => {
    const { p, deps } = page([field("f0")]);
    p.reveals.f0 = [field("f1")];
    p.cost = 5500;
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.settles[0]).toBe(500);
  });

  test("a field the user cleared between rounds is not refilled", async () => {
    const { p, deps } = page([field("country"), field("name")]);
    p.reveals.country = [field("state")];
    const settle = deps.settle;
    deps.settle = async ms => {
      await settle(ms);
      for (const f of p.fields) if (f.id === "name") f.info.value = []; // the user empties it while the page settles
    };
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["country", "name"], ["state"]]);
  });

  test("a field that already has a value is left to the fill (which never overwrites in later rounds)", async () => {
    const { p, deps } = page([field("country")]);
    p.reveals.country = [field("state", el("state"), ["Bavaria"])];
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["country"]]);
  });

  test("a field detached between the scan and the round is skipped", async () => {
    const gone = el("state", false);
    const { p, deps } = page([field("country")]);
    p.reveals.country = [field("state", gone)];
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["country"]]);
  });

  test("a list that had no options yet is tried again in a later round", async () => {
    const { p, deps } = page([field("country"), field("state")]);
    let tries = 0;
    const fill = deps.fill;
    deps.fill = async (refs, round) => {
      const out = await fill(refs, round);
      return refs.map((r, i) => {
        if (r.id !== "state" || tries++ > 0) return out[i]!;
        r.info.value = []; // nothing was chosen: the list had no options yet
        return { filled: false, reason: "no_option" };
      });
    };
    // the first fill of "state" fails with no_option but "country" filled, so the page is rescanned
    const out = await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds[0]).toEqual(["country", "state"]);
    expect(p.rounds[1]).toEqual(["state"]);
    expect(out.length).toBe(3);
  });

  test("the round number is passed through, so the caller can restrict overwriting to round 1", async () => {
    const seen: number[] = [];
    const { p, deps } = page([field("country")]);
    p.reveals.country = [field("state")];
    const fill = deps.fill;
    deps.fill = async (refs, round) => { seen.push(round); return fill(refs, round); };
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(seen).toEqual([1, 2]);
  });
});

describe("untriedRefs / recordAttempts", () => {
  test("only never-attempted, attached, empty fields", () => {
    const a = field("a"), b = field("b"), c = field("c", el("c", false)), d = field("d", el("d"), ["v"]), e = field("e");
    const seen = new WeakSet<Element>();
    recordAttempts(seen, [a], [{ filled: true }]);
    expect(untriedRefs(seen, [a, b, c, d, e] as never).map(r => (r as unknown as FakeRef).id)).toEqual(["b", "e"]);
  });

  test("every element of a group counts as attempted", () => {
    const radios: FakeRef = { ...field("g"), els: [el("g1"), el("g2")] };
    const seen = new WeakSet<Element>();
    recordAttempts(seen, [radios] as never, [{ filled: true }]);
    const again: FakeRef = { ...field("g"), els: [radios.els[1]!] };
    expect(untriedRefs(seen, [again] as never)).toEqual([]);
    expect(seen.has(asEl(radios.els[0]!))).toBe(true);
  });
});

describe("carryMatches", () => {
  const withInfo = (id: string, e: FakeEl, label = id): FakeRef => ({ ...field(id, e), info: { ...field(id, e).info, label } });

  test("carries a match to a fresh field over the same elements with the same description", () => {
    const e1 = el("a"), e2 = el("b");
    const oldA = withInfo("a", e1), oldB = withInfo("b", e2);
    const matches = new Map<FakeRef, string | null>([[oldA, "A"], [oldB, null]]);
    const freshA = withInfo("a", e1), freshB = withInfo("b", e2);
    const out = carryMatches([freshA, freshB] as never, { refs: [oldA, oldB] as never, matches: matches as never });
    expect(out.get(freshA as never)).toBe("A");
    expect(out.has(freshB as never)).toBe(true);
    expect(out.get(freshB as never)).toBeNull();
  });

  test("leaves out a field whose label changed, whose element was replaced, or that is detached", () => {
    const e1 = el("a"), e2 = el("b"), e3 = el("c");
    const oldA = withInfo("a", e1), oldB = withInfo("b", e2), oldC = withInfo("c", e3);
    const matches = new Map<FakeRef, string | null>([[oldA, "A"], [oldB, "B"], [oldC, "C"]]);
    const relabelled = withInfo("a", e1, "now a different question");
    const replaced = withInfo("b", el("b2"));
    const detached = withInfo("c", { ...e3, isConnected: false });
    const out = carryMatches([relabelled, replaced, detached] as never, { refs: [oldA, oldB, oldC] as never, matches: matches as never });
    expect(out.size).toBe(0);
  });

  test("a field the earlier scan never matched (skipped) is not invented", () => {
    const e1 = el("a");
    const oldA = withInfo("a", e1);
    const out = carryMatches([withInfo("a", e1)] as never, { refs: [oldA] as never, matches: new Map() as never });
    expect(out.size).toBe(0);
  });
});

describe("waitForQuiet", () => {
  test("resolves after the quiet period when nothing changes", async () => {
    let stopped = 0;
    const t0 = Date.now();
    await waitForQuiet(() => () => { stopped++; }, 30, 500);
    expect(Date.now() - t0).toBeGreaterThanOrEqual(25);
    expect(Date.now() - t0).toBeLessThan(400);
    expect(stopped).toBe(1);
  });

  test("changes push the quiet period back, but the cap ends the wait on a page that never settles", async () => {
    let change: () => void = () => undefined;
    let stopped = 0;
    const timer = setInterval(() => change(), 10);
    const t0 = Date.now();
    await waitForQuiet(onChange => { change = onChange; return () => { stopped++; }; }, 50, 150);
    clearInterval(timer);
    const took = Date.now() - t0;
    expect(took).toBeGreaterThanOrEqual(140);
    expect(took).toBeLessThan(400);
    expect(stopped).toBe(1);
  });

  test("an observer that cannot start does not hang the fill", async () => {
    await waitForQuiet(() => { throw new Error("no observer"); }, 20, 200);
  });
});

// A MutationObserver that records what it watches, so a shadow root is a root of its own, as in a browser.
class FakeMO {
  static live = new Set<FakeMO>();
  static created = 0;
  static failOn: unknown = undefined;
  target: unknown;
  constructor(private cb: () => void) { FakeMO.created++; }
  observe(target: unknown): void {
    if (target === FakeMO.failOn) throw new Error("cannot observe");
    this.target = target;
    FakeMO.live.add(this);
  }
  disconnect(): void { FakeMO.live.delete(this); }
  /** A node was added under `root`: only observers of that very root hear it. */
  static mutate(root: unknown): void { for (const o of [...FakeMO.live]) if (o.target === root) o.cb(); }
}
const fakeDoc = { nodeType: 9, defaultView: { MutationObserver: FakeMO } };
const fakeShadow = (): Node => ({ nodeType: 11, ownerDocument: fakeDoc }) as unknown as Node;
const docNode = fakeDoc as unknown as Node;
const resetMO = (): void => { FakeMO.live.clear(); FakeMO.created = 0; FakeMO.failOn = undefined; };

describe("observeRoots / waitForQuiet over shadow roots", () => {
  test("a field inserted into a shadow root during the quiet window delays quiet and is filled in the next round", async () => {
    resetMO();
    const shadow = fakeShadow();
    const roots = [docNode, shadow];
    const { p, deps } = page([field("country")]);
    const QUIET = 60;
    const fill = deps.fill;
    // The country's fill makes a dependent field appear 50 ms later, inside the shadow root only.
    deps.fill = async (refs, round) => {
      const out = await fill(refs, round);
      if (round === 1) setTimeout(() => { p.fields = [...p.fields, field("state")]; FakeMO.mutate(shadow); }, 50);
      return out;
    };
    let settled = 0;
    deps.settle = async ms => {
      const t0 = performance.now();
      await waitForQuiet(onChange => observeRoots(roots, onChange), QUIET, ms);
      settled ||= performance.now() - t0; // the first settle is the one the insertion lands in
    };
    await runRounds<FakeRef, FakeOutcome>(deps as never);
    expect(p.rounds).toEqual([["country"], ["state"]]);
    // quiet restarts at the insertion (50 ms): it ends >= 50 + 60 ms after the settle began. Watching the document alone would end at 60.
    expect(settled).toBeGreaterThanOrEqual(100);
    expect(FakeMO.live.size).toBe(0);
  });

  test("the same insertion is invisible to an observer on the document alone (what the roots fix)", async () => {
    resetMO();
    const shadow = fakeShadow();
    const t0 = performance.now();
    setTimeout(() => FakeMO.mutate(shadow), 40);
    await waitForQuiet(onChange => observeRoots([docNode], onChange), 60, 500);
    expect(performance.now() - t0).toBeLessThan(95);
    expect(FakeMO.live.size).toBe(0);
  });

  test("every root's observer is disconnected after quiet", async () => {
    resetMO();
    await waitForQuiet(onChange => observeRoots([docNode, fakeShadow(), fakeShadow()], onChange), 20, 500);
    expect(FakeMO.created).toBe(3);
    expect(FakeMO.live.size).toBe(0);
  });

  test("every root's observer is disconnected after the budget cap on a page that never settles", async () => {
    resetMO();
    const shadow = fakeShadow();
    const timer = setInterval(() => FakeMO.mutate(shadow), 10);
    const t0 = performance.now();
    await waitForQuiet(onChange => observeRoots([docNode, shadow, fakeShadow()], onChange), 50, 150);
    clearInterval(timer);
    expect(performance.now() - t0).toBeGreaterThanOrEqual(140);
    expect(FakeMO.created).toBe(3);
    expect(FakeMO.live.size).toBe(0);
  });

  test("a root that cannot be observed leaves none of the others running", async () => {
    resetMO();
    const bad = fakeShadow();
    FakeMO.failOn = bad;
    expect(() => observeRoots([docNode, fakeShadow(), bad, fakeShadow()], () => undefined)).toThrow("cannot observe");
    expect(FakeMO.live.size).toBe(0);
    await waitForQuiet(onChange => observeRoots([docNode, bad], onChange), 20, 200); // does not hang or leak
    expect(FakeMO.live.size).toBe(0);
  });

  test("a stop that throws still resolves the wait, with every observer disconnected", async () => {
    resetMO();
    const roots = [docNode, fakeShadow()];
    await waitForQuiet(onChange => {
      const stop = observeRoots(roots, onChange);
      return () => { stop(); throw new Error("stop failed"); };
    }, 20, 100);
    expect(FakeMO.live.size).toBe(0);
  });

  test("no more than 64 roots are observed", () => {
    resetMO();
    const stop = observeRoots([docNode, ...Array.from({ length: 100 }, fakeShadow)], () => undefined);
    expect(FakeMO.created).toBe(MAX_OBSERVED_ROOTS);
    expect(MAX_OBSERVED_ROOTS).toBe(64);
    stop();
    expect(FakeMO.live.size).toBe(0);
  });
});
