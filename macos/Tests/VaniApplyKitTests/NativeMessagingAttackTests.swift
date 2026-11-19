import Foundation
import XCTest
@testable import VaniApplyKit

/// Attack tests for NativeMessaging.encodeReply / replyFrame: values JSONSerialization rejects must
/// become an error reply, never an Objective-C exception that kills the host.
final class NativeMessagingAttackTests: XCTestCase {
    private func errorText(_ reply: Data) throws -> String {
        let body = try JSONSerialization.jsonObject(with: reply.dropFirst(4)) as? [String: Any]
        return try XCTUnwrap(body?["error"] as? String)
    }

    private func assertFrameConsistent(_ reply: Data, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertGreaterThanOrEqual(reply.count, 4, file: file, line: line)
        let n = reply.prefix(4).enumerated().reduce(0) { $0 | Int($1.element) << (8 * $1.offset) }
        XCTAssertEqual(n, reply.count - 4, "the header is the little-endian body length", file: file, line: line)
        XCTAssertLessThanOrEqual(reply.count - 4, NativeMessaging.maxReplyBytes, file: file, line: line)
    }

    private struct Custom { let x = 1 }

    func testEveryValueJSONCannotHoldBecomesAnErrorReplyNotACrash() throws {
        let bad: [(String, [String: Any])] = [
            ("nested NaN in array", ["a": [1, [2, Double.nan]]]),
            ("nested infinity in dict", ["a": ["b": ["c": Double.infinity]]]),
            ("negative infinity", ["a": -Double.infinity]),
            ("Float NaN", ["a": Float.nan]),
            ("NSNumber NaN", ["a": NSNumber(value: Double.nan)]),
            ("NSDecimalNumber NaN", ["a": NSDecimalNumber.notANumber]),
            ("Date", ["a": Date()]),
            ("Data", ["a": Data([1, 2, 3])]),
            ("URL", ["a": URL(fileURLWithPath: "/tmp")]),
            ("custom struct", ["a": Custom()]),
            ("non-string nested key", ["a": [1: "x"] as [Int: String]]),
            ("Set", ["a": Set([1, 2])]),
            ("tuple-ish Any", ["a": (1, 2)]),
            ("NSObject", ["a": NSObject()]),
        ]
        for (name, reply) in bad {
            for op in [nil, "get", "resumeGet"] {
                let out = NativeMessaging.encodeReply(reply, op: op)
                assertFrameConsistent(out)
                XCTAssertEqual(try errorText(out), "reply encoding failed", "\(name) op=\(String(describing: op))")
            }
        }
    }

    func testValuesJSONHoldsStillPass() throws {
        let ok: [String: Any] = ["n": NSNull(), "b": true, "i": Int.max, "d": Double.greatestFiniteMagnitude, "neg0": -0.0,
                                 "s": "\u{0}\u{1F600}\u{2028}", "arr": [[], [:], [1, "a"]], "dec": NSDecimalNumber(string: "1.5")]
        let out = NativeMessaging.encodeReply(ok, op: "get")
        assertFrameConsistent(out)
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: out.dropFirst(4)) as? [String: Any])
        XCTAssertNil(body["error"])
        XCTAssertEqual(body["s"] as? String, "\u{0}\u{1F600}\u{2028}")
    }

    func testAnOversizedReplyWithMultiByteContentIsCountedInBytesNotCharacters() throws {
        // 3-byte characters: character count is under the limit, byte count is over.
        let n = NativeMessaging.maxReplyBytes / 3
        let over = NativeMessaging.encodeReply(["b": String(repeating: "\u{20AC}", count: n + 10)], op: "get")
        assertFrameConsistent(over)
        XCTAssertTrue(try errorText(over).contains("saved answers"))
        // Escaped slashes are not added (withoutEscapingSlashes): a slash string at the limit passes.
        let overhead = #"{"b":""}"#.utf8.count
        let ok = NativeMessaging.encodeReply(["b": String(repeating: "/", count: NativeMessaging.maxReplyBytes - overhead)], op: "get")
        XCTAssertEqual(ok.count - 4, NativeMessaging.maxReplyBytes)
    }

    func testTheErrorReplyIsAlwaysSmallEvenForAHugeOrHostileOp() throws {
        let blob = String(repeating: "x", count: NativeMessaging.maxReplyBytes)
        let hostile = [String(repeating: "a", count: 10_000_000), String(repeating: "\u{202E}", count: 1000), "get\u{0}", "job" + String(repeating: "z", count: 40), "jobé", "\u{FF47}\u{FF45}\u{FF54}"]
        for op in hostile {
            let out = NativeMessaging.encodeReply(["blob": blob], op: op)
            assertFrameConsistent(out)
            XCTAssertLessThan(out.count, 512)
            let t = try errorText(out)
            XCTAssertTrue(t.hasPrefix("the reply is ") || t.hasPrefix("the job data is "), t)
            XCTAssertFalse(t.contains(op.prefix(40)) && op.count > 32 && !t.hasPrefix("the job data"), "a hostile op is not echoed")
        }
        // 32 bytes exactly is echoed, 33 is not.
        let o32 = String(repeating: "q", count: 32)
        XCTAssertTrue(try errorText(NativeMessaging.encodeReply(["blob": blob], op: o32)).contains(o32))
        XCTAssertFalse(try errorText(NativeMessaging.encodeReply(["blob": blob], op: o32 + "q")).contains(o32))
    }

    func testReplyFrameSurvivesAHandlerReplyOfBadValuesAndHostileRequests() throws {
        let bad = NativeMessaging.replyFrame(forRequest: Data(#"{"op":"get"}"#.utf8)) { _ in ["x": Double.nan] }
        XCTAssertEqual(try errorText(bad), "reply encoding failed")
        // Fragments, deep nesting, duplicate keys, a BOM, invalid UTF-8, and a NUL-bearing op.
        let deep = String(repeating: "[", count: 100_000) + String(repeating: "]", count: 100_000)
        let requests: [Data] = [Data("7".utf8), Data("null".utf8), Data(deep.utf8), Data(#"{"op":"get","op":"resumeGet"}"#.utf8),
                                Data([0xEF, 0xBB, 0xBF] + Array(#"{"op":"get"}"#.utf8)), Data([0xFF, 0xFE, 0x00]),
                                Data(#"{"op":"get\u0000"}"#.utf8), Data(#"{"op":["get"]}"#.utf8)]
        for r in requests {
            let out = NativeMessaging.replyFrame(forRequest: r) { _ in ["blob": String(repeating: "x", count: NativeMessaging.maxReplyBytes)] }
            assertFrameConsistent(out)
            _ = try errorText(out)
        }
    }

    func testEncodeReplyIsSafeFromManyThreads() async throws {
        await withTaskGroup(of: Bool.self) { g in
            for i in 0..<64 {
                g.addTask {
                    let r: [String: Any] = i % 2 == 0 ? ["a": Double.nan] : ["a": i]
                    let out = NativeMessaging.encodeReply(r, op: "get")
                    return (try? JSONSerialization.jsonObject(with: out.dropFirst(4))) != nil
                }
            }
            for await ok in g { XCTAssertTrue(ok) }
        }
    }
}
