import Foundation
import ServiceManagement

/// What the Pipeline tab's "Run on schedule" switch says, as a function of three facts: what
/// macOS says about the schedule agent's registration (SMAppService.Status), the last failed
/// register or unregister, and the daemon as `vani status --json` reports it. Pure, so every
/// combination is tested without registering anything.
public struct ScheduleToggle: Equatable, Sendable {
    /// SMAppService.Status, named here so a caller does not have to pass that type.
    public enum Registration: Equatable, Sendable {
        case notRegistered
        case enabled
        case requiresApproval
        /// ServiceManagement has no record of the agent. That is also what it reports
        /// before the first registration. `copy` on `make` says whether this bundle can
        /// register it: only a missing plist means this copy has no schedule agent.
        case notFound
    }

    public enum Tone: Equatable, Sendable { case normal, warning, problem }

    /// Where the switch sits: on when the agent is registered or waiting for approval.
    public var isOn: Bool
    /// The switch can be flipped.
    public var isEnabled: Bool
    public var headline: String
    public var detail: String?
    public var tone: Tone
    /// Offer System Settings > Login Items (an approval is waiting, or it can be turned off there).
    public var offersLoginItems: Bool
    /// Offer to register again: registered, but no daemon has announced itself, which is what a
    /// replaced app bundle (launchd's copy of the job went stale) looks like.
    public var offersRepair: Bool

    /// - Parameters:
    ///   - daemon: the status read's daemon facet; nil before the first read or from a vani that
    ///     does not report one.
    ///   - leftAlone: this copy was launched with -VaniApplyNoAgent YES.
    ///   - copy: what `ScheduleAgent.inspect` said about this bundle. ServiceManagement's
    ///     `.notFound` describes the installed service, not the file, so an agent that has
    ///     never been registered reports it too. `.ready` is the same as `.notRegistered`
    ///     and the switch can register it. `.unusable` stays off and says why.
    public static func make(registration: Registration, error: String?, daemon: PipelineStatus.Daemon?,
                            leftAlone: Bool, copy: ScheduleAgent.BundleCopy = .absent) -> ScheduleToggle {
        func result(on: Bool, enabled: Bool = true, _ headline: String, _ detail: String? = nil, tone: Tone = .normal,
                    loginItems: Bool = false, repair: Bool = false) -> ScheduleToggle {
            ScheduleToggle(isOn: on, isEnabled: enabled, headline: headline, detail: detail, tone: tone,
                           offersLoginItems: loginItems, offersRepair: repair)
        }
        let isOn = registration == .enabled || registration == .requiresApproval
        if leftAlone {
            return result(on: isOn, enabled: false, "Left alone on purpose",
                          "This copy was launched with \(AgentLaunch.argument) YES, so it never registers the schedule agent. Relaunch it without that argument.")
        }
        if let error, !error.isEmpty {
            return result(on: isOn, "Could not change the schedule agent", BoundedText.sentence(error), tone: .problem, loginItems: true)
        }
        // A file that must not be registered disables the switch only while the agent is off.
        // An agent that is already on can still be turned off.
        if case let .unusable(why) = copy, registration == .notFound || registration == .notRegistered {
            return result(on: false, enabled: false, "This copy of the app cannot register the schedule agent", why, tone: .problem)
        }
        // A never-registered agent and a bundle with no plist both come back `.notFound`.
        // The inspected bundle is what tells them apart; the system record is not.
        let registration = (registration == .notFound && copy == .ready) ? Registration.notRegistered : registration
        switch registration {
        case .notFound:
            return result(on: false, enabled: false, "This copy of the app has no schedule agent",
                          "Rebuild it with macos/scripts/bundle.sh.", tone: .problem)
        case .requiresApproval:
            return result(on: true, "Waiting for your approval",
                          "Allow Vani Apply in System Settings > General > Login Items & Extensions. Until then nothing fires the daily run.",
                          tone: .warning, loginItems: true)
        case .notRegistered:
            if daemon?.running == true {
                return result(on: false, "Off here, but a daemon is running",
                              "A daemon started elsewhere (a shell, a launchd agent of your own) is keeping the schedule. Turning this on would start a second one, which the ledger lock refuses.")
            }
            return result(on: false, "Off", "Nothing fires the daily run unless you start it.")
        case .enabled:
            guard let daemon else {
                return result(on: true, "On", "Checking whether the daemon is running…")
            }
            guard daemon.ok else {
                return result(on: true, "On, but the daemon's state is unknown", daemon.reason, tone: .warning)
            }
            guard daemon.running else {
                return result(on: true, "On, but no daemon has started yet",
                              [daemon.reason, "If this app was just replaced, register again. Otherwise the daemon's log is logs/vani.err.log in Vani's state folder."]
                                  .compactMap { $0 }.joined(separator: " "),
                              tone: .warning, loginItems: true, repair: true)
            }
            let pid = daemon.pid.map { " (pid \($0))" } ?? ""
            if let model = daemon.model, !model.enabled {
                return result(on: true, "Keeping the schedule, with the decision model off\(pid)",
                              (model.reason ?? "The decision key did not reach the daemon.")
                                  + " Add it to the login Keychain (service \(Keychain.openRouterService)). A key that was missing is picked up by a restart the agent makes by itself; after a refused or unanswered Keychain read, turn this off and on.",
                              tone: .warning)
            }
            return result(on: true, "Keeping the schedule\(pid)", nil)
        }
    }

    /// What to ask ServiceManagement. A service it has no record of (`.notFound`, which is
    /// also never-registered) is not unregistered: that call fails with job-not-found and
    /// used to stop the register that should have followed. Turning on an agent that is
    /// already on does not register again (`already registered`). Repair replaces it.
    public enum Order: Equatable, Sendable {
        case leave
        case register
        case unregister
        case replace
    }

    public static func order(on: Bool, repair: Bool, status: Registration) -> Order {
        if !on {
            switch status {
            case .enabled, .requiresApproval: return .unregister
            case .notRegistered, .notFound: return .leave
            }
        }
        if repair { return .replace }
        switch status {
        case .notRegistered, .notFound: return .register
        case .enabled, .requiresApproval: return .leave
        }
    }

    /// A ServiceManagement error, classified so a missing job and an already-registered
    /// agent can be handled instead of shown as a failure. Any other domain is a message,
    /// bounded, and is never treated as one of those two.
    public enum Fault: Error, Equatable, Sendable {
        case jobMissing
        case alreadyRegistered
        case message(String)

        public static func classify(_ error: NSError) -> Fault {
            let text = BoundedText.sentence(error.localizedDescription)
            // SMAppServiceErrorDomain is macOS 15. On the macOS 14 deployment target
            // the same error is reported as a message, not as job-missing.
            guard #available(macOS 15, *), error.domain == SMAppServiceErrorDomain else { return .message(text) }
            switch error.code {
            case Int(kSMErrorJobNotFound): return .jobMissing
            case Int(kSMErrorAlreadyRegistered): return .alreadyRegistered
            default: return .message(text)
            }
        }

        public var text: String {
            switch self {
            case .jobMissing: return "The schedule agent is not registered."
            case .alreadyRegistered: return "The schedule agent is already registered."
            case .message(let text): return text
            }
        }
    }

    /// Bumps when a register or unregister starts and again just before it publishes.
    /// A status read captures `current` and publishes only while `isCurrent` and the
    /// toggle is not busy, so a read that started before the change cannot overwrite it.
    public struct Epoch: Equatable, Sendable {
        public private(set) var current = 0

        public init() {}

        public mutating func begin() -> Int {
            current &+= 1
            return current
        }

        public func isCurrent(_ token: Int) -> Bool { token == current }
    }
}
