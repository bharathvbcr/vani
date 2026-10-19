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
let helpers = URL(fileURLWithPath: CommandLine.arguments[0]).resolvingSymlinksInPath().deletingLastPathComponent()
var vaniBinary = helpers.appendingPathComponent("vani")
if let i = args.firstIndex(of: "--vani"), i + 1 < args.count {
    guard args[i + 1].hasPrefix("/") else { fail("--vani needs an absolute path") }
    vaniBinary = URL(fileURLWithPath: args[i + 1])
}
// `--no-keychain` (tests only) supplies no key instead of reading the login Keychain.
let settings = DraftSettings.load(from: store.directory)
let drafter = args.contains("--no-keychain")
    ? settings.runner(vaniBinary: vaniBinary, key: { nil })
    : settings.runner(vaniBinary: vaniBinary)

// Exit after 60 s with no request, but never while one is being answered: a
// draft can take up to DraftRunner.timeout.
let idleSeconds = 60.0
let lastActivity = NSLock()
nonisolated(unsafe) var lastRequest = Date()
nonisolated(unsafe) var busy = false
Thread.detachNewThread {
    while true {
        Thread.sleep(forTimeInterval: 5)
        lastActivity.lock()
        let idle = busy ? 0 : Date().timeIntervalSince(lastRequest)
        lastActivity.unlock()
        if idle > idleSeconds { exit(0) }
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
        reply = NativeProtocol.handle(obj, store: store, source: .chrome, drafter: drafter)
    } else {
        reply = ["error": "request is not JSON"]
    }
    output.write(NativeMessaging.encodeReply(reply))
    lastActivity.lock(); lastRequest = Date(); busy = false; lastActivity.unlock()
}
