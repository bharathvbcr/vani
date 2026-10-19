// The slice of the Chrome extension API this extension uses. Declared here
// instead of adding @types/chrome as a dependency.
declare namespace chrome {
  namespace runtime {
    /** `tab` is absent for an extension page such as the popup; `frameId` is 0 for a tab's top frame. */
    interface MessageSender { tab?: { id?: number; url?: string }; frameId?: number; url?: string }
    const lastError: { message?: string } | undefined;
    function sendMessage<T = unknown>(message: unknown): Promise<T>;
    /** Callback form only: Chrome does not return a promise from this call. The reply is whatever JSON the host sent: check it before use. */
    function sendNativeMessage(application: string, message: object, cb: (response: unknown) => void): void;
    /** A long-lived native port. While it is open Chrome keeps the service worker alive (Chrome 105+). */
    interface Port {
      postMessage(message: object): void;
      disconnect(): void;
      onMessage: { addListener(cb: (message: unknown) => void): void };
      onDisconnect: { addListener(cb: () => void): void };
    }
    function connectNative(application: string): Port;
    /**
     * `message` is whatever another part of the extension sent: check it (messages.ts) before use. Return true only when
     * sendResponse will be called later; Chrome then holds the sender's port open until it is.
     */
    const onMessage: {
      addListener(cb: (message: unknown, sender: MessageSender, sendResponse: (response?: unknown) => void) => boolean): void;
    };
  }
  namespace storage {
    const local: {
      get(keys: string | string[]): Promise<Record<string, unknown>>;
      set(items: Record<string, unknown>): Promise<void>;
      remove(keys: string | string[]): Promise<void>;
    };
    /** In memory for the browser session, kept across the worker being stopped. Absent in older browsers. */
    const session: {
      get(keys: string | string[]): Promise<Record<string, unknown>>;
      set(items: Record<string, unknown>): Promise<void>;
    } | undefined;
  }
  namespace tabs {
    /** `url` is present for the active tab once the user has opened the popup (activeTab) or on a matched host. */
    interface Tab { id?: number; url?: string }
    function query(q: { active: boolean; currentWindow: boolean }): Promise<Tab[]>;
    /** Without `frameId` every frame with a listener receives it and the promise takes the first reply; frames.ts always names one. */
    function sendMessage<T = unknown>(tabId: number, message: unknown, options?: { frameId?: number }): Promise<T>;
    const onUpdated: { addListener(cb: (tabId: number, changeInfo: { status?: string }) => void): void };
    const onRemoved: { addListener(cb: (tabId: number) => void): void };
  }
  /** The toolbar button; a badge marks a tab with a question waiting. May be absent (Safari). */
  const action: {
    setBadgeText(d: { tabId: number; text: string }): Promise<void>;
    setBadgeBackgroundColor(d: { tabId: number; color: string }): Promise<void>;
  } | undefined;
  namespace scripting {
    /** One entry per frame the script ran in; `result` is the function's return value (a returned promise is awaited). */
    interface InjectionResult<T> { frameId: number; result?: T }
    function executeScript(d: { target: { tabId: number; allFrames?: boolean }; files: string[] }): Promise<InjectionResult<unknown>[]>;
    function executeScript<T>(d: { target: { tabId: number; allFrames?: boolean }; func: () => T | Promise<T> }): Promise<InjectionResult<T>[]>;
  }
  namespace commands {
    const onCommand: { addListener(cb: (command: string) => void): void };
  }
}

/** Present in Safari (and Firefox); absent in Chrome. Promise-based. */
declare var browser: {
  runtime?: { sendNativeMessage?(application: string, message: object): Promise<unknown> };
} | undefined;
