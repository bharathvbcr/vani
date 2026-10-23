import Foundation
import XCTest
@testable import VaniApplyKit

/// The Pipeline tab's "Run on schedule" switch as a function of the registration, the last
/// error and the daemon `vani status` reports. No SMAppService is touched.
final class ScheduleToggleTests: XCTestCase {
    private func make(_ r: ScheduleToggle.Registration, error: String? = nil, daemon: PipelineStatus.Daemon? = nil,
                      leftAlone: Bool = false) -> ScheduleToggle {
        ScheduleToggle.make(registration: r, error: error, daemon: daemon, leftAlone: leftAlone)
    }

    private func running(model: PipelineStatus.Daemon.Model? = nil) -> PipelineStatus.Daemon {
        PipelineStatus.Daemon(running: true, pid: 4242, model: model)
    }

    func testOffByDefaultAndSaysNothingFiresTheRun() {
        let t = make(.notRegistered, daemon: PipelineStatus.Daemon(reason: "no daemon has announced itself", running: false))
        XCTAssertFalse(t.isOn)
        XCTAssertTrue(t.isEnabled)
        XCTAssertEqual(t.headline, "Off")
        XCTAssertEqual(t.tone, .normal)
        XCTAssertFalse(t.offersRepair)
    }

    func testEnabledAndTheDaemonRunningIsKept() {
        let t = make(.enabled, daemon: running())
        XCTAssertTrue(t.isOn)
        XCTAssertEqual(t.headline, "Keeping the schedule (pid 4242)")
        XCTAssertEqual(t.tone, .normal)
        XCTAssertNil(t.detail)
        let withModel = make(.enabled, daemon: running(model: .init(enabled: true, keyEnv: "OPENROUTER_API_KEY")))
        XCTAssertEqual(withModel.tone, .normal)
    }

    func testEnabledWithTheModelDisabledIsAWarningThatSaysHowToFixIt() {
        let t = make(.enabled, daemon: running(model: .init(enabled: false, keyEnv: "OPENROUTER_API_KEY", reason: "OPENROUTER_API_KEY is not set")))
        XCTAssertTrue(t.isOn)
        XCTAssertEqual(t.tone, .warning)
        XCTAssertTrue(t.headline.contains("decision model off"))
        let detail = t.detail ?? ""
        for want in ["OPENROUTER_API_KEY is not set", "openrouter-api-key", "turn this off and on"] {
            XCTAssertTrue(detail.contains(want), "\(want) in \(detail)")
        }
    }

    func testEnabledButNoDaemonYetOffersRegisterAgain() {
        // The warning in `vani status` goes away only once a daemon announces itself; until then
        // the switch says it is on but has nothing running, and offers the repair.
        let t = make(.enabled, daemon: PipelineStatus.Daemon(reason: "no daemon has announced itself", running: false))
        XCTAssertTrue(t.isOn)
        XCTAssertEqual(t.tone, .warning)
        XCTAssertTrue(t.headline.contains("no daemon has started"))
        XCTAssertTrue(t.offersRepair)
        XCTAssertTrue(t.offersLoginItems)
        XCTAssertTrue((t.detail ?? "").contains("logs/vani.err.log"))
        XCTAssertTrue((t.detail ?? "").contains("no daemon has announced itself"))
    }

    func testAStaleDaemonRecordIsNotAKeeper() {
        let stale = PipelineStatus.Daemon(reason: "the daemon recorded as pid 99 is gone (it did not exit cleanly)", running: false, pid: 99)
        let t = make(.enabled, daemon: stale)
        XCTAssertTrue(t.headline.contains("no daemon has started"))
        XCTAssertTrue((t.detail ?? "").contains("is gone"))
    }

    func testUnknownDaemonStateIsNotCalledKept() {
        XCTAssertEqual(make(.enabled, daemon: nil).headline, "On")
        XCTAssertEqual(make(.enabled, daemon: nil).detail, "Checking whether the daemon is running…")
        let unreadable = make(.enabled, daemon: PipelineStatus.Daemon(ok: false, reason: "daemon record is corrupt", running: false))
        XCTAssertEqual(unreadable.tone, .warning)
        XCTAssertFalse(unreadable.headline.contains("Keeping"))
    }

    func testWaitingForApprovalPointsAtLoginItems() {
        let t = make(.requiresApproval)
        XCTAssertTrue(t.isOn, "the owner asked for it")
        XCTAssertEqual(t.tone, .warning)
        XCTAssertTrue(t.offersLoginItems)
        XCTAssertTrue((t.detail ?? "").contains("Login Items"))
        // Even a daemon that happens to run does not make an unapproved login item kept.
        XCTAssertEqual(make(.requiresApproval, daemon: running()).headline, "Waiting for your approval")
    }

    func testAnotherDaemonRunningWhileOffIsNamedNotClaimed() {
        let t = make(.notRegistered, daemon: running())
        XCTAssertFalse(t.isOn)
        XCTAssertTrue(t.headline.contains("a daemon is running"))
        XCTAssertTrue((t.detail ?? "").contains("ledger lock"))
    }

    func testNotFoundMeansThisCopyCannotRegisterIt() {
        let t = make(.notFound)
        XCTAssertFalse(t.isOn)
        XCTAssertFalse(t.isEnabled)
        XCTAssertEqual(t.tone, .problem)
        XCTAssertTrue((t.detail ?? "").contains("bundle.sh"))
    }

    func testAFailedChangeShowsTheErrorAndKeepsTheSwitchWhereMacOSHasIt() {
        let t = make(.notRegistered, error: "Operation not permitted")
        XCTAssertFalse(t.isOn)
        XCTAssertEqual(t.tone, .problem)
        XCTAssertEqual(t.detail, "Operation not permitted")
        let still = make(.enabled, error: "could not unregister", daemon: running())
        XCTAssertTrue(still.isOn, "a failed unregister leaves it registered, and the switch says so")
    }

    func testARigThatLeavesAgentsAloneCannotBeSwitched() {
        for r in [ScheduleToggle.Registration.notRegistered, .enabled, .requiresApproval, .notFound] {
            let t = make(r, daemon: running(), leftAlone: true)
            XCTAssertFalse(t.isEnabled, "\(r)")
            XCTAssertTrue(t.headline.contains("Left alone"))
        }
    }

    /// Every registration with every daemon state yields a headline: no combination is blank.
    func testEveryCombinationSaysSomething() {
        let daemons: [PipelineStatus.Daemon?] = [nil, running(), running(model: .init(enabled: false)),
                                                 PipelineStatus.Daemon(running: false), PipelineStatus.Daemon(ok: false, running: false)]
        for r in [ScheduleToggle.Registration.notRegistered, .enabled, .requiresApproval, .notFound] {
            for d in daemons {
                for e in [nil, "boom"] as [String?] {
                    XCTAssertFalse(make(r, error: e, daemon: d).headline.isEmpty)
                }
            }
        }
    }

    // MARK: the key outcomes a launch reports

    func testPrepareReportsWhatBecameOfTheDecisionKey() {
        let vani = URL(fileURLWithPath: "/opt/vani/vani")
        func outcome(_ lookup: @escaping KeyLookup, withKey: Bool = true) -> DraftRunner.KeyOutcome? {
            let r = DraftRunner(vaniBinary: vani, keychainTimeout: 0.3, key: { nil }, decisionKey: lookup)
            guard case let .launch(l) = r.prepare(["run-once"], withDecisionKey: withKey) else { return nil }
            return l.decisionKey
        }
        XCTAssertEqual(outcome({ "k" }), .present)
        XCTAssertEqual(outcome({ nil }), .notFound)
        XCTAssertEqual(outcome({ "" }), .notFound)
        XCTAssertEqual(outcome({ "k" }, withKey: false), .notRequested)
        if case let .failed(why)? = outcome({ throw BankError("Keychain refused access to openrouter-api-key (status -25293)") }) {
            XCTAssertTrue(why.contains("-25293"))
        } else { XCTFail("a refusal is a failure outcome") }
        if case let .failed(why)? = outcome({ Thread.sleep(forTimeInterval: 30); return "late" }) {
            XCTAssertTrue(why.contains("not answered"))
        } else { XCTFail("a hung read is a failure outcome, bounded") }
    }
}
