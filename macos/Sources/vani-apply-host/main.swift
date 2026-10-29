import AppKit
import Foundation
import VaniApplyKit

// Chrome native messaging host for Vani Apply. Chrome starts it with the
// calling extension's origin as the first argument, writes framed requests
// to stdin and reads framed replies from stdout. It answers until stdin
// closes, and exits after 60 seconds without a request. Nothing is written
// to stdout except replies; diagnostics go to stderr, never answer values.

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("vani-apply-host: \(message)\n".utf8))
    exit(1)
}

let args = CommandLine.arguments
if args.count >= 2, args[1] == "--version" {
    print("vani-apply-host protocol \(NativeProtocol.version)")
    exit(0)
}
// Chrome passes "chrome-extension://<id>/"; anything else is not Chrome.
// This check only turns away a hand-run host. The boundary is Chrome's own:
// it starts this host only for an extension id listed in allowed_origins of
// the manifest ChromeConnector writes on Connect. The argument comes from
// whoever launched the process, so matching the id here would add no
// protection (a same-user process can pass any id), and the id the user
// chose lives in the app's defaults, which this host cannot read. A 2026-10
// audit's exact-id check was declined on that ground (Fable D18).
guard args.count >= 2, args[1].hasPrefix("chrome-extension://") else {
    fail("started without a chrome-extension:// origin; this binary is run by Chrome, not by hand")
}

// `--store-dir <absolute path>` (after the origin) points the host at another
// store; the process tests use it so they never touch the real App Group
// container. Chrome never passes it.
let store: BankStore
if let i = args.firstIndex(of: "--store-dir"), i + 1 < args.count {
    guard args[i + 1].hasPrefix("/") else { fail("--store-dir needs an absolute path") }
    store = BankStore(directory: URL(fileURLWithPath: args[i + 1], isDirectory: true))
} else {
    do { store = try BankStore.shared() } catch { fail("\(error)") }
}

// Drafting runs the vani binary bundled beside this host, with the paths the
// app saved in the group folder. `--vani <absolute path>` overrides the binary
// (the process tests use it); Chrome never passes it.
var vaniBinary = VaniProcess.helpersDirectory().appendingPathComponent("vani")
if let i = args.firstIndex(of: "--vani"), i + 1 < args.count {
    guard args[i + 1].hasPrefix("/") else { fail("--vani needs an absolute path") }
    vaniBinary = URL(fileURLWithPath: args[i + 1])
}
// An anthropic draft starts vani through anthropic-account, found where it is
// installed; `--anthropic-account <absolute path>` overrides it (the process
// tests use it). `--no-keychain` (tests only) supplies no key at all: vani
// starts directly, without the launcher. Chrome passes neither.
var launcher = AnthropicAccount.locate()
if let i = args.firstIndex(of: "--anthropic-account"), i + 1 < args.count {
    guard args[i + 1].hasPrefix("/") else { fail("--anthropic-account needs an absolute path") }
    launcher = URL(fileURLWithPath: args[i + 1])
}
// A draft-settings.json that is present but unreadable is not "defaults": the drafter then refuses
// every draft with code "settings", and the Jobs backend refuses find/mark/ingest with the reason.
let drafter = DraftSettings.runner(loadingFrom: store.directory, vaniBinary: vaniBinary,
                                   keys: args.contains("--no-keychain") ? .noKey : .anthropicAccount(launcher))
// The extension's find/mark ops read and write the Jobs marks through `vani jobs`.
// Each is bounded under the extension's own 10 s wait, so a hung vani is a coded
// error there instead of a host that never answers.
let jobs = DraftSettings.jobsBackend(loadingFrom: store.directory, vaniBinary: vaniBinary, timeout: 8)

// The on-device model helper for the sameQuestion op sits in the app's Contents/MacOS, beside the Helpers
// folder this host runs from. `--fm <absolute path>` overrides it (the process tests use it); Chrome never passes it.
var fmHelper = FMHelper.bundled()
if let i = args.firstIndex(of: "--fm"), i + 1 < args.count {
    guard args[i + 1].hasPrefix("/") else { fail("--fm needs an absolute path") }
    fmHelper = FMHelper(binary: URL(fileURLWithPath: args[i + 1]))
}

// Exit after 60 s with no request, but never while one is being answered: a
// draft can take up to DraftRunner.timeout.
let idleSeconds = 60.0
let lastActivity = NSLock()
nonisolated(unsafe) var lastRequest = Date()
nonisolated(unsafe) var busy = false
/// Whether bytes are waiting on stdin right now (poll with no wait).
@Sendable func inputPending() -> Bool {
    var fd = pollfd(fd: STDIN_FILENO, events: Int16(POLLIN), revents: 0)
    return poll(&fd, 1, 0) > 0
}
Thread.detachNewThread {
    while true {
        Thread.sleep(forTimeInterval: 5)
        lastActivity.lock()
        let idle = busy ? 0 : Date().timeIntervalSince(lastRequest)
        lastActivity.unlock()
        // A persistent port (Chrome's connectNative) can send a request at the moment the idle
        // time runs out: a frame already waiting on stdin is not idle, so it is answered first.
        if idle > idleSeconds && !inputPending() { exit(0) }
    }
}

let input = FileHandle.standardInput
let output = FileHandle.standardOutput
while true {
    let request: Data
    do {
        guard let next = try NativeMessaging.readMessage(from: input) else { exit(0) }
        request = next
    } catch NativeMessaging.ReadError.tooLarge(let n) {
        output.write(NativeMessaging.encodeReply(["error": "request of \(n) bytes is over the limit"]))
        continue
    } catch {
        fail("input ended mid-message")
    }
    lastActivity.lock(); lastRequest = Date(); busy = true; lastActivity.unlock()

    let reply: [String: Any]
    if let obj = try? JSONSerialization.jsonObject(with: request) {
        reply = NativeProtocol.handle(obj, store: store, source: .chrome, drafter: drafter, agent: XPCAgentClient(), jobs: jobs,
                                      openResume: { NSWorkspace.shared.open($0) }, sameQuestion: fmHelper)
    } else {
        reply = ["error": "request is not JSON"]
    }
    output.write(NativeMessaging.encodeReply(reply))
    lastActivity.lock(); lastRequest = Date(); busy = false; lastActivity.unlock()
}
