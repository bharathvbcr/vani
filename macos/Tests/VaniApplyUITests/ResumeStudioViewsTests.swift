import AppKit
import Network
import SwiftUI
import WebKit
import XCTest
@testable import VaniApply
import VaniApplyKit

/// The Studio window drawn, and its two AppKit guards checked against the real thing: the preview web view
/// against a local server that counts what reaches it, the close guard against a real window. Nothing here
/// needs accessibility trust. Pages and names are made up.
@MainActor
final class ResumeStudioViewsTests: StudioFakeCase {
    // MARK: ImageRenderer smoke

    /// Draw the window's main states. `ImageRenderer` cannot draw PDFKit or WebKit, so the preview pane's
    /// content is a blank there; the outline, the bars and the banner are what these look at. Set
    /// VANI_STUDIO_PNG_DIR to a folder to keep the images for a look.
    private func render(_ name: String, _ r: Rig) throws {
        let jobs = try XCTUnwrap(r.jobs)
        let view = ResumeStudioContent(editor: r.editor, closer: WindowCloser(), flat: true)
            .environment(jobs)
            .frame(width: 1300, height: 820)
            .background(Color(nsColor: .windowBackgroundColor))
        let renderer = ImageRenderer(content: view)
        renderer.scale = 1
        let image = try XCTUnwrap(renderer.nsImage, name)
        XCTAssertGreaterThan(image.size.width, 100, name)
        XCTAssertGreaterThan(image.size.height, 100, name)
        if let dir = ProcessInfo.processInfo.environment["VANI_STUDIO_PNG_DIR"],
           let tiff = image.tiffRepresentation, let png = NSBitmapImageRep(data: tiff)?.representation(using: .png, properties: [:]) {
            try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("studio-\(name).png"))
        }
    }

    func testTheMainStatesDraw() async throws {
        // Loaded, a fitted version.
        let a = try rig(viaJobs: true)
        await a.editor.open()
        try render("loaded", a)

        // An edit the page cannot take, marked on its row, and the entry editor open.
        a.editor.setOverride(.lead, "Doubled it 2x", entry: "projA")
        a.editor.setIncluded("j3", false, in: .entry("job1"))
        try render("marked", a)

        // An unfitted preview: the banner, the checks, what rkb said.
        let b = try rig(viaJobs: true)
        await b.editor.open()
        b.editor.setIncluded("j3", false, in: .entry("job1"))
        let file = try previewFile(b)
        try b.put("preview.json", rendered(path: file.path))
        await b.editor.preview()
        XCTAssertNotNil(b.editor.html)
        try render("unfitted", b)

        // A fit that Go refused, on the row and in the status band.
        try b.put("fit.json", #"{"error":"jobs resume: bullet \"j2\" is not available for this page","code":"ineligible"}"#)
        try b.touch("fit.fail")
        await b.editor.fit()
        XCTAssertNotNil(b.editor.failure)
        try render("refused", b)

        // A job with no version, the base as the draft.
        let c = try rig(versions: false, viaJobs: true)
        await c.editor.open()
        try render("noversion", c)

        // No pool: the version shows, editing is paused and says so.
        let d = try rig(pool: false, checkError: "rkb could not be run", viaJobs: true)
        await d.editor.open()
        try render("nopool", d)

        // A show that failed.
        let e = try rig(viaJobs: true)
        try e.put("show.json", #"{"error":"the ledger is locked","code":"ledger"}"#)
        try e.touch("show.fail")
        await e.editor.open()
        try render("loadfailed", e)

        // A fit running: the progress, Stop.
        let f = try rig(viaJobs: true)
        await f.editor.open()
        f.editor.setIncluded("j3", false, in: .entry("job1"))
        try f.touch("slow")
        let running = Task { await f.editor.fit() }
        try await eventually("the fit to start") { f.editor.isFitting }
        try render("fitting", f)
        f.editor.stop()
        await running.value
    }

    private func eventually(_ what: String, _ done: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(10)
        while !done() {
            if Date() > deadline { return XCTFail("timed out waiting for \(what)") }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    // MARK: The preview web view

    /// Accepts TCP connections on a loopback port and counts the ones that send a request: whatever a page loads
    /// from here is a request, so a count of zero says no request reached the network. (A bare connection with
    /// nothing sent, which WebKit may open to a link it is about to follow, is counted apart in `connections`.)
    private final class Server: @unchecked Sendable {
        private let listener: NWListener
        private let lock = NSLock()
        private var requests = 0
        private var opened = 0
        private(set) var port: UInt16 = 0

        var count: Int { lock.lock(); defer { lock.unlock() }; return requests }
        var connections: Int { lock.lock(); defer { lock.unlock() }; return opened }

        init() throws {
            listener = try NWListener(using: .tcp, on: .any)
            let ready = DispatchSemaphore(value: 0)
            listener.newConnectionHandler = { [weak self] conn in
                self?.lock.lock(); self?.opened += 1; self?.lock.unlock()
                conn.start(queue: .global())
                conn.receive(minimumIncompleteLength: 1, maximumLength: 4096) { data, _, _, _ in
                    if let data, !data.isEmpty { self?.lock.lock(); self?.requests += 1; self?.lock.unlock() }
                    conn.cancel()
                }
            }
            listener.stateUpdateHandler = { _ in ready.signal() }
            listener.start(queue: .global())
            _ = ready.wait(timeout: .now() + 5)
            port = listener.port?.rawValue ?? 0
        }

        deinit { listener.cancel() }
    }

    private func pageHitting(_ port: UInt16) -> String {
        let base = "http://127.0.0.1:\(port)"
        return """
        <!doctype html><html><head><title>plain</title>
        <link rel="stylesheet" href="\(base)/style.css">
        <script>document.title = 'script ran'; new Image().src = '\(base)/script-beacon';</script></head>
        <body><p id="text">made-up resume text</p>
        <img src="\(base)/img.png"><iframe src="\(base)/frame.html"></iframe>
        <a id="link" href="\(base)/clicked">a link</a>
        <form id="form" action="\(base)/form" method="get"><input name="q"></form></body></html>
        """
    }

    /// Load `page` and wait until the web view is done and a moment more, for subresources to be asked for.
    private func load(_ page: String, in web: WKWebView, using guardObject: ResumeHTMLGuard?) async throws -> Bool {
        if let guardObject { guardObject.load(page, id: 1, into: web) } else { web.loadHTMLString(page, baseURL: nil) }
        // Done when the document says so (WebKit starts its content process on the first load, which can take
        // a while), then a moment more for the subresources a page would ask for.
        let deadline = Date().addingTimeInterval(20)
        while Date() < deadline {
            try await Task.sleep(nanoseconds: 100_000_000)
            if !web.isLoading, let state = try? await web.evaluateJavaScript("document.readyState") as? String, state == "complete" { break }
        }
        guard !web.isLoading else { return false }
        try await Task.sleep(nanoseconds: 1_000_000_000)
        return true
    }

    /// A plain web view with scripts on, loading the page: how many connections the page makes.
    private func controlHits() async throws -> Int? {
        let server = try Server()
        XCTAssertNotEqual(server.port, 0, "the counting server must be listening")
        let control = WKWebView(frame: NSRect(x: 0, y: 0, width: 600, height: 800))
        guard try await load(pageHitting(server.port), in: control, using: nil) else { return nil }
        control.stopLoading()
        return server.count
    }

    func testThePreviewWebViewRunsNoScriptAndReachesNoNetworkAndFollowsNoLink() async throws {
        // Control: a plain web view does reach a server, so a count of zero below means something.
        guard let reached = try await controlHits() else {
            throw XCTSkip("WebKit could not load a page in this test process, so the web view's guard was not exercised")
        }
        guard reached > 0 else { throw XCTSkip("the control page reached no server in this test process, so the web view's guard was not exercised") }

        let server = try Server()
        XCTAssertNotEqual(server.port, 0)
        let guardObject = ResumeHTMLGuard()
        let web = ResumeHTMLGuard.makeWebView()
        web.frame = NSRect(x: 0, y: 0, width: 600, height: 800)
        web.navigationDelegate = guardObject
        web.uiDelegate = guardObject
        guard try await load(pageHitting(server.port), in: web, using: guardObject) else { return XCTFail("the page did not load") }

        let text = try await web.evaluateJavaScript("document.getElementById('text') ? document.getElementById('text').innerText : ''") as? String
        XCTAssertEqual(text, "made-up resume text", "the page itself is shown")
        let title = try await web.evaluateJavaScript("document.title") as? String
        XCTAssertEqual(title, "plain", "the page's own script did not run")
        XCTAssertEqual(server.count, 0, "nothing the page asked for (style, script beacon, image, frame) reached the network")

        let url = web.url
        _ = try? await web.evaluateJavaScript("document.getElementById('link').click(); 1")
        try await Task.sleep(nanoseconds: 700_000_000)
        XCTAssertEqual(guardObject.decisions.first, "allow -1 about", "the page's own load is the one thing allowed")
        XCTAssertEqual(guardObject.decisions.filter { $0.hasPrefix("allow") }.count, 1, "\(guardObject.decisions)")
        XCTAssertTrue(guardObject.decisions.contains("cancel 0 http"), "the link was refused: \(guardObject.decisions)")
        XCTAssertEqual(web.url, url, "a link does not navigate")
        XCTAssertEqual(server.count, 0, "and reaches nothing")
        _ = try? await web.evaluateJavaScript("document.getElementById('form').submit(); 1")
        try await Task.sleep(nanoseconds: 700_000_000)
        XCTAssertEqual(web.url, url, "a form does not navigate")
        XCTAssertEqual(server.count, 0, "and reaches nothing")
    }

    func testTheBlockRuleIsValidAndTheViewIsNotPersistent() async throws {
        let list = try await WKContentRuleListStore.default().compileContentRuleList(forIdentifier: "dev.vbcr.vani.test.resume-rules", encodedContentRuleList: ResumeHTMLGuard.blockRules)
        XCTAssertNotNil(list, "WebKit accepts the rule")
        let web = ResumeHTMLGuard.makeWebView()
        XCTAssertFalse(web.configuration.websiteDataStore.isPersistent)
        XCTAssertFalse(web.configuration.defaultWebpagePreferences.allowsContentJavaScript)
    }

    // MARK: The close guard

    private final class OriginalDelegate: NSObject, NSWindowDelegate {
        var shouldCloseAsked = 0
        var resized = 0
        func windowShouldClose(_ sender: NSWindow) -> Bool { shouldCloseAsked += 1; return true }
        func windowDidResize(_ notification: Notification) { resized += 1 }
    }

    func testTheCloseGuardAsksFirstKeepsTheWindowsOwnDelegateAndLetsAnApprovedCloseThrough() throws {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 200, height: 100), styleMask: [.titled, .closable], backing: .buffered, defer: true)
        window.isReleasedWhenClosed = false
        let original = OriginalDelegate()
        window.delegate = original
        let closer = WindowCloser()
        let proxy = WindowCloseGuard.CloseProxy()
        var asked = 0
        var answer = false
        proxy.shouldClose = { asked += 1; return answer }
        proxy.attach(to: window, closer: closer)
        XCTAssertTrue(window.delegate === proxy)
        XCTAssertTrue(closer.window === window)

        XCTAssertFalse(proxy.windowShouldClose(window), "unsaved edits: the close is held")
        XCTAssertEqual(asked, 1)
        XCTAssertEqual(original.shouldCloseAsked, 0, "the window's own delegate is not asked about a close that was refused")
        answer = true
        XCTAssertTrue(proxy.windowShouldClose(window))
        XCTAssertEqual(original.shouldCloseAsked, 1, "and is asked about one that was allowed")

        // What the window's own delegate answers is still answered by it.
        XCTAssertTrue(proxy.responds(to: #selector(NSWindowDelegate.windowDidResize(_:))))
        XCTAssertTrue(proxy.forwardingTarget(for: #selector(NSWindowDelegate.windowDidResize(_:))) as AnyObject === original, "forwarded")
        XCTAssertFalse(proxy.responds(to: #selector(NSWindowDelegate.windowWillReturnUndoManager(_:))), "what neither answers is not claimed")

        // Attaching again to the same window does not wrap the proxy in itself.
        proxy.attach(to: window, closer: closer)
        XCTAssertTrue(proxy.original === original)

        // Discard was chosen: the close goes through without asking again.
        answer = false
        closer.closeApproved()
        XCTAssertTrue(closer.approved)
        XCTAssertTrue(proxy.windowShouldClose(window))
        XCTAssertEqual(asked, 2, "the guard's question was not asked again")
    }
}
