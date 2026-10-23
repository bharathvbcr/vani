import Foundation
import XCTest
@testable import VaniApplyKit

/// What `VaniProcess.call` says when exit 0 comes with a reply it cannot decode: a contract
/// version drift is named as one, and a wrong shape names where, never what.
final class VaniProcessDecodeTests: XCTestCase {
    private func fakeVani(_ reply: String) throws -> URL {
        let dir = try tempDir(self)
        let url = dir.appendingPathComponent("vani")
        try "#!/bin/sh\nprintf '%s' '\(reply)'\n".write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    private struct Item: Decodable { var n: Int }
    private struct Reply: Decodable { var items: [Item] }

    private func call(_ reply: String) throws -> Reply {
        try VaniProcess.call(try fakeVani(reply), ["x"], configPath: nil, stateDir: nil, timeout: 10, maxOutputBytes: 1 << 20,
                             tooOld: "too old")
    }

    func testAnotherContractVersionWinsOverTheShapeError() throws {
        let vani = try fakeVani(#"{"version":2,"surprise":[1,{"a":null}]}"#)
        XCTAssertThrowsError(try PipelineClient(vaniBinary: vani).status()) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("version 2"), text)
            XCTAssertFalse(text.contains("without a reply"), text)
        }
    }

    func testAMistypedFieldNamesItsPathAndNotItsValue() throws {
        XCTAssertThrowsError(try call(#"{"items":[{"n":1},{"n":"SECRET-VALUE-42"}]}"#)) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("items[1].n"), text)
            XCTAssertFalse(text.contains("SECRET-VALUE-42"), text)
        }
    }

    func testAMissingKeyNamesItsPath() throws {
        XCTAssertThrowsError(try call(#"{"items":[{}]}"#)) {
            XCTAssertTrue(String(describing: $0).contains("items[0].n"), "\($0)")
        }
    }

    func testAMatchingReplyStillDecodes() throws {
        XCTAssertEqual(try call(#"{"items":[{"n":3}]}"#).items.first?.n, 3)
    }

    func testNonJSONStaysWithoutAReply() throws {
        XCTAssertThrowsError(try call("not json")) {
            XCTAssertTrue(String(describing: $0).contains("without a reply"), "\($0)")
        }
    }
}
