import Darwin
import Foundation
import XCTest
@testable import VaniApplyKit

/// Adversarial children for VaniProcess.run: pipe floods, orphaned pipe holders, hostile
/// timeouts, unstartable binaries, input nobody reads.
final class VaniProcessAdversarialTests: XCTestCase {
    private let sh = URL(fileURLWithPath: "/bin/sh")
    private var env: [String: String] { VaniProcess.environment(configPath: nil, stateDir: nil) }

    private func run(_ args: [String], binary: URL? = nil, timeout: TimeInterval, input: Data? = nil,
                     max: Int = 1 << 20, within limit: TimeInterval = 15) -> (VaniProcess.Outcome, TimeInterval)? {
        let box = AdvBox()
        let finished = expectation(description: "run returned")
        let binary = binary ?? sh, env = self.env
        let started = Date()
        DispatchQueue.global().async {
            box.value = VaniProcess.run(binary, arguments: args, environment: env, input: input, timeout: timeout, maxOutputBytes: max)
            finished.fulfill()
        }
        guard XCTWaiter().wait(for: [finished], timeout: limit) == .completed else {
            XCTFail("VaniProcess.run did not return within \(limit) s")
            return nil
        }
        return box.value.map { ($0, Date().timeIntervalSince(started)) }
    }

    private func threadCount() -> Int {
        var info = proc_taskinfo()
        let size = Int32(MemoryLayout<proc_taskinfo>.size)
        guard proc_pidinfo(getpid(), PROC_PIDTASKINFO, 0, &info, size) == size else { return -1 }
        return Int(info.pti_threadnum)
    }

    private func settle(_ before: Int, slack: Int = 2) {
        let deadline = Date().addingTimeInterval(2)
        while threadCount() - before > slack, Date() < deadline { usleep(50_000) }
    }

    /// Both pipes flooded at once, far past the 64 KiB pipe buffer: no deadlock, nothing lost
    /// on stdout, stderr kept to its 64 KiB.
    func testBothStreamsFloodedAtOnce() throws {
        let (out, elapsed) = try XCTUnwrap(run(["-c", "(head -c 400000 /dev/zero | tr '\\0' x) & (head -c 400000 /dev/zero | tr '\\0' y >&2) & wait"], timeout: 10))
        XCTAssertEqual(out.status, 0)
        XCTAssertFalse(out.timedOut)
        XCTAssertEqual(out.stdout.count, 400_000)
        XCTAssertEqual(out.stderr.count, 64 * 1024)
        XCTAssertLessThan(elapsed, 5)
    }

    /// Exits 0 at once with 5 MB: the ceiling holds, the flag says cut short, and the rest is drained.
    func testInstantExitWithHugeOutputIsTruncatedNotStuck() throws {
        let (out, elapsed) = try XCTUnwrap(run(["-c", "head -c 5000000 /dev/zero"], timeout: 10, max: 1 << 20))
        XCTAssertEqual(out.status, 0)
        XCTAssertEqual(out.stdout.count, 1 << 20)
        XCTAssertTrue(out.truncated)
        XCTAssertLessThan(elapsed, 5)
    }

    /// A daemonized grandchild keeps both pipes open and the child exits 0: the call still
    /// returns within timeout + grace and the pipe threads end.
    func testOrphanedGrandchildHoldingThePipesDoesNotHangTheCall() throws {
        _ = run(["-c", "exit 0"], timeout: 5)
        let before = threadCount()
        let (out, elapsed) = try XCTUnwrap(run(["-c", "sleep 25 & exit 0"], timeout: 1))
        XCTAssertLessThan(elapsed, 1 + 2 * VaniProcess.terminationGrace + 1)
        XCTAssertTrue(out.timedOut)
        settle(before)
        XCTAssertLessThanOrEqual(threadCount() - before, 2)
    }

    func testTimeoutZeroAndNegativeReturnPromptly() throws {
        for timeout in [0, -1, -1e9] as [TimeInterval] {
            let (out, elapsed) = try XCTUnwrap(run(["-c", "sleep 5"], timeout: timeout))
            XCTAssertTrue(out.timedOut, "timeout \(timeout)")
            XCTAssertLessThan(elapsed, 2 * VaniProcess.terminationGrace + 1, "timeout \(timeout)")
        }
    }

    /// Hostile timeout values must not trap the process.
    func testInfiniteTimeoutOnAFastChildDoesNotTrap() throws {
        let (out, _) = try XCTUnwrap(run(["-c", "printf ok"], timeout: .infinity))
        XCTAssertEqual(out.status, 0)
        XCTAssertEqual(String(data: out.stdout, encoding: .utf8), "ok")
    }

    /// Nothing startable is an error at once, never a hang.
    func testUnstartableBinariesAreErrorsNotHangs() throws {
        let dir = try tempDir(self)
        let missing = dir.appendingPathComponent("nope")
        let plain = dir.appendingPathComponent("plain"); try Data("x".utf8).write(to: plain)
        let badShebang = dir.appendingPathComponent("bad")
        try Data("#!/nonexistent/interpreter\n".utf8).write(to: badShebang)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: badShebang.path)
        let notABinary = dir.appendingPathComponent("garbage")
        try Data([0x7f, 0x01, 0x02, 0x03, 0x04]).write(to: notABinary)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: notABinary.path)
        for target in [missing, plain, dir, badShebang, notABinary] {
            let (out, elapsed) = try XCTUnwrap(run([], binary: target, timeout: 5, within: 8))
            XCTAssertNotNil(out.launchError, "\(target.lastPathComponent): \(out)")
            XCTAssertNil(out.status, target.lastPathComponent)
            XCTAssertFalse(out.timedOut, target.lastPathComponent)
            XCTAssertLessThan(elapsed, 2, target.lastPathComponent)
        }
    }

    /// 10 MB of stdin to a child that never reads: returns at the timeout, writer thread ends.
    func testUnreadInputDoesNotStrandTheWriter() throws {
        _ = run(["-c", "exit 0"], timeout: 5)
        let before = threadCount()
        let (out, elapsed) = try XCTUnwrap(run(["-c", "sleep 30"], timeout: 0.5, input: Data(count: 10 << 20)))
        XCTAssertTrue(out.timedOut)
        XCTAssertLessThan(elapsed, 0.5 + 2 * VaniProcess.terminationGrace + 1)
        settle(before)
        XCTAssertLessThanOrEqual(threadCount() - before, 2)
    }

    /// A child that exits at once without reading 10 MB of stdin: EPIPE, not SIGPIPE; status 0.
    func testExitWithoutReadingInputIsNotAFatalSigpipe() throws {
        let (out, _) = try XCTUnwrap(run(["-c", "exit 0"], timeout: 5, input: Data(count: 10 << 20)))
        XCTAssertEqual(out.status, 0)
    }

    /// The SIGKILL guard window: children that exit right at the timeout (and right at the end
    /// of the SIGTERM grace) never make the call trap, hang or overrun, and a bystander process
    /// is never signalled. The window between Foundation reaping a pid and reporting
    /// !isRunning is not directly observable from here; this stresses its neighbourhood.
    func testChildrenExitingAtTheTimeoutEdgeNeverOverrunOrHitABystander() throws {
        let bystander = Process()
        bystander.executableURL = URL(fileURLWithPath: "/bin/sleep")
        bystander.arguments = ["120"]
        try bystander.run()
        addTeardownBlock { if bystander.isRunning { bystander.terminate() } }
        var worst: TimeInterval = 0
        for i in 0..<150 {
            if i % 2 == 1 && i % 10 != 1 { continue } // a TERM-ignoring child costs the whole grace
            let sleepFor = 0.03 + Double(i % 10) * 0.004
            let script = i % 2 == 0 ? "sleep \(sleepFor)" : "trap '' TERM; sleep \(sleepFor + VaniProcess.terminationGrace)"
            let (out, elapsed) = try XCTUnwrap(run(["-c", script], timeout: 0.05 + Double(i % 7) * 0.005, within: 6))
            worst = max(worst, elapsed)
            XCTAssertTrue(out.timedOut || out.status != nil)
        }
        XCTAssertLessThan(worst, 0.1 + 2 * VaniProcess.terminationGrace + 1)
        XCTAssertTrue(bystander.isRunning, "an unrelated process was signalled")
    }

    /// stop() signals the child only; whatever it forked survives the call.
    func testTimedOutChildDoesNotLeaveAGrandchildRunning() throws {
        let pidFile = try tempDir(self).appendingPathComponent("gc").path
        let (out, _) = try XCTUnwrap(run(["-c", "sleep 40 & echo $! > \(pidFile); wait"], timeout: 0.5))
        XCTAssertTrue(out.timedOut)
        let pid = try XCTUnwrap(Int32(String(contentsOfFile: pidFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)))
        usleep(300_000)
        let alive = kill(pid, 0) == 0
        if alive { kill(pid, SIGKILL) }
        XCTAssertFalse(alive, "grandchild \(pid) outlived the timed-out call")
    }
}

private final class AdvBox: @unchecked Sendable {
    var value: VaniProcess.Outcome?
}
