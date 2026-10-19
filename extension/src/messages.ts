// Messages between the content script, the popup and the background worker.
// The background worker is the only writer to storage.
import type { Bank, FieldInfo, Settings } from "./types";
import type { CaptureResult } from "./bank";
import type { FillOutcome } from "./fill";
import type { Coverage } from "./coverage";
import type { DraftReply, JobInfo } from "./draft";

export type ToBackground =
  | { type: "getBank" }
  | { type: "capture"; host: string; fields: FieldInfo[] }
  | { type: "filled"; keys: string[] }
  | { type: "setSettings"; settings: Partial<Settings> }
  | { type: "deleteAnswer"; key: string }
  | { type: "editAnswer"; key: string; value: string[] }
  | { type: "replaceBank"; json: string }
  /** Only the question label and the posting as shown on the page: never field values or bank content. */
  | { type: "draft"; question: string; job: JobInfo; maxWords?: number };

export type DraftResponse = DraftReply;

export type ToContent =
  | { type: "scan" }
  | { type: "fill"; overwrite?: boolean; typeahead?: boolean }
  | { type: "capture" };

/** `fields`/`matched` are kept for older callers; `coverage` is the full breakdown. */
export interface ScanReply { fields: number; matched: number; host: string; disabled: boolean; coverage?: Coverage }
export interface FillReply { outcomes: FillOutcome[] }
/** `unchanged` counts answers the bank already held, which a repeat save skips. */
export type CaptureReply = Omit<CaptureResult, "bank"> & { unchanged?: number };
export type BankReply = { bank: Bank; mode: "mac-app" | "browser"; note: string | null } | { error: string };
