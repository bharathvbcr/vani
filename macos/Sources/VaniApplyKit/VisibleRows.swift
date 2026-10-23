/// The rows of a lazily loaded list that are on screen, kept from the rows'
/// appear and disappear callbacks, so a view can tell whether the list is
/// scrolled past its first row (macOS 14 has no scroll-offset API for a List).
///
/// It keeps the set of rows on screen rather than one "first row visible"
/// flag: when a tab or filter swaps the rows, SwiftUI may report the new
/// first row appearing before the old one disappears, and a flag left by the
/// last callback would then be wrong. The first row is named when asked, so
/// the answer follows the list as it is now.
public struct VisibleRows: Equatable, Sendable {
    /// Rows report only while on screen, so this is never reached in use; it
    /// bounds the set if a list drops its disappear callbacks.
    public static let maxTracked = 512

    /// On-screen keys, oldest report first.
    private var keys: [String] = []

    public init() {}

    public var count: Int { keys.count }

    public mutating func appeared(_ key: String) {
        keys.removeAll { $0 == key }
        keys.append(key)
        if keys.count > Self.maxTracked { keys.removeFirst(keys.count - Self.maxTracked) }
    }

    public mutating func disappeared(_ key: String) {
        keys.removeAll { $0 == key }
    }

    /// True when rows are on screen and `first` is not one of them. Before any
    /// row reports, or once the list is gone, it is false: nothing to scroll.
    public func scrolledPast(first: String?) -> Bool {
        guard let first, !keys.isEmpty else { return false }
        return !keys.contains(first)
    }
}
