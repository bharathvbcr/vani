import CryptoKit
import Foundation

// The resume chosen for each job, for the browser extensions' Fill (extension/src/resume.ts).
// The owner binds a PDF to a job's application page from the popup; the bytes are copied
// here, into the App Group container beside the answer bank, because the sandboxed Safari
// extension cannot read a file elsewhere on disk. Both browsers then read the same copy.
//
//   <group>/apply/resumes/index.json   {"version":1,"entries":{"<pageKey>":{file,name,bytes,sha256,pickedAt}}}
//   <group>/apply/resumes/<sha256>.pdf one file per distinct PDF, shared by the jobs bound to it
//   <group>/apply/resumes/index.lock   the advisory lock every writer holds
//
// A job with nothing bound has nothing: there is no default resume, because a resume made
// for another job is the wrong one. The bytes leave this store in a resumeGet reply to the
// browser tab being filled, and in a temporary copy the owner opens in Preview. The file
// name (it carries the owner's name) is kept here and shown in the popup, on the page and
// in Preview's title; it never reaches the activity log or a model prompt.

public final class ResumeStore: @unchecked Sendable {
    /// The largest resume. A Chrome native-messaging reply is capped at 1 MiB and base64 adds a
    /// third, so 512 KiB leaves room for the envelope. extension/src/resume.ts MAX_RESUME_BYTES matches.
    public static let maxBytes = 512 * 1024
    /// Jobs with a resume bound. Rebinding a job already in the index is allowed when full.
    public static let maxEntries = 200
    /// Preview copies kept under the temporary directory. Older ones are removed.
    public static let maxPreviewCopies = 8
    static let maxNameChars = 200
    static let previewRootName = "VaniApply-resume-preview"
    /// index.json is a few hundred bytes per job; anything far past that is not ours.
    static let maxIndexBytes = 1024 * 1024

    public struct Entry: Codable, Equatable, Sendable {
        public var file: String
        public var name: String
        public var bytes: Int
        public var sha256: String
        public var pickedAt: String
    }

    /// One saved resume, without its bytes. `key` is the page key and is not shown.
    public struct Listed: Equatable, Sendable {
        public var key: String
        public var name: String
        public var bytes: Int
        public var pickedAt: String
        public init(key: String, name: String, bytes: Int, pickedAt: String) {
            self.key = key
            self.name = name
            self.bytes = bytes
            self.pickedAt = pickedAt
        }
    }

    struct Index: Codable {
        var version: Int
        var entries: [String: Entry]
    }

    /// A refusal or failure with the code the extension shows a reason for.
    public struct Failure: Error, CustomStringConvertible {
        public let code: String
        public let message: String
        public var description: String { message }
        init(_ code: String, _ message: String) { self.code = code; self.message = message }
    }

    public let directory: URL
    var indexURL: URL { directory.appendingPathComponent("index.json") }
    var lockURL: URL { directory.appendingPathComponent("index.lock") }
    let lockWait: TimeInterval

    public init(directory: URL, lockWait: TimeInterval = 5) {
        self.directory = directory
        self.lockWait = lockWait
    }

    // MARK: - Checks at the boundary

    /// A bounded, printable file name ending in .pdf, trimmed; nil otherwise.
    /// Format characters (bidi overrides, zero-width, BOM) are refused: Preview's title
    /// is this string, and one of them can make a different name look like a .pdf.
    public static func checkedName(_ raw: Any?) -> String? {
        guard let s = (raw as? String)?.trimmingCharacters(in: .whitespaces), s.count > 4, s.count <= maxNameChars,
              s.lowercased().hasSuffix(".pdf")
        else { return nil }
        let bad = s.unicodeScalars.contains { scalar in
            if scalar == "/" || scalar == "\\" || scalar == "\u{2028}" || scalar == "\u{2029}" { return true }
            switch scalar.properties.generalCategory {
            case .control, .format: return true
            default: return false
            }
        }
        return bad ? nil : s
    }

    static func isPDF(_ d: Data) -> Bool { d.starts(with: Array("%PDF-".utf8)) }

    static func hex(_ d: Data) -> String { SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined() }

    /// The only file names the index may point at, so a tampered index cannot name another file.
    static func isStoredFileName(_ s: String) -> Bool {
        s.count == 68 && s.hasSuffix(".pdf") && s.prefix(64).allSatisfy { ("0"..."9").contains($0) || ("a"..."f").contains($0) }
    }

    // MARK: - Operations

    /// Bind data (a checked PDF) to the job page key. Replaces an earlier binding for the same job.
    @discardableResult
    public func put(key: String, name: String, data: Data) throws -> Entry {
        guard data.count <= Self.maxBytes else { throw Failure("too_large", "the resume is over \(Self.maxBytes / 1024) KB") }
        guard !data.isEmpty, Self.isPDF(data) else { throw Failure("bad_request", "the resume is not a PDF") }
        return try withLock(exclusive: true) {
            var index = try readIndex()
            if index.entries[key] == nil && index.entries.count >= Self.maxEntries {
                throw Failure("full", "\(Self.maxEntries) jobs already have a resume; remove one first")
            }
            let sha = Self.hex(data)
            let file = "\(sha).pdf"
            try write(data, to: directory.appendingPathComponent(file), what: "the resume")
            let old = index.entries[key]
            let entry = Entry(file: file, name: name, bytes: data.count, sha256: sha, pickedAt: isoNow())
            index.entries[key] = entry
            try writeIndex(index)
            if let old, old.file != file { removeIfUnused(old.file, index) }
            return entry
        }
    }

    /// The binding for key, without the bytes; nil when nothing is bound.
    public func info(key: String) throws -> Entry? {
        try withLock(exclusive: false) { try readIndex().entries[key] }
    }

    /// The binding and the PDF, read and checked; nil when nothing is bound.
    public func get(key: String) throws -> (entry: Entry, data: Data)? {
        try withLock(exclusive: false) {
            guard let entry = try readIndex().entries[key] else { return nil }
            guard Self.isStoredFileName(entry.file) else { throw Failure("unreadable", "the resume index is damaged") }
            let url = directory.appendingPathComponent(entry.file)
            guard let size = (try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? NSNumber else {
                throw Failure("missing", "the resume chosen for this job is no longer in the Vani Apply app; choose it again")
            }
            guard size.intValue <= Self.maxBytes else { throw Failure("too_large", "the stored resume is over \(Self.maxBytes / 1024) KB") }
            let h: FileHandle
            do { h = try FileHandle(forReadingFrom: url) } catch {
                throw Failure("missing", "the resume chosen for this job could not be opened; choose it again")
            }
            defer { try? h.close() }
            // Bounded read: one byte past the cap is enough to know it grew.
            let data = (try? h.read(upToCount: Self.maxBytes + 1)) ?? Data()
            guard data.count <= Self.maxBytes else { throw Failure("too_large", "the stored resume is over \(Self.maxBytes / 1024) KB") }
            guard Self.isPDF(data), Self.hex(data) == entry.sha256 else {
                throw Failure("unreadable", "the stored resume was changed or damaged; choose it again")
            }
            return (entry, data)
        }
    }

    /// Every binding, newest first. The bytes stay in the store.
    public func catalog() throws -> [Listed] {
        try withLock(exclusive: false) {
            try readIndex().entries.map { Listed(key: $0.key, name: $0.value.name, bytes: $0.value.bytes, pickedAt: $0.value.pickedAt) }
                .sorted { $0.pickedAt == $1.pickedAt ? $0.key < $1.key : $0.pickedAt > $1.pickedAt }
        }
    }

    /// The binding whose key is a URL's posting key, or its older page key. The first URL that hits wins.
    public static func match(urls: [String], listed: [Listed]) -> Listed? {
        for raw in urls {
            guard let post = ResumePageKey.postingKey(raw) else { continue }
            if let hit = listed.first(where: { $0.key == post }) { return hit }
            if let page = ResumePageKey.pageKey(raw), page != post, let hit = listed.first(where: { $0.key == page }) {
                return hit
            }
        }
        return nil
    }

    /// A copy Preview can open, named for the owner, under a private temporary directory.
    /// The job's URL is not part of the path. A name that is not a safe file name is `Resume.pdf`.
    /// At most `maxPreviewCopies` copies are kept.
    public static func previewCopy(name: String, data: Data) throws -> URL {
        guard !data.isEmpty, data.count <= maxBytes, isPDF(data) else {
            throw Failure(data.count > maxBytes ? "too_large" : "bad_request",
                          data.count > maxBytes ? "the resume is over \(maxBytes / 1024) KB" : "the resume is not a PDF")
        }
        let display = checkedName(name) ?? "Resume.pdf"
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent(previewRootName, isDirectory: true)
        do {
            try fm.createDirectory(at: root, withIntermediateDirectories: true)
            try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: root.path)
        } catch {
            throw Failure("io", "cannot create a folder for the resume preview: \(error.localizedDescription)")
        }
        let dir = root.appendingPathComponent(UUID().uuidString, isDirectory: true)
        let file = dir.appendingPathComponent(display, isDirectory: false)
        let dirPath = dir.standardizedFileURL.path
        guard file.standardizedFileURL.path.hasPrefix(dirPath + "/") else {
            throw Failure("bad_request", "the resume's file name was refused")
        }
        do {
            try fm.createDirectory(at: dir, withIntermediateDirectories: false)
            try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: dir.path)
            try data.write(to: file, options: .atomic)
            try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
        } catch {
            throw Failure("io", "the resume could not be prepared for Preview: \(error.localizedDescription)")
        }
        prunePreviews(root, keeping: dir)
        return file
    }

    private static func prunePreviews(_ root: URL, keeping: URL) {
        let fm = FileManager.default
        // contentsOfDirectory does not mark a directory with a trailing slash, so
        // hasDirectoryPath would see no directories and the cap would never run.
        let keys: Set<URLResourceKey> = [.isDirectoryKey, .contentModificationDateKey]
        guard let kids = try? fm.contentsOfDirectory(at: root, includingPropertiesForKeys: Array(keys), options: [.skipsHiddenFiles]) else { return }
        let dirs = kids.filter { (try? $0.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true }
        guard dirs.count > maxPreviewCopies else { return }
        let sorted = dirs.sorted { a, b in
            let da = (try? a.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate) ?? .distantPast
            let db = (try? b.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate) ?? .distantPast
            return da < db
        }
        let keep = keeping.standardizedFileURL.path
        var extra = dirs.count - maxPreviewCopies
        for dir in sorted where extra > 0 {
            if dir.standardizedFileURL.path == keep { continue }
            try? fm.removeItem(at: dir)
            extra -= 1
        }
    }

    /// Unbind key. True when something was bound.
    @discardableResult
    public func delete(key: String) throws -> Bool {
        try withLock(exclusive: true) {
            var index = try readIndex()
            guard let old = index.entries.removeValue(forKey: key) else { return false }
            try writeIndex(index)
            removeIfUnused(old.file, index)
            return true
        }
    }

    // MARK: - Unlocked primitives (callers hold the lock)

    private func readIndex() throws -> Index {
        guard FileManager.default.fileExists(atPath: indexURL.path) else { return Index(version: 1, entries: [:]) }
        let data: Data
        do {
            let h = try FileHandle(forReadingFrom: indexURL)
            defer { try? h.close() }
            data = (try h.read(upToCount: Self.maxIndexBytes + 1)) ?? Data()
        } catch {
            throw Failure("unreadable", "the resume index could not be opened: \(error.localizedDescription)")
        }
        guard data.count <= Self.maxIndexBytes, let index = try? JSONDecoder().decode(Index.self, from: data), index.version == 1 else {
            throw Failure("unreadable", "the resume index is unreadable; it was left untouched")
        }
        return index
    }

    private func writeIndex(_ index: Index) throws {
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        try write(try enc.encode(index), to: indexURL, what: "the resume index")
    }

    private func write(_ data: Data, to url: URL, what: String) throws {
        do { try atomicWrite(data, to: url, what: what) } catch { throw Failure("io", String(describing: error)) }
    }

    private func removeIfUnused(_ file: String, _ index: Index) {
        guard Self.isStoredFileName(file), !index.entries.values.contains(where: { $0.file == file }) else { return }
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(file))
    }

    private func withLock<T>(exclusive: Bool, _ body: () throws -> T) throws -> T {
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        } catch {
            throw Failure("io", "cannot create the resume folder: \(error.localizedDescription)")
        }
        do { return try withFileLock(lockURL, exclusive: exclusive, wait: lockWait, what: "the resume index", body) }
        catch let f as Failure { throw f }
        catch { throw Failure("io", String(describing: error)) }
    }
}
