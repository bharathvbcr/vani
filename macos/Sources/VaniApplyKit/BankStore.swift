import Foundation

/// The bank as stored, with the revision a writer must name to replace it.
public struct Snapshot: Equatable, Sendable {
    public let rev: Int
    public let bank: Bank

    public init(rev: Int, bank: Bank) {
        self.rev = rev
        self.bank = bank
    }
}

public enum PutResult: Equatable, Sendable {
    case stored(rev: Int, droppedSecret: Int)
    /// Someone else wrote first; this is what they wrote.
    case conflict(Snapshot)
}

/// Who wrote, for the activity log.
public enum Source: String, Codable, Sendable {
    case chrome, safari, app
}

/// One activity-log line. It never holds answer values or question text.
public struct ActivityEntry: Codable, Equatable, Sendable {
    public enum Event: String, Codable, CaseIterable, Sendable {
        case capture, fill, edit, delete, `import`, export, settings, draft
    }
    public var at: String
    public var source: Source
    public var event: Event
    public var host: String?
    public var counts: [String: Int]?

    public init(at: String = isoNow(), source: Source, event: Event, host: String? = nil, counts: [String: Int]? = nil) {
        self.at = at
        self.source = source
        self.event = event
        self.host = host
        self.counts = counts
    }
}

// The single shared answer bank, used by three processes: the Chrome native
// host, the sandboxed Safari extension and the control app. Writes are
// serialized with an advisory lock on a sibling file and land atomically
// (write to a temporary file, then rename), so a reader never sees half a
// bank. Every write names the revision it read (compare-and-swap), so two
// browsers capturing at once cannot silently drop each other's answers.
// A corrupt file is reported, never replaced: the next write would otherwise
// erase every answer.

public final class BankStore: @unchecked Sendable {
    public let directory: URL
    var bankURL: URL { directory.appendingPathComponent("bank.json") }
    var lockURL: URL { directory.appendingPathComponent("bank.lock") }
    var activityURL: URL { directory.appendingPathComponent("activity.jsonl") }

    /// Activity log size at which it is rotated to activity.1.jsonl.
    let maxActivityBytes: Int
    /// How long a lock may be waited for. A writer holds it for one small file
    /// write, so five seconds of contention means something is stuck.
    let lockWait: TimeInterval

    public init(directory: URL, maxActivityBytes: Int = 512 * 1024, lockWait: TimeInterval = 5) {
        self.directory = directory
        self.maxActivityBytes = maxActivityBytes
        self.lockWait = lockWait
    }

    /// What identifies the activity log's contents without reading it: each
    /// file's size and modification time. A changed stamp means
    /// recentActivity() would return something new. The log is appended by
    /// every process without changing the bank's revision, so the revision
    /// alone cannot say that.
    public struct ActivityStamp: Equatable, Sendable {
        var sizes: [Int64?]
        var modified: [Date?]
    }

    public func activityStamp() -> ActivityStamp {
        var stamp = ActivityStamp(sizes: [], modified: [])
        for url in [activityURL, directory.appendingPathComponent("activity.1.jsonl")] {
            let a = try? FileManager.default.attributesOfItem(atPath: url.path)
            stamp.sizes.append((a?[.size] as? NSNumber)?.int64Value)
            stamp.modified.append(a?[.modificationDate] as? Date)
        }
        return stamp
    }

    /// The store in the App Group container shared by the app and both browser extensions.
    public static func shared() throws -> BankStore {
        try BankStore(directory: AppGroup.storeDirectory())
    }

    private struct StoredFile: Codable {
        let rev: Int
        let bank: Bank
    }

    public func read() throws -> Snapshot {
        try withLock(exclusive: false) { try readUnlocked() }
    }

    public func put(baseRev: Int, bank raw: Any) throws -> PutResult {
        let checked = try BankValidator.validate(raw)
        return try withLock(exclusive: true) {
            let current = try readUnlocked()
            if current.rev != baseRev { return .conflict(current) }
            let rev = current.rev + 1
            try writeUnlocked(StoredFile(rev: rev, bank: checked.bank))
            return .stored(rev: rev, droppedSecret: checked.droppedSecret)
        }
    }

    public func put(baseRev: Int, bank: Bank) throws -> PutResult {
        try put(baseRev: baseRev, bank: bank.jsonObject())
    }

    /// Read, change and write under one lock, for the app's own edits.
    @discardableResult
    public func update(_ change: (inout Bank) throws -> Void) throws -> Snapshot {
        try withLock(exclusive: true) {
            let current = try readUnlocked()
            var bank = current.bank
            try change(&bank)
            let checked = try BankValidator.validate(bank)
            let next = StoredFile(rev: current.rev + 1, bank: checked.bank)
            try writeUnlocked(next)
            return Snapshot(rev: next.rev, bank: next.bank)
        }
    }

    /// Move an unreadable bank aside (bank.corrupt-<time>.json) so the app can start fresh. Never deletes it.
    public func quarantineCorruptBank() throws -> URL? {
        try withLock(exclusive: true) {
            guard FileManager.default.fileExists(atPath: bankURL.path) else { return nil }
            do {
                _ = try readUnlocked()
                return nil
            } catch {
                let stamp = isoNow().replacingOccurrences(of: ":", with: "-")
                let dest = directory.appendingPathComponent("bank.corrupt-\(stamp).json")
                try FileManager.default.moveItem(at: bankURL, to: dest)
                let dirFd = open(directory.path, O_RDONLY | O_CLOEXEC)
                if dirFd >= 0 {
                    if fcntl(dirFd, F_FULLFSYNC) != 0 { _ = fsync(dirFd) }
                    close(dirFd)
                }
                return dest
            }
        }
    }

    public func log(_ entry: ActivityEntry) throws {
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        var line = try enc.encode(entry)
        line.append(0x0A)
        try withLock(exclusive: true) {
            try ensureDirectory()
            let fm = FileManager.default
            if let size = (try? fm.attributesOfItem(atPath: activityURL.path)[.size] as? NSNumber)?.intValue,
               size + line.count > maxActivityBytes {
                let old = directory.appendingPathComponent("activity.1.jsonl")
                try? fm.removeItem(at: old)
                try fm.moveItem(at: activityURL, to: old)
                _ = chmod(old.path, 0o600)
                let dirFd = open(directory.path, O_RDONLY | O_CLOEXEC)
                if dirFd >= 0 {
                    if fcntl(dirFd, F_FULLFSYNC) != 0 { _ = fsync(dirFd) }
                    close(dirFd)
                }
            }
            if !fm.fileExists(atPath: activityURL.path) {
                guard fm.createFile(atPath: activityURL.path, contents: nil, attributes: [.posixPermissions: 0o600]) else {
                    throw BankError("cannot create \(activityURL.lastPathComponent)")
                }
            }
            _ = chmod(activityURL.path, 0o600)
            let h = try FileHandle(forWritingTo: activityURL)
            defer { try? h.close() }
            try h.seekToEnd()
            try h.write(contentsOf: line)
            try? h.synchronize()
        }
    }

    /// The newest activity entries first, at most `limit`. Malformed lines are skipped.
    public func recentActivity(limit: Int = 500) throws -> [ActivityEntry] {
        try withLock(exclusive: false) {
            let dec = JSONDecoder()
            var out: [ActivityEntry] = []
            for url in [activityURL, directory.appendingPathComponent("activity.1.jsonl")] {
                guard let data = try? Data(contentsOf: url) else { continue }
                let lines = data.split(separator: 0x0A)
                for line in lines.reversed() {
                    if out.count >= limit { return out }
                    if let e = try? dec.decode(ActivityEntry.self, from: Data(line)) { out.append(e) }
                }
            }
            return out
        }
    }

    // MARK: - Unlocked primitives (callers hold the lock)

    private func readUnlocked() throws -> Snapshot {
        let data: Data
        do { data = try Data(contentsOf: bankURL) } catch CocoaError.fileReadNoSuchFile {
            return Snapshot(rev: 0, bank: .empty)
        } catch let e as NSError where e.domain == NSPOSIXErrorDomain && e.code == Int(ENOENT) {
            return Snapshot(rev: 0, bank: .empty)
        }
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let revNum = obj["rev"], BankValidator.isNumber(revNum), let rev = (revNum as? NSNumber)?.intValue, rev >= 0,
              let rawBank = obj["bank"] else {
            throw BankError("the stored answer bank is unreadable; it was left untouched")
        }
        let checked: BankValidator.Result
        do { checked = try BankValidator.validate(rawBank) } catch {
            throw BankError("the stored answer bank is invalid (\(error)); it was left untouched")
        }
        return Snapshot(rev: rev, bank: checked.bank)
    }

    private func writeUnlocked(_ file: StoredFile) throws {
        try ensureDirectory()
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        let data = try enc.encode(file)
        let tmp = directory.appendingPathComponent(".bank.\(UUID().uuidString).tmp")
        let fd = open(tmp.path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0o600)
        if fd < 0 {
            throw BankError("cannot write the answer bank: \(String(cString: strerror(errno)))")
        }
        _ = fchmod(fd, 0o600)
        var writeErr: Int32 = 0
        let written = data.withUnsafeBytes { buf -> Int in
            guard let ptr = buf.baseAddress else { return 0 }
            var total = 0
            while total < data.count {
                let n = write(fd, ptr + total, data.count - total)
                if n < 0 {
                    if errno == EINTR { continue }
                    writeErr = errno
                    return -1
                }
                total += n
            }
            return total
        }
        if written != data.count {
            close(fd)
            unlink(tmp.path)
            let errStr = writeErr != 0 ? String(cString: strerror(writeErr)) : "short write (\(written) of \(data.count) bytes)"
            throw BankError("cannot write the answer bank: \(errStr)")
        }
        if fcntl(fd, F_FULLFSYNC) != 0 && fsync(fd) != 0 {
            let err = errno
            close(fd)
            unlink(tmp.path)
            throw BankError("cannot sync the answer bank temp file: \(String(cString: strerror(err)))")
        }
        if close(fd) != 0 {
            let err = errno
            unlink(tmp.path)
            throw BankError("cannot close the answer bank temp file: \(String(cString: strerror(err)))")
        }
        if rename(tmp.path, bankURL.path) != 0 {
            let err = errno
            unlink(tmp.path)
            throw BankError("cannot replace the answer bank: \(String(cString: strerror(err)))")
        }
        let dirFd = open(directory.path, O_RDONLY | O_CLOEXEC)
        if dirFd >= 0 {
            if fcntl(dirFd, F_FULLFSYNC) != 0 {
                _ = fsync(dirFd)
            }
            close(dirFd)
        }
    }

    private func ensureDirectory() throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        _ = chmod(directory.path, 0o700)
    }

    private func withLock<T>(exclusive: Bool, _ body: () throws -> T) throws -> T {
        try ensureDirectory()
        let fd = open(lockURL.path, O_RDWR | O_CREAT | O_CLOEXEC, 0o600)
        if fd < 0 { throw BankError("cannot open the bank lock: \(String(cString: strerror(errno)))") }
        _ = fchmod(fd, 0o600)
        defer { close(fd) }
        // Bounded wait (lockWait). The retry starts at 1 ms, for the usual sub-millisecond write, and
        // doubles to 64 ms, so a longer hold costs a few wake-ups rather than
        // one every 10 ms; no sleep runs past the deadline.
        let deadline = Date().addingTimeInterval(lockWait)
        var backoff: TimeInterval = 0.001
        while flock(fd, (exclusive ? LOCK_EX : LOCK_SH) | LOCK_NB) != 0 {
            if errno != EWOULDBLOCK && errno != EINTR {
                throw BankError("cannot lock the answer bank: \(String(cString: strerror(errno)))")
            }
            let left = deadline.timeIntervalSinceNow
            if left <= 0 { throw BankError("the answer bank is locked by another process") }
            usleep(useconds_t(min(backoff, left) * 1_000_000))
            backoff = min(backoff * 2, 0.064)
        }
        defer { flock(fd, LOCK_UN) }
        return try body()
    }
}
