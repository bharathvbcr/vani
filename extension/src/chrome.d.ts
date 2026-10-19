// The slice of the Chrome extension API this extension uses. Declared here
// instead of adding @types/chrome as a dependency.
declare namespace chrome {
  namespace runtime {
    interface MessageSender { tab?: { id?: number; url?: string } }
    const lastError: { message?: string } | undefined;
    function sendMessage<T = unknown>(message: unknown): Promise<T>;
    /** Callback form only: Chrome does not return a promise from this call. The reply is whatever JSON the host sent. */
    function sendNativeMessage(application: string, message: object, cb: (response: any) => void): void;
    /** A long-lived native port. While it is open Chrome keeps the service worker alive (Chrome 105+). */
    interface Port {
      postMessage(message: object): void;
      disconnect(): void;
      onMessage: { addListener(cb: (message: unknown) => void): void };
      onDisconnect: { addListener(cb: () => void): void };
    }
    function connectNative(application: string): Port;
    const onMessage: {
      addListener(cb: (message: any, sender: MessageSender, sendResponse: (response?: unknown) => void) => boolean | void): void;
    };
  }
  namespace storage {
    const local: {
      get(keys: string | string[]): Promise<Record<string, unknown>>;
      set(items: Record<string, unknown>): Promise<void>;
      remove(keys: string | string[]): Promise<void>;
    };
  }
  namespace tabs {
    interface Tab { id?: number; url?: string }
    function query(q: { active: boolean; currentWindow: boolean }): Promise<Tab[]>;
    function sendMessage<T = unknown>(tabId: number, message: unknown): Promise<T>;
  }
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
