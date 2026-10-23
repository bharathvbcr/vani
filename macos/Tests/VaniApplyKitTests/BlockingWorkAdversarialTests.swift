import Darwin
import Foundation
import XCTest
@testable import VaniApplyKit

private func threadCount() -> Int {
    var info = proc_taskinfo()
    let size = Int32(MemoryLayout<proc_taskinfo>.size)
    guard proc_pidinfo(getpid(), PROC_PIDTASKINFO, 0, &info, size) == size else { return -1 }
    return Int(info.pti_threadnum)
}

private final class Peak: @unchecked Sendable {
    private let lock = NSLock()
    private var value = 0, stop = false
    var peak: Int { lock.lock(); defer { lock.unlock() }; return value }
    func sample() { let n = threadCount(); lock.lock(); value = max(value, n); lock.unlock() }
    var stopped: Bool { lock.lock(); defer { lock.unlock() }; return stop }
    func end() { lock.lock(); stop = true; lock.unlock() }
}

private final class Tally: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0, running = 0, maxRunning = 0
    func started() { lock.lock(); running += 1; maxRunning = max(maxRunning, running); lock.unlock() }
    func finished() { lock.lock(); running -= 1; n += 1; lock.unlock() }
    var done: Int { lock.lock(); defer { lock.unlock() }; return n }
    var concurrent: Int { lock.lock(); defer { lock.unlock() }; return maxRunning }
}

final class BlockingWorkAdversarialTests: XCTestCase {
    /// 1000 blocking calls at once: how many threads does the dedicated queue use at peak, how
    /// many calls ran at the same time, and does every call finish exactly once.
    func testThousandConcurrentBlockingCalls() async {
        let base = threadCount()
        let peak = Peak(), tally = Tally()
        let sampler = Thread { while !peak.stopped { peak.sample(); usleep(5_000) } }
        sampler.start()
        let started = Date()
        let total = await withTaskGroup(of: Int.self) { group in
            for _ in 0..<1000 {
                group.addTask {
                    await BlockingWork.value { () -> Int in
                        tally.started(); usleep(50_000); tally.finished(); return 1
                    }
                }
            }
            return await group.reduce(0, +)
        }
        let elapsed = Date().timeIntervalSince(started)
        peak.end()
        print("B2-10 thread peak: base \(base), peak \(peak.peak) (+\(peak.peak - base)), max concurrent calls \(tally.concurrent), 1000 x 50 ms took \(elapsed) s")
        XCTAssertEqual(total, 1000)
        XCTAssertEqual(tally.done, 1000)
        XCTAssertLessThan(peak.peak - base, 200, "BlockingWork grew the process by \(peak.peak - base) threads")
    }

    /// With the queue saturated by long calls, a later short call waits behind them (a bound on
    /// threads is a bound on concurrency). Measured, printed, and asserted only to finish.
    func testShortCallBehindSaturatingLongCalls() async {
        let longCalls = 300
        let tasks = (0..<longCalls).map { _ in Task { await BlockingWork.value { usleep(1_000_000) } } }
        try? await Task.sleep(nanoseconds: 300_000_000)
        let started = Date()
        let r = await BlockingWork.value { 1 }
        let latency = Date().timeIntervalSince(started)
        print("B2-10 short call behind \(longCalls) x 1 s calls: \(latency) s")
        XCTAssertEqual(r, 1)
        for t in tasks { await t.value }
    }

    /// Each throwing call is resumed exactly once (a double resume traps under CheckedContinuation).
    func testManyThrowingCallsResumeOnce() async {
        struct Boom: Error {}
        let failures = await withTaskGroup(of: Int.self) { group in
            for i in 0..<300 {
                group.addTask {
                    switch await BlockingWork.run({ () -> Int in if i % 2 == 0 { throw Boom() }; return i }) {
                    case .failure: return 1
                    case .success: return 0
                    }
                }
            }
            return await group.reduce(0, +)
        }
        XCTAssertEqual(failures, 150)
    }

    /// Cancelled awaiting tasks do not stop their work and do not leak: all 100 children run to
    /// the end and every awaiting task still returns.
    func testCancelledAwaitersStillFinishTheirWork() async {
        let tally = Tally()
        let tasks = (0..<100).map { _ in
            Task { await BlockingWork.value { tally.started(); usleep(100_000); tally.finished() } }
        }
        for t in tasks { t.cancel() }
        for t in tasks { await t.value }
        XCTAssertEqual(tally.done, 100)
    }
}
