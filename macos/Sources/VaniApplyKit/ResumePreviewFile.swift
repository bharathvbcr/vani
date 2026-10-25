import Foundation

/// The page an unfitted `jobs resume render` leaves for the Studio to look at: rkb's screen HTML, copied by
/// vani to `<state>/resumes/.tmp/preview-N.html` (0600). It holds the header (the owner's name and contact
/// lines), so it must not outlive the look: the app reads it once and deletes it in the same step, before
/// any view is asked to draw it, and a path that is not exactly that shape is neither read nor removed.
public enum ResumePreviewFile {
    /// vani's own bound on the page (maxPreviewHTML, 4 MiB).
    public static let maxBytes = 4 << 20

    public struct Failure: Error, Equatable, Sendable {
        public var message: String
    }

    /// Whether `path` is a preview vani writes: absolute, `.../resumes/.tmp/preview-<digits>.html`.
    public static func isPreviewPath(_ path: String) -> Bool {
        guard path.hasPrefix("/"), !path.contains("\0") else { return false }
        let url = URL(fileURLWithPath: path)
        let name = url.lastPathComponent
        guard name.hasPrefix("preview-"), name.hasSuffix(".html") else { return false }
        let digits = name.dropFirst("preview-".count).dropLast(".html".count)
        guard !digits.isEmpty, digits.utf8.allSatisfy({ $0 >= 0x30 && $0 <= 0x39 }) else { return false }
        let dir = url.deletingLastPathComponent()
        return dir.lastPathComponent == ".tmp" && dir.deletingLastPathComponent().lastPathComponent == "resumes"
    }

    /// Read the page and remove the file. The file is removed whether or not it could be read, as long
    /// as it is a plain file of the right name; a link, a folder or a path of another shape is left alone
    /// and refused. Blocks briefly (one small file): call it off the main thread.
    public static func take(path: String) throws -> String {
        guard isPreviewPath(path) else { throw Failure(message: "vani named a preview page this app will not read") }
        var st = stat()
        guard lstat(path, &st) == 0 else { throw Failure(message: "the preview page is not where vani said it is") }
        guard (st.st_mode & S_IFMT) == S_IFREG else { throw Failure(message: "vani named a preview page that is not a plain file") }
        defer { unlink(path) }
        guard Int(st.st_size) <= maxBytes else {
            throw Failure(message: "the preview page is over \(maxBytes >> 20) MiB, so it is not shown")
        }
        guard let h = FileHandle(forReadingAtPath: path) else { throw Failure(message: "the preview page could not be opened") }
        defer { try? h.close() }
        let data = (try? h.read(upToCount: maxBytes + 1)) ?? Data()
        guard !data.isEmpty, data.count <= maxBytes else { throw Failure(message: "the preview page is empty or too large") }
        guard let html = String(data: data, encoding: .utf8) else { throw Failure(message: "the preview page is not text this app can show") }
        return html
    }
}
