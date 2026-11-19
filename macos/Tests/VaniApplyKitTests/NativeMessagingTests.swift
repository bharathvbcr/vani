import Foundation
import XCTest
@testable import VaniApplyKit

final class NativeMessagingTests: XCTestCase {
    func handle(_ bytes: Data) throws -> FileHandle {
        let url = try tempDir(self).appendingPathComponent("stream")
        try bytes.write(to: url)
        return try FileHandle(forReadingFrom: url)
    }

    func testFramesRoundTrip() throws {
        let a = Data(#"{"op":"ping"}"#.utf8), b = Data(#"{"op":"get"}"#.utf8)
        let h = try handle(NativeMessaging.frame(a) + NativeMessaging.frame(b))
        XCTAssertEqual(try NativeMessaging.readMessage(from: h), a)
        XCTAssertEqual(try NativeMessaging.readMessage(from: h), b)
        XCTAssertNil(try NativeMessaging.readMessage(from: h))
    }

    func testLengthIsLittleEndian() {
        XCTAssertEqual(Array(NativeMessaging.frame(Data(count: 258)).prefix(4)), [2, 1, 0, 0])
    }

    func testTruncatedInputIsAnError() throws {
        XCTAssertThrowsError(try NativeMessaging.readMessage(from: handle(Data([5, 0]))))
        XCTAssertThrowsError(try NativeMessaging.readMessage(from: handle(Data([5, 0, 0, 0, 0x7B]))))
    }

    func testOversizedRequestIsDrainedAndRefused() throws {
        let big = Data(repeating: 0x20, count: NativeMessaging.maxRequestBytes + 1)
        let next = Data(#"{"op":"ping"}"#.utf8)
        let h = try handle(NativeMessaging.frame(big) + NativeMessaging.frame(next))
        XCTAssertThrowsError(try NativeMessaging.readMessage(from: h)) { err in
            XCTAssertEqual(err as? NativeMessaging.ReadError, .tooLarge(big.count))
        }
        XCTAssertEqual(try NativeMessaging.readMessage(from: h), next, "the stream stays in step")
    }

    private func errorText(_ reply: Data) throws -> String {
        let body = try JSONSerialization.jsonObject(with: reply.dropFirst(4)) as? [String: Any]
        return try XCTUnwrap(body?["error"] as? String)
    }

    func testOversizedRepliesNameWhatWasTooLargeAndGiveAdviceThatFitsTheRequest() throws {
        let sentinel = "SENTINEL-reply-content"
        let blob = sentinel + String(repeating: "x", count: NativeMessaging.maxReplyBytes)
        let answers = NativeMessaging.encodeReply(["answers": blob], op: "get")
        let resume = NativeMessaging.encodeReply(["data": blob], op: "resumeGet")
        for reply in [answers, resume] {
            XCTAssertLessThan(reply.count - 4, NativeMessaging.maxReplyBytes, "the frame stays under Chrome's limit")
            XCTAssertEqual(reply.prefix(4), NativeMessaging.frame(reply.dropFirst(4)).prefix(4), "the frame header is the body length")
        }
        let a = try errorText(answers), r = try errorText(resume)
        XCTAssertTrue(a.contains("saved answers") && a.contains("delete some answers"), a)
        XCTAssertTrue(r.contains("stored resume") && !r.contains("answers"), "a resume reply is not blamed on answers: \(r)")
        XCTAssertTrue(r.contains("use a smaller resume file"), r)
        XCTAssertTrue(a.contains("\(blob.utf8.count + #"{"answers":""}"#.utf8.count) bytes"), "it says how large: \(a)")
        for text in [a, r] { XCTAssertFalse(text.contains(sentinel), "no reply content is echoed") }
    }

    func testOversizedReplyWithoutAKnownRequestGetsNeutralAdvice() throws {
        let blob = String(repeating: "x", count: NativeMessaging.maxReplyBytes)
        let none = try errorText(NativeMessaging.encodeReply(["blob": blob]))
        XCTAssertTrue(none.hasPrefix("the reply is "), none)
        XCTAssertFalse(none.contains("answers"), "no advice about answers when the request is unknown: \(none)")
        XCTAssertTrue(none.contains("narrow the request"), none)
        // An op comes from the request: only a short plain identifier is echoed.
        for hostile in ["", String(repeating: "a", count: 33), "get\nnow", "a b", "résumé", "<script>", "sk-or-v1-secret-key-value"] {
            let text = try errorText(NativeMessaging.encodeReply(["blob": blob], op: hostile))
            XCTAssertEqual(text, none, "\(hostile.debugDescription) is not echoed")
        }
        let named = try errorText(NativeMessaging.encodeReply(["blob": blob], op: "somethingNew"))
        XCTAssertTrue(named.hasPrefix("the somethingNew reply is "), named)
        let jobs = try errorText(NativeMessaging.encodeReply(["blob": blob], op: "jobFind"))
        XCTAssertTrue(jobs.contains("job data") && !jobs.contains("answers"), jobs)
    }

    func testAReplyThatCannotBeEncodedIsNotCalledTooLarge() throws {
        let text = try errorText(NativeMessaging.encodeReply(["bad": Double.nan], op: "get"))
        XCTAssertEqual(text, "reply encoding failed")
    }

    func testAReplyJustUnderTheLimitPassesUnchanged() throws {
        let overhead = #"{"b":""}"#.utf8.count
        let ok = NativeMessaging.encodeReply(["b": String(repeating: "x", count: NativeMessaging.maxReplyBytes - overhead)], op: "get")
        XCTAssertEqual(ok.count - 4, NativeMessaging.maxReplyBytes)
        let over = NativeMessaging.encodeReply(["b": String(repeating: "x", count: NativeMessaging.maxReplyBytes - overhead + 1)], op: "get")
        XCTAssertLessThan(over.count, 1024)
        XCTAssertTrue(try errorText(over).contains("\(NativeMessaging.maxReplyBytes + 1) bytes"))
    }

    func testReplyOverChromesLimitBecomesAnError() throws {
        let reply = NativeMessaging.encodeReply(["blob": String(repeating: "x", count: NativeMessaging.maxReplyBytes)])
        XCTAssertLessThan(reply.count, 1024)
        let body = try JSONSerialization.jsonObject(with: reply.dropFirst(4)) as? [String: Any]
        XCTAssertNotNil(body?["error"])
    }
}
