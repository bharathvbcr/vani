import XCTest
@testable import VaniApplyKit

/// A notification click with the window closed must reopen it: before the fix
/// the click only looked for an open window to bring forward, and with none it
/// did nothing (verified in the bundled app).
final class MainWindowRevealTests: XCTestCase {
    func testClosedWindowIsReopenedThroughTheSceneOpener() {
        var opened = 0
        var searched = 0
        let reveal = MainWindowReveal(open: { opened += 1 }, bringForward: { searched += 1; return false })
        XCTAssertEqual(reveal.reveal(), .opened)
        XCTAssertEqual(opened, 1)
        XCTAssertEqual(searched, 0, "the opener handles an open window too; no window search")
    }

    func testOpenWindowStillGoesThroughTheOpener() {
        var opened = 0
        let reveal = MainWindowReveal(open: { opened += 1 }, bringForward: { true })
        XCTAssertEqual(reveal.reveal(), .opened)
        XCTAssertEqual(opened, 1)
    }

    func testWithoutAnOpenerAnOpenWindowIsBroughtForward() {
        var searched = 0
        let reveal = MainWindowReveal(open: nil, bringForward: { searched += 1; return true })
        XCTAssertEqual(reveal.reveal(), .broughtForward)
        XCTAssertEqual(searched, 1)
    }

    func testWithoutAnOpenerOrAWindowItSaysSo() {
        let reveal = MainWindowReveal(open: nil, bringForward: { false })
        XCTAssertEqual(reveal.reveal(), .noWindow, "a click that shows nothing must not read as handled")
    }

    func testEachRevealOpensOnce() {
        var opened = 0
        let reveal = MainWindowReveal(open: { opened += 1 }, bringForward: { false })
        reveal.reveal()
        reveal.reveal()
        XCTAssertEqual(opened, 2)
    }
}
