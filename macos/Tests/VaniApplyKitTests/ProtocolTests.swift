import Foundation
import XCTest
@testable import VaniApplyKit

final class ProtocolTests: XCTestCase {
    func roundTrip(_ req: Any, _ store: BankStore) throws -> [String: Any] {
        // Through JSON both ways, as the Chrome host sees it.
        let data = try JSONSerialization.data(withJSONObject: req)
        let reply = NativeProtocol.handle(try JSONSerialization.jsonObject(with: data), store: store, source: .chrome)
        return try JSONSerialization.jsonObject(with: JSONSerialization.data(withJSONObject: reply)) as! [String: Any]
    }

    func testPingGetPutConflict() throws {
        let store = BankStore(directory: try tempDir(self))
        let ping = try roundTrip(["op": "ping"], store)
        XCTAssertEqual(ping["ok"] as? Bool, true)
        XCTAssertEqual(ping["protocol"] as? Int, 1)
        XCTAssertEqual(ping["rev"] as? Int, 0)

        let get = try roundTrip(["op": "get"], store)
        XCTAssertEqual(get["rev"] as? Int, 0)
        XCTAssertEqual((get["bank"] as? [String: Any])?["version"] as? Int, 1)

        let put = try roundTrip(["op": "put", "baseRev": 0, "bank": bankJSON([answerJSON("aa"), answerJSON("ssn")])], store)
        XCTAssertEqual(put["rev"] as? Int, 1)
        XCTAssertEqual(put["droppedSecret"] as? Int, 1)

        let stale = try roundTrip(["op": "put", "baseRev": 0, "bank": bankJSON([])], store)
        XCTAssertEqual(stale["conflict"] as? Bool, true)
        XCTAssertEqual(stale["rev"] as? Int, 1)
        XCTAssertNotNil(((stale["bank"] as? [String: Any])?["answers"] as? [String: Any])?["aa"])
    }

    func testRefusals() throws {
        let store = BankStore(directory: try tempDir(self))
        for req: Any in [
            [1], ["op": 3], ["op": "drop"], ["op": "put", "bank": bankJSON([])],
            ["op": "put", "baseRev": -1, "bank": bankJSON([])], ["op": "put", "baseRev": true, "bank": bankJSON([])],
            ["op": "put", "baseRev": 0], ["op": "log"], ["op": "log", "entry": ["event": "steal"]],
        ] {
            let reply = try roundTrip(req, store)
            XCTAssertNotNil(reply["error"] as? String, "\(req) should be refused, got \(reply)")
        }
        XCTAssertEqual(try store.read().rev, 0)
    }

    func testLogAcceptsOnlyNamesHostsAndCounts() throws {
        let store = BankStore(directory: try tempDir(self))
        XCTAssertEqual(try roundTrip(["op": "log", "entry": ["event": "capture", "host": "Boards.Greenhouse.io", "counts": ["added": 3]]], store)["ok"] as? Bool, true)
        for entry: [String: Any] in [
            ["event": "capture", "host": "not a host"],
            ["event": "capture", "host": "evil.example/path?q=Ada"],
            ["event": "capture", "counts": ["first name: Ada": 1]],
            ["event": "capture", "counts": ["added": "Ada"]],
            ["event": "capture", "counts": ["added": -1]],
            ["event": "capture", "counts": ["added": 1.5]],
        ] {
            XCTAssertNotNil(try roundTrip(["op": "log", "entry": entry], store)["error"], "\(entry) should be refused")
        }
        let logged = try store.recentActivity()
        XCTAssertEqual(logged.count, 1)
        XCTAssertEqual(logged.first?.host, "boards.greenhouse.io")
        XCTAssertEqual(logged.first?.source, .chrome)
    }
}
