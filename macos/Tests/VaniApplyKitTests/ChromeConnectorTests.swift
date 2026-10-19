import Foundation
import XCTest
@testable import VaniApplyKit

final class ChromeConnectorTests: XCTestCase {
    let id = ChromeConnector.defaultExtensionID

    func testStatusLifecycle() throws {
        let support = try tempDir(self)
        let c = ChromeConnector(applicationSupport: support, hostBinary: URL(fileURLWithPath: "/bin/sh"))
        XCTAssertEqual(c.status(.chrome, extensionID: id), .browserNotFound)

        try FileManager.default.createDirectory(at: support.appendingPathComponent("Google/Chrome"), withIntermediateDirectories: true)
        XCTAssertEqual(c.status(.chrome, extensionID: id), .notConnected)

        try c.connect(.chrome, extensionID: id)
        XCTAssertEqual(c.status(.chrome, extensionID: id), .connected)
        let m = try JSONSerialization.jsonObject(with: Data(contentsOf: c.manifestURL(.chrome))) as? [String: Any]
        XCTAssertEqual(m?["name"] as? String, "dev.vbcr.vani_apply")
        XCTAssertEqual(m?["type"] as? String, "stdio")
        XCTAssertEqual(m?["path"] as? String, "/bin/sh")
        XCTAssertEqual(m?["allowed_origins"] as? [String], ["chrome-extension://\(id)/"])
        XCTAssertEqual(c.manifestURL(.chrome).path, support.appendingPathComponent("Google/Chrome/NativeMessagingHosts/dev.vbcr.vani_apply.json").path)

        // The app moved: the manifest names another binary.
        let moved = ChromeConnector(applicationSupport: support, hostBinary: URL(fileURLWithPath: "/bin/zsh"))
        guard case .stale = moved.status(.chrome, extensionID: id) else { return XCTFail("moved app must read stale") }
        // A different extension id.
        guard case .stale = c.status(.chrome, extensionID: String(repeating: "a", count: 32)) else { return XCTFail("other id must read stale") }

        try c.disconnect(.chrome)
        XCTAssertEqual(c.status(.chrome, extensionID: id), .notConnected)
    }

    func testDisconnectRemovesOnlyItsOwnManifest() throws {
        let support = try tempDir(self)
        let c = ChromeConnector(applicationSupport: support, hostBinary: URL(fileURLWithPath: "/bin/sh"))
        try c.connect(.chromium, extensionID: id)
        let neighbour = c.manifestURL(.chromium).deletingLastPathComponent().appendingPathComponent("com.other.host.json")
        try Data("{}".utf8).write(to: neighbour)
        try c.disconnect(.chromium)
        XCTAssertFalse(FileManager.default.fileExists(atPath: c.manifestURL(.chromium).path))
        XCTAssertTrue(FileManager.default.fileExists(atPath: neighbour.path))
        XCTAssertNoThrow(try c.disconnect(.chromium), "disconnecting twice is fine")
    }

    func testRefusesABadExtensionID() {
        let c = ChromeConnector(applicationSupport: URL(fileURLWithPath: "/nonexistent"), hostBinary: URL(fileURLWithPath: "/bin/sh"))
        for bad in ["", "short", String(repeating: "z", count: 32), String(repeating: "A", count: 32), id + "a"] {
            XCTAssertThrowsError(try c.manifest(extensionID: bad), bad)
        }
    }
}
