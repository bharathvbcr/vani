import Foundation

public enum AppGroup {
    /// Team-ID-prefixed, so macOS 15+ grants the container without a provisioning profile.
    public static let identifier = "HAUHVD8M37.dev.vbcr.vani"

    /// `<group container>/apply`, where the bank and the activity log live.
    public static func storeDirectory() throws -> URL {
        guard let base = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: identifier) else {
            throw BankError("App Group container \(identifier) is unavailable (is this binary signed with the group entitlement?)")
        }
        return base.appendingPathComponent("apply", isDirectory: true)
    }
}
