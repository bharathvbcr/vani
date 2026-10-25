import Foundation

// The one place a PDF is bound to a job's page keys in the browsers' resume store (ResumeStore) and
// logged. Two callers go through it: the native protocol's `resumePut` (a browser's popup chose a file,
// one key) and Resume Studio's "Use for this job" (a tailored version, under the posting's URL and its
// application URL). It binds, writes the activity entry once, and tells the app, so a window that shows
// the saved resume reloads (`Notification.Name.vaniResumeBound`).

extension Notification.Name {
    /// A resume was bound to one or more page keys. `userInfo[ResumeBinding.keysUserInfoKey]` is the
    /// `[String]` of keys that were bound. Posted on the posting thread: observers hop to the main one.
    public static let vaniResumeBound = Notification.Name("dev.vbcr.vani.apply.resumeBound")
}

public enum ResumeBinding {
    public static let keysUserInfoKey = "keys"

    public struct Bound: Equatable, Sendable {
        public var key: String
        public var entry: ResumeStore.Entry
    }

    /// A key the store refused after another was bound (full, or an I/O failure).
    public struct KeyFailure: Equatable, Sendable {
        public var key: String
        public var code: String
        public var message: String
    }

    public struct Outcome: Equatable, Sendable {
        /// Keys now bound, in the order given.
        public var bound: [Bound]
        /// Keys that were not, when at least one was. Never silent: the caller says so.
        public var failures: [KeyFailure]
    }

    /// Bind `data` (a PDF, at most `ResumeStore.maxBytes`) under each of `keys` (already reduced to page
    /// keys; a repeated key is bound once). One `.resume {"chosen":1}` activity entry is written when at
    /// least one key was bound, whatever their number, and the notification is posted with the bound
    /// keys. When none is bound the first failure is thrown and nothing is logged or posted; when only
    /// some are, the rest are in `failures`.
    @discardableResult
    public static func bind(data: Data, name: String, keys: [String], resumes: ResumeStore, store: BankStore?, source: Source,
                            center: NotificationCenter = .default) throws -> Outcome {
        var unique: [String] = []
        for k in keys where !unique.contains(k) { unique.append(k) }
        guard !unique.isEmpty else { throw ResumeStore.Failure("bad_request", "there is no page key to save the resume under") }
        var bound: [Bound] = []
        var failures: [KeyFailure] = []
        var first: ResumeStore.Failure?
        for key in unique {
            do {
                bound.append(Bound(key: key, entry: try resumes.put(key: key, name: name, data: data)))
            } catch let f as ResumeStore.Failure {
                first = first ?? f
                failures.append(KeyFailure(key: key, code: f.code, message: f.message))
            }
        }
        guard !bound.isEmpty else { throw first ?? ResumeStore.Failure("io", "the resume could not be saved") }
        try? store?.log(ActivityEntry(source: source, event: .resume, counts: ["chosen": 1]))
        center.post(name: .vaniResumeBound, object: nil, userInfo: [keysUserInfoKey: bound.map(\.key)])
        return Outcome(bound: bound, failures: failures)
    }

    // MARK: A job's page keys

    /// A URL of the job that Resume Studio binds under, and why one was left out.
    public struct Targets: Equatable, Sendable {
        /// The reduced page keys, posting page first, without repeats.
        public var keys: [String]
        /// One sentence for each address that has no key (never silent).
        public var skipped: [String]
        /// Both addresses reduce to one key (the same page, or one Workday posting).
        public var sameKey: Bool
    }

    /// The keys a job's resume is bound under: `ResumePageKey.postingKey` of `url` and of `applyURL`, which is
    /// what the browser extension looks a page up by. An address with no key (not an http(s) page) is left
    /// out and said so.
    public static func targets(url: String?, applyURL: String?) -> Targets {
        var keys: [String] = []
        var skipped: [String] = []
        var present = 0
        for (label, raw) in [("posting page", url), ("application page", applyURL)] {
            guard let raw = raw?.trimmingCharacters(in: .whitespacesAndNewlines), !raw.isEmpty else { continue }
            present += 1
            if let key = ResumePageKey.postingKey(raw) {
                if !keys.contains(key) { keys.append(key) }
            } else {
                skipped.append("The \(label) has no address a browser resume can be saved under, so none is saved for it.")
            }
        }
        return Targets(keys: keys, skipped: skipped, sameKey: present == 2 && skipped.isEmpty && keys.count == 1)
    }

    // MARK: A version's PDF

    /// Read the PDF a `use` named: an absolute path to a regular file (not a link), at most
    /// `ResumeStore.maxBytes`, starting `%PDF-`, whose SHA-256 is the one vani reported.
    public static func loadPDF(path: String, sha256: String) throws -> Data {
        guard path.hasPrefix("/"), !path.contains("\0"), path.lowercased().hasSuffix(".pdf") else {
            throw ResumeStore.Failure("bad_request", "vani named a PDF path this app will not read")
        }
        var st = stat()
        guard lstat(path, &st) == 0 else { throw ResumeStore.Failure("missing", "the version's PDF is not where vani said it is") }
        guard (st.st_mode & S_IFMT) == S_IFREG else { throw ResumeStore.Failure("bad_request", "vani named a PDF path that is not a plain file") }
        guard Int(st.st_size) <= ResumeStore.maxBytes else {
            throw ResumeStore.Failure("too_large", "the version's PDF is \(Int(st.st_size) / 1024) KB, over the \(ResumeStore.maxBytes / 1024) KB a browser resume can be")
        }
        guard let h = FileHandle(forReadingAtPath: path) else { throw ResumeStore.Failure("missing", "the version's PDF could not be opened") }
        defer { try? h.close() }
        let data = (try? h.read(upToCount: ResumeStore.maxBytes + 1)) ?? Data()
        guard data.count <= ResumeStore.maxBytes else {
            throw ResumeStore.Failure("too_large", "the version's PDF is over the \(ResumeStore.maxBytes / 1024) KB a browser resume can be")
        }
        guard ResumeStore.isPDF(data) else { throw ResumeStore.Failure("bad_request", "the version's file is not a PDF") }
        guard ResumeStore.hex(data) == sha256.lowercased() else {
            throw ResumeStore.Failure("unreadable", "the version's PDF does not match the checksum vani reported; it was changed or damaged")
        }
        return data
    }

    /// Bind a version `use` just bound in vani's store for the browsers: read its PDF, check it, and bind it under
    /// the job's page keys. `skipped` (addresses with no key) is carried into the outcome's note by the caller.
    public static func bindUsed(_ used: ResumeUsed, targets: Targets, resumes: ResumeStore, store: BankStore?,
                                center: NotificationCenter = .default) throws -> Outcome {
        guard let name = ResumeStore.checkedName(used.name) else {
            throw ResumeStore.Failure("bad_request", "the resume's file name was refused")
        }
        guard !targets.keys.isEmpty else {
            throw ResumeStore.Failure("bad_request", targets.skipped.first ?? "this job has no address a browser resume can be saved under")
        }
        let data = try loadPDF(path: used.pdfPath, sha256: used.pdfSHA256)
        return try bind(data: data, name: name, keys: targets.keys, resumes: resumes, store: store, source: .app, center: center)
    }

    /// What was bound, in a sentence, and every address that was not.
    public static func note(for outcome: Outcome, targets: Targets) -> String {
        var parts: [String] = []
        let n = outcome.bound.count
        if targets.sameKey {
            parts.append("Saved for the browsers under this job's address.")
        } else {
            parts.append(n == 1 ? "Saved for the browsers under 1 address of this job." : "Saved for the browsers under \(n) addresses of this job.")
        }
        for f in outcome.failures { parts.append("One address was not saved: \(f.message)") }
        parts += targets.skipped
        return parts.joined(separator: " ")
    }
}
