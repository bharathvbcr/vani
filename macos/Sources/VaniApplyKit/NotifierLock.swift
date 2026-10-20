import Darwin
import Foundation

/// The app's claim on run notices. While the app holds an exclusive flock on
/// `<state dir>/app/notifier.lock`, the daemon posts none of its own
/// (internal/notify UnlessHeld, AppLockFile): the daemon is the fallback for
/// when the app is not running. A flock belongs to the open file, so it goes
/// when the app quits or crashes and can never silence the daemon for good.
public final class NotifierLock {
    /// AppLockFile in internal/notify/held.go.
    public static let relativePath = "app/notifier.lock"

    /// The lock file held, or nil.
    public private(set) var path: String?
    /// When the lock was taken: runs that finished before it were the daemon's to announce.
    public private(set) var heldSince: Date?
    private var fd: Int32 = -1

    public init() {}

    deinit { release() }

    /// Holds the lock for stateDir (the state_dir `vani status` reports),
    /// moving it if the state dir changed. Nil when held; otherwise why not.
    @discardableResult
    public func hold(stateDir: String, at now: Date = Date()) -> String? {
        guard !stateDir.isEmpty else { return "vani reported no state folder" }
        let target = URL(fileURLWithPath: stateDir, isDirectory: true).appendingPathComponent(Self.relativePath).path
        if fd >= 0, path == target { return nil }
        release()
        let dir = (target as NSString).deletingLastPathComponent
        do {
            try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true,
                                                    attributes: [.posixPermissions: 0o700])
        } catch {
            return "could not create \(dir): \(error.localizedDescription)"
        }
        let f = open(target, O_RDWR | O_CREAT | O_CLOEXEC | O_NOFOLLOW, 0o600)
        guard f >= 0 else { return "could not open \(target): \(String(cString: strerror(errno)))" }
        guard flock(f, LOCK_EX | LOCK_NB) == 0 else {
            let why = errno == EWOULDBLOCK ? "another copy of Vani Apply announces runs" : String(cString: strerror(errno))
            close(f)
            return why
        }
        fd = f
        path = target
        heldSince = now
        return nil
    }

    /// Lets go: from now on the daemon announces runs.
    public func release() {
        if fd >= 0 {
            flock(fd, LOCK_UN)
            close(fd)
        }
        fd = -1
        path = nil
        heldSince = nil
    }
}
