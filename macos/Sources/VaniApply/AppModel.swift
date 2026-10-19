import Foundation
import Observation
import SafariServices
import ServiceManagement
import VaniApplyKit

/// The app's view of the shared bank. Every change goes through BankStore
/// (validated, locked, revisioned), the same path the browser extensions use;
/// the app re-reads when another process writes.
@MainActor
@Observable
final class AppModel {
    static let safariExtensionID = "dev.vbcr.vani.apply.extension"

    private(set) var store: BankStore?
    private(set) var snapshot = Snapshot(rev: 0, bank: .empty)
    private(set) var activity: [ActivityEntry] = []
    /// A problem reading the store (corrupt file, container unavailable). Edits are blocked while set.
    private(set) var storeError: String?
    /// The last failed action, shown as an alert.
    var actionError: String?
    var notice: String?

    private(set) var safariEnabled: Bool?
    private(set) var safariStateError: String?
    private(set) var chromeStatus: [ChromeConnector.Browser: ChromeConnector.Status] = [:]

    var extensionID: String {
        didSet { UserDefaults.standard.set(extensionID, forKey: "chromeExtensionID"); refreshChrome() }
    }

    private var timer: Timer?

    init() {
        extensionID = UserDefaults.standard.string(forKey: "chromeExtensionID") ?? ChromeConnector.defaultExtensionID
        do { store = try BankStore.shared() } catch { storeError = String(describing: error) }
        reload()
        refreshChrome()
        refreshSafari()
        registerAgent()
        // Re-read when a browser writes. A read is one small file under a shared lock.
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.reloadIfChanged() }
        }
    }

    var answers: [Answer] { snapshot.bank.answers.values.sorted { $0.key < $1.key } }
    var settings: Settings { snapshot.bank.settings }
    var bankBytes: Int { (try? snapshot.bank.encoded().count) ?? 0 }

    var hostBinary: URL { Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/vani-apply-host") }
    var vaniBinary: URL { Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/vani") }

    /// `vani jobs` for the Jobs tab, with the same Vani config and state folder as drafting.
    /// It reads no key: listing and marking spend nothing.
    var jobsClient: JobsClient { draftSettings.jobsClient(vaniBinary: vaniBinary) }

    /// `vani status --json` for the Pipeline tab: local files only, no key.
    var pipelineClient: PipelineClient {
        let s = draftSettings
        return PipelineClient(vaniBinary: vaniBinary,
                              configPath: s.vaniConfig.isEmpty ? nil : s.vaniConfig,
                              stateDir: s.vaniStateDir.isEmpty ? nil : s.vaniStateDir)
    }

    // MARK: - Agent

    /// The login-item agent that runs drafts and suggestions for both browsers.
    private let agentService = SMAppService.agent(plistName: AgentConstants.plistName)
    private(set) var agentStatus: SMAppService.Status = .notRegistered
    private(set) var agentError: String?

    /// Register the agent, and repair a stale registration. macOS may ask for
    /// approval in Login Items. Replacing the app bundle (every rebuild) makes
    /// launchd's copy of the job stale while SMAppService still says .enabled,
    /// so an enabled agent that does not answer is unregistered and registered
    /// again. `repair` forces that (Settings > Register again).
    func registerAgent(repair: Bool = false) {
        agentError = nil
        Task {
            let failure: String? = await Task.detached(priority: .utility) {
                // Its own handle: SMAppService is not Sendable across actors.
                let service = SMAppService.agent(plistName: AgentConstants.plistName)
                do {
                    if service.status == .enabled, repair || !XPCAgentClient().reachable() {
                        try await service.unregister()
                    }
                    if service.status != .enabled { try service.register() }
                    return nil
                } catch {
                    return error.localizedDescription
                }
            }.value
            agentError = failure
            agentStatus = agentService.status
        }
    }

    func openLoginItems() {
        SMAppService.openSystemSettingsLoginItems()
    }

    var agentSummary: String {
        switch agentStatus {
        case .enabled: return "Running on demand: Safari and Chrome can ask for drafts and suggestions."
        case .requiresApproval: return "Waiting for approval in System Settings > General > Login Items."
        case .notFound: return "Not found in this copy of the app; rebuild it with scripts/bundle.sh."
        case .notRegistered: return "Not registered" + (agentError.map { ": \($0)" } ?? ".")
        @unknown default: return "Unknown state."
        }
    }

    // MARK: - Drafting

    var draftSettings: DraftSettings {
        guard let dir = store?.directory else { return DraftSettings() }
        return DraftSettings.load(from: dir)
    }

    func saveDraftSettings(_ s: DraftSettings) {
        guard let dir = store?.directory else {
            actionError = "Settings were not saved: " + (storeError ?? "Vani Apply's App Group folder is unavailable.")
            return
        }
        do { try s.save(to: dir) } catch { actionError = String(describing: error) }
    }

    /// Run one draft off the main thread. The Keychain read happens in the runner,
    /// so macOS asks about it the first time.
    func draft(_ request: DraftRequest) async -> DraftReply {
        let runner = draftSettings.runner(vaniBinary: vaniBinary)
        let reply = await Task.detached(priority: .userInitiated) { runner.run(request) }.value
        try? store?.log(ActivityEntry(source: .app, event: .draft, counts: [reply.error == nil ? "drafted" : "failed": 1]))
        activity = (try? store?.recentActivity()) ?? activity
        return reply
    }

    @discardableResult
    func saveDraft(question: String, text: String) -> Bool {
        change(.edit, counts: ["drafted": 1]) { bank in
            let a = try Editing.savingDraft(question: question, text: text, in: bank)
            bank.answers[a.key] = a
        }
    }
    var chromeExtensionFolder: URL { Bundle.main.bundleURL.appendingPathComponent("Contents/Resources/chrome-extension") }
    var connector: ChromeConnector { .forCurrentUser(hostBinary: hostBinary) }

    // MARK: - Reading

    func reload() {
        guard let store else { return }
        do {
            snapshot = try store.read()
            activity = (try? store.recentActivity()) ?? []
            storeError = nil
        } catch {
            storeError = String(describing: error)
        }
    }

    private func reloadIfChanged() {
        guard let store else { return }
        do {
            let next = try store.read()
            if next.rev != snapshot.rev || storeError != nil {
                snapshot = next
                storeError = nil
            }
            activity = (try? store.recentActivity()) ?? activity
        } catch {
            storeError = String(describing: error)
        }
    }

    // MARK: - Editing

    private func change(_ event: ActivityEntry.Event, counts: [String: Int]? = nil, _ body: (inout Bank) throws -> Void) -> Bool {
        guard let store, storeError == nil else {
            actionError = storeError ?? "The answer bank is unavailable."
            return false
        }
        do {
            snapshot = try store.update(body)
            try? store.log(ActivityEntry(source: .app, event: event, counts: counts))
            activity = (try? store.recentActivity()) ?? activity
            return true
        } catch {
            actionError = String(describing: error)
            return false
        }
    }

    @discardableResult
    func add(question: String, kind: FieldKind, text: String) -> Bool {
        change(.edit, counts: ["added": 1]) { bank in
            let a = try Editing.newAnswer(question: question, kind: kind, text: text, in: bank)
            bank.answers[a.key] = a
        }
    }

    @discardableResult
    func edit(key: String, text: String) -> Bool {
        change(.edit, counts: ["edited": 1]) { bank in
            guard let a = bank.answers[key] else { throw BankError("that answer was deleted elsewhere") }
            bank.answers[key] = try Editing.edited(a, text: text)
        }
    }

    func delete(keys: Set<String>) {
        _ = change(.delete, counts: ["deleted": keys.count]) { bank in
            for k in keys { bank.answers.removeValue(forKey: k) }
        }
    }

    func setCaptureSelfId(_ on: Bool) {
        _ = change(.settings) { $0.settings.captureSelfId = on }
    }

    @discardableResult
    func disable(host input: String) -> Bool {
        guard let host = Editing.hostname(input) else {
            actionError = "“\(input)” is not a hostname (for example jobs.lever.co)."
            return false
        }
        return change(.settings) { bank in
            if !bank.settings.disabledHosts.contains(host) { bank.settings.disabledHosts.append(host) }
        }
    }

    func enable(host: String) {
        _ = change(.settings) { $0.settings.disabledHosts.removeAll { $0 == host } }
    }

    func importBank(from url: URL) {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        do {
            let data = try Data(contentsOf: url)
            let checked = try BankValidator.validate(json: data)
            if change(.import, counts: ["answers": checked.bank.answers.count, "droppedSecret": checked.droppedSecret], { $0 = checked.bank }) {
                notice = "Imported \(checked.bank.answers.count) answers" + (checked.droppedSecret > 0 ? "; dropped \(checked.droppedSecret) secret-looking answers." : ".")
            }
        } catch {
            actionError = "Import refused: \(error)"
        }
    }

    func exportData() -> Data? {
        do {
            let data = try Editing.exportData(snapshot.bank)
            try? store?.log(ActivityEntry(source: .app, event: .export, counts: ["answers": snapshot.bank.answers.count]))
            return data
        } catch {
            actionError = "Export failed: \(error)"
            return nil
        }
    }

    func quarantineCorruptBank() {
        guard let store else { return }
        do {
            if let moved = try store.quarantineCorruptBank() {
                notice = "The unreadable bank was moved to \(moved.lastPathComponent) in the store folder; starting empty."
            }
            reload()
        } catch {
            actionError = String(describing: error)
        }
    }

    // MARK: - Browsers

    func refreshChrome() {
        var out: [ChromeConnector.Browser: ChromeConnector.Status] = [:]
        for b in ChromeConnector.Browser.allCases { out[b] = connector.status(b, extensionID: extensionID) }
        chromeStatus = out
    }

    func connect(_ browser: ChromeConnector.Browser) {
        do {
            try connector.connect(browser, extensionID: extensionID)
            notice = "\(browser.rawValue) can now reach Vani Apply. Reload the extension in chrome://extensions."
        } catch {
            actionError = "Could not connect \(browser.rawValue): \(error)"
        }
        refreshChrome()
    }

    func disconnect(_ browser: ChromeConnector.Browser) {
        do { try connector.disconnect(browser) } catch { actionError = "Could not disconnect \(browser.rawValue): \(error)" }
        refreshChrome()
    }

    func refreshSafari() {
        SFSafariExtensionManager.getStateOfSafariExtension(withIdentifier: Self.safariExtensionID) { [weak self] state, error in
            let enabled = state?.isEnabled
            let message = error.map { $0.localizedDescription }
            Task { @MainActor in
                self?.safariEnabled = enabled
                self?.safariStateError = message
            }
        }
    }

    func openSafariSettings() {
        SFSafariApplication.showPreferencesForExtension(withIdentifier: Self.safariExtensionID) { [weak self] error in
            guard let error else { return }
            let message = error.localizedDescription
            Task { @MainActor in self?.actionError = "Safari: \(message)" }
        }
    }
}
