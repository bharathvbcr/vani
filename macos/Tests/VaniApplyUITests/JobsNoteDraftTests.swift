import Foundation
import XCTest
@testable import VaniApply
import VaniApplyKit

/// A note typed in the detail pane is kept by the model, keyed by job, and saved when the selection
/// moves away by any route (the detail view is rebuilt per job and cannot do it). Against the fake
/// `vani jobs` (JobsFixture); model level, no accessibility trust.
@MainActor
final class JobsNoteDraftTests: XCTestCase {
    private func model(_ f: JobsFixture) -> JobsModel {
        let vani = f.dir.appendingPathComponent("vani")
        let m = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        m.reconcileDelay = 30
        return m
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(5)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    private func noteCalls(_ f: JobsFixture) -> [String] { f.calls().filter { $0.hasPrefix("jobs note") } }

    private let a = JobsFixture.roles[0].key
    private let b = JobsFixture.roles[1].key

    private func typed(_ m: JobsModel, _ f: JobsFixture, _ text: String) async throws {
        try f.setWriteReply(a, note: text)
        await m.refresh()
        m.selection = a
        m.editNote(a, text)
    }

    func testKeyboardOrClickSelectionOfAnotherJobSavesTheTypedNote() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        try await typed(m, f, "call Tuesday")
        m.selection = b // arrow key and click both set the selection
        try await eventually("the note save") { !self.noteCalls(f).isEmpty }
        XCTAssertEqual(noteCalls(f).count, 1, "\(f.calls())")
        XCTAssertTrue((noteCalls(f).first ?? "").contains(a) && (noteCalls(f).first ?? "").contains("call Tuesday"), "\(noteCalls(f))")
        try await eventually("the draft cleared") { m.noteDrafts[self.a] == nil }
    }

    func testAMultiSelectionOrAClearedSelectionSavesTheTypedNote() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        try await typed(m, f, "ask about visa-free remote")
        m.selectedKeys = [a, b]
        try await eventually("the note save") { !self.noteCalls(f).isEmpty }
        XCTAssertTrue((noteCalls(f).first ?? "").contains(a), "\(noteCalls(f))")
    }

    func testAChangedViewSavesTheTypedNote() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        try await typed(m, f, "applied via referral")
        m.view = .saved
        try await eventually("the note save") { !self.noteCalls(f).isEmpty }
        XCTAssertTrue((noteCalls(f).first ?? "").contains(a), "\(noteCalls(f))")
    }

    /// A mark that takes the job out of the view moves the selection; the note goes with it.
    func testASelectionMovedByAMarkSavesTheTypedNote() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        try await typed(m, f, "keep for later")
        try f.setWriteReply(a, mark: "hidden")
        try f.setListReply(JobsFixture.standardOrder.filter { $0 != a }, counts: ["new": 3, "all": 4])
        await m.setMark(a, .hidden)
        await m.refresh()
        try await eventually("the note save") { !self.noteCalls(f).isEmpty }
        XCTAssertTrue((noteCalls(f).first ?? "").contains(a), "\(noteCalls(f))")
    }

    func testNoteTypedBackToTheSavedTextOrBlankWritesNothing() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        m.selection = a
        m.editNote(a, "scratch")
        m.editNote(a, "  \n ") // back to the saved (empty) note
        XCTAssertNil(m.noteDrafts[a])
        m.selection = b
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertTrue(noteCalls(f).isEmpty, "\(f.calls())")
    }

    func testAnOversizedDraftIsKeptAndNeverSent() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        await m.refresh()
        m.selection = a
        m.editNote(a, String(repeating: "x", count: JobsClient.maxNoteBytes + 1))
        m.selection = b
        try await Task.sleep(nanoseconds: 300_000_000)
        XCTAssertTrue(noteCalls(f).isEmpty, "\(f.calls())")
        XCTAssertNotNil(m.noteDrafts[a], "the draft is shown again when the job is reopened")
    }

    func testAFailedSaveKeepsTheDraftAndSaysWhy() async throws {
        let f = try JobsFixture(self) // no write reply for a: vani fails
        let m = model(f)
        await m.refresh()
        m.selection = a
        m.editNote(a, "will not stick")
        m.selection = b
        try await eventually("the failure") { m.actionError != nil }
        XCTAssertEqual(m.noteDrafts[a], "will not stick")
    }

    func testCommittingTwiceAtOnceSavesOnce() async throws {
        let f = try JobsFixture(self)
        let m = model(f)
        try await typed(m, f, "once")
        let key = a
        let first = Task { await m.commitNote(key) }
        let second = Task { await m.commitNote(key) }
        _ = await (first.value, second.value)
        XCTAssertEqual(noteCalls(f).count, 1, "\(f.calls())")
    }
}
