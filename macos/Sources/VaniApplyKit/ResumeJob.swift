import CryptoKit
import Foundation

// The agent's keyed resume job (`JobKind.resumeJob`): the resume for a posting Vani tracks, built by the
// one builder (`vani jobs resume build`, Opus, saved as the next version of that job's resume) and bound for
// applying (`vani jobs resume use`, which checks the version afresh and refuses a stale or unchecked one).
//
// Request  {"key": "<posting key>"}  and nothing else.
// Reply    the stateless `vani resume` shape (pdf base64, name, bytes, pages, selection, reason, notes, ...) plus
//          `key` and `version`, so the extension attaches and binds it exactly as it does a stateless build; or
//          {"error", "code"} (vani's own refusal passed through, or one of this file's), which names `version`
//          when a version was saved and then not used, so the owner learns that the money bought something.
//
// The PDF is read from the `pdf_path` vani names, not carried in vani's reply. The agent is the owner's own
// process (Support/Group.entitlements holds only the App Group, no sandbox) and already spawns the `vani`
// that wrote that file, so it can read it. The path is still checked before it is opened: it must be the
// store's own `<state>/resumes/<sha256(key)>/vN.pdf` for this version, a regular file (not a link), at most
// ResumeStore.maxBytes, and its SHA-256 must be the one `use` reported. Anything else is refused, never sent on.
// Nothing here logs a name, a path or a byte of the PDF.

extension DraftRunner {
    /// `vani jobs resume build` is bounded by vani at 360 s (rkbresume.DefaultDeadline); the agent allows 375, as for `resume`.
    public static let resumeBuildTimeout: TimeInterval = 375
    /// `vani jobs resume use` is bounded by vani at 10 s; the agent allows 25.
    public static let resumeUseTimeout: TimeInterval = 25

    /// Run the keyed job. Never throws: every failure is a reply with a code.
    func runResumeJob(input: Data) -> Data {
        let reply = resumeJobReply(input: input)
        return (try? JSONSerialization.data(withJSONObject: reply)) ?? Data(#"{"error":"encoding failed","code":"provider"}"#.utf8)
    }

    private func resumeJobReply(input: Data) -> [String: Any] {
        guard let request = (try? JSONSerialization.jsonObject(with: input)) as? [String: Any], request.count == 1,
              let key = JobLinks.checkedKey(request["key"]) else {
            return Self.refusal("bad_request", "a keyed resume job needs a posting key and nothing else")
        }

        // 1. Build and save a version. A refusal (busy, no_model, kb_changed, timeout, ...) is vani's own, passed on.
        let built: [String: Any]
        switch step(["jobs", "resume", "build", "--json", key], name: "jobs resume build", timeout: Self.resumeBuildTimeout, usesModel: true) {
        case let .refused(r): return r
        case let .reply(r): built = r
        }
        if built["error"] is String { return built }
        guard let version = BankValidator.strictInt(built["version"]), version > 0 else {
            return Self.refusal("bad_reply", "vani built a resume but named no version")
        }

        // 2. Use it only if the build says it is usable. `use` checks again; this keeps a version that plainly is not from being asked for.
        if let why = Self.notUsable(built) {
            return Self.refusal(why.code, "the built version v\(version) was not used: \(why.message)", version: version)
        }

        // 3. Bind it for applying. No model, so no key is read for it.
        let used: [String: Any]
        switch step(["jobs", "resume", "use", "--json", key, "v\(version)"], name: "jobs resume use", timeout: Self.resumeUseTimeout, usesModel: false) {
        case let .refused(r): return Self.tagged(r, version: version)
        case let .reply(r): used = r
        }
        if used["error"] is String { return Self.tagged(used, version: version) }
        guard BankValidator.strictInt(used["version"]) == version,
              let name = ResumeStore.checkedName(used["name"]),
              let path = used["pdf_path"] as? String,
              let sha = used["pdf_sha256"] as? String, Self.isHex(sha, length: 64) else {
            return Self.refusal("bad_reply", "vani's reply for v\(version) was not understood, so it was not used", version: version)
        }

        // 4. Read the PDF vani wrote, and check it is the one `use` vouched for.
        let pdf: Data
        switch Self.readResumePDF(path: path, version: version, sha256: sha, stateDir: stateDir) {
        case let .failure(f): return Self.refusal(f.code, f.message, version: version)
        case let .success(data): pdf = data
        }

        var reply: [String: Any] = [
            "pdf": pdf.base64EncodedString(), "name": name, "bytes": pdf.count, "key": key, "version": version,
        ]
        if let pages = BankValidator.strictInt(built["pages"]), pages >= 0 { reply["pages"] = pages }
        if let source = built["source"] as? String { reply["selection"] = String(source.prefix(40)) }
        if let reason = built["reason"] as? String, !reason.isEmpty { reply["reason"] = String(reason.prefix(400)) }
        for field in ["notes", "unverified_dropped"] {
            if let list = built[field] as? [String], !list.isEmpty { reply[field] = list.prefix(40).map { String($0.prefix(300)) } }
        }
        return reply
    }

    // MARK: - one vani call

    private enum Step {
        /// vani's own JSON object (it may hold "error"), or what this runner says when vani could not run or spoke no JSON.
        case reply([String: Any])
        case refused([String: Any])
    }

    private func step(_ arguments: [String], name: String, timeout: TimeInterval, usesModel: Bool) -> Step {
        switch invoke(arguments: arguments, timeout: timeout, usesModel: usesModel) {
        case let .failure(f):
            return .refused(f.asJSONObject)
        case let .output(out):
            if let object = (try? JSONSerialization.jsonObject(with: out.stdout)) as? [String: Any] { return .reply(object) }
            if usesModel, keys != .noKey, let refusal = AnthropicAccount.refusal(in: out.stderr) {
                return .refused(Self.refusal("account", refusal))
            }
            return .refused(Self.refusal("provider", "vani \(name) exited \(out.status ?? -1) without a reply"))
        }
    }

    // MARK: - what is usable

    /// Why a build's reply is not a version to use, or nil when it is: the fit passed or soft-passed, the
    /// eligibility is current, and there is a PDF. (The store refuses the same for `use`; this is the
    /// early read of the reply the build already gave.)
    static func notUsable(_ built: [String: Any]) -> (code: String, message: String)? {
        let checks = built["checks"] as? [String: Any] ?? [:]
        let fit = checks["fit"] as? String ?? "unknown"
        guard fit == "passed" || fit == "soft" else {
            return ("fit", "its page fit did not pass (\(String(fit.prefix(40))))")
        }
        let eligibility = checks["eligibility"] as? String ?? "unknown"
        guard eligibility == "current" else {
            return (eligibility == "stale" ? "stale" : "unchecked", "its eligibility is \(String(eligibility.prefix(40))), not current")
        }
        guard let path = built["pdf_path"] as? String, !path.isEmpty else {
            return ("not_usable", "it has no PDF")
        }
        return nil
    }

    // MARK: - the PDF

    /// The stored PDF of one version, read within the cap and checked against the hash `use` reported.
    static func readResumePDF(path: String, version: Int, sha256 expected: String, stateDir: String?) -> Result<Data, ResumeJobRefusal> {
        func refuse(_ code: String, _ message: String) -> Result<Data, ResumeJobRefusal> { .failure(ResumeJobRefusal(code: code, message: message)) }
        let url = URL(fileURLWithPath: path)
        let parts = url.pathComponents
        guard path.hasPrefix("/"), !path.contains("\0"), !parts.contains(".."), !parts.contains("."),
              parts.count >= 4, parts[parts.count - 1] == "v\(version).pdf",
              isHex(parts[parts.count - 2], length: 64), parts[parts.count - 3] == "resumes" else {
            return refuse("bad_reply", "vani named a PDF outside its resume store, so it was not read")
        }
        if let stateDir, !stateDir.isEmpty {
            let want = URL(fileURLWithPath: stateDir, isDirectory: true).appendingPathComponent("resumes", isDirectory: true).resolvingSymlinksInPath().path
            let have = url.deletingLastPathComponent().deletingLastPathComponent().resolvingSymlinksInPath().path
            guard want == have else { return refuse("bad_reply", "vani named a PDF outside its state folder, so it was not read") }
        }
        // attributesOfItem does not follow a link: a symlink reads as one, and is refused.
        guard let attributes = try? FileManager.default.attributesOfItem(atPath: path),
              attributes[.type] as? FileAttributeType == .typeRegular,
              let size = (attributes[.size] as? NSNumber)?.intValue else {
            return refuse("not_found", "the version's PDF is not a readable file")
        }
        guard size <= ResumeStore.maxBytes else {
            return refuse("too_large", "the resume is over \(ResumeStore.maxBytes / 1024) KB")
        }
        let data: Data
        do {
            let handle = try FileHandle(forReadingFrom: url)
            defer { try? handle.close() }
            // One byte past the cap: a file that grew since the size was read is still refused, never read whole.
            data = try handle.read(upToCount: ResumeStore.maxBytes + 1) ?? Data()
        } catch {
            return refuse("not_found", "the version's PDF could not be read")
        }
        guard data.count <= ResumeStore.maxBytes else { return refuse("too_large", "the resume is over \(ResumeStore.maxBytes / 1024) KB") }
        guard data.starts(with: Array("%PDF-".utf8)) else { return refuse("damaged", "the version's file is not a PDF, so it was not used") }
        let actual = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        guard actual == expected.lowercased() else {
            return refuse("damaged", "the version's PDF does not match its recorded hash, so it was not used")
        }
        return .success(data)
    }

    // MARK: - replies

    static func refusal(_ code: String, _ message: String, version: Int? = nil) -> [String: Any] {
        var out: [String: Any] = ["error": message, "code": code]
        if let version { out["version"] = version }
        return out
    }

    /// vani's refusal with the version it concerns, unless it already names one.
    static func tagged(_ reply: [String: Any], version: Int) -> [String: Any] {
        var out = reply
        if out["version"] == nil { out["version"] = version }
        return out
    }

    private static func isHex(_ s: String, length: Int) -> Bool {
        s.utf8.count == length && s.utf8.allSatisfy { ($0 >= 0x30 && $0 <= 0x39) || ($0 >= 0x61 && $0 <= 0x66) || ($0 >= 0x41 && $0 <= 0x46) }
    }
}

/// Why a stored PDF was not read.
struct ResumeJobRefusal: Error, Equatable {
    let code: String
    let message: String
}
