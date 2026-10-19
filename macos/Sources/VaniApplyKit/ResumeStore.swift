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
// for another job is the wrong one. The bytes leave this store only in a resumeGet reply to
// the browser tab being filled. The file name (it carries the owner's name) is kept here and
// shown in the popup and on the page; it never reaches the activity log.

public final class ResumeStore: @unchecked Sendable {
    /// The largest resume. A Chrome native-messaging reply is capped at 1 MiB and base64 adds a
    /// third, so 512 KiB leaves room for the envelope. extension/src/resume.ts MAX_RESUME_BYTES matches.
    public static let maxBytes = 512 * 1024
    /// Jobs with a resume bound. Rebinding a job already in the index is allowed when full.
    public static let maxEntries = 200
    static let maxNameChars = 200
    /// index.json is a few hundred bytes per job; anything far past that is not ours.
    static let maxIndexBytes = 1024 * 1024

    public struct Entry: Codable, Equatable, Sendable {
        public var file: String
        public var name: String
        public var bytes: Int
        public var sha256: String
        public var pickedAt: String
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
    public static func checkedName(_ raw: Any?) -> String? {
        guard let s = (raw as? String)?.trimmingCharacters(in: .whitespaces), s.count > 4, s.count <= maxNameChars,
              s.lowercased().hasSuffix(".pdf"),
              s.unicodeScalars.allSatisfy({ !CharacterSet.controlCharacters.contains($0) && $0 != "/" && $0 != "\\" })
        else { return nil }
        return s
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
