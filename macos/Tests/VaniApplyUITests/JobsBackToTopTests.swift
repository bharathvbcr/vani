import ApplicationServices
import XCTest

/// The Back to top buttons of the job list and the detail pane, driven in a
/// host process (see JobsTabHost): the host scrolls a pane as a trackpad
/// would, the button is found and pressed through the accessibility API, and
/// the pane's scroll offset is read back.
final class JobsBackToTopTests: XCTestCase {
    static let fillers = 60
    /// Long enough for the fade in or out to finish.
    static let settle: TimeInterval = 0.6

    private var session: JobsTabSession?

    override func tearDown() {
        session?.close()
        session = nil
        super.tearDown()
    }

    private func start() throws -> JobsTabSession {
        let s = try JobsTabSession(fixture: JobsFixture(self, fillers: Self.fillers))
        session = s
        try s.waitForState("the long list") { $0.jobs.count == JobsFixture.roles.count + Self.fillers }
        return s
    }

    private func buttons(_ s: JobsTabSession) -> [AX] {
        s.elements { $0.role == kAXButtonRole && $0.label == "Back to top" }
    }

    private func waitForNoButton(_ s: JobsTabSession, _ what: String) throws {
        let deadline = Date().addingTimeInterval(JobsTabSession.waitTimeout)
        while !buttons(s).isEmpty {
            guard Date() < deadline else { throw JobsTabSession.Failure(description: "a Back to top button stayed \(what)") }
            Thread.sleep(forTimeInterval: 0.1)
        }
    }

    func testListButtonAppearsOnceScrolledAndReturnsToTheFirstRow() throws {
        let s = try start()
        _ = try s.element("the job list") { $0.role == kAXOutlineRole || $0.role == kAXTableRole }
        Thread.sleep(forTimeInterval: Self.settle)
        XCTAssertEqual(try s.offset(.list), 0, accuracy: 1, "the list opens at its top")
        XCTAssertTrue(buttons(s).isEmpty, "at the top there is no button")

        let scrolled = try s.scroll(.list, to: nil)
        XCTAssertGreaterThan(scrolled, 200, "the list really scrolled")
        let button = try s.element("the list's Back to top button") { $0.role == kAXButtonRole && $0.label == "Back to top" }
        try s.press(button, "Back to top")
        try s.waitForOffset(.list, "its top") { $0 <= 1 }
        try waitForNoButton(s, "with the list back at its top")
        XCTAssertNil(try s.state().selection, "scrolling back selects nothing")
    }

    /// Opens the Filler Role 03 row as VoiceOver would (a click on a row selects only while the
    /// window is key, which the host cannot count on: JobsTabHost).
    private func openFiller03(_ s: JobsTabSession) throws -> String {
        let key = "fx:filler:03"
        let list = try s.element("the job list") { $0.role == kAXOutlineRole || $0.role == kAXTableRole }
        let text = try XCTUnwrap(list.all(limit: 2000) { $0.role == kAXStaticTextRole && (($0.attribute(kAXValueAttribute) as? String)?.hasPrefix("Filler Role 03,") ?? false) }.first,
                                 "no row for \(key)")
        let row = try XCTUnwrap(text.ancestor(role: kAXRowRole), "the Filler Role 03 text is in no row")
        try s.select(row, "the Filler Role 03 row")
        try s.waitForState("\(key) open") { $0.selection == key }
        return key
    }

    /// The flake this guards against: another app held the user's focus, the host's window was not
    /// key, and the click that was to open a row only brought the window forward.
    func testARowOpensWhileTheWindowIsNotKey() throws {
        let s = try start()
        XCTAssertFalse(try s.isKey(unkey: true), "another host window took key")
        _ = try openFiller03(s)
        XCTAssertFalse(try s.isKey(), "opening the row did not depend on taking key back")
    }

    func testDetailButtonAppearsOnceTheHeaderScrollsOffAndReturnsToTheTop() throws {
        let s = try start()
        let key = try openFiller03(s)
        // The pane appears once the role opens, and its long description arrives with `jobs show`:
        // wait until it is on screen and can scroll.
        let deadline = Date().addingTimeInterval(JobsTabSession.waitTimeout)
        while try (s.scrollIfShown(.detail, to: nil) ?? 0) < 400 {
            guard Date() < deadline else { throw JobsTabSession.Failure(description: "the detail pane never grew long enough to scroll") }
            Thread.sleep(forTimeInterval: 0.1)
        }

        // A short scroll keeps the header partly in view: no button.
        XCTAssertEqual(try s.scroll(.detail, to: 40), 40, accuracy: 1)
        Thread.sleep(forTimeInterval: Self.settle)
        XCTAssertTrue(buttons(s).isEmpty, "the header is still on screen at 40 pt")

        try s.scroll(.detail, to: nil)
        let button = try s.element("the detail's Back to top button") { $0.role == kAXButtonRole && $0.label == "Back to top" }
        try s.press(button, "Back to top")
        try s.waitForOffset(.detail, "its top") { $0 <= 1 }
        try waitForNoButton(s, "with the detail back at its top")
        XCTAssertEqual(try s.state().selection, key, "the same role stays open")
    }
}
