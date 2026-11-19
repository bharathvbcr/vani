import Foundation
import XCTest
@testable import VaniApplyKit

// Attack tests for ft-753373e2d375227c55ddb7fdd1342c79: a stopped vani's reply is kept so Stop shows the cost.
// Every child here is a shell script; none reads the Keychain, a key or the owner's state.

private final class AtkBox<T>: @unchecked Sendable { var value: T? }

private func atkScript(_ t: XCTestCase, _ body: String) throws -> (vani: URL, dir: URL) {
    let dir = try tempDir(t)
    let url = dir.appendingPathComponent("vani")
    try Data(("#!/bin/sh\n" + body + "\n").utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    return (url, dir)
}

private func atkWaitFile(_ file: URL, timeout: TimeInterval = 8) -> String? {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if let text = try? String(contentsOf: file, encoding: .utf8), !text.isEmpty { return text.trimmingCharacters(in: .whitespacesAndNewlines) }
        Thread.sleep(forTimeInterval: 0.02)
    }
    return nil
}

private let paid = #"{"error":"cancelled","code":"cancelled","cost":{"input_tokens":1790,"output_tokens":212,"usd":0.0072}}"#

/// Runs work on a thread, waits for the ready file, sets the flag, returns the value and the seconds the stop took.
private func atkStop<T: Sendable>(_ ready: URL, limit: TimeInterval = 12, _ work: @escaping @Sendable (CancelFlag) -> T) throws -> (T, TimeInterval) {
    let box = AtkBox<T>(), done = DispatchSemaphore(value: 0), cancel = CancelFlag()
    Thread.detachNewThread { box.value = work(cancel); done.signal() }
    guard atkWaitFile(ready) != nil else { throw XCTSkip("the fake never started") }
    let began = Date()
    cancel.set()
    XCTAssertEqual(done.wait(timeout: .now() + limit), .success, "a stopped call did not return")
    return (try XCTUnwrap(box.value), Date().timeIntervalSince(began))
}

private func openFDs() -> Int { (try? FileManager.default.contentsOfDirectory(atPath: "/dev/fd").count) ?? -1 }

final class RetriageStopAttackTests: XCTestCase {
    private func client(_ vani: URL) -> RetriageClient {
        RetriageClient(runner: DraftRunner(vaniBinary: vani, key: { nil }, decisionKey: { nil }))
    }

    private func run(_ vani: URL, input: Data? = nil, max: Int = 64 << 10, _ ready: URL) throws -> (VaniProcess.Outcome, TimeInterval) {
        try atkStop(ready) { cancel in
            VaniProcess.run(vani, arguments: [], environment: [:], input: input, timeout: 60, maxOutputBytes: max, cancellation: cancel)
        }
    }

    func testNewlineTerminatedReplyKeepsCost() throws {
        let dir = try tempDir(self)
        let reply = dir.appendingPathComponent("reply.json"), ready = dir.appendingPathComponent("ready")
        try Data((paid + "\n").utf8).write(to: reply)
        let (vani, _) = try atkScript(self, "trap 'cat \(reply.path); exit 1' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
        let c = client(vani)
        let (out, _) = try atkStop(ready) { c.run("k", cancellation: $0) }
        guard case let .failure(f) = out else { return XCTFail("a newline-terminated paid reply was dropped: \(out)") }
        XCTAssertEqual(f.cost?.usd, 0.0072)
    }

    func testBOMOrLeadingWhitespaceOnAPaidReplyStillCarriesTheCostOrIsAPlainCancelNeverACrash() throws {
        for prefix in ["\u{FEFF}", "  \n", "\t"] {
            let dir = try tempDir(self)
            let reply = dir.appendingPathComponent("reply.json"), ready = dir.appendingPathComponent("ready")
            try Data((prefix + paid + "\n").utf8).write(to: reply)
            let (vani, _) = try atkScript(self, "trap 'cat \(reply.path); exit 1' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
            let c = client(vani)
            let (out, _) = try atkStop(ready) { c.run("k", cancellation: $0) }
            if case let .failure(f) = out { XCTAssertEqual(f.cost?.usd, 0.0072) } else { XCTAssertEqual(out, .cancelled, "prefix \(prefix.debugDescription)") }
        }
    }

    func testDeeplyNestedJSONUnderTheCeilingIsAPlainCancelNotACrash() throws {
        let nested = String(repeating: "[", count: 30_000) + String(repeating: "]", count: 30_000)
        let big = #"{"code":"cancelled","cost":{"usd":0.5},"x":"# + nested + "}"
        let dir = try tempDir(self)
        let reply = dir.appendingPathComponent("reply.json"), ready = dir.appendingPathComponent("ready")
        try Data(big.utf8).write(to: reply)
        let (vani, _) = try atkScript(self, "trap 'cat \(reply.path); exit 1' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
        let c = client(vani)
        let (out, _) = try atkStop(ready) { c.run("k", cancellation: $0) }
        XCTAssertEqual(out, .cancelled)
    }

    func testAReplyWrittenThenSleepingWithoutExitingIgnoringTermIsKeptAndBounded() throws {
        let dir = try tempDir(self)
        let ready = dir.appendingPathComponent("ready")
        let (vani, _) = try atkScript(self, "trap '' TERM\nprintf '%s' '\(paid)'\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
        let (out, seconds) = try run(vani, ready)
        XCTAssertTrue(out.cancelled)
        XCTAssertLessThan(seconds, 2 * VaniProcess.terminationGrace + 1.5)
        XCTAssertEqual(String(decoding: out.stdout, as: UTF8.self), paid)
    }

    func testAGrandchildHoldingThePipesGivesEmptyStdoutBoundedTimeAndNoLeakedDescriptors() throws {
        func cycle() throws -> (VaniProcess.Outcome, TimeInterval) {
            let dir = try tempDir(self)
            let ready = dir.appendingPathComponent("ready"), gpid = dir.appendingPathComponent("gpid")
            let (vani, _) = try atkScript(self, "(trap '' TERM; exec sleep 30) &\necho $! > '\(gpid.path)'\ntrap 'printf %s '\"'\"'\(paid)'\"'\"'; exit 1' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
            let r = try run(vani, ready)
            if let g = atkWaitFile(gpid), let p = pid_t(g) { kill(p, SIGKILL) }
            return r
        }
        _ = try cycle()
        let before = openFDs()
        for _ in 0..<6 {
            let (out, seconds) = try cycle()
            XCTAssertTrue(out.cancelled)
            XCTAssertTrue(out.stdout.isEmpty, "a pipe a grandchild holds is never drained to the end: stdout=\(String(decoding: out.stdout, as: UTF8.self)) after \(seconds) s")
            XCTAssertLessThan(seconds, 2.5, "drain grace plus the reader join is bounded; a leaked reader would add the full 1 s join")
        }
        XCTAssertLessThanOrEqual(openFDs(), before + 2, "descriptors leaked across stops")
    }

    func testStderrFloodingDuringTheStopDoesNotLoseTheReplyOrHang() throws {
        let dir = try tempDir(self)
        let ready = dir.appendingPathComponent("ready")
        let (vani, _) = try atkScript(self, "trap 'head -c 4000000 /dev/zero | tr \"\\0\" x >&2; printf %s '\"'\"'\(paid)'\"'\"'; exit 1' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
        let (out, seconds) = try run(vani, ready)
        XCTAssertTrue(out.cancelled)
        XCTAssertLessThan(seconds, 5)
        XCTAssertEqual(String(decoding: out.stdout, as: UTF8.self), paid)
    }

    func testStdoutFloodingDuringTheStopIsBoundedAndTruncated() throws {
        let dir = try tempDir(self)
        let ready = dir.appendingPathComponent("ready")
        let (vani, _) = try atkScript(self, "trap 'head -c 20000000 /dev/zero | tr \"\\0\" x; exit 1' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
        let (out, seconds) = try run(vani, max: 1024, ready)
        XCTAssertTrue(out.cancelled)
        XCTAssertLessThan(seconds, 8)
        XCTAssertLessThanOrEqual(out.stdout.count, 1024)
    }

    func testCancelDuringAHugeStdinFeedToAChildThatNeverReadsIsBounded() throws {
        let dir = try tempDir(self)
        let ready = dir.appendingPathComponent("ready")
        let (vani, _) = try atkScript(self, "echo up > '\(ready.path)'\nexec sleep 60")
        let (out, seconds) = try run(vani, input: Data(repeating: 0x61, count: 16 << 20), ready)
        XCTAssertTrue(out.cancelled)
        XCTAssertTrue(out.stdout.isEmpty)
        XCTAssertLessThan(seconds, 3)
    }

    func testCancelRacingTheChildsOwnExitNeverReturnsAPartialReply() throws {
        for offsetMs in [0, 10, 30, 50, 80, 120] {
            let dir = try tempDir(self)
            let (vani, _) = try atkScript(self, "sleep 0.05\nprintf '%s' '\(paid)'\nexit 1")
            let cancel = CancelFlag()
            Thread.detachNewThread {
                Thread.sleep(forTimeInterval: Double(offsetMs) / 1000)
                cancel.set()
            }
            _ = dir
            let out = VaniProcess.run(vani, arguments: [], environment: [:], timeout: 30, maxOutputBytes: 64 << 10, cancellation: cancel)
            let text = String(decoding: out.stdout, as: UTF8.self)
            XCTAssertTrue(text.isEmpty || text == paid, "offset \(offsetMs) ms: partial reply \(text.debugDescription)")
            if !out.cancelled { XCTAssertEqual(out.status, 1) }
        }
    }

    func testRepeatedAndConcurrentCancelsAreHarmless() throws {
        let dir = try tempDir(self)
        let ready = dir.appendingPathComponent("ready")
        let (vani, _) = try atkScript(self, "trap 'printf %s '\"'\"'\(paid)'\"'\"'; exit 1' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
        let c = client(vani)
        let (out, _) = try atkStop(ready) { cancel in
            for _ in 0..<4 {
                Thread.detachNewThread {
                    _ = atkWaitFile(ready)
                    cancel.set()
                }
            }
            return c.run("k", cancellation: cancel)
        }
        XCTAssertEqual(out.fail?.code ?? "cancelled", "cancelled")
    }

    /// call() and the other plain callers keep throwing VaniCallCancelled whatever the child wrote.
    func testPlainCallsStillThrowCancelledWhateverTheChildWrote() throws {
        let dir = try tempDir(self)
        let ready = dir.appendingPathComponent("ready")
        let (vani, _) = try atkScript(self, "trap 'printf %s '\"'\"'{\"ok\":true}'\"'\"'; exit 0' TERM\necho up > '\(ready.path)'\nwhile true; do sleep 0.1; done")
        struct R: Decodable { var ok: Bool }
        let (result, _) = try atkStop(ready) { cancel -> String in
            do {
                let r: R = try VaniProcess.call(vani, [], configPath: nil, stateDir: nil, timeout: 30, maxOutputBytes: 1024, tooOld: "old", cancellation: cancel)
                return "value \(r.ok)"
            } catch is VaniCallCancelled { return "cancelled" } catch { return "other \(error)" }
        }
        XCTAssertEqual(result, "cancelled")
    }
}
