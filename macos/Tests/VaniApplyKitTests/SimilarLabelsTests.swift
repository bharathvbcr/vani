import Foundation
import XCTest
@testable import VaniApplyKit

final class SimilarLabelsTests: XCTestCase {
    private var bank: Bank {
        var b = Bank()
        b.answers["alpha"] = Answer(key: "alpha", labels: ["alpha", "alpha one"], kind: .text, value: ["TOP-SECRET-VALUE-111"],
                                    sensitivity: .standard, signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        b.answers["beta"] = Answer(key: "beta", labels: ["beta"], kind: .text, value: ["ANOTHER-VALUE-222"],
                                   sensitivity: .standard, signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-18T00:00:00.000Z", review: false)
        return b
    }

    /// A temporary executable standing in for `vani`. It saves its stdin to `capture`, then runs `body`.
    private func fakeVani(_ body: String) throws -> (binary: URL, capture: URL) {
        let dir = try tempDir(self)
        let capture = dir.appendingPathComponent("stdin.json")
        let bin = dir.appendingPathComponent("vani")
        let script = "#!/bin/sh\n[ \"$1 $2\" = \"answers similar\" ] || { echo \"unknown command $1\" >&2; exit 2; }\n"
            + "/bin/cat > '\(capture.path)'\n\(body)\n"
        try script.write(to: bin, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: bin.path)
        return (bin, capture)
    }

    private func client(_ bin: URL, timeout: TimeInterval = 10) -> SimilarLabels {
        SimilarLabels(vaniBinary: bin, timeout: timeout)
    }

    private func assertUnavailable(_ r: SimilarResult, containing needle: String = "", file: StaticString = #filePath, line: UInt = #line) {
        guard case let .unavailable(reason) = r else { return XCTFail("expected unavailable, got \(r)", file: file, line: line) }
        if !needle.isEmpty { XCTAssertTrue(reason.contains(needle), reason, file: file, line: line) }
        XCTAssertFalse(reason.isEmpty, file: file, line: line)
    }

    func testValidReplyBecomesPairsByKey() throws {
        // The texts sent are: 0 alpha, 1 alpha one, 2 beta.
        let (bin2, _) = try fakeVani(#"echo '{"available":true,"model":"tessl-embed","pairs":[{"a":0,"b":2,"cos":0.93,"tier":"high"},{"a":1,"b":2,"cos":0.9},{"a":2,"b":0,"cos":0.93},{"a":0,"b":1,"cos":0.99}]}'"#)
        let r = client(bin2).find(in: bank)
        XCTAssertEqual(r, .available([SimilarPair(a: "alpha", b: "beta", cos: 0.93, tier: "high")], model: "tessl-embed"),
                       "pairs name rows, repeats and a row's own wordings collapse")
    }

    func testOnlyKeysAndWordingsAreSentNeverValues() throws {
        let (bin, capture) = try fakeVani(#"echo '{"available":true,"pairs":[]}'"#)
        _ = client(bin).find(in: bank)
        let sent = try Data(contentsOf: capture)
        let text = String(decoding: sent, as: UTF8.self)
        XCTAssertFalse(text.contains("TOP-SECRET-VALUE-111") || text.contains("ANOTHER-VALUE-222"), text)
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: sent) as? [String: Any])
        XCTAssertEqual(Array(obj.keys), ["texts"])
        XCTAssertEqual(obj["texts"] as? [String], ["alpha", "alpha one", "beta"])
    }

    func testSecretAndHiddenSelfIDRowsAreNotSent() throws {
        var b = bank
        b.answers["passport"] = Answer(key: "passport", labels: ["passport"], kind: .text, value: ["P1"], sensitivity: .secret,
                                       signatures: [], uses: 1, fills: 0, updatedAt: "x", review: false)
        b.answers["gender"] = Answer(key: "gender", labels: ["gender"], kind: .select, value: ["G"], sensitivity: .selfID,
                                     signatures: [], uses: 1, fills: 0, updatedAt: "x", review: false)
        XCTAssertEqual(SimilarLabels.request(for: b).texts, ["alpha", "alpha one", "beta"])
        b.settings.captureSelfId = true
        XCTAssertEqual(SimilarLabels.request(for: b).texts, ["alpha", "alpha one", "beta", "gender"])
    }

    func testUnknownSubcommandIsUnavailableNotEmpty() throws {
        let dir = try tempDir(self)
        let bin = dir.appendingPathComponent("vani")
        try "#!/bin/sh\necho 'unknown command \"answers\"' >&2\nexit 2\n".write(to: bin, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: bin.path)
        let r = client(bin).find(in: bank)
        assertUnavailable(r, containing: "exited 2")
        XCTAssertNotEqual(r, .available([], model: nil))
    }

    func testTimeoutIsUnavailable() throws {
        let (bin, _) = try fakeVani("exec /bin/sleep 30")
        let started = Date()
        let r = client(bin, timeout: 0.5).find(in: bank)
        assertUnavailable(r, containing: "did not finish")
        XCTAssertLessThan(Date().timeIntervalSince(started), 10)
    }

    func testGarbageIsUnavailable() throws {
        for body in ["echo 'not json'", "echo '{\"available\":true}'", "echo '[]'", "printf ''", "exit 3",
                     #"echo '{"available":true,"pairs":[{"a":"x","b":1,"cos":0.9}]}'"#] {
            let (bin, _) = try fakeVani(body)
            assertUnavailable(client(bin).find(in: bank))
        }
    }

    func testOutOfRangeSelfAndNonFiniteIndexesAreUnavailable() throws {
        for pair in [#"{"a":0,"b":3,"cos":0.9}"#, #"{"a":-1,"b":1,"cos":0.9}"#, #"{"a":1,"b":1,"cos":0.9}"#] {
            let (bin, _) = try fakeVani("echo '{\"available\":true,\"pairs\":[\(pair)]}'")
            assertUnavailable(client(bin).find(in: bank), containing: "not sent")
        }
    }

    func testReplyThatSaysUnavailableCarriesItsReason() throws {
        let (bin, _) = try fakeVani(#"echo '{"available":false,"reason":"no embedding model installed"}'"#)
        XCTAssertEqual(client(bin).find(in: bank), .unavailable("no embedding model installed"))
        let (bin2, _) = try fakeVani(#"echo '{"available":false}'"#)
        assertUnavailable(client(bin2).find(in: bank))
    }

    func testErrorReplyIsUnavailableWithItsMessage() throws {
        let (bin, _) = try fakeVani(#"echo '{"error":"model failed to load"}'; exit 1"#)
        XCTAssertEqual(client(bin).find(in: bank), .unavailable("model failed to load"))
    }

    func testMissingBinaryIsUnavailable() {
        assertUnavailable(client(URL(fileURLWithPath: "/nonexistent/vani")).find(in: bank), containing: "missing")
    }

    func testOversizedReplyIsUnavailable() throws {
        let (bin, _) = try fakeVani("head -c 9000000 /dev/zero | tr '\\0' ' '")
        assertUnavailable(client(bin, timeout: 20).find(in: bank), containing: "larger")
    }

    func testFewerThanTwoWordingsNeedNoCall() {
        XCTAssertEqual(client(URL(fileURLWithPath: "/nonexistent/vani")).find(in: .empty), .available([], model: nil))
    }
}
