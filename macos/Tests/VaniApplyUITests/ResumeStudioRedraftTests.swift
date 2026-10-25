import AppKit
import Foundation
import SwiftUI
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Studio's Redraft sheet, versions panel, coverage panel, hand-off, quit prompt and window restoration,
/// against the fake `vani jobs resume` of `StudioFakeCase` (it records every argument list in calls.log and
/// replies from files). No real vani, no model, no Keychain, no owner state. Every id, company and sentence is
/// made up. Nothing here spends: the fake is the only thing a "build" reaches.
@MainActor
final class ResumeStudioRedraftTests: StudioFakeCase {
    private let key = "fx:alpha:1"

    private func dryJSON(from: Int? = 3, scope: String? = "Projects", lock: [String] = ["Summary"], runes: Int? = 24, usable: Bool = true, current: Int = 3) -> String {
        var extra = ""
        if let from { extra += #","from":\#(from)"# }
        if let scope { extra += #","scope":"\#(scope)""# }
        if !lock.isEmpty { extra += #","lock":[\#(lock.map { "\"\($0)\"" }.joined(separator: ","))]"# }
        if let runes { extra += #","instruction_runes":\#(runes)"# }
        let model = usable ? #""opus_usable":true"# : #""opus_usable":false,"no_model":"no key""#
        let estimate = usable ? #","estimate":{"turns":2,"input_tokens":18000,"max_output_tokens":24000,"max_usd":0.42,"cap_reached":false}"# : ""
        return #"{"key":"fx:alpha:1","dry_run":true,"model":"claude-opus-5-5",\#(model),"current":\#(current)\#(extra),"budget":{"daily_cap_usd":20,"spent_usd":1,"remaining_usd":19}\#(estimate)}"#
    }

    /// The version a redraft saves: v4, made from v3, asked with an instruction, scoped and locked.
    private func redraftSaved(_ r: Rig) -> String {
        """
        {"schema":2,"key":"fx:alpha:1","version":4,"parent":3,"source":"tailored","instruction":"prefer tracing","scope":"Projects","lock":["Summary"],
         "checks":{"eligibility":"current","lint":"passed","fit":"passed","figures":"verified"},"pages":2,"pdf_sha256":"\(sha(r.pdf4))",
         "created":"2026-10-25T12:30:00Z","current_moved":true,"pdf_path":"\(r.pdf4Path)"}
        """
    }

    private func armRedraft(_ r: Rig) throws {
        try r.put("dry.json", dryJSON())
        try r.put("build.json", redraftSaved(r))
        try r.put("show-v4.json", shownJSON(version: 4, versions: [
            versionJSON(4, pdfPath: r.pdf4Path, pdfSHA: sha(r.pdf4), selection: ResumeEditTestsData.selection2, current: true,
                        meta: #""parent":3,"instruction":"prefer tracing","scope":"Projects","lock":["Summary"],"#),
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(r.pdf3)),
        ], current: 4, used: 2))
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(10)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func buildCalls(_ r: Rig) -> [String] { r.calls().filter { $0.contains(" build ") } }

    private func fillForm(_ e: ResumeStudioEditor) {
        e.redraftForm.scope = .section("Projects")
        e.redraftForm.locked = ["Summary", "Projects"] // Projects is the scope, so its lock is not sent
        e.redraftForm.instruction = "  prefer tracing  "
    }

    // MARK: The redraft, end to end

    func testRedraftAsksTheCostInTheStudioWindowThenBuildsWithTheFlagsAndLoadsTheNewVersion() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        await e.open()
        XCTAssertNil(e.redraftBlockedReason)
        e.openRedraft()
        XCTAssertTrue(e.isRedraftOpen)
        fillForm(e)
        XCTAssertEqual(e.redraftProblems, [])
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        // The dry run carries the flags, trimmed, in the equals form.
        XCTAssertEqual(buildCalls(r), ["jobs resume build --json --dry-run --from=v3 --scope=Projects --lock=Summary --instruction=prefer tracing fx:alpha:1"])
        // Only this window shows the confirmation: the Jobs window's sheet does not.
        XCTAssertFalse(r.resume.confirmingInJobs)
        XCTAssertEqual(r.resume.confirmationHost, .studio)
        let pending = try XCTUnwrap(r.resume.confirming)
        let words = ResumeWords.confirmation(for: pending.dry, redraft: r.resume.activeRedraft)
        XCTAssertEqual(words.redraftText, "Redraft of v3: re-picks only Projects, keeps Summary, and sends your 24-character instruction with the posting (it is never printed on the page).")
        XCTAssertTrue(words.canConfirm)
        XCTAssertFalse(words.offersBase)
        // Nothing was spent so far: no build call that is not a dry run.
        XCTAssertEqual(buildCalls(r).filter { !$0.contains("--dry-run") }, [])
        r.resume.confirm()
        try await eventually("the redraft to be saved") { if case .built = r.resume.phase { true } else { false } }
        XCTAssertEqual(buildCalls(r).last, "jobs resume build --json --from=v3 --scope=Projects --lock=Summary --instruction=prefer tracing fx:alpha:1")
        await e.phaseChanged(r.resume.phase)
        XCTAssertFalse(e.isRedraftOpen, "a saved redraft closes the sheet")
        XCTAssertEqual(e.shownVersion, 4, "the new version loads")
        XCTAssertEqual(e.version?.instruction, "prefer tracing")
        XCTAssertEqual(e.version?.scope, "Projects")
        XCTAssertEqual(e.version?.lock, ["Summary"])
        XCTAssertFalse(e.isDirty)
    }

    func testAnInstructionThatLooksLikeAVerbOrAVersionIsOnlyAValue() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        await e.open()
        e.openRedraft()
        e.redraftForm.instruction = "show use v2 revalidate --dry-run -x"
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        XCTAssertEqual(buildCalls(r), ["jobs resume build --json --dry-run --from=v3 --instruction=show use v2 revalidate --dry-run -x fx:alpha:1"])
        XCTAssertNotNil(r.resume.confirming, "the fake treated it as the build it is, not as another verb")
    }

    func testAnEstimateThatVaniRefusesComesBackOnTheFormWithWhatWasTypedAndNothingSpent() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        try r.put("dry.json", #"{"error":"the instruction looks like it holds a phone number; leave contact details out","code":"bad_instruction"}"#)
        try r.touch("dry.fail")
        await e.open()
        e.openRedraft()
        fillForm(e)
        e.askRedraftCost()
        try await eventually("the refusal") { if case .failed = r.resume.phase { true } else { false } }
        await e.phaseChanged(r.resume.phase)
        XCTAssertTrue(e.isRedraftOpen, "the sheet stays open on a refusal of the redraft")
        XCTAssertEqual(e.redraftForm.instruction, "  prefer tracing  ", "what was typed is still there")
        guard case let .failed(_, f) = r.resume.phase else { return XCTFail() }
        XCTAssertEqual(f.code, "bad_instruction")
        let text = try XCTUnwrap(ResumeWords.failure(f)).message
        XCTAssertTrue(text.contains("phone number") && text.contains("Nothing was spent"), text)
        XCTAssertEqual(buildCalls(r).filter { !$0.contains("--dry-run") }, [], "nothing was built")
        // Closing the sheet dismisses the refusal; asking again goes back through the estimate.
        e.closeRedraft()
        XCTAssertFalse(e.isRedraftOpen)
        if case .failed = r.resume.phase { XCTFail("the refusal was not dismissed with the sheet") }
    }

    func testAScopeRefusalAfterTheBuildReopensTheSheetAndASavedVersionClosesIt() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        try r.put("build.json", #"{"error":"the model changed a part that was kept","code":"scope","cost":{"usd":0.2,"complete":true,"turns":2},"attempt_recorded":true}"#)
        try r.touch("build.fail")
        await e.open()
        e.openRedraft()
        fillForm(e)
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        await e.phaseChanged(r.resume.phase)
        XCTAssertTrue(e.isRedraftOpen)
        r.resume.confirm()
        await e.phaseChanged(.building(key: key, allowBase: false, cancelling: false))
        XCTAssertFalse(e.isRedraftOpen, "the build runs in the status band where it can be stopped")
        try await eventually("the refusal") { if case .failed = r.resume.phase { true } else { false } }
        await e.phaseChanged(r.resume.phase)
        XCTAssertTrue(e.isRedraftOpen, "a refusal of the redraft itself opens the form again, to be corrected in place")
        guard case let .failed(_, f) = r.resume.phase else { return XCTFail() }
        let text = try XCTUnwrap(ResumeWords.failure(f))
        XCTAssertEqual(text.costNote, "Opus 5.5 was asked and charged $0.200, but nothing was saved; Vani recorded the attempt.")
        XCTAssertEqual(e.redraftForm.locked, ["Summary", "Projects"])
    }

    func testAFailureThatIsNotARefusalOfTheRedraftStaysInTheBandAndTheSheetStaysShut() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        try r.put("build.json", #"{"error":"rkb timed out","code":"timeout"}"#)
        try r.touch("build.fail")
        await e.open()
        e.openRedraft()
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        r.resume.confirm()
        await e.phaseChanged(.building(key: key, allowBase: false, cancelling: false))
        try await eventually("the failure") { if case .failed = r.resume.phase { true } else { false } }
        await e.phaseChanged(r.resume.phase)
        XCTAssertFalse(e.isRedraftOpen)
    }

    func testClosingTheSheetBeforeConfirmingSpendsNothingAndDropsTheCost() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        await e.open()
        e.openRedraft()
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        e.closeRedraft()
        XCTAssertFalse(e.isRedraftOpen)
        XCTAssertEqual(r.resume.phase, .idle, "closing is cancelling the cost question")
        XCTAssertEqual(buildCalls(r).filter { !$0.contains("--dry-run") }, [])
    }

    func testTheBaseSelectionIsNeverBuiltForARedraft() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        try r.put("dry.json", dryJSON(usable: false))
        await e.open()
        e.openRedraft()
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        let words = ResumeWords.confirmation(for: try XCTUnwrap(r.resume.confirming).dry, redraft: r.resume.activeRedraft)
        XCTAssertFalse(words.canConfirm)
        XCTAssertFalse(words.offersBase)
        r.resume.confirmBase()
        r.resume.confirm()
        XCTAssertNotNil(r.resume.confirming, "neither button builds anything")
        XCTAssertEqual(buildCalls(r).filter { !$0.contains("--dry-run") }, [], "no build, base or otherwise")
    }

    func testRetryAsksForTheSameRedraftAgainAndAPlainBuildCarriesNoFlags() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armRedraft(r)
        try r.put("dry.json", #"{"error":"vani took too long","code":"timeout"}"#)
        try r.touch("dry.fail")
        await e.open()
        e.openRedraft()
        fillForm(e)
        e.askRedraftCost()
        try await eventually("the failure") { if case .failed = r.resume.phase { true } else { false } }
        r.remove("dry.fail")
        try r.put("dry.json", dryJSON())
        r.resume.retry()
        try await eventually("the confirmation") { r.resume.confirming != nil }
        let calls = buildCalls(r)
        XCTAssertEqual(calls.count, 2)
        XCTAssertEqual(calls[0], calls[1], "the retry asks for the very same redraft")
        XCTAssertTrue(calls[1].contains("--instruction=prefer tracing"))
        // A plain build afterwards has none of it.
        r.resume.cancel()
        let job = try XCTUnwrap(e.job)
        r.resume.start(job)
        try await eventually("a new confirmation") { r.resume.confirming != nil }
        XCTAssertEqual(buildCalls(r).last, "jobs resume build --json --dry-run fx:alpha:1")
        XCTAssertNil(r.resume.activeRedraft)
        XCTAssertTrue(r.resume.confirmingInJobs, "and its confirmation is the Jobs window's")
    }

    func testRedraftIsRefusedWhileEditsAreUnsavedWithoutAPoolOrWithNothingToRedraft() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        XCTAssertNotNil(e.redraftBlockedReason, "before anything is loaded")
        await e.open()
        XCTAssertNil(e.redraftBlockedReason)
        e.setIncluded("s1", false, in: .summary("Summary"))
        XCTAssertTrue(e.redraftBlockedReason?.contains("Fit or discard your edits first") ?? false, e.redraftBlockedReason ?? "")
        e.openRedraft()
        XCTAssertFalse(e.isRedraftOpen)
        let none = try rig(versions: false, viaJobs: true)
        await none.editor.open()
        XCTAssertTrue(none.editor.redraftBlockedReason?.contains("no saved version") ?? false)
        let nopool = try rig(pool: false, checkError: "rkb could not be run", viaJobs: true)
        await nopool.editor.open()
        XCTAssertTrue(nopool.editor.redraftBlockedReason?.contains("could not be checked") ?? false)
    }

    func testTheSheetOffersOnlyWhatThePoolCanFillAndNeverALockOnTheScopedSection() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        await e.open()
        let t = try XCTUnwrap(e.redraftTargets)
        XCTAssertEqual(t.sections.map(\.title), ["Summary", "Experience", "Projects", "Education"])
        XCTAssertEqual(t.sections.first { $0.title == "Projects" }?.entries.map(\.id), ["projA"])
        e.redraftForm.scope = .entry("projA")
        e.redraftForm.locked = ["Projects", "Experience"]
        XCTAssertFalse(e.redraftForm.isLockable("Projects", t))
        XCTAssertEqual(e.redraftForm.request(from: 3, targets: t).lock, ["Experience"])
        // A form that names something this version does not hold is cleaned when the sheet opens.
        e.redraftForm.scope = .section("Gone")
        e.redraftForm.locked = ["Gone", "Summary"]
        e.openRedraft()
        XCTAssertEqual(e.redraftForm.scope, .whole)
        XCTAssertEqual(e.redraftForm.locked, ["Summary"])
        // A whole redraft sends only --from.
        e.redraftForm = ResumeRedraftForm()
        try r.put("dry.json", dryJSON(scope: nil, lock: [], runes: nil))
        e.askRedraftCost()
        try await eventually("a call") { !self.buildCalls(r).isEmpty }
        XCTAssertEqual(buildCalls(r).first, "jobs resume build --json --dry-run --from=v3 fx:alpha:1")
    }

    // MARK: Versions

    /// Four versions: v4 (a revalidation of v3) is current, v3 (asked with an instruction, scoped, locked) is the one shown, v2 is in use.
    private func armVersions(_ r: Rig) throws {
        try r.put("show.json", shownJSON(version: 3, versions: [
            versionJSON(4, source: "revalidated", pdfPath: r.pdf4Path, pdfSHA: sha(r.pdf4), current: true, meta: #""parent":3,"#),
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(r.pdf3), selection: ResumeEditTestsData.selection,
                        meta: #""parent":2,"instruction":"prefer tracing","scope":"Projects","lock":["Summary"],"#),
            versionJSON(2, source: "edited", pdfPath: nil, pdfSHA: nil, used: true, meta: #""parent":1,"#),
            versionJSON(1, pdfPath: nil, pdfSHA: nil),
        ], current: 4, used: 2))
    }

    func testTheVersionsListCarriesSourceParentBadgesKeptPartsAndTheInstructionOrTheParents() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armVersions(r)
        await e.open()
        let rows = e.versionRows
        XCTAssertEqual(rows.map(\.version), [4, 3, 2, 1])
        XCTAssertEqual(rows[0].badges, ["current"])
        XCTAssertEqual(rows[2].badges, ["in use"])
        XCTAssertEqual(rows[1].kept, "re-picked only Projects · kept Summary")
        XCTAssertEqual(rows[1].instruction?.display, "prefer tracing")
        XCTAssertEqual(rows[0].instruction?.display, "from v3: prefer tracing", "v4 was made without a model, so the parent's instruction is shown, named")
        XCTAssertEqual(rows[2].instruction?.display, nil, "v2 came from v1, which was asked with none")
        XCTAssertEqual(rows[0].parent, 3)
    }

    func testComparingWithTheParentRunsDiffParentToVersionAndShowsTextFromThePool() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armVersions(r)
        try r.put("diff.json", """
        {"key":"fx:alpha:1","from":{"version":3,"source":"tailored","parent":2,"instruction":"prefer tracing","scope":"Projects","lock":["Summary"],"created":"2026-10-25T12:00:00Z"},
         "to":{"version":4,"source":"revalidated","parent":3,"created":"2026-10-25T13:00:00Z"},"same":false,
         "bullets":{"added":[],"removed":[{"id":"j2","section":"Experience","entry":"job1","index":1}],"moved":[]},
         "overrides":[],"keywords":{"added":[],"removed":["etl"]}}
        """)
        await e.open()
        await e.compareWithParent(4)
        XCTAssertTrue(r.calls().contains("jobs resume diff --json fx:alpha:1 v3 v4"), "\(r.calls())")
        let view = try XCTUnwrap(e.comparisonView)
        XCTAssertEqual(view.removed.first?.text, "Shipped them.", "the wording is the pools")
        XCTAssertEqual(view.removed.first?.place, "Experience · ML Engineer · #2")
        XCTAssertEqual(view.keywordsRemoved, ["etl"])
        XCTAssertTrue(view.fromLine.contains("asked: prefer tracing"), view.fromLine)
        XCTAssertTrue(view.toLine.contains("asked from v3: prefer tracing"), "the revalidated side shows the parent's instruction, named: \(view.toLine)")
        e.clearComparison()
        XCTAssertNil(e.comparison)
        // A version with no parent has nothing to compare with.
        await e.compareWithParent(1)
        XCTAssertNil(e.comparison)
        // Compare with another version: that one against this.
        await e.compare(from: 2, to: 4)
        XCTAssertTrue(r.calls().contains("jobs resume diff --json fx:alpha:1 v2 v4"))
    }

    func testAComparisonThatCouldNotBeMadeIsSaidSoNeverShownAsNoDifference() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armVersions(r)
        try r.put("diff.json", #"{"error":"v9 does not exist","code":"not_found"}"#)
        try r.touch("diff.fail")
        await e.open()
        await e.compare(from: 3, to: 4)
        guard case let .failed(from, to, f) = e.comparison else { return XCTFail("\(String(describing: e.comparison))") }
        XCTAssertEqual([from, to, f.code == "not_found" ? 1 : 0], [3, 4, 1])
        XCTAssertNil(e.comparisonView)
        // A reply that contradicts itself is not a comparison either.
        r.remove("diff.fail")
        try r.put("diff.json", """
        {"key":"fx:alpha:1","from":{"version":3,"source":"tailored","created":"2026-10-25T12:00:00Z"},"to":{"version":4,"source":"edited","created":"2026-10-25T12:00:00Z"},
         "same":true,"bullets":{"added":[{"id":"p1","section":"Projects","entry":"projA","index":0}],"removed":[],"moved":[]},"overrides":[],"keywords":{"added":[],"removed":[]}}
        """)
        await e.compare(from: 3, to: 4)
        guard case let .failed(_, _, g) = e.comparison else { return XCTFail("a contradiction was drawn") }
        XCTAssertEqual(g.code, "unreadable_reply")
    }

    func testMakeCurrentRunsCurrentThenRereadsTheVersionsAndSaysWhatMoved() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armVersions(r)
        await e.open()
        XCTAssertEqual(e.makeCurrentBlockedReason(4), "v4 already is the current version.")
        XCTAssertNil(e.makeCurrentBlockedReason(3))
        try r.put("current.json", #"{"key":"fx:alpha:1","version":3,"previous":4,"changed":true}"#)
        try r.put("show-v3.json", shownJSON(version: 3, versions: [
            versionJSON(4, source: "revalidated", pdfPath: r.pdf4Path, pdfSHA: sha(r.pdf4), meta: #""parent":3,"#),
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(r.pdf3), selection: ResumeEditTestsData.selection, current: true, meta: #""parent":2,"#),
        ], current: 3, used: 2))
        await e.makeCurrent(3)
        XCTAssertTrue(r.calls().contains("jobs resume current --json fx:alpha:1 v3"), "\(r.calls())")
        XCTAssertEqual(e.versionsNote, "v3 is now the current version (it was v4).")
        XCTAssertNil(e.versionsFailure)
        XCTAssertEqual(e.shown?.current, 3, "the list was read again")
        XCTAssertEqual(e.makeCurrentBlockedReason(3), "v3 already is the current version.")
        XCTAssertEqual(r.calls().last.map { $0.contains("--no-check") }, true, "a stored-files read, no rkb check")
        // A restore that moved nothing says so.
        try r.put("current.json", #"{"key":"fx:alpha:1","version":3,"changed":false}"#)
        e.dismissVersionsMessage()
    }

    func testMakeCurrentFailingShowsVanisWordsAndMovesNothing() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armVersions(r)
        try r.put("current.json", #"{"error":"v3 is damaged","code":"damaged"}"#)
        try r.touch("current.fail")
        await e.open()
        await e.makeCurrent(3)
        XCTAssertNil(e.versionsNote)
        XCTAssertEqual(e.versionsFailure?.code, "damaged")
        XCTAssertEqual(e.shown?.current, 4, "nothing moved")
        e.dismissVersionsMessage()
        XCTAssertNil(e.versionsFailure)
    }

    func testMakeCurrentWaitsForAStepThatMayMoveCurrentItself() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armVersions(r)
        await e.open()
        try r.put("dry.json", dryJSON())
        e.openRedraft()
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        XCTAssertTrue(e.makeCurrentBlockedReason(3)?.contains("Another resume step is running") ?? false, e.makeCurrentBlockedReason(3) ?? "")
        await e.makeCurrent(3)
        XCTAssertFalse(r.calls().contains { $0.contains(" current ") }, "no restore ran while a build waited")
        e.closeRedraft()
    }

    // MARK: Coverage

    private func armCoverage(_ r: Rig) throws {
        try r.put("coverage.json", """
        {"key":"fx:alpha:1","version":3,"keywords_source":"posting+jd_keywords",
         "hit":[{"term":"Kubernetes","bullets":["s3","j2"],"n":2}],"miss":["Rust","Triton"],"uncovered":["Triton"],"unjudged":[],"unresolved":[],
         "terms_total":3,"gap_missing":[]}
        """)
    }

    func testCoverageIsAskedForTheShownVersionAndReadAsHitMissedUncovered() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armCoverage(r)
        await e.open()
        await e.loadCoverage()
        XCTAssertTrue(r.calls().contains("jobs resume coverage --json fx:alpha:1 v3"), "\(r.calls())")
        let v = try XCTUnwrap(e.coverageView)
        XCTAssertEqual(v.headline, "v3 covers 1 of 3 judged keywords.")
        XCTAssertEqual(v.hits.first?.bullets.map(\.text), ["Writes Kubernetes operators.", "Shipped them."], "the hit expands to the pool's bullets")
        XCTAssertEqual(v.swappable, ["Rust"])
        XCTAssertEqual(v.uncovered, ["Triton"])
        // Loaded for this version: not measured again unless asked.
        let before = r.calls().count
        await e.loadCoverage()
        XCTAssertEqual(r.calls().count, before)
        await e.loadCoverage(force: true)
        XCTAssertEqual(r.calls().count, before + 1)
        // Another version shown: the coverage on screen is stale, and measured again for it.
        XCTAssertFalse(e.coverageIsStale)
        await e.requestSwitch(to: 2)
        XCTAssertTrue(e.coverageIsStale)
        try r.put("coverage.json", #"{"key":"fx:alpha:1","version":2,"keywords_source":"posting","hit":[],"miss":["Rust"],"uncovered":[],"unjudged":[],"unresolved":[],"terms_total":1}"#)
        await e.loadCoverage()
        XCTAssertTrue(r.calls().contains("jobs resume coverage --json fx:alpha:1 v2"))
        XCTAssertEqual(e.coverageView?.headline, "v2 covers 0 of 1 judged keywords.")
    }

    func testAJobWithNoVersionIsMeasuredOnItsBaseAndAFailureIsNotAZero() async throws {
        let r = try rig(versions: false, viaJobs: true), e = r.editor
        try r.put("coverage.json", #"{"key":"fx:alpha:1","base":true,"keywords_source":"posting","hit":[],"miss":["Rust"],"uncovered":[],"unjudged":[],"unresolved":[],"terms_total":1}"#)
        await e.open()
        await e.loadCoverage()
        XCTAssertTrue(r.calls().contains("jobs resume coverage --json fx:alpha:1"), "no version asked, none sent")
        XCTAssertEqual(e.coverageView?.scopeLine, "Measured on your base selection: this job has no saved version yet.")
        try r.put("coverage.json", #"{"error":"the knowledge base could not be read","code":"kb"}"#)
        try r.touch("coverage.fail")
        await e.loadCoverage(force: true)
        guard case let .failed(f) = e.coverage else { return XCTFail("\(e.coverage)") }
        XCTAssertEqual(f.code, "kb")
        XCTAssertNil(e.coverageView, "no coverage is shown for a measurement that did not run")
    }

    func testClickingACoveredBulletSelectsItsRowAndASavedBulletMissingFromTheDraftSaysSo() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        await e.open()
        let tick = e.selectionTick
        e.select(bullet: "j2")
        XCTAssertEqual(e.selectedBullet, "j2")
        XCTAssertEqual(e.selectionTick, tick + 1)
        e.select(bullet: "j2")
        XCTAssertEqual(e.selectionTick, tick + 2, "clicking again scrolls again")
        e.select(bullet: "zzz")
        XCTAssertEqual(e.selectedBullet, "j2", "a bullet that is not in the draft selects nothing")
        XCTAssertTrue(e.editNote?.contains("not on the page you are editing") ?? false, e.editNote ?? "")
        // A row switched off is still a row.
        e.setIncluded("s1", false, in: .summary("Summary"))
        e.select(bullet: "s1")
        XCTAssertEqual(e.selectedBullet, "s1")
    }

    // MARK: Hand-off

    func testTheHandOffShowsTheTermsFirstAndWritesOnlyAfterConfirmation() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try r.put("gaps-dry.json", #"{"key":"fx:alpha:1","terms":["Triton","Rust"],"written":false,"file":"/s/resumes/gaps.jsonl","dry_run":true,"note":"dry run"}"#)
        try r.put("gaps.json", #"{"key":"fx:alpha:1","terms":["Triton","Rust"],"written":true,"file":"/s/resumes/gaps.jsonl"}"#)
        await e.open()
        await e.startHandOff()
        XCTAssertEqual(r.calls().filter { $0.contains(" gaps ") }, ["jobs resume gaps --json --dry-run fx:alpha:1"], "the dry run first, and only that")
        guard case let .confirming(g) = e.handOff else { return XCTFail("\(e.handOff)") }
        XCTAssertEqual(g.terms, ["Triton", "Rust"])
        XCTAssertFalse(g.written)
        await e.confirmHandOff()
        XCTAssertEqual(r.calls().filter { $0.contains(" gaps ") }.last, "jobs resume gaps --json fx:alpha:1", "the write has no --dry-run")
        guard case let .done(done) = e.handOff else { return XCTFail("\(e.handOff)") }
        XCTAssertTrue(done.written)
        XCTAssertTrue(ResumeWords.handOff(done).hasPrefix("Sent 2 terms"))
        e.dismissHandOff()
        XCTAssertEqual(e.handOff, .idle)
    }

    func testCancellingTheHandOffWritesNothingAndAnEmptyOneCannotBeSent() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try r.put("gaps-dry.json", #"{"key":"fx:alpha:1","terms":["Triton"],"written":false,"dry_run":true}"#)
        try r.put("gaps.json", #"{"key":"fx:alpha:1","terms":["Triton"],"written":true}"#)
        await e.open()
        await e.startHandOff()
        e.dismissHandOff()
        await e.confirmHandOff()
        XCTAssertEqual(r.calls().filter { $0.contains(" gaps ") }.count, 1, "no write after a cancel")
        try r.put("gaps-dry.json", #"{"key":"fx:alpha:1","terms":[],"written":false,"dry_run":true,"note":"no eligible bullet is missing a term, and DevPrism named none: nothing to hand over"}"#)
        await e.startHandOff()
        guard case let .confirming(empty) = e.handOff else { return XCTFail("\(e.handOff)") }
        XCTAssertTrue(empty.terms.isEmpty)
        await e.confirmHandOff()
        XCTAssertEqual(r.calls().filter { $0.contains(" gaps ") }.count, 2, "an empty hand-off is never written")
        XCTAssertTrue(ResumeWords.handOff(empty).contains("Nothing to hand over"))
    }

    func testAHandOffThatFailsOrComesBackAsTheWrongKindIsNeverSentOrShownAsSent() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try r.put("gaps-dry.json", #"{"error":"the posting's keywords could not be read, so the gaps cannot be judged and nothing was written","code":"failed"}"#)
        try r.touch("gaps.fail")
        await e.open()
        await e.startHandOff()
        guard case let .failed(f) = e.handOff else { return XCTFail("\(e.handOff)") }
        XCTAssertEqual(f.code, "failed")
        // A real write that comes back as a dry run is unreadable, not "sent".
        r.remove("gaps.fail")
        try r.put("gaps-dry.json", #"{"key":"fx:alpha:1","terms":["Triton"],"written":false,"dry_run":true}"#)
        try r.put("gaps.json", #"{"key":"fx:alpha:1","terms":["Triton"],"written":false,"dry_run":true}"#)
        await e.startHandOff()
        await e.confirmHandOff()
        guard case let .failed(g) = e.handOff else { return XCTFail("a dry run passed for a write: \(e.handOff)") }
        XCTAssertEqual(g.code, "unreadable_reply")
    }

    func testTheHandOffSaysWhenItUsesTheCurrentVersionAndNotTheOneOnScreen() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try armVersions(r)
        try r.put("show-v2.json", shownJSON(version: 2, versions: [
            versionJSON(4, source: "revalidated", pdfPath: r.pdf4Path, pdfSHA: sha(r.pdf4), current: true, fresh: nil),
            versionJSON(2, source: "edited", pdfPath: nil, pdfSHA: nil, selection: ResumeEditTestsData.selection2, used: true, fresh: nil),
        ], current: 4, used: 2, pool: nil, checked: false))
        await e.open()
        XCTAssertEqual(e.handOffNote, "The hand-off uses the current version, v4, not the v3 on screen.")
        await e.requestSwitch(to: 2)
        XCTAssertEqual(e.handOffNote, "The hand-off uses the current version, v4, not the v2 on screen.")
        try r.put("show-v4.json", shownJSON(version: 4, versions: [
            versionJSON(4, source: "revalidated", pdfPath: r.pdf4Path, pdfSHA: sha(r.pdf4), selection: ResumeEditTestsData.selection2, current: true, fresh: nil),
        ], current: 4, used: 2, pool: nil, checked: false))
        await e.requestSwitch(to: 4)
        XCTAssertNil(e.handOffNote, "v4 is on screen and is the current one")
    }

    // MARK: Orphans

    func testABulletRkbNamedAsAnOrphanIsMarkedOnlyWhileTheDraftIsTheFittedVersion() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        try r.put("show.json", shownJSON(version: 3, versions: [
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(r.pdf3), selection: ResumeEditTestsData.selection, current: true,
                        notes: #""notes":["OK: 2 page(s). STRICT: 1 orphan last line(s) (orphan j2, orphan zz9)"],"bullets":[{"id":"j1","text_hash":"111111111111"},{"id":"j2","text_hash":"222222222222"}],"#),
        ]))
        await e.open()
        XCTAssertEqual(e.orphanBullets, ["j2"], "zz9 is not on the page")
        e.setIncluded("j3", false, in: .entry("job1"))
        XCTAssertEqual(e.orphanBullets, [], "once the draft differs from the fitted page the line breaks are not known")
    }

    // MARK: Quitting

    func testQuittingAsksOnceForAllDirtyWindowsAndNeverForCleanOnes() async throws {
        let a = try rig(viaJobs: true), b = try rig(viaJobs: true)
        await a.editor.open()
        await b.editor.open()
        let windows = StudioWindows()
        windows.register(a.editor)
        var asked: [ResumeQuitPrompt] = []
        func reply(_ answer: Bool) -> NSApplication.TerminateReply {
            windows.terminateReply { asked.append($0); return answer }
        }
        XCTAssertEqual(reply(true), .terminateNow, "nothing unsaved: not asked")
        XCTAssertTrue(asked.isEmpty)
        a.editor.setIncluded("s1", false, in: .summary("Summary"))
        XCTAssertEqual(windows.dirtyJobs, ["Alpha Labs, Platform Engineer"])
        XCTAssertEqual(reply(false), .terminateCancel, "Keep Editing cancels the quit")
        XCTAssertEqual(asked.count, 1)
        XCTAssertEqual(reply(true), .terminateNow, "Quit and Discard quits")
        XCTAssertEqual(asked.count, 2)
        XCTAssertTrue(asked[0].message.contains("Alpha Labs, Platform Engineer"))
        // A second window (another key) with edits is named in the same single question.
        let other = ResumeStudioEditor(key: "fx:beta:2", resume: b.resume, job: nil)
        windows.register(other)
        XCTAssertEqual(windows.dirtyJobs.count, 1, "an editor with nothing loaded has no edits")
        // A closed window is forgotten, and a window that is gone is not asked about.
        windows.forget(key: a.editor.key)
        XCTAssertEqual(windows.dirtyJobs, [])
        XCTAssertEqual(reply(false), .terminateNow)
        XCTAssertEqual(asked.count, 2, "nobody was asked")
    }

    func testTwoDirtyWindowsAreOneQuestionNamingBoth() async throws {
        let a = try rig(viaJobs: true), b = try rig(viaJobs: true)
        // Different keys so both can be registered at once.
        await a.editor.open()
        let second = ResumeStudioEditor(key: "fx:beta:2", resume: b.resume, job: a.editor.job.map { j in
            var copy = j
            copy.company = "Beta Works"
            return copy
        })
        try b.put("show.json", try String(contentsOf: b.dir.appendingPathComponent("show.json"), encoding: .utf8).replacingOccurrences(of: "fx:alpha:1", with: "fx:beta:2"))
        await second.open()
        a.editor.setIncluded("s1", false, in: .summary("Summary"))
        second.setIncluded("s1", false, in: .summary("Summary"))
        let windows = StudioWindows()
        windows.register(a.editor)
        windows.register(second)
        var asked = 0
        var prompt: ResumeQuitPrompt?
        XCTAssertEqual(windows.terminateReply { prompt = $0; asked += 1; return false }, .terminateCancel)
        XCTAssertEqual(asked, 1, "one question, however many windows")
        XCTAssertEqual(prompt?.title, "Quit and discard unsaved resume edits in 2 windows?")
        XCTAssertTrue(prompt?.message.contains("Beta Works, Platform Engineer") ?? false)
        XCTAssertTrue(prompt?.message.contains("Alpha Labs, Platform Engineer") ?? false)
    }

    func testShuttingAWindowDownForgetsItInTheAppWideRegistry() async throws {
        let r = try rig(viaJobs: true)
        await r.editor.open()
        StudioWindows.shared.register(r.editor)
        r.editor.setIncluded("s1", false, in: .summary("Summary"))
        XCTAssertTrue(StudioWindows.shared.dirtyJobs.contains("Alpha Labs, Platform Engineer"))
        r.editor.shutdown()
        XCTAssertFalse(StudioWindows.shared.dirtyJobs.contains("Alpha Labs, Platform Engineer"))
    }

    // MARK: Window restoration

    func testAStudioWindowIsNotRestoredAtLaunch() {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 200, height: 100), styleMask: [.titled, .closable], backing: .buffered, defer: true)
        window.isReleasedWhenClosed = false
        XCTAssertTrue(window.isRestorable, "a window is restorable until it is told otherwise (the control)")
        let proxy = WindowCloseGuard.CloseProxy()
        proxy.attach(to: window, closer: WindowCloser())
        XCTAssertFalse(window.isRestorable, "macOS must not reopen a Studio window at launch")
    }

    // MARK: The Jobs window

    func testOpeningTheStudioFromTheJobsListRemembersTheJobAndAsksTheWindowToOpen() async throws {
        let r = try rig(viaJobs: true)
        let jobs = try XCTUnwrap(r.jobs)
        var opened: [String] = []
        jobs.resume.openStudio = { opened.append($0) }
        let job = try XCTUnwrap(r.editor.job)
        jobs.openStudio(job)
        XCTAssertEqual(opened, [Self.key])
        XCTAssertEqual(jobs.resume.studioJobs[Self.key]?.company, "Alpha Labs", "the window keeps its title after the list moves on")
        jobs.resume.openStudio = nil
        jobs.openStudio(job)
        XCTAssertEqual(opened.count, 1, "nothing pretends to open")
    }

    // MARK: Drawing

    private func render(_ name: String, _ view: some View, width: CGFloat = 1300, height: CGFloat = 820) throws {
        let renderer = ImageRenderer(content: view.frame(width: width, height: height).background(Color(nsColor: .windowBackgroundColor)))
        renderer.scale = 1
        let image = try XCTUnwrap(renderer.nsImage, name)
        XCTAssertGreaterThan(image.size.width, 100, name)
        if let dir = ProcessInfo.processInfo.environment["VANI_STUDIO_PNG_DIR"],
           let tiff = image.tiffRepresentation, let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]) {
            try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("studio-\(name).png"))
        }
    }

    func testTheNewPanelsDraw() async throws {
        let r = try rig(viaJobs: true), e = r.editor
        let jobs = try XCTUnwrap(r.jobs)
        try armVersions(r)
        try armCoverage(r)
        try r.put("diff.json", """
        {"key":"fx:alpha:1","from":{"version":3,"source":"tailored","parent":2,"instruction":"prefer tracing","scope":"Projects","lock":["Summary"],"created":"2026-10-25T12:00:00Z"},
         "to":{"version":4,"source":"revalidated","parent":3,"created":"2026-10-25T13:00:00Z"},"same":false,
         "bullets":{"added":[{"id":"p1","section":"Projects","entry":"projA","index":0}],"removed":[{"id":"gone9","section":"Experience","entry":"job1","index":1}],
                    "moved":[{"id":"j2","from":{"section":"Experience","entry":"job1","index":1},"to":{"section":"Experience","entry":"job1","index":0}}]},
         "overrides":[{"entry":"job1","field":"title","from":"","to":"Principal Engineer"}],
         "section_order":{"from":["Summary","Projects"],"to":["Projects","Summary"]},"keywords":{"added":["tracing"],"removed":["etl"]}}
        """)
        try r.put("gaps-dry.json", #"{"key":"fx:alpha:1","terms":["Triton","Rust"],"written":false,"dry_run":true}"#)
        await e.open()
        func content(_ pane: ResumeStudioEditor.RightPane) -> some View {
            e.rightPane = pane
            return ResumeStudioContent(editor: e, closer: WindowCloser(), flat: true).environment(jobs)
        }
        try render("versions", content(.versions))
        await e.compareWithParent(4)
        try render("compare", content(.versions))
        await e.loadCoverage()
        e.select(bullet: "j2")
        try render("coverage", content(.coverage))
        await e.startHandOff()
        try render("handoff", content(.coverage))
        e.setIncluded("j3", false, in: .entry("job1"))
        try render("coverage-dirty", content(.coverage))
        // The redraft form, the refusal on it, and the confirmation inside the sheet.
        e.redraftForm.instruction = String(repeating: "x", count: 512)
        e.redraftForm.scope = .entry("projA")
        try render("redraft-form", RedraftFormView(editor: e).environment(jobs).padding(), width: 560, height: 700)
        e.redraftForm = ResumeRedraftForm(scope: .section("Projects"), locked: ["Summary"], instruction: "prefer tracing")
        e.setIncluded("j3", true, in: .entry("job1"))
        try armRedraft(r)
        e.askRedraftCost()
        try await eventually("the confirmation") { r.resume.confirmingInStudio(Self.key) != nil }
        let pending = try XCTUnwrap(r.resume.confirming)
        try render("redraft-confirm", ResumeConfirmContent(key: pending.key, dry: pending.dry).environment(jobs), width: 560, height: 560)
        r.resume.cancel()
        // A refusal on the form.
        try r.put("dry.json", #"{"error":"the instruction looks like it holds a phone number; leave contact details out","code":"bad_instruction"}"#)
        try r.touch("dry.fail")
        e.askRedraftCost()
        try await eventually("the refusal") { if case .failed = r.resume.phase { true } else { false } }
        try render("redraft-refused", RedraftFormView(editor: e).environment(jobs).padding(), width: 560, height: 700)
        r.resume.dismiss()
    }
}
