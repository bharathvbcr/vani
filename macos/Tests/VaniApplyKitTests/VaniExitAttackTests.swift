import Foundation
import XCTest
@testable import VaniApplyKit

/// Attacks on VaniExit: what stderr and exit status a hostile or noisy vani can produce.
final class VaniExitAttackTests: XCTestCase {
    private func fakeVani(out: String = "", err: Data = Data(), status: Int32) throws -> URL {
        let dir = try tempDir(self)
        try Data(out.utf8).write(to: dir.appendingPathComponent("out"))
        try err.write(to: dir.appendingPathComponent("err"))
        let url = dir.appendingPathComponent("vani")
        try "#!/bin/sh\ncat '\(dir.path)/out'\ncat '\(dir.path)/err' >&2\nexit \(status)\n".write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    private struct Reply: Decodable { var ok: Bool }

    private func failure(_ vani: URL, raw: Bool = false) -> BankError? {
        do {
            if raw {
                _ = try VaniProcess.callRaw(vani, ["x"], configPath: nil, stateDir: nil, timeout: 10, maxOutputBytes: 1 << 20, tooOld: "too old")
            } else {
                let _: Reply = try VaniProcess.call(vani, ["x"], configPath: nil, stateDir: nil, timeout: 10, maxOutputBytes: 1 << 20, tooOld: "too old")
            }
            return nil
        } catch let e as BankError { return e } catch { return BankError("other: \(error)") }
    }

    /// A complete valid reply followed by a crash is a failure, not a success.
    func testACompleteReplyThenCrashIsAFailure() throws {
        for raw in [false, true] {
            let e = failure(try fakeVani(out: "{\"ok\":true}\n", err: Data("vani: crashed: late\n".utf8), status: 70), raw: raw)
            XCTAssertNotNil(e, "exit 70 with a complete reply was accepted")
            XCTAssertTrue(e?.description.contains("crashed: late") ?? false, e?.description ?? "")
        }
    }

    func testAPartialReplyThenCrashIsACrash() throws {
        for raw in [false, true] {
            let e = try XCTUnwrap(failure(try fakeVani(out: "{\"ok\":tr", err: Data("vani: crashed: x\n".utf8), status: 70), raw: raw))
            XCTAssertTrue(e.description.contains("crashed: x"), e.description)
        }
    }

    func testCrashEnvelopeOnStdoutIsShownAsWritten() throws {
        let e = try XCTUnwrap(failure(try fakeVani(out: "{\"error\":\"crashed: boom\",\"code\":\"crash\"}\n", status: 70)))
        XCTAssertEqual(e.code, "crash")
        XCTAssertFalse(e.description.contains("too old"))
    }

    func testInvalidUTF8AndNULInAnOldVanisPanicStillCrash() throws {
        var d = Data("panic: ".utf8)
        d.append(contentsOf: [0xFF, 0xFE, 0x00, 0x1B])
        d.append(Data("[31m bad\ngoroutine 1 [running]:\n".utf8))
        let e = try XCTUnwrap(failure(try fakeVani(err: d, status: 2)))
        XCTAssertTrue(e.description.contains("crashed"), e.description)
        XCTAssertFalse(e.description.unicodeScalars.contains { $0.value == 0x1B || $0.value == 0 }, e.description)
    }

    func testGoPanicWithLeadingWhitespaceAndStackOnlyStillCrash() throws {
        let e = try XCTUnwrap(failure(try fakeVani(err: Data("   panic: indented\n\ngoroutine 1 [running]:\n".utf8), status: 2)))
        XCTAssertTrue(e.description.contains("crashed"), e.description)
        let stackOnly = try XCTUnwrap(failure(try fakeVani(err: Data("goroutine 1 [running]:\nmain.main()\n".utf8), status: 2)))
        XCTAssertTrue(stackOnly.description.contains("too old"), stackOnly.description) // documents the reading: no panic: line
    }

    func testAUsageExitFromANewVaniStaysTooOldWithHugeStderr() throws {
        let big = String(repeating: "usage line\n", count: 20_000)
        let e = try XCTUnwrap(failure(try fakeVani(err: Data(big.utf8), status: 2)))
        XCTAssertTrue(e.description.contains("too old"), e.description)
    }

    /// An old vani that logged more than the first 64 KB of stderr before it panicked: the
    /// panic is past the head of the capture, so it is read from the tail.
    func testPanicAfterMoreThan64KBOfStderrIsStillACrash() throws {
        let noise = String(repeating: "progress line\n", count: 8_000)
        let e = try XCTUnwrap(failure(try fakeVani(err: Data((noise + "panic: boom\n\ngoroutine 1 [running]:\n").utf8), status: 2)))
        XCTAssertTrue(e.description.contains("crashed: boom"), e.description)
    }

    /// Exit 70 after more than 64 KB of stderr: the crash line is read from the tail, so the
    /// reason shown is the crash line, not an arbitrary log line.
    func testCrashAfterMoreThan64KBShowsTheCrashLine() throws {
        let noise = String(repeating: "progress line\n", count: 8_000)
        let e = try XCTUnwrap(failure(try fakeVani(err: Data((noise + "vani: crashed: the real reason\n").utf8), status: 70)))
        XCTAssertEqual(e.description, "vani x crashed: the real reason")
    }

    /// A noisy but healthy new vani: exit 2 with a huge stderr and a panic-looking word in the
    /// head only is still "too old" (no goroutine report), and the text shown stays bounded.
    func testHugeStderrWithPanicWordInTheHeadIsStillBounded() throws {
        let big = "retry after panic: x\n" + String(repeating: "usage line\n", count: 20_000)
        let e = try XCTUnwrap(failure(try fakeVani(err: Data(big.utf8), status: 2)))
        XCTAssertTrue(e.description.contains("too old"), e.description)
        XCTAssertLessThan(e.description.count, 400)
    }

    /// A stderr write without a newline right before the Go runtime's report puts "panic:" mid-line.
    func testPanicAfterAnUnterminatedStderrWriteIsStillACrash() throws {
        let e = try XCTUnwrap(failure(try fakeVani(err: Data("fetching 12/40...panic: boom\n\ngoroutine 1 [running]:\n".utf8), status: 2)))
        XCTAssertEqual(e.description, "vani x crashed: boom")
        let fatal = try XCTUnwrap(failure(try fakeVani(err: Data("loading...fatal error: all goroutines are asleep - deadlock!\n\ngoroutine 1 [chan receive]:\n".utf8), status: 2)))
        XCTAssertEqual(fatal.description, "vani x crashed: fatal error: all goroutines are asleep - deadlock!")
    }

    /// The word "panic:" in an ordinary message, with no goroutine report after it, is not a crash.
    func testPanicWordWithoutAGoroutineReportIsNotACrash() throws {
        for err in ["retry after panic: x\n", "panic: x\n", "fatal error: x\n", "panic: x\ngoroutine x [running]:\n",
                    "panic: x\ngoroutine 1 running\n", "goroutine 1 [running]:\nthen later panic: x\n", ""] {
            let e = try XCTUnwrap(failure(try fakeVani(err: Data(err.utf8), status: 2)))
            XCTAssertTrue(e.description.contains("too old"), "\(err.debugDescription): \(e.description)")
        }
    }

    /// Several goroutines, a nested panic and a log line mentioning panic: the report that
    /// precedes the stack is the one shown.
    func testTheReportBeforeTheStackIsShown() throws {
        let err = "worker: panic: handled elsewhere\nstarting\npanic: first [recovered]\n\tpanic: second\n\ngoroutine 1 [running]:\nmain.f()\n\ngoroutine 7 [select]:\n"
        let e = try XCTUnwrap(failure(try fakeVani(err: Data(err.utf8), status: 2)))
        XCTAssertEqual(e.description, "vani x crashed: second")
    }

    /// Every way a crash is read gives the one failure code Go's envelope uses.
    func testEveryCrashReadingCarriesTheCodeCrash() throws {
        for (status, err) in [(Int32(70), "vani: crashed: x\n"), (Int32(70), ""), (Int32(2), "panic: x\n\ngoroutine 1 [running]:\n")] {
            var out = VaniProcess.Outcome(status: status, stdout: Data(), timedOut: false)
            out.stderr = Data(err.utf8)
            XCTAssertEqual(VaniExit.code(out), "crash")
                    }
        let envelope = try XCTUnwrap(failure(try fakeVani(out: "{\"error\":\"crashed: boom\",\"code\":\"crash\"}\n", status: 70)))
        XCTAssertEqual(envelope.code, "crash")
    }

    func testCrashLineWithUnicodeBidiAndEmojiIsBounded() {
        let s = String(repeating: "\u{202E}😀", count: 500)
        guard case let .crashed(line) = VaniExit.read(status: 70, stderr: Data("vani: crashed: \(s)\n".utf8)) else { return XCTFail() }
        XCTAssertLessThanOrEqual(line.count, VaniExit.maxCrashLine + 3)
    }
}
