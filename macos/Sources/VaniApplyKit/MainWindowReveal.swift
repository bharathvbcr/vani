import Foundation

/// How a notification click brings the main window up. The app keeps running
/// after its window is closed, so it can announce runs, and a click must then
/// reopen the window rather than look for one to bring forward.
public struct MainWindowReveal {
    /// What the reveal did.
    public enum Outcome: Equatable, Sendable {
        /// The window scene was opened, or brought forward if it was open.
        case opened
        /// No opener was captured yet; an open window was brought forward.
        case broughtForward
        /// No opener, and no open window to bring forward.
        case noWindow
    }

    /// SwiftUI's openWindow(id:) for the main scene, captured from a view once
    /// one has appeared. It reopens a closed `Window` scene and brings an open
    /// one forward.
    public var open: (() -> Void)?
    /// Brings an already open window forward; false when there is none.
    public var bringForward: () -> Bool

    public init(open: (() -> Void)?, bringForward: @escaping () -> Bool) {
        self.open = open
        self.bringForward = bringForward
    }

    /// Prefers the scene opener, which works whether or not the window is open.
    /// Without one (a click that launched the app, before any view appeared)
    /// it falls back to the window SwiftUI is opening at launch.
    @discardableResult
    public func reveal() -> Outcome {
        if let open {
            open()
            return .opened
        }
        return bringForward() ? .broughtForward : .noWindow
    }
}
