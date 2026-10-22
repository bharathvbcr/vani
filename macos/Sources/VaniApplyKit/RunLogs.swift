import Foundation

// What a run leaves behind in <state dir>/runs, and how the app keeps the logs of
// the runs it starts. The run record side is vani's (cmd/vani/runhistory.go keeps
// the newest 30 finished runs in runs/history/<id>.json and prunes them); the
// manual log side is this app's, because this app opens manual.log (VaniProcess
// .launch truncates it) and so is the one place that can keep the old one first.
// The two never touch each other's files: vani prunes `*.json` named by run id,
// this prunes `manual-<time>.log`.

/// The run files the Pipeline tab can open or reveal, only the ones that exist.
public struct RunFiles: Equatable, Sendable {
    /// runs/manual.log: the output of the last run this app started. Absent
    /// until a run was started here; each run replaces it (the old one is kept
    /// in `historyFolder`).
    public var manualLog: URL?
    /// runs/last.json: the latest run's record, which `vani status` reads.
    public var record: URL?
    /// runs/history: every finished run's record, and the logs this app rotated.
    public var historyFolder: URL?

    public var isEmpty: Bool { manualLog == nil && record == nil && historyFolder == nil }

    public static func record(stateDir: String) -> URL {
        URL(fileURLWithPath: stateDir).appendingPathComponent("runs/last.json")
    }

    public static func historyFolder(stateDir: String) -> URL {
        URL(fileURLWithPath: stateDir).appendingPathComponent("runs/history", isDirectory: true)
    }

    /// The files that exist now. An empty state folder name has none.
    public static func existing(stateDir: String, fileManager fm: FileManager = .default) -> RunFiles {
        guard !stateDir.isEmpty else { return RunFiles() }
        func file(_ url: URL) -> URL? {
            var isDir: ObjCBool = false
            return fm.fileExists(atPath: url.path, isDirectory: &isDir) && !isDir.boolValue ? url : nil
        }
        var folder: ObjCBool = false
        let history = historyFolder(stateDir: stateDir)
        return RunFiles(manualLog: file(ManualRunLauncher.logURL(stateDir: stateDir)),
                        record: file(record(stateDir: stateDir)),
                        historyFolder: fm.fileExists(atPath: history.path, isDirectory: &folder) && folder.boolValue ? history : nil)
    }
}

/// Keeps the previous manual run's log when the app starts the next one.
public enum ManualRunLog {
    /// Rotated logs kept; older ones are removed.
    public static let maxKept = 20
    /// Rotated logs kept together, and the largest one kept at all. A log is
    /// some KB (vani prints bounded lines); this keeps a runaway one from
    /// filling the disk.
    public static let maxKeptBytes = 8 << 20

    static let prefix = "manual-"
    static let suffix = ".log"

    public enum Rotation: Equatable, Sendable {
        /// No log, an empty one, or something that is not a regular file (a
        /// symlink is refused when the next run opens it).
        case nothingToKeep
        /// The previous log, now at this URL.
        case kept(URL)
        /// The previous log was larger than the bound (this many bytes) and was
        /// removed, not kept.
        case tooLarge(Int)
    }

    /// Moves runs/manual.log to runs/history/manual-<UTC time of its last write>.log,
    /// then prunes the rotated logs to `maxKept` and `maxBytes`. Call it just before
    /// the next run opens the log. A rename that fails throws: the run would
    /// otherwise destroy the log it is meant to keep. Pruning that fails does not
    /// throw (a leftover old log is harmless, and the next rotation prunes again).
    @discardableResult
    public static func rotate(stateDir: String, fileManager fm: FileManager = .default,
                              maxKept: Int = ManualRunLog.maxKept, maxBytes: Int = ManualRunLog.maxKeptBytes) throws -> Rotation {
        let log = ManualRunLauncher.logURL(stateDir: stateDir)
        let attrs: [FileAttributeKey: Any]
        do {
            attrs = try fm.attributesOfItem(atPath: log.path)
        } catch let error as CocoaError where error.code == .fileReadNoSuchFile || error.code == .fileNoSuchFile {
            return .nothingToKeep
        }
        guard attrs[.type] as? FileAttributeType == .typeRegular else { return .nothingToKeep }
        let size = (attrs[.size] as? NSNumber)?.intValue ?? 0
        if size == 0 {
            try fm.removeItem(at: log)
            return .nothingToKeep
        }
        if size > maxBytes {
            try fm.removeItem(at: log)
            return .tooLarge(size)
        }
        let folder = RunFiles.historyFolder(stateDir: stateDir)
        try fm.createDirectory(at: folder, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let stamp = stampFormatter().string(from: (attrs[.modificationDate] as? Date) ?? Date())
        var dest = folder.appendingPathComponent("\(prefix)\(stamp)\(suffix)")
        // Two logs written in the same second: "~NN" sorts after the bare name, so newest stays last.
        var n = 2
        while fm.fileExists(atPath: dest.path) {
            guard n < 100 else { throw BankError("cannot keep the previous run's log: too many logs from the same second") }
            dest = folder.appendingPathComponent("\(prefix)\(stamp)~\(String(format: "%02d", n))\(suffix)")
            n += 1
        }
        do { try fm.moveItem(at: log, to: dest) } catch {
            throw BankError("cannot keep the previous run's log: \(error.localizedDescription)")
        }
        _ = prune(folder: folder, fileManager: fm, maxKept: maxKept, maxBytes: maxBytes)
        return .kept(dest)
    }

    /// Removes rotated logs past the newest `maxKept` or past `maxBytes` together;
    /// the newest is kept whatever its size. Other files in the folder (vani's
    /// run records) are never touched. Returns how many it removed and how many it
    /// could not.
    @discardableResult
    static func prune(folder: URL, fileManager fm: FileManager, maxKept: Int, maxBytes: Int) -> (removed: Int, failed: Int) {
        let keys: [URLResourceKey] = [.isRegularFileKey, .fileSizeKey]
        guard let urls = try? fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: keys) else { return (0, 0) }
        let logs = urls.filter { $0.lastPathComponent.hasPrefix(prefix) && $0.lastPathComponent.hasSuffix(suffix) }
            .filter { (try? $0.resourceValues(forKeys: [.isRegularFileKey]).isRegularFile) == true }
            .sorted { $0.lastPathComponent > $1.lastPathComponent }
        var total = 0, removed = 0, failed = 0
        for (i, url) in logs.enumerated() {
            let size = (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
            total += size
            if i < maxKept && (i == 0 || total <= maxBytes) { continue }
            total -= size
            do { try fm.removeItem(at: url); removed += 1 } catch { failed += 1 }
        }
        return (removed, failed)
    }

    private static func stampFormatter() -> DateFormatter {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = TimeZone(identifier: "UTC")
        f.dateFormat = "yyyyMMdd'T'HHmmss'Z'"
        return f
    }
}
