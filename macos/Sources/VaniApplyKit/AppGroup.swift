import Foundation

public enum AppGroup {
    /// Team-ID-prefixed, so macOS 15+ grants the container without a provisioning profile.
    public static let identifier = "HAUHVD8M37.dev.vbcr.vani"

    /// `<group container>/apply`, where the bank and the activity log live.
    /// Falls back to ~/Library/Group Containers/<identifier>/apply or Application Support
    /// when the binary is not signed with the App Group entitlement (e.g. dev/test builds).
    /// Can be overridden via the VANI_APPLY_STORE_DIR environment variable.
    public static func storeDirectory() throws -> URL {
        if let env = ProcessInfo.processInfo.environment["VANI_APPLY_STORE_DIR"], !env.isEmpty {
            return URL(fileURLWithPath: env, isDirectory: true)
        }
        if let base = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: identifier) {
            let apply = base.appendingPathComponent("apply", isDirectory: true)
            if isUsableDirectory(apply) || (try? FileManager.default.createDirectory(at: apply, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])) != nil {
                return apply
            }
        }
        let home = FileManager.default.homeDirectoryForCurrentUser
        let groupApply = home.appendingPathComponent("Library/Group Containers/\(identifier)/apply", isDirectory: true)
        if isUsableDirectory(groupApply) || (try? FileManager.default.createDirectory(at: groupApply, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])) != nil {
            return groupApply
        }
        if let appSupport = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first {
            let appSupportApply = appSupport.appendingPathComponent("Vani/apply", isDirectory: true)
            if isUsableDirectory(appSupportApply) || (try? FileManager.default.createDirectory(at: appSupportApply, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])) != nil {
                return appSupportApply
            }
        }
        throw BankError("App Group container \(identifier) is unavailable and no writable store directory could be resolved")
    }

    private static func isUsableDirectory(_ url: URL) -> Bool {
        let fm = FileManager.default
        var isDir: ObjCBool = false
        if fm.fileExists(atPath: url.path, isDirectory: &isDir) {
            return isDir.boolValue && fm.isWritableFile(atPath: url.path)
        }
        return false
    }
}
