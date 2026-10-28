import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Jobs tab's stage action, driven directly against the fake `vani jobs` (JobsFixture).
@MainActor
final class JobStageModelTests: XCTestCase {
    /// The fake answers no `jobs mark`, as a vani that refuses would (an older one, or an illegal stage): the
    /// error is shown, the list is not re-read as if the stage had landed, and selection stays.
    func testARefusedStageIsShownAndChangesNothing() async throws {
        let f = try JobsFixture(self)
        let m = JobsModel { JobsClient(vaniBinary: f.dir.appendingPathComponent("vani"), timeout: 10) }
        await m.refresh()
        let lists = f.calls().filter { $0.hasPrefix("jobs list") }.count
        let key = JobsFixture.roles[0].key
        m.selection = key
        await m.setStage(key, .interview)
        XCTAssertEqual(m.actionError, "fake vani: no reply for jobs mark")
        XCTAssertEqual(m.selection, key)
        XCTAssertEqual(f.calls().filter { $0.hasPrefix("jobs list") }.count, lists)
        XCTAssertTrue(f.calls().contains { $0.contains("jobs mark --json \(key) interview") }, "\(f.calls())")
        await m.setStage(key, nil)
        XCTAssertTrue(f.calls().contains { $0.contains("jobs mark --json \(key) unstage") }, "\(f.calls())")
    }

    /// vani refuses to swap rejected and withdrawn; the buttons are disabled up front, and everything else is open.
    func testOnlyTheOtherEndingIsBlocked() {
        for next in JobStage.allCases {
            XCTAssertFalse(JobStageViews.blocked(next, from: nil), "\(next) from applied")
            for current in JobStage.allCases {
                let want = current.isTerminal && next.isTerminal && current != next
                XCTAssertEqual(JobStageViews.blocked(next, from: current), want, "\(current) -> \(next)")
            }
        }
    }
}
