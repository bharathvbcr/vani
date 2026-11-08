import Foundation

/// Where `jarvis serve` lives. The Chrome host reads `jarvis.json` beside the
/// bank. There is no network listener: vani talks to the binary on stdin.
public struct JarvisLocation: Equatable, Sendable {
    public var binary: String
    public var root: String
    public var bank: String

    public static let fileName = "jarvis.json"

    public init(binary: String, root: String, bank: String = "") {
        self.binary = binary
        self.root = root
        self.bank = bank
    }

    /// A missing file is "not configured", which the caller reports as a check
    /// that did not run. A present file that is not a small JSON object is the
    /// same, rather than a path guessed from somewhere else.
    public static func load(from directory: URL) -> JarvisLocation? {
        if let binary = ProcessInfo.processInfo.environment["VANI_JARVIS_BINARY"],
           let root = ProcessInfo.processInfo.environment["VANI_JARVIS_ROOT"],
           let loc = JarvisLocation(binary: binary, root: root, bank: ProcessInfo.processInfo.environment["VANI_JARVIS_BANK"] ?? "").checked {
            return loc
        }
        let file = directory.appendingPathComponent(fileName)
        guard let data = try? Data(contentsOf: file), data.count <= 8192,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let binary = obj["binary"] as? String, let root = obj["root"] as? String else { return nil }
        let bank = obj["bank"] as? String ?? ""
        return JarvisLocation(binary: binary, root: root, bank: bank).checked
    }

    var checked: JarvisLocation? {
        guard binary.hasPrefix("/"), root.hasPrefix("/"), bank.isEmpty || bank.hasPrefix("/") else { return nil }
        var isDir: ObjCBool = false
        guard FileManager.default.fileExists(atPath: binary),
              FileManager.default.isExecutableFile(atPath: binary),
              FileManager.default.fileExists(atPath: root, isDirectory: &isDir), isDir.boolValue else { return nil }
        if !bank.isEmpty {
            var bankDir: ObjCBool = false
            if FileManager.default.fileExists(atPath: bank, isDirectory: &bankDir), bankDir.boolValue { return nil }
        }
        return self
    }
}

/// Written after a contact import stored every value, so a later export of the
/// answer bank leaves those values out.
public enum ContactsMarker {
    public static let name = "contacts-copied"

    public static func exists(in directory: URL) -> Bool {
        FileManager.default.fileExists(atPath: directory.appendingPathComponent(name).path)
    }

    public static func write(in directory: URL) {
        let url = directory.appendingPathComponent(name)
        if FileManager.default.fileExists(atPath: url.path) { return }
        try? Data().write(to: url, options: .atomic)
        try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
    }
}

extension Bank {
    /// Answer-bank keys the contact catalog used to live under.
    public static let contactAnswerKeys: Set<String> = [
        "first name", "last name", "email", "phone", "city", "country", "linkedin", "github", "website",
    ]

    public func omittingContactAnswers() -> Bank {
        var copy = self
        for key in Self.contactAnswerKeys { copy.answers.removeValue(forKey: key) }
        return copy
    }
}

/// The host op `contacts`. `request` is the JSON `vani contacts` reads.
public protocol ContactsRunning: Sendable {
    func run(_ request: [String: Any]) -> [String: Any]
}

extension DraftRunner {
    /// One `vani contacts` call. The reply may hold a contact value for the
    /// extension to write; this method does not log it.
    public func contacts(_ request: [String: Any], jarvis: JarvisLocation) -> [String: Any] {
        let notRun: [String: Any] = ["status": "not_run", "note": "the check did not run", "code": "unavailable"]
        guard jarvis.checked != nil else {
            var out = notRun
            out["error"] = "jarvis serve is not configured"
            return out
        }
        let data: Data
        do { data = try JSONSerialization.data(withJSONObject: request) } catch {
            return ["error": "the request is not a contacts request", "code": "E_BAD_REQUEST"]
        }
        let op = request["op"] as? String ?? ""
        let model = op == "choose"
        var extra = [
            "VANI_JARVIS_BINARY": jarvis.binary,
            "VANI_JARVIS_ROOT": jarvis.root,
            "VANI_NO_KEYCHAIN": "1",
        ]
        if !jarvis.bank.isEmpty { extra["VANI_JARVIS_BANK"] = jarvis.bank }
        switch invoke(arguments: ["contacts"], input: data, timeout: model ? 45 : 8,
                      withDecisionKey: model, usesModel: model, extraEnvironment: extra) {
        case let .failure(f):
            return ["error": f.error ?? "jarvis serve did not answer", "code": f.code ?? "unavailable",
                    "status": "not_run", "note": "the check did not run"]
        case let .output(out):
            if let obj = try? JSONSerialization.jsonObject(with: out.stdout) as? [String: Any] { return obj }
            return ["error": "jarvis serve did not answer", "code": "unavailable", "status": "not_run", "note": "the check did not run"]
        }
    }
}

public struct ContactsClient: ContactsRunning {
    public var runner: DraftRunner
    public var jarvis: JarvisLocation
    public init(runner: DraftRunner, jarvis: JarvisLocation) {
        self.runner = runner
        self.jarvis = jarvis
    }
    public func run(_ request: [String: Any]) -> [String: Any] { runner.contacts(request, jarvis: jarvis) }
}
