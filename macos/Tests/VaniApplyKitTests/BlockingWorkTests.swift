import Foundation
import XCTest
@testable import VaniApplyKit

/// BlockingWork keeps blocking `vani` calls off Swift's cooperative pool, so unrelated tasks
/// keep running while many of them wait on children.
final class BlockingWorkTests: XCTestCase {
    private var env: [String: String] { VaniProcess.environment(configPath: nil, stateDir: nil) }

    /// More simultaneous two-second child calls than there are cores: an unrelated `Task {}`
    /// must still start at once. With `Task.detached` around the same calls the pool is full and
    /// it waits for one of them to finish.
    func testUnrelatedTaskRunsWhileMoreCallsThanCoresBlock() async throws {
        let sh = URL(fileURLWithPath: "/bin/sh"), env = self.env
        let count = ProcessInfo.processInfo.activeProcessorCount + 4
        let calls = (0..<count).map { _ in
            Task {
                await BlockingWork.run {
                    VaniProcess.run(sh, arguments: ["-c", "sleep 2"], environment: env, timeout: 20, maxOutputBytes: 1024)
                }
            }
        }
        try await Task.sleep(nanoseconds: 500_000_000) // every call is inside its child by now
        let started = Date()
        await Task {}.value
        let latency = Date().timeIntervalSince(started)
        XCTAssertLessThan(latency, 0.1, "an unrelated Task waited \(latency) s behind \(count) blocking calls")
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
