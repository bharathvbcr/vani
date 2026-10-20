import AppKit
import ApplicationServices
import XCTest

/// The driver side: starts `JobsTabHost` as a second process over a fixture,
/// finds the Jobs tab's controls through the accessibility API, presses them
/// (AXPress, as VoiceOver or XCUITest would) or clicks them (a click the host
/// sends itself, at the control's accessibility frame), and reads the host's
/// `JobsModel` back. Every wait has a deadline; a failure carries the last
/// model state and the accessibility tree it searched.
final class JobsTabSession {
    struct State: Decodable, Equatable {
        var sort: String
        var fits: [String]
        var selection: String?
        var showInsights: Bool
        var jobs: [String]
        var stats: Bool
        var loading: Bool
        var error: String?
    }

    struct Failure: Error, CustomStringConvertible {
        var description: String
    }

    static let launchTimeout: TimeInterval = 30
    static let waitTimeout: TimeInterval = 10
    static let maxNodes = 4000

    let fixture: JobsFixture
    private let process: Process
    private let input: FileHandle
    private let lines: LineReader
    private let app: AXUIElement

    /// Starts the host, or skips when this runner may not use the
    /// accessibility API (a skip, never a pass).
    init(fixture: JobsFixture) throws {
        guard #available(macOS 15, *) else {
            throw XCTSkip("the host needs macOS 15 for allowsWindowActivationEvents, so a click reaches a window that is not key")
        }
        guard AXIsProcessTrusted() else {
            throw XCTSkip("the test runner is not trusted for accessibility (System Settings › Privacy & Security › Accessibility), so the Jobs tab's controls cannot be driven")
        }
        self.fixture = fixture
        let p = Process()
        p.executableURL = URL(fileURLWithPath: ProcessInfo.processInfo.arguments[0])
        p.arguments = ["-XCTest", "VaniApplyUITests.JobsTabHost/testServe", Bundle(for: JobsTabHost.self).bundlePath]
        var env = ProcessInfo.processInfo.environment
        env[JobsTabHost.fixtureKey] = fixture.dir.path
        p.environment = env
        let inPipe = Pipe(), outPipe = Pipe()
        p.standardInput = inPipe
        p.standardOutput = outPipe
        try p.run()
        process = p
        input = inPipe.fileHandleForWriting
        lines = LineReader(outPipe.fileHandleForReading)
        app = AXUIElementCreateApplication(p.processIdentifier)
        AXUIElementSetMessagingTimeout(app, 5)
        guard lines.next(prefix: "READY", timeout: Self.launchTimeout) != nil else {
            close()
            throw Failure(description: "the host did not start within \(Int(Self.launchTimeout)) s")
        }
    }

    /// Ends the host: closing stdin makes it exit; a host that does not is killed.
    func close() {
        try? input.close()
        let deadline = Date().addingTimeInterval(5)
        while process.isRunning, Date() < deadline { Thread.sleep(forTimeInterval: 0.05) }
        if process.isRunning { process.terminate() }
    }

    // MARK: Model

    func state() throws -> State {
        send("state")
        guard let line = lines.next(prefix: "STATE ", timeout: Self.waitTimeout) else {
            throw Failure(description: "the host did not report its state")
        }
        return try JSONDecoder().decode(State.self, from: Data(line.dropFirst("STATE ".count).utf8))
    }

    /// Polls the model until `ok` holds, failing with the last state seen.
    @discardableResult
    func waitForState(_ what: String, timeout: TimeInterval = waitTimeout, _ ok: (State) -> Bool) throws -> State {
        let deadline = Date().addingTimeInterval(timeout)
        var last = try state()
        while !ok(last) {
            guard Date() < deadline else {
                throw Failure(description: "the model never reached \(what) within \(Int(timeout)) s; last state: \(last)")
            }
            Thread.sleep(forTimeInterval: 0.1)
            last = try state()
        }
        return last
    }

    // MARK: Scrolling

    enum Pane: String { case list, detail }

    /// Scrolls `pane` to `y` points from its top (nil: to its end) and returns
    /// where it landed.
    @discardableResult
    func scroll(_ pane: Pane, to y: Double?) throws -> Double {
        send("scroll \(pane.rawValue) " + (y.map { String($0) } ?? "end"))
        return try readOffset("scroll \(pane.rawValue)")
    }

    /// Points `pane` is scrolled from its top.
    func offset(_ pane: Pane) throws -> Double {
        send("offset \(pane.rawValue)")
        return try readOffset("offset \(pane.rawValue)")
    }

    private func readOffset(_ what: String) throws -> Double {
        guard let line = lines.next(prefix: "OFFSET ", timeout: Self.waitTimeout),
              let y = Double(line.dropFirst("OFFSET ".count)) else {
            throw Failure(description: "the host did not answer \(what) (no such pane yet?)")
        }
        return y
    }

    /// Polls `pane`'s offset until `ok` holds, failing with the last one seen.
    func waitForOffset(_ pane: Pane, _ what: String, _ ok: (Double) -> Bool) throws {
        let deadline = Date().addingTimeInterval(Self.waitTimeout)
        var last = try offset(pane)
        while !ok(last) {
            guard Date() < deadline else {
                throw Failure(description: "the \(pane.rawValue) never reached \(what) within \(Int(Self.waitTimeout)) s; last offset: \(last)")
            }
            Thread.sleep(forTimeInterval: 0.1)
            last = try offset(pane)
        }
    }

    /// The `vani` argument lists the fake recorded, in order.
    func vaniCalls() -> [String] { fixture.calls() }

    // MARK: Accessibility

    /// Polls the host's accessibility tree for the first element `match`
    /// accepts, failing with the tree when none appears.
    func element(_ what: String, timeout: TimeInterval = waitTimeout, _ match: (AX) -> Bool) throws -> AX {
        let deadline = Date().addingTimeInterval(timeout)
        while true {
            if let found = AX(app).first(limit: Self.maxNodes, match) { return found }
            guard Date() < deadline else {
                throw Failure(description: "no \(what) appeared within \(Int(timeout)) s in:\n" + AX(app).dump(limit: 200))
            }
            Thread.sleep(forTimeInterval: 0.1)
        }
    }

    func elements(_ match: (AX) -> Bool) -> [AX] { AX(app).all(limit: Self.maxNodes, match) }

    func press(_ el: AX, _ what: String) throws {
        let err = AXUIElementPerformAction(el.ref, kAXPressAction as CFString)
        guard err == .success else { throw Failure(description: "AXPress on \(what) failed: \(err.rawValue)") }
    }

    /// Clicks the middle of `el` (a mouse down and up the host sends itself).
    func click(_ el: AX, _ what: String) throws {
        guard let f = el.frame, f.width > 0, f.height > 0 else { throw Failure(description: "\(what) has no frame") }
        try click(at: CGPoint(x: f.midX, y: f.midY))
    }

    func click(at p: CGPoint) throws {
        send("click \(p.x) \(p.y)")
        guard lines.next(prefix: "CLICKED", timeout: Self.waitTimeout) != nil else {
            throw Failure(description: "the host did not confirm a click at \(p)")
        }
    }

    private func send(_ line: String) {
        input.write(Data((line + "\n").utf8))
    }
}

/// One accessibility element of the host, read with bounded walks.
struct AX {
    let ref: AXUIElement
    init(_ ref: AXUIElement) { self.ref = ref }

    func attribute(_ name: String) -> AnyObject? {
        var v: AnyObject?
        return AXUIElementCopyAttributeValue(ref, name as CFString, &v) == .success ? v : nil
    }

    var role: String { attribute(kAXRoleAttribute) as? String ?? "" }
    var title: String { attribute(kAXTitleAttribute) as? String ?? "" }
    var label: String { attribute(kAXDescriptionAttribute) as? String ?? "" }
    var identifier: String { attribute(kAXIdentifierAttribute) as? String ?? "" }
    var children: [AX] { ((attribute(kAXChildrenAttribute) as? [AXUIElement]) ?? []).map(AX.init) }

    var frame: CGRect? {
        guard let pv = attribute(kAXPositionAttribute), let sv = attribute(kAXSizeAttribute),
              CFGetTypeID(pv) == AXValueGetTypeID(), CFGetTypeID(sv) == AXValueGetTypeID() else { return nil }
        var origin = CGPoint.zero, size = CGSize.zero
        // The type IDs were checked above, so these casts cannot fail.
        guard AXValueGetValue(pv as! AXValue, .cgPoint, &origin), AXValueGetValue(sv as! AXValue, .cgSize, &size) else { return nil }
        return CGRect(origin: origin, size: size)
    }

    func first(limit: Int, _ match: (AX) -> Bool) -> AX? {
        var budget = limit
        return first(&budget, match)
    }

    private func first(_ budget: inout Int, _ match: (AX) -> Bool) -> AX? {
        guard budget > 0 else { return nil }
        budget -= 1
        if match(self) { return self }
        for c in children {
            if let f = c.first(&budget, match) { return f }
        }
        return nil
    }

    func all(limit: Int, _ match: (AX) -> Bool) -> [AX] {
        var out: [AX] = [], budget = limit
        collect(&budget, match, into: &out)
        return out
    }

    private func collect(_ budget: inout Int, _ match: (AX) -> Bool, into out: inout [AX]) {
        guard budget > 0 else { return }
        budget -= 1
        if match(self) { out.append(self) }
        for c in children { c.collect(&budget, match, into: &out) }
    }

    func dump(limit: Int) -> String {
        var out: [String] = [], budget = limit
        dump(depth: 0, &budget, into: &out)
        return out.joined(separator: "\n")
    }

    /// The error reading the children, if any: an element whose children
    /// cannot be read must not look like one that has none.
    var childrenError: AXError? {
        var v: AnyObject?
        let err = AXUIElementCopyAttributeValue(ref, kAXChildrenAttribute as CFString, &v)
        return err == .success || err == .noValue ? nil : err
    }

    private func dump(depth: Int, _ budget: inout Int, into out: inout [String]) {
        guard budget > 0 else { return }
        budget -= 1
        let err = childrenError.map { " (children unreadable: AXError \($0.rawValue))" } ?? ""
        out.append(String(repeating: "  ", count: depth) + "\(role) title=\(title) desc=\(label) id=\(identifier)" + err)
        for c in children { c.dump(depth: depth + 1, &budget, into: &out) }
    }
}

/// Lines from a pipe, each awaited with a deadline.
final class LineReader: @unchecked Sendable {
    static let maxLine = 1 << 20
    private var lines: [String] = []
    private var partial = Data()
    private let cond = NSCondition()

    init(_ h: FileHandle) {
        h.readabilityHandler = { [weak self] h in
            let d = h.availableData
            if d.isEmpty { h.readabilityHandler = nil }
            self?.append(d)
        }
    }

    private func append(_ d: Data) {
        cond.lock()
        partial.append(d)
        while let nl = partial.firstIndex(of: 0x0A) {
            lines.append(String(decoding: partial[..<nl], as: UTF8.self))
            partial.removeSubrange(...nl)
        }
        if partial.count > Self.maxLine { partial.removeAll() }
        cond.broadcast()
        cond.unlock()
    }

    /// The next line starting with `prefix` (earlier lines are dropped), or nil at the deadline.
    func next(prefix: String, timeout: TimeInterval) -> String? {
        let deadline = Date().addingTimeInterval(timeout)
        cond.lock()
        defer { cond.unlock() }
        while true {
            if let i = lines.firstIndex(where: { $0.hasPrefix(prefix) }) {
                let line = lines[i]
                lines.removeSubrange(...i)
                return line
            }
            if !cond.wait(until: deadline) { return nil }
        }
    }
}
