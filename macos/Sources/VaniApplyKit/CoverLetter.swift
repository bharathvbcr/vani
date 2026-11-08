import Foundation

/// Runs `vani cover`'s tracked commands. Build saves a version. Use is what the
/// extension may fetch. The reply paths are under the state directory's letters
/// folder. Letter text and letterhead are not logged.
public struct CoverLetterClient: Sendable {
    public let runner: DraftRunner
    public let timeout: TimeInterval

    public init(runner: DraftRunner, timeout: TimeInterval = 120) {
        self.runner = runner
        self.timeout = timeout
    }

    public struct Saved: Equatable, Sendable {
        public var version: Int
        public var pdf: Bool
        public var code: String?
    }

    public struct Used: Equatable, Sendable {
        public var version: Int
        public var pdf: Bool
        public var textPath: String
        public var pdfPath: String?
    }

    public enum Outcome<Value: Sendable>: Sendable {
        case success(Value)
        case failure(code: String, message: String)
    }

    public func build(_ key: String) -> Outcome<Saved> {
        guard let key = checked(key) else { return .failure(code: "bad_request", message: "this posting's key is not one Vani issued") }
        return run(["jobs", "cover", "build", "--json", key], usesModel: true) { obj in
            let version = obj["version"] as? Int ?? 0
            if version < 1 {
                let code = obj["code"] as? String ?? "failed"
                return .failure(code: code, message: (obj["error"] as? String) ?? "the cover letter was not saved")
            }
            return .success(Saved(version: version, pdf: obj["pdf"] as? Bool ?? false, code: obj["code"] as? String))
        }
    }

    public func use(_ key: String) -> Outcome<Used> {
        guard let key = checked(key) else { return .failure(code: "bad_request", message: "this posting's key is not one Vani issued") }
        return run(["jobs", "cover", "use", "--json", key], usesModel: false) { obj in
            guard let version = obj["version"] as? Int, version >= 1, let text = obj["text_path"] as? String, !text.isEmpty else {
                let code = obj["code"] as? String ?? "failed"
                return .failure(code: code, message: (obj["error"] as? String) ?? "the cover letter could not be used")
            }
            let pdf = obj["pdf_path"] as? String
            return .success(Used(version: version, pdf: obj["pdf"] as? Bool ?? false, textPath: text, pdfPath: pdf?.isEmpty == false ? pdf : nil))
        }
    }

    /// A file under `<state>/letters` that is a version's text or PDF. Symlinks are refused.
    public func readAllowed(_ path: String, max: Int) -> Data? {
        guard let state = runner.stateDir, !state.isEmpty else { return nil }
        let root = URL(fileURLWithPath: state).appendingPathComponent("letters").standardizedFileURL.path
        let file = URL(fileURLWithPath: path).standardizedFileURL.path
        guard file.hasPrefix(root + "/") else { return nil }
        var st = stat()
        guard lstat(file, &st) == 0, (st.st_mode & S_IFMT) == S_IFREG, st.st_size > 0, st.st_size <= max else { return nil }
        return try? Data(contentsOf: URL(fileURLWithPath: file), options: [.mappedIfSafe])
    }

    private func checked(_ key: String) -> String? {
        guard !key.isEmpty, key.utf8.count <= 512, !key.hasPrefix("-"),
              !key.unicodeScalars.contains(where: { $0.value < 0x20 }) else { return nil }
        return key
    }

    private func run<T: Sendable>(_ arguments: [String], usesModel: Bool, _ decode: ([String: Any]) -> Outcome<T>) -> Outcome<T> {
        let invocation = runner.invoke(arguments: arguments, timeout: timeout, usesModel: usesModel,
                                       maxOutputBytes: 256 * 1024, extraEnvironment: ["VANI_NO_KEYCHAIN": "1"])
        switch invocation {
        case let .failure(reply):
            return .failure(code: reply.code ?? "provider", message: reply.error ?? "vani could not be started")
        case let .output(out):
            guard let obj = try? JSONSerialization.jsonObject(with: out.stdout) as? [String: Any] else {
                return .failure(code: "unreadable_reply", message: "vani jobs cover did not reply with JSON")
            }
            if let error = obj["error"] as? String, (obj["version"] as? Int ?? 0) < 1 {
                return .failure(code: obj["code"] as? String ?? "failed", message: error)
            }
            return decode(obj)
        }
    }
}
