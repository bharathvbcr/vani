import Foundation

/// Whether this launch of the app registers the shared login agent.
///
/// The agent's launchd job is one per user, and the last copy of Vani Apply to
/// register owns it: Safari and Chrome then run that copy's `vani`. A screenshot
/// or fixture rig is built with the real bundle id and agent plist, so launching
/// it would take the agent over. A rig launches with `-VaniApplyNoAgent YES`
/// (`open -n "Vani Apply.app" --args -VaniApplyNoAgent YES`) and leaves the agent
/// to whichever copy registered it.
///
/// It is read from the launch arguments only, never from UserDefaults: a rig has
/// the owner's bundle id, so a persisted default would stop the owner's own app
/// from registering, with nothing on screen to say why.
public enum AgentLaunch: Equatable, Sendable {
    /// Register the agent, and repair or take back a stale registration.
    case register
    /// Leave the agent alone on purpose.
    case leaveAlone

    public static let argument = "-VaniApplyNoAgent"

    /// Read the switch from `arguments` (CommandLine.arguments, executable first).
    /// Only YES, TRUE or 1 (any case) leaves the agent alone; NO, FALSE or 0
    /// registers. The last occurrence wins, as in UserDefaults' argument domain.
    /// Anything else (no value, an empty or unknown one) registers, and `problem`
    /// says why, so a mistyped rig launch is reported rather than looking like the
    /// owner's app.
    public static func from(arguments: [String]) -> (mode: AgentLaunch, problem: String?) {
        let args = arguments.dropFirst()
        guard let i = args.lastIndex(of: argument) else {
            // A near miss (-VaniApplyNoAgent=YES, -vaniapplynoagent) is a rig that meant it.
            if let miss = args.first(where: { $0.lowercased().hasPrefix(argument.lowercased()) }) {
                let shown = miss.count > 60 ? String(miss.prefix(60)) + "…" : miss
                return (.register, "\"\(shown)\" is not \(argument) YES. Registering the login agent.")
            }
            return (.register, nil)
        }
        let next = i + 1 < arguments.count ? arguments[i + 1] : nil
        guard let value = next, !value.hasPrefix("-") else {
            return (.register, "\(argument) has no value; give YES to leave the login agent alone. Registering it.")
        }
        switch value.trimmingCharacters(in: .whitespaces).lowercased() {
        case "yes", "true", "1": return (.leaveAlone, nil)
        case "no", "false", "0": return (.register, nil)
        default:
            let shown = value.count > 40 ? String(value.prefix(40)) + "…" : value
            return (.register, "\(argument) \"\(shown)\" is not YES or NO. Registering the login agent.")
        }
    }
}
