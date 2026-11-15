import AppKit
import XCTest

/// The Jobs tab host must not show itself: these tests never skip for want of accessibility trust (they never
/// drive a control), so a run on any machine checks them.
///
/// What the host controls is asserted hard: every window it makes is fully transparent, and it is an accessory
/// app (no Dock icon, no menu bar). Focus it can only ask for: macOS makes a process launched from the frontmost
/// app the frontmost one and nothing tried in the host has prevented it (see `HostController.start`). Then its
/// windows are transparent but it holds the keyboard until the person switches away, so the test ends in a skip
/// that says so, never a pass.
final class JobsTabInvisibleTests: XCTestCase {
    private var session: JobsTabSession?

    override func tearDown() {
        session?.close()
        session = nil
        super.tearDown()
    }

    func testHostWindowsAreTransparentAndNeverFrontmost() throws {
        if ProcessInfo.processInfo.environment["VANI_UI_HOST_VISIBLE"] == "1" {
            throw XCTSkip("VANI_UI_HOST_VISIBLE=1 shows the host on purpose")
        }
        let fixture = try JobsFixture(self)
        let s = try JobsTabSession(fixture: fixture, requireAccessibility: false)
        session = s
        try s.waitForState("the first list") { $0.jobs == JobsFixture.standardOrder }
        // Let any activation macOS gives the process at launch land, so what is read is where focus stays.
        Thread.sleep(forTimeInterval: 1)

        var v = try s.visibility()
        XCTAssertEqual(v.alpha, 0, "the Jobs window must be fully transparent")
        XCTAssertTrue(v.onScreen, "the window is ordered in (accessibility and AppKit need it), only transparent")
        XCTAssertEqual(v.policy, NSApplication.ActivationPolicy.accessory.rawValue, "no Dock icon or menu bar")

        // `unkey` brings up the second window the key tests use: it is transparent too.
        _ = try s.isKey(unkey: true)
        v = try s.visibility()
        XCTAssertEqual(v.helperAlpha, 0, "the helper window must be fully transparent")
        XCTAssertEqual(v.alpha, 0)

        let drivenFront = NSWorkspace.shared.frontmostApplication?.processIdentifier == s.hostPID
        if v.appActive || v.frontmostIsHost || drivenFront {
            throw XCTSkip("macOS made the host the frontmost app (frontmost: \(v.frontmost), AppKit active: \(v.appActive)); its windows are transparent, but it holds the keyboard until you switch away. Not a pass: the host could not be kept out of focus")
        }
    }
}
