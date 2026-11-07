import XCTest
@testable import VaniApplyKit

/// The board the Pipeline and Home views draw. Counts that were not reported
/// stay unavailable; a rate-limited board is not also a broken one.
final class PipelineBoardTests: XCTestCase {
    func testListingTwelveOfFortyEight() {
        let board = PipelineBoard.present(.init(state: .running, progress: listing(
            index: 12, total: 48, done: 11, read: 9, failed: 2, rateLimited: 1, listed: 100, added: 8,
            company: "Acme", phase: "listing")))!
        XCTAssertEqual(board.mark.title, "Running")
        XCTAssertEqual(board.mark.tone, .running)
        XCTAssertEqual(board.steps.map(\.title), ["Start", "Boards", "Triage", "Digest"])
        XCTAssertEqual(board.steps.map(\.phase), [.done, .current, .later, .later])
        XCTAssertFalse(board.steps[1].spinner, "a real fraction is a bar, not a spinner")
        XCTAssertEqual(board.stageLine, "Reading Acme")
        XCTAssertEqual(board.fraction?.caption, "board 12 of 48")
        XCTAssertEqual(board.fraction?.done, 12)
        XCTAssertEqual(board.fraction?.total, 48)
        XCTAssertEqual(board.placeCaption, "board 12 of 48")
        XCTAssertEqual(board.segments.map(\.kind), [.scraped, .broken, .limited, .left])
        XCTAssertEqual(board.segments.map(\.count), [9, 1, 1, 37])
        XCTAssertEqual(board.segments.reduce(0) { $0 + $1.count }, 48, "rate-limited is part of failed, not an extra board")
        XCTAssertEqual(value(board, "scraped"), .number(9))
        XCTAssertEqual(value(board, "left"), .number(37))
        XCTAssertEqual(value(board, "listed"), .number(100))
        XCTAssertEqual(value(board, "added"), .number(8))
        XCTAssertEqual(board.current?.title, "Acme")
        XCTAssertEqual(board.current?.token, "gh:acme")
        XCTAssertEqual(board.current?.note, "")
    }

    func testEachRailStep() {
        let cases: [(String, PipelineBoard.Step.Kind)] = [
            ("starting", .start), ("preparing", .start), ("inbox", .start),
            ("listing", .boards),
            ("near_dup", .triage), ("exclusions", .triage), ("logos", .triage),
            ("warm", .triage), ("advance", .triage), ("backfill", .triage),
            ("digest", .digest),
        ]
        for (stage, kind) in cases {
            let board = PipelineBoard.present(.init(state: .running, progress: .init(ok: true, stage: stage)))!
            XCTAssertEqual(board.steps.first { $0.phase == .current }?.kind, kind, stage)
            XCTAssertEqual(board.steps.count, 4, stage)
        }
        let unknown = PipelineBoard.present(.init(state: .running, progress: .init(ok: true, stage: "teleporting")))!
        XCTAssertEqual(unknown.mark.title, "teleporting")
        XCTAssertNil(unknown.steps.first { $0.phase == .current }, "an unknown stage is not a fifth step")
        XCTAssertTrue(unknown.steps.allSatisfy { $0.phase == .later })
    }

    func testALaterStageKeepsTheListingTotals() {
        let progress = PipelineStatus.LastRun.Progress(
            ok: true, stage: "near_dup", total: 48, done: 11, read: 9, failed: 2, rateLimited: 1, listed: 100, added: 8)
        let board = PipelineBoard.present(.init(state: .running, progress: progress))!
        XCTAssertEqual(board.steps.map(\.phase), [.done, .done, .current, .later])
        XCTAssertEqual(board.stageLine, "Checking near-duplicates")
        XCTAssertNil(board.fraction, "triage has a bar only during advance")
        XCTAssertEqual(board.placeCaption, "")
        XCTAssertTrue(board.steps[2].spinner)
        XCTAssertEqual(value(board, "scraped"), .number(9))
        XCTAssertEqual(value(board, "left"), .number(37))
        XCTAssertEqual(value(board, "added"), .number(8))
        XCTAssertNil(board.current)
    }

    func testAdvanceFractionWhileRunningAndCaptionAfterItStops() {
        let live = PipelineBoard.present(.init(state: .running, progress: .init(
            ok: true, stage: "advance", total: 48, done: 48, read: 48, added: 3, attempted: 40, limit: 300)))!
        XCTAssertEqual(live.fraction?.caption, "40 of 300")
        XCTAssertEqual(live.placeCaption, "40 of 300")
        XCTAssertEqual(live.steps.first { $0.phase == .current }?.kind, .triage)
        XCTAssertFalse(live.steps[2].spinner)

        let stopped = PipelineBoard.present(.init(state: .interrupted, progress: .init(
            ok: true, stage: "advance", attempted: 40, limit: 300)))!
        XCTAssertNil(stopped.fraction, "a stopped run does not animate the bar")
        XCTAssertEqual(stopped.placeCaption, "40 of 300")
        XCTAssertFalse(stopped.steps.contains { $0.spinner })
    }

    func testDidNotFinishWithASnapshotUsesThoseCounts() {
        let board = PipelineBoard.present(.init(state: .interrupted, progress: listing(
            index: 12, total: 48, done: 11, read: 9, failed: 2, rateLimited: 1, listed: 100, added: 8,
            company: "Acme")))!
        XCTAssertEqual(board.mark.title, "Did not finish")
        XCTAssertEqual(board.mark.tone, .failed)
        XCTAssertTrue(board.mark.detail.contains("crash"))
        XCTAssertNotEqual(board.mark.title, "Crashed")
        XCTAssertEqual(board.steps.map(\.phase), [.done, .current, .later, .later])
        XCTAssertNil(board.fraction)
        XCTAssertEqual(board.placeCaption, "board 12 of 48")
        XCTAssertEqual(value(board, "left"), .number(37))
        XCTAssertEqual(value(board, "added"), .number(8))
        XCTAssertTrue(board.rows.isEmpty)
        XCTAssertFalse(board.listNote.isEmpty)
        XCTAssertFalse(board.listNote.localizedCaseInsensitiveContains("no boards"))
        XCTAssertNil(board.ledgerLine)
    }

    func testDidNotFinishWithNoSnapshotIsNotAZeroReport() {
        let board = PipelineBoard.present(.init(state: .interrupted))!
        XCTAssertEqual(board.mark.title, "Did not finish")
        XCTAssertFalse(board.showsCounts)
        XCTAssertTrue(board.counts.isEmpty)
        XCTAssertTrue(board.segments.isEmpty)
        XCTAssertTrue(board.steps.allSatisfy { $0.phase == .later })
        XCTAssertEqual(board.listNote, "")
        XCTAssertNil(board.ledgerLine)
    }

    func testACleanFailureUsesTheBoardsReport() {
        let item = PipelineStatus.LastRun.Boards.Item(ats: "gh", token: "nvidia", company: "NVIDIA", outcome: .read, listed: 35, reconcileNew: 35)
        let missed = PipelineStatus.LastRun.Boards.Item(ats: "wd", token: "intel", company: "Intel", outcome: .failed, error: "http 500")
        let boards = PipelineStatus.LastRun.Boards(
            configured: 4, read: 2, failed: 1, skipped: 1, notReached: 0, listed: 35,
            items: [item, missed], added: 35)
        let board = PipelineBoard.present(.init(state: .failed, stage: "pipeline", boards: boards))!
        XCTAssertEqual(board.mark.title, "Failed")
        XCTAssertEqual(board.steps.first { $0.phase == .failed }?.kind, .boards)
        XCTAssertEqual(value(board, "scraped"), .number(2))
        XCTAssertEqual(value(board, "left"), .number(0))
        XCTAssertEqual(value(board, "added"), .number(35))
        XCTAssertEqual(board.rows.map(\.title), ["NVIDIA", "Intel"])
        XCTAssertEqual(board.rows.map(\.added), [.number(35), .unavailable])
        XCTAssertTrue(board.problemsDefault)
        XCTAssertEqual(board.listNote, "")
        XCTAssertTrue(board.steps.filter { $0.phase == .done }.map(\.kind) == [.start])
    }

    func testASucceededRunChecksEveryStep() {
        let board = PipelineBoard.present(.init(state: .ok, boards: .init(configured: 2, read: 2, listed: 4, added: 1)))!
        XCTAssertEqual(board.mark.symbol, "checkmark.circle.fill")
        XCTAssertTrue(board.steps.allSatisfy { $0.phase == .done })
        XCTAssertFalse(board.steps.contains { $0.spinner })
    }

    func testAnUnreadableSnapshotOmitsTheVisuals() {
        let board = PipelineBoard.present(.init(state: .running, progress: .init(
            ok: false, reason: "live progress is from another run", stage: "listing", total: 5, read: 9, company: "OtherCo")))!
        XCTAssertEqual(board.mark.tone, .failed)
        XCTAssertEqual(board.mark.title, "Live progress could not be read")
        XCTAssertTrue(board.mark.detail.contains("another run"))
        XCTAssertFalse(board.showsVisuals)
        XCTAssertTrue(board.steps.isEmpty)
        XCTAssertTrue(board.segments.isEmpty)
        XCTAssertFalse(board.showsCounts)
        XCTAssertNil(board.current)
    }

    func testAMissingAddedKeyIsNotReported() throws {
        let json = #"{"ok":true,"state":"running","progress":{"ok":true,"stage":"listing","index":2,"total":5,"done":1,"read":1,"listed":10,"company":"Acme"}}"#
        let run = try JobsClient.decoder.decode(PipelineStatus.LastRun.self, from: Data(json.utf8))
        XCTAssertNil(run.progress?.added)
        let board = try XCTUnwrap(PipelineBoard.present(run))
        XCTAssertEqual(value(board, "scraped"), .number(1))
        XCTAssertEqual(value(board, "listed"), .number(10))
        XCTAssertEqual(value(board, "added"), .unavailable)

        let reported = #"{"ok":true,"state":"running","progress":{"ok":true,"stage":"listing","total":5,"added":0}}"#
        let zeroRun = try JobsClient.decoder.decode(PipelineStatus.LastRun.self, from: Data(reported.utf8))
        XCTAssertEqual(zeroRun.progress?.added, 0)
        let zeroBoard = try XCTUnwrap(PipelineBoard.present(zeroRun))
        XCTAssertEqual(value(zeroBoard, "added"), .number(0))
    }

    func testRateLimitedIsNotAddedOnTopOfFailed() {
        let board = PipelineBoard.present(.init(state: .running, progress: listing(
            index: 1, total: 5, done: 1, read: 0, failed: 1, rateLimited: 1, listed: 0, added: 0)))!
        XCTAssertEqual(board.segments.map(\.kind), [.limited, .left])
        XCTAssertEqual(board.segments.map(\.count), [1, 4])
        XCTAssertFalse(board.segments.contains { $0.kind == .broken })
        XCTAssertEqual(board.segments.reduce(0) { $0 + $1.count }, 5)
    }

    func testBeforeListingDrawsNoTiles() {
        let board = PipelineBoard.present(.init(state: .running, progress: .init(ok: true, stage: "preparing")))!
        XCTAssertEqual(board.steps.first { $0.phase == .current }?.kind, .start)
        XCTAssertTrue(board.steps[0].spinner)
        XCTAssertNil(board.fraction)
        XCTAssertFalse(board.showsCounts)
        XCTAssertTrue(board.segments.isEmpty)
        XCTAssertNil(board.current)
    }

    func testSavingNamesTheBoardInHand() {
        let board = PipelineBoard.present(.init(state: .running, progress: listing(
            index: 2, total: 5, done: 1, read: 1, failed: 0, rateLimited: 0, listed: 10, added: 2,
            company: "Acme", phase: "reconciling")))!
        XCTAssertEqual(board.current?.note, "saving the listing")
        XCTAssertEqual(board.current?.symbol, "arrow.down.circle")
    }

    func testACutListKeepsTheFooter() {
        let items = (0..<2).map {
            PipelineStatus.LastRun.Boards.Item(ats: "gh", token: "\($0)", outcome: .failed, error: "http 500")
        }
        let boards = PipelineStatus.LastRun.Boards(
            configured: 30, read: 1, failed: 20, skipped: 0, notReached: 9, listed: 3,
            items: items, unrecorded: 19, added: 3)
        let board = PipelineBoard.present(.init(state: .cancelled, boards: boards))!
        XCTAssertTrue(board.problemsDefault)
        XCTAssertTrue(board.footer.contains("18 more boards not read"), board.footer)
        XCTAssertTrue(board.footer.contains("19 boards not recorded"), board.footer)
        XCTAssertFalse(board.listNote.localizedCaseInsensitiveContains("no boards"))
        XCTAssertEqual(board.mark.title, "Cancelled")
        XCTAssertEqual(board.mark.tone, .cancelled)
    }

    func testParallelBadgeForInFlightBoardsAndWorkers() {
        var progress = listing(index: 12, total: 48, done: 11, read: 9, failed: 2, rateLimited: 1, listed: 100, added: 8)
        progress.inFlight = 6
        let liveListing = PipelineBoard.present(.init(state: .running, progress: progress))!
        XCTAssertEqual(liveListing.parallelBadge, "6 boards in flight")

        let liveTriage = PipelineBoard.present(.init(state: .running, progress: .init(
            ok: true, stage: "advance", attempted: 40, limit: 300, workers: 4)))!
        XCTAssertEqual(liveTriage.parallelBadge, "4 parallel workers")

        let finished = PipelineBoard.present(.init(state: .ok, advance: .init(eligible: 100, limit: 50, attempted: 50, deferred: 50, workers: 8)))!
        XCTAssertEqual(finished.parallelBadge, "8 parallel workers")

        let single = PipelineBoard.present(.init(state: .ok, advance: .init(eligible: 100, limit: 50, attempted: 50, deferred: 50, workers: 1)))!
        XCTAssertNil(single.parallelBadge)
    }

    private func value(_ board: PipelineBoard, _ id: String) -> PipelineBoard.CountValue? {
        board.counts.first { $0.id == id }?.value
    }

    private func listing(index: Int, total: Int, done: Int, read: Int, failed: Int, rateLimited: Int,
                         listed: Int, added: Int, company: String = "Acme", phase: String = "listing") -> PipelineStatus.LastRun.Progress {
        .init(ok: true, stage: "listing", phase: phase, index: index, total: total, done: done, read: read,
              failed: failed, rateLimited: rateLimited, listed: listed, added: added,
              ats: "gh", token: "acme", company: company)
    }
}
