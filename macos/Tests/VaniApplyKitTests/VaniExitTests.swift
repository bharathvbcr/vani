import Foundation
import XCTest
@testable import VaniApplyKit

/// The one mapping from a vani exit with no usable reply to what the app says. Exit 2 is vani's
/// usage code and an unrecovered Go panic also exits 2, so "this vani is too old" is claimed only
/// for exit 2 with nothing that says it crashed; vani's own crash code is shown as a crash.
final class VaniExitTests: XCTestCase {
    /// A stand-in vani that prints `out` on stdout and `err` on stderr, then exits `status`.
    private func fakeVani(out: String = "", err: String = "", status: Int32) throws -> URL {
        let dir = try tempDir(self)
        try Data(out.utf8).write(to: dir.appendingPathComponent("out"))
        try Data(err.utf8).write(to: dir.appendingPathComponent("err"))
        let url = dir.appendingPathComponent("vani")
        try "#!/bin/sh\ncat '\(dir.path)/out'\ncat '\(dir.path)/err' >&2\nexit \(status)\n".write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    private struct Reply: Decodable { var ok: Bool }

    private func failure(_ vani: URL, raw: Bool = false) -> BankError? {
        do {
            if raw {
                _ = try VaniProcess.callRaw(vani, ["x"], configPath: nil, stateDir: nil, timeout: 10, maxOutputBytes: 1 << 20, tooOld: "too old; rebuild the app")
            } else {
                let _: Reply = try VaniProcess.call(vani, ["x"], configPath: nil, stateDir: nil, timeout: 10, maxOutputBytes: 1 << 20,
                                                    tooOld: "too old; rebuild the app")
            }
            return nil
        } catch let e as BankError { return e } catch { return BankError("other: \(error)") }
    }

    func testTheCrashCodeIsNotTheUsageCode() {
        XCTAssertEqual(VaniExit.usage, 2)
        XCTAssertEqual(VaniExit.crash, 70)
    }

    func testCrashExitIsShownAsACrashWithItsLastLine() throws {
        for raw in [false, true] {
            let vani = try fakeVani(err: "vani: panic: boom\ngoroutine 1 [running]:\nvani: crashed: boom\n", status: 70)
            let e = try XCTUnwrap(failure(vani, raw: raw))
            XCTAssertEqual(e.description, "vani x crashed: boom")
            XCTAssertFalse(e.description.contains("too old"), e.description)
        }
    }

    func testUsageExitWithNothingElseIsTooOld() throws {
        for raw in [false, true] {
            let e = try XCTUnwrap(failure(try fakeVani(err: "vani: unknown command \"x\"\n", status: 2), raw: raw))
            XCTAssertEqual(e.description, "vani x exited 2 without a reply (too old; rebuild the app)")
            let empty = try XCTUnwrap(failure(try fakeVani(status: 2), raw: raw))
            XCTAssertTrue(empty.description.contains("too old"), empty.description)
        }
    }

    func testAnOlderVaniWhosePanicExitedUsageIsStillACrash() throws {
        let vani = try fakeVani(err: "panic: runtime error: index out of range [3] with length 2\n\ngoroutine 1 [running]:\nmain.f()\n", status: 2)
        let e = try XCTUnwrap(failure(vani))
        XCTAssertEqual(e.description, "vani x crashed: runtime error: index out of range [3] with length 2")
        let fatal = try XCTUnwrap(failure(try fakeVani(err: "fatal error: all goroutines are asleep - deadlock!\n\ngoroutine 1 [chan receive]:\n", status: 2)))
        XCTAssertEqual(fatal.description, "vani x crashed: fatal error: all goroutines are asleep - deadlock!")
    }

    /// ft-152c: a relative VANI_CONFIG is refused with exit 2 and a coded reply on stdout (the bytes
    /// `vani status --json` wrote); the app shows that message and its code, never "too old".
    func testARelativeVaniConfigReplyIsShownNotTooOld() throws {
        let recorded = #"{"error":"config: relative path in environment: VANI_CONFIG=\"rel/config.json\" is relative; set an absolute path","code":"relative_path"}"#
        for raw in [false, true] {
            let e = try XCTUnwrap(failure(try fakeVani(out: recorded + "\n", err: "vani: config: relative path\n", status: 2), raw: raw))
            XCTAssertTrue(e.description.contains("VANI_CONFIG"), e.description)
            XCTAssertFalse(e.description.contains("too old"), e.description)
            XCTAssertEqual(e.code, "relative_path")
        }
        let state = #"{"error":"config: relative path in environment: VANI_STATE_DIR=\"s\" is relative; set an absolute path","code":"relative_path"}"#
        let e = try XCTUnwrap(failure(try fakeVani(out: state, status: 2)))
        XCTAssertTrue(e.description.contains("VANI_STATE_DIR"), e.description)
        XCTAssertEqual(e.code, "relative_path")
    }

    func testOtherExitsAreNamedWithoutAReading() throws {
        let e = try XCTUnwrap(failure(try fakeVani(err: "boom\n", status: 1)))
        XCTAssertEqual(e.description, "vani x exited 1 without a reply")
    }

    func testReadingIsBoundedAndIgnoresControlCharacters() {
        let long = String(repeating: "a", count: 5000)
        guard case let .crashed(line) = VaniExit.read(status: 70, stderr: Data("vani: crashed: \(long)\n".utf8)) else { return XCTFail("not a crash") }
        XCTAssertLessThanOrEqual(line.count, VaniExit.maxCrashLine + 3)
        guard case let .crashed(clean) = VaniExit.read(status: 70, stderr: Data("vani: crashed: a\u{1B}[31mb\n".utf8)) else { return XCTFail("not a crash") }
        XCTAssertFalse(clean.unicodeScalars.contains { $0.value == 0x1B }, clean)
        XCTAssertEqual(VaniExit.read(status: 70, stderr: Data()), .crashed(line: "no reason given"))
        guard case .crashed = VaniExit.read(status: 70, stderr: Data([0xFF, 0xFE, 0x0A])) else { return XCTFail("invalid UTF-8 hid a crash") }
        XCTAssertEqual(VaniExit.read(status: nil, stderr: Data("panic: x".utf8)), .other)
        XCTAssertEqual(VaniExit.read(status: 0, stderr: Data()), .other)
        XCTAssertEqual(VaniExit.read(status: 2, stderr: Data("a panic: in the middle of a line".utf8)), .tooOld)
    }

    func testTheFailureCodeNamesACrash() {
        var out = VaniProcess.Outcome(status: 70, stdout: Data(), timedOut: false)
        out.stderr = Data("vani: crashed: x\n".utf8)
        XCTAssertEqual(VaniExit.code(out), "crash", "the same spelling as vani's own {\"code\":\"crash\"} envelope")
        out.status = 2
        out.stderr = Data()
        XCTAssertEqual(VaniExit.code(out), "no_reply")
    }
}
