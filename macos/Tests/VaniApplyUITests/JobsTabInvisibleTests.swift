import AppKit
import XCTest

/// The Jobs tab host must not show itself or take the keyboard: these tests never skip for want of
/// accessibility trust (they never drive a control), so a run on any machine checks them. A host that
/// became frontmost, or drew an opaque window, would steal the screen from whoever runs `swift test`.
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

        // The host may be handed activation at launch and gives it back at once: let that settle, then it must hold.
        Thread.sleep(forTimeInterval: 0.5)
        var v = try s.visibility()
        XCTAssertEqual(v.alpha, 0, "the Jobs window must be fully transparent")
        XCTAssertTrue(v.onScreen, "the window is ordered in (accessibility and AppKit need it), only transparent")
        XCTAssertFalse(v.appActive, "the host must never be made the active app")
        XCTAssertFalse(v.frontmostIsHost, "the host must never be the frontmost app")
        XCTAssertEqual(v.policy, NSApplication.ActivationPolicy.accessory.rawValue, "no Dock icon or menu bar")
        XCTAssertNotEqual(NSWorkspace.shared.frontmostApplication?.processIdentifier, s.hostPID)

        // `unkey` brings up the second window the key tests use: it is transparent too.
        _ = try s.isKey(unkey: true)
        Thread.sleep(forTimeInterval: 0.5)
        v = try s.visibility()
        XCTAssertEqual(v.helperAlpha, 0, "the helper window must be fully transparent")
        XCTAssertFalse(v.appActive)
        XCTAssertFalse(v.frontmostIsHost)
    }
}
