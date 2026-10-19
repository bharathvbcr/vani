import Foundation
import XCTest
@testable import VaniApplyKit

final class VaniProcessTests: XCTestCase {
    private let sh = URL(fileURLWithPath: "/bin/sh")
    private var env: [String: String] { VaniProcess.environment(configPath: nil, stateDir: nil) }

    /// Runs `VaniProcess.run` off the test thread so a deadlock fails the test
    /// instead of hanging the suite.
    private func run(_ script: String, input: Data? = nil, timeout: TimeInterval = 20,
                     maxOutputBytes: Int = 1 << 20, within limit: TimeInterval = 10) -> VaniProcess.Outcome? {
        let box = OutcomeBox()
        let finished = expectation(description: "run returned")
        let sh = self.sh, env = self.env
        DispatchQueue.global().async {
            box.value = VaniProcess.run(sh, arguments: ["-c", script], environment: env, input: input,
                                        timeout: timeout, maxOutputBytes: maxOutputBytes)
            finished.fulfill()
        }
        guard XCTWaiter().wait(for: [finished], timeout: limit) == .completed else {
            XCTFail("VaniProcess.run did not return within \(limit) s")
            return nil
        }
        return box.value
    }

    /// A child that fills its stderr pipe before writing stdout must not stall:
    /// stderr is drained while stdout is read.
    func testLargeStderrBeforeStdoutDoesNotDeadlock() throws {
        let started = Date()
        let out = try XCTUnwrap(run("head -c 200000 /dev/zero | tr '\\0' e >&2; printf ok"))
        XCTAssertFalse(out.timedOut)
        XCTAssertLessThan(Date().timeIntervalSince(started), 10)
        XCTAssertEqual(out.status, 0)
        XCTAssertEqual(String(decoding: out.stdout, as: UTF8.self), "ok")
        XCTAssertFalse(out.truncated)
        XCTAssertEqual(out.stderr.count, 64 * 1024, "stderr keeps only its first 64 KiB")
        XCTAssertEqual(out.stderr.first, UInt8(ascii: "e"))
    }

    /// A child that writes a lot of stdout before it reads its stdin must not
    /// stall the stdin write, which used to run before any reading started.
    func testLargeInputWithLargeOutputBeforeReadingDoesNotDeadlock() throws {
        let input = Data(repeating: UInt8(ascii: "i"), count: 300_000)
        let out = try XCTUnwrap(run("head -c 200000 /dev/zero | tr '\\0' o; wc -c | tr -d ' ' >&2", input: input))
        XCTAssertFalse(out.timedOut)
        XCTAssertEqual(out.status, 0)
        XCTAssertEqual(out.stdout.count, 200_000)
        XCTAssertEqual(String(decoding: out.stderr, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines),
                       "300000", "the child received all of its input")
    }

    /// A child that exits without reading its input must not take this process
    /// down with SIGPIPE; the run reports the child's own exit.
    func testChildIgnoringInputDoesNotKillParent() throws {
        let input = Data(repeating: UInt8(ascii: "i"), count: 1 << 20)
        let out = try XCTUnwrap(run("exit 7", input: input))
        XCTAssertFalse(out.timedOut)
        XCTAssertEqual(out.status, 7)
    }

    /// Both ceilings hold together: stdout is cut at maxOutputBytes and marked
    /// truncated, stderr at 64 KiB, and the child still runs to completion.
    func testBothStreamsBounded() throws {
        let out = try XCTUnwrap(run("head -c 150000 /dev/zero | tr '\\0' e >&2; head -c 150000 /dev/zero | tr '\\0' o; exit 3",
                                    maxOutputBytes: 1000))
        XCTAssertFalse(out.timedOut)
        XCTAssertEqual(out.status, 3)
        XCTAssertEqual(out.stdout.count, 1000)
        XCTAssertTrue(out.truncated)
        XCTAssertEqual(out.stderr.count, 64 * 1024)
    }

    /// The timeout still terminates a child that never finishes.
    func testTimeoutTerminatesChild() throws {
        let started = Date()
        let out = try XCTUnwrap(run("printf partial; sleep 30", timeout: 0.5))
        XCTAssertTrue(out.timedOut)
        XCTAssertNil(out.status)
        XCTAssertTrue(out.stdout.isEmpty)
        XCTAssertLessThan(Date().timeIntervalSince(started), 5)
    }

    func testEnvironmentPathIncludesHomebrew() {
        let path = (VaniProcess.environment(configPath: nil, stateDir: nil)["PATH"] ?? "").split(separator: ":")
        XCTAssertTrue(path.contains("/opt/homebrew/bin"))
        XCTAssertTrue(path.contains("/usr/local/bin"))
        XCTAssertTrue(path.contains("/usr/bin"))
    }
}

private final class OutcomeBox: @unchecked Sendable {
    var value: VaniProcess.Outcome?
}
