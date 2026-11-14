import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Jobs tab's "Would you apply?" row: a yes is written at once; a maybe or a no first asks
/// what kept it from a yes, as the Audit tab does, and nothing is written until it is answered.
@MainActor
final class JobsWouldApplyTests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        return JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
    }

    private func answers(_ f: JobsFixture) -> [String] { f.calls().filter { $0.hasPrefix("audit answer") } }

    func testAYesIsWrittenAtOnce() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.chooseWouldApply("fx:alpha:1", .yes)
        XCTAssertEqual(answers(f), ["audit answer --json --shown --key=fx:alpha:1 --verdict=yes"])
        XCTAssertEqual(m.wouldApply["fx:alpha:1"], .answered(.yes))
        XCTAssertNil(m.wouldApplyPending["fx:alpha:1"])
    }

    func testAMaybeWaitsForItsReasonThenWritesIt() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.chooseWouldApply("fx:alpha:1", .maybe)
        XCTAssertEqual(m.wouldApplyPending["fx:alpha:1"], .maybe)
        XCTAssertTrue(answers(f).isEmpty, "nothing is written until the reason is given")
        await m.giveWouldApplyReason("fx:alpha:1", .domain)
        XCTAssertEqual(answers(f), ["audit answer --json --shown --key=fx:alpha:1 --verdict=maybe --reason=domain"])
        XCTAssertEqual(m.wouldApply["fx:alpha:1"], .answered(.maybe))
        XCTAssertNil(m.wouldApplyPending["fx:alpha:1"])
    }

    func testANoWithNoReasonIsWrittenBare() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.chooseWouldApply("fx:beta:2", .no)
        await m.giveWouldApplyReason("fx:beta:2", nil)
        XCTAssertEqual(answers(f), ["audit answer --json --shown --key=fx:beta:2 --verdict=no"])
    }

    func testCancelTakesTheVerdictBackAndWritesNothing() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.chooseWouldApply("fx:beta:2", .no)
        m.cancelWouldApplyReason("fx:beta:2")
        XCTAssertNil(m.wouldApplyPending["fx:beta:2"])
        await m.giveWouldApplyReason("fx:beta:2", .level) // nothing pending: a stray tap writes nothing
        XCTAssertTrue(answers(f).isEmpty, "\(answers(f))")
        XCTAssertNil(m.wouldApply["fx:beta:2"])
    }
}
