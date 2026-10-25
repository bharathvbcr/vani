import AppKit
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
    private(set) var snapshot = Snapshot(rev: 0, bank: .empty) {
        didSet { bankBytesCache = nil }
    }
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
    private var activationObserver: NSObjectProtocol?

    /// The three things a tick or a body evaluation would otherwise do again and again, as
    /// seams a test can count.
    private let readBank: @Sendable (BankStore) throws -> Snapshot
    private let loadDraftSettings: @Sendable (URL) throws -> DraftSettings
    private let measureBank: @Sendable (Bank) -> Int

    init(store: BankStore? = nil, autoRegisterAgent: Bool = true, autoStartTimer: Bool = true,
         readBank: @escaping @Sendable (BankStore) throws -> Snapshot = { try $0.read() },
         loadDraftSettings: @escaping @Sendable (URL) throws -> DraftSettings = { try DraftSettings.load(from: $0) },
         measureBank: @escaping @Sendable (Bank) -> Int = { (try? $0.encoded().count) ?? 0 }) {
        self.readBank = readBank
        self.loadDraftSettings = loadDraftSettings
        self.measureBank = measureBank
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
        // Re-read when a browser writes: a stat of bank.json every tick, the read itself (off the
        // main actor) only when that stamp moved.
        if autoStartTimer {
            timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
                Task { @MainActor in await self?.reloadIfChanged() }
            }
            // The draft settings file is edited by hand too: read it again when the app comes forward.
            activationObserver = NotificationCenter.default.addObserver(
                forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main
            ) { [weak self] _ in
                // The owner may have approved (or switched off) the schedule agent in System Settings.
                MainActor.assumeIsolated { self?.refreshDraftSettings(); self?.refreshSchedule() }
            }
        }
    }

    var answers: [Answer] { snapshot.bank.answers.values.sorted { $0.key < $1.key } }
    var settings: Settings { snapshot.bank.settings }

    /// The size of the stored bank, encoded once per snapshot rather than once per body evaluation.
    @ObservationIgnored private var bankBytesCache: Int?
    var bankBytes: Int {
        if let bankBytesCache { return bankBytesCache }
        let size = measureBank(snapshot.bank)
        bankBytesCache = size
        return size
    }

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
        ManualRunLauncher(runner: draftRunner)
    }

    /// `vani jobs retriage` for the Jobs tab's "Re-triage with Sonnet": it asks a model, so it runs
    /// through the draft runner and gets the keys drafting gets (never read here: on the anthropic
    /// path `anthropic-account` adds them). Its estimate and clear start `vani` with no key.
    var retriageClient: RetriageClient {
        RetriageClient(runner: draftRunner)
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
            // The two blocking halves (an XPC round trip each) run off the main actor and off the
            // cooperative pool; the unregister between them is a real async call.
            // Each has its own SMAppService handle: it is not Sendable across actors.
            let stale = await BlockingWork.value { () -> Bool in
                let service = SMAppService.agent(plistName: AgentConstants.plistName)
                return service.status == .enabled
                    && AgentIdentity.reregister(repair: repair, agentBundle: XPCAgentClient().identity(), ownBundle: own) != nil
            }
            var failure: String?
            if stale {
                do { try await agentService.unregister() } catch { failure = error.localizedDescription }
            }
            if failure == nil {
                failure = await BlockingWork.value { () -> String? in
                    let service = SMAppService.agent(plistName: AgentConstants.plistName)
                    do {
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
                }
            }
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
            if let agentError, !agentError.isEmpty {
                return "Registered, but \(BoundedText.sentence(agentError))"
            }
            return "Running on demand: Safari and Chrome can ask for drafts and suggestions."
        case .requiresApproval: return "Waiting for approval in System Settings > General > Login Items."
        case .notFound:
            let relative = "Contents/Library/LaunchAgents/\(AgentConstants.plistName)"
            let inside: Bool
            if case .file = BundleMember.place(bundle: Bundle.main.bundleURL, relative: relative) { inside = true } else { inside = false }
            return AgentCopy.notFoundLine(error: agentError, plistInside: inside)
        case .notRegistered:
            guard let agentError, !agentError.isEmpty else { return "Not registered." }
            return "Not registered: \(BoundedText.sentence(agentError))"
        @unknown default: return "Unknown state."
        }
    }

    // MARK: - Schedule agent

    /// What macOS says about the schedule agent: the opt-in login item that keeps `vani daemon`
    /// running (ScheduleAgent.swift). It is never registered by launch, only by the Pipeline
    /// tab's "Run on schedule" switch. Read when the tab appears and after each change, since
    /// the owner can also switch it in System Settings.
    private(set) var scheduleStatus: ScheduleToggle.Registration = .notRegistered
    /// What this bundle can register. ServiceManagement's `.notFound` is also a
    /// never-registered agent, so the file decides whether the switch can turn it on.
    private(set) var scheduleCopy: ScheduleAgent.BundleCopy = .absent
    private(set) var scheduleError: String?
    private(set) var scheduleBusy = false
    /// Invalidates a status read that started before a register or unregister.
    private var scheduleEpoch = ScheduleToggle.Epoch()

    static func registration(_ status: SMAppService.Status) -> ScheduleToggle.Registration {
        switch status {
        case .enabled: .enabled
        case .requiresApproval: .requiresApproval
        case .notFound: .notFound
        case .notRegistered: .notRegistered
        @unknown default: .notRegistered
        }
    }

    /// Reads the registration. Reading registers nothing, but a rig that leaves the agents
    /// alone (and the tests) does not ask at all.
    func refreshSchedule() {
        guard !agentLeftAlone else { return }
        let token = scheduleEpoch.current
        Task {
            let status = await BlockingWork.value { SMAppService.agent(plistName: ScheduleAgentConstants.plistName).status }
            let copy = ScheduleAgent.inspect(bundle: Bundle.main.bundleURL)
            // A change that started after this read, or one still in flight, owns the screen.
            guard scheduleEpoch.isCurrent(token), !scheduleBusy else { return }
            // The file first: publishing `.notFound` while `scheduleCopy` is still `.absent`
            // would flash "this copy has no schedule agent" for a bundle that has one.
            scheduleCopy = copy
            scheduleStatus = Self.registration(status)
        }
    }

    /// Registers (macOS may then wait for approval in Login Items) or unregisters the schedule
    /// agent. Unregistering stops the helper, which ends the daemon first. `repair` registers
    /// again from scratch: a replaced app bundle leaves launchd's copy of the job stale.
    func setScheduleEnabled(_ on: Bool, repair: Bool = false) {
        guard !agentLeftAlone, !scheduleBusy else { return }
        scheduleBusy = true
        scheduleError = nil
        // Drops a refresh that already read the old status.
        _ = scheduleEpoch.begin()
        let statusNow = scheduleStatus
        let bundle = Bundle.main.bundleURL
        Task {
            var failure: String?
            let copy = ScheduleAgent.inspect(bundle: bundle)
            if on, copy != .ready {
                // Do not ask ServiceManagement to register a plist that is missing or refused.
                let why: String? = { if case let .unusable(text) = copy { return text } else { return nil } }()
                self.publishSchedule(status: copy == .absent ? .notFound : statusNow, copy: copy, failure: why)
                return
            }
            do {
                try await Self.performSchedule(ScheduleToggle.order(on: on, repair: repair, status: statusNow))
            } catch let fault as ScheduleToggle.Fault {
                failure = fault.text
            } catch {
                failure = BoundedText.sentence(error.localizedDescription)
            }
            let status = await BlockingWork.value { SMAppService.agent(plistName: ScheduleAgentConstants.plistName).status }
            let after = ScheduleAgent.inspect(bundle: bundle)
            // A registration that now waits for approval is the expected result, not a failure.
            self.publishSchedule(status: Self.registration(status), copy: after, failure: status == .requiresApproval ? nil : failure)
        }
    }

    /// Publishes a schedule read only if no newer change has started. Clears the busy flag
    /// either way when this call was the one that set it: a dropped publish must not leave
    /// the switch stuck.
    private func publishSchedule(status: ScheduleToggle.Registration, copy: ScheduleAgent.BundleCopy, failure: String?) {
        let token = scheduleEpoch.begin()
        guard scheduleEpoch.isCurrent(token) else {
            scheduleBusy = false
            return
        }
        scheduleCopy = copy
        scheduleError = failure
        scheduleStatus = status
        scheduleBusy = false
    }

    /// Runs `order`. A missing job on unregister is success. A register that finds the agent
    /// already registered is unregistered and registered once more, and not again.
    private static func performSchedule(_ order: ScheduleToggle.Order) async throws {
        switch order {
        case .leave: return
        case .unregister:
            try await scheduleUnregister(missingOK: true)
        case .register:
            try await scheduleRegister(replaceIfPresent: true)
        case .replace:
            try await scheduleUnregister(missingOK: true)
            try await scheduleRegister(replaceIfPresent: false)
        }
    }

    private static func scheduleRegister(replaceIfPresent: Bool) async throws {
        do {
            try await scheduleRegisterCall()
        } catch ScheduleToggle.Fault.alreadyRegistered where replaceIfPresent {
            try await scheduleUnregister(missingOK: true)
            try await scheduleRegisterCall()
        }
    }

    private static func scheduleRegisterCall() async throws {
        let result = await BlockingWork.run {
            try SMAppService.agent(plistName: ScheduleAgentConstants.plistName).register()
        }
        if case let .failure(error) = result { throw ScheduleToggle.Fault.classify(error as NSError) }
    }

    private static func scheduleUnregister(missingOK: Bool) async throws {
        do {
            try await SMAppService.agent(plistName: ScheduleAgentConstants.plistName).unregister()
        } catch {
            let fault = ScheduleToggle.Fault.classify(error as NSError)
            if missingOK, case .jobMissing = fault { return }
            throw fault
        }
    }

    func openScheduleLoginItems() { SMAppService.openSystemSettingsLoginItems() }

    // MARK: - Drafting

    /// Why the saved draft settings cannot be used, or nil when they load (a missing file is fine).
    /// As of the last `refreshDraftSettings()`.
    private(set) var draftSettingsProblem: String?

    /// The settings for display and for the local read-only clients. A corrupt file shows as
    /// defaults here, and `draftSettingsProblem` is what says so (Settings and reload() raise it).
    /// Anything that launches vani with a model or an account (drafts, Run Now) goes through
    /// `draftRunner`, which refuses instead. Read from disk by `refreshDraftSettings()` (at load,
    /// on a save, when the app comes forward, when Settings appears), not on every access: the
    /// views ask for it on every body evaluation.
    private(set) var draftSettings = DraftSettings()

    func refreshDraftSettings() {
        guard let dir = store?.directory else {
            draftSettings = DraftSettings()
            draftSettingsProblem = nil
            return
        }
        do {
            draftSettings = try loadDraftSettings(dir)
            draftSettingsProblem = nil
        } catch {
            draftSettings = DraftSettings()
            draftSettingsProblem = String(describing: error)
        }
    }

    /// The runner for the saved settings; it refuses every launch when the file is unreadable.
    var draftRunner: DraftRunner {
        guard let dir = store?.directory else { return DraftSettings().runner(vaniBinary: vaniBinary) }
        return DraftSettings.runner(loadingFrom: dir, vaniBinary: vaniBinary)
    }

    func saveDraftSettings(_ s: DraftSettings) {
        guard let dir = store?.directory else {
            actionError = "Settings were not saved: " + (storeError ?? "Vani Apply's App Group folder is unavailable.")
            return
        }
        do { try s.save(to: dir) } catch { actionError = String(describing: error) }
        refreshDraftSettings()
    }

    /// The accounts the account picker offers, from `anthropic-account list
    /// --json` off the main thread, or why there are none (the launcher not
    /// installed, no ~/.anthropic-accounts yet). Reads no secret.
    func anthropicAccounts() async -> (names: [String], problem: String?) {
        await BlockingWork.value { () -> (names: [String], problem: String?) in
            guard let launcher = AnthropicAccount.locate() else {
                return ([], "anthropic-account is not installed in ~/.local/bin or /opt/homebrew/bin.")
            }
            do { return (try AnthropicAccount.list(launcher: launcher), nil) } catch {
                return ([], String(describing: error))
            }
        }
    }

    /// Run one draft off the main thread. On the anthropic path the runner starts
    /// vani through anthropic-account, which reads the chosen account's key.
    func draft(_ request: DraftRequest) async -> DraftReply {
        let runner = draftRunner
        let reply = await BlockingWork.value { runner.run(request) }
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

    /// The bank.json stamp the current snapshot was read under, taken before that read so a write
    /// landing during it shows up as a changed stamp on the next tick.
    private var bankStampSeen: BankStore.BankStamp?
    /// Bumped by every read or write made on the main actor, so a background read that started
    /// before one cannot overwrite what it produced.
    private var bankGeneration = 0
    private var tickReading = false

    /// A read the user asked for (launch, Retry, after moving a corrupt bank aside). It waits
    /// for the lock on the main actor, which is acceptable for a deliberate action; the timer
    /// goes through `reloadIfChanged()`.
    func reload() {
        guard let store else { return }
        bankGeneration += 1
        refreshDraftSettings()
        let stamp = store.bankStamp()
        do {
            snapshot = try readBank(store)
            bankStampSeen = stamp
            activityStamp = nil
            activity = []
            refreshActivity(store)
            storeError = nil
            if let problem = draftSettingsProblem { actionError = problem }
        } catch {
            storeError = String(describing: error)
        }
    }

    /// One timer tick. A stat of bank.json says whether anything changed; only then is the bank
    /// read, off the main actor (the read waits up to the lock timeout and parses and validates
    /// the whole file), and the result applied here. While the store is in error the read is
    /// retried every tick, since a lock timeout clears without the file changing.
    func reloadIfChanged() async {
        guard let store, !tickReading else { return }
        let stamp = store.bankStamp()
        if stamp != bankStampSeen || storeError != nil {
            tickReading = true
            defer { tickReading = false }
            let generation = bankGeneration
            let read = readBank
            let result = await BlockingWork.run { try read(store) }
            guard generation == bankGeneration else { return } // a read or an edit here superseded this one
            switch result {
            case let .success(next):
                bankStampSeen = stamp
                if next.rev != snapshot.rev || storeError != nil {
                    snapshot = next
                    storeError = nil
                }
            case let .failure(error):
                storeError = String(describing: error)
            }
        }
        refreshActivity(store)
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
            bankGeneration += 1
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
        return await BlockingWork.value { client.find(in: bank) }
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
