import SwiftUI
import XCTest
@testable import VaniApply
import VaniApplyKit

final class ResumeViewsTests: XCTestCase {
    func testTheShortcutDoesNotCollideWithRetriageNorTheKnownJobsShortcuts() {
        let mine = ResumeShortcut.keyboardShortcut
        XCTAssertNotEqual(mine, RetriageShortcut.keyboardShortcut)
        XCTAssertEqual(ResumeShortcut.label, "⌥⌘B")
        // Every shortcut the app binds today (rg 'keyboardShortcut' macos/Sources/VaniApply, 2026-10-25).
        let taken: [KeyboardShortcut] = [
            KeyboardShortcut("i"), KeyboardShortcut("r"), KeyboardShortcut("o"), KeyboardShortcut("s"),
            KeyboardShortcut("a", modifiers: [.command, .shift]), KeyboardShortcut(.delete),
            KeyboardShortcut("c", modifiers: [.command, .shift]), KeyboardShortcut("r", modifiers: [.command, .shift]),
            RetriageShortcut.keyboardShortcut,
        ]
        XCTAssertFalse(taken.contains(mine))
    }
}
