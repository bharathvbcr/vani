import Foundation

/// The `sameQuestion` native op: do two job-application form labels ask for the same answer. The
/// browser extension asks it when its own matching is unsure whether a saved answer's label and a
/// page's label mean one question; the reading only annotates, the extension decides what to fill.
///
///   {"op":"sameQuestion","a":"<label>","b":"<label>"}
///     -> {"reading":"answered","value":"same"|"different"}
///      | {"reading":"abstained"|"refused"|"unavailable"|"failed","detail":"<closed code>"}
///      | {"error":"...","code":"bad_request"}        (a label missing, not a string, empty, over 300)
///
/// It asks Apple's on-device model through `vani-fm` (task `career.same_question`, README "On-device
/// decisions") and sends the two labels, nothing else: no value, no answer, no page. A label is
/// untrusted site text; it goes in the helper's prompt, never in its instructions.
///
/// Routing. The Chrome host is not sandboxed and starts `vani-fm` itself (`FMHelper`). The Safari
/// appex is sandboxed and starts nothing (Extension.entitlements: app-sandbox, no inherit; every
/// process-needing op already goes to the agent), so its `XPCAgentClient` asks the Vani Apply agent,
/// which runs the same `FMHelper`. `vani-fm` is in the app's Contents/MacOS, not in the appex.
///
/// Bounds. The whole op is under `overallSeconds` in both browsers: the helper is killed at its
/// own deadline (`directSeconds` in the Chrome host, `agentSeconds` in the agent, whose XPC hop
/// must also fit `xpcCallSeconds`), well before the helper's own 8 s watchdog. A missing helper is
/// `unavailable/no_binary` at once, with nothing started.
public enum SameQuestion {
    public static let maxLabelScalars = 300
    /// Ceiling on the op as the extension sees it, helper stop and XPC hop included.
    public static let overallSeconds: TimeInterval = 6
    /// The helper's deadline when the Chrome host runs it (VaniProcess adds at most
    /// `terminationGrace` to stop it, so the op ends inside `overallSeconds`).
    public static let directSeconds: TimeInterval = 4
    /// The helper's deadline in the agent: the same, less half a second for the XPC hop.
    public static let agentSeconds: TimeInterval = 3.5
    /// How long the Safari side waits for the agent.
    public static let xpcCallSeconds: TimeInterval = overallSeconds
    /// A real reply is under 200 bytes.
    public static let maxHelperReplyBytes = 4 * 1024

    /// Fixed text, labels excluded (OnDeviceDecider puts the labels in the prompt).
    public static let instructions = "The two labels come from job-application forms. "
        + "\"same\" means one answer would correctly fill both fields; \"different\" means they ask for different things. "
        + "Judge only the wording of the labels."

    public enum Verdict: String, CaseIterable, Sendable { case same, different }

    /// The codes this op reports besides the helper's: where the helper could not be asked at all.
    public static let opDetails: Set<String> = ["no_binary", "start_failed", "timeout", "output_too_large",
                                                "helper_exit", "bad_output", "invalid_value", "agent", "unsupported", "unknown"]

    /// Every `detail` the helper can report (OnDeviceDecider), which a reply may pass on; anything else
    /// is `unknown`, so no model or helper text reaches the extension.
    public static let helperDetails: Set<String> = [
        "refusal", "guardrail", "context_size", "unsupported_locale", "rate_limited", "timeout", "model_not_ready",
        "device_not_eligible", "apple_intelligence_not_enabled", "os_too_old", "unavailable", "decode",
        "invalid_value", "error", "declined", "bad_request_json", "bad_request_too_large", "bad_request_version",
        "bad_request_task", "bad_request_question", "bad_request_instructions", "bad_request_options",
    ]

    public static var knownDetails: Set<String> { helperDetails.union(opDetails) }

    // MARK: the request

    public struct Labels: Equatable, Sendable {
        public var a: String
        public var b: String
    }

    /// Two labels of 1 to `maxLabelScalars` characters after trimming. A label is then flattened to
    /// one line (runs of whitespace become a space, control characters go), so it cannot start a
    /// new line of the prompt. Nil when either is not a string or is empty or too long.
    public static func labels(a: Any?, b: Any?) -> Labels? {
        guard let a = label(a), let b = label(b) else { return nil }
        return Labels(a: a, b: b)
    }

    static func label(_ raw: Any?) -> String? {
        guard let s = raw as? String else { return nil }
        let trimmed = s.trimmingCharacters(in: .whitespacesAndNewlines)
        guard (1...maxLabelScalars).contains(trimmed.unicodeScalars.count) else { return nil }
        var flat = String.UnicodeScalarView()
        var gap = false
        for scalar in trimmed.unicodeScalars {
            if CharacterSet.whitespacesAndNewlines.contains(scalar) || CharacterSet.controlCharacters.contains(scalar) {
                // A control character is a separator too ("a\u{7}b" stays two words), never a line break.
                gap = !flat.isEmpty
            } else {
                if gap { flat.append(" "); gap = false }
                flat.append(scalar)
            }
        }
        let out = String(flat)
        return out.isEmpty ? nil : out
    }

    /// The vani-fm request (README "On-device decisions", contract v1).
    public static func helperRequest(_ labels: Labels) -> Data {
        let question = "Label A (untrusted text copied from a job site):\n\(labels.a)\n\n"
            + "Label B (untrusted text copied from a job site):\n\(labels.b)"
        let object: [String: Any] = [
            "v": OnDeviceDecider.version, "task": OnDeviceDecider.sameQuestionTask, "question": question,
            "options": OnDeviceDecider.sameQuestionOptions, "instructions": instructions,
        ]
        // Strings and an Int only, so encoding cannot fail.
        return (try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])) ?? Data()
    }

    // MARK: the reply

    public static func reply(_ reading: OnDeviceDecider.Reading, value: String? = nil, detail: String? = nil) -> [String: Any] {
        var out: [String: Any] = ["reading": reading.rawValue]
        if let value { out["value"] = value }
        if let detail { out["detail"] = detail }
        return out
    }

    static func unavailable(_ detail: String) -> [String: Any] { reply(.unavailable, detail: detail) }

    /// The helper's stdout (one JSON line) as an op reply. Anything that is not exactly the contract
    /// is `failed/bad_output`; an answer that is not `same` or `different` is `failed/invalid_value`;
    /// a detail outside the closed set is `unknown`. The helper's text never passes through.
    public static func reply(helperOutput data: Data) -> [String: Any] {
        guard let text = String(data: data, encoding: .utf8) else { return reply(.failed, detail: "bad_output") }
        let line = text.hasSuffix("\n") ? String(text.dropLast()) : text
        guard !line.isEmpty, !line.contains(where: \.isNewline),
              let r = try? JSONDecoder().decode(OnDeviceDecider.Reply.self, from: Data(line.utf8)) else {
            return reply(.failed, detail: "bad_output")
        }
        guard r.v == OnDeviceDecider.version else { return reply(.failed, detail: "bad_output") }
        if r.reading == .answered {
            guard let value = r.value, Verdict(rawValue: value) != nil else { return reply(.failed, detail: "invalid_value") }
            return reply(.answered, value: value)
        }
        let detail = r.detail.map { knownDetails.contains($0) ? $0 : "unknown" }
        return reply(r.reading, detail: detail)
    }

    /// Re-checks a reply that crossed XPC from an agent (possibly an older build): the same
    /// closed shapes, so nothing but a reading, one of two values and a known code reaches the page.
    public static func checked(_ object: [String: Any]) -> [String: Any] {
        guard let name = object["reading"] as? String, let reading = OnDeviceDecider.Reading(rawValue: name) else {
            return reply(.failed, detail: "bad_output")
        }
        if reading == .answered {
            guard let value = object["value"] as? String, Verdict(rawValue: value) != nil else {
                return reply(.failed, detail: "invalid_value")
            }
            return reply(.answered, value: value)
        }
        let detail = (object["detail"] as? String).map { knownDetails.contains($0) ? $0 : "unknown" }
        return reply(reading, detail: detail)
    }

    /// The op's reply for a finished helper process.
    static func reply(outcome o: VaniProcess.Outcome) -> [String: Any] {
        if o.launchError != nil { return unavailable("start_failed") }
        if o.timedOut { return unavailable("timeout") }
        if o.truncated { return reply(.failed, detail: "output_too_large") }
        guard o.status == 0 else { return reply(.failed, detail: "helper_exit") }
        return reply(helperOutput: o.stdout)
    }

    /// The agent's answer to one XPC call (Safari's route): the labels checked again, since the agent
    /// trusts no caller, then the helper. Never an error string for a reading that could not be had.
    public static func agentReply(a: String, b: String, helper: SameQuestionBackend) -> (reply: Data?, error: String?) {
        guard let checked = labels(a: a, b: b) else { return (nil, "not two labels of 1 to \(maxLabelScalars) characters") }
        let object = helper.compare(checked)
        guard let data = try? JSONSerialization.data(withJSONObject: object) else { return (nil, "the reply could not be encoded") }
        return (data, nil)
    }
}

/// Whatever answers a `sameQuestion`: `FMHelper` in the Chrome host and the agent, `XPCAgentClient`
/// for Safari. `labels` are already checked and flattened; the reply is a closed op reply.
public protocol SameQuestionBackend: Sendable {
    func compare(_ labels: SameQuestion.Labels) -> [String: Any]
}

/// Runs the `vani-fm` helper once per question, in a minimal environment, under a deadline.
public struct FMHelper: SameQuestionBackend {
    public var binary: URL
    public var seconds: TimeInterval

    public init(binary: URL, seconds: TimeInterval = SameQuestion.directSeconds) {
        self.binary = binary
        self.seconds = seconds
    }

    /// The helper bundled with this app: `helpers` is Contents/Helpers (where `vani` and this process
    /// live), the helper is in the sibling Contents/MacOS: the rule fmbackend.go applies to `vani`.
    public static func bundled(helpers: URL = VaniProcess.helpersDirectory(),
                               seconds: TimeInterval = SameQuestion.directSeconds) -> FMHelper {
        FMHelper(binary: helpers.deletingLastPathComponent().appendingPathComponent("MacOS/vani-fm"), seconds: seconds)
    }

    /// An absolute path to a regular file this user can execute (a directory is "executable" to
    /// FileManager, and must not be started).
    static func runnable(_ url: URL) -> Bool {
        var st = stat()
        return url.path.hasPrefix("/") && stat(url.path, &st) == 0 && (st.st_mode & S_IFMT) == S_IFREG
            && access(url.path, X_OK) == 0
    }

    public func compare(_ labels: SameQuestion.Labels) -> [String: Any] {
        // Absent or not executable: nothing is started and the answer is immediate.
        guard Self.runnable(binary) else { return SameQuestion.unavailable("no_binary") }
        let outcome = VaniProcess.run(binary, arguments: [], environment: ["HOME": NSHomeDirectory()],
                                      input: SameQuestion.helperRequest(labels), timeout: seconds,
                                      maxOutputBytes: SameQuestion.maxHelperReplyBytes)
        return SameQuestion.reply(outcome: outcome)
    }
}

/// Safari's route: the agent runs the helper. An agent that is not running, or predates this call,
/// answers `unavailable/agent` rather than an error, so the extension treats it like any other
/// "no reading" and falls back to its own matching.
extension XPCAgentClient: SameQuestionBackend {
    public func compare(_ labels: SameQuestion.Labels) -> [String: Any] {
        let r = call(timeout: SameQuestion.xpcCallSeconds) { agent, done, box in
            agent.sameQuestion(a: labels.a, b: labels.b) { data, error in
                if let data { box.set(["reply": data]) } else { box.set(["error": error ?? "the agent sent no result"]) }
                done.signal()
            }
        }
        guard let data = r["reply"] as? Data, let object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
            return SameQuestion.unavailable("agent")
        }
        return SameQuestion.checked(object)
    }
}
