import Foundation
import XCTest
@testable import VaniApplyKit

/// The unfitted page holds the header, so it is read once and removed in the same step (gap 30). The page
/// here is made up.
final class ResumePreviewFileTests: XCTestCase {
    private func folder() throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("vani-preview-\(UUID().uuidString)/resumes/.tmp", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let root = dir.deletingLastPathComponent().deletingLastPathComponent()
        addTeardownBlock { try? FileManager.default.removeItem(at: root) }
        return dir
    }

    private func exists(_ url: URL) -> Bool { FileManager.default.fileExists(atPath: url.path) }

    func testOnlyAPageOfVanisOwnNameAndFolderIsAPreviewPath() {
        for good in ["/s/resumes/.tmp/preview-1.html", "/a b/resumes/.tmp/preview-424242.html"] {
            XCTAssertTrue(ResumePreviewFile.isPreviewPath(good), good)
        }
        for bad in ["", "preview-1.html", "resumes/.tmp/preview-1.html", "/s/resumes/.tmp/preview-.html", "/s/resumes/.tmp/preview-1a.html",
                    "/s/resumes/.tmp/preview-1.htm", "/s/resumes/.tmp/Preview-1.html", "/s/resumes/tmp/preview-1.html",
                    "/s/other/.tmp/preview-1.html", "/s/.tmp/preview-1.html", "/s/resumes/.tmp/x/preview-1.html", "/s/resumes/.tmp/preview-1.html\0",
                    "/s/resumes/.tmp/../preview-1.html", "/s/resumes/.tmp/preview-١.html"] {
            XCTAssertFalse(ResumePreviewFile.isPreviewPath(bad), bad.debugDescription)
        }
    }

    func testTheTextIsReadAndTheFileIsGone() throws {
        let file = try folder().appendingPathComponent("preview-9.html")
        try Data("<html>made-up header and bullets</html>".utf8).write(to: file)
        XCTAssertEqual(try ResumePreviewFile.take(path: file.path), "<html>made-up header and bullets</html>")
        XCTAssertFalse(exists(file))
        XCTAssertThrowsError(try ResumePreviewFile.take(path: file.path), "gone is gone")
    }

    func testAnOversizedEmptyOrNonTextPageIsRefusedAndStillRemoved() throws {
        let dir = try folder()
        for (name, data) in [("preview-1.html", Data(repeating: 0x61, count: ResumePreviewFile.maxBytes + 1)),
                             ("preview-2.html", Data()),
                             ("preview-3.html", Data([0xFF, 0xFE, 0x00, 0xC3, 0x28]))] {
            let file = dir.appendingPathComponent(name)
            try data.write(to: file)
            XCTAssertThrowsError(try ResumePreviewFile.take(path: file.path), name)
            XCTAssertFalse(exists(file), "\(name) held the header: removed even though it was refused")
        }
        let exact = dir.appendingPathComponent("preview-4.html")
        try Data(repeating: 0x61, count: ResumePreviewFile.maxBytes).write(to: exact)
        XCTAssertEqual(try ResumePreviewFile.take(path: exact.path).utf8.count, ResumePreviewFile.maxBytes, "the bound itself is allowed")
    }

    func testALinkOrAFolderOrAMissingFileIsRefusedAndLeftAlone() throws {
        let dir = try folder()
        let target = dir.deletingLastPathComponent().appendingPathComponent("elsewhere.html")
        try Data("<html>not a preview</html>".utf8).write(to: target)
        let link = dir.appendingPathComponent("preview-5.html")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: target)
        XCTAssertThrowsError(try ResumePreviewFile.take(path: link.path))
        XCTAssertTrue(exists(target), "the link's target is never read or removed")
        XCTAssertTrue((try? FileManager.default.destinationOfSymbolicLink(atPath: link.path)) != nil, "the link itself is left too")

        let folder = dir.appendingPathComponent("preview-6.html", isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        XCTAssertThrowsError(try ResumePreviewFile.take(path: folder.path))
        XCTAssertTrue(exists(folder))

        XCTAssertThrowsError(try ResumePreviewFile.take(path: dir.appendingPathComponent("preview-7.html").path))

        let elsewhere = dir.deletingLastPathComponent().appendingPathComponent("preview-8.html")
        try Data("<html>x</html>".utf8).write(to: elsewhere)
        XCTAssertThrowsError(try ResumePreviewFile.take(path: elsewhere.path), "a path of another shape is not touched")
        XCTAssertTrue(exists(elsewhere))
    }
}
