import Darwin
import Foundation
import XCTest
@testable import VaniApplyKit

/// The bounds on a `vani` child: it is gone (SIGKILL if it ignores SIGTERM) shortly after its
/// timeout, whatever it does with its pipes, and no I/O thread outlives the call.
final class VaniProcessBoundsTests: XCTestCase {
    private let sh = URL(fileURLWithPath: "/bin/sh")
    private var env: [String: String] { VaniProcess.environment(configPath: nil, stateDir: nil) }

    /// Same watchdog as VaniProcessTests: a hang fails the test instead of the suite.
    private func run(_ script: String, timeout: TimeInterval, within limit: TimeInterval = 10) -> (VaniProcess.Outcome, TimeInterval)? {
        let box = BoundsBox()
        let finished = expectation(description: "run returned")
        let sh = self.sh, env = self.env
        let started = Date()
        DispatchQueue.global().async {
            box.value = VaniProcess.run(sh, arguments: ["-c", script], environment: env, timeout: timeout, maxOutputBytes: 1 << 20)
            finished.fulfill()
        }
        guard XCTWaiter().wait(for: [finished], timeout: limit) == .completed else {
            XCTFail("VaniProcess.run did not return within \(limit) s")
            return nil
        }
        return box.value.map { ($0, Date().timeIntervalSince(started)) }
    }

    private func zeros(_ count: Int, max: Int) -> VaniProcess.Outcome {
        VaniProcess.run(sh, arguments: ["-c", "head -c \(count) /dev/zero"], environment: env, timeout: 10, maxOutputBytes: max)
    }

    func testReplyOfExactlyTheCeilingIsNotTruncated() {
        let out = zeros(1000, max: 1000)
        XCTAssertEqual(out.stdout.count, 1000)
        XCTAssertFalse(out.truncated)
    }

    func testReplyOneByteOverTheCeilingIsTruncatedAndCapped() {
        let out = zeros(1001, max: 1000)
        XCTAssertEqual(out.stdout.count, 1000)
        XCTAssertTrue(out.truncated)
    }

    func testReplyOneByteUnderTheCeilingIsNotTruncated() {
        let out = zeros(999, max: 1000)
        XCTAssertEqual(out.stdout.count, 999)
        XCTAssertFalse(out.truncated)
    }

    /// The pid the script wrote is no longer a live process (it was killed and reaped).
    private func assertGone(_ pidFile: String, file: StaticString = #filePath, line: UInt = #line) throws {
        let pid = try XCTUnwrap(Int32(String(contentsOfFile: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)), file: file, line: line)
        XCTAssertTrue(kill(pid, 0) != 0 && errno == ESRCH, "pid \(pid) is still running", file: file, line: line)
        if kill(pid, 0) == 0 { kill(pid, SIGKILL) }
    }

    private func threadCount() -> Int {
        var info = proc_taskinfo()
        let size = Int32(MemoryLayout<proc_taskinfo>.size)
        guard proc_pidinfo(getpid(), PROC_PIDTASKINFO, 0, &info, size) == size else { return -1 }
        return Int(info.pti_threadnum)
    }

    /// A child that closes stdout and stderr, so both pipes reach EOF at once, and then
    /// keeps running used to leave waitUntilExit blocked for its whole life.
    func testChildThatClosesItsPipesThenSleepsTimesOut() throws {
        let (out, elapsed) = try XCTUnwrap(run("exec 1>&- 2>&-; exec sleep 30", timeout: 0.5))
        XCTAssertTrue(out.timedOut)
        XCTAssertNil(out.status)
        XCTAssertLessThan(elapsed, 0.5 + 2 * 1 + 1, "returned within the timeout plus the grace periods")
    }

    /// SIGTERM alone does not stop a child that ignores it: it is killed, not left running.
    func testChildIgnoringSigtermIsKilled() throws {
        let pidFile = try tempDir(self).appendingPathComponent("pid").path
        let (out, elapsed) = try XCTUnwrap(run("echo $$ > \(pidFile); trap '' TERM; exec sleep 30", timeout: 0.5))
        XCTAssertTrue(out.timedOut)
        XCTAssertLessThan(elapsed, 0.5 + 2 * 1 + 1)
        try assertGone(pidFile)
    }

    /// The same for a shell spinning in its own loop, which holds its pipes open.
    func testBusyChildIgnoringSigtermIsKilled() throws {
        let pidFile = try tempDir(self).appendingPathComponent("pid").path
        let (out, elapsed) = try XCTUnwrap(run("echo $$ > \(pidFile); trap '' TERM; while :; do :; done", timeout: 0.5))
        XCTAssertTrue(out.timedOut)
        XCTAssertLessThan(elapsed, 0.5 + 2 * 1 + 1)
        try assertGone(pidFile)
    }

    /// A grandchild that inherits the pipes keeps them open after the child is gone; the
    /// reader threads must still end with the call.
    func testNoReaderThreadOutlivesATimedOutCall() throws {
        _ = run("exit 0", timeout: 5) // warm the thread machinery
        let before = threadCount()
        for _ in 0..<3 {
            let (out, _) = try XCTUnwrap(run("trap '' TERM; sleep 12 & sleep 12", timeout: 0.3))
            XCTAssertTrue(out.timedOut)
        }
        let deadline = Date().addingTimeInterval(2)
        while threadCount() - before > 2, Date() < deadline { usleep(50_000) }
        XCTAssertLessThanOrEqual(threadCount() - before, 2, "I/O threads of the timed-out calls are still alive")
    }

    /// A call that finishes normally leaves no thread behind either.
    func testNoReaderThreadOutlivesANormalCall() throws {
        _ = run("exit 0", timeout: 5)
        let before = threadCount()
        for _ in 0..<5 { XCTAssertEqual(try XCTUnwrap(run("printf ok", timeout: 5)).0.status, 0) }
        let deadline = Date().addingTimeInterval(2)
        while threadCount() - before > 2, Date() < deadline { usleep(50_000) }
        XCTAssertLessThanOrEqual(threadCount() - before, 2)
    }
}

private final class BoundsBox: @unchecked Sendable {
    var value: VaniProcess.Outcome?
}
