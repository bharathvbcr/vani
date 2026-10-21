import Foundation
import XCTest
@testable import VaniApplyKit

/// Integers parsed from an untrusted message are accepted only when integral, finite, in Int range and
/// not a Bool. A fraction is refused, never truncated (baseRev 1.9 must not become 1), and a dangling
/// symlink is present-but-unreadable, never "missing".
final class StrictIntegerTests: XCTestCase {
    func roundTrip(_ req: Any, _ store: BankStore) throws -> [String: Any] {
        let data = try JSONSerialization.data(withJSONObject: req)
        let reply = NativeProtocol.handle(try JSONSerialization.jsonObject(with: data), store: store, source: .chrome)
        return try JSONSerialization.jsonObject(with: JSONSerialization.data(withJSONObject: reply)) as! [String: Any]
    }

    func seeded() throws -> BankStore {
        let store = BankStore(directory: try tempDir(self))
        let row: [String: Any] = ["key": "good", "kind": "text", "value": ["x"], "labels": ["good"]]
        _ = try roundTrip(["op": "patch", "upsert": [row]], store)
        XCTAssertEqual(try store.read().rev, 1)
        return store
    }

    let badNumbers: [(String, Any)] = [
        ("1.9", 1.9), ("1.5", 1.5), ("0.5", 0.5), ("-0.5", -0.5), ("true", true), ("false", false),
        ("1e300", 1e300), ("-1e300", -1e300), ("9.3e18", 9.3e18), ("string", "1"), ("null", NSNull()),
    ]

    func testPatchBaseRevFractionBoolOrHugeIsRefusedNotTruncated() throws {
        let store = try seeded()
        let fresh: [String: Any] = ["key": "fresh", "kind": "text", "value": ["y"]]
        for (name, bad) in badNumbers {
            let reply = try roundTrip(["op": "patch", "upsert": [fresh], "delete": ["good"], "baseRev": bad], store)
            XCTAssertEqual(reply["code"] as? String, "bad_request", "baseRev \(name): \(reply)")
            XCTAssertEqual(try store.read().rev, 1, "baseRev \(name) changed the revision")
            XCTAssertNotNil(try store.read().bank.answers["good"], "baseRev \(name) applied the delete")
            XCTAssertNil(try store.read().bank.answers["fresh"], "baseRev \(name) applied the upsert")
        }
    }

    func testPatchRowUsesAndFillsMustBeIntegers() throws {
        let store = try seeded()
        for field in ["uses", "fills"] {
            for (name, bad) in badNumbers + [("2.7", 2.7)] {
                var row: [String: Any] = ["key": "fresh", "kind": "text", "value": ["y"]]
                row[field] = bad
                let reply = try roundTrip(["op": "patch", "upsert": [row]], store)
                XCTAssertEqual(reply["code"] as? String, "bad_request", "\(field) \(name): \(reply)")
                XCTAssertEqual(try store.read().rev, 1, "\(field) \(name) changed the revision")
            }
        }
    }

    func testIntegralValuesStillWork() throws {
        let store = try seeded()
        // 2.0 is the integer 2 on the wire; a real count and a real baseRev are accepted.
        let row: [String: Any] = ["key": "fresh", "kind": "text", "value": ["y"], "uses": 2.0, "fills": 3]
        let reply = try roundTrip(["op": "patch", "upsert": [row], "baseRev": 1.0], store)
        XCTAssertEqual(reply["rev"] as? Int, 2, "\(reply)")
    }

    func testPutAndGetChunkRefuseFractions() throws {
        let store = try seeded()
        let bank = try store.read().bank.jsonObject()
        for (name, bad) in badNumbers {
            let put = try roundTrip(["op": "put", "baseRev": bad, "bank": bank], store)
            XCTAssertNotNil(put["error"], "put baseRev \(name): \(put)")
            XCTAssertEqual(try store.read().rev, 1)
        }
        for (name, bad) in [("1.9", 1.9), ("true", true), ("1e300", 1e300)] as [(String, Any)] {
            XCTAssertNotNil(try roundTrip(["op": "getChunk", "rev": bad, "chunkIndex": 0], store)["error"], "getChunk rev \(name)")
            XCTAssertNotNil(try roundTrip(["op": "getChunk", "rev": 1, "chunkIndex": bad], store)["error"], "getChunk chunkIndex \(name)")
        }
    }

    func testStrictIntParserDirectly() {
        XCTAssertEqual(BankValidator.strictInt(NSNumber(value: 3)), 3)
        XCTAssertEqual(BankValidator.strictInt(NSNumber(value: 3.0)), 3)
        XCTAssertEqual(BankValidator.strictInt(NSNumber(value: -4)), -4)
        XCTAssertNil(BankValidator.strictInt(NSNumber(value: 1.9)))
        XCTAssertNil(BankValidator.strictInt(NSNumber(value: Double.nan)))
        XCTAssertNil(BankValidator.strictInt(NSNumber(value: Double.infinity)))
        XCTAssertNil(BankValidator.strictInt(NSNumber(value: UInt64.max)))
        XCTAssertNil(BankValidator.strictInt(NSNumber(value: true)))
        XCTAssertNil(BankValidator.strictInt("3"))
        XCTAssertNil(BankValidator.strictInt(nil))
    }

    // MARK: dangling symlink

    func testDraftSettingsDanglingSymlinkIsUnreadableNotMissing() throws {
        let dir = try tempDir(self)
        let link = dir.appendingPathComponent(DraftSettings.fileName)
        try FileManager.default.createSymbolicLink(atPath: link.path, withDestinationPath: dir.appendingPathComponent("nowhere.json").path)
        XCTAssertThrowsError(try DraftSettings.load(from: dir)) { error in
            XCTAssertTrue(error is DraftSettings.LoadError, "\(error)")
        }
    }

    func testDraftSettingsTrulyAbsentIsDefaults() throws {
        let dir = try tempDir(self)
        XCTAssertEqual(try DraftSettings.load(from: dir), DraftSettings())
    }

    func testBankDanglingSymlinkIsUnreadableNotAnEmptyBank() throws {
        let dir = try tempDir(self)
        let store = BankStore(directory: dir)
        try FileManager.default.createSymbolicLink(atPath: store.bankURL.path, withDestinationPath: dir.appendingPathComponent("nowhere.json").path)
        XCTAssertThrowsError(try store.read())
    }
}
