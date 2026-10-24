import Foundation
import ServiceManagement
import XCTest
@testable import VaniApplyKit

/// The Pipeline tab's "Run on schedule" switch as a function of the registration, the last
/// error and the daemon `vani status` reports. No SMAppService is touched.
final class ScheduleToggleTests: XCTestCase {
    private func make(_ r: ScheduleToggle.Registration, error: String? = nil, daemon: PipelineStatus.Daemon? = nil,
                      leftAlone: Bool = false, copy: ScheduleAgent.BundleCopy = .absent) -> ScheduleToggle {
        ScheduleToggle.make(registration: r, error: error, daemon: daemon, leftAlone: leftAlone, copy: copy)
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

    /// ServiceManagement reports `.notFound` for an agent it has never registered, which is
    /// this app's schedule agent until the switch is turned on. A bundle inspect of `.ready`
    /// is what makes that registrable; the old reading disabled the switch and said to rebuild.
    func testNotFoundWithAReadyBundleIsTheSameAsNeverRegistered() {
        let quiet = PipelineStatus.Daemon(reason: "no daemon has announced itself", running: false)
        XCTAssertEqual(make(.notFound, daemon: quiet, copy: .ready), make(.notRegistered, daemon: quiet))
        XCTAssertEqual(make(.notFound, daemon: running(), copy: .ready), make(.notRegistered, daemon: running()))
        XCTAssertTrue(make(.notFound, copy: .ready).isEnabled)
        XCTAssertEqual(make(.notFound, copy: .ready).headline, "Off")
        let alone = make(.notFound, daemon: running(), leftAlone: true, copy: .ready)
        XCTAssertFalse(alone.isEnabled)
        XCTAssertTrue(alone.headline.contains("Left alone"))
    }

    func testNotFoundWithAnUnusablePlistSaysWhyAndStaysOff() {
        let why = "The schedule agent plist is empty."
        let t = make(.notFound, copy: .unusable(why))
        XCTAssertFalse(t.isOn)
        XCTAssertFalse(t.isEnabled)
        XCTAssertEqual(t.headline, "This copy of the app cannot register the schedule agent")
        XCTAssertEqual(t.detail, why)
        // A refused register is shown as the error, and an agent that is already on can be turned off.
        XCTAssertEqual(make(.notFound, error: "boom", copy: .unusable(why)).detail, "boom")
        let on = make(.enabled, daemon: running(), copy: .unusable(why))
        XCTAssertTrue(on.isOn)
        XCTAssertTrue(on.isEnabled)
    }

    func testAnErrorIsCappedAndHasNoControlCharacters() {
        let t = make(.notRegistered, error: "no\u{0}pe " + String(repeating: "x", count: 400))
        let detail = t.detail ?? ""
        XCTAssertFalse(detail.contains("\u{0}"))
        XCTAssertLessThanOrEqual(detail.count, BoundedText.cap + 1)
        XCTAssertTrue(detail.hasSuffix("…"))
    }

    func testTurningOnAServiceMacOSHasNoRecordOfRegisters() {
        XCTAssertEqual(ScheduleToggle.order(on: true, repair: false, status: .notFound), .register)
        XCTAssertEqual(ScheduleToggle.order(on: true, repair: false, status: .notRegistered), .register)
        XCTAssertEqual(ScheduleToggle.order(on: false, repair: false, status: .notFound), .leave)
        XCTAssertEqual(ScheduleToggle.order(on: false, repair: true, status: .notFound), .leave)
        XCTAssertEqual(ScheduleToggle.order(on: true, repair: true, status: .notFound), .replace)
        XCTAssertEqual(ScheduleToggle.order(on: true, repair: false, status: .enabled), .leave)
        XCTAssertEqual(ScheduleToggle.order(on: true, repair: true, status: .enabled), .replace)
        XCTAssertEqual(ScheduleToggle.order(on: false, repair: true, status: .enabled), .unregister)
        XCTAssertEqual(ScheduleToggle.order(on: false, repair: false, status: .requiresApproval), .unregister)
    }

    func testAMissingJobAndAnAlreadyRegisteredAgentAreNotGenericFailures() {
        if #available(macOS 15, *) {
            let missing = NSError(domain: SMAppServiceErrorDomain, code: Int(kSMErrorJobNotFound), userInfo: [NSLocalizedDescriptionKey: "gone"])
            XCTAssertEqual(ScheduleToggle.Fault.classify(missing), .jobMissing)
            let present = NSError(domain: SMAppServiceErrorDomain, code: Int(kSMErrorAlreadyRegistered), userInfo: [NSLocalizedDescriptionKey: "there"])
            XCTAssertEqual(ScheduleToggle.Fault.classify(present), .alreadyRegistered)
            let other = NSError(domain: SMAppServiceErrorDomain, code: Int(kSMErrorInvalidPlist), userInfo: [NSLocalizedDescriptionKey: "bad\nplist"])
            if case let .message(text) = ScheduleToggle.Fault.classify(other) {
                XCTAssertEqual(text, "bad plist")
            } else {
                XCTFail("an invalid plist is a message")
            }
        }
        let foreign = NSError(domain: "other", code: Int(kSMErrorJobNotFound), userInfo: [NSLocalizedDescriptionKey: "not ours"])
        XCTAssertEqual(ScheduleToggle.Fault.classify(foreign), .message("not ours"))
        let blank = NSError(domain: "other", code: 1, userInfo: [NSLocalizedDescriptionKey: " \n\u{0}"])
        XCTAssertEqual(ScheduleToggle.Fault.classify(blank).text, "The system refused.")
        let stock = ScheduleToggle.Fault.classify(NSError(domain: "other", code: 1, userInfo: [:])).text
        XCTAssertFalse(stock.isEmpty)
        XCTAssertNotEqual(stock, "The system refused.")
        XCTAssertFalse(stock.contains("\n"))
        XCTAssertLessThanOrEqual(stock.count, BoundedText.cap + 1)
    }

    func testAStaleStatusReadIsDroppedWhenAChangeStarts() {
        var epoch = ScheduleToggle.Epoch()
        let first = epoch.current
        let change = epoch.begin()
        XCTAssertFalse(epoch.isCurrent(first))
        XCTAssertTrue(epoch.isCurrent(change))
        let published = epoch.begin()
        XCTAssertFalse(epoch.isCurrent(change), "a read that started during the change does not publish over it")
        XCTAssertTrue(epoch.isCurrent(published))
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
                    for copy in [ScheduleAgent.BundleCopy.absent, .ready, .unusable("why")] {
                        XCTAssertFalse(make(r, error: e, daemon: d, copy: copy).headline.isEmpty)
                    }
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
