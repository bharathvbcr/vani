import AppKit
import PDFKit
import SwiftUI
import VaniApplyKit
import WebKit

// The Studio window's three AppKit pieces: the PDF a fitted version printed (PDFKit), the page rkb draws for
// an unfitted render (WebKit, shut in), and the close button's guard for unsaved edits.

/// A fitted version's PDF, exactly what Chrome printed.
struct ResumePDFView: NSViewRepresentable {
    let data: Data
    /// Changes when the document does, so the view is not rebuilt on every redraw.
    let token: String

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeNSView(context: Context) -> PDFView {
        let view = PDFView()
        view.autoScales = true
        view.displayMode = .singlePageContinuous
        view.displaysPageBreaks = true
        view.backgroundColor = .underPageBackgroundColor
        view.document = PDFDocument(data: data)
        context.coordinator.token = token
        return view
    }

    func updateNSView(_ view: PDFView, context: Context) {
        guard context.coordinator.token != token else { return }
        context.coordinator.token = token
        view.document = PDFDocument(data: data)
    }

    final class Coordinator { var token = "" }
}

/// rkb's screen HTML for an unfitted render, shown with nothing it could do: no script, no network (a content
/// rule blocks every http, https, ws and ftp load, and the view stays empty if the rule cannot be made), no
/// storage (a non-persistent data store), no file access (the page is a string with no base address, not a
/// file), no popups, and no navigation of its own: a link in the page goes nowhere. The page is text the
/// editor already read and deleted from disk (ResumePreviewFile), so nothing here touches a file.
struct ResumeHTMLView: NSViewRepresentable {
    let page: String
    /// Changes with each render, so the same page is not loaded twice and a new one always is.
    let id: Int

    func makeCoordinator() -> ResumeHTMLGuard { ResumeHTMLGuard() }

    func makeNSView(context: Context) -> WKWebView {
        let web = ResumeHTMLGuard.makeWebView()
        web.navigationDelegate = context.coordinator
        web.uiDelegate = context.coordinator
        context.coordinator.load(page, id: id, into: web)
        return web
    }

    func updateNSView(_ web: WKWebView, context: Context) {
        context.coordinator.load(page, id: id, into: web)
    }
}

/// The preview web view's rules, with its delegate. Kept apart from the SwiftUI wrapper so a test can build
/// the same view and check that it does nothing it was not asked to.
@MainActor
final class ResumeHTMLGuard: NSObject, WKNavigationDelegate, WKUIDelegate {
    /// Every load that leaves the machine, as a WebKit content rule list.
    /// (WebKit's url-filter takes no alternation, so one rule per scheme family.)
    static let blockRules = """
    [{"trigger":{"url-filter":"^https?://"},"action":{"type":"block"}},
     {"trigger":{"url-filter":"^wss?://"},"action":{"type":"block"}},
     {"trigger":{"url-filter":"^ftp://"},"action":{"type":"block"}}]
    """

    static func makeWebView() -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.defaultWebpagePreferences.allowsContentJavaScript = false
        config.preferences.javaScriptCanOpenWindowsAutomatically = false
        config.preferences.isElementFullscreenEnabled = false
        let web = WKWebView(frame: .zero, configuration: config)
        web.allowsLinkPreview = false
        web.allowsBackForwardNavigationGestures = false
        return web
    }

    private var loadedID: Int?
    private var rules: Task<Bool, Never>?

    /// Install the block rule once, then load the page. With no rule there is no page: the view is left empty
    /// rather than load something that could reach the network.
    func load(_ page: String, id: Int, into web: WKWebView) {
        guard loadedID != id else { return }
        loadedID = id
        let installed: Task<Bool, Never>
        if let rules { installed = rules } else {
            installed = Task { @MainActor in
                do {
                    let list = try await WKContentRuleListStore.default().compileContentRuleList(
                        forIdentifier: "dev.vbcr.vani.resume-preview.no-network", encodedContentRuleList: Self.blockRules)
                    if let list { web.configuration.userContentController.add(list) }
                    return list != nil
                } catch {
                    return false
                }
            }
            rules = installed
        }
        Task { @MainActor in
            let ok = await installed.value
            guard loadedID == id else { return }
            if ok { web.loadHTMLString(page, baseURL: nil) } else { web.loadHTMLString("", baseURL: nil) }
        }
    }

    /// The page's own load (a string, so about:blank) is the only navigation; a link, a form or a redirect is not.
    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 preferences: WKWebpagePreferences) async -> (WKNavigationActionPolicy, WKWebpagePreferences) {
        preferences.allowsContentJavaScript = false
        let isOwnLoad = navigationAction.navigationType == .other && navigationAction.request.url?.absoluteString == "about:blank"
        decisions.append("\(isOwnLoad ? "allow" : "cancel") \(navigationAction.navigationType.rawValue) \(navigationAction.request.url?.scheme ?? "-")")
        return (isOwnLoad ? .allow : .cancel, preferences)
    }

    /// What the page was allowed and refused, in order ("allow -1 about", "cancel 0 http"), for a test to read.
    private(set) var decisions: [String] = []

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for navigationAction: WKNavigationAction,
                 windowFeatures: WKWindowFeatures) -> WKWebView? { nil }
}

// MARK: - Closing with unsaved edits

/// Where the window's close goes through once the owner chose Discard.
@MainActor
final class WindowCloser {
    fileprivate(set) weak var window: NSWindow?
    fileprivate(set) var approved = false

    /// Close the window without asking again (Discard was chosen).
    func closeApproved() {
        approved = true
        window?.performClose(nil)
    }
}

/// Puts the window's close button (and ⌘W) behind `shouldClose`, and the edited dot on the button while
/// `isEdited`. SwiftUI offers no hook for this, so the window's delegate is wrapped: everything it already
/// answers is still answered by it, and only `windowShouldClose` is asked here first.
struct WindowCloseGuard: NSViewRepresentable {
    let isEdited: Bool
    let closer: WindowCloser
    let shouldClose: @MainActor () -> Bool

    func makeCoordinator() -> CloseProxy { CloseProxy() }

    func makeNSView(context: Context) -> NSView {
        let view = HookView()
        view.onWindow = { window in context.coordinator.attach(to: window, closer: closer) }
        return view
    }

    func updateNSView(_ view: NSView, context: Context) {
        context.coordinator.shouldClose = shouldClose
        context.coordinator.window?.isDocumentEdited = isEdited
    }

    final class HookView: NSView {
        var onWindow: ((NSWindow) -> Void)?
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if let window { onWindow?(window) }
        }
    }

    @MainActor
    final class CloseProxy: NSObject, NSWindowDelegate {
        weak var window: NSWindow?
        nonisolated(unsafe) weak var original: NSWindowDelegate?
        weak var closer: WindowCloser?
        var shouldClose: @MainActor () -> Bool = { true }

        func attach(to window: NSWindow, closer: WindowCloser) {
            guard self.window !== window else { return }
            self.window = window
            self.closer = closer
            closer.window = window
            if window.delegate !== self {
                original = window.delegate
                window.delegate = self
            }
        }

        func windowShouldClose(_ sender: NSWindow) -> Bool {
            guard closer?.approved == true || shouldClose() else { return false }
            return original?.windowShouldClose?(sender) ?? true
        }

        override func responds(to aSelector: Selector!) -> Bool {
            super.responds(to: aSelector) || (original?.responds(to: aSelector) ?? false)
        }

        override func forwardingTarget(for aSelector: Selector!) -> Any? {
            if let original, original.responds(to: aSelector) { return original }
            return super.forwardingTarget(for: aSelector)
        }
    }
}
