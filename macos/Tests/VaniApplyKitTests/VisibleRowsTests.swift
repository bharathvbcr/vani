import XCTest
@testable import VaniApplyKit

/// Whether the Jobs list is scrolled past its first row, read from row
/// appear/disappear callbacks, which SwiftUI delivers in no promised order.
final class VisibleRowsTests: XCTestCase {
    /// Replays appear (true) and disappear (false) callbacks in order.
    private func rows(_ events: [(String, Bool)]) -> VisibleRows {
        var r = VisibleRows()
        for (key, on) in events {
            if on { r.appeared(key) } else { r.disappeared(key) }
        }
        return r
    }

    func testNothingShownYetIsNotScrolled() {
        XCTAssertFalse(VisibleRows().scrolledPast(first: "a"), "before any row reports, the list is at its top: no button flash")
        XCTAssertFalse(VisibleRows().scrolledPast(first: nil))
    }

    func testAtTopWhileTheFirstRowIsOnScreen() {
        XCTAssertFalse(rows([("a", true), ("b", true), ("c", true)]).scrolledPast(first: "a"))
    }

    func testScrolledOnceTheFirstRowLeaves() {
        let r = rows([("a", true), ("b", true), ("c", true), ("a", false), ("d", true)])
        XCTAssertTrue(r.scrolledPast(first: "a"))
    }

    func testBackAtTopWhenTheFirstRowReturns() {
        let r = rows([("a", true), ("b", true), ("a", false), ("c", true), ("a", true), ("c", false)])
        XCTAssertFalse(r.scrolledPast(first: "a"))
    }

    // A new tab or filter swaps the rows: the new first row can report before
    // the old one leaves. Whatever the order, the list is at its top.
    func testSwappedListAtTopInEitherOrder() {
        let before: [(String, Bool)] = [("a", true), ("b", true)]
        let newFirstAppearsFirst = rows(before + [("x", true), ("y", true), ("a", false), ("b", false)])
        XCTAssertFalse(newFirstAppearsFirst.scrolledPast(first: "x"), "the new first row is on screen")
        let oldLeavesFirst = rows(before + [("a", false), ("b", false), ("x", true), ("y", true)])
        XCTAssertFalse(oldLeavesFirst.scrolledPast(first: "x"))
    }

    // A swap while scrolled down keeps the scroll: the new first row is off
    // screen, so the button stays.
    func testSwappedListStillScrolledWhenTheNewFirstRowIsOffScreen() {
        let r = rows([("a", true), ("m", true), ("a", false), ("n", true), ("x", true), ("m", false)])
        XCTAssertFalse(r.scrolledPast(first: "x"), "x is on screen")
        let off = rows([("a", true), ("m", true), ("a", false), ("p", true), ("q", true), ("m", false)])
        XCTAssertTrue(off.scrolledPast(first: "x"), "rows p and q are on screen, x is not")
    }

    // The first row is asked about with the list as it is now, so a stale
    // "first" passed with an event cannot pin the answer.
    func testAnswerFollowsTheCurrentFirstRow() {
        let r = rows([("a", true), ("b", true)])
        XCTAssertFalse(r.scrolledPast(first: "a"))
        XCTAssertTrue(r.scrolledPast(first: "z"), "the list now starts at z, which is not on screen")
        XCTAssertFalse(r.scrolledPast(first: "b"), "the list now starts at b, which is")
    }

    func testEmptyListIsNotScrolled() {
        XCTAssertFalse(rows([("a", true), ("a", false)]).scrolledPast(first: nil))
        XCTAssertFalse(rows([("a", true), ("a", false)]).scrolledPast(first: "a"), "no row on screen: the list is gone, not scrolled")
    }

    // Duplicate and unmatched callbacks do not corrupt the set.
    func testDuplicateAndUnmatchedCallbacks() {
        let r = rows([("a", true), ("a", true), ("b", true), ("a", false), ("zz", false), ("b", true)])
        XCTAssertTrue(r.scrolledPast(first: "a"), "one disappear removes a twice-reported row")
        XCTAssertFalse(rows([("q", false), ("a", true)]).scrolledPast(first: "a"))
    }

    // Rows are reported only while on screen, but a list that never delivers
    // a disappear must not grow without bound.
    func testBoundedWhenDisappearsAreLost() {
        var r = VisibleRows()
        for i in 0..<10_000 { r.appeared("k\(i)") }
        XCTAssertLessThanOrEqual(r.count, VisibleRows.maxTracked)
        r.appeared("k0")
        XCTAssertFalse(r.scrolledPast(first: "k0"), "the most recent report wins over evicted ones")
    }
}
