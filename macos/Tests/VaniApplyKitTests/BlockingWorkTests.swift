import Foundation
import XCTest
@testable import VaniApplyKit

/// BlockingWork keeps blocking `vani` calls off Swift's cooperative pool, so unrelated tasks
/// keep running while many of them wait on children.
final class BlockingWorkTests: XCTestCase {
    private var env: [String: String] { VaniProcess.environment(configPath: nil, stateDir: nil) }

    /// The pre-fix pattern, kept only as the oracle for the regression: a `Task.detached` that
    /// blocks its cooperative-pool thread for the whole call. Not used by the app.
    private static func oldPattern(_ work: @escaping @Sendable () -> Void) -> Task<Void, Never> {
        Task.detached { work() }
    }

    /// Starts `count` blocking calls of `seconds` each through `launch`, then measures how long a
    /// probe takes to start. The probe is `Task.detached`, so it needs a cooperative-pool thread
    /// (a plain `Task {}` from a test inherits an actor and would not). Returns the probe latency.
    private func probeLatency(count: Int, seconds: UInt32, launch: (@escaping @Sendable () -> Void) -> Task<Void, Never>) async throws -> TimeInterval {
        let calls = (0..<count).map { _ in launch { usleep(seconds * 1_000_000) } }
        try await Task.sleep(nanoseconds: 500_000_000) // every call has started by now
        let started = Date()
        await Task.detached {}.value
        let latency = Date().timeIntervalSince(started)
        for call in calls { await call.value }
        return latency
    }

    /// B2-10. The same saturating load through the old pattern (the "before" arm) and through
    /// BlockingWork. The cooperative pool is about one thread per core, so 3x the core count of
    /// blocking calls fills it. The old arm's probe must wait for a call to end; the new arm's must
    /// not. The old arm asserting a long latency is what proves this test can fail.
    func testUnrelatedTaskRunsWhileMoreCallsThanCoresBlock() async throws {
        let count = ProcessInfo.processInfo.activeProcessorCount * 3
        let old = try await probeLatency(count: count, seconds: 2, launch: Self.oldPattern)
        let new = try await probeLatency(count: count, seconds: 2) { work in
            Task { await BlockingWork.value(work) }
        }
        print("B2-10 \(count) calls x 2 s: old-pattern probe \(old) s, BlockingWork probe \(new) s")
        XCTAssertGreaterThan(old, 0.5, "the old pattern no longer starves the pool, so this test cannot fail")
        XCTAssertLessThan(new, 0.1, "an unrelated Task waited \(new) s behind \(count) blocking calls")
    }

    /// Real child processes through BlockingWork still finish cleanly.
    func testChildCallsThroughBlockingWorkComplete() async {
        let sh = URL(fileURLWithPath: "/bin/sh"), env = self.env
        let calls = (0..<4).map { _ in
            Task { await BlockingWork.run { VaniProcess.run(sh, arguments: ["-c", "sleep 0.2"], environment: env, timeout: 20, maxOutputBytes: 1024) } }
        }
        for call in calls {
            guard case let .success(outcome) = await call.value else { return XCTFail("a call threw") }
            XCTAssertEqual(outcome.status, 0)
            XCTAssertFalse(outcome.timedOut)
        }
    }

    private struct Boom: Error, Equatable {}

    /// A throw comes back as a failure, once, and the value as a success.
    func testResultCarriesTheValueOrTheError() async {
        let ok = await BlockingWork.run { 7 }
        XCTAssertEqual(try? ok.get(), 7)
        let failed = await BlockingWork.run { () -> Int in throw Boom() }
        guard case let .failure(error) = failed else { return XCTFail("expected a failure") }
        XCTAssertEqual(error as? Boom, Boom())
        let plain = await BlockingWork.value { "v" }
        XCTAssertEqual(plain, "v")
    }

    /// Cancelling the awaiting task does not strand the continuation: the await still ends,
    /// with the work's result, exactly once.
    func testCancellationStillResumesOnce() async {
        let task = Task { await BlockingWork.run { usleep(200_000); return 1 } }
        task.cancel()
        let result = await task.value
        XCTAssertEqual(try? result.get(), 1)
    }

    /// Many calls at once all finish; none is lost or resumed twice.
    func testManyConcurrentCallsAllReturn() async {
        let sum = await withTaskGroup(of: Int.self) { group in
            for i in 0..<200 { group.addTask { (try? await BlockingWork.run { i }.get()) ?? -1 } }
            return await group.reduce(0, +)
        }
        XCTAssertEqual(sum, (0..<200).reduce(0, +))
    }
}
