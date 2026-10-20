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
    /// The sidebar's selection: Home at launch; a notification click sets it to
    /// Jobs.
    var pane: Pane? = .home

    private(set) var safariEnabled: Bool?
    private(set) var safariStateError: String?
    private(set) var chromeStatus: [ChromeConnector.Browser: ChromeConnector.Status] = [:]

    var extensionID: String {
        didSet { UserDefaults.standard.set(extensionID, forKey: "chromeExtensionID"); refreshChrome() }
    }

    private var timer: Timer?

    init(store: BankStore? = nil, autoRegisterAgent: Bool = true, autoStartTimer: Bool = true) {
        extensionID = UserDefaults.standard.string(forKey: "chromeExtensionID") ?? ChromeConnector.defaultExtensionID
        if let store {
            self.store = store
        } else {
            do { self.store = try BankStore.shared() } catch { storeError = String(describing: error) }
        }
        reload()
        refreshChrome()
        refreshSafari()
        agentLeftAlone = !autoRegisterAgent
        if autoRegisterAgent {
            registerAgent()
        }
        // Re-read when a browser writes. A read is one small file under a shared lock.
        if autoStartTimer {
            timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
                Task { @MainActor in self?.reloadIfChanged() }
            }
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

    /// `vani audit` for the Audit tab: the weekly blind draw and its answers. It reads no key.
    var auditClient: AuditClient { AuditClient(jobs: jobsClient) }

    /// `vani status --json` for the Pipeline tab: local files only, no key.
    var pipelineClient: PipelineClient {
        let s = draftSettings
        return PipelineClient(vaniBinary: vaniBinary,
                              configPath: s.vaniConfig.isEmpty ? nil : s.vaniConfig,
                              stateDir: s.vaniStateDir.isEmpty ? nil : s.vaniStateDir)
    }

    /// `vani run-once` for the Pipeline tab's Run Now: the pipeline calls
    /// decision models, so it gets the keys drafting gets.
    var manualRunLauncher: ManualRunLauncher {
        ManualRunLauncher(runner: draftSettings.runner(vaniBinary: vaniBinary))
    }

    // MARK: - Agent

    /// The login-item agent that runs drafts and suggestions for both browsers.
    private let agentService = SMAppService.agent(plistName: AgentConstants.plistName)
    private(set) var agentStatus: SMAppService.Status = .notRegistered
    private(set) var agentError: String?
    /// This copy was launched with -VaniApplyNoAgent YES (a screenshot or fixture
    /// rig): it never registers the agent, which stays with the copy that owns it.
    private(set) var agentLeftAlone = false

    /// Register the agent, and repair a stale registration. macOS may ask for
    /// approval in Login Items. Replacing the app bundle (every rebuild) makes
    /// launchd's copy of the job stale while SMAppService still says .enabled,
    /// so an enabled agent that does not answer is unregistered and registered
    /// again. So is one that answers from another copy of the app (the job is
    /// one per user, and the last copy to register owns it): it would run that
    /// copy's `vani` for both browsers. `repair` forces it (Settings > Register again).
    func registerAgent(repair: Bool = false) {
        guard !agentLeftAlone else {
            notice = "This copy of Vani Apply was launched with \(AgentLaunch.argument) YES, so it leaves the login agent alone. Relaunch it without that argument to register the agent here."
            return
        }
        agentError = nil
        let own = Bundle.main.bundleURL
        Task {
            let failure: String? = await Task.detached(priority: .utility) {
                // Its own handle: SMAppService is not Sendable across actors.
                let service = SMAppService.agent(plistName: AgentConstants.plistName)
                do {
                    if service.status == .enabled,
                       AgentIdentity.reregister(repair: repair, agentBundle: XPCAgentClient().identity(), ownBundle: own) != nil {
                        try await service.unregister()
                    }
                    if service.status != .enabled { try service.register() }
                    // Ask again: a copy that still answers from elsewhere is reported, not taken for this app's.
                    if service.status == .enabled, let theirs = XPCAgentClient().identity(),
                       case let .foreign(path)? = AgentIdentity.reregister(repair: false, agentBundle: theirs, ownBundle: own) {
                        return "it runs from another copy of Vani Apply (\(path)). Quit that copy, then choose Register again."
                    }
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
        if agentLeftAlone {
            return "Left alone on purpose: this copy was launched with \(AgentLaunch.argument) YES, so Safari and Chrome use whichever copy of Vani Apply registered the agent."
        }
        switch agentStatus {
        case .enabled:
            if let agentError { return "Registered, but \(agentError)" }
            return "Running on demand: Safari and Chrome can ask for drafts and suggestions."
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

    /// The accounts the account picker offers, from `anthropic-account list
    /// --json` off the main thread, or why there are none (the launcher not
    /// installed, no ~/.anthropic-accounts yet). Reads no secret.
    func anthropicAccounts() async -> (names: [String], problem: String?) {
        await Task.detached(priority: .userInitiated) { () -> (names: [String], problem: String?) in
            guard let launcher = AnthropicAccount.locate() else {
                return ([], "anthropic-account is not installed in ~/.local/bin or /opt/homebrew/bin.")
            }
            do { return (try AnthropicAccount.list(launcher: launcher), nil) } catch {
                return ([], String(describing: error))
            }
        }.value
    }

    /// Run one draft off the main thread. On the anthropic path the runner starts
    /// vani through anthropic-account, which reads the chosen account's key.
    func draft(_ request: DraftRequest) async -> DraftReply {
        let runner = draftSettings.runner(vaniBinary: vaniBinary)
        let reply = await Task.detached(priority: .userInitiated) { runner.run(request) }.value
        try? store?.log(ActivityEntry(source: .app, event: .draft, counts: [reply.error == nil ? "drafted" : "failed": 1]))
        if let store { refreshActivity(store) }
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
            activityStamp = nil
            activity = []
            refreshActivity(store)
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
            refreshActivity(store)
        } catch {
            storeError = String(describing: error)
        }
    }

    /// The activity log as last read, by its files' stamp. Every tick used to
    /// re-read and re-decode up to 500 lines from both files and republish
    /// them, redrawing the Activity list, though nothing had been appended.
    private var activityStamp: BankStore.ActivityStamp?

    /// Re-read the log only when its files changed. The stamp is taken before
    /// the read, so an append landing in between is read again next tick
    /// rather than missed.
    private func refreshActivity(_ store: BankStore) {
        let stamp = store.activityStamp()
        guard stamp != activityStamp else { return }
        guard let entries = try? store.recentActivity() else { return }
        activityStamp = stamp
        activity = entries
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
            refreshActivity(store)
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

    /// Add `label` as another wording of the answer `key` (the duplicate an add would have hit).
    @discardableResult
    func addWording(_ label: String, to key: String) -> Bool {
        change(.edit, counts: ["wordings": 1]) { bank in
            bank.answers[key] = try Dedup.addingWording(label, to: key, in: bank)
        }
    }

    // MARK: - Duplicates

    /// `vani answers similar`, with Vani's config and state folder as drafting uses.
    var similarLabels: SimilarLabels {
        let s = draftSettings
        return SimilarLabels(vaniBinary: vaniBinary,
                             configPath: s.vaniConfig.isEmpty ? nil : s.vaniConfig,
                             stateDir: s.vaniStateDir.isEmpty ? nil : s.vaniStateDir)
    }

    /// Clusters from the bank as it is now, with any similar-wording pairs already found.
    func duplicateClusters(similar: SimilarResult? = nil) -> [Dedup.Cluster] {
        var pairs: [SimilarPair] = []
        if case let .available(found, _)? = similar { pairs = found }
        return Dedup.clusters(bank: snapshot.bank, similar: pairs)
    }

    /// The similar-wording pass, off the main thread. The child is bounded by SimilarLabels.timeout.
    func findSimilarWordings() async -> SimilarResult {
        let bank = snapshot.bank
        let client = similarLabels
        return await Task.detached(priority: .userInitiated) { client.find(in: bank) }.value
    }

    /// Merge the plans in one validated write (one revision). Counts only reach the log.
    @discardableResult
    func merge(_ plans: [Dedup.MergePlan]) -> Bool {
        let removed = plans.reduce(0) { $0 + $1.merged.count }
        return change(.edit, counts: ["merged": removed, "clusters": plans.count]) { bank in
            try Dedup.merge(bank: &bank, plans: plans)
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
        Self.safariState(Self.safariExtensionID) { [weak self] enabled, message in
            self?.safariEnabled = enabled
            self?.safariStateError = message
        }
    }

    func openSafariSettings() {
        Self.showSafariSettings(Self.safariExtensionID) { [weak self] message in
            self?.actionError = "Safari: \(message)"
        }
    }

    // Safari answers on a background queue, but the SDK declares the state handler
    // NS_SWIFT_UI_ACTOR, so a closure literal passed straight to it is inferred @MainActor and
    // the runtime flags it as a data race. Each handler is built as a plain @Sendable value first
    // (no actor inferred) and hands its result to the main actor itself.
    nonisolated private static func safariState(_ id: String, _ done: @escaping @MainActor @Sendable (Bool?, String?) -> Void) {
        let handler: @Sendable (SFSafariExtensionState?, Error?) -> Void = { state, error in
            let enabled = state?.isEnabled
            let message = error.map { $0.localizedDescription }
            Task { @MainActor in done(enabled, message) }
        }
        SFSafariExtensionManager.getStateOfSafariExtension(withIdentifier: id, completionHandler: handler)
    }

    nonisolated private static func showSafariSettings(_ id: String, _ failed: @escaping @MainActor @Sendable (String) -> Void) {
        let handler: @Sendable (Error?) -> Void = { error in
            guard let error else { return }
            let message = error.localizedDescription
            Task { @MainActor in failed(message) }
        }
        SFSafariApplication.showPreferencesForExtension(withIdentifier: id, completionHandler: handler)
    }
}
