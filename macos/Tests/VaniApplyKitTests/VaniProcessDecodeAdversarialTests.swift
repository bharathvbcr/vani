import Foundation
import XCTest
@testable import VaniApplyKit

/// Version-first decoding and the value-free decode errors, against hostile replies.
final class VaniProcessDecodeAdversarialTests: XCTestCase {
    private struct Item: Decodable { var n: Int }
    private struct Reply: Decodable, VersionedReply {
        static var supportedVersion: Int { 1 }
        var version: Int
        var items: [Item]
    }
    private struct Nested: Decodable { var items: [[Item]] }
    private struct Keyed: Decodable { var counts: [String: Item] }

    /// A fake vani that cats a file, so the reply can be any size.
    private func fakeVani(replyBytes: Data) throws -> URL {
        let dir = try tempDir(self)
        let reply = dir.appendingPathComponent("reply.json")
        try replyBytes.write(to: reply)
        let url = dir.appendingPathComponent("vani")
        try "#!/bin/sh\ncat '\(reply.path)'\n".write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    private func call<T: Decodable>(_ type: T.Type, _ reply: String, max: Int = 1 << 20) throws -> T {
        try VaniProcess.call(try fakeVani(replyBytes: Data(reply.utf8)), ["x"], configPath: nil, stateDir: nil,
                             timeout: 20, maxOutputBytes: max, tooOld: "too old")
    }

    func testVersionAsStringIsAShapeErrorOnVersionNotAnUnreadableReply() throws {
        XCTAssertThrowsError(try call(Reply.self, #"{"version":"2","items":[]}"#)) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("version"), text)
            XCTAssertFalse(text.contains("without a reply"), text)
        }
    }

    func testVersionTwoPointZeroIsEitherReadAsTwoOrRefused() throws {
        XCTAssertThrowsError(try call(Reply.self, #"{"version":2.0,"items":[]}"#)) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("version"), text)
            XCTAssertFalse(text.contains("without a reply"), text)
        }
    }

    func testFractionalVersionIsNamedAsAVersionProblem() throws {
        XCTAssertThrowsError(try call(Reply.self, #"{"version":1.5,"items":[]}"#)) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("version"), text)
            XCTAssertFalse(text.contains("without a reply"), text)
            XCTAssertFalse(text.contains("1.5"), text)
        }
    }

    func testHugeVersionIsNamedAsAVersionProblem() throws {
        XCTAssertThrowsError(try call(Reply.self, #"{"version":99999999999999999999999,"items":[]}"#)) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("version"), text)
            XCTAssertFalse(text.contains("without a reply"), text)
            XCTAssertFalse(text.contains("99999999999999999999999"), text)
        }
    }

    func testNonJSONStillSaysNoReply() throws {
        XCTAssertThrowsError(try call(Reply.self, "not json")) {
            XCTAssertTrue(String(describing: $0).contains("without a reply"), "\($0)")
        }
    }

    func testMissingVersionNamesTheKey() throws {
        XCTAssertThrowsError(try call(Reply.self, #"{"items":[]}"#)) {
            XCTAssertTrue(String(describing: $0).contains("version is missing"), "\($0)")
        }
    }

    /// 10 MB of a reply under the right version decodes, and under another version is named as
    /// that: both probe and decode run over the whole body without trouble.
    func testTenMegabytePayloads() throws {
        let item = #"{"n":1},"#
        let body = String(repeating: item, count: 10_000_000 / item.count) + #"{"n":1}"#
        let ok = try call(Reply.self, #"{"version":1,"items":["# + body + "]}", max: 32 << 20)
        XCTAssertGreaterThan(ok.items.count, 1_000_000)
        XCTAssertThrowsError(try call(Reply.self, #"{"version":7,"items":["# + body + "]}", max: 32 << 20)) {
            XCTAssertTrue(String(describing: $0).contains("version 7"), "\($0)")
        }
    }

    func testNestedArrayPathHasIndexesButNoValue() throws {
        XCTAssertThrowsError(try call(Nested.self, #"{"items":[[{"n":1}],[{"n":2},{"n":"TOPSECRET"}]]}"#)) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("items[1][1].n"), text)
            XCTAssertFalse(text.contains("TOPSECRET"), text)
        }
    }

    /// The promise is a path of schema keys. A reply typed as a dictionary puts its data keys in
    /// that path, so a key that is personal data would be shown. Characterized with a synthetic
    /// type: no reply type of the app decodes a dictionary keyed by personal data.
    func testDictionaryKeysAppearInThePath() throws {
        XCTAssertThrowsError(try call(Keyed.self, #"{"counts":{"alice@example.com":{"n":"x"}}}"#)) {
            let text = String(describing: $0)
            XCTAssertTrue(text.contains("alice@example.com"), "expected the key in the path: \(text)")
        }
    }
}
