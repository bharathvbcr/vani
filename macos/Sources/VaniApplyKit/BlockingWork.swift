import Foundation

/// Runs blocking work (a `vani` child and its pipes, an XPC round trip) off the main actor
/// without parking a thread of Swift's cooperative pool.
///
/// A `Task.detached` that calls `VaniProcess` blocks its pool thread for as long as the child
/// runs (up to 150 s). The pool is about one thread per core, so a few slow calls at once starve
/// every other `Task`, including the ones that draw the UI. Here the work runs on its own
/// concurrent queue and the caller suspends on a continuation, which holds no thread.
///
/// The work is not interruptible: cancelling the awaiting task does not stop it. Every caller is
/// bounded by a `VaniProcess` timeout, so the await ends when the call does.
public enum BlockingWork {
    private static let queue = DispatchQueue(label: "dev.vbcr.vani.blocking-work", qos: .userInitiated, attributes: .concurrent)

    /// `work`'s value, or the error it threw. The continuation is resumed exactly once, by the
    /// block that ran the work, on every path.
    public static func run<T: Sendable>(_ work: @escaping @Sendable () throws -> T) async -> Result<T, Error> {
        await withCheckedContinuation { continuation in
            queue.async { continuation.resume(returning: Result { try work() }) }
        }
    }

    /// The same for work that cannot throw.
    public static func value<T: Sendable>(_ work: @escaping @Sendable () -> T) async -> T {
        await withCheckedContinuation { continuation in
            queue.async { continuation.resume(returning: work()) }
        }
    }
}
