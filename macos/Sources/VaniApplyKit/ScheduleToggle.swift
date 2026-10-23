import Foundation

/// What the Pipeline tab's "Run on schedule" switch says, as a function of three facts: what
/// macOS says about the schedule agent's registration (SMAppService.Status), the last failed
/// register or unregister, and the daemon as `vani status --json` reports it. Pure, so every
/// combination is tested without registering anything.
public struct ScheduleToggle: Equatable, Sendable {
    /// SMAppService.Status, without importing it here.
    public enum Registration: Equatable, Sendable {
        case notRegistered
        case enabled
        case requiresApproval
        /// This copy of the app does not hold the agent's plist.
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
    public static func make(registration: Registration, error: String?, daemon: PipelineStatus.Daemon?,
                            leftAlone: Bool) -> ScheduleToggle {
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
            return result(on: isOn, "Could not change the schedule agent", error, tone: .problem, loginItems: true)
        }
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
}
