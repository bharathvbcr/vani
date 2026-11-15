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
/// `STATE <json>` with the model's fields), `click X Y` (accessibility
/// coordinates: global, top-left origin), a mouse click this process sends
/// itself, and `scroll PANE Y|end` and `offset PANE` (PANE is `list` or
/// `detail`; replies `OFFSET <points from the top>`, or `NOPANE PANE` while that pane is not on screen), which move and read the
/// pane's AppKit scroll view as a trackpad would. `key` replies `KEY true|false`,
/// whether the Jobs window is key, and `unkey` makes another host window key in
/// its place, then replies the same. Nothing is posted to the
/// window server, so the user's cursor and keyboard are never touched.
///
/// The Jobs window is key only when this host's app could become active at
/// launch, which macOS allows only while no other app holds the user's focus.
/// A click on a non-key window is spent bringing it forward: SwiftUI controls
/// take it anyway (`allowsWindowActivationEvents`), but a List row, an AppKit
/// table row, does not. So a test opens a row by selecting it through
/// accessibility (`JobsTabSession.select`), never by clicking it.
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
    /// The window `unkey` makes key in place of the Jobs window.
    private var helper: NSWindow?
    private var activationObserver: NSObjectProtocol?
    private var buffer = Data()

    init(vani: URL) {
        // The fixture's own (absent) state folder and config, so the model's list-input stamps never
        // read the owner's live App Support state.
        let dir = vani.deletingLastPathComponent()
        let stateDir = dir.appendingPathComponent("state").path, config = dir.appendingPathComponent("config.json").path
        model = JobsModel { JobsClient(vaniBinary: vani, configPath: config, stateDir: stateDir, timeout: 10) }
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
        Self.makeInvisible(window)
        if ProcessInfo.processInfo.environment["VANI_UI_HOST_VISIBLE"] != "1" {
            // macOS lets a process launched by the frontmost app take activation (seen: 2 of 5 runs from a
            // frontmost parent), and an active host holds the keyboard even with every window transparent.
            // Whenever the host is made active it gives that back at once; the tests do not need it (a click
            // reaches a window that is not key, and one test forces the non-key state anyway).
            activationObserver = NotificationCenter.default.addObserver(
                forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main) { _ in
                    NSApplication.shared.deactivate()
            }
        }
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
        case "key":
            print("KEY \(window.isKeyWindow)")
        case "visibility":
            print("VISIBILITY " + visibilityJSON())
        case "unkey":
            // Another window of this host takes key, as another app's would: the Jobs window is then
            // not key, and AppKit spends a click on it on bringing it forward.
            let other = helper ?? NSWindow(contentRect: NSRect(x: 0, y: 0, width: 120, height: 80),
                                           styleMask: [.titled], backing: .buffered, defer: false)
            other.isReleasedWhenClosed = false
            helper = other
            Self.makeInvisible(other)
            other.orderFrontRegardless()
            other.makeKey()
            print("KEY \(window.isKeyWindow)")
        case "click":
            guard parts.count == 3, let x = Double(parts[1]), let y = Double(parts[2]), x.isFinite, y.isFinite else {
                print("ERROR bad click: \(line.prefix(80))")
                return
            }
            click(atAccessibilityPoint: CGPoint(x: x, y: y))
        case "scroll":
            guard parts.count == 3, Self.panes.contains(parts[1]) else {
                print("ERROR bad scroll: \(line.prefix(80))")
                return
            }
            guard let sv = scrollView(parts[1]) else {
                print("NOPANE \(parts[1])")
                return
            }
            if parts[2] == "end" {
                scroll(sv, to: .greatestFiniteMagnitude)
            } else if let y = Double(parts[2]), y.isFinite {
                scroll(sv, to: CGFloat(y))
            } else {
                print("ERROR bad scroll offset: \(line.prefix(80))")
                return
            }
            print("OFFSET \(offset(sv))")
        case "offset":
            guard parts.count == 2, Self.panes.contains(parts[1]) else {
                print("ERROR bad offset: \(line.prefix(80))")
                return
            }
            guard let sv = scrollView(parts[1]) else {
                print("NOPANE \(parts[1])")
                return
            }
            print("OFFSET \(offset(sv))")
        default:
            print("ERROR unknown command: \(line.prefix(80))")
        }
    }

    /// The host's windows are for the accessibility API and AppKit, never for the person at the machine:
    /// fully transparent, shadowless, out of window cycling and Spaces switching, and (with the
    /// `.accessory` policy and no activation) never frontmost. `VANI_UI_HOST_VISIBLE=1` shows them,
    /// to watch a run.
    static func makeInvisible(_ w: NSWindow) {
        if ProcessInfo.processInfo.environment["VANI_UI_HOST_VISIBLE"] == "1" { return }
        w.alphaValue = 0
        w.hasShadow = false
        w.isOpaque = false
        w.backgroundColor = .clear
        w.collectionBehavior = [.stationary, .ignoresCycle, .transient, .fullScreenAuxiliary]
        w.isExcludedFromWindowsMenu = true
    }

    private func visibilityJSON() -> String {
        let s: [String: Any] = [
            "alpha": window.alphaValue,
            "helperAlpha": helper?.alphaValue ?? NSNull(),
            "appActive": NSApplication.shared.isActive,
            "policy": NSApplication.shared.activationPolicy().rawValue,
            "onScreen": window.isVisible,
            "frontmostIsHost": NSWorkspace.shared.frontmostApplication?.processIdentifier == getpid(),
        ]
        let data = (try? JSONSerialization.data(withJSONObject: s, options: [.sortedKeys])) ?? Data("{}".utf8)
        return String(decoding: data, as: UTF8.self)
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

    static let maxViews = 6000
    static let panes: Set<String> = ["list", "detail"]

    /// `list`: the job list's table. `detail`: the tallest other vertical
    /// scroll view, the detail pane once a role is open.
    private func scrollView(_ pane: String) -> NSScrollView? {
        var all: [NSScrollView] = [], stack: [NSView] = window.contentView.map { [$0] } ?? [], budget = Self.maxViews
        while let v = stack.popLast(), budget > 0 {
            budget -= 1
            if let sv = v as? NSScrollView, sv.documentView != nil { all.append(sv) }
            stack.append(contentsOf: v.subviews)
        }
        switch pane {
        case "list": return all.first { $0.documentView is NSTableView }
        case "detail":
            return all.filter { !($0.documentView is NSTableView) && $0.hasVerticalScroller }
                .max { ($0.documentView?.frame.height ?? 0) < ($1.documentView?.frame.height ?? 0) }
        default: return nil
        }
    }

    /// Points scrolled from the top.
    private func offset(_ sv: NSScrollView) -> CGFloat {
        guard let doc = sv.documentView else { return 0 }
        let clip = sv.contentView.bounds
        return doc.isFlipped ? clip.minY : doc.frame.height - clip.maxY
    }

    private func scroll(_ sv: NSScrollView, to y: CGFloat) {
        guard let doc = sv.documentView else { return }
        let clip = sv.contentView.bounds
        let maxY = max(0, doc.frame.height - clip.height)
        let target = min(max(y, 0), maxY)
        sv.contentView.scroll(to: NSPoint(x: clip.minX, y: doc.isFlipped ? target : maxY - target))
        sv.reflectScrolledClipView(sv.contentView)
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
