import CryptoKit
import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Studio window's model against a fake `vani jobs resume` that records its argument lists and its stdin
/// and replies from files. No window, no real vani, no Keychain (every key lookup returns nothing), no owner
/// state. Every id, company and sentence is made up; the PDFs are generated and carry no name.
@MainActor
final class ResumeStudioEditorTests: StudioFakeCase {
    // MARK: Loading

    func testOpenShowsWithTheCheckAndSeedsTheDraftFromTheShownVersion() async throws {
        let r = try rig(), e = r.editor
        XCTAssertEqual(e.loading, .loading)
        await e.open()
        XCTAssertEqual(e.loading, .loaded)
        XCTAssertEqual(r.calls(), ["jobs resume show --json fx:alpha:1"], "with the fresh check: no --no-check, and the current version")
        XCTAssertEqual(e.shownVersion, 3)
        XCTAssertEqual(e.title, "Resume — Alpha Labs, Platform Engineer")
        XCTAssertEqual(e.draft, e.seed)
        XCTAssertFalse(e.isDirty)
        XCTAssertEqual(e.draft?.sections.first?.bullets.map(\.id), ["s1", "s2"])
        XCTAssertNotNil(e.pool)
        XCTAssertTrue(e.canEdit)
        XCTAssertEqual(e.pdf, ResumeStudioEditor.PDFPreview(version: 3, data: r.pdf3), "a version with a PDF shows it")
        XCTAssertNil(e.html)
        XCTAssertEqual(e.versions.map(\.version), [3, 2])
        XCTAssertEqual(e.keywordTerms, ["Kubernetes", "CUDA"])
        XCTAssertEqual(e.sourceWord, "tailored")
        XCTAssertEqual(e.pagesWord, "2 pages")
        XCTAssertEqual(e.fitWord?.short, "Fits")
        XCTAssertEqual(e.eligibilityWord?.short, "Vouched for", "a fresh check says vouched for, not 'when built'")
        XCTAssertEqual(e.costWord, "at least $0.031", "a turn's cost was unknown: a lower bound, never an exact price")
        XCTAssertEqual(e.suggestedFileName, "Resume_Alpha_Labs.pdf")
        XCTAssertTrue(e.warnings.isEmpty, "\(e.warnings)")
    }

    func testAJobWithNoVersionStartsFromTheBaseAndFitsWithoutFrom() async throws {
        let r = try rig(versions: false), e = r.editor
        await e.open()
        XCTAssertNil(e.shownVersion)
        XCTAssertNil(e.version)
        XCTAssertEqual(e.draft?.section("Experience")?.entries.map(\.id), ["job1"])
        XCTAssertEqual(e.draft?.keywords, ["CUDA"], "the base's own keywords")
        XCTAssertNil(e.pdf)
        XCTAssertNil(e.eligibilityWord, "no version, no verdict")
        XCTAssertFalse(e.isDirty)
        try r.put("fit.json", saved4(r))
        try r.put("show-v4.json", shownJSON(version: 4, versions: [versionJSON(4, source: "edited", pdfPath: r.pdf4Path, pdfSHA: sha(r.pdf4), selection: ResumeEditTestsData.selection2, current: true)], current: 4, used: 0))
        XCTAssertNil(e.fitBlockedReason, "a job with nothing saved can fit its first version")
        await e.fit()
        XCTAssertEqual(r.calls().filter { $0.contains("render") }, ["jobs resume render --json --fit fx:alpha:1"], "no --from when there is no version")
    }

    func testWithoutThePoolNothingIsEditableAndNothingIsGuessed() async throws {
        let r = try rig(pool: false, checkError: "rkb could not be run"), e = r.editor
        await e.open()
        XCTAssertEqual(e.loading, .loaded)
        XCTAssertNil(e.pool)
        XCTAssertFalse(e.canEdit)
        XCTAssertTrue(e.editBlockedReason?.contains("could not be checked") ?? false, e.editBlockedReason ?? "")
        XCTAssertNotNil(e.draft, "the version can still be read")
        XCTAssertTrue(e.warnings.contains { $0.contains("rkb could not be run") }, "\(e.warnings)")
        e.setIncluded("s1", false, in: .summary("Summary"))
        XCTAssertEqual(e.draft, e.seed, "an edit with no pool changes nothing")
        XCTAssertNotNil(e.editNote)
        await e.requestReset()
        XCTAssertNotNil(e.editNote, "no base without a pool")
        XCTAssertNotNil(e.previewBlockedReason)
        await e.preview()
        XCTAssertFalse(r.calls().contains { $0.contains("render") })
        // A check that could not run is never a pass, whatever the version carries: not "vouched for", no Use.
        XCTAssertEqual(e.eligibilityWord?.short, "Not checked")
        XCTAssertEqual(e.eligibilityWord?.level, .notRun)
        XCTAssertNotNil(e.useBlockedReason)
        XCTAssertNotEqual(e.useAction, .available(version: 3))
        XCTAssertTrue(e.canRevalidate, "the offer is to check it again")
    }

    func testAFailedShowIsWordedAndAnUnreadableChoiceIsSaid() async throws {
        let r = try rig(), e = r.editor
        try r.put("show.json", #"{"error":"the ledger is locked","code":"ledger"}"#)
        try r.touch("show.fail")
        await e.open()
        guard case let .failed(f) = e.loading else { return XCTFail("\(e.loading)") }
        XCTAssertEqual(f.code, "ledger")
        XCTAssertNotNil(ResumeWords.failure(f))
        XCTAssertNil(e.draft)
        XCTAssertNotNil(e.editBlockedReason)

        r.remove("show.fail")
        try r.put("show.json", shownJSON(version: 3, versions: [versionJSON(3, pdfPath: nil, pdfSHA: nil, selection: #"{"sections":"nonsense"}"#, current: true)]))
        await e.reload(version: nil, check: true)
        XCTAssertEqual(e.loading, .loaded)
        XCTAssertNil(e.draft)
        XCTAssertTrue(e.warnings.contains { $0.contains("could not be read") }, "\(e.warnings)")
    }

    func testAVersionWhosePDFDoesNotMatchItsHashShowsNoPDFAndSaysWhy() async throws {
        let r = try rig(), e = r.editor
        try Data(pdf(0x43)).write(to: URL(fileURLWithPath: r.pdf3Path))
        await e.open()
        XCTAssertNil(e.pdf)
        XCTAssertTrue(e.previewError?.contains("checksum") ?? false, e.previewError ?? "")
        XCTAssertEqual(e.loading, .loaded)
    }

    // MARK: Local rules

    func testEditsMarkTheOffendingRowAndBlockPreviewAndFitUntilTheyAreFixed() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setOverride(.lead, "Doubled it 2x", entry: "projA")
        XCTAssertEqual(e.blockingIssues.map(\.target), [.override(entry: "projA", field: .lead)])
        XCTAssertEqual(e.issues(for: .override(entry: "projA", field: .lead)).count, 1)
        XCTAssertNotNil(e.previewBlockedReason)
        XCTAssertNotNil(e.fitBlockedReason)
        await e.preview()
        await e.fit()
        XCTAssertFalse(r.calls().contains { $0.contains("render") }, "nothing is sent while a row is marked")
        XCTAssertEqual(e.step, nil)
        e.setOverride(.lead, "Doubled it", entry: "projA")
        XCTAssertTrue(e.blockingIssues.isEmpty)
        XCTAssertNil(e.previewBlockedReason)
    }

    func testContinuationsMoveAndSwitchWithTheirLeadAndTheWindowSaysWhyARefusalHappened() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        let place = ResumeDraft.Place.entry("job1")
        e.addBullet("j4", to: place)
        e.moveGroup(in: place, from: 1, toOffset: 0)
        XCTAssertEqual(e.draft?.rows(in: place)?.map(\.id), ["j4", "j1", "j2", "j3"])
        e.setIncluded("j1", false, in: place)
        XCTAssertEqual(e.draft?.rows(in: place)?.map(\.included), [true, false, false, false])
        e.setIncluded("j2", true, in: place)
        XCTAssertNotNil(e.editNote, "j2 continues j1, which is off")
        XCTAssertTrue(e.isDirty)
        XCTAssertTrue(e.blockingIssues.isEmpty, "\(e.blockingIssues)")
    }

    func testSwapCandidatesAreThePoolBulletsNotOnThePageWithTheKeywordsTheyHoldFirst() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        let c = e.candidates(in: .entry("job1"))
        XCTAssertEqual(c.map(\.bullet.id), ["j4"], "j1 to j3 are on the page")
        XCTAssertEqual(c.first?.hits, ["CUDA"], "'Used CUDA kernels.' holds a posting keyword")
        let s = e.candidates(in: .summary("Summary"))
        XCTAssertEqual(s.map(\.bullet.id), ["s3"])
        XCTAssertEqual(s.first?.hits, ["Kubernetes"])
        XCTAssertEqual(e.addableEntries(in: "Open Source").map(\.id), ["projB"], "projA is on the page already, in Projects: an entry is on a page once")
        XCTAssertEqual(e.addableEntries(in: "Projects").map(\.id), ["projB"])
        XCTAssertEqual(e.addableEntries(in: "Experience").map(\.id), [], "a job is already there; a project does not go in the work history")
    }

    func testLineCountsShowOnlyWhileTheDraftIsTheFittedVersion() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        XCTAssertEqual(e.lines(for: "j1"), 2)
        XCTAssertNil(e.lines(for: "j2"), "no count for a bullet the fit report did not give")
        e.moveEntry("projA", by: 1)
        XCTAssertFalse(e.isDirty, "a single entry cannot move")
        e.setIncluded("j3", false, in: .entry("job1"))
        XCTAssertTrue(e.isDirty)
        XCTAssertNil(e.lines(for: "j1"), "an edited page's counts would be a guess")
    }

    // MARK: Preview

    func testPreviewSendsTheExactEditAndTheHTMLIsReadAndDeletedAtOnce() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setOverride(.stack, "Swift, Go", entry: "projA")
        e.setIncluded("j3", false, in: .entry("job1"))
        let file = try previewFile(r)
        try r.put("preview.json", rendered(path: file.path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: file.path))

        await e.preview()

        XCTAssertNil(e.failure)
        XCTAssertNil(e.step)
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path), "the page holds the header: gone as soon as it was read")
        XCTAssertEqual(e.html?.page, "<html><body>made-up page</body></html>")
        XCTAssertEqual(e.html?.checks.fit, "not_run", "an unfitted render never reads as fitted")
        XCTAssertEqual(r.calls().last, "jobs resume render --json fx:alpha:1")
        XCTAssertFalse(r.resume.isBusy, "the slot is given back")
        let sent = try XCTUnwrap(r.stdin("preview"))
        XCTAssertEqual(Set(sent.keys), ["sections", "jd_keywords"])
        let sections = try XCTUnwrap(sent["sections"] as? [[String: Any]])
        let experience = try XCTUnwrap(sections.first { $0["title"] as? String == "Experience" })
        XCTAssertEqual((experience["entries"] as? [[String: Any]])?.first?["bullets"] as? [String], ["j1", "j2"], "j3 is switched off, so it is not sent")
        let projects = try XCTUnwrap(sections.first { $0["title"] as? String == "Projects" })
        XCTAssertEqual((projects["entries"] as? [[String: Any]])?.first?["stack"] as? String, "Swift, Go")
        XCTAssertEqual(sent["jd_keywords"] as? [String], ["Kubernetes", "CUDA"])
        XCTAssertTrue(e.isDirty, "a preview saves nothing")
        XCTAssertEqual(r.calls().filter { $0.contains("--fit") }.count, 0)

        e.showSaved()
        XCTAssertNil(e.html)
        XCTAssertNotNil(e.pdf)
    }

    func testAPreviewPathOfAnotherShapeIsNeitherReadNorDeleted() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        let elsewhere = r.dir.appendingPathComponent("secret.html")
        try Data("<html>header</html>".utf8).write(to: elsewhere)
        let wrongFolder = r.dir.appendingPathComponent("preview-1.html")
        try Data("<html>header</html>".utf8).write(to: wrongFolder)
        let target = r.dir.appendingPathComponent("target.html")
        try Data("<html>target</html>".utf8).write(to: target)
        try FileManager.default.createSymbolicLink(at: r.tmp.appendingPathComponent("preview-7.html"), withDestinationURL: target)
        for path in [elsewhere.path, wrongFolder.path, r.tmp.appendingPathComponent("preview-7.html").path, r.tmp.appendingPathComponent("preview-x.html").path, "relative/preview-1.html"] {
            try r.put("preview.json", rendered(path: path))
            await e.preview()
            XCTAssertEqual(e.failure?.code, "render", path)
            XCTAssertNil(e.html, path)
            XCTAssertNil(e.step)
        }
        XCTAssertTrue(FileManager.default.fileExists(atPath: elsewhere.path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: wrongFolder.path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: target.path), "a link is refused, never followed")
    }

    func testAnOversizedPreviewIsRefusedAndStillDeleted() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        let file = try previewFile(r, String(repeating: "x", count: ResumePreviewFile.maxBytes + 10))
        try r.put("preview.json", rendered(path: file.path))
        await e.preview()
        XCTAssertEqual(e.failure?.code, "render")
        XCTAssertNil(e.html)
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path), "it holds the header, so it goes even when it is not shown")
    }

    func testAnUnfittedReplyThatClaimsAFitIsRefused() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        let file = try previewFile(r)
        try r.put("preview.json", rendered(path: file.path).replacingOccurrences(of: #""fit":"not_run""#, with: #""fit":"passed""#))
        await e.preview()
        XCTAssertEqual(e.failure?.code, "unreadable_reply")
        XCTAssertNil(e.html)
    }

    func testStoppingAPreviewEndsTheChildGivesTheSlotBackAndKeepsTheDraft() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        let before = e.draft
        try r.touch("slow")
        let running = Task { await e.preview() }
        try await eventually("the render to start") { e.step == .previewing && FileManager.default.fileExists(atPath: r.dir.appendingPathComponent("pid").path) }
        XCTAssertTrue(r.resume.isBusy, "a Studio render holds the one slot: the card's steps wait")
        XCTAssertNotNil(r.resume.blockedReason(for: try XCTUnwrap(e.job)))
        XCTAssertNotNil(e.previewBlockedReason)
        await e.preview()
        XCTAssertEqual(r.calls().filter { $0.contains("render") }.count, 1, "a second render does not start while one runs")
        e.stop()
        await running.value
        XCTAssertNil(e.step)
        XCTAssertNil(e.failure, "the owner's own stop is not an error")
        XCTAssertFalse(r.resume.isBusy)
        XCTAssertEqual(e.draft, before)
        XCTAssertNil(e.html)
    }

    func testClosingTheWindowMidRenderStopsTheChildAndShowsNothingAfterwards() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        try r.touch("slow")
        let running = Task { await e.preview() }
        try await eventually("the render to start") { e.step == .previewing && FileManager.default.fileExists(atPath: r.dir.appendingPathComponent("pid").path) }
        XCTAssertFalse(e.requestClose(), "unsaved edits: asks first")
        XCTAssertEqual(e.pending, .close)
        e.keepEditing()
        XCTAssertFalse(e.requestClose())
        await e.confirmPending()
        XCTAssertTrue(e.closeApproved)
        XCTAssertTrue(e.requestClose(), "Discard was chosen: it closes, and stops what was running")
        await running.value
        XCTAssertNil(e.html)
        XCTAssertNil(e.step)
        XCTAssertFalse(r.resume.isBusy)
    }

    // MARK: Fit

    func testFitSavesANewVersionFromTheShownOneAndShowsIt() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        try r.put("fit.json", saved4(r))
        try showAfterFit(r)
        let changed = ChangedKeys()
        r.resume.onChange = { await changed.add($0) }

        XCTAssertNil(e.fitBlockedReason)
        await e.fit()

        XCTAssertEqual(r.calls().filter { $0.contains("render") }, ["jobs resume render --json --fit --from v3 fx:alpha:1"])
        XCTAssertNil(e.failure)
        XCTAssertTrue(e.notice?.contains("v4 saved") ?? false, e.notice ?? "")
        XCTAssertEqual(e.shownVersion, 4, "the new version is the one shown")
        XCTAssertFalse(e.isDirty, "the draft is now the saved version")
        XCTAssertEqual(e.pdf, ResumeStudioEditor.PDFPreview(version: 4, data: r.pdf4))
        XCTAssertNil(e.html)
        XCTAssertNil(e.step)
        XCTAssertFalse(r.resume.isBusy)
        XCTAssertEqual(r.resume.knownPDF[Self.key]?.version, 4, "the card can open it")
        let keys = await changed.all
        XCTAssertEqual(keys, [Self.key], "the Jobs tab re-shows the posting")
        XCTAssertNotNil(r.stdin("fit"))
        XCTAssertEqual(e.fitBlockedReason, "Nothing changed since v4 was fitted.", "a second fit of the same page would only add a version")
    }

    func testAFailedFitKeepsTheDraftAndEveryCodeIsWorded() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        let draft = e.draft
        for code in ResumeWords.knownCodes {
            try r.put("fit.json", #"{"error":"vani said no (\#(code))","code":"\#(code)"}"#)
            try r.touch("fit.fail")
            await e.fit()
            XCTAssertNil(e.step, code)
            XCTAssertEqual(e.draft, draft, "\(code): a failed fit never touches the draft")
            XCTAssertFalse(r.resume.isBusy, code)
            if code == "cancelled" {
                XCTAssertNil(e.failureText, "the owner's own stop has no message")
            } else {
                XCTAssertEqual(e.failure?.code, code)
                XCTAssertFalse(e.failureText?.message.isEmpty ?? true, code)
            }
            e.dismissFailure()
        }
    }

    func testEveryFailureCodeIsWordedForAPreviewToo() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        for code in ResumeWords.knownCodes {
            try r.put("preview.json", #"{"error":"vani said no (\#(code))","code":"\#(code)"}"#)
            try r.touch("preview.fail")
            await e.preview()
            XCTAssertNil(e.step, code)
            if code == "cancelled" { XCTAssertNil(e.failureText, code) } else {
                XCTAssertEqual(e.failure?.code, code)
                XCTAssertFalse(e.failureText?.message.isEmpty ?? true, code)
            }
            e.dismissFailure()
        }
    }

    func testARefusalThatNamesABulletIsMarkedOnItsRowAndClearsWhenTheDraftChanges() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        try r.put("fit.json", #"{"error":"jobs resume: bullet \"j2\" is not available for this page","code":"ineligible"}"#)
        try r.touch("fit.fail")
        await e.fit()
        XCTAssertEqual(e.remoteIssues.map(\.target), [.bullet("j2")])
        XCTAssertTrue(e.remoteIssues.first?.message.contains("knowledge base does not offer") ?? false, "Go's refusal in the app's words")
        XCTAssertFalse(e.issues(for: .bullet("j2")).isEmpty)
        XCTAssertFalse(e.blockingIssues.isEmpty, "the same page is not sent again")
        e.setIncluded("j2", false, in: .entry("job1"))
        XCTAssertTrue(e.remoteIssues.isEmpty, "the refusal was about a page that no longer is")

        try r.put("fit.json", #"{"error":"jobs resume: the page is three pages long","code":"pages"}"#)
        await e.fit()
        XCTAssertEqual(e.remoteIssues.map(\.target), [.page], "a refusal about the whole page sits on the page")
    }

    func testStoppingAFitIsNotAnErrorAndAVersionSavedJustBeforeTheStopIsListed() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        let draft = e.draft
        try r.touch("slow")
        let running = Task { await e.fit() }
        try await eventually("the fit to start") { e.step == .fitting(cancelling: false) && FileManager.default.fileExists(atPath: r.dir.appendingPathComponent("pid").path) }
        XCTAssertNotNil(e.editBlockedReason, "the outline is locked while a fit runs")
        e.setIncluded("j1", false, in: .entry("job1"))
        XCTAssertEqual(e.draft, draft, "an edit during a fit would be lost to the reload, so it is refused")
        await e.requestSwitch(to: 2)
        XCTAssertNil(e.pending, "no version switch under a running fit")
        e.stop()
        XCTAssertEqual(e.step, .fitting(cancelling: true))
        r.remove("slow")
        try r.put("show.json", shownJSON(version: 3, versions: [versionJSON(4, pdfPath: nil, pdfSHA: nil, current: true), versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(r.pdf3), selection: ResumeEditTestsData.selection)], current: 4))
        await running.value
        XCTAssertNil(e.step)
        XCTAssertNil(e.failure)
        XCTAssertEqual(e.draft, draft, "the draft is kept")
        XCTAssertEqual(e.versions.map(\.version), [4, 3], "the list was read again")
        XCTAssertFalse(r.resume.isBusy)
    }

    // MARK: Versions, reset, close

    func testSwitchingVersionsAsksBeforeDiscardingEditsAndKeepsThePoolAndVerdicts() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        let verdict = e.versions.first { $0.version == 2 }?.eligibility?.state
        XCTAssertEqual(verdict, "stale")
        e.setIncluded("j3", false, in: .entry("job1"))
        let edited = e.draft

        await e.requestSwitch(to: 2)
        XCTAssertEqual(e.pending, .switchTo(2))
        XCTAssertEqual(e.shownVersion, 3, "nothing changed yet")
        XCTAssertEqual(e.draft, edited)
        XCTAssertEqual(r.calls().count, 1, "no call until the owner decides")
        e.keepEditing()
        XCTAssertNil(e.pending)
        XCTAssertEqual(e.draft, edited)

        await e.requestSwitch(to: 2)
        await e.confirmPending()
        XCTAssertNil(e.pending)
        XCTAssertEqual(e.shownVersion, 2)
        XCTAssertEqual(r.calls().last, "jobs resume show --json --no-check fx:alpha:1 v2", "stored files only (5 s): no rkb run to switch")
        XCTAssertEqual(e.draft?.section("Summary")?.bullets.map(\.id), ["s3"], "v2's own choice")
        XCTAssertFalse(e.isDirty)
        XCTAssertNotNil(e.pool, "the pool of the last full show is kept")
        XCTAssertTrue(e.canEdit)
        XCTAssertEqual(e.versions.first { $0.version == 2 }?.eligibility?.state, "stale", "the fresh verdicts are kept, not lost to the stored-only read")
        XCTAssertEqual(e.eligibilityWord?.short, "Out of date")
        XCTAssertNil(e.pdf, "v2 has no PDF: nothing is drawn as its page")
        XCTAssertEqual(e.shown?.eligibilityChecked, true)
        XCTAssertEqual(e.fitBlockedReason, nil)
    }

    func testSwitchingWithNoEditsGoesStraightThere() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        await e.requestSwitch(to: 2)
        XCTAssertNil(e.pending)
        XCTAssertEqual(e.shownVersion, 2)
        await e.requestSwitch(to: 2)
        XCTAssertEqual(r.calls().count, 2, "asking for the version already shown asks nothing")
    }

    func testResetToBaseAsksWhenThereAreEditsAndStartsFromThePoolsBase() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        await e.requestReset()
        XCTAssertEqual(e.pending, .reset)
        await e.confirmPending()
        let base = try XCTUnwrap(ResumeDraft.parse(selection: e.pool?.base, pool: e.pool))
        XCTAssertEqual(e.draft, base)
        XCTAssertEqual(e.draft?.section("Summary")?.bullets.map(\.id), ["s1"])
        XCTAssertTrue(e.isDirty, "the base is not what v3 is, so there is something to fit")
        XCTAssertEqual(e.shownVersion, 3, "a fit still builds on the version shown")
        await e.requestSwitch(to: 2)
        XCTAssertEqual(e.pending, .switchTo(2), "a reset draft is unsaved work too")
    }

    func testCloseAsksWhenThereAreEditsAndNeverWhenThereAreNone() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        XCTAssertTrue(e.requestClose(), "no edits: closes")
        let r2 = try rig(), e2 = r2.editor
        await e2.open()
        e2.setIncluded("j3", false, in: .entry("job1"))
        XCTAssertFalse(e2.requestClose())
        XCTAssertEqual(e2.pending, .close)
        e2.keepEditing()
        XCTAssertFalse(e2.requestClose())
        await e2.confirmPending()
        XCTAssertTrue(e2.closeApproved)
        XCTAssertTrue(e2.requestClose())
    }

    // MARK: Use and revalidate (the card's own steps)

    func testUseGoesThroughTheCardsModelAndOnlyForASavedFittedVouchedVersion() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        try r.put("use.json", #"{"key":"fx:alpha:1","version":3,"pdf_path":"\#(r.pdf3Path)","name":"Alpha_Resume.pdf","pdf_sha256":"\#(sha(r.pdf3))"}"#)
        XCTAssertEqual(e.useAction, .available(version: 3))
        XCTAssertNil(e.useBlockedReason)

        e.setIncluded("j3", false, in: .entry("job1"))
        XCTAssertTrue(e.useBlockedReason?.contains("Fit your edits") ?? false, "an unsaved draft cannot be used")
        e.use()
        XCTAssertEqual(r.resume.phase, .idle)
        XCTAssertFalse(r.calls().contains { $0.contains(" use ") })
        e.setIncluded("j3", true, in: .entry("job1"))
        XCTAssertFalse(e.isDirty)

        e.use()
        try await eventually("the use") { if case .used = r.resume.phase { true } else { false } }
        XCTAssertEqual(r.calls().last, "jobs resume use --json fx:alpha:1 v3", "the card's one use path, with the shown version")
        await e.phaseChanged(r.resume.phase)
        XCTAssertEqual(r.calls().last, "jobs resume show --json fx:alpha:1 v3", "the window looks again, with the check, at the version it shows")
        XCTAssertNil(e.failure)
    }

    func testAStaleVersionOffersRevalidateNotUseAndRevalidateIsTheCardsStep() async throws {
        let r = try rig(), e = r.editor
        try r.put("show.json", shownJSON(version: 3, versions: [
            versionJSON(3, pdfPath: r.pdf3Path, pdfSHA: sha(r.pdf3), selection: ResumeEditTestsData.selection, current: true, fresh: "stale"),
        ]))
        await e.open()
        XCTAssertEqual(e.eligibilityWord?.short, "Out of date")
        XCTAssertNotNil(e.useBlockedReason)
        XCTAssertTrue(e.canRevalidate)
        try r.put("revalidate.json", saved4(r, extra: #","dropped_stale":["j3"]"#).replacingOccurrences(of: #""source":"edited""#, with: #""source":"revalidated""#))
        try showAfterFit(r)
        e.revalidate()
        try await eventually("the revalidation") { if case .built = r.resume.phase { true } else { false } }
        XCTAssertEqual(r.calls().last, "jobs resume revalidate --json fx:alpha:1 v3")
        await e.phaseChanged(r.resume.phase)
        XCTAssertEqual(e.shownVersion, 4, "a clean window shows the version the revalidation made")
        XCTAssertTrue(e.notice?.contains("Dropped: j3") ?? false, e.notice ?? "")
    }

    func testAVersionMadeByTheCardWhileEditsAreOpenLeavesTheDraftAlone() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        e.setIncluded("j3", false, in: .entry("job1"))
        let draft = e.draft
        let saved = try JSONDecoder.resumeTest.decode(ResumeSaved.self, from: Data(saved4(r).utf8))
        await e.phaseChanged(.built(key: Self.key, saved: saved))
        XCTAssertEqual(e.draft, draft)
        XCTAssertEqual(e.shownVersion, 3)
        XCTAssertTrue(e.notice?.contains("Your edits here are unchanged") ?? false, e.notice ?? "")
        await e.phaseChanged(.built(key: "another:job", saved: saved))
        XCTAssertEqual(r.calls().filter { $0.contains(" show ") }.count, 2, "another job's step is not this window's business")
    }

    // MARK: Files

    func testThePDFIsReadThroughTheHashCheckAndNamedForTheCompany() async throws {
        let r = try rig(), e = r.editor
        await e.open()
        guard case let .success(data) = await e.pdfBytes() else { return XCTFail("pdf") }
        XCTAssertEqual(data, r.pdf3)
        guard case let .success(url) = await e.previewCopy() else { return XCTFail("copy") }
        XCTAssertEqual(url.lastPathComponent, "Resume_Alpha_Labs.pdf")
        XCTAssertEqual(try Data(contentsOf: url), r.pdf3)
        XCTAssertEqual(e.pdfURL?.path, r.pdf3Path)

        try Data(pdf(0x44)).write(to: URL(fileURLWithPath: r.pdf3Path))
        guard case let .failure(bad) = await e.pdfBytes() else { return XCTFail("a changed PDF must not be read") }
        XCTAssertTrue(bad.text.contains("checksum"), bad.text)

        await e.requestSwitch(to: 2)
        guard case let .failure(none) = await e.pdfBytes() else { return XCTFail("v2 has no PDF") }
        XCTAssertEqual(none, .none)
        XCTAssertFalse(e.hasPDF)
        XCTAssertNil(e.pdfURL)
    }

    // MARK: Helpers

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(10)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }
}

actor ChangedKeys {
    private(set) var all: [String] = []
    func add(_ key: String) { all.append(key) }
}

extension JSONDecoder {
    /// Go's times, for a reply a test decodes itself.
    static var resumeTest: JSONDecoder {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .iso8601
        return d
    }
}

/// One pool and two choices, shared by the editor tests. Invented ids and sentences.
enum ResumeEditTestsData {
    static let pool = """
    {"summary":[{"id":"s1","text":"Builds data pipelines.","text_hash":"aaaaaaaaaaaa"},
                {"id":"s2","text":"Keeps them fast.","text_hash":"bbbbbbbbbbbb","follows":"s1"},
                {"id":"s3","text":"Writes Kubernetes operators.","text_hash":"cccccccccccc"}],
     "summary_sections":["Summary"],
     "reorderable_sections":["Projects","Open Source"],
     "headings":[{"section":"Experience","entry":"job1"},{"section":"Education","entry":"edu"}],
     "entries":[
       {"id":"job1","title":"ML Engineer","org":"Acme","dates":"2022 – 2024","sections":["Experience"],
        "bullets":[{"id":"j1","text":"Trained models.","text_hash":"111111111111"},
                   {"id":"j2","text":"Shipped them.","text_hash":"222222222222","follows":"j1"},
                   {"id":"j3","text":"Watched them.","text_hash":"333333333333","follows":"j2"},
                   {"id":"j4","text":"Used CUDA kernels.","text_hash":"444444444444"}]},
       {"id":"projA","title":"Project A","sections":["Projects","Open Source"],
        "bullets":[{"id":"p1","text":"Built a compiler.","text_hash":"555555555555"},{"id":"p2","text":"Wrote docs.","text_hash":"666666666666"}]},
       {"id":"projB","title":"Project B","sections":["Projects","Open Source"],
        "bullets":[{"id":"q1","text":"Built a router.","text_hash":"777777777777"}]},
       {"id":"edu","title":"BS Computer Science","sections":["Education"],"bullets":[{"id":"e1","text":"Graduated.","text_hash":"888888888888"}]}],
     "base":{"sections":[
       {"title":"Summary","bullets":["s1"]},
       {"title":"Experience","entries":[{"entry":"job1","bullets":["j1","j2"]}]},
       {"title":"Projects","entries":[{"entry":"projA","bullets":["p1"]}]},
       {"title":"Open Source"},
       {"title":"Education","entries":[{"entry":"edu","bullets":[]}]}],
       "jd_keywords":["CUDA"]}}
    """

    static let selection = """
    {"sections":[
      {"title":"Summary","bullets":["s1","s2"]},
      {"title":"Experience","entries":[{"entry":"job1","bullets":["j1","j2","j3"]}]},
      {"title":"Projects","entries":[{"entry":"projA","bullets":["p1"],"title":"Compiler"}]},
      {"title":"Education","entries":[{"entry":"edu","bullets":[]}]}],
     "jd_keywords":["Kubernetes","CUDA"]}
    """

    static let selection2 = """
    {"sections":[
      {"title":"Summary","bullets":["s3"]},
      {"title":"Experience","entries":[{"entry":"job1","bullets":["j1","j2"]}]},
      {"title":"Education","entries":[{"entry":"edu","bullets":[]}]}],
     "jd_keywords":["Kubernetes"]}
    """
}
