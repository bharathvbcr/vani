import CryptoKit
import Foundation

// One used cover letter per posting key, beside the resume store:
//
//   <group>/apply/letters/index.json
//   <group>/apply/letters/<sha>.pdf   optional
//   <group>/apply/letters/<sha>.txt   the body
//
// Files are mode 0600. The activity log records an event and a count from the
// caller that binds a letter, never the text or the file name.

public final class CoverStore: @unchecked Sendable {
    public static let maxBytes = 512 * 1024
    public static let maxTextBytes = 32 * 1024
    public static let maxEntries = 200
    static let maxNameChars = 200

    public struct Entry: Codable, Equatable, Sendable {
        public var file: String
        public var textFile: String
        public var name: String
        public var bytes: Int
        public var textBytes: Int
        public var sha256: String
        public var textSHA256: String
        public var pickedAt: String
    }

    struct Index: Codable {
        var version: Int
        var entries: [String: Entry]
    }

    public struct Failure: Error, CustomStringConvertible {
        public let code: String
        public let message: String
        public var description: String { message }
        init(_ code: String, _ message: String) { self.code = code; self.message = message }
    }

    public let directory: URL
    private var indexURL: URL { directory.appendingPathComponent("index.json") }
    private var lockURL: URL { directory.appendingPathComponent("index.lock") }
    private let lockWait: TimeInterval

    public init(directory: URL, lockWait: TimeInterval = 5) {
        self.directory = directory
        self.lockWait = lockWait
    }

    public static func checkedName(_ raw: String) -> String? {
        let s = raw.trimmingCharacters(in: .whitespaces)
        guard s.count > 4, s.count <= maxNameChars, s.lowercased().hasSuffix(".pdf") else { return nil }
        let bad = s.unicodeScalars.contains { scalar in
            scalar == "/" || scalar == "\\" || scalar.value < 0x20
        }
        return bad ? nil : s
    }

    /// Bind the used letter. `pdf` may be nil when drawing failed and the text was kept.
    @discardableResult
    public func put(key: String, name: String, pdf: Data?, body: String) throws -> Entry {
        guard !key.isEmpty, key.utf8.count <= 2048 else { throw Failure("bad_request", "the posting key was refused") }
        let text = Data(body.utf8)
        guard !text.isEmpty, text.count <= Self.maxTextBytes else {
            throw Failure(text.isEmpty ? "bad_request" : "too_large", text.isEmpty ? "the letter has no text" : "the letter is over \(Self.maxTextBytes / 1024) KB")
        }
        if let pdf {
            guard pdf.count <= Self.maxBytes, pdf.starts(with: Data("%PDF-".utf8)) else {
                throw Failure(pdf.count > Self.maxBytes ? "too_large" : "bad_request", "the cover letter is not a PDF of at most \(Self.maxBytes / 1024) KB")
            }
        }
        let display = Self.checkedName(name) ?? "Cover_Letter.pdf"
        return try withLock {
            var index = try readIndex()
            if index.entries[key] == nil && index.entries.count >= Self.maxEntries {
                throw Failure("full", "\(Self.maxEntries) jobs already have a cover letter")
            }
            let textSHA = Self.hex(text)
            let textFile = "\(textSHA).txt"
            try write(text, to: directory.appendingPathComponent(textFile), what: "the cover letter")
            var file = ""
            var sha = ""
            var bytes = 0
            if let pdf {
                sha = Self.hex(pdf)
                file = "\(sha).pdf"
                try write(pdf, to: directory.appendingPathComponent(file), what: "the cover letter PDF")
                bytes = pdf.count
            }
            let old = index.entries[key]
            let entry = Entry(file: file, textFile: textFile, name: display, bytes: bytes, textBytes: text.count,
                              sha256: sha, textSHA256: textSHA, pickedAt: isoNow())
            index.entries[key] = entry
            try writeIndex(index)
            if let old {
                if old.file != file { removeIfUnused(old.file, index) }
                if old.textFile != textFile { removeIfUnused(old.textFile, index) }
            }
            return entry
        }
    }

    public func info(key: String) throws -> Entry? {
        try withLock { try readIndex().entries[key] }
    }

    /// The used letter. `pdf` is nil when this version has no PDF. The body is always present.
    public func get(key: String) throws -> (entry: Entry, pdf: Data?, body: String)? {
        try withLock {
            guard let entry = try readIndex().entries[key] else { return nil }
            let body = try readText(entry)
            let pdf = try readPDF(entry)
            return (entry, pdf, body)
        }
    }

    private func readText(_ entry: Entry) throws -> String {
        guard Self.isHashFile(entry.textFile, ext: "txt") else { throw Failure("unreadable", "the cover letter index is damaged") }
        let data = try readFile(directory.appendingPathComponent(entry.textFile), max: Self.maxTextBytes, what: "cover letter")
        guard Self.hex(data) == entry.textSHA256, let body = String(data: data, encoding: .utf8), !body.isEmpty else {
            throw Failure("unreadable", "the stored cover letter was changed or damaged")
        }
        return body
    }

    private func readPDF(_ entry: Entry) throws -> Data? {
        guard !entry.file.isEmpty else { return nil }
        guard Self.isHashFile(entry.file, ext: "pdf") else { throw Failure("unreadable", "the cover letter index is damaged") }
        let data = try readFile(directory.appendingPathComponent(entry.file), max: Self.maxBytes, what: "cover letter PDF")
        guard data.starts(with: Data("%PDF-".utf8)), Self.hex(data) == entry.sha256 else {
            throw Failure("unreadable", "the stored cover letter PDF was changed or damaged")
        }
        return data
    }

    private func readFile(_ url: URL, max: Int, what: String) throws -> Data {
        let h: FileHandle
        do { h = try FileHandle(forReadingFrom: url) } catch {
            throw Failure("missing", "the \(what) chosen for this job could not be opened")
        }
        defer { try? h.close() }
        let data = (try? h.read(upToCount: max + 1)) ?? Data()
        guard data.count <= max else { throw Failure("too_large", "the stored \(what) is over the cap") }
        return data
    }

    private static func isHashFile(_ s: String, ext: String) -> Bool {
        let suffix = ".\(ext)"
        guard s.hasSuffix(suffix) else { return false }
        let hex = s.dropLast(suffix.count)
        return hex.count == 64 && hex.allSatisfy { ("0"..."9").contains($0) || ("a"..."f").contains($0) }
    }

    private static func hex(_ d: Data) -> String { SHA256.hash(data: d).map { String(format: "%02x", $0) }.joined() }

    private func readIndex() throws -> Index {
        guard FileManager.default.fileExists(atPath: indexURL.path) else { return Index(version: 1, entries: [:]) }
        let data = try readFile(indexURL, max: 1 << 20, what: "cover letter index")
        guard let index = try? JSONDecoder().decode(Index.self, from: data), index.version == 1 else {
            throw Failure("unreadable", "the cover letter index is unreadable")
        }
        return index
    }

    private func writeIndex(_ index: Index) throws {
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        try write(try enc.encode(index), to: indexURL, what: "the cover letter index")
    }

    private func write(_ data: Data, to url: URL, what: String) throws {
        do { try atomicWrite(data, to: url, what: what) } catch { throw Failure("io", String(describing: error)) }
    }

    private func removeIfUnused(_ file: String, _ index: Index) {
        guard !file.isEmpty, !index.entries.values.contains(where: { $0.file == file || $0.textFile == file }) else { return }
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(file))
    }

    private func withLock<T>(_ body: () throws -> T) throws -> T {
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        } catch {
            throw Failure("io", "cannot create the cover letter folder")
        }
        do { return try withFileLock(lockURL, exclusive: true, wait: lockWait, what: "the cover letter index", body) }
        catch let f as Failure { throw f }
        catch { throw Failure("io", String(describing: error)) }
    }
}
