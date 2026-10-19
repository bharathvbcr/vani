import AppKit
import SwiftUI
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The host side of the Jobs tab UI tests: this test bundle, re-run by
/// `JobsTabSession` as a second process, shows the Jobs tab exactly as the app
/// builds it (`NavigationStack { JobsView() }` over a `JobsModel`) in a plain
/// window, fed by the fake `vani` in the fixture folder. It never starts the
/// app, so no Login Items agent is registered.
///
/// The driver reaches the controls through the accessibility API, which needs
/// a second process: an app does not answer AX requests about itself, and an
/// in-process window never becomes key. On stdin it takes `state` (replies
/// `STATE <json>` with the model's fields) and `click X Y` (accessibility
/// coordinates: global, top-left origin), a mouse click this process sends
/// itself. Nothing is posted to the window server, so the user's cursor and
/// keyboard are never touched.
final class JobsTabHost: XCTestCase {
    static let fixtureKey = "VANI_UI_HOST"

    @MainActor
    func testServe() throws {
        guard let dir = ProcessInfo.processInfo.environment[Self.fixtureKey] else {
            throw XCTSkip("runs only as the Jobs tab UI tests' host process")
        }
        setvbuf(stdout, nil, _IONBF, 0)
        let host = HostController(vani: URL(fileURLWithPath: dir).appendingPathComponent("vani"))
        host.start()
        // The driver closing stdin (or dying) ends the host; so does a stuck run.
        FileHandle.standardInput.readabilityHandler = { h in
            let data = h.availableData
            Task { @MainActor in host.feed(data) }
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + HostController.maxLifetime) {
            FileHandle.standardError.write(Data("host: lived past \(Int(HostController.maxLifetime)) s, exiting\n".utf8))
            exit(3)
        }
        NSApplication.shared.run()
    }
}

@MainActor
private final class HostController {
    static let maxLifetime: TimeInterval = 180
    static let maxLine = 4096

    let model: JobsModel
    let window: NSWindow
    private var buffer = Data()

    init(vani: URL) {
        model = JobsModel { JobsClient(vaniBinary: vani, timeout: 10) }
        window = NSWindow(contentRect: NSRect(x: 40, y: 40, width: 1200, height: 820),
                          styleMask: [.titled, .resizable, .closable, .miniaturizable], backing: .buffered, defer: false)
    }

    func start() {
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
        // The host's app is never made active (that would take the user's
        // keyboard), so its window is never key, and AppKit spends a click on
        // a non-key window on bringing it forward. In the app the window is
        // key once the user clicks it; this stands in for that first click
        // (JobsTabSession skips before macOS 15) and changes nothing else.
        guard #available(macOS 15, *) else { exit(4) }
        let view = NSHostingView(rootView: NavigationStack { JobsView() }.environment(model).allowsWindowActivationEvents(true))
        view.sceneBridgingOptions = [.toolbars, .title]
        window.isReleasedWhenClosed = false
        window.contentView = view
        window.setContentSize(NSSize(width: 1200, height: 820))
        window.orderFrontRegardless()
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { print("READY \(getpid())") }
    }

    func feed(_ data: Data) {
        if data.isEmpty { exit(0) }
        buffer.append(data)
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = String(decoding: buffer[..<nl], as: UTF8.self)
            buffer.removeSubrange(...nl)
            handle(line)
        }
        if buffer.count > Self.maxLine { buffer.removeAll() }
    }

    private func handle(_ line: String) {
        let parts = line.split(separator: " ").map(String.init)
        switch parts.first {
        case "state":
            print("STATE " + stateJSON())
        case "click":
            guard parts.count == 3, let x = Double(parts[1]), let y = Double(parts[2]), x.isFinite, y.isFinite else {
                print("ERROR bad click: \(line.prefix(80))")
                return
            }
            click(atAccessibilityPoint: CGPoint(x: x, y: y))
        default:
            print("ERROR unknown command: \(line.prefix(80))")
        }
    }

    private func stateJSON() -> String {
        let s: [String: Any] = [
            "sort": model.sort.rawValue,
            "fits": model.filter.fits.sorted(),
            "selection": model.selection ?? NSNull(),
            "showInsights": model.showInsights,
            "jobs": model.jobs.map(\.key),
            "stats": model.stats != nil,
            "loading": model.loading,
            "error": model.error ?? NSNull(),
        ]
        let data = (try? JSONSerialization.data(withJSONObject: s, options: [.sortedKeys])) ?? Data("{}".utf8)
        return String(decoding: data, as: UTF8.self)
    }

    /// Accessibility frames are global with a top-left origin on the primary
    /// screen; windows take Cocoa coordinates (bottom-left origin).
    private func click(atAccessibilityPoint p: CGPoint) {
        let primaryHeight = NSScreen.screens.first?.frame.height ?? 0
        let inWindow = window.convertPoint(fromScreen: NSPoint(x: p.x, y: primaryHeight - p.y))
        func event(_ type: NSEvent.EventType) -> NSEvent? {
            NSEvent.mouseEvent(with: type, location: inWindow, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
                               windowNumber: window.windowNumber, context: nil, eventNumber: 0, clickCount: 1,
                               pressure: type == .leftMouseDown ? 1 : 0)
        }
        guard let down = event(.leftMouseDown) else { print("ERROR no event"); return }
        window.sendEvent(down)
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [window] in
            if let up = event(.leftMouseUp) { window.sendEvent(up) }
            print("CLICKED")
        }
    }
}
