// Background entry for the live check only (test/live.ts): the real background
// worker, plus the frame fan-out exposed on the worker's global so the harness
// can call it the way the popup does. Never shipped.
import { toTabFrames } from "../src/background";
import { mergeFill, sumCaptures } from "../src/frames";

(globalThis as Record<string, unknown>).vaniLive = { toFrames: toTabFrames, mergeFill, sumCaptures };
