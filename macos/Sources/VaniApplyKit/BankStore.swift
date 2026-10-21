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
        case capture, fill, edit, delete, `import`, export, settings, draft, resume
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

/// The one clamp for listAnswers paging, used by the protocol and by both stores so the SQLite
/// mirror and the bank.json fallback answer a hostile or sloppy offset/limit identically.
enum PathProbe {
    /// True only when lstat reports ENOENT: nothing at all, not even a symlink. A dangling symlink is
    /// present (lstat succeeds), and so is any path lstat cannot answer for (permissions, ELOOP).
    static func isAbsent(_ url: URL) -> Bool {
        var st = stat()
        return lstat(url.path, &st) != 0 && errno == ENOENT
    }
}

public enum ListPaging {
    public static let maxLimit = 500

    /// offset >= 0 and 1 <= limit <= maxLimit.
    public static func clamped(offset: Int, limit: Int) -> (offset: Int, limit: Int) {
        (max(offset, 0), min(max(limit, 1), maxLimit))
    }
}

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
    public let sqlite: SQLiteBankStore?

    public init(directory: URL, maxActivityBytes: Int = 512 * 1024, lockWait: TimeInterval = 5) {
        self.directory = directory
        self.maxActivityBytes = maxActivityBytes
        self.lockWait = lockWait
        self.sqlite = try? SQLiteBankStore(directory: directory)
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
            mirror(Snapshot(rev: rev, bank: checked.bank))
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
            mirror(Snapshot(rev: next.rev, bank: next.bank))
            return Snapshot(rev: next.rev, bank: next.bank)
        }
    }

    /// Granular atomic row-level patch (upsert and delete) without transferring the entire bank.
    /// bank.json is the source of truth: the patch is applied to it, and the SQLite mirror is then
    /// brought to the same revision, so a mirror that missed an earlier write cannot lose rows and a
    /// missing SQLite file cannot fail a capture.
    public func patch(upsert: [Answer], deleteKeys: [String], baseRev: Int? = nil) throws -> SQLiteBankStore.PatchResult {
        try withLock(exclusive: true) {
            let current = try readUnlocked()
            if let base = baseRev, base != current.rev {
                throw SQLiteBankStore.StoreError.conflict(current.rev)
            }
            let (kept, droppedSecret) = SQLiteBankStore.screen(upsert)
            var bank = current.bank
            for key in deleteKeys { bank.answers[key] = nil }
            for a in kept { bank.answers[a.key] = a }
            // Rows the bank cannot hold are refused here; writing them would leave bank.json unreadable.
            let checked = try BankValidator.validate(bank)
            let next = Snapshot(rev: current.rev + 1, bank: checked.bank)
            try writeUnlocked(StoredFile(rev: next.rev, bank: next.bank))
            mirror(next)
            return SQLiteBankStore.PatchResult(rev: next.rev, droppedSecret: droppedSecret)
        }
    }

    /// Bring the SQLite mirror to the snapshot just written to bank.json (callers hold the lock).
    /// A failure leaves bank.json authoritative and is noted on stderr, with no bank content;
    /// the next write resyncs again, so it never fails the user's write.
    private func mirror(_ snapshot: Snapshot) {
        guard let sqlite else { return }
        do { try sqlite.resync(snapshot) } catch {
            FileHandle.standardError.write(Data("vani: bank.sqlite mirror not updated (\(error)); bank.json is unaffected\n".utf8))
        }
    }

    /// Fast indexed lookup for form matching candidates based on signatures, keys, and labels.
    public func queryCandidates(signatures: [String] = [], keys: [String] = [], labels: [String] = []) throws -> [Answer] {
        try readMirrorOrJSON({ try $0.queryCandidates(signatures: signatures, keys: keys, labels: labels) }) { snap in
            snap.bank.answers.values.filter { a in
                keys.contains(a.key) || a.signatures.contains(where: signatures.contains) || a.labels.contains(where: labels.contains)
            }
        }
    }

    /// Paginated list and search for UI tables. Search matches key, value and labels, ASCII
    /// case-insensitively, with % _ and \ matching themselves, the same on both paths.
    public func listAnswers(offset: Int = 0, limit: Int = 50, search: String? = nil) throws -> SQLiteBankStore.ListResult {
        let (offset, limit) = ListPaging.clamped(offset: offset, limit: limit)
        return try readMirrorOrJSON({ try $0.listAnswers(offset: offset, limit: limit, search: search) }) { snap in
            var all = Array(snap.bank.answers.values)
            if let s = search, !s.trimmingCharacters(in: .whitespaces).isEmpty {
                let needle = Self.asciiLowercased(s)
                all = all.filter { a in
                    ([a.key] + a.value + a.labels).contains { Self.asciiLowercased($0).contains(needle) }
                }
            }
            all.sort { $0.updatedAt > $1.updatedAt }
            let slice = all.dropFirst(offset).prefix(limit)
            return SQLiteBankStore.ListResult(total: all.count, rev: snap.rev, answers: Array(slice))
        }
    }

    /// SQLite's LIKE folds ASCII case only; the JSON path folds the same way so the two agree.
    private static func asciiLowercased(_ s: String) -> String {
        String(String.UnicodeScalarView(s.unicodeScalars.map { ("A"..."Z").contains($0) ? Unicode.Scalar($0.value + 32)! : $0 }))
    }

    /// Answer from the SQLite mirror only while it is at bank.json's revision, checked under the
    /// shared lock (writers hold the exclusive one, so the mirror cannot move mid-read). When it is
    /// behind, ahead or unreadable, answer from bank.json, the source of truth, and afterwards try
    /// to repair the mirror. The repair waits only briefly for the exclusive lock, so a read is
    /// never held up by a writer.
    private func readMirrorOrJSON<T>(_ fromMirror: (SQLiteBankStore) throws -> T, _ fromJSON: (Snapshot) -> T) throws -> T {
        var mirrorStale = false
        let result: T = try withLock(exclusive: false) {
            let snap = try readUnlocked()
            if let sqlite {
                if (try? sqlite.currentRevision()) == snap.rev, let fromSQLite = try? fromMirror(sqlite) { return fromSQLite }
                mirrorStale = true
            }
            return fromJSON(snap)
        }
        if mirrorStale { repairMirror() }
        return result
    }

    /// Best-effort: resync the mirror to bank.json if it is still out of step once the exclusive lock
    /// is held. A failure is noted on stderr (no bank content) and never reaches the reader.
    private func repairMirror() {
        guard let sqlite else { return }
        do {
            try withFileLock(lockURL, exclusive: true, wait: min(lockWait, 0.25), what: "the answer bank") {
                let snap = try readUnlocked()
                if (try? sqlite.currentRevision()) != snap.rev { try sqlite.resync(snap) }
            }
        } catch {
            FileHandle.standardError.write(Data("vani: bank.sqlite mirror not repaired (\(error)); bank.json is unaffected\n".utf8))
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
        do { data = try Data(contentsOf: bankURL) } catch let e as NSError
            where (e.domain == NSCocoaErrorDomain && e.code == CocoaError.fileReadNoSuchFile.rawValue)
                || (e.domain == NSPOSIXErrorDomain && e.code == Int(ENOENT)) {
            // Only a path that is truly absent is a bank never written. A dangling symlink answers
            // "no such file" too, but something is there: it is unreadable, and writing would replace it.
            guard PathProbe.isAbsent(bankURL) else {
                throw BankError("the stored answer bank is unreadable (a link to a missing file); it was left untouched")
            }
            return Snapshot(rev: 0, bank: .empty)
        }
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let rev = BankValidator.strictInt(obj["rev"]), rev >= 0,
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
        try atomicWrite(try enc.encode(file), to: bankURL, what: "the answer bank")
    }

    private func ensureDirectory() throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        _ = chmod(directory.path, 0o700)
    }

    private func withLock<T>(exclusive: Bool, _ body: () throws -> T) throws -> T {
        try ensureDirectory()
        return try withFileLock(lockURL, exclusive: exclusive, wait: lockWait, what: "the answer bank", body)
    }
}

// MARK: - Shared file primitives (the bank and the resume index)

/// Run body under an advisory flock on url, waiting at most `wait` seconds. The retry starts at
/// 1 ms, for the usual sub-millisecond write, and doubles to 64 ms, so a longer hold costs a few
/// wake-ups rather than one every 10 ms; no sleep runs past the deadline.
func withFileLock<T>(_ url: URL, exclusive: Bool, wait: TimeInterval, what: String, _ body: () throws -> T) throws -> T {
    let fd = open(url.path, O_RDWR | O_CREAT | O_CLOEXEC, 0o600)
    if fd < 0 { throw BankError("cannot open the lock for \(what): \(String(cString: strerror(errno)))") }
    _ = fchmod(fd, 0o600)
    defer { close(fd) }
    let deadline = Date().addingTimeInterval(wait)
    var backoff: TimeInterval = 0.001
    while flock(fd, (exclusive ? LOCK_EX : LOCK_SH) | LOCK_NB) != 0 {
        if errno != EWOULDBLOCK && errno != EINTR {
            throw BankError("cannot lock \(what): \(String(cString: strerror(errno)))")
        }
        let left = deadline.timeIntervalSinceNow
        if left <= 0 { throw BankError("\(what) is locked by another process") }
        usleep(useconds_t(min(backoff, left) * 1_000_000))
        backoff = min(backoff * 2, 0.064)
    }
    defer { flock(fd, LOCK_UN) }
    return try body()
}

/// Write data beside dest (mode 0600), fsync it, rename it over dest, and fsync the parent directory,
/// so a reader never sees half a file and crashes cannot lose the rename.
func atomicWrite(_ data: Data, to dest: URL, what: String) throws {
    let directory = dest.deletingLastPathComponent()
    let tmp = directory.appendingPathComponent(".\(dest.lastPathComponent).\(UUID().uuidString).tmp")
    let fd = open(tmp.path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0o600)
    if fd < 0 {
        throw BankError("cannot write \(what): \(String(cString: strerror(errno)))")
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
        throw BankError("cannot write \(what): \(errStr)")
    }
    if fcntl(fd, F_FULLFSYNC) != 0 && fsync(fd) != 0 {
        let err = errno
        close(fd)
        unlink(tmp.path)
        throw BankError("cannot sync \(what) temp file: \(String(cString: strerror(err)))")
    }
    if close(fd) != 0 {
        let err = errno
        unlink(tmp.path)
        throw BankError("cannot close \(what) temp file: \(String(cString: strerror(err)))")
    }
    if rename(tmp.path, dest.path) != 0 {
        let err = errno
        unlink(tmp.path)
        throw BankError("cannot replace \(what): \(String(cString: strerror(err)))")
    }
    let dirFd = open(directory.path, O_RDONLY | O_CLOEXEC)
    if dirFd >= 0 {
        if fcntl(dirFd, F_FULLFSYNC) != 0 {
            _ = fsync(dirFd)
        }
        close(dirFd)
    }
}
