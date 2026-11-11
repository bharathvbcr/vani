import AppKit
import SwiftUI
import XCTest
@testable import VaniApply

/// `ListTopScroller` finds the AppKit table under a List and scrolls it to the top of its content,
/// the margin above the first row included (the Jobs list's Back to top button).
@MainActor
final class ListTopScrollerTests: XCTestCase {
    /// A table as a SwiftUI List builds it: no header view (a header adds its height to the top inset).
    private func tableScrollView(_ frame: NSRect, rows height: CGFloat = 3000, header: Bool = false) -> NSScrollView {
        let sv = NSScrollView(frame: frame)
        sv.automaticallyAdjustsContentInsets = false
        let table = NSTableView(frame: NSRect(x: 0, y: 0, width: frame.width, height: height))
        if !header { table.headerView = nil }
        sv.documentView = table
        return sv
    }

    /// Two tables side by side: the one behind the probe is chosen, not the first found.
    func testPicksTheTableBehindTheView() throws {
        let root = NSView(frame: NSRect(x: 0, y: 0, width: 1000, height: 800))
        let left = tableScrollView(NSRect(x: 0, y: 0, width: 500, height: 800))
        let right = tableScrollView(NSRect(x: 500, y: 0, width: 500, height: 800))
        let probe = NSView(frame: right.frame)
        root.addSubview(left)
        root.addSubview(right)
        root.addSubview(probe)
        XCTAssertTrue(ListTopScroller.tableScrollView(behind: probe) === right)
    }

    /// A scroll view of anything but a table, a table covering under half of the view, and a
    /// zero-size view all find nothing, so the caller falls back.
    func testFindsNothingThatIsNotTheTableBehindIt() {
        let root = NSView(frame: NSRect(x: 0, y: 0, width: 1000, height: 800))
        let text = NSScrollView(frame: NSRect(x: 0, y: 0, width: 1000, height: 800))
        text.documentView = NSView(frame: NSRect(x: 0, y: 0, width: 1000, height: 3000))
        root.addSubview(text)
        let probe = NSView(frame: NSRect(x: 0, y: 0, width: 1000, height: 800))
        root.addSubview(probe)
        XCTAssertNil(ListTopScroller.tableScrollView(behind: probe), "a scroll view of a plain view is no List")

        let corner = tableScrollView(NSRect(x: 0, y: 0, width: 300, height: 300))
        root.addSubview(corner)
        XCTAssertNil(ListTopScroller.tableScrollView(behind: probe), "a table under a ninth of the view is not behind it")

        let empty = NSView(frame: .zero)
        root.addSubview(empty)
        XCTAssertNil(ListTopScroller.tableScrollView(behind: empty))
    }

    /// Scrolled deep, the table returns to where it opened: the top of its content, its top inset
    /// and any header included, above the first row's own top (where a scroll to the row stops).
    func testScrollsBackToWhereTheTableOpened() throws {
        let sv = tableScrollView(NSRect(x: 0, y: 0, width: 400, height: 600))
        let opened = sv.contentView.bounds.minY
        sv.contentView.scroll(to: NSPoint(x: 0, y: 1800))
        XCTAssertEqual(sv.contentView.bounds.minY, 1800)
        ListTopScroller.scrollToTop(sv, animated: false)
        XCTAssertEqual(sv.contentView.bounds.minY, opened)
        XCTAssertEqual(opened, 0)

        sv.contentInsets = NSEdgeInsets(top: 24, left: 0, bottom: 0, right: 0)
        sv.contentView.scroll(to: NSPoint(x: 0, y: 900))
        ListTopScroller.scrollToTop(sv, animated: false)
        XCTAssertEqual(sv.contentView.bounds.minY, -24, "the top inset is part of the top")

        // A table with a header: it opens under the header, and returns there.
        let headed = tableScrollView(NSRect(x: 0, y: 0, width: 400, height: 600), header: true)
        let headedOpened = headed.contentView.bounds.minY
        headed.contentView.scroll(to: NSPoint(x: 0, y: 1200))
        ListTopScroller.scrollToTop(headed, animated: false)
        XCTAssertEqual(headed.contentView.bounds.minY, headedOpened)
    }

    /// Hosted with no table behind it, so each scroll it attempts shows as a fallback: none when it is
    /// made (even after presses, as when the List is rebuilt), one for each later change of request.
    func testScrollsOnAChangeOfRequestOnly() throws {
        final class Count { var n = 0 }
        let fallbacks = Count()
        let view = { (request: Int) in ListTopScroller(request: request, animated: false) { fallbacks.n += 1 }.frame(width: 200, height: 200) }
        let host = NSHostingView(rootView: view(3))
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 200, height: 200), styleMask: [.titled], backing: .buffered, defer: true)
        window.contentView = host
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        XCTAssertEqual(fallbacks.n, 0, "made at request 3: nothing to scroll for")

        host.rootView = view(4)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        XCTAssertEqual(fallbacks.n, 1, "a press: one scroll, and no table, so the fallback")

        host.rootView = view(4)
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.2))
        XCTAssertEqual(fallbacks.n, 1, "the same request again is no press")
    }

    /// A table shorter than its view cannot scroll: the top is where it already is.
    func testAShortTableStaysPut() {
        let sv = tableScrollView(NSRect(x: 0, y: 0, width: 400, height: 600), rows: 100)
        let opened = sv.contentView.bounds.minY
        ListTopScroller.scrollToTop(sv, animated: false)
        XCTAssertEqual(sv.contentView.bounds.minY, opened)
    }
}
